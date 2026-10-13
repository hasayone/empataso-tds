package main

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/router"
)

func TestClickIDsMatchesOfferURL(t *testing.T) {
	s := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "o1", Share: 1}}}}
	cat := router.Catalogs{Offers: map[string]config.Offer{
		"o1": {ID: "o1", URL: "https://o.example/a", Status: true, NetworkID: "net"},
	}}
	offer, landing, network := clickIDs(s, cat, "https://o.example/a", false)
	if offer != "o1" || landing != "" || network != "net" {
		t.Fatalf("got %s %s %s", offer, landing, network)
	}
}

func TestClickIDsSkipsInactiveLanding(t *testing.T) {
	s := &config.Stream{Out: config.Output{
		Landings: []config.ShareRef{{ID: "l1", Share: 1}},
		Offers:   []config.ShareRef{{ID: "o1", Share: 1}},
	}}
	cat := router.Catalogs{
		Landings: map[string]config.Landing{"l1": {ID: "l1", URL: "https://l.example", Status: false}},
		Offers:   map[string]config.Offer{"o1": {ID: "o1", URL: "https://o.example/a", Status: true, NetworkID: "net"}},
	}
	offer, landing, network := clickIDs(s, cat, "https://o.example/a", false)
	if offer != "o1" || landing != "" || network != "net" {
		t.Fatalf("inactive landing hid the offer: %s %s %s", offer, landing, network)
	}
}

func TestStoredSubsKeepsTwelve(t *testing.T) {
	var in [config.MaxSubID]string
	for i := range in {
		in[i] = "s"
	}
	in[0] = "one"
	out := storedSubs(in)
	if out[0] != "one" || out[11] != "s" {
		t.Fatalf("%q", out)
	}
}
