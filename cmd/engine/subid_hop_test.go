package main

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// After redirect=group the keyword is read through the serving group's source:
// the entry group's mapped keyword does not leak, the plain q stays as the base.
func TestKeywordRecomputedOnGroupHop(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Status: true, SourceID: "sa", Redirect: "stop", Streams: []config.Stream{link("to_b", "b", config.Rules{})}},
		&config.Group{ID: "b", Status: true, SourceID: "sb", Redirect: "show_text", Out: "B"},
	)
	h, ins, stop := withClickLog(t, gg, withSources(t,
		config.TrafficSource{ID: "sa", Parameters: map[string]string{"keyword": "ka"}},
		config.TrafficSource{ID: "sb", Parameters: map[string]string{"keyword": "kb"}},
	))
	do(t, h, "/a?q=base&ka=entry-kw&kb=serving-kw", "8.8.8.8", chrome)
	do(t, h, "/a?q=base&ka=entry-kw", "8.8.8.8", chrome)
	stop()
	if len(ins.events) != 2 {
		t.Fatalf("%d events, want 2", len(ins.events))
	}
	got := map[string]bool{ins.events[0].Keyword: true, ins.events[1].Keyword: true}
	if !got["serving-kw"] || !got["base"] || len(got) != 2 {
		t.Errorf("keywords = %v, want serving-kw and base (never entry-kw)", got)
	}
}

// After redirect=group the macros and the click log carry the sub ids and
// extras read through the serving group's source, not the entry group's.
func TestSourceReadsRecomputedOnGroupHopForMacrosAndLog(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Status: true, SourceID: "sa", Redirect: "stop", Streams: []config.Stream{link("to_b", "b", config.Rules{})}},
		&config.Group{ID: "b", Status: true, SourceID: "sb", Redirect: "http_redirect", Out: "https://offer.example/go?s={sub_id_1}"},
	)
	h, ins, stop := withClickLog(t, gg, withSources(t,
		config.TrafficSource{ID: "sa", Parameters: map[string]string{"sub_id_1": "a1", "site": "asite"}},
		config.TrafficSource{ID: "sb", Parameters: map[string]string{"sub_id_1": "b1", "site": "bsite"}},
	))
	rec := do(t, h, "/a?a1=entry&b1=serving&asite=entry-site&bsite=serving-site", "8.8.8.8", chrome)
	stop()
	if loc := rec.Header().Get("Location"); loc != "https://offer.example/go?s=serving" {
		t.Errorf("macro read the entry group's parameter: Location = %q", loc)
	}
	if len(ins.events) != 1 {
		t.Fatalf("%d events, want 1", len(ins.events))
	}
	if ev := ins.events[0]; ev.Subs[0] != "serving" || ev.Site != "serving-site" {
		t.Errorf("event sub_id_1=%q site=%q, want the serving group's values", ev.Subs[0], ev.Site)
	}
}
