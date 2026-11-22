package router

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// TestCapLimitServe checks that a used-up limit and a stopped offer both send the click
// to the one alternative, that an empty alternative skips the offer, and that a loop ends.
func TestCapLimitServe(t *testing.T) {
	full := map[string]config.CapHit{
		"a":     config.CapDaily,
		"empty": config.CapDaily,
		"loop1": config.CapDaily,
		"loop2": config.CapTotal,
		"both":  config.CapDaily | config.CapTotal,
		"tot":   config.CapTotal,
		"stop":  0,
	}
	cat := Catalogs{
		Offers: map[string]config.Offer{
			"a":     {ID: "a", URL: "https://offer.example/a", Status: true, FallbackID: "b"},
			"b":     {ID: "b", URL: "https://offer.example/b", Status: true},
			"empty": {ID: "empty", URL: "https://offer.example/e", Status: true},
			"loop1": {ID: "loop1", URL: "https://offer.example/l1", Status: true, FallbackID: "loop2"},
			"loop2": {ID: "loop2", URL: "https://offer.example/l2", Status: true, FallbackID: "loop1"},
			"both":  {ID: "both", URL: "https://offer.example/both", Status: true, FallbackID: "b"},
			"tot":   {ID: "tot", URL: "https://offer.example/tot", Status: true, FallbackID: "b"},
			"stop":  {ID: "stop", URL: "https://offer.example/stop", Status: false, FallbackID: "b"},
		},
		OfferFull: func(id string) config.CapHit { return full[id] },
	}
	if id, ok := cat.Serve("a"); !ok || id != "b" {
		t.Fatalf("daily limit: served %q %v, want the alternative", id, ok)
	}
	if id, ok := cat.Serve("empty"); ok || id != "" {
		t.Fatalf("empty alternative served %q", id)
	}
	if id, ok := cat.Serve("loop1"); ok || id != "" {
		t.Fatalf("a loop of alternatives served %q", id)
	}
	if id, ok := cat.Serve("both"); !ok || id != "b" {
		t.Fatalf("both limits: served %q %v, want the alternative", id, ok)
	}
	if id, ok := cat.Serve("tot"); !ok || id != "b" {
		t.Fatalf("total limit: served %q %v, want the alternative", id, ok)
	}
	if id, ok := cat.Serve("stop"); !ok || id != "b" {
		t.Fatalf("stopped offer: served %q %v, want the same alternative", id, ok)
	}
	s := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "a", Share: 1}}}}
	if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", nil); out != "https://offer.example/b" {
		t.Fatalf("stream out = %s, want the alternative URL", out)
	}
	s.Out.Offers = []config.ShareRef{{ID: "empty", Share: 1}}
	if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", nil); out != "fallback" {
		t.Fatalf("empty alternative out = %s, want the campaign fallback", out)
	}
	s.Out.Offers = []config.ShareRef{{ID: "stop", Share: 1}}
	if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", nil); out != "https://offer.example/b" {
		t.Fatalf("stopped offer out = %s, want the alternative URL", out)
	}
}
