package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// expectEnv is an admin server whose requests carry the aiExpect stored in cur.
type expectEnv struct {
	s      *Server
	srv    *httptest.Server
	groups *memGroups
	c      *http.Client
	csrf   string
	cur    atomic.Pointer[aiExpect]
}

// newExpectEnv starts an admin server with catalogs, a campaign store, a numeric offer counter,
// the section b2 and a logged-in administrator client.
func newExpectEnv(t *testing.T) *expectEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := &expectEnv{groups: &memGroups{}}
	boards := NewJSONStore[config.Board](filepath.Join(dir, "boards.json"))
	if err := boards.Save(context.Background(), []config.Board{{ID: config.MainBoardID, Name: "Main"}, {ID: "b2", Name: "B2"}}); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, UsersFile: filepath.Join(dir, "users.json"),
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: env.groups, Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		Boards:    boards,
		Offers:    NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:  NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:   NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:  NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		SeqOffers: NewIDSeq(filepath.Join(dir, "offer_seq.json")),
	})
	if err != nil {
		t.Fatal(err)
	}
	env.s = s
	h := s.Handler()
	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e := env.cur.Load(); e != nil {
			r = r.WithContext(withAIExpect(r.Context(), e))
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(env.srv.Close)
	env.c = newClient(t)
	code, csrf := login(t, env.c, env.srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	env.csrf = csrf
	return env
}

// do runs one request as the client c with exp as the precondition and returns the status and
// the JSON body. It never calls t, so it is safe in a goroutine.
func (e *expectEnv) do(c *http.Client, csrf, method, path string, body any, exp *aiExpect) (int, map[string]any, error) {
	e.cur.Store(exp)
	defer e.cur.Store(nil)
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequest(method, e.srv.URL+path, &buf)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

// send runs one administrator request and fails the test on a transport error.
func (e *expectEnv) send(t *testing.T, method, path string, body any, exp *aiExpect) (int, map[string]any) {
	t.Helper()
	code, out, err := e.do(e.c, e.csrf, method, path, body, exp)
	if err != nil {
		t.Fatal(err)
	}
	return code, out
}

// wantCode fails unless the status is want and, when errCode is not empty, the "code" field is errCode.
func wantCode(t *testing.T, label string, status int, body map[string]any, want int, errCode string) {
	t.Helper()
	if status != want {
		t.Fatalf("%s: status %d, want %d (%v)", label, status, want, body)
	}
	if errCode != "" && body["code"] != errCode {
		t.Fatalf("%s: code %v, want %s", label, body["code"], errCode)
	}
}

func (e *expectEnv) offers(t *testing.T) []config.Offer {
	t.Helper()
	list, err := e.s.cfg.Offers.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// offerHashOf returns the recordHash of the stored offer, or "" when it is missing.
func (e *expectEnv) offerHashOf(id string) string {
	list, err := e.s.cfg.Offers.List(context.Background())
	if err != nil {
		return ""
	}
	for _, o := range list {
		if o.ID == id {
			return recordHash(o)
		}
	}
	return ""
}

func (e *expectEnv) offerHash(t *testing.T, id string) string {
	t.Helper()
	h := e.offerHashOf(id)
	if h == "" {
		t.Fatalf("offer %s missing", id)
	}
	return h
}

// groupHashOf returns the recordHash of the stored campaign, or "" when it is missing.
func (e *expectEnv) groupHashOf(id string) string {
	g, ok := findGroup(e.groups.list, id)
	if !ok {
		return ""
	}
	return recordHash(g)
}

func (e *expectEnv) groupHash(t *testing.T, id string) string {
	t.Helper()
	h := e.groupHashOf(id)
	if h == "" {
		t.Fatalf("campaign %s missing", id)
	}
	return h
}

func testOffer(id, name string) config.Offer {
	return config.Offer{ID: id, Name: name, URL: "https://offer.example/" + id, Status: true}
}

func testGroup(id, name string) config.Group {
	return config.Group{ID: id, Name: name, Status: true, Redirect: "stop", Streams: []config.Stream{}}
}

func TestRawHashCanonical(t *testing.T) {
	a := rawHash(json.RawMessage(`{"b":1,"a":{"y":[1,2],"x":"s"},"n":12.50}`))
	b := rawHash(json.RawMessage("{ \"n\": 12.50,\n \"a\": {\"x\":\"s\",\"y\":[1,2]}, \"b\":1 }"))
	if a == "" || a != b {
		t.Fatalf("field order changed the hash: %q vs %q", a, b)
	}
	if c := rawHash(json.RawMessage(`{"b":2,"a":{"y":[1,2],"x":"s"},"n":12.50}`)); c == a {
		t.Fatal("a changed value kept the hash")
	}
	if rawHash(json.RawMessage(`{bad`)) != "" {
		t.Fatal("invalid json must hash to an empty string")
	}
	o := testOffer("o1", "One")
	if recordHash(o) != rawHash(aiRaw(o)) || recordHash(o) == "" {
		t.Fatal("recordHash must be rawHash of the record JSON")
	}
}

func TestAIExpectContext(t *testing.T) {
	if aiExpectFrom(context.Background()) != nil {
		t.Fatal("empty context must carry no expectation")
	}
	e := &aiExpect{Hash: "h"}
	if got := aiExpectFrom(withAIExpect(context.Background(), e)); got != e {
		t.Fatalf("got %p, want %p", got, e)
	}
	ctx := context.Background()
	if withAIExpect(ctx, nil) != ctx {
		t.Fatal("a nil expectation must return the context unchanged")
	}
}

func TestAIExpectCheck(t *testing.T) {
	o := testOffer("o1", "One")
	h := recordHash(o)
	cases := []struct {
		name   string
		exp    *aiExpect
		exists bool
		want   error
	}{
		{"nil accepts", nil, true, nil},
		{"hash matches", &aiExpect{Hash: h}, true, nil},
		{"hash differs", &aiExpect{Hash: "x"}, true, errAIStale},
		{"hash without a record", &aiExpect{Hash: h}, false, errAIConflict},
		{"empty hash", &aiExpect{}, true, errAIConflict},
		{"empty hash without a record", &aiExpect{}, false, errAIConflict},
		{"absent and missing", &aiExpect{Absent: true}, false, nil},
		{"absent and present", &aiExpect{Absent: true}, true, errAIConflict},
		{"absent ignores the hash", &aiExpect{Absent: true, Hash: "x"}, false, nil},
	}
	for _, c := range cases {
		if got := c.exp.check(o, c.exists); got != c.want && !errors.Is(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAIExpectEmptyHashIsConflict(t *testing.T) {
	env := newExpectEnv(t)
	env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "One"), nil)
	env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "One"), nil)

	empty := func() *aiExpect { return &aiExpect{} }
	code, body := env.send(t, http.MethodPut, "/api/offers/o1", testOffer("o1", "Two"), empty())
	wantCode(t, "offer put", code, body, http.StatusConflict, aiErrConflict)
	code, body = env.send(t, http.MethodDelete, "/api/offers/o1", nil, empty())
	wantCode(t, "offer delete", code, body, http.StatusConflict, aiErrConflict)
	code, body = env.send(t, http.MethodPost, "/api/offers", testOffer("o2", "Two"), empty())
	wantCode(t, "offer create without Absent", code, body, http.StatusConflict, aiErrConflict)
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "Two"), empty())
	wantCode(t, "campaign put", code, body, http.StatusConflict, aiErrConflict)
	code, body = env.send(t, http.MethodDelete, "/api/groups/c1", nil, empty())
	wantCode(t, "campaign delete", code, body, http.StatusConflict, aiErrConflict)

	if got := env.offers(t); len(got) != 1 || got[0].Name != "One" {
		t.Fatalf("offers = %+v", got)
	}
	if g, _ := findGroup(env.groups.list, "c1"); g.Name != "One" || len(env.groups.list) != 1 {
		t.Fatalf("campaigns = %+v", env.groups.list)
	}
}

func TestAIExpectCatalogHash(t *testing.T) {
	env := newExpectEnv(t)
	code, body := env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "One"), nil)
	wantCode(t, "create", code, body, http.StatusCreated, "")
	h0 := env.offerHash(t, "o1")

	var after json.RawMessage
	code, body = env.send(t, http.MethodPut, "/api/offers/o1", testOffer("o1", "Two"), &aiExpect{Hash: h0, OnAfter: func(raw json.RawMessage) { after = raw }})
	wantCode(t, "put with matching hash", code, body, http.StatusOK, "")
	h1 := env.offerHash(t, "o1")
	if h1 == h0 || rawHash(after) != h1 {
		t.Fatalf("OnAfter raw hash %q, stored %q", rawHash(after), h1)
	}

	code, body = env.send(t, http.MethodPut, "/api/offers/o1", testOffer("o1", "Three"), &aiExpect{Hash: h0})
	wantCode(t, "put with stale hash", code, body, http.StatusConflict, aiErrStale)
	if got := env.offers(t)[0].Name; got != "Two" {
		t.Fatalf("stale put changed the record to %q", got)
	}

	code, body = env.send(t, http.MethodPut, "/api/offers/nope", testOffer("nope", "X"), &aiExpect{Hash: h0})
	wantCode(t, "put of a missing record with a hash", code, body, http.StatusConflict, aiErrConflict)
	code, body = env.send(t, http.MethodPut, "/api/offers/nope", testOffer("nope", "X"), nil)
	wantCode(t, "put of a missing record", code, body, http.StatusNotFound, "not_found")
}

func TestAIExpectCatalogAbsent(t *testing.T) {
	env := newExpectEnv(t)
	var created json.RawMessage
	code, body := env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "One"), &aiExpect{Absent: true, OnAfter: func(raw json.RawMessage) { created = raw }})
	wantCode(t, "create when absent", code, body, http.StatusCreated, "")
	if rawHash(created) != env.offerHash(t, "o1") {
		t.Fatal("OnAfter of create did not get the stored record")
	}
	code, body = env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "Dup"), &aiExpect{Absent: true})
	wantCode(t, "create over an existing id", code, body, http.StatusConflict, aiErrConflict)
	code, body = env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "Dup"), nil)
	wantCode(t, "plain duplicate", code, body, http.StatusConflict, "conflict")
	if got := env.offers(t); len(got) != 1 || got[0].Name != "One" {
		t.Fatalf("offers = %+v", got)
	}
}

func TestAIExpectCatalogDelete(t *testing.T) {
	env := newExpectEnv(t)
	env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "One"), nil)
	h := env.offerHash(t, "o1")

	code, body := env.send(t, http.MethodDelete, "/api/offers/o1", nil, &aiExpect{Hash: "stale"})
	wantCode(t, "delete with stale hash", code, body, http.StatusConflict, aiErrStale)
	if len(env.offers(t)) != 1 {
		t.Fatal("stale delete removed the record")
	}

	code, body = env.send(t, http.MethodDelete, "/api/offers/o1", nil, &aiExpect{Hash: h, OnBefore: func(json.RawMessage) error {
		return newAIError(http.StatusConflict, aiErrHasLinks, "used")
	}})
	wantCode(t, "OnBefore refusal", code, body, http.StatusConflict, aiErrHasLinks)
	code, body = env.send(t, http.MethodDelete, "/api/offers/o1", nil, &aiExpect{Hash: h, OnBefore: func(json.RawMessage) error {
		return errors.New("disk detail")
	}})
	wantCode(t, "OnBefore plain error", code, body, http.StatusInternalServerError, "internal")
	if msg, _ := body["error"].(string); msg == "" || msg == "disk detail" {
		t.Fatalf("an internal error text reached the client: %q", msg)
	}
	if len(env.offers(t)) != 1 {
		t.Fatal("an aborted delete removed the record")
	}

	var before config.Offer
	code, body = env.send(t, http.MethodDelete, "/api/offers/o1", nil, &aiExpect{Hash: h, OnBefore: func(raw json.RawMessage) error {
		return json.Unmarshal(raw, &before)
	}})
	wantCode(t, "delete", code, body, http.StatusOK, "")
	if before.Name != "One" || len(env.offers(t)) != 0 {
		t.Fatalf("before = %+v, offers = %+v", before, env.offers(t))
	}

	code, body = env.send(t, http.MethodDelete, "/api/offers/o1", nil, &aiExpect{Hash: h})
	wantCode(t, "delete of a removed record", code, body, http.StatusConflict, aiErrConflict)
}

func TestAIExpectCampaign(t *testing.T) {
	env := newExpectEnv(t)
	var created json.RawMessage
	code, body := env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "One"), &aiExpect{Absent: true, OnAfter: func(raw json.RawMessage) { created = raw }})
	wantCode(t, "create when absent", code, body, http.StatusOK, "")
	if rawHash(created) != env.groupHash(t, "c1") {
		t.Fatal("OnAfter of create did not get the stored campaign")
	}
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "Again"), &aiExpect{Absent: true})
	wantCode(t, "absent over an existing campaign", code, body, http.StatusConflict, aiErrConflict)
	code, body = env.send(t, http.MethodPut, "/api/groups/ghost", testGroup("ghost", "G"), &aiExpect{Hash: "x"})
	wantCode(t, "update of a missing campaign", code, body, http.StatusConflict, aiErrConflict)
	if _, ok := findGroup(env.groups.list, "ghost"); ok {
		t.Fatal("an update of a missing campaign created it")
	}

	h0 := env.groupHash(t, "c1")
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "Stale"), &aiExpect{Hash: "wrong"})
	wantCode(t, "stale hash", code, body, http.StatusConflict, aiErrStale)
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "Refused"), &aiExpect{Hash: h0, OnBefore: func(json.RawMessage) error {
		return newAIError(http.StatusConflict, aiErrHasLinks, "x")
	}})
	wantCode(t, "OnBefore refusal", code, body, http.StatusConflict, aiErrHasLinks)
	if g, _ := findGroup(env.groups.list, "c1"); g.Name != "One" {
		t.Fatalf("refused or stale writes changed the campaign: %q", g.Name)
	}

	var before, after json.RawMessage
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "Two"), &aiExpect{Hash: h0,
		OnBefore: func(raw json.RawMessage) error { before = raw; return nil },
		OnAfter:  func(raw json.RawMessage) { after = raw }})
	wantCode(t, "update", code, body, http.StatusOK, "")
	h1 := env.groupHash(t, "c1")
	if rawHash(before) != h0 {
		t.Fatalf("OnBefore got hash %q, want the stored %q", rawHash(before), h0)
	}
	if h1 == h0 || rawHash(after) != h1 {
		t.Fatalf("OnAfter raw hash %q, stored %q", rawHash(after), h1)
	}

	code, body = env.send(t, http.MethodDelete, "/api/groups/c1", nil, &aiExpect{Hash: h0})
	wantCode(t, "delete with stale hash", code, body, http.StatusConflict, aiErrStale)
	var deleted config.Group
	var afterCalled bool
	code, body = env.send(t, http.MethodDelete, "/api/groups/c1", nil, &aiExpect{Hash: h1,
		OnBefore: func(raw json.RawMessage) error { return json.Unmarshal(raw, &deleted) },
		OnAfter:  func(json.RawMessage) { afterCalled = true }})
	wantCode(t, "delete", code, body, http.StatusOK, "")
	if deleted.Name != "Two" || len(env.groups.list) != 0 {
		t.Fatalf("before = %+v, campaigns = %+v", deleted, env.groups.list)
	}
	if afterCalled {
		t.Fatal("OnAfter must not run for a delete")
	}
	code, body = env.send(t, http.MethodDelete, "/api/groups/c1", nil, &aiExpect{Hash: h1})
	wantCode(t, "delete of a removed campaign", code, body, http.StatusConflict, aiErrConflict)
}

// TestAIExpectCampaignRefusedWriteSkipsOnBefore checks that OnBefore runs only after every
// right and validation check, right before the save.
func TestAIExpectCampaignRefusedWriteSkipsOnBefore(t *testing.T) {
	env := newExpectEnv(t)
	env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "One"), nil)
	h := env.groupHash(t, "c1")
	var calls int
	onBefore := func(json.RawMessage) error { calls++; return nil }

	bad := testGroup("c1", "Bad")
	bad.Streams = []config.Stream{{Name: "s", Status: true, Rules: config.Rules{SubIDs: map[string]config.SubIDFilter{"bogus": {Flag: 1}}}}}
	code, body := env.send(t, http.MethodPut, "/api/groups/c1", bad, &aiExpect{Hash: h, OnBefore: onBefore})
	wantCode(t, "invalid campaign", code, body, http.StatusBadRequest, "")
	linked := testGroup("c1", "Linked")
	linked.Streams = []config.Stream{{Name: "s", Status: true, Out: config.Output{Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "missing", Share: 100}}}}}
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", linked, &aiExpect{Hash: h, OnBefore: onBefore})
	wantCode(t, "unknown link", code, body, http.StatusBadRequest, "")

	reader := mustUser(t, env.s, "reader", "secret1", security.Access{
		Sections: userSectionsAll(),
		Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeRead}},
	})
	_ = reader
	rc := newClient(t)
	status, csrf := login(t, rc, env.srv.URL, "reader", "secret1")
	if status != http.StatusOK {
		t.Fatalf("reader login %d", status)
	}
	code, out, err := env.do(rc, csrf, http.MethodPut, "/api/groups/c1", testGroup("c1", "Hijack"), &aiExpect{Hash: h, OnBefore: onBefore})
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusForbidden {
		t.Fatalf("read-only user: %d %v", code, out)
	}
	code, out, err = env.do(rc, csrf, http.MethodDelete, "/api/groups/c1", nil, &aiExpect{Hash: h, OnBefore: onBefore})
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusForbidden {
		t.Fatalf("read-only user delete: %d %v", code, out)
	}

	if calls != 0 {
		t.Fatalf("OnBefore ran %d times for refused writes", calls)
	}
	if g, _ := findGroup(env.groups.list, "c1"); g.Name != "One" {
		t.Fatalf("a refused write changed the campaign: %q", g.Name)
	}
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "Good"), &aiExpect{Hash: h, OnBefore: onBefore})
	wantCode(t, "valid update", code, body, http.StatusOK, "")
	if calls != 1 {
		t.Fatalf("OnBefore ran %d times for the accepted write, want 1", calls)
	}
}

func TestAIExpectOnAfterRunsUnderLock(t *testing.T) {
	env := newExpectEnv(t)
	var offerHeld, groupHeld bool
	env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "One"), nil)
	code, body := env.send(t, http.MethodPut, "/api/offers/o1", testOffer("o1", "Two"), &aiExpect{Hash: env.offerHash(t, "o1"), OnAfter: func(json.RawMessage) {
		if env.s.cfg.Offers.mu.TryLock() {
			env.s.cfg.Offers.mu.Unlock()
			return
		}
		offerHeld = true
	}})
	wantCode(t, "offer put", code, body, http.StatusOK, "")
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "One"), &aiExpect{Absent: true, OnAfter: func(json.RawMessage) {
		if env.s.groupsMu.TryLock() {
			env.s.groupsMu.Unlock()
			return
		}
		groupHeld = true
	}})
	wantCode(t, "campaign put", code, body, http.StatusOK, "")
	if !offerHeld || !groupHeld {
		t.Fatalf("OnAfter ran without the write lock: offers %v, campaigns %v", offerHeld, groupHeld)
	}
}

func TestAIExpectWithoutExpectation(t *testing.T) {
	env := newExpectEnv(t)
	code, body := env.send(t, http.MethodDelete, "/api/offers/none", nil, nil)
	wantCode(t, "delete of a missing offer", code, body, http.StatusNotFound, "not_found")
	code, body = env.send(t, http.MethodDelete, "/api/groups/none", nil, nil)
	wantCode(t, "delete of a missing campaign", code, body, http.StatusNotFound, "not_found")
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "One"), nil)
	wantCode(t, "campaign upsert", code, body, http.StatusOK, "")
	code, body = env.send(t, http.MethodPut, "/api/groups/c1", testGroup("c1", "Two"), nil)
	wantCode(t, "campaign replace", code, body, http.StatusOK, "")
	code, body = env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "One"), nil)
	wantCode(t, "offer create", code, body, http.StatusCreated, "")
	code, body = env.send(t, http.MethodPut, "/api/offers/o1", testOffer("o1", "Two"), nil)
	wantCode(t, "offer replace", code, body, http.StatusOK, "")
	code, body = env.send(t, http.MethodDelete, "/api/offers/o1", nil, nil)
	wantCode(t, "offer delete", code, body, http.StatusOK, "")
	code, body = env.send(t, http.MethodDelete, "/api/groups/c1", nil, nil)
	wantCode(t, "campaign delete", code, body, http.StatusOK, "")
}

// TestAIExpectNoDeadlock runs every guarded write with callbacks that read the stores.
// The callbacks run under the handler's own lock, so a second lock taken by the gate itself
// would hang here instead of finishing within the limit. The goroutine never calls t;
// it reports one line per unexpected status, checked in the test goroutine.
func TestAIExpectNoDeadlock(t *testing.T) {
	env := newExpectEnv(t)
	read := func(json.RawMessage) error {
		_, err := env.s.cfg.Offers.List(context.Background())
		if err == nil {
			_, err = env.s.cfg.Groups.List(context.Background())
		}
		return err
	}
	readAfter := func(raw json.RawMessage) { _ = read(raw) }
	problems := make(chan []string, 1)
	go func() {
		var bad []string
		step := func(label string, want int, method, path string, body any, exp *aiExpect) {
			code, out, err := env.do(env.c, env.csrf, method, path, body, exp)
			if err != nil || code != want {
				bad = append(bad, fmt.Sprintf("%s: status %d, want %d, err %v, body %v", label, code, want, err, out))
			}
		}
		step("create offer", http.StatusCreated, http.MethodPost, "/api/offers", testOffer("o1", "One"), &aiExpect{Absent: true, OnAfter: readAfter})
		step("update offer", http.StatusOK, http.MethodPut, "/api/offers/o1", testOffer("o1", "Two"), &aiExpect{Hash: env.offerHashOf("o1"), OnBefore: read, OnAfter: readAfter})
		step("delete offer", http.StatusOK, http.MethodDelete, "/api/offers/o1", nil, &aiExpect{Hash: env.offerHashOf("o1"), OnBefore: read})
		step("create campaign", http.StatusOK, http.MethodPut, "/api/groups/c1", testGroup("c1", "One"), &aiExpect{Absent: true, OnAfter: readAfter})
		step("update campaign", http.StatusOK, http.MethodPut, "/api/groups/c1", testGroup("c1", "Two"), &aiExpect{Hash: env.groupHashOf("c1"), OnBefore: read, OnAfter: readAfter})
		step("delete campaign", http.StatusOK, http.MethodDelete, "/api/groups/c1", nil, &aiExpect{Hash: env.groupHashOf("c1"), OnBefore: read})
		problems <- bad
	}()
	select {
	case bad := <-problems:
		for _, line := range bad {
			t.Error(line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guarded writes did not finish in 5s: deadlock")
	}
	if len(env.offers(t)) != 0 || len(env.groups.list) != 0 {
		t.Fatalf("offers %+v, campaigns %+v", env.offers(t), env.groups.list)
	}
}

// boardCtx returns a context whose request section is board.
func boardCtx(board string) context.Context {
	return context.WithValue(context.Background(), boardKey, boardSession{current: board})
}

func TestRestoreCatalogKeepsOwnerBoardSeq(t *testing.T) {
	env := newExpectEnv(t)
	ctx := boardCtx("b2")
	offer := testOffer("7", "Seven")
	offer.CreatedBy = "u7"
	offer.BoardID = "b2"
	raw := aiRaw(offer)

	code, msg := env.s.restoreCatalogByRes(ctx, resOffers, raw)
	if code != http.StatusCreated {
		t.Fatalf("restore: %d %s", code, msg)
	}
	got := env.offers(t)
	if len(got) != 1 || got[0].ID != "7" || got[0].CreatedBy != "u7" || got[0].BoardID != "b2" || got[0].Name != "Seven" {
		t.Fatalf("restored offers = %+v", got)
	}
	if next, err := env.s.cfg.SeqOffers.read(); err != nil || next != 8 {
		t.Fatalf("counter = %d, %v; want 8", next, err)
	}
}

func TestRestoreCatalogConflictAndValidation(t *testing.T) {
	env := newExpectEnv(t)
	env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "Live"), nil)

	code, msg := env.s.restoreCatalogByRes(context.Background(), resOffers, aiRaw(testOffer("o1", "Old")))
	if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
		t.Fatalf("taken id: %d %q", code, msg)
	}
	if got := env.offers(t); len(got) != 1 || got[0].Name != "Live" {
		t.Fatalf("a conflicting restore changed the catalog: %+v", got)
	}
	if code, _ := env.s.restoreCatalogByRes(context.Background(), resOffers, json.RawMessage(`{bad`)); code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", code)
	}
	if code, _ := env.s.restoreCatalogByRes(context.Background(), resOffers, aiRaw(config.Offer{ID: "bad id", URL: "https://x/"})); code != http.StatusBadRequest {
		t.Fatalf("invalid record: %d", code)
	}
	if code, _ := env.s.restoreCatalogByRes(context.Background(), resDomains, aiRaw(testOffer("x", "X"))); code != http.StatusBadRequest {
		t.Fatalf("unknown resource: %d", code)
	}
}

func TestRestoreCatalogDeletedBoardIsConflict(t *testing.T) {
	env := newExpectEnv(t)
	offer := testOffer("o1", "One")
	offer.BoardID = "gone"
	code, msg := env.s.restoreCatalogByRes(boardCtx("gone"), resOffers, aiRaw(offer))
	if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
		t.Fatalf("deleted section: %d %q", code, msg)
	}
	if len(env.offers(t)) != 0 {
		t.Fatal("a refused restore saved the record")
	}
	offer.BoardID = "b2"
	if code, msg := env.s.restoreCatalogByRes(boardCtx("b2"), resOffers, aiRaw(offer)); code != http.StatusCreated {
		t.Fatalf("existing section: %d %q", code, msg)
	}
}

func TestRestoreCatalogMissingNetworkIsConflict(t *testing.T) {
	env := newExpectEnv(t)
	offer := testOffer("o1", "One")
	offer.NetworkID = "n1"
	code, msg := env.s.restoreCatalogByRes(context.Background(), resOffers, aiRaw(offer))
	if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
		t.Fatalf("missing network: %d %q", code, msg)
	}
	if len(env.offers(t)) != 0 {
		t.Fatal("a refused restore saved the record")
	}
	status, body := env.send(t, http.MethodPost, "/api/affiliate-networks", config.AffiliateNetwork{ID: "n1", Name: "N"}, nil)
	wantCode(t, "network create", status, body, http.StatusCreated, "")
	if code, msg := env.s.restoreCatalogByRes(context.Background(), resOffers, aiRaw(offer)); code != http.StatusCreated {
		t.Fatalf("restore with the network present: %d %q", code, msg)
	}
}

func TestRestoreCatalogPreCheckAborts(t *testing.T) {
	env := newExpectEnv(t)
	var seen config.Landing
	var calls int
	ctx := withAIExpect(context.Background(), &aiExpect{OnBefore: func(raw json.RawMessage) error {
		calls++
		if err := json.Unmarshal(raw, &seen); err != nil {
			return err
		}
		return newAIError(http.StatusConflict, aiErrConflict, "the landing directory is not empty")
	}})
	landing := config.Landing{ID: "l1", Name: "L", URL: "https://l.example/", Status: true}
	code, msg := env.s.restoreCatalogByRes(ctx, resLandings, aiRaw(landing))
	if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
		t.Fatalf("pre-check refusal: %d %q", code, msg)
	}
	if calls != 1 || seen.ID != "l1" {
		t.Fatalf("pre-check calls = %d, saw %+v", calls, seen)
	}
	list, err := env.s.cfg.Landings.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("an aborted restore saved %+v, %v", list, err)
	}

	offerCtx := withAIExpect(context.Background(), &aiExpect{OnBefore: func(json.RawMessage) error {
		return newAIError(http.StatusConflict, aiErrConflict, "refused")
	}})
	if code, _ := env.s.restoreCatalogByRes(offerCtx, resOffers, aiRaw(testOffer("7", "Seven"))); code != http.StatusConflict {
		t.Fatalf("offer pre-check: %d", code)
	}
	if next, err := env.s.cfg.SeqOffers.read(); err != nil || next != 0 {
		t.Fatalf("the counter moved to %d (%v) although nothing was saved", next, err)
	}
}

func TestRestoreCatalogOnAfterAndRights(t *testing.T) {
	env := newExpectEnv(t)
	var after json.RawMessage
	ctx := withAIExpect(context.Background(), &aiExpect{OnAfter: func(raw json.RawMessage) { after = raw }})
	if code, msg := env.s.restoreCatalogByRes(ctx, resOffers, aiRaw(testOffer("o1", "One"))); code != http.StatusCreated {
		t.Fatalf("restore: %d %s", code, msg)
	}
	if rawHash(after) != env.offerHash(t, "o1") {
		t.Fatal("OnAfter of restore did not get the stored record")
	}

	reader := mustUser(t, env.s, "reader", "secret1", security.Access{
		Sections: userSectionsAll(),
		Grants:   map[string]security.Grant{resOffers: {Mode: security.ModeRead}},
	})
	readCtx := context.WithValue(context.Background(), sessionKey, security.Session{UID: reader.ID})
	if code, _ := env.s.restoreCatalogByRes(readCtx, resOffers, aiRaw(testOffer("o2", "Two"))); code != http.StatusForbidden {
		t.Fatalf("read-only user: %d, want 403", code)
	}
	writer := mustUser(t, env.s, "writer", "secret2", security.Access{
		Sections: userSectionsAll(),
		Grants:   map[string]security.Grant{resOffers: {Mode: security.ModeFull}},
	})
	writeCtx := context.WithValue(context.Background(), sessionKey, security.Session{UID: writer.ID})
	foreign := testOffer("o3", "Three")
	foreign.CreatedBy = "someone"
	if code, msg := env.s.restoreCatalogByRes(writeCtx, resOffers, aiRaw(foreign)); code != http.StatusCreated {
		t.Fatalf("full-access user: %d %s", code, msg)
	}
	for _, o := range env.offers(t) {
		if o.ID == "o3" && o.CreatedBy != "someone" {
			t.Fatalf("owner replaced by %q", o.CreatedBy)
		}
	}
}

func TestRestoreGroupKeepsOwnerBoardNum(t *testing.T) {
	env := newExpectEnv(t)
	env.groups.list = []config.Group{{ID: "live", Num: 1, Status: true, Streams: []config.Stream{}}}
	g := testGroup("gone", "Gone")
	g.Num = 5
	g.CreatedBy = "u9"
	g.BoardID = "b2"
	got, renumbered, code, msg := env.s.restoreGroup(boardCtx("b2"), aiRaw(g))
	if code != http.StatusOK {
		t.Fatalf("restore: %d %s", code, msg)
	}
	if renumbered || got.Num != 5 || got.CreatedBy != "u9" || got.BoardID != "b2" {
		t.Fatalf("restored = %+v renumbered=%v", got, renumbered)
	}
	if stored, ok := findGroup(env.groups.list, "gone"); !ok || stored.CreatedBy != "u9" || stored.BoardID != "b2" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestRestoreGroupConflicts(t *testing.T) {
	env := newExpectEnv(t)
	live := testGroup("live", "Live")
	live.Aliases = []string{"short"}
	live.Num = 1
	env.groups.list = []config.Group{live}
	ctx := context.Background()

	cases := map[string]config.Group{
		"taken id":          testGroup("live", "Again"),
		"id used as alias":  testGroup("short", "Again"),
		"alias of other id": func() config.Group { g := testGroup("a", "A"); g.Aliases = []string{"live"}; return g }(),
		"alias of alias":    func() config.Group { g := testGroup("b", "B"); g.Aliases = []string{"short"}; return g }(),
	}
	for name, g := range cases {
		_, _, code, msg := env.s.restoreGroup(ctx, aiRaw(g))
		if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
			t.Fatalf("%s: %d %q", name, code, msg)
		}
	}
	if len(env.groups.list) != 1 {
		t.Fatalf("a conflicting restore changed the list: %+v", env.groups.list)
	}
}

func TestRestoreGroupRenumbersTakenNum(t *testing.T) {
	env := newExpectEnv(t)
	live := testGroup("live", "Live")
	live.Num = 3
	env.groups.list = []config.Group{live}
	g := testGroup("gone", "Gone")
	g.Num = 3
	got, renumbered, code, msg := env.s.restoreGroup(context.Background(), aiRaw(g))
	if code != http.StatusOK {
		t.Fatalf("restore: %d %s", code, msg)
	}
	if !renumbered || got.Num != 4 {
		t.Fatalf("num = %d renumbered=%v, want 4 true", got.Num, renumbered)
	}
	if first, _ := findGroup(env.groups.list, "live"); first.Num != 3 {
		t.Fatalf("the live campaign number changed to %d", first.Num)
	}
}

func TestRestoreGroupMissingLinkIsConflict(t *testing.T) {
	env := newExpectEnv(t)
	g := testGroup("gone", "Gone")
	g.Streams = []config.Stream{{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "deleted-offer", Share: 100}},
	}}}
	_, _, code, msg := env.s.restoreGroup(context.Background(), aiRaw(g))
	if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
		t.Fatalf("missing offer: %d %q", code, msg)
	}
	if len(env.groups.list) != 0 {
		t.Fatalf("a refused restore saved %+v", env.groups.list)
	}
	env.send(t, http.MethodPost, "/api/offers", testOffer("deleted-offer", "Back"), nil)
	if _, _, code, msg := env.s.restoreGroup(context.Background(), aiRaw(g)); code != http.StatusOK {
		t.Fatalf("restore with the offer present: %d %s", code, msg)
	}
}

func TestRestoreGroupSharesAndSubIDs(t *testing.T) {
	env := newExpectEnv(t)
	env.send(t, http.MethodPost, "/api/offers", testOffer("a", "A"), nil)
	env.send(t, http.MethodPost, "/api/offers", testOffer("b", "B"), nil)

	over := testGroup("over", "Over")
	over.Streams = []config.Stream{{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "a", Share: 60}, {ID: "b", Share: 60}},
	}}}
	_, _, code, msg := env.s.restoreGroup(context.Background(), aiRaw(over))
	if code != http.StatusBadRequest || !containsText(msg, errSharesExceed) {
		t.Fatalf("shares above 100: %d %q", code, msg)
	}

	badSub := testGroup("badsub", "BadSub")
	badSub.Streams = []config.Stream{{Name: "s", Status: true, Rules: config.Rules{SubIDs: map[string]config.SubIDFilter{"bogus": {Flag: 1}}}}}
	_, _, code, msg = env.s.restoreGroup(context.Background(), aiRaw(badSub))
	if code != http.StatusBadRequest || !containsText(msg, "unknown sub id rule") {
		t.Fatalf("bad Sub ID rule: %d %q", code, msg)
	}
	if len(env.groups.list) != 0 {
		t.Fatalf("a refused restore saved %+v", env.groups.list)
	}
}

func containsText(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

func TestRestoreGroupDeletedBoardIsConflict(t *testing.T) {
	env := newExpectEnv(t)
	g := testGroup("gone", "Gone")
	g.BoardID = "removed"
	_, _, code, msg := env.s.restoreGroup(boardCtx("removed"), aiRaw(g))
	if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
		t.Fatalf("deleted section: %d %q", code, msg)
	}
	if len(env.groups.list) != 0 {
		t.Fatalf("a refused restore saved %+v", env.groups.list)
	}
}

func TestRestoreGroupCallbacks(t *testing.T) {
	env := newExpectEnv(t)
	var before, after json.RawMessage
	ctx := withAIExpect(context.Background(), &aiExpect{
		OnBefore: func(raw json.RawMessage) error { before = raw; return nil },
		OnAfter:  func(raw json.RawMessage) { after = raw },
	})
	got, _, code, msg := env.s.restoreGroup(ctx, aiRaw(testGroup("gone", "Gone")))
	if code != http.StatusOK {
		t.Fatalf("restore: %d %s", code, msg)
	}
	if len(before) == 0 || rawHash(after) != recordHash(got) || rawHash(after) != env.groupHash(t, "gone") {
		t.Fatalf("callbacks: before %s, after hash %q, stored %q", before, rawHash(after), env.groupHash(t, "gone"))
	}

	refuse := withAIExpect(context.Background(), &aiExpect{OnBefore: func(json.RawMessage) error {
		return newAIError(http.StatusConflict, aiErrConflict, "refused")
	}})
	_, _, code, msg = env.s.restoreGroup(refuse, aiRaw(testGroup("other", "Other")))
	if code != http.StatusConflict || aiCodeOf(msg) != aiErrConflict {
		t.Fatalf("pre-check refusal: %d %q", code, msg)
	}
	if _, ok := findGroup(env.groups.list, "other"); ok {
		t.Fatal("an aborted restore saved the campaign")
	}
}

func TestRestoreGroupRights(t *testing.T) {
	env := newExpectEnv(t)
	reader := mustUser(t, env.s, "reader", "secret1", security.Access{
		Sections: userSectionsAll(),
		Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeRead}},
	})
	ctx := context.WithValue(context.Background(), sessionKey, security.Session{UID: reader.ID})
	if _, _, code, _ := env.s.restoreGroup(ctx, aiRaw(testGroup("gone", "Gone"))); code != http.StatusForbidden {
		t.Fatalf("read-only user: %d, want 403", code)
	}
	if _, _, code, _ := env.s.restoreGroup(boardCtx("other"), aiRaw(func() config.Group { g := testGroup("x", "X"); g.BoardID = "b9"; return g }())); code != http.StatusForbidden {
		t.Fatalf("foreign section: %d, want 403", code)
	}
	if _, _, code, _ := env.s.restoreGroup(context.Background(), json.RawMessage(`{"name":"no id"}`)); code != http.StatusBadRequest {
		t.Fatalf("missing id: %d, want 400", code)
	}
}

// TestRestoreNoDeadlock restores a catalog record and a campaign, then deletes the record
// through the guarded handler. The restores take their own locks and must not hang.
// The goroutine never calls t; it reports the statuses it saw.
func TestRestoreNoDeadlock(t *testing.T) {
	env := newExpectEnv(t)
	env.send(t, http.MethodPost, "/api/offers", testOffer("o1", "One"), nil)
	type result struct{ offer, group, del int }
	done := make(chan result, 1)
	go func() {
		var r result
		r.offer, _ = env.s.restoreCatalogByRes(context.Background(), resOffers, aiRaw(testOffer("o2", "Two")))
		_, _, r.group, _ = env.s.restoreGroup(context.Background(), aiRaw(testGroup("c1", "One")))
		r.del, _, _ = env.do(env.c, env.csrf, http.MethodDelete, "/api/offers/o2", nil, &aiExpect{Hash: env.offerHashOf("o2")})
		done <- r
	}()
	select {
	case r := <-done:
		if r.offer != http.StatusCreated || r.group != http.StatusOK || r.del != http.StatusOK {
			t.Fatalf("statuses: restore offer %d, restore campaign %d, delete %d", r.offer, r.group, r.del)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not finish in 5s: deadlock")
	}
}

func TestKeitaroWriteGate(t *testing.T) {
	j := newKeitaroJob()
	if j.busy() {
		t.Fatal("an idle job is busy")
	}
	if err := j.holdWrite(); err != nil {
		t.Fatalf("hold on an idle job: %v", err)
	}
	if err := j.holdWrite(); err != nil {
		t.Fatalf("second hold: %v", err)
	}
	if j.busy() {
		t.Fatal("a write hold must not count as a running job")
	}
	for name, begin := range map[string]func() string{"import": j.beginImport, "purge": j.beginPurge, "sync": j.beginSync} {
		msg := begin()
		if msg != keitaroWriteBusyMsg || aiCodeOf(msg) != aiErrKeitaroRunning || errCode(http.StatusConflict, msg) != aiErrKeitaroRunning {
			t.Fatalf("%s during a hold: %q", name, msg)
		}
	}
	j.releaseWrite()
	if msg := j.beginImport(); msg != keitaroWriteBusyMsg {
		t.Fatalf("one hold is left, import = %q", msg)
	}
	j.releaseWrite()
	j.releaseWrite()
	if msg := j.beginImport(); msg != "" {
		t.Fatalf("import after the holds are released: %q", msg)
	}
	if !j.busy() {
		t.Fatal("a running import is not busy")
	}
	if err := j.holdWrite(); !errors.Is(err, errKeitaroRunning) {
		t.Fatalf("hold during an import: %v", err)
	}
	j.finish("", nil, nil, "")

	if msg := j.beginPurge(); msg != "" {
		t.Fatal(msg)
	}
	if err := j.holdWrite(); !errors.Is(err, errKeitaroRunning) {
		t.Fatalf("hold during a purge: %v", err)
	}
	j.endPurge()

	if msg := j.beginSync(); msg != "" {
		t.Fatal(msg)
	}
	if err := j.holdWrite(); !errors.Is(err, errKeitaroRunning) {
		t.Fatalf("hold during a sync: %v", err)
	}
	j.finishSync()
	if j.busy() {
		t.Fatal("job still busy after every stage ended")
	}
	if err := j.holdWrite(); err != nil {
		t.Fatalf("hold after the job ended: %v", err)
	}
	j.releaseWrite()
}

func TestKeitaroWriteGateWithWrite(t *testing.T) {
	j := newKeitaroJob()
	var inside string
	if err := j.withWrite(func() error {
		inside = j.beginImport()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if inside != keitaroWriteBusyMsg {
		t.Fatalf("import inside withWrite: %q", inside)
	}
	if msg := j.beginImport(); msg != "" {
		t.Fatalf("import after withWrite: %q", msg)
	}
	var called bool
	err := j.withWrite(func() error { called = true; return nil })
	if !errors.Is(err, errKeitaroRunning) || called {
		t.Fatalf("withWrite during an import: err %v, called %v", err, called)
	}
	j.finish("", nil, nil, "")

	boom := errors.New("boom")
	if err := j.withWrite(func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("error of fn: %v", err)
	}
	if msg := j.beginPurge(); msg != "" {
		t.Fatalf("hold left after an error: %q", msg)
	}
	j.endPurge()
}

func TestKeitaroWriteGatePanicReleasesHold(t *testing.T) {
	j := newKeitaroJob()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate")
			}
		}()
		_ = j.withWrite(func() error { panic("fn failed") })
	}()
	j.mu.Lock()
	left := j.writers
	j.mu.Unlock()
	if left != 0 {
		t.Fatalf("holds left after a panic: %d", left)
	}
	if msg := j.beginImport(); msg != "" {
		t.Fatalf("import after a panic inside withWrite: %q", msg)
	}
}

func TestKeitaroWriteGateConcurrent(t *testing.T) {
	j := newKeitaroJob()
	var wg sync.WaitGroup
	var started, held atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if msg := j.beginImport(); msg == "" {
				started.Add(1)
				return
			}
			if err := j.holdWrite(); err == nil {
				held.Add(1)
			}
		}()
	}
	wg.Wait()
	if started.Load() != 1 {
		t.Fatalf("imports started = %d, want 1", started.Load())
	}
	if held.Load() != 0 {
		t.Fatalf("holds taken during a running import = %d", held.Load())
	}
}

func TestAIErrorAnswer(t *testing.T) {
	env := newExpectEnv(t)
	rec := httptest.NewRecorder()
	env.s.failWrite(rec, errAIStale)
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || body["code"] != aiErrStale {
		t.Fatalf("answer %d %v", rec.Code, body)
	}
	custom := newAIError(http.StatusConflict, aiErrHasLinks, "used by 2 campaigns")
	if errCode(http.StatusConflict, custom.Error()) != aiErrHasLinks {
		t.Fatalf("errCode lost the code of %q", custom.Error())
	}
	if aiCodeOf(errKeitaroRunning.Error()) != aiErrKeitaroRunning || aiCodeOf("already exists") != "" || aiCodeOf("ai_stale") != "" {
		t.Fatal("aiCodeOf does not recover the code from the message")
	}
	if errCode(http.StatusConflict, "already exists") != "conflict" {
		t.Fatal("a plain conflict must keep the conflict code")
	}
	rec = httptest.NewRecorder()
	env.s.failWrite(rec, errors.New("secret detail"))
	if rec.Code != http.StatusInternalServerError || bytes.Contains(rec.Body.Bytes(), []byte("secret detail")) {
		t.Fatalf("internal error answer %d %s", rec.Code, rec.Body.String())
	}
}
