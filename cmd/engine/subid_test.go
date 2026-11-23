package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// withSources loads a traffic-source book into the engine under test.
func withSources(t *testing.T, sources ...config.TrafficSource) func(*engineDeps) {
	t.Helper()
	raw, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	book, err := config.OpenBook(path, func(o config.TrafficSource) string { return o.ID })
	if err != nil {
		t.Fatal(err)
	}
	return func(d *engineDeps) { d.sources = book }
}

var chrome = map[string]string{"User-Agent": uaWinChrome}

func vipOnly(name, out string) config.Stream {
	return text(name, out, config.Rules{SubIDs: map[string]config.SubIDFilter{
		"sub_id_1": {Flag: config.FlagB, Values: []string{"vip"}},
	}})
}

func TestSubIDRuleReadsSourceMappedParameter(t *testing.T) {
	gg := linked(&config.Group{
		ID: "c", Status: true, SourceID: "sc", Redirect: "show_text", Out: "DEFAULT",
		Streams: []config.Stream{vipOnly("vip", "VIP")},
	})
	h := testEnv(t, gg, withSources(t, config.TrafficSource{ID: "sc", Parameters: map[string]string{"sub_id_1": "sub1"}}))
	if got := do(t, h, "/c?sub1=vip", "8.8.8.8", chrome).Body.String(); got != "VIP" {
		t.Errorf("renamed parameter: body = %q, want VIP", got)
	}
	if got := do(t, h, "/c?sub_id_1=vip", "8.8.8.8", chrome).Body.String(); got != "DEFAULT" {
		t.Errorf("raw name under a renaming source: body = %q, want DEFAULT", got)
	}
}

func TestSubIDRuleWithoutSourceReadsOwnName(t *testing.T) {
	gg := linked(&config.Group{
		ID: "c", Status: true, Redirect: "show_text", Out: "DEFAULT",
		Streams: []config.Stream{vipOnly("vip", "VIP")},
	})
	h := testEnv(t, gg)
	if got := do(t, h, "/c?sub_id_1=VIP", "8.8.8.8", chrome).Body.String(); got != "VIP" {
		t.Errorf("body = %q, want VIP", got)
	}
	if got := do(t, h, "/c", "8.8.8.8", chrome).Body.String(); got != "DEFAULT" {
		t.Errorf("blank sub id: body = %q, want DEFAULT", got)
	}
}

// redirect=group keeps a sub id the entry campaign already collected.
// The next campaign's mapping fills a sub id that is still empty and does not
// replace a collected value, even with a different non-empty read.
func TestSubIDKeptAcrossGroupHop(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Status: true, SourceID: "sa", Redirect: "stop", Streams: []config.Stream{link("to_b", "b", config.Rules{})}},
		&config.Group{ID: "b", Status: true, SourceID: "sb", Redirect: "show_text", Out: "B DEFAULT",
			Streams: []config.Stream{vipOnly("vip", "B VIP")}},
	)
	h := testEnv(t, gg, withSources(t,
		config.TrafficSource{ID: "sa", Parameters: map[string]string{"sub_id_1": "sub1"}},
		config.TrafficSource{ID: "sb", Parameters: map[string]string{"sub_id_1": "x"}},
	))
	if got := do(t, h, "/a?x=vip&sub1=other", "8.8.8.8", chrome).Body.String(); got != "B DEFAULT" {
		t.Errorf("collected sub id replaced: body = %q, want B DEFAULT", got)
	}
	if got := do(t, h, "/a?sub1=vip", "8.8.8.8", chrome).Body.String(); got != "B VIP" {
		t.Errorf("collected sub id cleared: body = %q, want B VIP", got)
	}
	if got := do(t, h, "/a?x=vip", "8.8.8.8", chrome).Body.String(); got != "B VIP" {
		t.Errorf("empty sub id not filled: body = %q, want B VIP", got)
	}
}
