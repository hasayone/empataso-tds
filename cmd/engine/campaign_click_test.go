package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// promoCampaign is the shape of the local promo flow: the public path is
// /promo, a desktop browser is redirected, and the visit must be logged.
func promoCampaign() *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "promo", Name: "promo", Status: true, Redirect: "stop",
		Streams: []config.Stream{{
			Name:   "desktop",
			Status: true,
			Rules:  config.Rules{Phone: config.FlagA, Tablet: config.FlagA},
			Out:    config.Output{Redirect: "http_redirect", Out: "https://example.com/b"},
		}},
	})
}

// openCampaignLink sends the request a browser makes when the address from
// the campaign page is opened: host localhost, port 8080, path /promo, a
// normal Chrome user agent, and the admin page as the referer. peer is the
// TCP address the engine sees. No proxy headers are set.
func openCampaignLink(h http.Handler, peer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/promo", nil)
	req.Host = "localhost:8080"
	req.RemoteAddr = peer
	req.Header.Set("User-Agent", uaWinChrome)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	req.Header.Set("Referer", "http://localhost:8090/")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestCampaignLinkFromLocalhostIsCounted checks that opening the campaign
// link records one human click. The first peer is the machine itself. The
// second is the address Docker shows when the browser on the host reaches
// the published port. Both must be stored.
func TestCampaignLinkFromLocalhostIsCounted(t *testing.T) {
	cases := []struct {
		name string
		peer string
		ip   string
	}{
		{name: "loopback", peer: "127.0.0.1:52341", ip: "127.0.0.1"},
		{name: "published port", peer: "172.17.0.1:52341", ip: "172.17.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, ins, stop := curlEnv(t, promoCampaign(), "0")
			rec := openCampaignLink(h, c.peer)
			stop()

			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != "https://example.com/b" {
				t.Fatalf("location = %q", got)
			}
			if got := rec.Header().Get("X-Kuztds-Stream"); got != "desktop" {
				t.Fatalf("stream = %q", got)
			}
			if got := rec.Header().Get("X-Kuztds-Bot"); got != "-" {
				t.Fatalf("bot = %q, a browser opening the link must stay a click", got)
			}
			if got := rec.Header().Get("X-Kuztds-Uniq"); got != "yes" {
				t.Fatalf("uniq = %q", got)
			}
			if len(ins.events) != 1 {
				t.Fatalf("logged clicks = %d, want 1", len(ins.events))
			}
			ev := ins.events[0]
			if ev.GroupID != "promo" || ev.Stream != "desktop" || ev.Redirect != "http_redirect" {
				t.Fatalf("event = group %q stream %q redirect %q", ev.GroupID, ev.Stream, ev.Redirect)
			}
			if ev.Uniq != 1 || ev.Bot != "" || ev.IP != c.ip {
				t.Fatalf("event uniq=%d bot=%q ip=%q", ev.Uniq, ev.Bot, ev.IP)
			}
		})
	}
}
