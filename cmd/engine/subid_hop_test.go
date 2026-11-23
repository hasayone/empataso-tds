package main

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// redirect=group keeps the keyword the entry campaign already collected.
// A different keyword name on the next campaign does not replace it, and a
// missing name there does not fall back to q once a keyword was collected.
func TestKeywordKeptAcrossGroupHop(t *testing.T) {
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
	if ins.events[0].Keyword != "entry-kw" || ins.events[1].Keyword != "entry-kw" {
		t.Errorf("keywords = %q %q, want entry-kw both times", ins.events[0].Keyword, ins.events[1].Keyword)
	}
}

// redirect=group keeps the sub ids and extras the entry campaign collected.
// Macros of a later redirect expand those values. A foreign host is not given
// the tracker's own query names.
func TestSourceReadsKeptAcrossGroupHopForMacrosAndLog(t *testing.T) {
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
	if loc := rec.Header().Get("Location"); loc != "https://offer.example/go?s=entry" {
		t.Errorf("Location = %q, want the entry sub id and no added query", loc)
	}
	if len(ins.events) != 1 {
		t.Fatalf("%d events, want 1", len(ins.events))
	}
	if ev := ins.events[0]; ev.Subs[0] != "entry" || ev.Site != "entry-site" {
		t.Errorf("event sub_id_1=%q site=%q, want the entry campaign's values", ev.Subs[0], ev.Site)
	}
}
