package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

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
	Sort                                                        string
	Desc                                                        bool
	Conds                                                       []Cond
	// ExcludeDomain drops rows whose domain equals this host (Keitaro import filter).
	ExcludeDomain string
}

const (
	// PostbackExportMax is the row cap for CSV export of conversions.
	PostbackExportMax  = 50000
	postbackFetchBatch = 1000
)

// PostbackTotals is the size of one conversion-journal match.
// Profit is the sum of payouts. Count is the number of rows, not the page.
type PostbackTotals struct {
	Profit float64
	Count  int64
}

// postbackOriginSnap records which origin columns are ready for one journal page.
type postbackOriginSnap struct {
	originConv   bool
	originEvents bool
}

// postbackPage is one paginated conversion-journal SELECT with shared match SQL.
type postbackPage struct {
	from   string
	where  string
	args   []any
	sort   string
	desc   bool
	limit  int
	offset int
	origin postbackOriginSnap
}

// Postbacks returns conversions over a period (optional group filter) and the total profit.
// It is QueryPostbacks with only the period, the group and the limit set.
// The profit is PostbackTotals.Profit. Count is not returned.
func (c *CH) Postbacks(ctx context.Context, from, to time.Time, group string, limit int) ([]PostbackRow, float64, error) {
	return c.PostbacksExcluding(ctx, from, to, group, limit, "")
}

// PostbacksExcluding is Postbacks but drops rows whose domain equals excludeDomain.
// ctx bounds the query. An empty excludeDomain applies no domain filter.
// Returns matching rows, total profit over the match, and a query or scan error.
func (c *CH) PostbacksExcluding(ctx context.Context, from, to time.Time, group string, limit int, excludeDomain string) ([]PostbackRow, float64, error) {
	rows, tot, err := c.QueryPostbacks(ctx, PostbackQuery{
		From: from, To: to, Group: group, Limit: limit, ExcludeDomain: excludeDomain,
	})
	return rows, tot.Profit, err
}

// QueryPostbacks returns conversions matching q and the totals.
// ctx bounds the queries. A filter on offer, landing, source or network
// yields no rows when those columns are not on the table yet.
// Conds that name a wave that is not ready do the same, without a query.
// ErrUnknownField and ErrBadCond come from Conds. Offset below zero becomes
// zero. Profit is the sum over the whole match, not the page. Count is the
// number of matching rows. A query error is returned as-is.
func (c *CH) QueryPostbacks(ctx context.Context, q PostbackQuery) ([]PostbackRow, PostbackTotals, error) {
	c.maybeRefreshOriginWave()
	originConv := c.originConv.Load()
	tot, where, args, from, skip, err := c.postbackMatch(ctx, q, originConv)
	if err != nil || skip {
		return []PostbackRow{}, tot, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	off := q.Offset
	if off < 0 {
		off = 0
	}
	page := postbackPage{
		from: from, where: where, args: args,
		sort: q.Sort, desc: q.Desc, limit: limit, offset: off,
		origin: postbackOriginSnap{originConv: originConv, originEvents: c.originEvents.Load()},
	}
	out, err := c.queryPostbackRows(ctx, page)
	return out, tot, err
}

// EachPostback reads conversions matching q in store order and calls fn for each row.
// ctx bounds the queries. At most max rows are delivered; max above PostbackExportMax
// is clamped. The interactive limit clamp in QueryPostbacks does not apply.
// prepare runs once with match totals before the first row; nil is skipped.
// Match totals are computed once; truncated is true when count exceeds max.
func (c *CH) EachPostback(ctx context.Context, q PostbackQuery, max int, prepare func(PostbackTotals) error, fn func(PostbackRow) error) (bool, error) {
	if max <= 0 {
		max = PostbackExportMax
	}
	if max > PostbackExportMax {
		max = PostbackExportMax
	}
	c.maybeRefreshOriginWave()
	originConv := c.originConv.Load()
	originEvents := c.originEvents.Load()
	tot, where, args, from, skip, err := c.postbackMatch(ctx, q, originConv)
	if err != nil {
		return false, err
	}
	if skip {
		return false, nil
	}
	truncated := tot.Count > int64(max)
	if prepare != nil {
		if err := prepare(tot); err != nil {
			return false, err
		}
	}
	offset := 0
	delivered := 0
	for delivered < max {
		batch := postbackFetchBatch
		if rem := max - delivered; rem < batch {
			batch = rem
		}
		page := postbackPage{
			from: from, where: where, args: args,
			sort: q.Sort, desc: q.Desc, limit: batch, offset: offset,
			origin: postbackOriginSnap{originConv: originConv, originEvents: originEvents},
		}
		rows, err := c.queryPostbackRows(ctx, page)
		if err != nil {
			return false, err
		}
		if len(rows) == 0 {
			return truncated, nil
		}
		for _, p := range rows {
			if err := fn(p); err != nil {
				return false, err
			}
		}
		delivered += len(rows)
		offset += len(rows)
		if len(rows) < batch {
			return truncated, nil
		}
	}
	return truncated, nil
}

// postbackMatch returns totals and the shared FROM/WHERE for conversion journal queries.
func (c *CH) postbackMatch(ctx context.Context, q PostbackQuery, originConv bool) (PostbackTotals, string, []any, string, bool, error) {
	if !c.clickCols && (q.Offer != "" || q.Landing != "" || q.Source != "" || q.Network != "") {
		return PostbackTotals{}, "", nil, "", true, nil
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	where, args := postbackWhere(q, c.clickCols)
	extra, extraArgs, skip, err := buildWhere(c, "conversions", q.Conds)
	if err != nil {
		return PostbackTotals{}, "", nil, "", false, err
	}
	if skip {
		return PostbackTotals{}, "", nil, "", true, nil
	}
	where += extra
	args = append(args, extraArgs...)
	from := conversionUnion(c.clickCols, c.extConv, c.modelConv, originConv)
	var tot PostbackTotals
	var n uint64
	if err := c.conn.QueryRow(ctx, "SELECT sum(profit), count() FROM ("+from+") WHERE "+where, args...).Scan(&tot.Profit, &n); err != nil {
		return PostbackTotals{}, "", nil, "", false, err
	}
	tot.Count = int64(n)
	return tot, where, args, from, false, nil
}

// queryPostbackRows loads one page of conversion rows with click backfill.
// ctx bounds the queries. page carries the union FROM clause, WHERE, sort,
// pagination and origin-wave flags. It returns scanned rows or a query error.
func (c *CH) queryPostbackRows(ctx context.Context, page postbackPage) ([]PostbackRow, error) {
	order, slow, err := c.journalOrderByCH("conversions", page.sort, page.desc)
	if err != nil {
		return nil, err
	}
	sel, extCols := c.postbackColumns(page.origin.originConv)
	q := "SELECT " + sel + " FROM (" + page.from + ") WHERE " + page.where + " ORDER BY " + order + " LIMIT ? OFFSET ?"
	if slow {
		q += " SETTINGS max_execution_time=20"
	}
	rows, err := c.conn.Query(ctx, q, append(page.args, page.limit, page.offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PostbackRow
	for rows.Next() {
		var p PostbackRow
		subs := make([]string, 12)
		if err := rows.Scan(c.postbackDest(&p, subs, extCols)...); err != nil {
			return nil, err
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
		return nil, err
	}
	if cids := cidsNeedingClickFill(out); len(cids) > 0 {
		fills, err := c.clickFillByCIDs(ctx, cids, page.origin.originEvents)
		if err != nil {
			return nil, err
		}
		fillFromClicks(out, fills)
	}
	return out, nil
}

// clickFillByCIDs loads the newest event per cid for backfilling conversions.
// ctx bounds the query. cids must be non-empty. The map holds one ClickFill per
// cid found; cids with no event are omitted. A query error is returned as-is.
func (c *CH) clickFillByCIDs(ctx context.Context, cids []string, originEvents bool) (map[string]ClickFill, error) {
	if len(cids) == 0 {
		return nil, nil
	}
	cols := "cid, ts, group_id, group_name, ip, useragent, os, os_version, browser, browser_ver, brand, referer, lang, city, region"
	if c.clickCols {
		cols += ", landing_id, offer_id, network_id, source_id"
	}
	if originEvents {
		cols += ", origin_group_id, origin_group_name"
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
			&cid, &fill.TS, &fill.GroupID, &fill.GroupName, &fill.IP, &fill.UserAgent,
			&fill.OS, &fill.OSVersion, &fill.Browser, &fill.BrowserVer, &fill.Brand,
			&fill.Referer, &fill.Lang, &fill.City, &fill.Region,
		}
		if c.clickCols {
			dest = append(dest, &fill.LandingID, &fill.OfferID, &fill.NetworkID, &fill.SourceID)
		}
		if originEvents {
			dest = append(dest, &fill.OriginGroupID, &fill.OriginGroupName)
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
// p receives the values. subs holds sub_id scan slots when the click wave is on.
// cols matches convLogExtraCols. It does not fail.
func postbackExtDest(p *PostbackRow, subs []string, cols []string) []any {
	dest := make([]any, 0, len(cols))
	for _, col := range cols {
		if strings.HasPrefix(col, "sub_id_") {
			var n int
			if _, err := fmt.Sscanf(col, "sub_id_%d", &n); err == nil && n >= 1 && n <= 12 {
				dest = append(dest, &subs[n-1])
				continue
			}
		}
		dest = append(dest, postbackExtPtr(p, col))
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
	case "device_model":
		return &p.DeviceModel
	case "offer_id":
		return &p.Offer
	case "landing_id":
		return &p.Landing
	case "source_id":
		return &p.Source
	case "network_id":
		return &p.Network
	case "origin_group_name":
		return &p.OriginCampaign
	case "origin_group_id":
		return &p.OriginCampaignID
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
	if d := strings.TrimSpace(q.ExcludeDomain); d != "" {
		where += " AND domain != ?"
		args = append(args, d)
	}
	if click {
		add("offer_id", q.Offer)
		add("landing_id", q.Landing)
		add("source_id", q.Source)
		add("network_id", q.Network)
	}
	return where, args
}

// convLogExtraCols lists optional conversions columns for the journal SELECT,
// in the same order as convExtra and conversionUnion.
func convLogExtraCols(click, ext, model, origin bool) []string {
	_, cols := convExtra(click, ext, model, origin)
	if cols == "" {
		return nil
	}
	return strings.Split(cols, ", ")
}

// postbackColumns builds the SELECT list of the conversion log for the waves
// that are ready. It returns the list and the optional column names in the order
// postbackDest scans them (matches conversionUnion).
func (c *CH) postbackColumns(originConv bool) (sel string, extraCols []string) {
	sel = "ts, group_name, stream_name, domain, page, device, country, city, profit, cid, status"
	extraCols = convLogExtraCols(c.clickCols, c.extConv, c.modelConv, originConv)
	if len(extraCols) > 0 {
		sel += ", " + strings.Join(extraCols, ", ")
	}
	return sel, extraCols
}

// postbackDest lists the scan targets for one row of postbackColumns.
// p is the row being filled, subs the twelve sub id slots and extraCols the
// optional names that postbackColumns returned.
func (c *CH) postbackDest(p *PostbackRow, subs []string, extraCols []string) []any {
	dest := []any{&p.TS, &p.Group, &p.Stream, &p.Domain, &p.Page, &p.Device, &p.Country, &p.City, &p.Profit, &p.CID, &p.Status}
	if len(extraCols) > 0 {
		dest = append(dest, postbackExtDest(p, subs, extraCols)...)
	}
	return dest
}

// conversionFrom is conversionUnion without the model wave.
// click and ext select the waves; see conversionUnion.
func conversionFrom(click, ext bool) string {
	return conversionUnion(click, ext, false, false)
}

// conversionUnion is the old postbacks table plus one row per cid and status.
// click adds the click-wave columns, ext the ext-wave columns and model the
// device_model column. origin adds origin_group_name and origin_group_id.
// The postbacks branch uses typed stand-ins so the union lines up.
// All flags false keep the original two-column status shape.
func conversionUnion(click, ext, model, origin bool) string {
	base := `ts, domain, page, device, operator, country, city, profit, group_name, stream_name, cid`
	post := `SELECT ` + base + `, '' AS status`
	conv := `SELECT ` + base + `, status`
	if click || ext || model || origin {
		stubs, cols := convExtra(click, ext, model, origin)
		if stubs != "" {
			post += ", " + stubs
			conv += ", " + cols
		}
	}
	return post + ` FROM postbacks
		UNION ALL
		` + conv + ` FROM conversions FINAL`
}

// convExtra lists stand-ins and real names for the click, ext and model waves.
// click, ext and model select which waves are included. Base columns are
// omitted. The two strings have the same length and the same order. Both are
// empty when no flag is set or the waves add nothing.
func convExtra(click, ext, model, origin bool) (stubs, cols string) {
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
		if f.ConvWave == waveModel && !model {
			continue
		}
		if f.ConvWave == waveOrigin && !origin {
			continue
		}
		if f.ConvWave != waveClick && f.ConvWave != waveExt && f.ConvWave != waveModel && f.ConvWave != waveOrigin {
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
