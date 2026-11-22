package config

import (
	"math"
	"testing"
)

// TestOfferReservePayout checks when an offer gives a reserve payout: the tick is on, the amount is a finite
// number above zero and the currency is USD or empty. An offer with the tick off keeps its amount to itself.
func TestOfferReservePayout(t *testing.T) {
	cases := []struct {
		name  string
		offer Offer
		want  float64
		ok    bool
	}{
		{"tick on, USD", Offer{PayoutParam: true, Payout: 12.5, PayoutCurrency: "USD"}, 12.5, true},
		{"tick on, no currency", Offer{PayoutParam: true, Payout: 3}, 3, true},
		{"tick on, lower case with spaces", Offer{PayoutParam: true, Payout: 3, PayoutCurrency: " usd "}, 3, true},
		{"the amount is not rounded", Offer{PayoutParam: true, Payout: 1.005, PayoutCurrency: "USD"}, 1.005, true},
		{"tick off", Offer{Payout: 12.5, PayoutCurrency: "USD"}, 0, false},
		{"tick on, no amount", Offer{PayoutParam: true, PayoutCurrency: "USD"}, 0, false},
		{"tick on, negative amount", Offer{PayoutParam: true, Payout: -1}, 0, false},
		{"tick on, not a number", Offer{PayoutParam: true, Payout: math.NaN()}, 0, false},
		{"tick on, infinite", Offer{PayoutParam: true, Payout: math.Inf(1)}, 0, false},
		{"tick on, another currency is not written as dollars", Offer{PayoutParam: true, Payout: 500, PayoutCurrency: "RUB"}, 0, false},
	}
	for _, c := range cases {
		got, ok := c.offer.ReservePayout()
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
