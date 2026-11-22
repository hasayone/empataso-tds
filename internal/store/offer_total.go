package store

import (
	"context"
	"strings"
)

// offerTotalSQL counts the leads and sales stored for an offer, once for each click and status. A
// click whose lead or sale a later rejection voided is not counted. The only placeholder is the
// offer id. It does not fail.
func offerTotalSQL() string {
	return `SELECT count() FROM conversions FINAL
		WHERE offer_id = ? AND ` + confirmedStatusSQL("status") + voidedConfirmedSQL("cid", "status")
}

// OfferConversionTotal returns how many leads and sales the offer has over all time in the store.
// offerID is the catalog id of the offer. A store whose conversions carry no offer column has none
// to count and returns zero. A failed query returns its error.
func (c *CH) OfferConversionTotal(ctx context.Context, offerID string) (int64, error) {
	offerID = strings.TrimSpace(offerID)
	if c == nil || offerID == "" || !c.clickCols {
		return 0, nil
	}
	var n uint64
	if err := c.conn.QueryRow(ctx, offerTotalSQL(), offerID).Scan(&n); err != nil {
		return 0, err
	}
	return int64(n), nil
}
