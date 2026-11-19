package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

func groupAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &memGroups{},
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func fetchGroups(t *testing.T, c *http.Client, base string) []config.Group {
	t.Helper()
	resp, err := c.Get(base + "/api/groups")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET groups %d %s", resp.StatusCode, b)
	}
	var got []config.Group
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func groupIDs(gs []config.Group) []string {
	ids := make([]string, len(gs))
	for i, g := range gs {
		ids[i] = g.ID
	}
	return ids
}

func groupByID(gs []config.Group, id string) (config.Group, bool) {
	for _, g := range gs {
		if g.ID == id {
			return g, true
		}
	}
	return config.Group{}, false
}

func TestGroupPutUpsert(t *testing.T) {
	srv := groupAPIServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
		{ID: "c", Name: "C"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save list %d", resp.StatusCode)
	}

	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/b", csrf, config.Group{ID: "b", Name: "B2"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put b %d", resp.StatusCode)
	}
	got := fetchGroups(t, c, srv.URL)
	if !slices.Equal(groupIDs(got), []string{"a", "b", "c"}) {
		t.Fatalf("ids after put b: %v", groupIDs(got))
	}
	b, ok := groupByID(got, "b")
	if !ok || b.Name != "B2" {
		t.Fatalf("b after put: %+v", b)
	}

	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/d", csrf, config.Group{Name: "D"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put d %d", resp.StatusCode)
	}
	got = fetchGroups(t, c, srv.URL)
	if !slices.Equal(groupIDs(got), []string{"a", "b", "c", "d"}) {
		t.Fatalf("ids after put d: %v", groupIDs(got))
	}
	d, ok := groupByID(got, "d")
	if !ok || d.Name != "D" {
		t.Fatalf("d after put: %+v", d)
	}

	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/b", csrf, config.Group{ID: "nope", Name: "X", Num: b.Num})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename b %d", resp.StatusCode)
	}
	got = fetchGroups(t, c, srv.URL)
	if !slices.Equal(groupIDs(got), []string{"a", "nope", "c", "d"}) {
		t.Fatalf("ids after rename: %v", groupIDs(got))
	}
}

// TestGroupPutRename checks PUT /api/groups/{old} with another id in the body: the campaign keeps its
// place, number and section under the new id, links of other campaigns follow it, and a taken, malformed
// or missing id is refused without touching the list.
func TestGroupPutRename(t *testing.T) {
	srv := groupAPIServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	hub := config.Group{ID: "hub", Name: "Hub", Status: true, Redirect: "group", Out: "old",
		Streams: []config.Stream{{Name: "s1", Status: true, Out: config.Output{Redirect: "group", Out: "old"}}}}
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{
		{ID: "old", Name: "Old", Status: true, Num: 7, Aliases: []string{"keep"}},
		hub,
		{ID: "other", Name: "Other", Status: true, Aliases: []string{"busy"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seed %d", resp.StatusCode)
	}

	refuse := func(path string, body config.Group, code int, msg string) {
		t.Helper()
		status, raw, err := doJSON(c, http.MethodPut, srv.URL+path, csrf, body)
		if err != nil {
			t.Fatal(err)
		}
		var er struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &er)
		if status != code || er.Error != msg {
			t.Fatalf("PUT %s as %q: %d %q, want %d %q", path, body.ID, status, er.Error, code, msg)
		}
	}
	refuse("/api/groups/old", config.Group{ID: "other", Name: "Old"}, http.StatusConflict, "campaign id is taken: other")
	refuse("/api/groups/old", config.Group{ID: "busy", Name: "Old"}, http.StatusConflict, "campaign id is taken: busy")
	refuse("/api/groups/old", config.Group{ID: "a/b", Name: "Old"}, http.StatusBadRequest, "campaign id must not contain spaces, /, ? or #")
	refuse("/api/groups/ghost", config.Group{ID: "fresh", Name: "Ghost"}, http.StatusNotFound, "not found")
	if got := fetchGroups(t, c, srv.URL); !slices.Equal(groupIDs(got), []string{"old", "hub", "other"}) {
		t.Fatalf("ids after refused renames: %v", groupIDs(got))
	}

	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/old", csrf,
		config.Group{ID: "fresh", Name: "Fresh", Status: true, Num: 7, Aliases: []string{"keep"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename %d", resp.StatusCode)
	}
	got := fetchGroups(t, c, srv.URL)
	if !slices.Equal(groupIDs(got), []string{"fresh", "hub", "other"}) {
		t.Fatalf("ids after rename: %v", groupIDs(got))
	}
	fresh, _ := groupByID(got, "fresh")
	if fresh.Name != "Fresh" || fresh.Num != 7 || !slices.Equal(fresh.Aliases, []string{"keep"}) {
		t.Fatalf("renamed campaign: %+v", fresh)
	}
	h, _ := groupByID(got, "hub")
	if h.Out != "fresh" || h.Streams[0].Out.Out != "fresh" {
		t.Fatalf("hub links: default %q, stream %q", h.Out, h.Streams[0].Out.Out)
	}
}

// TestRenameGroupKeepsStored checks that renameGroup leaves the stored list and its streams untouched,
// gives the save checks the old row under the new id and reports the campaigns whose links moved.
func TestRenameGroupKeepsStored(t *testing.T) {
	stored := []config.Group{
		{ID: "a", Name: "A", BoardID: "seo"},
		{ID: "b", Streams: []config.Stream{{Name: "s", Out: config.Output{Redirect: "group", Out: " a "}}}},
		{ID: "c", Redirect: "http_redirect", Out: "a"},
	}
	before, groups, moved := renameGroup(stored, "a", config.Group{ID: "z", Name: "Z"})
	if stored[0].ID != "a" || stored[1].Streams[0].Out.Out != " a " {
		t.Fatalf("stored changed: %+v", stored)
	}
	if before[0].ID != "z" || before[0].Name != "A" || before[0].BoardID != "seo" {
		t.Fatalf("baseline row: %+v", before[0])
	}
	if groups[0].ID != "z" || groups[0].Name != "Z" || groups[1].Streams[0].Out.Out != "z" || groups[2].Out != "a" {
		t.Fatalf("candidate: %+v", groups)
	}
	if !slices.Equal(moved, []int{1}) {
		t.Fatalf("moved %v, want [1]", moved)
	}
}

// TestGroupPutBodyTooLarge checks that a campaign body over maxGroupBody answers 413, not 400.
func TestGroupPutBodyTooLarge(t *testing.T) {
	srv := groupAPIServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	big := config.Group{ID: "a", Name: strings.Repeat("x", maxGroupBody)}
	status, _, err := doJSON(c, http.MethodPut, srv.URL+"/api/groups/a", csrf, big)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", status)
	}
}

func TestGroupPutAssignsNum(t *testing.T) {
	srv := groupAPIServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	put := func(id string, body config.Group) {
		t.Helper()
		resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/"+id, csrf, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("put %s %d", id, resp.StatusCode)
		}
	}

	put("a", config.Group{ID: "a", Name: "A"})
	got := fetchGroups(t, c, srv.URL)
	a, ok := groupByID(got, "a")
	if !ok || a.Num != 1 {
		t.Fatalf("a num: %+v ok=%v", a, ok)
	}

	put("b", config.Group{ID: "b", Name: "B"})
	got = fetchGroups(t, c, srv.URL)
	a, _ = groupByID(got, "a")
	b, ok := groupByID(got, "b")
	if a.Num != 1 || !ok || b.Num != 2 {
		t.Fatalf("after b: a=%+v b=%+v ok=%v", a, b, ok)
	}

	put("c", config.Group{ID: "c", Name: "C", Num: 10})
	got = fetchGroups(t, c, srv.URL)
	gc, ok := groupByID(got, "c")
	if !ok || gc.Num != 10 {
		t.Fatalf("c num: %+v ok=%v", gc, ok)
	}

	put("d", config.Group{ID: "d", Name: "D"})
	got = fetchGroups(t, c, srv.URL)
	d, ok := groupByID(got, "d")
	if !ok || d.Num != 11 {
		t.Fatalf("d num: %+v ok=%v", d, ok)
	}

	put("a", config.Group{ID: "a", Name: "A2", Num: 1})
	got = fetchGroups(t, c, srv.URL)
	a, ok = groupByID(got, "a")
	if !ok || a.Num != 1 || a.Name != "A2" {
		t.Fatalf("a after rename: %+v ok=%v", a, ok)
	}
}

func TestAssignGroupNums(t *testing.T) {
	assignGroupNums(nil)

	numbered := []config.Group{{ID: "a", Num: 2}, {ID: "b", Num: 2}, {ID: "c", Num: 5}}
	assignGroupNums(numbered)
	if numbered[0].Num != 2 || numbered[1].Num != 2 || numbered[2].Num != 5 {
		t.Fatalf("numbered: %+v", numbered)
	}

	mixed := []config.Group{
		{ID: "a"},
		{ID: "b", Num: 4},
		{ID: "c", Num: 4},
		{ID: "d", Num: -1},
	}
	assignGroupNums(mixed)
	if mixed[0].Num != 5 || mixed[1].Num != 4 || mixed[2].Num != 4 || mixed[3].Num != 6 {
		t.Fatalf("mixed: %+v", mixed)
	}
}

func TestGroupDelete(t *testing.T) {
	srv := groupAPIServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
		{ID: "c", Name: "C"},
		{ID: "d", Name: "D"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save list %d", resp.StatusCode)
	}

	resp = csrfDo(t, c, http.MethodDelete, srv.URL+"/api/groups/b", csrf, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete b %d", resp.StatusCode)
	}
	got := fetchGroups(t, c, srv.URL)
	if !slices.Equal(groupIDs(got), []string{"a", "c", "d"}) {
		t.Fatalf("ids after delete: %v", groupIDs(got))
	}

	resp = csrfDo(t, c, http.MethodDelete, srv.URL+"/api/groups/missing", csrf, nil)
	defer resp.Body.Close()
	var er struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound || er.Error != "not found" {
		t.Fatalf("missing delete %d %q", resp.StatusCode, er.Error)
	}
	got = fetchGroups(t, c, srv.URL)
	if !slices.Equal(groupIDs(got), []string{"a", "c", "d"}) {
		t.Fatalf("ids after missing delete: %v", groupIDs(got))
	}
}

func TestGroupPutConcurrent(t *testing.T) {
	srv := groupAPIServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
		{ID: "c", Name: "C"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save list %d", resp.StatusCode)
	}

	var wg sync.WaitGroup
	errc := make(chan error, 2)
	put := func(id, name string) {
		defer wg.Done()
		code, body, err := doJSON(c, http.MethodPut, srv.URL+"/api/groups/"+id, csrf, config.Group{ID: id, Name: name})
		if err != nil {
			errc <- err
			return
		}
		if code != http.StatusOK {
			errc <- fmt.Errorf("put %s: %d %s", id, code, body)
			return
		}
		errc <- nil
	}
	wg.Add(2)
	go put("a", "A2")
	go put("c", "C2")
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}

	got := fetchGroups(t, c, srv.URL)
	if !slices.Equal(groupIDs(got), []string{"a", "b", "c"}) {
		t.Fatalf("ids after concurrent put: %v", groupIDs(got))
	}
	a, _ := groupByID(got, "a")
	b, _ := groupByID(got, "b")
	gc, _ := groupByID(got, "c")
	if a.Name != "A2" || b.Name != "B" || gc.Name != "C2" {
		t.Fatalf("names a=%q b=%q c=%q", a.Name, b.Name, gc.Name)
	}
}

func doJSON(c *http.Client, method, url, csrf string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func TestReplaceGroupMergeCreated(t *testing.T) {
	stored := []config.Group{
		{ID: "a", Name: "A", CreatedBy: "u1"},
		{ID: "b", Name: "B", CreatedBy: "u2"},
		{ID: "c", Name: "C", CreatedBy: "u1"},
	}
	v := viewer{uid: "u1", grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeCreated}}}
	own := stored[0]
	own.Name = "A2"
	got, code, msg := mergeGroups(v, stored, replaceGroup(stored, own))
	if code != 0 || msg != "" {
		t.Fatalf("own replace: %d %s", code, msg)
	}
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	if got[0].ID != "a" || got[0].Name != "A2" || got[0].CreatedBy != "u1" {
		t.Fatalf("own %+v", got[0])
	}
	if got[1].ID != "b" || got[1].Name != "B" || got[1].CreatedBy != "u2" {
		t.Fatalf("foreign %+v", got[1])
	}
	if got[2].ID != "c" || got[2].Name != "C" || got[2].CreatedBy != "u1" {
		t.Fatalf("other own %+v", got[2])
	}
	if stored[0].Name != "A" || stored[1].Name != "B" || stored[2].Name != "C" {
		t.Fatal("stored mutated")
	}

	foreign := stored[1]
	foreign.Name = "hack"
	_, code, msg = mergeGroups(v, stored, replaceGroup(stored, foreign))
	if code != http.StatusForbidden || msg != "foreign campaign: b" {
		t.Fatalf("foreign replace: %d %s", code, msg)
	}

	full, ok := dropGroup(stored, "a")
	if !ok || len(full) != 2 || full[0].ID != "b" || full[1].ID != "c" {
		t.Fatalf("drop a: %v %+v", ok, full)
	}
	full[0].Name = "mut"
	if stored[0].Name != "A" || stored[1].Name != "B" {
		t.Fatal("drop aliases stored")
	}
	if _, ok := dropGroup(stored, "missing"); ok {
		t.Fatal("missing drop")
	}
}

func TestGroupPutWithNetworksStore(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	networks := NewJSONStore[config.AffiliateNetwork](filepath.Join(t.TempDir(), "networks.json"))
	if err := networks.Save(context.Background(), []config.AffiliateNetwork{{ID: "net1"}}); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &memGroups{},
		Networks:     networks,
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{{ID: "a", Name: "A"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put groups %d", resp.StatusCode)
	}
	if got := fetchGroups(t, c, srv.URL); !slices.Equal(groupIDs(got), []string{"a"}) {
		t.Fatalf("ids: %v", groupIDs(got))
	}
}
