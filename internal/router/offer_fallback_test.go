package router

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// fallbackCatalog is a catalog of offers a (stopped, spare b), b (on), c (on, no spare), d (stopped,
// spare e), e (stopped, no spare), x and y (stopped, spare each other) and cap (on, spare b).
func fallbackCatalog(full map[string]bool) Catalogs {
	return Catalogs{
		Offers: map[string]config.Offer{
			"a":   {ID: "a", URL: "https://offer.example/a", Status: false, FallbackID: "b"},
			"b":   {ID: "b", URL: "https://offer.example/b", Status: true},
			"c":   {ID: "c", URL: "https://offer.example/c", Status: true},
			"d":   {ID: "d", URL: "https://offer.example/d", Status: false, FallbackID: "e"},
			"e":   {ID: "e", URL: "https://offer.example/e", Status: false},
			"x":   {ID: "x", URL: "https://offer.example/x", Status: false, FallbackID: "y"},
			"y":   {ID: "y", URL: "https://offer.example/y", Status: false, FallbackID: "x"},
			"cap": {ID: "cap", URL: "https://offer.example/cap", Status: true, FallbackID: "b"},
		},
		OfferFull: func(id string) config.CapHit {
			if full[id] {
				return config.CapDaily
			}
			return 0
		},
	}
}

// TestResolveOutStoppedOfferGoesToSpare checks that a stopped offer with a spare sends its click to the
// spare URL and never to its own, that a stopped offer without a spare is skipped, and that a stream with
// no live offer keeps the fallback of the campaign.
func TestResolveOutStoppedOfferGoesToSpare(t *testing.T) {
	cat := fallbackCatalog(nil)
	stream := func(ids ...string) *config.Stream {
		var refs []config.ShareRef
		for _, id := range ids {
			refs = append(refs, config.ShareRef{ID: id, Share: 1})
		}
		return &config.Stream{Out: config.Output{Offers: refs}}
	}
	_, out := ResolveOut(stream("a"), cat, false, "http_redirect", "fallback", nil)
	if out != "https://offer.example/b" {
		t.Fatalf("stopped offer with a spare: out = %s, want the spare URL", out)
	}
	for roll := 0; roll < 2; roll++ {
		r := roll
		_, out = ResolveOut(stream("a", "c"), cat, false, "http_redirect", "fallback", func(int) int { return r })
		if out == "https://offer.example/a" {
			t.Fatalf("roll %d sent a click to the stopped offer", roll)
		}
	}
	_, out = ResolveOut(stream("a", "c"), cat, false, "http_redirect", "fallback", func(int) int { return 1 })
	if out != "https://offer.example/c" {
		t.Fatalf("second share: out = %s, want the live offer", out)
	}
	_, out = ResolveOut(stream("e", "c"), cat, false, "http_redirect", "fallback", func(int) int { return 0 })
	if out != "https://offer.example/c" {
		t.Fatalf("stopped offer without a spare: out = %s, want it skipped", out)
	}
	if _, out = ResolveOut(stream("e"), cat, false, "http_redirect", "fallback", nil); out != "fallback" {
		t.Fatalf("no live offer: out = %s, want the campaign fallback", out)
	}
}

// TestResolveOutSpareChain checks that the chain of spare offers is followed, that a chain whose end is
// stopped skips the offer, that a loop of spare offers ends, that a spare id missing from the catalog ends the
// chain and that an empty spare id picks nothing.
func TestResolveOutSpareChain(t *testing.T) {
	cat := fallbackCatalog(nil)
	cat.Offers["f"] = config.Offer{ID: "f", URL: "https://offer.example/f", Status: false, FallbackID: "a"}
	cat.Offers["lost"] = config.Offer{ID: "lost", URL: "https://offer.example/lost", Status: false, FallbackID: "gone"}
	cat.Offers["self"] = config.Offer{ID: "self", URL: "https://offer.example/self", Status: false, FallbackID: "self"}
	cases := []struct {
		id   string
		want string
	}{
		{"f", "https://offer.example/b"},
		{"d", "fallback"},
		{"x", "fallback"},
		{"y", "fallback"},
		{"self", "fallback"},
		{"lost", "fallback"},
	}
	for _, c := range cases {
		s := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: c.id, Share: 1}}}}
		if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", nil); out != c.want {
			t.Errorf("offer %s: out = %s, want %s", c.id, out, c.want)
		}
	}
	if id, ok := cat.Serve("x"); ok || id != "" {
		t.Fatalf("a loop of spare offers served %q", id)
	}
	if id, ok := cat.Serve("b"); !ok || id != "b" {
		t.Fatalf("a live offer serves itself: %q %v", id, ok)
	}
}

// TestResolveOutFullOfferUsesAlternative checks that a used-up limit sends the click to the
// same alternative a stopped offer uses, and that a stopped offer whose alternative is also
// full is skipped.
func TestResolveOutFullOfferUsesAlternative(t *testing.T) {
	full := map[string]bool{}
	cat := fallbackCatalog(full)
	s := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "cap", Share: 1}}}}
	if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", nil); out != "https://offer.example/cap" {
		t.Fatalf("limit open: out = %s, want the offer itself", out)
	}
	full["cap"] = true
	if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", nil); out != "https://offer.example/b" {
		t.Fatalf("limit used: out = %s, want the alternative", out)
	}
	full["cap"] = false
	full["b"] = true
	stopped := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "a", Share: 1}}}}
	if _, out := ResolveOut(stopped, cat, false, "http_redirect", "fallback", nil); out != "fallback" {
		t.Fatalf("spare is full: out = %s, want the campaign fallback", out)
	}
}

// TestServeSpareCountry checks that the country cut of an offer does not send the click to its spare
// offer, and that a spare offer whose countries leave out the click is not used.
func TestServeSpareCountry(t *testing.T) {
	cat := fallbackCatalog(nil)
	cat.Offers["pl"] = config.Offer{ID: "pl", URL: "https://offer.example/pl", Status: true, Countries: []string{"pl"}, FallbackID: "b"}
	cat.Offers["a"] = config.Offer{ID: "a", URL: "https://offer.example/a", Status: false, FallbackID: "pl"}
	cat.Country = "de"
	if id, ok := cat.Serve("pl"); ok {
		t.Fatalf("country cut sent the click to the spare offer %q", id)
	}
	if id, ok := cat.Serve("a"); ok {
		t.Fatalf("spare offer outside the click country served %q", id)
	}
	cat.Country = "pl"
	if id, ok := cat.Serve("a"); !ok || id != "pl" {
		t.Fatalf("pl click: %q %v, want the spare offer", id, ok)
	}
}
