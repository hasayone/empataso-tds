package config

import "testing"

// TestOfferActionAnswer checks that a ready action answers without a URL and that
// an unknown action, or text and HTML with an empty body, stay a URL redirect.
func TestOfferActionAnswer(t *testing.T) {
	cases := []struct {
		name     string
		offer    Offer
		redirect string
		out      string
		ok       bool
	}{
		{name: "404", offer: Offer{Action: OfferAction404}, redirect: "404_not_found", ok: true},
		{name: "text", offer: Offer{Action: OfferActionText, ActionBody: "hello"}, redirect: "show_text", out: "hello", ok: true},
		{name: "html", offer: Offer{Action: OfferActionHTML, ActionBody: "<b>x</b>"}, redirect: "show_html", out: "<b>x</b>", ok: true},
		{name: "none", offer: Offer{Action: OfferActionNone}, redirect: "stop", ok: true},
		{name: "unknown url", offer: Offer{Action: "group", URL: "https://offer.example/a"}, out: "https://offer.example/a", ok: true},
		{name: "empty text url", offer: Offer{Action: OfferActionText, URL: "https://offer.example/b"}, out: "https://offer.example/b", ok: true},
		{name: "empty html no url", offer: Offer{Action: OfferActionHTML}, ok: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			redirect, out, ok := c.offer.ClickAnswer()
			if redirect != c.redirect || out != c.out || ok != c.ok {
				t.Fatalf("got %q %q %v, want %q %q %v", redirect, out, ok, c.redirect, c.out, c.ok)
			}
		})
	}
	if (Offer{Action: OfferAction404}).SendsURL() {
		t.Fatal("a 404 offer must not take the network query")
	}
	if !(Offer{URL: "https://offer.example/a"}).SendsURL() {
		t.Fatal("a URL offer sends the visitor to the URL")
	}
}
