package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PostbackIn is one conversion to record from a click id.
// CID selects the click. Profit is the payout. Status is stored lowercased
// and trimmed. Original is the raw status; when it is empty, Status is kept
// as the raw value. TID is the network transaction id.
type PostbackIn struct {
	CID              string
	Profit           float64
	Status, Original string
	TID              string
}

// RecordConversion finds the click cid and writes one conversion.
// ctx bounds the reads and the insert. in supplies payout, status and tid.
// The returned group id is the campaign the click belonged to.
// Base click columns are always read. Net, click and ext-event columns are
// read only when that wave is ready. Ext conversion columns, including
// prev_status, are written only when the conversions ext wave is ready.
// prev_status is the latest stored status for the same cid, or "" when this
// is the first row. An unknown cid returns the driver's error. A batch error
// is returned as-is and nothing is reported as stored.
func (c *CH) RecordConversion(ctx context.Context, in PostbackIn) (string, error) {
	snap, err := c.readClick(ctx, in.CID)
	if err != nil {
		return "", err
	}
	prev := ""
	if c.extConv {
		prev, err = c.prevStatus(ctx, in.CID)
		if err != nil {
			return "", err
		}
	}
	if err := c.writeConversion(ctx, snap, in, prev); err != nil {
		return "", err
	}
	return snap.gid, nil
}

// RecordPostback finds an event by cid and writes one conversion.
// status is part of the storage key, so a later sale does not replace an
// earlier lead. The returned group id is the campaign the click belonged to.
// It is RecordConversion with the raw status used as both the stored status
// and the original status. Errors are the same as RecordConversion.
func (c *CH) RecordPostback(ctx context.Context, cid string, profit float64, status string) (string, error) {
	return c.RecordConversion(ctx, PostbackIn{CID: cid, Profit: profit, Status: status})
}

// clickSnap is the part of a click that a conversion copies.
type clickSnap struct {
	ts                                          time.Time
	gid, gname, sname                           string
	country, city, device, operator             string
	domain, page                                string
	ip, region, lang, os, osVer                 string
	browser, browserVer, brand                  string
	ua, referer, se, keyword                    string
	asn                                         uint32
	org                                         string
	offer, landing, source, network             string
	subs                                        [12]string
	site, adCampaign, creative, externalID, xrw string
}

// readClick loads the newest event with cid.
// ctx bounds the query. The base columns, including the ones the ext
// conversion wave copies, are always selected. Net, click and ext-event
// columns are added only when that wave is ready.
// An unknown cid returns the driver's error (no row).
func (c *CH) readClick(ctx context.Context, cid string) (clickSnap, error) {
	var s clickSnap
	cols := []string{
		"ts", "group_id", "group_name", "stream_name", "country", "city", "device",
		"operator", "domain", "page", "ip", "region", "lang", "os", "os_version",
		"browser", "browser_ver", "brand", "useragent", "referer", "se", "keyword",
	}
	dest := []any{
		&s.ts, &s.gid, &s.gname, &s.sname, &s.country, &s.city, &s.device,
		&s.operator, &s.domain, &s.page, &s.ip, &s.region, &s.lang, &s.os, &s.osVer,
		&s.browser, &s.browserVer, &s.brand, &s.ua, &s.referer, &s.se, &s.keyword,
	}
	if c.netCols {
		cols = append(cols, "asn", "org")
		dest = append(dest, &s.asn, &s.org)
	}
	if c.clickCols {
		for i := 1; i <= 12; i++ {
			cols = append(cols, fmt.Sprintf("sub_id_%d", i))
			dest = append(dest, &s.subs[i-1])
		}
		cols = append(cols, "offer_id", "landing_id", "source_id", "network_id")
		dest = append(dest, &s.offer, &s.landing, &s.source, &s.network)
	}
	if c.extEvents {
		for _, col := range columnsOf("events", waveExt) {
			cols = append(cols, col)
			dest = append(dest, clickExtPtr(&s, col))
		}
	}
	q := "SELECT " + strings.Join(cols, ", ") + " FROM events WHERE cid = ? ORDER BY ts DESC LIMIT 1"
	err := c.conn.QueryRow(ctx, q, cid).Scan(dest...)
	return s, err
}

// clickExtPtr is the Scan target of one ext events column on a click.
// s receives the value. col is the physical name. Every known column has
// its own field. An unknown column returns a fresh string and does not
// overwrite xrw. It does not fail.
func clickExtPtr(s *clickSnap, col string) any {
	switch col {
	case "site":
		return &s.site
	case "ad_campaign_id":
		return &s.adCampaign
	case "creative_id":
		return &s.creative
	case "external_id":
		return &s.externalID
	case "xrw":
		return &s.xrw
	default:
		return new(string)
	}
}

// prevStatus returns the latest conversion status for cid.
// ctx bounds the query. No row returns "" and a nil error.
// Any other scan error is returned.
func (c *CH) prevStatus(ctx context.Context, cid string) (string, error) {
	var status string
	err := c.conn.QueryRow(ctx, `SELECT status FROM conversions WHERE cid = ? ORDER BY ts DESC LIMIT 1`, cid).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

// writeConversion inserts one conversion copied from snap.
// ctx bounds the batch. in supplies profit, status and tid. prev is the
// previous status, written only with the ext wave. Status is stored
// lowercased and trimmed; original_status keeps the raw text.
// The row time is the server default. Returns a batch error unchanged.
func (c *CH) writeConversion(ctx context.Context, snap clickSnap, in PostbackIn, prev string) error {
	raw := in.Original
	if raw == "" {
		raw = in.Status
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	cols := `domain, page, device, operator, country, city, profit, group_name, stream_name, cid, status`
	row := []any{snap.domain, snap.page, snap.device, snap.operator, snap.country, snap.city, in.Profit, snap.gname, snap.sname, in.CID, status}
	if c.clickCols {
		cols += `, offer_id, landing_id, source_id, network_id, ` + subColList()
		row = append(row, snap.offer, snap.landing, snap.source, snap.network)
		for _, s := range snap.subs {
			row = append(row, s)
		}
	}
	if c.extConv {
		copied := ConversionInsert{
			GroupID: snap.gid, ClickTS: snap.ts, OriginalStatus: raw, PrevStatus: prev, TID: in.TID,
			IP: snap.ip, Region: snap.region, Lang: snap.lang, OS: snap.os, OSVersion: snap.osVer,
			Browser: snap.browser, BrowserV: snap.browserVer, Brand: snap.brand, UserAgent: snap.ua,
			Referer: snap.referer, SE: snap.se, Keyword: snap.keyword, ASN: snap.asn, Org: snap.org,
			Site: snap.site, AdCampaignID: snap.adCampaign, CreativeID: snap.creative,
			ExternalID: snap.externalID, XRW: snap.xrw,
		}
		for _, col := range columnsOf("conversions", waveExt) {
			cols += ", " + col
			row = append(row, conversionExtValue(copied, col))
		}
	}
	batch, err := c.conn.PrepareBatch(ctx, `INSERT INTO conversions (`+cols+`)`)
	if err != nil {
		return err
	}
	if err := batch.Append(row...); err != nil {
		return err
	}
	return batch.Send()
}

// PostbackRow is one conversion as the journal shows it.
// The first fields keep their existing JSON names. The rest are the click
// copy and are omitted when empty. click_ts uses RFC3339 and omitzero:
// a zero time and the Unix epoch are omitted.
type PostbackRow struct {
	TS      time.Time `json:"ts"`
	Group   string    `json:"group"`
	Stream  string    `json:"stream"`
	Domain  string    `json:"domain"`
	Page    string    `json:"page"`
	Device  string    `json:"device"`
	Country string    `json:"country"`
	City    string    `json:"city"`
	Profit  float64   `json:"profit"`
	CID     string    `json:"cid"`
	Status  string    `json:"status"`
	Offer   string    `json:"offer_id,omitempty"`
	Landing string    `json:"landing_id,omitempty"`
	Source  string    `json:"source_id,omitempty"`
	Network string    `json:"network_id,omitempty"`
	Subs    []string  `json:"subs,omitempty"`

	CampaignID     string    `json:"campaign_id,omitempty"`
	ClickTS        time.Time `json:"click_ts,omitzero"`
	OriginalStatus string    `json:"original_status,omitempty"`
	PrevStatus     string    `json:"prev_status,omitempty"`
	TID            string    `json:"tid,omitempty"`
	IP             string    `json:"ip,omitempty"`
	Region         string    `json:"region,omitempty"`
	Lang           string    `json:"lang,omitempty"`
	OS             string    `json:"os,omitempty"`
	OSVersion      string    `json:"os_version,omitempty"`
	Browser        string    `json:"browser,omitempty"`
	BrowserVer     string    `json:"browser_ver,omitempty"`
	Brand          string    `json:"brand,omitempty"`
	UserAgent      string    `json:"useragent,omitempty"`
	Referer        string    `json:"referer,omitempty"`
	SE             string    `json:"se,omitempty"`
	Keyword        string    `json:"keyword,omitempty"`
	ASN            uint32    `json:"asn,omitempty"`
	Org            string    `json:"org,omitempty"`
	Site           string    `json:"site,omitempty"`
	AdCampaignID   string    `json:"ad_campaign_id,omitempty"`
	CreativeID     string    `json:"creative_id,omitempty"`
	ExternalID     string    `json:"external_id,omitempty"`
	XRW            string    `json:"xrw,omitempty"`
}

// PostbackQuery filters the conversion journal.
// CID is the click id, shown in the admin as subid.
// Offer, landing, source and network match only after those columns exist.
// Conds are extra field filters. Offset skips that many rows and is clamped
// at zero.
type PostbackQuery struct {
	From, To                                                    time.Time
	Group, Status, Stream, Offer, Landing, Source, Network, CID string
	Limit                                                       int
	Offset                                                      int
	Conds                                                       []Cond
}

// Postbacks returns conversions over a period (optional group filter) and the total profit.
// It is QueryPostbacks with only the period, the group and the limit set.
func (c *CH) Postbacks(ctx context.Context, from, to time.Time, group string, limit int) ([]PostbackRow, float64, error) {
	return c.QueryPostbacks(ctx, PostbackQuery{From: from, To: to, Group: group, Limit: limit})
}

// QueryPostbacks returns conversions matching q and the total profit.
// ctx bounds the queries. A filter on offer, landing, source or network
// yields no rows when those columns are not on the table yet.
// Conds that name a wave that is not ready do the same, without a query.
// ErrUnknownField and ErrBadCond come from Conds. Offset below zero becomes
// zero. The profit is the sum over the whole match, not the page.
// A query error is returned as-is.
func (c *CH) QueryPostbacks(ctx context.Context, q PostbackQuery) ([]PostbackRow, float64, error) {
	if !c.clickCols && (q.Offer != "" || q.Landing != "" || q.Source != "" || q.Network != "") {
		return []PostbackRow{}, 0, nil
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	where, args := postbackWhere(q, c.clickCols)
	extra, extraArgs, skip, err := buildWhere(c, "conversions", q.Conds)
	if err != nil {
		return nil, 0, err
	}
	if skip {
		return []PostbackRow{}, 0, nil
	}
	where += extra
	args = append(args, extraArgs...)
	from := conversionFrom(c.clickCols, c.extConv)
	var total float64
	if err := c.conn.QueryRow(ctx, "SELECT sum(profit) FROM ("+from+") WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	off := q.Offset
	if off < 0 {
		off = 0
	}
	sel := "ts, group_name, stream_name, domain, page, device, country, city, profit, cid, status"
	if c.clickCols {
		sel += ", offer_id, landing_id, source_id, network_id, " + subColList()
	}
	var extCols []string
	if c.extConv {
		extCols = columnsOf("conversions", waveExt)
		sel += ", " + strings.Join(extCols, ", ")
	}
	rows, err := c.conn.Query(ctx,
		"SELECT "+sel+" FROM ("+from+") WHERE "+where+" ORDER BY ts DESC LIMIT ? OFFSET ?",
		append(args, limit, off)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []PostbackRow
	for rows.Next() {
		var p PostbackRow
		dest := []any{&p.TS, &p.Group, &p.Stream, &p.Domain, &p.Page, &p.Device, &p.Country, &p.City, &p.Profit, &p.CID, &p.Status}
		subs := make([]string, 12)
		if c.clickCols {
			dest = append(dest, &p.Offer, &p.Landing, &p.Source, &p.Network)
			for i := range subs {
				dest = append(dest, &subs[i])
			}
		}
		if len(extCols) > 0 {
			dest = append(dest, postbackExtDest(&p, extCols)...)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		if p.ClickTS.Unix() <= 0 {
			p.ClickTS = time.Time{}
		}
		if c.clickCols {
			p.Subs = subs
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if cids := cidsNeedingClickFill(out); len(cids) > 0 {
		fills, err := c.clickFillByCIDs(ctx, cids)
		if err != nil {
			return nil, 0, err
		}
		fillFromClicks(out, fills)
	}
	return out, total, nil
}

// clickFillByCIDs loads the newest event per cid for backfilling conversions.
// ctx bounds the query. cids must be non-empty. The map holds one ClickFill per
// cid found; cids with no event are omitted. A query error is returned as-is.
func (c *CH) clickFillByCIDs(ctx context.Context, cids []string) (map[string]ClickFill, error) {
	if len(cids) == 0 {
		return nil, nil
	}
	cols := "cid, ts, group_id, ip, useragent, os, os_version, browser, browser_ver, brand, referer, lang, city, region"
	if c.clickCols {
		cols += ", landing_id, offer_id, network_id, source_id"
	}
	q := "SELECT " + cols + " FROM events WHERE cid IN (" + placeholders(len(cids)) + ") ORDER BY ts DESC"
	args := make([]any, len(cids))
	for i, id := range cids {
		args[i] = id
	}
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ClickFill, len(cids))
	for rows.Next() {
		var cid string
		var fill ClickFill
		dest := []any{
			&cid, &fill.TS, &fill.GroupID, &fill.IP, &fill.UserAgent,
			&fill.OS, &fill.OSVersion, &fill.Browser, &fill.BrowserVer, &fill.Brand,
			&fill.Referer, &fill.Lang, &fill.City, &fill.Region,
		}
		if c.clickCols {
			dest = append(dest, &fill.LandingID, &fill.OfferID, &fill.NetworkID, &fill.SourceID)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if cid == "" {
			continue
		}
		if _, ok := out[cid]; ok {
			continue
		}
		out[cid] = fill
	}
	return out, rows.Err()
}

// postbackExtDest returns Scan targets for cols, in that order.
// p receives the values. cols is the conversions ext list for this query,
// computed once by the caller. Every name has a target. An unknown name
// scans into a fresh string and does not overwrite xrw. It does not fail.
func postbackExtDest(p *PostbackRow, cols []string) []any {
	dest := make([]any, len(cols))
	for i, col := range cols {
		dest[i] = postbackExtPtr(p, col)
	}
	return dest
}

// postbackExtPtr is the Scan target of one ext conversions column.
// p is the row. col is the physical name. Every known column has its own
// field. An unknown column returns a fresh string and does not overwrite
// xrw. It does not fail.
func postbackExtPtr(p *PostbackRow, col string) any {
	switch col {
	case "group_id":
		return &p.CampaignID
	case "click_ts":
		return &p.ClickTS
	case "original_status":
		return &p.OriginalStatus
	case "prev_status":
		return &p.PrevStatus
	case "tid":
		return &p.TID
	case "ip":
		return &p.IP
	case "region":
		return &p.Region
	case "lang":
		return &p.Lang
	case "os":
		return &p.OS
	case "os_version":
		return &p.OSVersion
	case "browser":
		return &p.Browser
	case "browser_ver":
		return &p.BrowserVer
	case "brand":
		return &p.Brand
	case "useragent":
		return &p.UserAgent
	case "referer":
		return &p.Referer
	case "se":
		return &p.SE
	case "keyword":
		return &p.Keyword
	case "asn":
		return &p.ASN
	case "org":
		return &p.Org
	case "site":
		return &p.Site
	case "ad_campaign_id":
		return &p.AdCampaignID
	case "creative_id":
		return &p.CreativeID
	case "external_id":
		return &p.ExternalID
	case "xrw":
		return &p.XRW
	default:
		return new(string)
	}
}

// postbackWhere builds the period predicate and the optional equality filters.
// click must be true before offer, landing, source or network are included.
// The SQL does not start with WHERE. args follow the placeholders.
func postbackWhere(q PostbackQuery, click bool) (string, []any) {
	where := "ts >= ? AND ts < ?"
	args := []any{q.From, q.To}
	add := func(col, val string) {
		if val == "" {
			return
		}
		where += " AND " + col + " = ?"
		args = append(args, val)
	}
	add("group_name", q.Group)
	add("status", q.Status)
	add("stream_name", q.Stream)
	add("cid", q.CID)
	if click {
		add("offer_id", q.Offer)
		add("landing_id", q.Landing)
		add("source_id", q.Source)
		add("network_id", q.Network)
	}
	return where, args
}

// subColList is sub_id_1 through sub_id_12, comma-separated.
// It takes nothing and does not fail.
func subColList() string {
	parts := make([]string, 12)
	for i := 1; i <= 12; i++ {
		parts[i-1] = fmt.Sprintf("sub_id_%d", i)
	}
	return strings.Join(parts, ", ")
}

// conversionFrom is the old postbacks table plus one row per cid and status.
// click adds the click-wave columns. ext adds the ext-wave columns.
// The postbacks branch uses typed stand-ins so the union lines up.
// Both flags false keep the original two-column status shape.
func conversionFrom(click, ext bool) string {
	base := `ts, domain, page, device, operator, country, city, profit, group_name, stream_name, cid`
	post := `SELECT ` + base + `, '' AS status`
	conv := `SELECT ` + base + `, status`
	if click || ext {
		stubs, cols := convExtra(click, ext)
		if stubs != "" {
			post += ", " + stubs
			conv += ", " + cols
		}
	}
	return post + ` FROM postbacks
		UNION ALL
		` + conv + ` FROM conversions`
}

// convExtra lists stand-ins and real names for the click and ext waves.
// click and ext select which waves are included. Base columns are omitted.
// The two strings have the same length and the same order. Both are empty
// when neither flag is set or the waves add nothing.
func convExtra(click, ext bool) (stubs, cols string) {
	var stubParts, colParts []string
	for _, f := range fields {
		if f.Conv == "" || f.ConvWave == waveBase {
			continue
		}
		if f.ConvWave == waveClick && !click {
			continue
		}
		if f.ConvWave == waveExt && !ext {
			continue
		}
		if f.ConvWave != waveClick && f.ConvWave != waveExt {
			continue
		}
		stubParts = append(stubParts, convStub(f))
		colParts = append(colParts, f.Conv)
	}
	if len(stubParts) == 0 {
		return "", ""
	}
	return strings.Join(stubParts, ", "), strings.Join(colParts, ", ")
}
