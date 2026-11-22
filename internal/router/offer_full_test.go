package router

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// TestResolveOutSkipsFullOffer checks that an offer reported as full leaves
// the rotation, that its share goes to the others and that a stream whose only
// offer is full keeps the fallback.
func TestResolveOutSkipsFullOffer(t *testing.T) {
	full := map[string]bool{}
	cat := Catalogs{
		Offers: map[string]config.Offer{
			"o1": {ID: "o1", URL: "https://offer.example/a", Status: true},
			"o2": {ID: "o2", URL: "https://offer.example/b", Status: true},
		},
		OfferFull: func(id string) config.CapHit {
			if full[id] {
				return config.CapDaily
			}
			return 0
		},
	}
	s := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "o1", Share: 1}, {ID: "o2", Share: 1}}}}
	if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", func(int) int { return 0 }); out != "https://offer.example/a" {
		t.Fatalf("no offer is full: out = %s", out)
	}
	full["o1"] = true
	for roll := 0; roll < 2; roll++ {
		r := roll
		if _, out := ResolveOut(s, cat, false, "http_redirect", "fallback", func(int) int { return r }); out != "https://offer.example/b" {
			t.Fatalf("o1 is full, roll %d: out = %s, want the second offer", roll, out)
		}
	}
	full["o2"] = true
	if redirect, out := ResolveOut(s, cat, false, "http_redirect", "fallback", nil); redirect != "http_redirect" || out != "fallback" {
		t.Fatalf("every offer is full: got %s %s, want the fallback", redirect, out)
	}
	if (Catalogs{}).Full("o1") {
		t.Fatal("a catalog without OfferFull has a full offer")
	}
}

// TestResolveOutCutsOfferByCountry checks that an offer with countries is served to a click from one of them
// only, that an offer without countries takes every click, that a click of unknown country reaches an offer
// only through the unknown entry, that the dash and an empty country list behave as documented, and that a
// catalog without a click country cuts nothing.
func TestResolveOutCutsOfferByCountry(t *testing.T) {
	cat := Catalogs{
		Offers: map[string]config.Offer{
			"pl":   {ID: "pl", URL: "https://offer.example/pl", Status: true, Countries: []string{"PL"}},
			"any":  {ID: "any", URL: "https://offer.example/any", Status: true},
			"anon": {ID: "anon", URL: "https://offer.example/anon", Status: true, Countries: []string{"unknown"}},
		},
	}
	pick := func(country, id string) string {
		c := cat
		c.Country = country
		s := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: id, Share: 1}}}}
		_, out := ResolveOut(s, c, false, "http_redirect", "fallback", func(int) int { return 0 })
		return out
	}
	cases := []struct{ country, id, want string }{
		{"pl", "pl", "https://offer.example/pl"},
		{"PL", "pl", "https://offer.example/pl"},
		{"de", "pl", "fallback"},
		{"-", "pl", "fallback"},
		{"de", "any", "https://offer.example/any"},
		{"-", "any", "https://offer.example/any"},
		{"-", "anon", "https://offer.example/anon"},
		{"de", "anon", "fallback"},
		{"", "pl", "https://offer.example/pl"},
	}
	for _, c := range cases {
		if got := pick(c.country, c.id); got != c.want {
			t.Errorf("click %q, offer %s: got %s, want %s", c.country, c.id, got, c.want)
		}
	}
	both := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "pl", Share: 1}, {ID: "any", Share: 1}}}}
	c := cat
	c.Country = "de"
	for roll := 0; roll < 2; roll++ {
		r := roll
		if _, out := ResolveOut(both, c, false, "http_redirect", "fallback", func(int) int { return r }); out != "https://offer.example/any" {
			t.Fatalf("de click, roll %d: got %s, want the offer without countries", roll, out)
		}
	}
}
