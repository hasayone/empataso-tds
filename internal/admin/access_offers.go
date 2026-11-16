package admin

import "github.com/egerkuzma/emtds/internal/config"

// maskOfferPayout clears the payout amount and currency of o when the profit group
// is hidden for v, so the offer carries no number that gives the profit away.
// o is a copy owned by the caller. It does nothing for an administrator.
func maskOfferPayout(v viewer, o *config.Offer) {
	if fieldHidden(v, "profit") {
		o.Payout = 0
		o.PayoutCurrency = ""
	}
}

// keepOfferPayout restores the stored payout amount and currency into o when the
// profit group is hidden for v, because the caller never saw them and cannot have
// meant to change them. old is the stored offer, nil for a new offer: the payout
// of a new offer then stays empty.
func keepOfferPayout(v viewer, o *config.Offer, old *config.Offer) {
	if !fieldHidden(v, "profit") {
		return
	}
	o.Payout, o.PayoutCurrency = 0, ""
	if old != nil {
		o.Payout, o.PayoutCurrency = old.Payout, old.PayoutCurrency
	}
}
