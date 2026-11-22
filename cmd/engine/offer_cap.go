package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

const (
	// offerCapTimeout bounds the Redis write that counts one conversion.
	offerCapTimeout = time.Second
	// offerSeedTimeout bounds the store count that seeds a total counter.
	offerSeedTimeout = 20 * time.Second
	// offerSeedRetry is how long the engine waits before it seeds the counter of the same offer
	// again, whether the last try is still running or failed.
	offerSeedRetry = time.Minute
)

// totalSeeds remembers, per offer, when a total counter may be seeded again. mu guards next.
type totalSeeds struct {
	mu   sync.Mutex
	next map[string]time.Time
}

// claim reports whether the caller may seed the counter of offer id now, and blocks the next try
// for offerSeedRetry. now is the current time.
func (s *totalSeeds) claim(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until, ok := s.next[id]; ok && now.Before(until) {
		return false
	}
	if s.next == nil {
		s.next = map[string]time.Time{}
	}
	s.next[id] = now.Add(offerSeedRetry)
	return true
}

// offerFull builds the check the router uses to leave full offers out of the rotation.
// ctx bounds the Redis reads. offers is the published offer map of this request.
// The returned function takes an offer id and reports which of its conversion limits are
// used up: the daily one when the leads and sales of the current day in the offer's zone reached its number,
// the total one when the leads and sales over all time did. Each offer is read at most once
// per request. A missing counter, an offer without a limit and a Redis failure all report
// nothing, so a broken counter never empties a rotation. A total counter that does not exist
// yet is seeded from the store in the background and reports nothing until it does.
// It returns nil when there are no counters.
func (d *engineDeps) offerFull(ctx context.Context, offers map[string]config.Offer) func(string) config.CapHit {
	if d == nil || d.counters == nil {
		return nil
	}
	seen := map[string]config.CapHit{}
	return func(id string) config.CapHit {
		o := offers[id]
		dayLimit, dayOn := o.CapLimit()
		totalLimit, totalOn := o.TotalLimit()
		if !dayOn && !totalOn {
			return 0
		}
		if hit, ok := seen[id]; ok {
			return hit
		}
		var hit config.CapHit
		if dayOn {
			if n, err := d.counters.OfferConversions(ctx, id, o.CapTZ); err == nil && n >= int64(dayLimit) {
				hit |= config.CapDaily
			}
		}
		if totalOn {
			n, known, err := d.counters.OfferTotal(ctx, id)
			switch {
			case err != nil:
			case !known:
				d.seedOfferTotal(id)
			case n >= int64(totalLimit):
				hit |= config.CapTotal
			}
		}
		seen[id] = hit
		return hit
	}
}

// seedOfferTotal starts, once a minute at most for each offer, the background count of the leads
// and sales the store holds for the offer, and sets the total counter to it. id is the offer id.
// Without a store, or when the count fails, the counter stays missing and the offer stays in the
// rotation until a later click tries again. Failures are logged.
func (d *engineDeps) seedOfferTotal(id string) {
	count := d.totalCount
	if count == nil {
		ch := d.clickhouse()
		if ch == nil {
			return
		}
		count = ch.OfferConversionTotal
	}
	if !d.totalSeeds.claim(id, time.Now()) {
		return
	}
	d.goBG(func() {
		ctx, cancel := context.WithTimeout(context.Background(), offerSeedTimeout)
		defer cancel()
		n, err := count(ctx, id)
		if err == nil {
			_, err = d.counters.SeedOfferTotal(ctx, id, n)
		}
		if err != nil && d.log != nil {
			d.log.Warn("offer total counter seed failed", "offer", id, "err", err)
		}
	})
}

// noteOfferCap counts a stored conversion toward the limits of its offer.
// res is what the store reported and status is the postback status. Only the
// first arrival of a lead or sale for the click counts: a repeated postback,
// a rejection and a click without an offer add nothing. Every offer is counted
// by day, so a limit switched on during the day sees the conversions made before it.
// The total counter of an offer counts only once it has been seeded from the store.
// A Redis failure is logged and ignored.
func (d *engineDeps) noteOfferCap(ctx context.Context, res store.ConversionResult, status string) {
	if d == nil || d.counters == nil || d.offers == nil || !res.Fresh || res.OfferID == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "lead", "sale":
	default:
		return
	}
	if _, ok := d.offers.Get(res.OfferID); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, offerCapTimeout)
	defer cancel()
	zone := ""
	if o, ok := d.offers.Get(res.OfferID); ok {
		zone = o.CapTZ
	}
	if _, err := d.counters.NoteOfferConversion(ctx, res.OfferID, zone); err != nil && d.log != nil {
		d.log.Warn("offer cap counter failed", "offer", res.OfferID, "err", err)
	}
	if _, _, err := d.counters.NoteOfferTotal(ctx, res.OfferID); err != nil && d.log != nil {
		d.log.Warn("offer total counter failed", "offer", res.OfferID, "err", err)
	}
}
