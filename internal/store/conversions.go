package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

// PostbackIn is one conversion to record from a click id.
// CID selects the click. Profit is the payout. DepSum is the deposit and is
// not the payout. Status is stored lowercased and trimmed. Original is the
// raw status; when it is empty, Status is kept as the raw value. TID is the
// network transaction id. At is the moment the postback reached the engine; it
// becomes the row time. A zero At means now. A postback that waited in the
// disk queue keeps its own At, so the order of statuses of one click does not
// change with the delay.
type PostbackIn struct {
	CID              string
	Profit           float64
	DepSum           float64
	Status, Original string
	TID              string
	At               time.Time
	// Legacy marks the old /?pb= pixel. It copies the click and does not
	// read the chain root.
	Legacy bool
}

// ConversionResult is what RecordConversion reports about a stored conversion.
// GroupID is the campaign the click belonged to. Fresh is true when no row with
// this click id and status has a time other than this postback's, that is when
// this is the first arrival of the pair: only then does the campaign send its
// outbound postbacks. A row that carries exactly this postback's time is its
// own earlier write.
type ConversionResult struct {
	GroupID string
	Fresh   bool
	// RootCID is the chain root copied from the click. Empty means the click
	// is its own root. It is set even when the root row was not found.
	RootCID string
	// NetworkID is the affiliate network of the click, empty when the click has
	// none or the click columns are not on the table yet.
	NetworkID string
}

// RecordConversion finds the click cid and writes one conversion.
// ctx bounds the reads and the insert. in supplies payout, deposit, status,
// tid and the arrival time.
// The row time is in.At cut to whole seconds, or the current time when At is
// zero. Base click columns are always read. Net, click and ext-event columns
// are read only when that wave is ready. Ext conversion columns, including
// prev_status, are written only when the conversions ext wave is ready.
// prev_status is the latest stored status of the same cid before the row time
// (see prevStatus for rows of the same second), or "" when there is none.
// Fresh in the result is decided by one point lookup on (cid, status) before
// the insert. Counting rows with another time, and not rows in general, keeps
// the answer the same when the postback is written twice: a retry of an
// attempt whose answer was lost finds its own first row at the same second
// and is still the first arrival, while a row written earlier with a later
// time (a postback that waited in the disk queue) makes this one not first.
// The lookup follows the sort key and does not depend on prev_status, which is
// wrong for statuses that alternate.
// An unknown cid returns sql.ErrNoRows. A batch error is returned as-is and
// nothing is reported as stored.
func (c *CH) RecordConversion(ctx context.Context, in PostbackIn) (ConversionResult, error) {
	c.maybeRefreshOriginWave()
	if in.At.IsZero() {
		in.At = time.Now()
	}
	in.At = in.At.Truncate(time.Second)
	snap, err := c.readClick(ctx, in.CID)
	if err != nil {
		return ConversionResult{}, err
	}
	status := normalStatus(in.Status)
	prev := ""
	if c.extConv {
		prev, err = c.prevStatus(ctx, in.CID, status, in.At)
		if err != nil {
			return ConversionResult{}, err
		}
	}
	earlier, err := c.pairSeenBefore(ctx, in.CID, status, in.At)
	if err != nil {
		return ConversionResult{}, err
	}
	if snap.rootCID != "" && !in.Legacy && c.ready("events", waveRoot) && c.ready("conversions", waveRoot) {
		stream, keyword, found, err := c.readRootClick(ctx, snap.rootCID, snap.ts)
		if err != nil {
			return ConversionResult{}, err
		}
		if found {
			snap.originStream, snap.originKeyword = stream, keyword
		} else {
			c.rootMissing.Add(1)
		}
	}
	if err := c.writeConversion(ctx, snap, in, prev); err != nil {
		return ConversionResult{}, err
	}
	return ConversionResult{GroupID: snap.gid, Fresh: !earlier, RootCID: snap.rootCID, NetworkID: snap.network}, nil
}

// RecordPostback finds an event by cid and writes one conversion.
// status is part of the storage key, so a later sale does not replace an
// earlier lead. A newer rejected row drops that lead and sale from revenue,
// from lead and sale counts, and from the conversion journal.
// The result names the campaign the click belonged to and tells whether the
// pair is new.
// It is RecordConversion with the raw status used as both the stored status
// and the original status. Errors are the same as RecordConversion.
func (c *CH) RecordPostback(ctx context.Context, cid string, profit float64, status string) (ConversionResult, error) {
	return c.RecordConversion(ctx, PostbackIn{CID: cid, Profit: profit, Status: status, Legacy: true})
}

// RootMissing is how many conversions did not find their root click in the window.
// The count is since the process started. It does not fail.
func (c *CH) RootMissing() int64 {
	if c == nil {
		return 0
	}
	return c.rootMissing.Load()
}

// rootLookback is how far before the click the root row is still accepted:
// the path token lives two hours, plus the five minutes the token allows a clock to run ahead.
const rootLookback = 2*time.Hour + 5*time.Minute

// rootClickOrder is the sort of the root lookup.
// step is true when events have a step column. The first step of the root
// click wins; without the column the oldest row wins. It does not fail.
func rootClickOrder(step bool) string {
	if step {
		return "ORDER BY step, ts LIMIT 1"
	}
	return "ORDER BY ts LIMIT 1"
}

// readRootClick loads the first step of the chain root.
// ctx bounds the query. cid is the root click id. clickTS is the time of the
// click that named it. The window runs from clickTS minus rootLookback through
// clickTS. It returns the root stream and keyword. ok is false when no row is
// in the window. A query error is returned as-is.
func (c *CH) readRootClick(ctx context.Context, cid string, clickTS time.Time) (stream, keyword string, ok bool, err error) {
	q := "SELECT group_id, stream_name, keyword FROM events WHERE cid = ? AND ts >= ? AND ts <= ? " + rootClickOrder(c.stepEvents)
	var gid string
	err = c.conn.QueryRow(ctx, q, cid, clickTS.Add(-rootLookback), clickTS).Scan(&gid, &stream, &keyword)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return stream, keyword, true, nil
}

// normalStatus returns status the way writeConversion stores it: trimmed and
// lowercased. It does not fail.
func normalStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
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
	subs                                        [config.MaxSubID]string
	site, adCampaign, creative, externalID, xrw string
	deviceModel                                 string
	originGid, originGname                      string
	rootCID, originStream, originKeyword        string
}

// readClick loads the newest event with cid.
// ctx bounds the query. The base columns, including the ones the ext
// conversion wave copies, are always selected. Net, click and ext-event
// columns are added only when that wave is ready.
// The lookup first covers the last 7 days; when it finds no row, it repeats
// without the time bound, so any click inside the events TTL is found.
// An unknown cid returns sql.ErrNoRows.
func (c *CH) readClick(ctx context.Context, cid string) (clickSnap, error) {
	originEvents := c.originEvents.Load()
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
		for i := 1; i <= subScanCount(c); i++ {
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
	if c.modelEvents {
		cols = append(cols, "device_model")
		dest = append(dest, &s.deviceModel)
	}
	if originEvents {
		cols = append(cols, columnsOf("events", waveOrigin)...)
		dest = append(dest, &s.originGname, &s.originGid)
	}
	if c.ready("events", waveRoot) {
		cols = append(cols, "root_cid")
		dest = append(dest, &s.rootCID)
	}
	q := "SELECT " + strings.Join(cols, ", ") + " FROM events WHERE cid = ?"
	err := c.conn.QueryRow(ctx, q+clickRecentFilter+clickNewest, cid).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		err = c.conn.QueryRow(ctx, q+clickNewest, cid).Scan(dest...)
	}
	return s, err
}

// clickRecentFilter limits the first click lookup to the days in which almost
// every conversion arrives, so ClickHouse reads only those day partitions.
// clickNewest picks the latest row for the cid. A click older than the
// recent window is found by the second query without the filter.
const (
	clickRecentFilter = " AND ts >= now() - INTERVAL 7 DAY"
	clickNewest       = " ORDER BY ts DESC LIMIT 1"
)

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

// voidedCIDSelect lists clicks whose newest rejected row is at least as new
// as their newest lead or sale. A click with no rejected row is absent.
// A later lead or sale after the rejection is absent too.
// The statement has no placeholders and does not fail.
func voidedCIDSelect() string {
	return `SELECT cid FROM conversions FINAL
		GROUP BY cid
		HAVING maxIf(ts, status = 'rejected') > toDateTime(0)
			AND maxIf(ts, status = 'rejected') >= maxIf(ts, status IN ('lead', 'sale'))`
}

// voidedConfirmedSQL is an AND fragment that drops lead and sale rows of a
// click listed by voidedCIDSelect. cid and status are the outer column names,
// qualified when the outer query aliases the conversions table.
// Rejected rows and every other status stay. It has no placeholders and does not fail.
func voidedConfirmedSQL(cid, status string) string {
	return ` AND NOT (` + status + ` IN ('lead', 'sale') AND ` + cid + ` IN (` + voidedCIDSelect() + `))`
}

// sumDep is the deposit aggregate, or a typed zero when the wave is absent.
// on is true when conversions carry depsum. col is the column, qualified when
// the query aliases the table; it is used only when on is true, so a closed
// wave does not name the column. The string is a SELECT expression and does not fail.
func sumDep(on bool, col string) string {
	if !on {
		return "toFloat64(0)"
	}
	return "sum(" + col + ")"
}

// prevStatus returns the latest conversion status that precedes the row of cid
// with the given status written at time at. ctx bounds the query. status is
// the stored form (see normalStatus). No row returns "" and a nil error.
// Any other scan error is returned. Rows newer than at are ignored, so a
// postback written late from the disk queue does not take a later status of
// the same click as its predecessor. A row with the same time counts only when
// its status differs, so a postback written twice in the same second does not
// find its own first row as its predecessor, while a different status stored in
// that second still does. This point lookup omits FINAL so the newest physical
// row is seen before ReplacingMergeTree merges parts.
func (c *CH) prevStatus(ctx context.Context, cid, status string, at time.Time) (string, error) {
	var prev string
	err := c.conn.QueryRow(ctx, `SELECT status FROM conversions WHERE cid = ? AND (ts < ? OR (ts = ? AND status <> ?)) ORDER BY ts DESC LIMIT 1`, cid, at, at, status).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return prev, err
}

// pairSeenBefore reports whether a conversion with cid and status is stored
// with a time other than at: any other row of the pair means this postback is
// not the first arrival, whether that row is older or was written first with a
// later time. A row with exactly at is this postback's own earlier write and is
// ignored. ctx bounds the query. status is the stored form (see normalStatus).
// at must be cut to whole seconds, as the row time is. Any scan error is
// returned. This point lookup omits FINAL: one physical row is enough.
func (c *CH) pairSeenBefore(ctx context.Context, cid, status string, at time.Time) (bool, error) {
	var n uint64
	if err := c.conn.QueryRow(ctx, `SELECT count() FROM conversions WHERE cid = ? AND status = ? AND ts <> ?`, cid, status, at).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// writeConversion inserts one conversion copied from snap.
// ctx bounds the batch. in supplies profit, the deposit, status, tid and the
// arrival time. prev is the previous status, written only with the ext wave.
// Status is stored lowercased and trimmed; original_status keeps the raw text.
// depsum is written only when that wave is ready and is never part of the
// base column list. The row time is in.At written explicitly, never the server
// default: voidedCIDSelect compares the times of lead and rejected rows, and a
// lead written late with the time of its write would outlive the rejection that
// arrived after it. Returns a batch error unchanged.
func (c *CH) writeConversion(ctx context.Context, snap clickSnap, in PostbackIn, prev string) error {
	originConv := c.originConv.Load()
	raw := in.Original
	if raw == "" {
		raw = in.Status
	}
	status := normalStatus(in.Status)
	if in.At.IsZero() {
		in.At = time.Now()
	}
	cols := `ts, domain, page, device, operator, country, city, profit, group_name, stream_name, cid, status`
	row := []any{in.At, snap.domain, snap.page, snap.device, snap.operator, snap.country, snap.city, in.Profit, snap.gname, snap.sname, in.CID, status}
	if c.clickCols {
		cols += `, offer_id, landing_id, source_id, network_id, ` + subColList()
		row = append(row, snap.offer, snap.landing, snap.source, snap.network)
		for i := 0; i < clickWaveSubs; i++ {
			row = append(row, snap.subs[i])
		}
		if c.subConv {
			cols += ", " + subTailList()
			for i := clickWaveSubs; i < len(snap.subs); i++ {
				row = append(row, snap.subs[i])
			}
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
	if c.modelConv {
		cols += ", device_model"
		row = append(row, snap.deviceModel)
	}
	if c.depsumConv {
		cols += ", depsum"
		row = append(row, in.DepSum)
	}
	if c.ready("conversions", waveRoot) {
		for _, col := range columnsOf("conversions", waveRoot) {
			cols += ", " + col
			switch col {
			case "root_cid":
				row = append(row, snap.rootCID)
			case "origin_stream_name":
				row = append(row, snap.originStream)
			case "origin_keyword":
				row = append(row, snap.originKeyword)
			default:
				row = append(row, "")
			}
		}
	}
	if originConv {
		for _, col := range columnsOf("conversions", waveOrigin) {
			cols += ", " + col
			switch col {
			case "origin_group_id":
				if snap.originGid != "" {
					row = append(row, snap.originGid)
				} else {
					row = append(row, snap.gid)
				}
			case "origin_group_name":
				if snap.originGname != "" {
					row = append(row, snap.originGname)
				} else {
					row = append(row, snap.gname)
				}
			}
		}
	}
	batch, err := c.conn.PrepareBatch(insertCtx(ctx), `INSERT INTO conversions (`+cols+`)`)
	if err != nil {
		return err
	}
	if err := batch.Append(row...); err != nil {
		return err
	}
	return batch.Send()
}

// subColList is sub_id_1 through sub_id_12, comma-separated.
// It takes nothing and does not fail.
func subColList() string {
	return subColRange(1, clickWaveSubs)
}

// subTailList is sub_id_13 through sub_id_30, comma-separated.
// It takes nothing and does not fail. The click wave does not include these.
func subTailList() string {
	return subColRange(clickWaveSubs+1, config.MaxSubID)
}

// subColRange lists sub_id_from through sub_id_to, comma-separated.
// from and to are inclusive and from must be at least 1. It does not fail.
func subColRange(from, to int) string {
	if to < from {
		return ""
	}
	parts := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		parts = append(parts, fmt.Sprintf("sub_id_%d", i))
	}
	return strings.Join(parts, ", ")
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
	DepSum  float64   `json:"depsum"`
	CID     string    `json:"cid"`
	Status  string    `json:"status"`
	Offer   string    `json:"offer_id,omitempty"`
	Landing string    `json:"landing_id,omitempty"`
	Source  string    `json:"source_id,omitempty"`
	Network string    `json:"network_id,omitempty"`
	Subs    []string  `json:"subs,omitempty"`

	CampaignID       string    `json:"campaign_id,omitempty"`
	ClickTS          time.Time `json:"click_ts,omitzero"`
	OriginalStatus   string    `json:"original_status,omitempty"`
	PrevStatus       string    `json:"prev_status,omitempty"`
	TID              string    `json:"tid,omitempty"`
	IP               string    `json:"ip,omitempty"`
	Region           string    `json:"region,omitempty"`
	Lang             string    `json:"lang,omitempty"`
	OS               string    `json:"os,omitempty"`
	OSVersion        string    `json:"os_version,omitempty"`
	Browser          string    `json:"browser,omitempty"`
	BrowserVer       string    `json:"browser_ver,omitempty"`
	Brand            string    `json:"brand,omitempty"`
	UserAgent        string    `json:"useragent,omitempty"`
	Referer          string    `json:"referer,omitempty"`
	SE               string    `json:"se,omitempty"`
	Keyword          string    `json:"keyword,omitempty"`
	ASN              uint32    `json:"asn,omitempty"`
	Org              string    `json:"org,omitempty"`
	Site             string    `json:"site,omitempty"`
	AdCampaignID     string    `json:"ad_campaign_id,omitempty"`
	CreativeID       string    `json:"creative_id,omitempty"`
	ExternalID       string    `json:"external_id,omitempty"`
	XRW              string    `json:"xrw,omitempty"`
	DeviceModel      string    `json:"device_model,omitempty"`
	OriginCampaign   string    `json:"origin_campaign,omitempty"`
	OriginCampaignID string    `json:"origin_campaign_id,omitempty"`
	RootCID          string    `json:"root_cid,omitempty"`
	OriginStream     string    `json:"origin_stream,omitempty"`
	OriginKeyword    string    `json:"origin_keyword,omitempty"`
}
