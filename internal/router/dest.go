package router

import (
	"math/rand"
	"strconv"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// Catalogs are the offer and landing records a destination can point at.
type Catalogs struct {
	Offers   map[string]config.Offer
	Landings map[string]config.Landing
}

// ResolveOut chooses where a stream sends the visitor.
// When the stream has both a landing and an offer, the first response is the
// landing; offerStep serves the offer instead.
// A local landing, or a stream whose redirect is "landing", returns redirect
// "landing" and the landing id. A remote landing returns its URL.
// Local offers are skipped. A stream without references keeps
// fallbackRedirect and fallbackOut.
func ResolveOut(s *config.Stream, cat Catalogs, offerStep bool, fallbackRedirect, fallbackOut string, roll func(n int) int) (redirect, out string) {
	if s == nil {
		return fallbackRedirect, fallbackOut
	}
	if fallbackRedirect == "group" {
		return fallbackRedirect, fallbackOut
	}
	landings := landingChoices(s.Out.Landings, cat)
	offers := liveRefs(s.Out.Offers, func(id string) (string, bool, bool) {
		item, ok := cat.Offers[id]
		if !ok {
			return "", false, false
		}
		return item.URL, item.Status, item.Local
	})
	if len(landings) > 0 && len(offers.urls) > 0 && !offerStep {
		return serveLanding(fallbackRedirect, s.Out.Redirect, pickLanding(landings, roll))
	}
	if offerStep || (len(offers.urls) > 0 && len(landings) == 0) {
		if len(offers.urls) == 0 {
			return fallbackRedirect, fallbackOut
		}
		return serveURL(fallbackRedirect, pickURL(offers, roll))
	}
	if len(landings) > 0 {
		return serveLanding(fallbackRedirect, s.Out.Redirect, pickLanding(landings, roll))
	}
	return fallbackRedirect, fallbackOut
}

// landingChoice is one active landing a stream may serve.
// id is the catalog id. url is the remote address and is empty for a local
// landing. local reports that the engine serves the directory itself.
type landingChoice struct {
	id    string
	url   string
	local bool
	share int
}

// landingChoices lists active landings. Local records stay in the list.
// refs are the stream shares. cat holds the catalog. Disabled records and
// remote records with an empty URL are left out. There is no error return.
func landingChoices(refs []config.ShareRef, cat Catalogs) []landingChoice {
	var out []landingChoice
	for _, ref := range refs {
		item, ok := cat.Landings[ref.ID]
		if !ok || !item.Status {
			continue
		}
		share := ref.Share
		if share < 0 {
			share = 0
		}
		if item.Local {
			out = append(out, landingChoice{id: ref.ID, local: true, share: share})
			continue
		}
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		out = append(out, landingChoice{id: ref.ID, url: item.URL, share: share})
	}
	return out
}

// pickLanding draws one landing with the same weights as pickURL.
// set is the candidates. roll draws an integer, or nil uses math/rand.
// The result is the chosen record. An empty set returns the zero value.
func pickLanding(set []landingChoice, roll func(n int) int) landingChoice {
	n := len(set)
	if n == 0 {
		return landingChoice{}
	}
	shares := make([]int, n)
	urls := make([]string, n)
	for i, item := range set {
		shares[i] = item.share
		urls[i] = strconv.Itoa(i)
	}
	i, err := strconv.Atoi(pickURL(urlShares{urls: urls, shares: shares}, roll))
	if err != nil || i < 0 || i >= n {
		return set[0]
	}
	return set[i]
}

// serveLanding returns the redirect and the value the handler serves.
// fallback is the stream or group redirect. streamRedirect is Out.Redirect.
// item is the chosen landing. A local landing, or redirect "landing", yields
// ("landing", id). A remote landing keeps today's URL behaviour.
func serveLanding(fallback, streamRedirect string, item landingChoice) (string, string) {
	if item.local || streamRedirect == "landing" || fallback == "landing" {
		return "landing", item.id
	}
	return serveURL(fallback, item.url)
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

// ApplyOutShares picks one ||| part by OutShares.
// ok is false when the string has a single part, the weights are missing,
// or their positive sum is zero. The caller then keeps its own distribution.
func ApplyOutShares(raw string, shares []int, roll func(n int) int) (string, bool) {
	if !strings.Contains(raw, "|||") {
		return raw, false
	}
	var parts []string
	for _, p := range strings.Split(raw, "|||") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 || len(shares) < len(parts) {
		return raw, false
	}
	sh := shares[:len(parts)]
	total := 0
	for _, s := range sh {
		if s > 0 {
			total += s
		}
	}
	if total <= 0 {
		return raw, false
	}
	r := 0
	if roll != nil {
		r = roll(total)
	} else if total > 1 {
		r = rand.Intn(total)
	}
	return parts[PickShare(sh, r)], true
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
