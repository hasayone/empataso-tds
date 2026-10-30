package admin

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
)

// handlePostbackLog writes the raw postback intake log.
// r supplies from, to and limit. Missing stats yields an empty list with 200.
// A query failure is 500.
func (s *Server) handlePostbackLog(w http.ResponseWriter, r *http.Request) {
	v := s.viewerFrom(r.Context())
	if !v.boardsAll {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
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

// postbackFilter reads the conversion-journal query shared by the table and CSV export.
func (s *Server) postbackFilter(w http.ResponseWriter, r *http.Request) (store.PostbackQuery, bool) {
	conds, ok := s.condsFrom(w, r, "conversions")
	q := r.URL.Query()
	if !ok || !s.rejectHiddenParams(w, r, q, legacyPostbackParams) {
		return store.PostbackQuery{}, false
	}
	sort, desc, ok := s.journalSort(w, r, "conversions")
	if !ok {
		return store.PostbackQuery{}, false
	}
	from, to := parseRange(r)
	return store.PostbackQuery{
		From: from, To: to, Group: q.Get("group"), Status: q.Get("status"),
		Stream: q.Get("stream"), Offer: q.Get("offer"), Landing: q.Get("landing"),
		Source: q.Get("source"), Network: q.Get("network"), CID: q.Get("cid"),
		Sort: sort, Desc: desc,
		Conds: conds,
	}, true
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
	pq, ok := s.postbackFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	pq.Limit = atoiDef(q.Get("limit"), 200)
	pq.Offset = max(atoiDef(q.Get("offset"), 0), 0)
	rows, tot, err := s.cfg.Stats.QueryPostbacks(r.Context(), pq)
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

// handlePostbacksExport writes the conversion journal for the period as CSV.
// r supplies the same filters as the table; limit and offset are ignored.
func (s *Server) handlePostbacksExport(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	pq, ok := s.postbackFilter(w, r)
	if !ok {
		return
	}
	header := postbackExportHeader()
	keep := visibleColumns(s.viewerFrom(r.Context()), header)
	cw := csv.NewWriter(w)
	_, err := s.cfg.Stats.EachPostback(r.Context(), pq, exportRows, func(tot store.PostbackTotals) error {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=postbacks.csv")
		if tot.Count > int64(exportRows) {
			w.Header().Set("X-Export-Truncated", "1")
		}
		return cw.Write(pickColumns(header, keep))
	}, func(p store.PostbackRow) error {
		return cw.Write(pickColumns(postbackExportRecord(p), keep))
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "postbacks error")
		return
	}
	cw.Flush()
}

// postbackExportHeader is the CSV column list in postbackExportRecord order.
func postbackExportHeader() []string {
	h := []string{
		"ts", "group", "stream", "domain", "page", "device", "country", "city",
		"profit", "cid", "status", "offer_id", "landing_id", "source_id", "network_id",
	}
	for i := 1; i <= 12; i++ {
		h = append(h, fmt.Sprintf("sub_id_%d", i))
	}
	return append(h,
		"campaign_id", "origin_campaign", "origin_campaign_id", "click_ts", "original_status", "prev_status", "tid",
		"ip", "region", "lang", "os", "os_version", "browser", "browser_ver", "brand",
		"useragent", "referer", "se", "keyword", "asn", "org",
		"site", "ad_campaign_id", "creative_id", "external_id", "xrw", "device_model",
	)
}

// postbackExportRecord builds one CSV row in postbackExportHeader order.
func postbackExportRecord(p store.PostbackRow) []string {
	rec := []string{
		p.TS.Format(time.RFC3339), p.Group, p.Stream, p.Domain, p.Page, p.Device, p.Country, p.City,
		fmt.Sprintf("%.2f", p.Profit), p.CID, p.Status, p.Offer, p.Landing, p.Source, p.Network,
	}
	for i := 0; i < 12; i++ {
		cell := ""
		if i < len(p.Subs) {
			cell = p.Subs[i]
		}
		rec = append(rec, cell)
	}
	clickTS := ""
	if !p.ClickTS.IsZero() && p.ClickTS.Unix() > 0 {
		clickTS = p.ClickTS.Format(time.RFC3339)
	}
	rec = append(rec,
		p.CampaignID, p.OriginCampaign, p.OriginCampaignID, clickTS, p.OriginalStatus, p.PrevStatus, p.TID,
		p.IP, p.Region, p.Lang, p.OS, p.OSVersion, p.Browser, p.BrowserVer, p.Brand,
		p.UserAgent, p.Referer, p.SE, p.Keyword,
		strconv.FormatUint(uint64(p.ASN), 10), p.Org,
		p.Site, p.AdCampaignID, p.CreativeID, p.ExternalID, p.XRW, p.DeviceModel,
	)
	for i := range rec {
		rec[i] = csvSafe(rec[i])
	}
	return rec
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
