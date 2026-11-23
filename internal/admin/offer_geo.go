package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/store"
)

// offerCountryStats is the facts source that can split one offer by its countries.
// *store.CH implements it. Catalog facts stay on StatsProvider.
type offerCountryStats interface {
	OfferCountryFacts(ctx context.Context, from, to time.Time, offerID string, countries []string, scope []store.Cond) (store.Facts, error)
}

// parseOfferGeo reads the optional per-offer country split of GET /api/stats/facts.
// q is the request query. geo is false when offer and countries are both absent,
// and the caller keeps the catalog facts. When either is present both are required.
// countries is the lowercase list in first-seen order; an empty value is an empty
// list, which the handler answers without a query. errMsg is a client error when
// the pair is incomplete, a code is not two letters or unknown, or the list is
// longer than the store allows. It does not write a response.
func parseOfferGeo(q url.Values) (geo bool, offerID string, countries []string, errMsg string) {
	_, hasOffer := q["offer"]
	_, hasCountries := q["countries"]
	if !hasOffer && !hasCountries {
		return false, "", nil, ""
	}
	offerID = strings.TrimSpace(q.Get("offer"))
	if offerID == "" {
		return true, "", nil, "offer is required"
	}
	if len(offerID) > 128 {
		return true, "", nil, "offer is too long"
	}
	if !hasCountries {
		return true, offerID, nil, "countries is required"
	}
	raw := strings.TrimSpace(q.Get("countries"))
	if raw == "" {
		return true, offerID, []string{}, ""
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		code := strings.ToLower(strings.TrimSpace(part))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		countries = append(countries, code)
	}
	if len(countries) > store.MaxOfferGeoCountries {
		return true, offerID, nil, "too many countries"
	}
	if msg := validOfferCountries(countries); msg != "" {
		return true, offerID, nil, msg
	}
	if countries == nil {
		countries = []string{}
	}
	return true, offerID, countries, ""
}

// writeOfferGeoFacts writes the click and conversion facts of one offer split by countries.
// An empty countries list is an empty answer and does not query. A hidden country
// field is 400. Missing statistics and a source that cannot split answer an empty body.
// A bad scope is 400. A query stopped as too heavy is 504. Any other query error is 500.
// Status rows are folded when the viewer may not see the conversion status.
func (s *Server) writeOfferGeoFacts(w http.ResponseWriter, r *http.Request, from, to time.Time, offerID string, countries []string, scope []store.Cond) {
	if fieldHidden(s.viewerFrom(r.Context()), "country") {
		writeErr(w, http.StatusBadRequest, "bad filter: hidden field country")
		return
	}
	empty := store.Facts{Clicks: []store.ClickFact{}, Status: []store.StatusFact{}}
	if len(countries) == 0 || s.cfg.Stats == nil {
		s.writeStats(w, r, http.StatusOK, empty)
		return
	}
	src, ok := s.cfg.Stats.(offerCountryStats)
	if !ok {
		s.writeStats(w, r, http.StatusOK, empty)
		return
	}
	facts, err := src.OfferCountryFacts(r.Context(), from, to, offerID, countries, scope)
	if err != nil {
		if errors.Is(err, store.ErrBadCond) || errors.Is(err, store.ErrUnknownField) {
			writeErr(w, http.StatusBadRequest, "bad filter")
			return
		}
		writeStatsErr(w, err, "stats error")
		return
	}
	if facts.Clicks == nil {
		facts.Clicks = []store.ClickFact{}
	}
	if facts.Status == nil {
		facts.Status = []store.StatusFact{}
	}
	if fieldHidden(s.viewerFrom(r.Context()), "status") {
		facts.Status = foldConfirmed(facts.Status)
		s.writeStatsWith(w, r, http.StatusOK, facts, statsOpts{keep: map[string]bool{"status": true}})
		return
	}
	s.writeStats(w, r, http.StatusOK, facts)
}
