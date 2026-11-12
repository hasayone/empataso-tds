package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

// PostbackIn is one conversion to record from a click id.
// CID selects the click. Profit is the payout. DepSum is the deposit and is
// not the payout. Status is stored lowercased and trimmed. Original is the
// raw status; when it is empty, Status is kept as the raw value. TID is the
// network transaction id.
type PostbackIn struct {
	CID              string
	Profit           float64
	DepSum           float64
	Status, Original string
	TID              string
}

// RecordConversion finds the click cid and writes one conversion.
// ctx bounds the reads and the insert. in supplies payout, deposit, status and tid.
// The returned group id is the campaign the click belonged to.
// Base click columns are always read. Net, click and ext-event columns are
// read only when that wave is ready. Ext conversion columns, including
// prev_status, are written only when the conversions ext wave is ready.
// prev_status is the latest stored status for the same cid, or "" when this
// is the first row. An unknown cid returns the driver's error. A batch error
// is returned as-is and nothing is reported as stored.
func (c *CH) RecordConversion(ctx context.Context, in PostbackIn) (string, error) {
	c.maybeRefreshOriginWave()
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
// earlier lead. A newer rejected row drops that lead and sale from revenue,
// from lead and sale counts, and from the conversion journal.
// The returned group id is the campaign the click belonged to.
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
	subs                                        [config.MaxSubID]string
	site, adCampaign, creative, externalID, xrw string
	deviceModel                                 string
	originGid, originGname                      string
}

// readClick loads the newest event with cid.
// ctx bounds the query. The base columns, including the ones the ext
// conversion wave copies, are always selected. Net, click and ext-event
// columns are added only when that wave is ready.
// An unknown cid returns the driver's error (no row).
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

// prevStatus returns the latest conversion status for cid.
// ctx bounds the query. No row returns "" and a nil error.
// Any other scan error is returned. This point lookup omits FINAL so the
// newest physical row is seen before ReplacingMergeTree merges parts.
func (c *CH) prevStatus(ctx context.Context, cid string) (string, error) {
	var status string
	err := c.conn.QueryRow(ctx, `SELECT status FROM conversions WHERE cid = ? ORDER BY ts DESC LIMIT 1`, cid).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

// writeConversion inserts one conversion copied from snap.
// ctx bounds the batch. in supplies profit, the deposit, status and tid.
// prev is the previous status, written only with the ext wave. Status is
// stored lowercased and trimmed; original_status keeps the raw text.
// depsum is written only when that wave is ready and is never part of the
// base column list. The row time is the server default. Returns a batch
// error unchanged.
func (c *CH) writeConversion(ctx context.Context, snap clickSnap, in PostbackIn, prev string) error {
	originConv := c.originConv.Load()
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
	batch, err := c.conn.PrepareBatch(ctx, `INSERT INTO conversions (`+cols+`)`)
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
}
