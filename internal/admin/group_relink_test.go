package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/security"
)

const (
	relinkTrackerHost = "kt.example"
	// relinkTrackerURL is the address a migrated stream keeps: a campaign link on the old tracker.
	relinkTrackerURL = "https://kt.example/middleware?subid=[CID]&bundle_id={bundle_id}&country=[COUNTRY]"
)

// TestTrackerCampaignKey checks which destinations count as a plain campaign link on the old tracker.
func TestTrackerCampaignKey(t *testing.T) {
	cases := []struct{ raw, want string }{
		{relinkTrackerURL, "middleware"},
		{"http://KT.example/alloffers/", "alloffers"},
		{"https://kt.example/a?x=1#f", "a"},
		{"https://kt.example/", ""},
		{"https://kt.example", ""},
		{"https://kt.example/a/offer", ""},
		{"https://other.example/middleware", ""},
		{"https://kt.example.evil/middleware", ""},
		{"ftp://kt.example/middleware", ""},
		{"https://kt.example/a|||https://kt.example/b", ""},
		{"https://[DOMAIN]/a", ""},
		{"@page:landing", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := trackerCampaignKey(c.raw, relinkTrackerHost); got != c.want {
			t.Errorf("trackerCampaignKey(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	if got := trackerCampaignKey(relinkTrackerURL, ""); got != "" {
		t.Errorf("an empty tracker host must change nothing, got %q", got)
	}
}

// relinkFixture is a stored list with one campaign that has the link of the old tracker in a stream.
func relinkFixture() []config.Group {
	return []config.Group{
		{ID: "middleware", Name: "Middleware", Status: true, Aliases: []string{"mw"}, Redirect: "show_text", Out: "MW"},
		{ID: "orig", Name: "Orig", Status: true, DomainID: "7", Num: 5, Redirect: "http_redirect", Out: relinkTrackerURL,
			Streams: []config.Stream{
				{Name: "mid", Status: true, Out: config.Output{Redirect: "http_redirect", Out: relinkTrackerURL, Chance: 100}},
				{Name: "with offer", Status: true, Out: config.Output{Redirect: "http_redirect", Out: relinkTrackerURL, Offers: []config.ShareRef{{ID: "o1"}}}},
				{Name: "plain", Status: true, Out: config.Output{Redirect: "http_redirect", Out: "https://google.com/"}},
				{Name: "alias", Status: true, Out: config.Output{Redirect: "http_redirect", Out: "https://kt.example/mw"}},
				{Name: "unknown", Status: true, Out: config.Output{Redirect: "http_redirect", Out: "https://kt.example/blocked"}},
			}},
	}
}

// TestRelinkTrackerOuts checks which fields of a copy are moved: the default and streams without catalog
// references, only to a campaign that exists, never the stored row's own slice.
func TestRelinkTrackerOuts(t *testing.T) {
	stored := relinkFixture()
	cp := stored[1]
	cp.ID = "fresh"
	all := append(append([]config.Group(nil), stored...), cp)
	if !relinkTrackerOuts(viewer{admin: true}, &all[2], all, relinkTrackerHost) {
		t.Fatal("nothing moved")
	}
	got := all[2]
	if got.Redirect != "group" || got.Out != "middleware" {
		t.Errorf("default = %q %q, want group middleware", got.Redirect, got.Out)
	}
	want := []struct{ redirect, out string }{
		{"group", "middleware"},
		{"http_redirect", relinkTrackerURL}, // serves the offer, the out is a spare
		{"http_redirect", "https://google.com/"},
		{"group", "middleware"}, // key is an alias of the campaign
		{"http_redirect", "https://kt.example/blocked"},
	}
	for i, w := range want {
		if o := got.Streams[i].Out; o.Redirect != w.redirect || o.Out != w.out {
			t.Errorf("stream %q = %q %q, want %q %q", got.Streams[i].Name, o.Redirect, o.Out, w.redirect, w.out)
		}
	}
	if o := stored[1].Streams[0].Out; o.Redirect != "http_redirect" || o.Out != relinkTrackerURL {
		t.Errorf("the stored campaign was rewritten: %+v", o)
	}
}

// TestRelinkTrackerOutsTargetRules checks that a campaign is not a target when the caller cannot see it
// or it sits in another section, and that a campaign never links to itself.
func TestRelinkTrackerOutsTargetRules(t *testing.T) {
	mk := func(boardOfTarget string) []config.Group {
		return []config.Group{
			{ID: "middleware", Status: true, BoardID: boardOfTarget},
			{ID: "fresh", Status: true, Redirect: "http_redirect", Out: relinkTrackerURL},
		}
	}
	if all := mk("b2"); relinkTrackerOuts(viewer{admin: true}, &all[1], all, relinkTrackerHost) {
		t.Error("a campaign of another section was linked")
	}
	if all := mk(""); !relinkTrackerOuts(viewer{admin: true}, &all[1], all, relinkTrackerHost) {
		t.Error("a campaign of the same section was not linked")
	}
	self := []config.Group{{ID: "middleware", Status: true, Redirect: "http_redirect", Out: relinkTrackerURL}}
	if relinkTrackerOuts(viewer{admin: true}, &self[0], self, relinkTrackerHost) {
		t.Error("a campaign was linked to itself")
	}
}

// TestRelinkNewCampaignsOnlyNew checks that a campaign already stored keeps its destinations.
func TestRelinkNewCampaignsOnlyNew(t *testing.T) {
	stored := relinkFixture()
	groups := append([]config.Group(nil), stored...)
	if rows := relinkNewCampaigns(viewer{admin: true}, stored, groups, relinkTrackerHost); len(rows) != 0 {
		t.Fatalf("stored campaigns were changed: %v", groupIDs(rows))
	}
	if groups[1].Redirect != "http_redirect" || groups[1].Out != relinkTrackerURL {
		t.Fatalf("stored campaign rewritten: %+v", groups[1])
	}
}

// testEngine is the campaign set of an engine process: it loads the groups file the way the engine
// does and routes a click the way its handler does.
type testEngine struct {
	path    string
	live    *config.Groups
	reloads atomic.Int32
}

// reload swaps the live set for the file as it is now. It is what POST /internal/reload does.
func (e *testEngine) reload(t *testing.T) {
	t.Helper()
	fresh, err := config.LoadGroups(e.path)
	if err != nil {
		t.Fatal(err)
	}
	e.live.Replace(fresh)
	e.reloads.Add(1)
}

// click follows the link /<id> like the engine: first segment names the campaign, the first matching
// stream decides, a "group" destination hands the visitor on. It returns the final redirect type and
// out, or "trash" for an unknown or disabled campaign.
func (e *testEngine) click(id string) (string, string) {
	grp, ok := e.live.Get(id)
	for hop := 0; ; hop++ {
		if !ok || grp == nil || !grp.Status || hop > config.MaxGroupHops {
			return "trash", ""
		}
		redirect, out := grp.Redirect, grp.Out
		if s, sel := router.Select(grp, router.Visitor{IP: netip.MustParseAddr("203.0.113.5"), Query: url.Values{}}, router.Deps{}); sel {
			redirect, out = s.Out.Redirect, s.Out.Out
		}
		if redirect != "group" {
			return redirect, out
		}
		grp, ok = e.live.Get(out)
	}
}

// relinkServer starts an admin on a real groups file, tells testEngine about every save and
// configures the old tracker.
func relinkServer(t *testing.T, seed []config.Group) (*httptest.Server, *testEngine) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	domains := NewJSONStore[config.Domain](filepath.Join(dir, "domains.json"))
	var hosts []config.Domain
	for _, id := range []string{"7", "8", "9"} {
		hosts = append(hosts, config.Domain{ID: id, Host: "d" + id + ".example", Role: "links"})
	}
	if err := domains.Save(t.Context(), hosts); err != nil {
		t.Fatal(err)
	}
	fg := NewFileGroups(path)
	if err := fg.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	eng := &testEngine{path: path, live: config.NewGroups()}
	eng.reload(t)
	fg.OnSave(func() { eng.reload(t) })
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: fg, Domains: domains, Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		KeitaroBase: "https://" + relinkTrackerHost, KeitaroKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, eng
}

// TestCopyOfMigratedCampaignRoutesToNewLink saves a copy of a migrated campaign under a new id and
// domain, then renames it, and checks every next click: the new link opens the new campaign and
// reaches the campaign in this panel, not the old tracker; the replaced link opens nothing; the
// campaign the copy came from keeps its old destination.
func TestCopyOfMigratedCampaignRoutesToNewLink(t *testing.T) {
	seed := relinkFixture()
	seed[1].Streams = slices.Delete(seed[1].Streams, 1, 2) // the stream with an offer needs a catalog record
	srv, eng := relinkServer(t, seed)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	if redirect, out := eng.click("orig"); redirect != "http_redirect" || out != relinkTrackerURL {
		t.Fatalf("seed: click on orig = %q %q", redirect, out)
	}
	base := eng.reloads.Load()

	cp := seed[1]
	cp.ID, cp.Name, cp.DomainID, cp.Num = "fresh", "Orig (copy)", "8", 6
	status, raw, err := doJSON(c, http.MethodPut, srv.URL+"/api/groups/fresh", csrf, cp)
	if err != nil || status != http.StatusOK {
		t.Fatalf("save copy: %d %s %v", status, raw, err)
	}
	var reply struct {
		Status   string         `json:"status"`
		Relinked []config.Group `json:"relinked"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Status != "saved" || len(reply.Relinked) != 1 || reply.Relinked[0].ID != "fresh" {
		t.Fatalf("answer of the save: %s", raw)
	}
	if reply.Relinked[0].Streams[0].Out.Redirect != "group" {
		t.Fatalf("answer does not carry the stored stream: %s", raw)
	}
	if eng.reloads.Load() != base+1 {
		t.Fatalf("the engine was told %d times by the save, want 1", eng.reloads.Load()-base)
	}
	if redirect, out := eng.click("fresh"); redirect != "show_text" || out != "MW" {
		t.Errorf("click on the new link = %q %q, want the campaign middleware (show_text MW)", redirect, out)
	}
	if redirect, out := eng.click("orig"); redirect != "http_redirect" || out != relinkTrackerURL {
		t.Errorf("another campaign was rewritten: %q %q", redirect, out)
	}

	// The panel adopts the stored destinations from the answer, then edits id and domain.
	cp.Redirect, cp.Out, cp.Streams = reply.Relinked[0].Redirect, reply.Relinked[0].Out, reply.Relinked[0].Streams
	cp.ID, cp.DomainID = "fresher", "9"
	status, raw, err = doJSON(c, http.MethodPut, srv.URL+"/api/groups/fresh", csrf, cp)
	if err != nil || status != http.StatusOK {
		t.Fatalf("rename copy: %d %s %v", status, raw, err)
	}
	if redirect, out := eng.click("fresh"); redirect != "trash" {
		t.Errorf("the replaced link still opens %q %q", redirect, out)
	}
	if redirect, out := eng.click("fresher"); redirect != "show_text" || out != "MW" {
		t.Errorf("click on the renamed link = %q %q, want show_text MW", redirect, out)
	}
	got, ok := eng.live.Get("fresher")
	if !ok || got.DomainID != "9" {
		t.Errorf("the engine holds domain %q for the renamed campaign, want 9", got.DomainID)
	}
	if redirect, out := eng.click("orig"); redirect != "http_redirect" || out != relinkTrackerURL {
		t.Errorf("another campaign was rewritten by the rename: %q %q", redirect, out)
	}
}

// TestFileGroupsOnSave checks that the save hook runs after the file is in place and not after a failed save.
func TestFileGroupsOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "groups.json")
	fg := NewFileGroups(path)
	var seen [][]string
	fg.OnSave(func() {
		list, err := fg.List(t.Context())
		if err != nil {
			t.Error(err)
		}
		seen = append(seen, groupIDs(list))
	})
	if err := fg.Save(t.Context(), []config.Group{{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := fg.Save(t.Context(), []config.Group{{ID: "b"}}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !slices.Equal(seen[0], []string{"a"}) || !slices.Equal(seen[1], []string{"b"}) {
		t.Fatalf("hook saw %v, want the file after each save", seen)
	}
	bad := NewFileGroups(filepath.Join(t.TempDir(), "missing-dir", "groups.json"))
	called := false
	bad.OnSave(func() { called = true })
	if err := bad.Save(t.Context(), nil); err == nil || called {
		t.Fatalf("failed save: err=%v hook=%v", err, called)
	}
}

// TestEngineReloader checks the call a save makes: POST /internal/reload, and a dead or refusing
// engine never fails the caller.
func TestEngineReloader(t *testing.T) {
	var hits atomic.Int32
	status := http.StatusOK
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/internal/reload" {
			hits.Add(1)
		}
		w.WriteHeader(status)
	}))
	reload := EngineReloader(engine.URL+"/", nil)
	reload()
	if hits.Load() != 1 {
		t.Fatalf("engine got %d reloads, want 1", hits.Load())
	}
	status = http.StatusInternalServerError
	reload()
	engine.Close()
	reload()
	EngineReloader("", nil)()
}
