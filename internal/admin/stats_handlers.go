package admin

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

// msgQueryTooHeavy is the error text of a statistics query stopped by its
// ClickHouse time or memory limit.
const msgQueryTooHeavy = "query too heavy"

// writeStatsErr answers a failed statistics query.
// A query stopped by its limits is 504 msgQueryTooHeavy; any other err is 500 msg.
func writeStatsErr(w http.ResponseWriter, err error, msg string) {
	if store.IsQueryTooHeavy(err) {
		writeErr(w, http.StatusGatewayTimeout, msgQueryTooHeavy)
		return
	}
	writeErr(w, http.StatusInternalServerError, msg)
}

// handleSummary writes the period totals.
// r supplies from and to. The body is a StatsSummary.
// It writes 503 when stats are not configured and 500 when the query fails.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	from, to := parseRange(r)
	var sum store.StatsSummary
	var err error
	if len(conds) > 0 {
		sum, err = s.cfg.Stats.SummaryFiltered(r.Context(), from, to, conds)
	} else {
		sum, err = s.cfg.Stats.Summary(r.Context(), from, to)
	}
	if err != nil {
		writeStatsErr(w, err, "stats error")
		return
	}
	s.writeStats(w, r, http.StatusOK, sum)
}

// handleTimeSeries writes hit counts bucketed by step.
// r supplies from, to and an optional step in seconds. The body is a list of points.
// step below 3600 is 400. Missing stats is 503. A query failure is 500.
func (s *Server) handleTimeSeries(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	from, to := parseRange(r)
	rawStep := r.URL.Query().Get("step")
	step := 3600
	if rawStep != "" {
		step = atoiDef(rawStep, 0)
		if step < 3600 {
			writeErr(w, http.StatusBadRequest, "step too small")
			return
		}
	}
	var pts []store.TSPoint
	var err error
	if len(conds) > 0 {
		pts, err = s.cfg.Stats.TimeSeriesFiltered(r.Context(), from, to, step, conds)
	} else {
		pts, err = s.cfg.Stats.TimeSeries(r.Context(), from, to, step)
	}
	if err != nil {
		writeStatsErr(w, err, "stats error")
		return
	}
	s.writeStats(w, r, http.StatusOK, pts)
}

// handleBreakdown writes the top values of one dimension.
// r supplies from, to, dim, limit and optional f= for events.
// Conditions call BreakdownFiltered. No conditions call Breakdown.
// The body is a list of key/count pairs. A bad f= and a hidden dim are 400.
// ErrUnknownField and ErrBadCond are 400. ErrFieldNotReady is 200 with an empty list.
// Any other query error is 500. Missing stats is 503.
func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	from, to := parseRange(r)
	dim := r.URL.Query().Get("dim")
	if fieldHidden(s.viewerFrom(r.Context()), dim) {
		writeErr(w, http.StatusBadRequest, "bad filter: hidden field "+dim)
		return
	}
	limit := atoiDef(r.URL.Query().Get("limit"), 50)
	var kv []store.KV
	var err error
	if len(conds) > 0 {
		kv, err = s.cfg.Stats.BreakdownFiltered(r.Context(), from, to, dim, limit, conds)
	} else {
		kv, err = s.cfg.Stats.Breakdown(r.Context(), from, to, dim, limit)
	}
	if err != nil {
		if errors.Is(err, store.ErrFieldNotReady) {
			writeJSON(w, http.StatusOK, []store.KV{})
			return
		}
		if errors.Is(err, store.ErrUnknownField) || errors.Is(err, store.ErrBadCond) {
			writeErr(w, http.StatusBadRequest, "bad dimension")
			return
		}
		writeStatsErr(w, err, "stats error")
		return
	}
	s.writeStats(w, r, http.StatusOK, kv)
}

// handlePerformance writes hits, uniques, bots, conversions and profit per group and stream.
// r supplies from and to. The body is a list of rows, empty rather than null when there are none.
// Missing stats is 503. A query failure is 500.
func (s *Server) handlePerformance(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	from, to := parseRange(r)
	var rows []store.PerfRow
	var err error
	if len(conds) > 0 {
		rows, err = s.cfg.Stats.PerformanceFiltered(r.Context(), from, to, conds)
	} else {
		rows, err = s.cfg.Stats.Performance(r.Context(), from, to)
	}
	if err != nil {
		writeStatsErr(w, err, "stats error")
		return
	}
	if rows == nil {
		rows = []store.PerfRow{}
	}
	s.writeStats(w, r, http.StatusOK, rows)
}

// logFilter reads the click-journal query.
// w and r supply the period, legacy parameters and f=. A bad filter and a filter
// by a hidden field write 400 to w.
func (s *Server) logFilter(w http.ResponseWriter, r *http.Request) (store.LogFilter, bool) {
	q := r.URL.Query()
	conds, ok := s.condsFrom(w, r, "events")
	if !ok || !s.rejectHiddenParams(w, r, q, legacyLogParams) {
		return store.LogFilter{}, false
	}
	from, to := parseRange(r)
	sort, desc, ok := s.journalSort(w, r, "events")
	if !ok {
		return store.LogFilter{}, false
	}
	return store.LogFilter{
		From: from, To: to,
		Group: csvVals(q.Get("group")), Stream: csvVals(q.Get("stream")),
		Country: csvVals(q.Get("country")), Device: csvVals(q.Get("device")),
		OS: csvVals(q.Get("os")), Browser: csvVals(q.Get("browser")), Brand: csvVals(q.Get("brand")),
		IP:         q.Get("ip"),
		BotsOnly:   q.Get("bots") == "1",
		HumansOnly: q.Get("humans") == "1",
		Limit:      clampPage(atoiDef(q.Get("limit"), defaultPageRows)),
		Offset:     max(atoiDef(q.Get("offset"), 0), 0),
		Sort:       sort,
		Desc:       desc,
		Conds:      conds,
	}, true
}

// journalSort reads sort and dir for a journal endpoint on table.
// A hidden, unknown or not-ready field and a bad dir answer 400 "bad sort".
func (s *Server) journalSort(w http.ResponseWriter, r *http.Request, table string) (sort string, desc bool, ok bool) {
	q := r.URL.Query()
	sort = strings.TrimSpace(q.Get("sort"))
	if sort != "" {
		if fieldHidden(s.viewerFrom(r.Context()), sort) {
			writeErr(w, http.StatusBadRequest, "bad sort")
			return "", false, false
		}
		if s.cfg.Stats == nil {
			writeErr(w, http.StatusBadRequest, "bad sort")
			return "", false, false
		}
		if _, err := store.SortColumnFor(s.cfg.Stats.Fields(), table, sort); err != nil {
			writeErr(w, http.StatusBadRequest, "bad sort")
			return "", false, false
		}
	}
	dir := strings.ToLower(strings.TrimSpace(q.Get("dir")))
	switch dir {
	case "", "desc":
		desc = true
	case "asc":
		desc = false
	default:
		writeErr(w, http.StatusBadRequest, "bad sort")
		return "", false, false
	}
	return sort, desc, true
}

const (
	// defaultPageRows is one page of the log table.
	defaultPageRows = 100
	// maxPageRows bounds the interactive log endpoint. It is paginated, so a
	// caller must not be able to turn one request into a full-table dump by
	// asking for a huge limit; whole-period reads go through the CSV export.
	maxPageRows = 1000
	// exportRows is how many rows the CSV export pulls for the selected period.
	// The store accepts this as-is (see store.maxLogRows).
	exportRows = 50000
)

// clampPage bounds a page size requested by the client.
// n <= 0 becomes one page. n above maxPageRows becomes maxPageRows.
// It does not fail.
func clampPage(n int) int {
	if n <= 0 {
		return defaultPageRows
	}
	if n > maxPageRows {
		return maxPageRows
	}
	return n
}

// csvVals splits a comma-separated query value into trimmed non-empty parts.
// s is the raw parameter. An empty s returns nil. It does not fail.
func csvVals(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// handleLogFilters returns the available log filter values for each dimension
// over the period (for the checkbox dropdowns in the UI).
// r supplies from and to. Missing stats yields an empty object with 200.
// ErrFieldNotReady for a dimension sets it to an empty list; any other error returns 500 "stats error".
func (s *Server) handleLogFilters(w http.ResponseWriter, r *http.Request) {
	out := map[string][]string{}
	if s.cfg.Stats == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	from, to := parseRange(r)
	vw := s.viewerFrom(r.Context())
	for _, dim := range []string{"group", "stream", "country", "device", "os", "browser", "brand"} {
		if fieldHidden(vw, dim) {
			continue
		}
		var kv []store.KV
		var err error
		if len(conds) > 0 {
			kv, err = s.cfg.Stats.BreakdownFiltered(r.Context(), from, to, dim, 1000, conds)
		} else {
			kv, err = s.cfg.Stats.Breakdown(r.Context(), from, to, dim, 1000)
		}
		if err != nil {
			if errors.Is(err, store.ErrFieldNotReady) {
				out[dim] = []string{}
				continue
			}
			writeErr(w, http.StatusInternalServerError, "stats error")
			return
		}
		vals := []string{}
		for _, x := range kv {
			if x.Key != "" && x.Key != "-" {
				vals = append(vals, x.Key)
			}
		}
		out[dim] = vals
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLogs writes one page of the click journal.
// r supplies the log filter. The body is total plus rows.
// A bad f= is 400. Missing stats is 503. A query failure is 500.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	f, ok := s.logFilter(w, r)
	if !ok {
		return
	}
	rows, total, err := s.cfg.Stats.Logs(r.Context(), f)
	if err != nil {
		writeStatsErr(w, err, "logs error")
		return
	}
	if rows == nil {
		rows = []store.LogRow{}
	}
	s.writeStats(w, r, http.StatusOK, map[string]any{"total": total, "rows": rows})
}

// handleLogsExport writes the click journal for the period as CSV.
// r supplies the same filter as the table, but the row cap is the export size and the offset is zero.
// Columns of fields hidden for the viewer are left out of the header and of every row.
// A bad f= is 400. Missing stats is 503. A query failure is 500.
func (s *Server) handleLogsExport(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	f, ok := s.logFilter(w, r)
	if !ok {
		return
	}
	f.Limit, f.Offset = exportRows, 0
	rows, _, err := s.cfg.Stats.Logs(r.Context(), f)
	if err != nil {
		writeStatsErr(w, err, "logs error")
		return
	}
	header := logExportHeader()
	keep := visibleColumns(s.viewerFrom(r.Context()), header)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=logs.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write(pickColumns(header, keep))
	if len(rows) >= exportRows {
		w.Header().Set("X-Export-Truncated", "1")
	}
	for _, x := range rows {
		_ = cw.Write(pickColumns(logExportRecord(x), keep))
	}
	cw.Flush()
}

// logExportHeader is the CSV column list.
// The first three names stay ts, group, stream. The later names are the extra click fields.
// It does not fail.
func logExportHeader() []string {
	h := []string{
		"ts", "group", "stream", "country", "city", "device", "os", "browser", "brand", "bot", "uniq", "ip", "keyword", "out",
		"region", "lang", "operator", "redirect", "referer", "useragent", "domain", "cid", "asn", "org", "timezone",
		"page", "os_version", "browser_ver", "se", "via", "origin_campaign", "origin_campaign_id", "root_cid", "campaign_id", "offer_id", "landing_id", "source_id", "network_id",
	}
	for i := 1; i <= config.MaxSubID; i++ {
		h = append(h, fmt.Sprintf("sub_id_%d", i))
	}
	return append(h, "site", "ad_campaign_id", "creative_id", "external_id", "xrw", "device_model")
}

// logExportRecord builds one CSV row in logExportHeader order.
// x is the click. Every cell is passed through csvSafe. A short subs list is padded with empty cells.
// It does not fail.
func logExportRecord(x store.LogRow) []string {
	rec := []string{
		x.TS.Format(time.RFC3339), x.Group, x.Stream, x.Country, x.City, x.Device,
		x.OS, x.Browser, x.Brand, x.Bot, strconv.Itoa(int(x.Uniq)), x.IP, x.Keyword, x.Out,
		x.Region, x.Lang, x.Operator, x.Redirect, x.Referer, x.UserAgent, x.Domain, x.CID,
		strconv.FormatUint(uint64(x.ASN), 10), x.Org, x.Timezone,
		x.Page, x.OSVersion, x.BrowserVer, x.SE, x.Via, x.OriginCampaign, x.OriginCampaignID, x.RootCID, x.CampaignID, x.OfferID, x.LandingID, x.SourceID, x.NetworkID,
	}
	for i := 0; i < config.MaxSubID; i++ {
		cell := ""
		if i < len(x.Subs) {
			cell = x.Subs[i]
		}
		rec = append(rec, cell)
	}
	rec = append(rec, x.Site, x.AdCampaignID, x.CreativeID, x.ExternalID, x.XRW, x.DeviceModel)
	for i := range rec {
		rec[i] = csvSafe(rec[i])
	}
	return rec
}

// csvSafe defuses spreadsheet formulas. Keyword, referer, User-Agent and the
// rest are visitor-controlled text, and a cell that starts with = + - or @ is
// executed by Excel and its relatives when the export is opened. A leading
// apostrophe makes it a literal and is not shown in the cell.
func csvSafe(v string) string {
	if v == "" || v == "-" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

// handleDeleteLogs removes the click journal of one group.
// r supplies group. The body is a status object.
// A missing group is 400. Missing stats is 503. A delete failure is 500.
func (s *Server) handleDeleteLogs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		writeErr(w, http.StatusBadRequest, "group required")
		return
	}
	if err := s.cfg.Stats.DeleteGroupLogs(r.Context(), group); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// sliceMeter is the optional report source that also says the visit rows were cut at store.SliceLimit.
type sliceMeter interface {
	SliceMeta(ctx context.Context, from, to time.Time, conds []store.Cond) ([]store.SliceRow, bool, error)
}

// reportMeta is the body of /api/report?meta=1: the rows and whether they stop at store.SliceLimit.
type reportMeta struct {
	Rows      []store.SliceRow `json:"rows"`
	Truncated bool             `json:"truncated"`
}

// handleReport writes the period slice.
// r supplies from, to and optional f=. When f= is present the slice is filtered.
// With limit, offset or sort the body is one sorted page of the whole period:
// {rows, total, offset, limit, sort, dir, sort_cur, totals, truncated}, see pageParams and
// store.PageSlice. A paged request may add day=1 (and tz) to cut the rows by event day, see reportZone.
// Without them, meta=1 gives {rows, truncated} instead of the bare array,
// and truncated is true when the store cut the visit groups at store.SliceLimit; a store
// without that answer reports false. Without meta the body is the array, as other clients expect.
// A bad f=, page or sort is 400. Missing stats yields an empty list with 200. A query failure is 500.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	pq, paged, ok := s.pageParams(w, r, reportSortKeys)
	if !ok {
		return
	}
	if paged {
		conds, ok := s.condsFrom(w, r, "events")
		if !ok {
			return
		}
		tz, ok := reportZone(w, r)
		if !ok {
			return
		}
		from, to := parseRange(r)
		s.writeReportPage(w, r, from, to, conds, pq, tz)
		return
	}
	meta := r.URL.Query().Get("meta") == "1"
	if s.cfg.Stats == nil {
		if meta {
			writeJSON(w, http.StatusOK, reportMeta{Rows: []store.SliceRow{}})
			return
		}
		writeJSON(w, http.StatusOK, []store.SliceRow{})
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	from, to := parseRange(r)
	var rows []store.SliceRow
	truncated := false
	var err error
	if sm, ok := s.cfg.Stats.(sliceMeter); ok && meta {
		rows, truncated, err = sm.SliceMeta(r.Context(), from, to, conds)
	} else if len(conds) > 0 {
		rows, err = s.cfg.Stats.SliceFiltered(r.Context(), from, to, conds)
	} else {
		rows, err = s.cfg.Stats.Slice(r.Context(), from, to)
	}
	if err != nil {
		writeStatsErr(w, err, "report error")
		return
	}
	if !meta {
		s.writeStats(w, r, http.StatusOK, rows)
		return
	}
	if rows == nil {
		rows = []store.SliceRow{}
	}
	s.writeStats(w, r, http.StatusOK, reportMeta{Rows: rows, Truncated: truncated})
}

// parseRange reads from/to (RFC3339) from the query; defaults to the last 24 hours.
// r is the request. Unparseable values keep the default. It does not fail.
func parseRange(r *http.Request) (time.Time, time.Time) {
	to := time.Now()
	from := to.Add(-24 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	return from, to
}
