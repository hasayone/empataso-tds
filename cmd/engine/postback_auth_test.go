package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/server"
	"github.com/hasayone/empataso-tds/internal/store"
)

// authDeps builds a minimal engineDeps for auth tests. cs replaces the store
// when non-nil. opts are applied after the defaults.
func authDeps(auth postbackAuth, cs *fakeConv, opts ...func(*engineDeps)) *engineDeps {
	d := &engineDeps{log: discardLog(), pbAuth: auth}
	if cs != nil {
		d.convOverride = cs
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// rootHandler wraps d.root with a realIP middleware that trusts 127.0.0.1.
// The mux includes both /postback and / to match the production route table.
func rootHandler(t *testing.T, d *engineDeps) http.Handler {
	t.Helper()
	realIP, err := server.NewRealIP([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		t.Fatalf("realip: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/postback", d.postback)
	mux.HandleFunc("/", d.root)
	return realIP.Middleware(mux)
}

// doRoot issues a GET request to target on h, with the XFF header set to ip.
func doRoot(h http.Handler, target, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:1"
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestPostbackAuthNoSecret verifies that an empty secret in compat mode does
// not change any behaviour: /postback and /{seg}/postback both proceed as before.
func TestPostbackAuthNoSecret(t *testing.T) {
	auth := postbackAuth{secret: "", mode: "compat"}

	// /postback with no secret → proceeds to store check → 503 no store.
	d := authDeps(auth, nil)
	rec := sendPostback(d, "/postback?subid=abc&status=lead&payout=1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/postback no secret: want 503, got %d", rec.Code)
	}

	// /abc/postback with no secret → falls through to group routing → trash 200.
	h := testEnv(t, group(), func(dep *engineDeps) { dep.pbAuth = auth })
	rec = do(t, h, "/abc/postback?subid=xyz", "8.8.8.8", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("/abc/postback no secret: want 200 (trash), got %d", rec.Code)
	}
}

// TestPostbackAuthCompatNoKey verifies that in compat mode, a missing key
// still lets the postback through (unchanged behaviour).
func TestPostbackAuthCompatNoKey(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "compat"}
	d := authDeps(auth, nil)

	// No store → 503, but we got past the auth check.
	rec := sendPostback(d, "/postback?subid=abc&status=lead&payout=1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("compat no key: want 503 (past auth), got %d", rec.Code)
	}
	if got := d.stats.postbacks.snapshot(); got.BadKey != 0 {
		t.Errorf("compat no key: bad_key count want 0, got %d", got.BadKey)
	}
}

// TestPostbackAuthCompatWrongKey verifies that in compat mode, a wrong key
// returns 403 and counts bad_key in metrics.
func TestPostbackAuthCompatWrongKey(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "compat"}
	d := authDeps(auth, nil)

	rec := sendPostback(d, "/postback?subid=abc&key=WRONGKEY&status=lead&payout=1")
	if rec.Code != http.StatusForbidden {
		t.Errorf("compat wrong key: want 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"bad key"`) {
		t.Errorf("compat wrong key: want bad key in body, got %s", rec.Body.String())
	}
	if got := d.stats.postbacks.snapshot(); got.BadKey != 1 {
		t.Errorf("compat wrong key: bad_key want 1, got %d", got.BadKey)
	}
}

// TestPostbackAuthRequireNoKey verifies that require mode rejects a request
// without a key with 403.
func TestPostbackAuthRequireNoKey(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "require"}
	d := authDeps(auth, nil)

	rec := sendPostback(d, "/postback?subid=abc&status=lead&payout=1")
	if rec.Code != http.StatusForbidden {
		t.Errorf("require no key: want 403, got %d", rec.Code)
	}
	if got := d.stats.postbacks.snapshot(); got.BadKey != 1 {
		t.Errorf("require no key: bad_key want 1, got %d", got.BadKey)
	}
}

// TestPostbackAuthRequireCorrectKey verifies that require mode allows a
// request with the correct key and processes it normally.
func TestPostbackAuthRequireCorrectKey(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "require"}
	d := authDeps(auth, nil)

	// Correct key, no store → 503 (past auth).
	rec := sendPostback(d, "/postback?subid=abc&key=mysecret&status=lead&payout=1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("require correct key: want 503 (past auth), got %d", rec.Code)
	}
	if got := d.stats.postbacks.snapshot(); got.BadKey != 0 {
		t.Errorf("require correct key: bad_key want 0, got %d", got.BadKey)
	}
}

// TestPostbackAuthPOSTFormKey verifies that the key can be supplied via a POST
// form body, not only as a query parameter.
func TestPostbackAuthPOSTFormKey(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "require"}
	d := authDeps(auth, nil)

	body := url.Values{"key": {"mysecret"}, "subid": {"abc"}, "status": {"lead"}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/postback", io.NopCloser(strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	d.postback(rec, req)

	// Correct key via form → past auth → 503 no store.
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST form key: want 503, got %d", rec.Code)
	}
	if got := d.stats.postbacks.snapshot(); got.BadKey != 0 {
		t.Errorf("POST form key: bad_key want 0, got %d", got.BadKey)
	}
}

// TestPostbackAuthKeitaroPath verifies that /{secret}/postback with the correct
// key dispatches to the postback handler and processes the request normally.
func TestPostbackAuthKeitaroPath(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "require"}
	h := testEnv(t, group(), func(d *engineDeps) { d.pbAuth = auth })

	rec := do(t, h, "/mysecret/postback?subid=abc&status=lead&payout=1", "8.8.8.8", nil)
	// No store → 503 (past auth, dispatched to handlePostback).
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("keitaro path correct key: want 503 (past auth), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Errorf("keitaro path correct key: want JSON error body, got %s", rec.Body.String())
	}
}

// TestPostbackAuthKeitaroWrongKeyFallsToRoot verifies that /{wrong}/postback
// with a wrong key does NOT dispatch to the postback handler: it falls through
// to normal campaign routing (trash 200 when no group matches).
func TestPostbackAuthKeitaroWrongKeyFallsToRoot(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "require"}
	h := testEnv(t, group(), func(d *engineDeps) { d.pbAuth = auth })

	rec := do(t, h, "/WRONGKEY/postback?subid=abc&status=lead", "8.8.8.8", nil)
	// Falls through to root → no group "WRONGKEY" → trash 200.
	if rec.Code != http.StatusOK {
		t.Errorf("keitaro wrong key: want 200 (trash), got %d", rec.Code)
	}
}

// TestPostbackAuthRequireEmptySecret verifies that require mode with no secret
// returns 403 "postback secret not set" for every postback.
func TestPostbackAuthRequireEmptySecret(t *testing.T) {
	auth := postbackAuth{secret: "", mode: "require"}
	d := authDeps(auth, nil)

	rec := sendPostback(d, "/postback?subid=abc&status=lead")
	if rec.Code != http.StatusForbidden {
		t.Errorf("require empty secret: want 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "postback secret not set") {
		t.Errorf("require empty secret: want 'postback secret not set' in body, got %s", rec.Body.String())
	}
	if got := d.stats.postbacks.snapshot(); got.BadKey != 1 {
		t.Errorf("require empty secret: bad_key want 1, got %d", got.BadKey)
	}
}

// TestPostbackAuthUnknownMode verifies that an unrecognized EMTDS_POSTBACK_AUTH
// value is treated as "require" (a request without a key is rejected).
func TestPostbackAuthUnknownMode(t *testing.T) {
	// loadPostbackAuth sets mode="require" for unrecognized values.
	auth := postbackAuth{secret: "mysecret", mode: "require"}
	d := authDeps(auth, nil)

	rec := sendPostback(d, "/postback?subid=abc&status=lead")
	if rec.Code != http.StatusForbidden {
		t.Errorf("unknown mode (require): want 403, got %d", rec.Code)
	}
}

// TestPostbackAuthKeyNotInSpool verifies that the "key" parameter does not
// appear in the spooled postback record (CID, Subs) or the sub_id fields.
func TestPostbackAuthKeyNotInSpool(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "compat"}
	cs := &fakeConv{err: errors.New("boom")}
	d := queueEnv(t, cs)
	d.pbAuth = auth

	// Send with key=mysecret; the key must not end up as the sub id or CID.
	rec := sendPostback(d, "/postback?subid=abc&key=mysecret&status=lead&payout=1&sub_id_1=mysub")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (queued), got %d", rec.Code)
	}
	if len(d.pbq.q.List()) != 1 {
		t.Fatalf("want 1 queued segment, got %d", len(d.pbq.q.List()))
	}

	// Replay after the store recovers; check the stored input.
	cs.set(store.ConversionResult{GroupID: "g1"}, nil)
	drain(d)
	if cs.callCount() < 2 {
		t.Fatalf("want at least 2 store calls, got %d", cs.callCount())
	}
	last := cs.calls[len(cs.calls)-1]
	if last.CID != "abc" {
		t.Errorf("CID in spool: want 'abc', got %q", last.CID)
	}
	// The first call was the original (which failed), the replay is the second.
	// Verify the original call also had CID=abc.
	first := cs.calls[0]
	if first.CID != "abc" {
		t.Errorf("original CID: want 'abc', got %q", first.CID)
	}
}

// TestPostbackAuthBadKeyInJournal verifies that a rejected (bad_key) request is
// journaled with its reason and the key masked, and is not stored.
func TestPostbackAuthBadKeyInJournal(t *testing.T) {
	auth := postbackAuth{secret: "mysecret", mode: "require"}
	cs := &fakeConv{}
	d := queueEnv(t, cs)
	d.pbAuth = auth
	d.plog = newPostbackLog(d, d.pbq.q, discardLog())

	rec := sendPostback(d, "/postback?subid=abc&key=WRONG&status=lead&payout=1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if err := d.plog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cs.logged) != 1 || len(cs.logged[0]) != 1 {
		t.Fatalf("journal %+v, want one line", cs.logged)
	}
	row := cs.logged[0][0]
	if row.Outcome != store.PostbackOutcomeBadKey || row.Subid != "abc" || row.Method != http.MethodGet {
		t.Fatalf("line %+v", row)
	}
	if strings.Contains(row.Params, "WRONG") || !strings.Contains(row.Params, "key=%2A%2A%2A") {
		t.Fatalf("the key must be masked: %q", row.Params)
	}
	if cs.callCount() != 0 {
		t.Fatal("a rejected postback must not reach the store")
	}
}

// TestPostbackAuthMetricsSnapshot verifies that bad_key appears in the
// PostbackMetrics snapshot with the right count.
func TestPostbackAuthMetricsSnapshot(t *testing.T) {
	var s postbackStats
	s.note("bad_key")
	s.note("bad_key")
	m := s.snapshot()
	if m.BadKey != 2 {
		t.Errorf("snapshot BadKey: want 2, got %d", m.BadKey)
	}
	// Other counters must not be affected.
	if m.OK != 0 || m.NoSubID != 0 {
		t.Errorf("other counters affected: %+v", m)
	}
}

// TestPostbackAuthKeitaroPathWithRootMux verifies the Keitaro path via the
// full production route table (both /postback and / registered).
func TestPostbackAuthKeitaroPathWithRootMux(t *testing.T) {
	auth := postbackAuth{secret: "tok", mode: "compat"}
	d := authDeps(auth, nil, func(dep *engineDeps) {
		dep.groups = group()
		dep.trashMode = "0"
	})
	h := rootHandler(t, d)

	// /{secret}/postback → dispatched to handlePostback → 503 no store.
	rec := doRoot(h, "/tok/postback?subid=abc&status=lead", "8.8.8.8")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("keitaro root mux correct key: want 503, got %d", rec.Code)
	}

	// /postback → goes to d.postback directly (mux match) → compat no key → 503.
	rec = doRoot(h, "/postback?subid=abc&status=lead", "8.8.8.8")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/postback compat no key: want 503 (past auth), got %d", rec.Code)
	}
}
