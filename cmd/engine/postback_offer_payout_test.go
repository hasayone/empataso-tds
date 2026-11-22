package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

// settlingConv is a conversion store that settles the payout of every conversion through store.SettlePayout the way
// the ClickHouse store does, for a click that always belongs to offer.
type settlingConv struct {
	fakeConv
	offer string
	paid  []float64
}

// RecordConversion settles the payout of the click's offer and remembers what the row would be written with.
func (s *settlingConv) RecordConversion(ctx context.Context, in store.PostbackIn) (store.ConversionResult, error) {
	profit, fromOffer := store.SettlePayout(in, s.offer)
	s.paid = append(s.paid, profit)
	return store.ConversionResult{Profit: profit, ProfitFromOffer: fromOffer}, nil
}

// offersBook writes the offers to a temp file and opens them as the engine catalog.
func offersBook(t *testing.T, list []config.Offer) *config.Book[config.Offer] {
	t.Helper()
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "offers.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	book, err := config.OpenBook(path, func(o config.Offer) string { return o.ID })
	if err != nil {
		t.Fatal(err)
	}
	return book
}

// TestPostbackPayoutParameterOfOffer checks the three cases of the offer tick on the real postback path: the tick is
// on and the postback carries a payout, the tick is on and it does not (the offer amount is written), and the tick is
// off (nothing is substituted). A payout of zero that was sent is a payout, and the amounts are not rounded.
func TestPostbackPayoutParameterOfOffer(t *testing.T) {
	book := offersBook(t, []config.Offer{
		{ID: "on", Payout: 12.345, PayoutCurrency: "USD", PayoutParam: true},
		{ID: "off", Payout: 12.345, PayoutCurrency: "USD"},
	})
	cases := []struct {
		name, offer, query string
		want               float64
	}{
		{"tick on, payout in the postback", "on", "payout=7.5", 7.5},
		{"tick on, payout absent, the offer amount", "on", "", 12.345},
		{"tick on, payout empty, the offer amount", "on", "payout=", 12.345},
		{"tick on, payout not a number, the offer amount", "on", "payout=abc", 12.345},
		{"tick on, payout of zero is kept", "on", "payout=0", 0},
		{"tick off, payout in the postback", "off", "payout=7.5", 7.5},
		{"tick off, payout absent, nothing substituted", "off", "", 0},
	}
	for _, c := range cases {
		cs := &settlingConv{offer: c.offer}
		d := queueEnv(t, &cs.fakeConv)
		d.convOverride = cs
		d.offers = book
		target := "/postback?subid=abc&status=sale"
		if c.query != "" {
			target += "&" + c.query
		}
		if rec := sendPostback(d, target); rec.Code != 200 || !strings.Contains(rec.Body.String(), "success") {
			t.Fatalf("%s: answer %d %s", c.name, rec.Code, rec.Body.String())
		}
		if len(cs.paid) != 1 || cs.paid[0] != c.want {
			t.Errorf("%s: written payout %v, want %v", c.name, cs.paid, c.want)
		}
	}
}

// TestPostbackPayoutAbsentMark checks what the engine tells the store: a payout that was sent, even a zero, is not
// absent, and a query without a number (missing, empty, text) is.
func TestPostbackPayoutAbsentMark(t *testing.T) {
	for query, absent := range map[string]bool{
		"subid=a":                 true,
		"subid=a&payout=":         true,
		"subid=a&payout=x":        true,
		"subid=a&payout=0":        false,
		"subid=a&profit=3":        false,
		"subid=a&sum=3":           false,
		"subid=a&payout=x&sum=4":  false,
		"subid=a&depsum=9":        true,
		"subid=a&payout=1&sum=2":  false,
		"subid=a&payout=%20&sum=": true,
	} {
		vals, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if got := parsePostback(vals).payoutAbsent; got != absent {
			t.Errorf("%q: payoutAbsent %v, want %v", query, got, absent)
		}
	}
}

// TestOfferReserveLookup checks the lookup the engine hands to the store: an offer with the tick gives its amount,
// an offer without it, an unknown id and an engine without a catalog give none.
func TestOfferReserveLookup(t *testing.T) {
	d := &engineDeps{offers: offersBook(t, []config.Offer{
		{ID: "on", Payout: 4, PayoutParam: true},
		{ID: "off", Payout: 4},
	})}
	if v, ok := d.offerReserve("on"); !ok || v != 4 {
		t.Errorf("tick on: %v %v", v, ok)
	}
	for _, id := range []string{"off", "none"} {
		if v, ok := d.offerReserve(id); ok || v != 0 {
			t.Errorf("%s: %v %v, want none", id, v, ok)
		}
	}
	if _, ok := (&engineDeps{}).offerReserve("on"); ok {
		t.Error("an engine without a catalog has no reserve")
	}
	var none *engineDeps
	if _, ok := none.offerReserve("on"); ok {
		t.Error("a nil engine has no reserve")
	}
}
