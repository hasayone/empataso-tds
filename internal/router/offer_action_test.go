package router

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// TestOfferActionResolve checks that the chosen offer's action replaces the stream
// redirect, that an unknown action stays a URL redirect, and that the offer id is
// the one that was drawn.
func TestOfferActionResolve(t *testing.T) {
	cat := Catalogs{Offers: map[string]config.Offer{
		"a": {ID: "a", Status: true, Action: config.OfferAction404},
		"b": {ID: "b", Status: true, Action: config.OfferActionText, ActionBody: "plain"},
		"c": {ID: "c", Status: true, Action: config.OfferActionHTML, ActionBody: "<i>h</i>"},
		"d": {ID: "d", Status: true, Action: config.OfferActionNone},
		"e": {ID: "e", Status: true, Action: "campaign", URL: "https://offer.example/e"},
		"f": {ID: "f", Status: true, Action: config.OfferActionText, URL: "https://offer.example/f"},
	}}
	one := func(id string) *config.Stream {
		return &config.Stream{Out: config.Output{
			Redirect: "http_redirect",
			Offers:   []config.ShareRef{{ID: id, Share: 1}},
		}}
	}
	got := func(id string) Served {
		return ResolveServed(one(id), cat, false, "http_redirect", "fallback", nil)
	}
	if s := got("a"); s.Redirect != "404_not_found" || s.Out != "" || s.OfferID != "a" {
		t.Fatalf("404: %+v", s)
	}
	if s := got("b"); s.Redirect != "show_text" || s.Out != "plain" || s.OfferID != "b" {
		t.Fatalf("text: %+v", s)
	}
	if s := got("c"); s.Redirect != "show_html" || s.Out != "<i>h</i>" || s.OfferID != "c" {
		t.Fatalf("html: %+v", s)
	}
	if s := got("d"); s.Redirect != "stop" || s.Out != "" || s.OfferID != "d" {
		t.Fatalf("none: %+v", s)
	}
	if s := got("e"); s.Redirect != "http_redirect" || s.Out != "https://offer.example/e" || s.OfferID != "e" {
		t.Fatalf("unknown: %+v", s)
	}
	if s := got("f"); s.Redirect != "http_redirect" || s.Out != "https://offer.example/f" || s.OfferID != "f" {
		t.Fatalf("empty text: %+v", s)
	}
	both := &config.Stream{Out: config.Output{
		Redirect: "http_redirect",
		Offers:   []config.ShareRef{{ID: "a", Share: 1}, {ID: "b", Share: 1}},
	}}
	if s := ResolveServed(both, cat, false, "http_redirect", "", func(int) int { return 1 }); s.OfferID != "b" || s.Out != "plain" {
		t.Fatalf("share: %+v", s)
	}
}
