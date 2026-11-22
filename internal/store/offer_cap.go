package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// offerCapTTL is how long the counter of one day lives; the margin covers
// a conversion that arrives right at midnight.
const offerCapTTL = 48 * time.Hour

// capLocation is the zone whose midnight ends the daily counter.
// zone is an IANA name. Empty, Local and an unknown name stay on UTC, so a
// counter written before a zone was saved is not split across two keys.
func capLocation(zone string) *time.Location {
	zone = strings.TrimSpace(zone)
	if zone == "" || strings.EqualFold(zone, "Local") {
		return time.UTC
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// offerCapKey is the Redis key of the offer's conversion counter for the day of now in zone.
// offerID is the catalog id of the offer. zone is the offer's CapTZ.
func (c *Counters) offerCapKey(offerID, zone string) string {
	return "cap:o:" + offerID + ":" + c.now().In(capLocation(zone)).Format("20060102")
}

// NoteOfferConversion counts one lead or sale of the offer in the current day of zone.
// The caller passes only conversions that were written for the first time and
// whose status is lead or sale; a rejected conversion is not counted.
// zone is the offer's time zone; empty counts the UTC day.
// It returns the new count. On a Redis failure it returns the error and the
// count is lost: a broken counter must not stop the postback.
func (c *Counters) NoteOfferConversion(ctx context.Context, offerID, zone string) (int64, error) {
	if c == nil || offerID == "" {
		return 0, nil
	}
	return c.incrWithTTL(ctx, c.offerCapKey(offerID, zone), offerCapTTL)
}

// OfferConversions returns how many leads and sales the offer has in the current day of zone.
// An offer without a counter has zero. On a Redis failure it returns the error
// and the caller decides; the engine keeps the offer in rotation.
func (c *Counters) OfferConversions(ctx context.Context, offerID, zone string) (int64, error) {
	if c == nil || offerID == "" {
		return 0, nil
	}
	n, err := c.rdb.Get(ctx, c.offerCapKey(offerID, zone)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}
// offerTotalKey is the Redis key of the offer's conversion counter over all time. It has no TTL.
// offerID is the catalog id of the offer.
func offerTotalKey(offerID string) string {
	return "cap:t:" + offerID
}

// noteTotalScript adds one to a counter that exists and leaves a missing one missing: a counter
// that is not yet seeded from the stored conversions must not start from one.
var noteTotalScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return redis.call('INCR', KEYS[1]) end
return -1
`)

// NoteOfferTotal counts one lead or sale of the offer over all time.
// The caller passes only conversions written for the first time with status lead or sale.
// It returns the new count and true, or zero and false when the counter is not seeded yet; the
// seed then reads this conversion from the store. On a Redis failure it returns the error and the
// count is lost: a broken counter must not stop the postback.
func (c *Counters) NoteOfferTotal(ctx context.Context, offerID string) (int64, bool, error) {
	if c == nil || offerID == "" {
		return 0, false, nil
	}
	n, err := noteTotalScript.Run(ctx, c.rdb, []string{offerTotalKey(offerID)}).Int64()
	if err != nil {
		return 0, false, err
	}
	return n, n >= 0, nil
}

// OfferTotal returns how many leads and sales the offer has over all time.
// known is false while the counter has not been seeded; the caller then keeps the offer in rotation
// and seeds it. On a Redis failure it returns the error and the caller keeps the offer too.
func (c *Counters) OfferTotal(ctx context.Context, offerID string) (n int64, known bool, err error) {
	if c == nil || offerID == "" {
		return 0, false, nil
	}
	n, err = c.rdb.Get(ctx, offerTotalKey(offerID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	return n, err == nil, err
}

// SeedOfferTotal sets the total counter of the offer to n, the leads and sales stored so far, unless
// a counter exists already. It reports whether it wrote the counter.
func (c *Counters) SeedOfferTotal(ctx context.Context, offerID string, n int64) (bool, error) {
	if c == nil || offerID == "" || n < 0 {
		return false, nil
	}
	return c.rdb.SetNX(ctx, offerTotalKey(offerID), n, 0).Result()
}
