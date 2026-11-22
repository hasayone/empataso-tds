package main

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/store"
	"github.com/redis/go-redis/v9"
)

// capCounters opens counters on a throwaway Redis.
func capCounters(t *testing.T) *store.Counters {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return store.NewCounters(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
}

// offerStream is a stream that sends every click to the offers o1 and o2 with equal shares.
func offerStream() config.Stream {
	return config.Stream{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect",
		Offers:   []config.ShareRef{{ID: "o1", Share: 1}, {ID: "o2", Share: 1}},
	}}
}

// TestOfferValueInRedirect checks on the real click path that the value of an
// offer is put into its URL encoded and that other macros of the URL still work.
func TestOfferValueInRedirect(t *testing.T) {
	offers := offersBook(t, []config.Offer{{
		ID: "o1", Status: true,
		URL:    "https://offer.example/a?p={offer_value:promo}&c={subid}&k={offer_value:кл}",
		Values: []config.OfferValue{{Name: "promo", Value: "spring sale&1"}, {Name: "кл", Value: "{subid}"}},
	}})
	s := config.Stream{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o1", Share: 1}},
	}}
	h := testEnv(t, group(s), func(d *engineDeps) { d.offers = offers })
	loc := do(t, h, "/g1", "8.8.8.8", nil).Header().Get("Location")
	if !strings.Contains(loc, "p=spring+sale%261") {
		t.Fatalf("the value is not in the URL: %q", loc)
	}
	if !strings.Contains(loc, "k=%7Bsubid%7D") {
		t.Fatalf("a value that looks like a macro was not kept as text: %q", loc)
	}
	if strings.Contains(loc, "{offer_value") || strings.Contains(loc, "c={subid}") {
		t.Fatalf("a placeholder is left in the URL: %q", loc)
	}
}

// TestOfferCapStopsAfterN checks the daily limit: before N leads and sales the
// offer is served, after them it leaves the rotation. A rejection, a repeated
// postback and an offer without the limit do not count or stop anything.
func TestOfferCapStopsAfterN(t *testing.T) {
	offers := offersBook(t, []config.Offer{
		{ID: "o1", Status: true, URL: "https://offer.example/one", ConvCap: true, ConvCapLimit: 2},
		{ID: "o2", Status: true, URL: "https://offer.example/two"},
	})
	counters := capCounters(t)
	var d *engineDeps
	h := testEnv(t, group(offerStream()), func(e *engineDeps) { e.offers, e.counters = offers, counters; d = e })

	seen := func() map[string]int {
		got := map[string]int{}
		for i := 0; i < 60; i++ {
			ip := "8.8." + strconv.Itoa(i/200) + "." + strconv.Itoa(i%200+1)
			got[do(t, h, "/g1", ip, nil).Header().Get("Location")]++
		}
		return got
	}
	if got := seen(); got["https://offer.example/one"] == 0 || got["https://offer.example/two"] == 0 {
		t.Fatalf("both offers must serve before the limit: %v", got)
	}

	ctx := context.Background()
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, "rejected")
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: false, OfferID: "o1"}, "lead")
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: ""}, "lead")
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, "lead")
	if got := seen(); got["https://offer.example/one"] == 0 {
		t.Fatalf("one conversion of two must not stop the offer: %v", got)
	}

	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, " Sale ")
	got := seen()
	if got["https://offer.example/one"] != 0 || got["https://offer.example/two"] != 60 {
		t.Fatalf("after N conversions only the other offer serves: %v", got)
	}

	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o2"}, "sale")
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o2"}, "sale")
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o2"}, "sale")
	if got := seen(); got["https://offer.example/two"] != 60 {
		t.Fatalf("an offer without the limit keeps serving: %v", got)
	}
}

// TestOfferCapOffAndFailOpen checks that a switched-off limit changes nothing
// whatever the counter holds, and that an engine without counters keeps every offer.
func TestOfferCapOffAndFailOpen(t *testing.T) {
	counters := capCounters(t)
	d := &engineDeps{counters: counters}
	offers := map[string]config.Offer{
		"off": {ID: "off", ConvCap: false, ConvCapLimit: 1},
		"on":  {ID: "on", ConvCap: true, ConvCapLimit: 1},
	}
	for i := 0; i < 3; i++ {
		_, _ = counters.NoteOfferConversion(context.Background(), "off", "")
		_, _ = counters.NoteOfferConversion(context.Background(), "on", "")
	}
	full := d.offerFull(context.Background(), offers)
	if full("off") != 0 {
		t.Error("an offer with the limit off was stopped")
	}
	if full("on") == 0 {
		t.Error("an offer that reached its limit was not stopped")
	}
	if full("unknown") != 0 {
		t.Error("an unknown offer was stopped")
	}
	if (&engineDeps{}).offerFull(context.Background(), offers) != nil {
		t.Error("an engine without counters must not filter offers")
	}
}

// TestOfferSpareOnRealPath checks on the click path that a stopped offer with a spare offer sends its
// visitors to the spare URL and never to its own, and that an offer whose daily limit is used up does not
// use its spare offer: it leaves the rotation.
func TestOfferSpareOnRealPath(t *testing.T) {
	offers := offersBook(t, []config.Offer{
		{ID: "o1", Status: false, URL: "https://offer.example/stopped", FallbackID: "o3"},
		{ID: "o2", Status: true, URL: "https://offer.example/capped", ConvCap: true, ConvCapLimit: 1, FallbackID: "o3"},
		{ID: "o3", Status: true, URL: "https://offer.example/spare"},
	})
	counters := capCounters(t)
	var d *engineDeps
	s := config.Stream{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o1", Share: 1}, {ID: "o2", Share: 1}},
	}}
	h := testEnv(t, group(s), func(e *engineDeps) { e.offers, e.counters = offers, counters; d = e })
	seen := func() map[string]int {
		got := map[string]int{}
		for i := 0; i < 40; i++ {
			ip := "8.8.8." + strconv.Itoa(i+1)
			got[do(t, h, "/g1", ip, nil).Header().Get("Location")]++
		}
		return got
	}
	got := seen()
	if got["https://offer.example/stopped"] != 0 || got["https://offer.example/capped"] == 0 || got["https://offer.example/spare"] == 0 {
		t.Fatalf("stopped offer must pass its click to the spare one: %v", got)
	}
	d.noteOfferCap(context.Background(), store.ConversionResult{Fresh: true, OfferID: "o2"}, "sale")
	got = seen()
	if got["https://offer.example/stopped"] != 0 || got["https://offer.example/capped"] != 0 || got["https://offer.example/spare"] != 40 {
		t.Fatalf("limit used up: the capped offer leaves the rotation and the spare offer of the stopped one takes every click: %v", got)
	}
}

// TestClickIDsNameSpareOffer checks that a click sent to a spare offer is counted on the spare offer and
// its network, not on the stopped one, and that a click with no live offer in the chain has no offer.
func TestClickIDsNameSpareOffer(t *testing.T) {
	cat := router.Catalogs{Offers: map[string]config.Offer{
		"o1": {ID: "o1", URL: "https://o.example/dead", Status: false, NetworkID: "dead-net", FallbackID: "o2"},
		"o2": {ID: "o2", URL: "https://o.example/spare", Status: true, NetworkID: "spare-net"},
		"o3": {ID: "o3", URL: "https://o.example/lone", Status: false},
	}}
	s := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "o1", Share: 1}, {ID: "o3", Share: 1}}}}
	if offer, _, network := clickIDs(s, cat, "https://o.example/spare", false); offer != "o2" || network != "spare-net" {
		t.Fatalf("spare click: offer %q network %q, want o2 and spare-net", offer, network)
	}
	if offer, _, _ := clickIDs(s, cat, "https://o.example/dead", false); offer != "" {
		t.Fatalf("a stopped offer was named as the offer of a click: %q", offer)
	}
	if offer, _, _ := clickIDs(s, cat, "https://o.example/lone", false); offer != "" {
		t.Fatalf("a stopped offer without a spare was named: %q", offer)
	}
}

// TestCapLimitRedirectPath checks on the click path that a used-up daily limit sends the click
// to the offer of that limit and not to the spare, and that the total limit counts leads and
// sales only. A rejection adds nothing, and the daily counter is not cleared.
func TestCapLimitRedirectPath(t *testing.T) {
	ctx := context.Background()
	offers := offersBook(t, []config.Offer{
		{ID: "o1", Status: true, URL: "https://offer.example/capped", ConvCap: true, ConvCapLimit: 1, FallbackID: "o3"},
		{ID: "o2", Status: true, URL: "https://offer.example/spare"},
		{ID: "o3", Status: true, URL: "https://offer.example/redirect"},
	})
	counters := capCounters(t)
	var d *engineDeps
	s := config.Stream{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o1", Share: 1}},
	}}
	h := testEnv(t, group(s), func(e *engineDeps) { e.offers, e.counters = offers, counters; d = e })
	seen := func() map[string]int {
		got := map[string]int{}
		for i := 0; i < 20; i++ {
			ip := "8.8.8." + strconv.Itoa(i+1)
			got[do(t, h, "/g1", ip, nil).Header().Get("Location")]++
		}
		return got
	}
	if got := seen(); got["https://offer.example/capped"] != 20 {
		t.Fatalf("before the limit: %v", got)
	}
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, "rejected")
	if got := seen(); got["https://offer.example/capped"] != 20 {
		t.Fatalf("a rejection counted toward the daily limit: %v", got)
	}
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, "sale")
	got := seen()
	if got["https://offer.example/capped"] != 0 || got["https://offer.example/redirect"] != 20 || got["https://offer.example/spare"] != 0 {
		t.Fatalf("daily limit used up: %v", got)
	}
	if n, err := counters.OfferConversions(ctx, "o1", ""); err != nil || n != 1 {
		t.Fatalf("daily counter = %d %v, want 1", n, err)
	}

	offers = offersBook(t, []config.Offer{
		{ID: "o1", Status: true, URL: "https://offer.example/total", ConvTotal: true, ConvTotalLimit: 2, FallbackID: "o2", ConvCap: true, ConvCapLimit: 100},
		{ID: "o2", Status: true, URL: "https://offer.example/next"},
	})
	counters = capCounters(t)
	if ok, err := counters.SeedOfferTotal(ctx, "o1", 0); err != nil || !ok {
		t.Fatalf("seed = %v %v", ok, err)
	}
	h = testEnv(t, group(s), func(e *engineDeps) { e.offers, e.counters = offers, counters; d = e })
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, "rejected")
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, "lead")
	if got := seen(); got["https://offer.example/total"] != 20 {
		t.Fatalf("one lead of two must not stop the offer: %v", got)
	}
	d.noteOfferCap(ctx, store.ConversionResult{Fresh: true, OfferID: "o1"}, "sale")
	if got := seen(); got["https://offer.example/next"] != 20 || got["https://offer.example/total"] != 0 {
		t.Fatalf("total limit used up: %v", got)
	}
	if n, err := counters.OfferConversions(ctx, "o1", ""); err != nil || n != 2 {
		t.Fatalf("daily counter after the total limit = %d %v, want 2", n, err)
	}
}
