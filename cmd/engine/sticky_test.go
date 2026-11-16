package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

// stickyGroup is a weighted group with two catch-all streams and sticky on.
func stickyGroup(method string, streams ...config.Stream) *config.Group {
	if len(streams) == 0 {
		streams = []config.Stream{weighted("a", 1), weighted("b", 1)}
	}
	return &config.Group{
		ID: "g1", Status: true, Redirect: "show_text", Out: "DEFAULT", Weight: true, StickyStream: true,
		UniqMethod: method, UniqSeconds: 3600, Streams: streams,
	}
}

func weighted(name string, weight int) config.Stream {
	return config.Stream{Name: name, Status: true, Weight: weight,
		Out: config.Output{Redirect: "show_text", Out: "OUT-" + name}}
}

func streamOf(rec interface{ Header() http.Header }) string {
	return rec.Header().Get("X-Emtds-Stream")
}

// stickyToken is the token the engine itself issues for a stream of group g1.
func stickyToken(stream string) string {
	return stickyMAC((*cidSigner)(nil).derive(stickyKeyLabel), "g1", stream)
}

// repeatStays asserts that n returning visits all land on the first visit's stream.
func repeatStays(t *testing.T, h http.Handler, j jar, path string, n int) string {
	t.Helper()
	first := streamOf(j.visit(t, h, path))
	if first == "" || first == "-" {
		t.Fatalf("first visit got no stream: %q", first)
	}
	for i := 0; i < n; i++ {
		if got := streamOf(j.visit(t, h, path)); got != first {
			t.Fatalf("visit %d went to %q, the first visit got %q", i+2, got, first)
		}
	}
	return first
}

func TestStickyCookieKeepsStream(t *testing.T) {
	h := testEnv(t, config.NewGroups(stickyGroup("cookie")))
	j := jar{}
	rec := j.visit(t, h, "/g1")
	c := j["zts_g1"]
	if c == nil {
		t.Fatal("no zts_g1 cookie after the first visit")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 3600 || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
	if c.Value != stickyToken(streamOf(rec)) {
		t.Errorf("cookie value %q is not the token of the served stream %q", c.Value, streamOf(rec))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	repeatStays(t, h, jar{}, "/g1", 30)
}

func TestStickyRedisKeepsStreamPerIP(t *testing.T) {
	h := testEnv(t, config.NewGroups(stickyGroup("ip")), withRedis(t))
	j := jar{}
	repeatStays(t, h, j, "/g1", 30)
	if _, has := j["zts_g1"]; has {
		t.Error("the ip method must not set a cookie")
	}
}

func TestStickyOffWithoutRedisOrFlag(t *testing.T) {
	cases := map[string]func(*config.Group){
		"flag off":      func(g *config.Group) { g.StickyStream = false },
		"no weight":     func(g *config.Group) { g.Weight = false },
		"no window":     func(g *config.Group) { g.UniqSeconds = 0 },
		"ip, no redis":  func(g *config.Group) { g.UniqMethod = "ip" },
		"cookie method": func(g *config.Group) { g.UniqMethod = "cookie"; g.StickyStream = false },
	}
	for name, edit := range cases {
		g := stickyGroup("cookie")
		edit(g)
		j := jar{}
		rec := j.visit(t, testEnv(t, config.NewGroups(g)), "/g1")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", name, rec.Code)
		}
		if _, has := j["zts_g1"]; has {
			t.Errorf("%s: cookie set", name)
		}
		if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control = %q", name, cc)
		}
	}
}

func TestStickyLimitSpentOnce(t *testing.T) {
	limited := weighted("a", 1)
	limited.Rules.Limit = config.LimitRule{Enabled: true, Type: 1, Count: 3}
	h := testEnv(t, config.NewGroups(stickyGroup("cookie", limited)), withRedis(t))
	j := jar{}
	for i := 1; i <= 3; i++ {
		if got := streamOf(j.visit(t, h, "/g1")); got != "a" {
			t.Fatalf("visit %d served %q, want a: the limit counts each serve once", i, got)
		}
	}
	rec := j.visit(t, h, "/g1")
	if got := streamOf(rec); got != "-" || rec.Body.String() != "DEFAULT" {
		t.Errorf("visit 4 served %q %q, want the group default once the limit is spent", got, rec.Body.String())
	}
}

func TestStickyExhaustedLimitDrawsAnother(t *testing.T) {
	limited := weighted("a", 1000)
	limited.Rules.Limit = config.LimitRule{Enabled: true, Type: 1, Count: 1}
	h := testEnv(t, config.NewGroups(stickyGroup("cookie", limited, weighted("b", 1))), withRedis(t))
	j := jar{"zts_g1": {Name: "zts_g1", Value: stickyToken("a")}}
	if got := streamOf(j.visit(t, h, "/g1")); got != "a" {
		t.Fatalf("first returning visit served %q, want the remembered a", got)
	}
	for i := 0; i < 5; i++ {
		if got := streamOf(j.visit(t, h, "/g1")); got != "b" {
			t.Fatalf("limit spent: visit served %q, want b", got)
		}
	}
}

func TestStickyAcrossGroupLink(t *testing.T) {
	entry := &config.Group{ID: "a", Status: true, Redirect: "stop", Streams: []config.Stream{link("to_b", "b", config.Rules{})}}
	target := stickyGroup("cookie")
	target.ID = "b"
	h := testEnv(t, config.NewGroups(entry, target))
	j := jar{}
	repeatStays(t, h, j, "/a", 30)
	if _, has := j["zts_b"]; !has {
		t.Error("the serving group b must hold the cookie")
	}
	if _, has := j["zts_a"]; has {
		t.Error("the entry group did not choose by weight and must not hold a cookie")
	}
}

func TestStickyOnEntryGroupWithLinks(t *testing.T) {
	toB, toC := link("to_b", "b", config.Rules{}), link("to_c", "c", config.Rules{})
	toB.Weight, toC.Weight = 1, 1
	entry := stickyGroup("cookie", toB, toC)
	entry.ID = "a"
	b := &config.Group{ID: "b", Status: true, Redirect: "show_text", Out: "B"}
	c := &config.Group{ID: "c", Status: true, Redirect: "show_text", Out: "C"}
	h := testEnv(t, config.NewGroups(entry, b, c))
	j := jar{}
	first := j.visit(t, h, "/a").Body.String()
	for i := 0; i < 30; i++ {
		if got := j.visit(t, h, "/a").Body.String(); got != first {
			t.Fatalf("visit %d reached %q, the first visit reached %q", i+2, got, first)
		}
	}
}

func TestStickyOnIgnoresApiMode(t *testing.T) {
	g := stickyGroup("cookie")
	d := &engineDeps{}
	if !d.stickyOn(g, false) || d.stickyOn(g, true) || d.stickyOn(nil, false) {
		t.Fatal("stickyOn must be true only for a weighted sticky group outside the api")
	}
	g.UniqMethod = "ip"
	if d.stickyOn(g, false) {
		t.Fatal("the ip method needs Redis")
	}
}
