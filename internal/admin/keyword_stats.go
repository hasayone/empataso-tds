package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hasayone/empataso-tds/internal/store"
)

// keywordSlots is how many keyword statistics queries may run at once on one Server.
// A third request is answered 429 rather than queued behind two heavy ClickHouse reads.
const keywordSlots = 2

// keywordStatser is the optional statistics source of the keyword table and its trend.
// StatsProvider is not extended: a source without it answers those routes with an empty body.
type keywordStatser interface {
	KeywordStats(ctx context.Context, from, to time.Time, conds []store.Cond, mode store.KeywordMode) (store.KeywordStats, error)
	KeywordTrend(ctx context.Context, from, to time.Time, conds []store.Cond, params store.KeywordTrendParams) (store.KeywordTrend, error)
}

// keywordQuery is a checked keyword statistics request.
// from and to are the period (at most store.KeywordStatsMaxSpan) and conds the events
// conditions with the campaign scope of the session.
type keywordQuery struct {
	from, to time.Time
	conds    []store.Cond
	mode     store.KeywordMode
}

// keywordQueryFrom checks the part of the request the table and the trend share.
// w receives 400 "bad by" for an unknown attribution, 403 when keyword is hidden or origin
// attribution is hidden, 400 "bad range" for a bad period, and the 400 or 500 of condsFrom.
// The bool is false after an answer has been written.
func (s *Server) keywordQueryFrom(w http.ResponseWriter, r *http.Request) (keywordQuery, bool) {
	v := s.viewerFrom(r.Context())
	if fieldHidden(v, "keyword") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return keywordQuery{}, false
	}
	var mode store.KeywordMode
	switch r.URL.Query().Get("by") {
	case "":
	case string(store.KeywordModeOrigin):
		if fieldHidden(v, "origin_campaign_id") {
			writeErr(w, http.StatusForbidden, "forbidden")
			return keywordQuery{}, false
		}
		mode = store.KeywordModeOrigin
	default:
		writeErr(w, http.StatusBadRequest, "bad by")
		return keywordQuery{}, false
	}
	from, to := parseRange(r)
	if !store.KeywordRangeOK(from, to) {
		writeErr(w, http.StatusBadRequest, "bad range")
		return keywordQuery{}, false
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return keywordQuery{}, false
	}
	return keywordQuery{from: from, to: to, conds: conds, mode: mode}, true
}

// acquireKeywordSlot takes one of the keywordSlots without waiting.
// When both are busy it writes 429 "busy" and returns false. Otherwise the returned
// function gives the slot back and must be called, normally by defer.
func (s *Server) acquireKeywordSlot(w http.ResponseWriter) (release func(), ok bool) {
	select {
	case s.kwSlots <- struct{}{}:
		return func() { <-s.kwSlots }, true
	default:
		writeErr(w, http.StatusTooManyRequests, "busy")
		return nil, false
	}
}

// writeKeywordErr answers a failed keyword query.
// A bad condition or range from the store is 400, a query stopped by its limits is 504
// (writeStatsErr), anything else is 500 with msg.
func writeKeywordErr(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, store.ErrUnknownField) || errors.Is(err, store.ErrBadCond) {
		writeBadFilter(w, err)
		return
	}
	writeStatsErr(w, err, msg)
}

// handleKeywordStats writes the keyword table of the period.
// r supplies from, to, f= and by; by=origin selects first-click attribution.
// The campaign is f=campaign_id:eq:<id>, the search is
// f=keyword:contains:<text>. The body is {rows, truncated, conv_ready}; rows are ordered
// by payout, then clicks, at most store.KeywordStatsLimit. The keys of hidden fields are
// removed: leads, sales and rejected go with status, profit and depsum with profit.
// With limit, offset or sort the body is one page sorted on the server,
// {rows, total, offset, limit, sort, dir, totals, truncated, conv_ready}, with the totals of
// every keyword; see pageParams and store.KeywordStatsPage.
// A hidden keyword field is 403, a bad period, filter, page or sort 400, both slots busy 429, a query
// stopped by its limits 504, any other failure 500. A source without keyword statistics
// answers 200 with an empty table.
func (s *Server) handleKeywordStats(w http.ResponseWriter, r *http.Request) {
	q, ok := s.keywordQueryFrom(w, r)
	if !ok {
		return
	}
	pq, paged, ok := s.pageParams(w, r, keywordSortKeys)
	if !ok {
		return
	}
	src, ok := s.cfg.Stats.(keywordStatser)
	if !ok {
		if paged {
			writeJSON(w, http.StatusOK, emptyKeywordPage(pq))
			return
		}
		writeJSON(w, http.StatusOK, store.KeywordStats{Rows: []store.KeywordRow{}})
		return
	}
	release, ok := s.acquireKeywordSlot(w)
	if !ok {
		return
	}
	defer release()
	if paged {
		s.writeKeywordPage(w, r, q, pq)
		return
	}
	res, err := src.KeywordStats(r.Context(), q.from, q.to, q.conds, q.mode)
	if err != nil {
		writeKeywordErr(w, err, "keyword stats error")
		return
	}
	if res.Rows == nil {
		res.Rows = []store.KeywordRow{}
	}
	s.writeStats(w, r, http.StatusOK, res)
}

// writeKeywordPage answers a paged keyword table request; the caller holds a keyword slot.
// q is the checked request and pq the page. A source without KeywordStatsPage answers an
// empty page. A failed query is written by writeKeywordErr.
func (s *Server) writeKeywordPage(w http.ResponseWriter, r *http.Request, q keywordQuery, pq store.PageQuery) {
	kp, ok := s.cfg.Stats.(keywordPager)
	if !ok {
		writeJSON(w, http.StatusOK, emptyKeywordPage(pq))
		return
	}
	res, err := kp.KeywordStatsPage(r.Context(), q.from, q.to, q.conds, q.mode, pq)
	if err != nil {
		writeKeywordErr(w, err, "keyword stats error")
		return
	}
	s.writeStats(w, r, http.StatusOK, res)
}

// emptyKeywordPage is the paged answer of a source without keyword statistics. It does not fail.
func emptyKeywordPage(pq store.PageQuery) store.KeywordPage {
	sort, dir := pq.Sort, pq.Dir()
	if sort == "" {
		sort, dir = store.KeywordDefaultSort, "desc"
	}
	return store.KeywordPage{Rows: []store.KeywordRow{}, Offset: pq.Offset, Limit: pq.Limit, Sort: sort, Dir: dir}
}

// handleKeywordTrend writes the day-by-day course of the top keywords of the table.
// r supplies the period, attribution and filters of handleKeywordStats, top (1 to
// store.KeywordTrendMaxTop, default the maximum) and tz, an IANA zone name (default UTC).
// The keywords are the first rows of the table in its order, so no keys travel in the URL.
// The body is {days, series, conv_ready}; days has every day of the period, and a series
// has hits, conv and profit with one number per day. Without conv_ready the conv and profit
// arrays are left out. Hidden profit removes the profit arrays. A top outside the range or
// not a number, and an unknown zone, are 400; the other codes are those of handleKeywordStats.
func (s *Server) handleKeywordTrend(w http.ResponseWriter, r *http.Request) {
	q, ok := s.keywordQueryFrom(w, r)
	if !ok {
		return
	}
	top := store.KeywordTrendMaxTop
	if raw := r.URL.Query().Get("top"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.KeywordTrendMaxTop {
			writeErr(w, http.StatusBadRequest, "bad top")
			return
		}
		top = n
	}
	tz := r.URL.Query().Get("tz")
	if _, err := store.KeywordZone(tz); err != nil {
		writeErr(w, http.StatusBadRequest, "bad tz")
		return
	}
	src, ok := s.cfg.Stats.(keywordStatser)
	if !ok {
		writeJSON(w, http.StatusOK, store.KeywordTrend{Days: []string{}, Series: []store.KeywordSeries{}})
		return
	}
	release, ok := s.acquireKeywordSlot(w)
	if !ok {
		return
	}
	defer release()
	res, err := src.KeywordTrend(r.Context(), q.from, q.to, q.conds, store.KeywordTrendParams{Top: top, TZ: tz, Mode: q.mode})
	if err != nil {
		writeKeywordErr(w, err, "keyword trend error")
		return
	}
	if res.Days == nil {
		res.Days = []string{}
	}
	if res.Series == nil {
		res.Series = []store.KeywordSeries{}
	}
	s.writeStats(w, r, http.StatusOK, res)
}
