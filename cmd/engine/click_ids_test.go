package main

import (
	"encoding/json"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/router"
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

func TestClickIDsLocalLandingID(t *testing.T) {
	s := &config.Stream{Out: config.Output{Landings: []config.ShareRef{{ID: "7", Share: 1}}}}
	cat := router.Catalogs{Landings: map[string]config.Landing{
		"7": {ID: "7", Status: true, Local: true, Entry: "index.html"},
	}}
	offer, landing, network := clickIDs(s, cat, "7", false)
	if offer != "" || landing != "7" || network != "" {
		t.Fatalf("got %s %s %s", offer, landing, network)
	}
}

// TestCatalogsReadOnlyForRouting checks that choosing a destination and
// reading the click ids leave the shared catalog snapshots untouched: they
// are the books' own maps and every concurrent click reads them.
func TestCatalogsReadOnlyForRouting(t *testing.T) {
	offers := loadBook(t, "offers.json", []config.Offer{
		{ID: "o1", URL: "https://o.example/a", Status: true, NetworkID: "net"},
		{ID: "o2", URL: "https://o.example/b", Status: true, NetworkID: "net"},
		{ID: "o3", URL: "https://o.example/off", Status: false},
	}, func(o config.Offer) string { return o.ID })
	landings := loadBook(t, "landings.json", []config.Landing{
		{ID: "l1", URL: "https://l.example/", Status: true},
		{ID: "7", Status: true, Local: true, Entry: "index.html"},
	}, func(l config.Landing) string { return l.ID })
	d := &engineDeps{offers: offers, landings: landings}
	s := &config.Stream{Out: config.Output{
		Redirect: "http_redirect",
		Offers:   []config.ShareRef{{ID: "o1", Share: 50}, {ID: "o2", Share: 50}, {ID: "o3", Share: 10}},
		Landings: []config.ShareRef{{ID: "l1", Share: 50}, {ID: "7", Share: 50}},
	}}

	snapshot := func() string {
		raw, err := json.Marshal(d.catalogs())
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := snapshot()
	cat := d.catalogs()
	for _, offerStep := range []bool{false, true} {
		for pick := 0; pick < 3; pick++ {
			_, out := router.ResolveOut(s, cat, offerStep, "http_redirect", "", func(n int) int { return pick % n })
			clickIDs(s, cat, out, offerStep)
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("routing changed the catalog maps:\nbefore %s\nafter  %s", before, after)
	}
	if len(cat.Offers) != 3 || len(cat.Landings) != 2 {
		t.Fatalf("catalog sizes %d offers, %d landings, want 3 and 2", len(cat.Offers), len(cat.Landings))
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
