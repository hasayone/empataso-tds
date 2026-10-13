package router

import (
	"math/rand"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// Catalogs are the offer and landing records a destination can point at.
type Catalogs struct {
	Offers   map[string]config.Offer
	Landings map[string]config.Landing
}

// ResolveOut chooses the URL a stream serves.
// When the stream has both a landing and an offer, the first response is the
// landing; offerStep serves the offer instead. Local records are skipped.
// A stream without references keeps fallbackRedirect and fallbackOut.
func ResolveOut(s *config.Stream, cat Catalogs, offerStep bool, fallbackRedirect, fallbackOut string, roll func(n int) int) (redirect, out string) {
	if s == nil {
		return fallbackRedirect, fallbackOut
	}
	if fallbackRedirect == "group" {
		return fallbackRedirect, fallbackOut
	}
	landings := liveRefs(s.Out.Landings, func(id string) (string, bool, bool) {
		item, ok := cat.Landings[id]
		if !ok {
			return "", false, false
		}
		return item.URL, item.Status, item.Local
	})
	offers := liveRefs(s.Out.Offers, func(id string) (string, bool, bool) {
		item, ok := cat.Offers[id]
		if !ok {
			return "", false, false
		}
		return item.URL, item.Status, item.Local
	})
	if len(landings.urls) > 0 && len(offers.urls) > 0 && !offerStep {
		return serveURL(fallbackRedirect, pickURL(landings, roll))
	}
	if offerStep || (len(offers.urls) > 0 && len(landings.urls) == 0) {
		if len(offers.urls) == 0 {
			return fallbackRedirect, fallbackOut
		}
		return serveURL(fallbackRedirect, pickURL(offers, roll))
	}
	if len(landings.urls) > 0 {
		return serveURL(fallbackRedirect, pickURL(landings, roll))
	}
	return fallbackRedirect, fallbackOut
}

type urlShares struct {
	urls   []string
	shares []int
}

func liveRefs(refs []config.ShareRef, lookup func(string) (url string, status, local bool)) urlShares {
	var out urlShares
	for _, ref := range refs {
		raw, status, local := lookup(ref.ID)
		if !status || local || strings.TrimSpace(raw) == "" {
			continue
		}
		share := ref.Share
		if share < 0 {
			share = 0
		}
		out.urls = append(out.urls, raw)
		out.shares = append(out.shares, share)
	}
	return out
}

func pickURL(set urlShares, roll func(n int) int) string {
	n := len(set.urls)
	if n == 0 {
		return ""
	}
	if n == 1 {
		return set.urls[0]
	}
	total := 0
	for _, s := range set.shares {
		if s > 0 {
			total += s
		}
	}
	drawN := n
	if total > 0 {
		drawN = total
	}
	r := 0
	if roll != nil {
		r = roll(drawN)
	} else if drawN > 1 {
		r = rand.Intn(drawN)
	}
	return set.urls[PickShare(set.shares, r)]
}

// PickShare returns the index drawn by shares. A non-positive share is skipped
// when any share is positive; otherwise every entry has an equal share.
// roll is an integer in [0, total) or [0, len) for the equal case.
func PickShare(shares []int, roll int) int {
	if len(shares) == 0 {
		return 0
	}
	total := 0
	for _, s := range shares {
		if s > 0 {
			total += s
		}
	}
	if total == 0 {
		if roll < 0 {
			roll = 0
		}
		return roll % len(shares)
	}
	if roll < 0 {
		roll = 0
	}
	roll = roll % total
	acc := 0
	for i, s := range shares {
		if s <= 0 {
			continue
		}
		acc += s
		if roll < acc {
			return i
		}
	}
	return 0
}

func serveURL(redirect, raw string) (string, string) {
	if redirect == "" || redirect == "stop" {
		redirect = "http_redirect"
	}
	return redirect, raw
}
