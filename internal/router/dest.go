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
	// OfferFull reports which conversion limits of the offer are used up. An offer with one
	// used up leaves the rotation. A nil function means no offer is full.
	OfferFull func(id string) config.CapHit `json:"-"`
	// Country is the country of the click as the engine holds it: two letters or
	// the "no data" dash. Empty means the country is not known to the caller and
	// no offer is cut by its countries.
	Country string `json:"-"`
}

// Hit returns the conversion limits of the offer id that are used up, zero when none is.
// id is the offer catalog id. A catalog without OfferFull has no full offer.
func (c Catalogs) Hit(id string) config.CapHit {
	if c.OfferFull == nil {
		return 0
	}
	return c.OfferFull(id)
}

// Full reports whether the offer id has used up a conversion limit, the daily or the total one.
func (c Catalogs) Full(id string) bool {
	return c.Hit(id) != 0
}

// Excluded reports whether the offer id leaves the rotation for this click:
// a conversion limit is used up, or it has countries and the click country is not among them.
// id is the offer catalog id. An id missing from the catalog is not excluded here.
func (c Catalogs) Excluded(id string) bool {
	if c.Full(id) {
		return true
	}
	if c.Country == "" {
		return false
	}
	o, ok := c.Offers[id]
	return ok && !o.AllowsCountry(c.Country)
}

// Serve names the offer that takes a click meant for the offer id.
// An offer that is on and has used up no limit takes the click itself. A stopped offer, and an
// offer whose daily or total limit is used up, hands the click to the same alternative
// (Offer.FallbackID, returned by CapRedirect for a used-up limit) and so on along the chain.
// An id met twice ends the chain, so a loop of offers never repeats. The chain also ends
// at an id missing from the catalog, at an empty alternative and at an offer that is on but cannot
// take this click: a local one without a ready action, one without an address and without a ready
// action, or one whose countries leave out the click country. A ready action (404, text, HTML or
// nothing) answers without an address. The country cut does not send the click to the alternative.
// The result is the serving offer id and true, or "" and false when no offer of the chain is left.
func (c Catalogs) Serve(id string) (string, bool) {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		o, ok := c.Offers[id]
		if !ok {
			return "", false
		}
		if !o.Status {
			id = strings.TrimSpace(o.FallbackID)
			continue
		}
		if hit := c.Hit(id); hit != 0 {
			id = o.CapRedirect(hit)
			continue
		}
		if c.Excluded(id) {
			return "", false
		}
		if o.ActionReady() {
			return id, true
		}
		if o.Local || strings.TrimSpace(o.URL) == "" {
			return "", false
		}
		return id, true
	}
	return "", false
}

// ResolveOut chooses where a stream sends the visitor.
// When the stream has both a landing and an offer, the first response is the
// landing; offerStep serves the offer instead.
// A local landing, or a stream whose redirect is "landing", returns redirect
// "landing" and the landing id. A remote landing returns its URL.
// Each offer of the stream takes its share through Catalogs.Serve: a stopped or full offer sends its
// share to the spare offer, and an offer with no live offer left in its chain is skipped, as are local
// offers without a ready action and offers whose countries leave out the click country.
// A ready action replaces the stream redirect. A stream without references keeps
// fallbackRedirect and fallbackOut.
func ResolveOut(s *config.Stream, cat Catalogs, offerStep bool, fallbackRedirect, fallbackOut string, roll func(n int) int) (redirect, out string) {
	served := ResolveServed(s, cat, offerStep, fallbackRedirect, fallbackOut, roll)
	return served.Redirect, served.Out
}

// Served is the destination a stream serves and the catalog id it came from.
// OfferID is set when an offer was chosen. LandingID is set when a landing was chosen.
type Served struct {
	Redirect  string
	Out       string
	OfferID   string
	LandingID string
}

// ResolveServed chooses the destination and the catalog id of the offer or landing.
// s is the stream, cat the catalogs, offerStep true on the offer step of a landing.
// fallbackRedirect and fallbackOut are kept when the stream names nothing that can be served.
// roll draws the share, or nil uses math/rand. It does not fail.
func ResolveServed(s *config.Stream, cat Catalogs, offerStep bool, fallbackRedirect, fallbackOut string, roll func(n int) int) Served {
	if s == nil {
		return Served{Redirect: fallbackRedirect, Out: fallbackOut}
	}
	if fallbackRedirect == "group" {
		return Served{Redirect: fallbackRedirect, Out: fallbackOut}
	}
	landings := landingChoices(s.Out.Landings, cat)
	offers := offerChoices(s.Out.Offers, cat)
	if len(landings) > 0 && len(offers) > 0 && !offerStep {
		return servedLanding(fallbackRedirect, s.Out.Redirect, pickLanding(landings, roll))
	}
	if offerStep || (len(offers) > 0 && len(landings) == 0) {
		if len(offers) == 0 {
			return Served{Redirect: fallbackRedirect, Out: fallbackOut}
		}
		return servedOffer(fallbackRedirect, pickOffer(offers, roll))
	}
	if len(landings) > 0 {
		return servedLanding(fallbackRedirect, s.Out.Redirect, pickLanding(landings, roll))
	}
	return Served{Redirect: fallbackRedirect, Out: fallbackOut}
}

// offerChoice is one offer a stream may serve.
// id is the offer that takes the click. redirect is empty when out is a URL and the
// stream redirect is kept. share is the stream weight.
type offerChoice struct {
	id       string
	redirect string
	out      string
	share    int
}

// offerChoices lists offers the stream can serve.
// refs are the stream shares. cat holds the catalog. A stopped or full offer is
// replaced through Catalogs.Serve. An offer that cannot answer is left out.
// It does not fail.
func offerChoices(refs []config.ShareRef, cat Catalogs) []offerChoice {
	var out []offerChoice
	for _, ref := range refs {
		served, ok := cat.Serve(ref.ID)
		if !ok {
			continue
		}
		kind, body, ok := cat.Offers[served].ClickAnswer()
		if !ok {
			continue
		}
		share := ref.Share
		if share < 0 {
			share = 0
		}
		out = append(out, offerChoice{id: served, redirect: kind, out: body, share: share})
	}
	return out
}

// pickOffer draws one offer with the same weights as pickURL.
// set is the candidates. roll draws an integer, or nil uses math/rand.
// The result is the chosen record. An empty set returns the zero value.
func pickOffer(set []offerChoice, roll func(n int) int) offerChoice {
	n := len(set)
	if n == 0 {
		return offerChoice{}
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

// servedOffer returns the render type and the offer id.
// fallback is the stream redirect, used when the offer is a URL.
// A ready action keeps its own type. It does not fail.
func servedOffer(fallback string, item offerChoice) Served {
	if item.redirect != "" {
		return Served{Redirect: item.redirect, Out: item.out, OfferID: item.id}
	}
	redirect, out := serveURL(fallback, item.out)
	return Served{Redirect: redirect, Out: out, OfferID: item.id}
}

// servedLanding returns the render type and the landing id.
// fallback and streamRedirect are the same values serveLanding takes.
// item is the chosen landing. It does not fail.
func servedLanding(fallback, streamRedirect string, item landingChoice) Served {
	redirect, out := serveLanding(fallback, streamRedirect, item)
	return Served{Redirect: redirect, Out: out, LandingID: item.id}
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
