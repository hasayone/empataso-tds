package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/store"
)

// reportSortKeys maps each sort key of GET /api/report to the answer keys its value reads.
// A key whose answer key is hidden for the viewer cannot be sorted on.
var reportSortKeys = map[string][]string{
	"day": {"day"}, "group": {"group"}, "stream": {"stream"}, "country": {"country"}, "offer": {"offer"}, "source": {"source"},
	"hits": {"hits"}, "uniq": {"unique"}, "bots": {"bots"}, "conv": {"conv"}, "cr": {"conv", "hits"},
	"profit": {"profit"}, "depsum": {"depsum"}, "cost": {"cost"},
}

// keywordSortKeys maps each sort key of GET /api/keywords/stats to the answer keys its value reads.
var keywordSortKeys = map[string][]string{
	"keyword": {"keyword"}, "hits": {"hits"}, "uniq": {"unique"}, "conv": {"conv"}, "leads": {"leads"},
	"sales": {"sales"}, "rej": {"rejected"}, "cr": {"conv", "hits"}, "revenue": {"profit"}, "depsum": {"depsum"},
}

// pageParams reads limit, offset, sort and dir of a table paged on the server.
// keys maps each sort key the table knows to the answer keys it reads.
// paged is false when the request names none of limit, offset and sort: the caller keeps
// the answer it gave before paging existed. limit defaults to store.PageDefaultRows and must be
// 1..store.PageMaxRows; offset defaults to 0 and must not be negative; dir is asc or desc
// (default desc). A bad number writes 400 "bad page"; an unknown sort, a sort on a field hidden
// for the viewer or a bad dir writes 400 "bad sort". ok is false after an answer was written.
func (s *Server) pageParams(w http.ResponseWriter, r *http.Request, keys map[string][]string) (q store.PageQuery, paged, ok bool) {
	v := r.URL.Query()
	if !v.Has("limit") && !v.Has("offset") && !v.Has("sort") {
		return store.PageQuery{}, false, true
	}
	q.Limit, q.Desc = store.PageDefaultRows, true
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.PageMaxRows {
			writeErr(w, http.StatusBadRequest, "bad page")
			return q, true, false
		}
		q.Limit = n
	}
	if raw := v.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "bad page")
			return q, true, false
		}
		q.Offset = n
	}
	switch strings.ToLower(strings.TrimSpace(v.Get("dir"))) {
	case "", "desc":
	case "asc":
		q.Desc = false
	default:
		writeErr(w, http.StatusBadRequest, "bad sort")
		return q, true, false
	}
	q.Sort = strings.TrimSpace(v.Get("sort"))
	if q.Sort == "" {
		return q, true, true
	}
	reads, known := keys[q.Sort]
	hidden := statsStripKeys(s.viewerFrom(r.Context()), nil)
	for _, k := range reads {
		known = known && !hidden[k]
	}
	if !known {
		writeErr(w, http.StatusBadRequest, "bad sort")
		return q, true, false
	}
	return q, true, true
}

// slicePager is the optional report source that reads the whole period for paging.
type slicePager interface {
	SliceAll(ctx context.Context, from, to time.Time, conds []store.Cond) ([]store.SliceRow, store.SliceTotals, bool, error)
}

// sliceDayPager is the optional report source that also cuts the cells by event day in a zone.
type sliceDayPager interface {
	SliceAllDays(ctx context.Context, from, to time.Time, conds []store.Cond, tz string) ([]store.SliceRow, store.SliceTotals, bool, error)
}

// reportZone reads day and tz of GET /api/report.
// day=1 asks for rows cut by event day; tz is the IANA zone name that cuts the days, UTC when empty.
// It returns the zone name when the days are asked and "" when they are not. A zone that
// store.KeywordZone refuses writes 400 "bad tz" and ok is false.
func reportZone(w http.ResponseWriter, r *http.Request) (tz string, ok bool) {
	v := r.URL.Query()
	if raw := strings.ToLower(v.Get("day")); raw != "1" && raw != "true" {
		return "", true
	}
	tz = v.Get("tz")
	if _, err := store.KeywordZone(tz); err != nil {
		writeErr(w, http.StatusBadRequest, "bad tz")
		return "", false
	}
	if tz == "" {
		tz = "UTC"
	}
	return tz, true
}

// keywordPager is the optional keyword source that pages the table on the server.
type keywordPager interface {
	KeywordStatsPage(ctx context.Context, from, to time.Time, conds []store.Cond, mode store.KeywordMode, q store.PageQuery) (store.KeywordPage, error)
}

// writeReportPage answers GET /api/report with one sorted page and the totals of every row.
// from, to and conds are the checked request and q the page. tz is the zone of the day
// grouping, empty for rows that are not cut by day; a source that cannot cut by day answers
// without it. A source without SliceAll pages the rows of SliceFiltered and sums them.
// Missing stats is an empty page. A query failure is written by writeStatsErr.
func (s *Server) writeReportPage(w http.ResponseWriter, r *http.Request, from, to time.Time, conds []store.Cond, q store.PageQuery, tz string) {
	if s.cfg.Stats == nil {
		writeJSON(w, http.StatusOK, store.PageSlice(nil, store.SliceTotals{}, false, q, nil))
		return
	}
	var rows []store.SliceRow
	var tot store.SliceTotals
	var truncated bool
	var err error
	if dp, ok := s.cfg.Stats.(sliceDayPager); ok && tz != "" {
		rows, tot, truncated, err = dp.SliceAllDays(r.Context(), from, to, conds, tz)
	} else if sp, ok := s.cfg.Stats.(slicePager); ok {
		rows, tot, truncated, err = sp.SliceAll(r.Context(), from, to, conds)
	} else if rows, err = s.cfg.Stats.SliceFiltered(r.Context(), from, to, conds); err == nil {
		tot = store.SumSlice(rows)
	}
	if err != nil {
		writeStatsErr(w, err, "report error")
		return
	}
	var label store.SliceLabel
	if q.Sort == "offer" || q.Sort == "source" {
		label = s.entityLabels(r.Context(), q.Sort)
	}
	s.writeStats(w, r, http.StatusOK, store.PageSlice(rows, tot, truncated, q, label))
}

// entityLabels returns the namer of offer or source ids for sorting the report.
// field is "offer" or "source". An id without a stored name, or a list that cannot be read,
// is compared as the id itself. It does not fail.
func (s *Server) entityLabels(ctx context.Context, field string) store.SliceLabel {
	names := map[string]string{}
	switch field {
	case "offer":
		if list, err := s.offerList(ctx); err == nil {
			for _, o := range list {
				names[o.ID] = o.Name
			}
		}
	case "source":
		if s.cfg.Sources != nil {
			if list, err := s.cfg.Sources.List(ctx); err == nil {
				for _, src := range list {
					names[src.ID] = src.Name
				}
			}
		}
	}
	return func(_, id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
}
