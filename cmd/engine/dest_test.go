package main

import (
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// TestClickLogsDestination — each exit type logs where the visitor went: the
// expanded URL, the action with no address, the target campaign's exit after a
// hop, and a body capped at logbuf.MaxOut.
func TestClickLogsDestination(t *testing.T) {
	body := strings.Repeat("<p>x</p>", 1000)
	tests := []struct {
		name     string
		groups   *config.Groups
		redirect string
		out      string
	}{
		{"redirect", group(config.Stream{Name: "s", Status: true,
			Out: config.Output{Redirect: "http_redirect", Out: "https://offer.example/?k=[KEY]"}}),
			"http_redirect", "https://offer.example/?k=promo"},
		{"action", group(config.Stream{Name: "s", Status: true,
			Out: config.Output{Redirect: "404_not_found"}}),
			"404_not_found", ""},
		{"body", group(config.Stream{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_html", Out: body}}),
			"show_html", body[:logbuf.MaxOut]},
		{"campaign hop", config.NewGroups(
			&config.Group{ID: "g1", Name: "g1", Status: true, Redirect: "group", Out: "g2", UniqSeconds: 86400},
			&config.Group{ID: "g2", Name: "g2", Status: true, Redirect: "http_redirect", Out: "https://two.example/", UniqSeconds: 86400}),
			"http_redirect", "https://two.example/"},
	}
	for _, tc := range tests {
		h, ins, stop := withClickLog(t, tc.groups, nil)
		do(t, h, "/g1?q=promo", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
		stop()
		if len(ins.events) != 1 {
			t.Fatalf("%s: %d events", tc.name, len(ins.events))
		}
		ev := ins.events[0]
		if ev.Redirect != tc.redirect || ev.Out != tc.out {
			t.Fatalf("%s: redirect %q out %q, want %q %q", tc.name, ev.Redirect, ev.Out, tc.redirect, tc.out)
		}
	}
}
