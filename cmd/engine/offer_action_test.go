package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// TestOfferActionClick checks the click path: 404, text, HTML and do-nothing are
// the response, and an unknown action or an empty text body stays a URL redirect.
func TestOfferActionClick(t *testing.T) {
	cases := []struct {
		name  string
		offer config.Offer
		code  int
		ctype string
		body  string
		loc   string
	}{
		{name: "404", offer: config.Offer{ID: "o1", Status: true, Action: "404"}, code: http.StatusNotFound},
		{name: "text", offer: config.Offer{ID: "o1", Status: true, Action: "text", ActionBody: "hello"}, code: http.StatusOK, ctype: "text/plain", body: "hello"},
		{name: "html", offer: config.Offer{ID: "o1", Status: true, Action: "html", ActionBody: "<p>hi</p>"}, code: http.StatusOK, ctype: "text/html", body: "<p>hi</p>"},
		{name: "none", offer: config.Offer{ID: "o1", Status: true, Action: "none"}, code: http.StatusOK},
		{name: "unknown stays url", offer: config.Offer{ID: "o1", Status: true, Action: "group", URL: "https://offer.example/a"}, code: http.StatusFound, loc: "https://offer.example/a"},
		{name: "empty text stays url", offer: config.Offer{ID: "o1", Status: true, Action: "text", URL: "https://offer.example/b"}, code: http.StatusFound, loc: "https://offer.example/b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			offers := offersBook(t, []config.Offer{c.offer})
			s := config.Stream{Name: "s", Status: true, Out: config.Output{
				Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o1", Share: 1}},
			}}
			h := testEnv(t, group(s), func(d *engineDeps) { d.offers = offers })
			rec := do(t, h, "/g1", "8.8.8.8", nil)
			if rec.Code != c.code {
				t.Fatalf("status %d, want %d, body %q loc %q", rec.Code, c.code, rec.Body.String(), rec.Header().Get("Location"))
			}
			if rec.Header().Get("Location") != c.loc {
				t.Fatalf("location %q, want %q", rec.Header().Get("Location"), c.loc)
			}
			if c.ctype != "" && !strings.Contains(rec.Header().Get("Content-Type"), c.ctype) {
				t.Fatalf("content-type %q, want %s", rec.Header().Get("Content-Type"), c.ctype)
			}
			if c.body != "" && rec.Body.String() != c.body {
				t.Fatalf("body %q, want %q", rec.Body.String(), c.body)
			}
			if c.name == "404" && (rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "") {
				t.Fatalf("404 must be a bare status, got %q (%q)", rec.Body.String(), rec.Header().Get("Content-Type"))
			}
			if c.name == "html" && strings.Contains(rec.Body.String(), "<!DOCTYPE") {
				t.Fatalf("html was wrapped: %q", rec.Body.String())
			}
			if c.name == "none" && rec.Body.Len() != 0 {
				t.Fatalf("none wrote %q", rec.Body.String())
			}
			if rec.Header().Get("X-Emtds-Stream") != "s" {
				t.Fatalf("stream %q", rec.Header().Get("X-Emtds-Stream"))
			}
		})
	}
}
