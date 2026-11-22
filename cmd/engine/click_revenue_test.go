package main

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// TestClickRevenueCPC checks that a CPC offer writes its payout on the click
// and a CPA offer leaves the click revenue at zero.
func TestClickRevenueCPC(t *testing.T) {
	serve := func(id, model string, payout float64) float64 {
		t.Helper()
		offers := offersBook(t, []config.Offer{{
			ID: id, Status: true, URL: "https://offer.example/" + id,
			PayoutModel: model, Payout: payout, PayoutCurrency: "USD", PayoutParam: true,
		}})
		s := config.Stream{Name: "s", Status: true, Out: config.Output{
			Redirect: "http_redirect", Offers: []config.ShareRef{{ID: id, Share: 1}},
		}}
		h, ins, stop := withClickLog(t, group(s), func(d *engineDeps) { d.offers = offers })
		done := false
		finish := func() {
			if done {
				return
			}
			done = true
			stop()
		}
		defer finish()
		loc := do(t, h, "/g1", "8.8.8.8", nil).Header().Get("Location")
		if loc != "https://offer.example/"+id {
			t.Fatalf("%s location %q", model, loc)
		}
		finish()
		ev := ins.snapshotEvents()
		if len(ev) != 1 {
			t.Fatalf("%s events %d", model, len(ev))
		}
		return ev[0].Revenue
	}
	if got := serve("cpc", "cpc", 1.25); got != 1.25 {
		t.Fatalf("cpc revenue %v, want 1.25", got)
	}
	if got := serve("cpa", "cpa", 4); got != 0 {
		t.Fatalf("cpa wrote revenue on the click: %v", got)
	}
}
