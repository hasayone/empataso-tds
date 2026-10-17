package main

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/geo"
)

func TestProxyOrgSelectsStream(t *testing.T) {
	proxy := config.Stream{
		Name:  "proxies", Status: true,
		Rules: config.Rules{Proxy: config.FlagA},
		Out:   config.Output{Redirect: "show_text", Out: "P"},
	}
	open := config.Stream{Name: "open", Status: true, Out: config.Output{Redirect: "show_text", Out: "O"}}
	gg := group(proxy, open)
	hdr := map[string]string{"User-Agent": uaWinChrome}

	hetzner := geo.None()
	hetzner.Org = "Hetzner Online GmbH"
	h := testEnv(t, gg, withGeo(hetzner))
	wantStream(t, do(t, h, "/g1", "8.8.8.8", hdr), "proxies")

	comcast := geo.None()
	comcast.Org = "Comcast"
	h = testEnv(t, gg, withGeo(comcast))
	wantStream(t, do(t, h, "/g1", "8.8.8.8", hdr), "open")
}

func TestIPVersionSelectsIPv6(t *testing.T) {
	v6 := config.Stream{
		Name:  "v6", Status: true,
		Rules: config.Rules{IPVersion: config.FlagB},
		Out:   config.Output{Redirect: "show_text", Out: "6"},
	}
	open := config.Stream{Name: "open", Status: true, Out: config.Output{Redirect: "show_text", Out: "O"}}
	h := testEnv(t, group(v6, open))
	hdr := map[string]string{"User-Agent": uaWinChrome}
	wantStream(t, do(t, h, "/g1", "2001:db8::1", hdr), "v6")
	wantStream(t, do(t, h, "/g1", "8.8.8.8", hdr), "open")
}
