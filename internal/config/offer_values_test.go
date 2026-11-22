package config

import "testing"

// TestOfferApplyValues checks that a named value goes into the offer URL
// encoded, that an unknown name stays as written and that the substituted
// text cannot carry a macro into the pass that follows.
func TestOfferApplyValues(t *testing.T) {
	o := Offer{Values: []OfferValue{
		{Name: "promo", Value: "spring sale&1"},
		{Name: " token ", Value: "{subid}[IP]"},
		{Name: "", Value: "skipped"},
		{Name: "ключ", Value: "значение"},
	}}
	cases := []struct{ in, want string }{
		{"https://x.example/?p={offer_value:promo}", "https://x.example/?p=spring+sale%261"},
		{"https://x.example/?t={offer_value:token}", "https://x.example/?t=%7Bsubid%7D%5BIP%5D"},
		{"https://x.example/?k={offer_value:ключ}", "https://x.example/?k=%D0%B7%D0%BD%D0%B0%D1%87%D0%B5%D0%BD%D0%B8%D0%B5"},
		{"https://x.example/?p={offer_value:missing}", "https://x.example/?p={offer_value:missing}"},
		{"https://x.example/?p={offer_value:}", "https://x.example/?p={offer_value:}"},
		{"https://x.example/{offer_value:promo}/{offer_value:promo}", "https://x.example/spring+sale%261/spring+sale%261"},
		{"https://x.example/plain", "https://x.example/plain"},
	}
	for _, c := range cases {
		if got := o.ApplyValues(c.in); got != c.want {
			t.Errorf("ApplyValues(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := (Offer{}).ApplyValues("a={offer_value:promo}"); got != "a={offer_value:promo}" {
		t.Errorf("an offer without values changed the URL: %q", got)
	}
}

// TestOfferCapLimit checks that the limit is reported only when it is on and above zero.
func TestOfferCapLimit(t *testing.T) {
	cases := []struct {
		o    Offer
		n    int
		want bool
	}{
		{Offer{ConvCap: true, ConvCapLimit: 5}, 5, true},
		{Offer{ConvCap: false, ConvCapLimit: 5}, 0, false},
		{Offer{ConvCap: true, ConvCapLimit: 0}, 0, false},
		{Offer{}, 0, false},
	}
	for _, c := range cases {
		n, ok := c.o.CapLimit()
		if n != c.n || ok != c.want {
			t.Errorf("CapLimit(%+v) = %d, %v; want %d, %v", c.o, n, ok, c.n, c.want)
		}
	}
}

// TestOfferAllowsCountry checks the country filter of an offer: no list takes every click, a list takes its
// own codes without regard to case or space, and a click that is not two letters counts as unknown.
func TestOfferAllowsCountry(t *testing.T) {
	cases := []struct {
		list    []string
		country string
		want    bool
	}{
		{nil, "de", true},
		{nil, "-", true},
		{[]string{}, "", true},
		{[]string{"pl"}, "pl", true},
		{[]string{" PL "}, "Pl", true},
		{[]string{"pl", "de"}, "de", true},
		{[]string{"pl"}, "de", false},
		{[]string{"pl"}, "-", false},
		{[]string{"pl"}, "", false},
		{[]string{"unknown"}, "-", true},
		{[]string{"Unknown", "pl"}, "xyz", true},
		{[]string{"unknown"}, "pl", false},
	}
	for _, c := range cases {
		if got := (Offer{Countries: c.list}).AllowsCountry(c.country); got != c.want {
			t.Errorf("countries %q, click %q: got %v, want %v", c.list, c.country, got, c.want)
		}
	}
}
