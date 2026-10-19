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

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
)

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
		writeErr(w, http.StatusInternalServerError, "stats error")
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
		writeErr(w, http.StatusInternalServerError, "stats error")
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
		writeErr(w, http.StatusInternalServerError, "stats error")
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
		writeErr(w, http.StatusInternalServerError, "stats error")
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
		Conds:      conds,
	}, true
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
// r supplies from and to. A dimension that fails is omitted. Missing stats yields an empty object.
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
			continue
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
		writeErr(w, http.StatusInternalServerError, "logs error")
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
		writeErr(w, http.StatusInternalServerError, "logs error")
		return
	}
	header := logExportHeader()
	keep := visibleColumns(s.viewerFrom(r.Context()), header)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=logs.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write(pickColumns(header, keep))
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
		"page", "os_version", "browser_ver", "se", "via", "campaign_id", "offer_id", "landing_id", "source_id", "network_id",
	}
	for i := 1; i <= 12; i++ {
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
		x.Page, x.OSVersion, x.BrowserVer, x.SE, x.Via, x.CampaignID, x.OfferID, x.LandingID, x.SourceID, x.NetworkID,
	}
	for i := 0; i < 12; i++ {
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

// handlePostbackLog writes the raw postback intake log.
// r supplies from, to and limit. Missing stats yields an empty list with 200.
// A query failure is 500.
func (s *Server) handlePostbackLog(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeJSON(w, http.StatusOK, []store.PostbackLogRow{})
		return
	}
	from, to := parseRange(r)
	rows, err := s.cfg.Stats.PostbackLog(r.Context(), from, to, atoiDef(r.URL.Query().Get("limit"), 200))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "postback log error")
		return
	}
	writeJSON(w, http.StatusOK, rows)
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
// With meta=1 the body is {rows, truncated} instead of the bare array, and truncated is
// true when the store cut the visit groups at store.SliceLimit; a store without that
// answer reports false. Without meta the body is the array, as other clients expect.
// A bad f= is 400. Missing stats yields an empty list with 200. A query failure is 500.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusInternalServerError, "report error")
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

// handlePostbacks writes one page of the conversion journal, the profit sum and the match count.
// r supplies the old parameters, repeated f= and offset. Offset below zero becomes zero.
// total is the profit. count is the number of matching rows. A bad f= is 400.
// Missing stats is 503. A query failure is 500.
func (s *Server) handlePostbacks(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	conds, ok := s.condsFrom(w, r, "conversions")
	q := r.URL.Query()
	if !ok || !s.rejectHiddenParams(w, r, q, legacyPostbackParams) {
		return
	}
	from, to := parseRange(r)
	rows, tot, err := s.cfg.Stats.QueryPostbacks(r.Context(), store.PostbackQuery{
		From: from, To: to, Group: q.Get("group"), Status: q.Get("status"),
		Stream: q.Get("stream"), Offer: q.Get("offer"), Landing: q.Get("landing"),
		Source: q.Get("source"), Network: q.Get("network"), CID: q.Get("cid"),
		Limit:  atoiDef(q.Get("limit"), 200),
		Offset: max(atoiDef(q.Get("offset"), 0), 0),
		Conds:  conds,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "postbacks error")
		return
	}
	if rows == nil {
		rows = []store.PostbackRow{}
	}
	if err := s.fillCampaignNames(r.Context(), rows); err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	s.writeStatsExtra(w, r, http.StatusOK, map[string]any{"total": tot.Profit, "count": tot.Count, "rows": rows},
		map[string][]string{"profit": {"total"}})
}

// fillCampaignNames sets the campaign name of journal rows that have a campaign id and no name.
// ctx carries the viewer. rows is changed in place. The name is the stored campaign name,
// or its id when the name is empty. A campaign the viewer may not see is left unnamed.
// The groups list is read only when a row needs a name; a missing store changes nothing.
// It runs before the hidden fields are stripped, so a hidden campaign still drops the name.
// It returns an error only when the groups list cannot be read.
func (s *Server) fillCampaignNames(ctx context.Context, rows []store.PostbackRow) error {
	need := false
	for _, p := range rows {
		if p.CampaignID != "" && p.Group == "" {
			need = true
			break
		}
	}
	if !need || s.cfg.Groups == nil {
		return nil
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return err
	}
	v := s.viewerFrom(ctx)
	byID := make(map[string]config.Group, len(groups))
	for _, g := range groups {
		if _, ok := byID[g.ID]; !ok && v.canSee(resCampaigns, campaignRef(g)) {
			byID[g.ID] = g
		}
	}
	for i := range rows {
		if rows[i].CampaignID == "" || rows[i].Group != "" {
			continue
		}
		if g, ok := byID[rows[i].CampaignID]; ok {
			rows[i].Group = campaignName(g)
		}
	}
	return nil
}

// campaignName is the display name of a campaign: its name, or its id when the name is empty.
func campaignName(g config.Group) string {
	if g.Name != "" {
		return g.Name
	}
	return g.ID
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
