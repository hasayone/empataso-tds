package store

import "testing"

// offerReserve answers like the offer catalog: offer "on" has a reserve of 12.5, every other offer has none.
func offerReserve(id string) (float64, bool) {
	if id == "on" {
		return 12.5, true
	}
	return 0, false
}

// TestSettlePayout checks the payout a conversion is written with: the three cases of the payout parameter tick
// and the edges around them. The amounts are taken as they are, with no rounding.
func TestSettlePayout(t *testing.T) {
	cases := []struct {
		name      string
		in        PostbackIn
		offer     string
		want      float64
		wantOffer bool
	}{
		{"tick on, payout in the postback", PostbackIn{Profit: 7.333, Status: "sale", OfferPayout: offerReserve}, "on", 7.333, false},
		{"tick on, payout absent, the offer amount is the reserve", PostbackIn{PayoutAbsent: true, Status: "sale", OfferPayout: offerReserve}, "on", 12.5, true},
		{"tick off, no reserve on its own", PostbackIn{PayoutAbsent: true, Status: "sale", OfferPayout: offerReserve}, "off", 0, false},
		{"a payout of zero that was sent stays zero", PostbackIn{Profit: 0, Status: "sale", OfferPayout: offerReserve}, "on", 0, false},
		{"a lead without payout takes the reserve too", PostbackIn{PayoutAbsent: true, Status: " Lead ", OfferPayout: offerReserve}, "on", 12.5, true},
		{"a rejected conversion never takes it", PostbackIn{PayoutAbsent: true, Status: "rejected", OfferPayout: offerReserve}, "on", 0, false},
		{"a click without an offer has no reserve", PostbackIn{PayoutAbsent: true, Status: "sale", OfferPayout: offerReserve}, "", 0, false},
		{"no lookup, no reserve", PostbackIn{PayoutAbsent: true, Status: "sale"}, "on", 0, false},
		{"an offer the catalog does not know", PostbackIn{PayoutAbsent: true, Status: "sale", OfferPayout: offerReserve}, "gone", 0, false},
	}
	for _, c := range cases {
		got, fromOffer := SettlePayout(c.in, c.offer)
		if got != c.want || fromOffer != c.wantOffer {
			t.Errorf("%s: got %v from offer %v, want %v from offer %v", c.name, got, fromOffer, c.want, c.wantOffer)
		}
	}
}
