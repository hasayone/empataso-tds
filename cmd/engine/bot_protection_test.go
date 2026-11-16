package main

import (
	"net/http"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

// botProtectionGroup builds a campaign shaped like a Keitaro import: a first stream
// "Bot Protection" that keeps bots, desktops (device reject mobile and tablet) or a Google
// organization with OR, and a regular stream that keeps humans only.
func botProtectionGroup() *config.Groups {
	bp := config.Stream{Name: "Bot Protection", Status: true, FilterOr: true,
		Rules: config.Rules{
			Phone: config.FlagA, Tablet: config.FlagA, Bot: config.FlagA,
			Org: config.ListFilter{Flag: config.FlagB, Values: []string{"Google LLC"}},
		},
		Out: config.Output{Redirect: "404_not_found"}}
	flow := config.Stream{Name: "Flow", Status: true,
		Rules: config.Rules{Bot: config.FlagB},
		Out:   config.Output{Redirect: "show_text", Out: "HUMAN"}}
	return group(bp, flow)
}

func TestBotProtectionCatchesBotsBeforeRegularStream(t *testing.T) {
	stubPTR(t, ptrNamed(), fwdTo())
	h := testEnv(t, botProtectionGroup())
	cases := []struct {
		name, ip, ua, stream, bot string
		code                      int
	}{
		{"googlebot", "66.249.66.1", uaGoogleBot, "Bot Protection", "google", http.StatusNotFound},
		{"empty user agent", "8.8.8.8", "", "Bot Protection", "empty_ua", http.StatusNotFound},
		{"desktop human", "8.8.8.8", uaWinChrome, "Bot Protection", "-", http.StatusNotFound},
		{"mobile human", "8.8.8.8", uaIPhone, "Flow", "-", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, "/g1", c.ip, map[string]string{"User-Agent": c.ua, "Accept-Language": "en"})
			wantStream(t, rec, c.stream)
			wantHdr(t, rec, "X-Emtds-Bot", c.bot)
			wantCode(t, rec, c.code)
		})
	}
}
