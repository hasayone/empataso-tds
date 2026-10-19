package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/logbuf"
)

// LogFilter selects rows of the click journal. Every field is optional.
// Categorical slices become SQL IN. IP is one exact value.
// Conds are the extra field filters from the admin. BotsOnly and HumansOnly
// restrict bot. Limit and Offset page the result; Limit is clamped.
type LogFilter struct {
	From, To             time.Time
	Group, Stream        []string
	Country, Device      []string
	OS, Browser, Brand   []string
	IP                   string
	BotsOnly, HumansOnly bool
	Limit, Offset        int
	Sort                 string
	Desc                 bool
	Conds                []Cond
}

const (
	// defaultLogRows is one page of the interactive log table.
	defaultLogRows = 100
	// maxLogRows bounds a single Logs call. The ceiling is the size of a CSV
	// export, not of a page: the export legitimately asks for a whole period,
	// and clamping it down to a page size silently truncated every export to
	// defaultLogRows rows. Paginating the interactive endpoint is the admin
	// layer's job (see maxPageRows there).
	maxLogRows = 50000
)

// clampLogLimit bounds a requested row count to something the process can hold.
// limit <= 0 becomes one page. limit above maxLogRows becomes maxLogRows.
// It does not fail.
func clampLogLimit(limit int) int {
	if limit <= 0 {
		return defaultLogRows
	}
	if limit > maxLogRows {
		return maxLogRows
	}
	return limit
}

// LogRow is one click for the journal table and its detail view.
// Existing JSON names stay as they are. The later fields are omitted when
// empty: page and the client hints are always read, via needs the net wave,
// the click and ext waves appear only after those columns exist.
type LogRow struct {
	TS      time.Time `json:"ts"`
	Group   string    `json:"group"`
	Stream  string    `json:"stream"`
	Country string    `json:"country"`
	City    string    `json:"city"`
	Device  string    `json:"device"`
	OS      string    `json:"os"`
	Browser string    `json:"browser"`
	Brand   string    `json:"brand"`
	Bot     string    `json:"bot"`
	Uniq    uint8     `json:"uniq"`
	IP      string    `json:"ip"`
	Keyword string    `json:"keyword"`
	Out     string    `json:"out"`

	Region    string `json:"region"`
	Lang      string `json:"lang"`
	Operator  string `json:"operator"`
	Redirect  string `json:"redirect"`
	Referer   string `json:"referer"`
	UserAgent string `json:"useragent"`
	Domain    string `json:"domain"`
	CID       string `json:"cid"`
	ASN       uint32 `json:"asn"`
	Org       string `json:"org"`
	Timezone  string `json:"timezone"`

	Page         string   `json:"page,omitempty"`
	OSVersion    string   `json:"os_version,omitempty"`
	BrowserVer   string   `json:"browser_ver,omitempty"`
	SE           string   `json:"se,omitempty"`
	Via                string `json:"via,omitempty"`
	OriginCampaign     string `json:"origin_campaign,omitempty"`
	OriginCampaignID   string `json:"origin_campaign_id,omitempty"`
	CampaignID         string `json:"campaign_id,omitempty"`
	OfferID      string   `json:"offer_id,omitempty"`
	LandingID    string   `json:"landing_id,omitempty"`
	SourceID     string   `json:"source_id,omitempty"`
	NetworkID    string   `json:"network_id,omitempty"`
	Subs         []string `json:"subs,omitempty"`
	Site         string   `json:"site,omitempty"`
	AdCampaignID string   `json:"ad_campaign_id,omitempty"`
	CreativeID   string   `json:"creative_id,omitempty"`
	ExternalID   string   `json:"external_id,omitempty"`
	XRW          string   `json:"xrw,omitempty"`
	DeviceModel  string   `json:"device_model,omitempty"`
}

// Logs returns the click rows that match f and the total number of matches.
// ctx bounds both queries. Conditions are parameterized. A condition on a
// wave that is not ready returns an empty page and a zero total without
// querying. ErrUnknownField and ErrBadCond come from the extra conditions.
// A query error is returned as-is.
func (c *CH) Logs(ctx context.Context, f LogFilter) ([]LogRow, int64, error) {
	c.maybeRefreshOriginWave()
	originEvents := c.originEvents.Load()
	where := "ts >= ? AND ts < ?"
	args := []any{f.From, f.To}
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		ph := make([]string, len(vals))
		for i, v := range vals {
			ph[i] = "?"
			args = append(args, v)
		}
		where += " AND " + col + " IN (" + strings.Join(ph, ",") + ")"
	}
	eq := func(col, val string) {
		if val != "" {
			where += " AND " + col + " = ?"
			args = append(args, val)
		}
	}
	in("group_name", f.Group)
	in("stream_name", f.Stream)
	in("country", f.Country)
	in("device", f.Device)
	in("os", f.OS)
	in("browser", f.Browser)
	in("brand", f.Brand)
	eq("ip", f.IP)
	if f.BotsOnly {
		where += " AND bot != '' AND bot != '-'"
	}
	if f.HumansOnly {
		where += " AND (bot = '' OR bot = '-')"
	}
	extra, extraArgs, skip, err := buildWhere(c, "events", f.Conds)
	if err != nil {
		return nil, 0, err
	}
	if skip {
		return []LogRow{}, 0, nil
	}
	where += extra
	args = append(args, extraArgs...)

	var total uint64
	if err := c.conn.QueryRow(ctx, "SELECT count() FROM events WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := clampLogLimit(f.Limit)
	var extCols []string
	if c.extEvents {
		extCols = columnsOf("events", waveExt)
	}
	sel := logSelect(c, extCols, originEvents)
	order, slow, err := c.journalOrderByCH("events", f.Sort, f.Desc)
	if err != nil {
		return nil, 0, err
	}
	q := "SELECT " + sel + " FROM events WHERE " + where + " ORDER BY " + order + " LIMIT ? OFFSET ?"
	if slow {
		q += " SETTINGS max_execution_time=20"
	}
	rows, err := c.conn.Query(ctx, q, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var r LogRow
		dest, subs := logDest(c, &r, extCols, originEvents)
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		if subs != nil {
			r.Subs = subs
		}
		out = append(out, r)
	}
	return out, int64(total), rows.Err()
}

// logSelect is the click SELECT list.
// c decides which optional waves are named. extCols is the events ext list
// for this query, computed once by the caller; nil omits that wave.
// page, os_version, browser_ver, se and group_id are always present on events.
// asn/org/timezone are stubbed when the net wave is missing; via is selected
// only with that wave; device_model is selected only with the model wave.
// It does not fail.
func logSelect(c *CH, extCols []string, originEvents bool) string {
	cols := []string{
		"ts", "group_name", "stream_name", "country", "city", "device", "os", "browser",
		"brand", "bot", "uniq", "ip", "keyword", "out", "region", "lang", "operator",
		"redirect", "referer", "useragent", "domain", "cid",
	}
	if c.netCols {
		cols = append(cols, "asn", "org", "timezone", "via")
	} else {
		cols = append(cols, "toUInt32(0) AS asn", "'' AS org", "'' AS timezone")
	}
	cols = append(cols, "page", "os_version", "browser_ver", "se", "group_id")
	if c.clickCols {
		for i := 1; i <= 12; i++ {
			cols = append(cols, fmt.Sprintf("sub_id_%d", i))
		}
		cols = append(cols, "offer_id", "landing_id", "source_id", "network_id")
	}
	cols = append(cols, extCols...)
	if c.modelEvents {
		cols = append(cols, "device_model")
	}
	if originEvents {
		cols = append(cols, columnsOf("events", waveOrigin)...)
	} else {
		cols = append(cols, "'' AS origin_group_name", "'' AS origin_group_id")
	}
	return strings.Join(cols, ", ")
}

// logDest returns Scan targets matching logSelect, and the subs buffer when
// the click wave is selected. c is the same client. r receives the row.
// extCols is the events ext list for this query, the same slice logSelect
// used. The subs slice is nil when click columns are off; its elements are
// the sub_id targets and stay valid until Scan returns. It does not fail.
func logDest(c *CH, r *LogRow, extCols []string, originEvents bool) ([]any, []string) {
	dest := []any{
		&r.TS, &r.Group, &r.Stream, &r.Country, &r.City, &r.Device, &r.OS, &r.Browser,
		&r.Brand, &r.Bot, &r.Uniq, &r.IP, &r.Keyword, &r.Out, &r.Region, &r.Lang,
		&r.Operator, &r.Redirect, &r.Referer, &r.UserAgent, &r.Domain, &r.CID,
	}
	if c.netCols {
		dest = append(dest, &r.ASN, &r.Org, &r.Timezone, &r.Via)
	} else {
		dest = append(dest, &r.ASN, &r.Org, &r.Timezone)
	}
	dest = append(dest, &r.Page, &r.OSVersion, &r.BrowserVer, &r.SE, &r.CampaignID)
	var subs []string
	if c.clickCols {
		subs = make([]string, 12)
		for i := range subs {
			dest = append(dest, &subs[i])
		}
		dest = append(dest, &r.OfferID, &r.LandingID, &r.SourceID, &r.NetworkID)
	}
	for _, col := range extCols {
		dest = append(dest, logExtPtr(r, col))
	}
	if c.modelEvents {
		dest = append(dest, &r.DeviceModel)
	}
	if originEvents {
		dest = append(dest, &r.OriginCampaign, &r.OriginCampaignID)
	} else {
		var stubName, stubID string
		dest = append(dest, &stubName, &stubID)
	}
	return dest, subs
}

// logExtPtr is the Scan target of one ext events column.
// r is the row. col is the physical name. Every known ext column is a
// distinct string field. An unknown column returns a fresh string and
// does not overwrite xrw. It does not fail.
func logExtPtr(r *LogRow, col string) any {
	switch col {
	case "site":
		return &r.Site
	case "ad_campaign_id":
		return &r.AdCampaignID
	case "creative_id":
		return &r.CreativeID
	case "external_id":
		return &r.ExternalID
	case "xrw":
		return &r.XRW
	default:
		return new(string)
	}
}

// eventBaseCols is the events INSERT prefix that has been on the table
// since the first release. Order matches the historical batch.
var eventBaseCols = []string{
	"ts", "group_id", "group_name", "stream_name", "out", "keyword", "redirect",
	"device", "operator", "country", "city", "region", "lang", "uniq", "bot", "ip",
	"referer", "useragent", "domain", "page", "se", "os", "os_version", "browser",
	"browser_ver", "brand", "counter", "cid", "postback",
}

// eventColumns is the events INSERT column list for this client.
// c's wave flags append net, then click (subs before the ids), then cost,
// then ext, then model, then step. A nil client returns only the base columns.
func eventColumns(c *CH, originEvents bool) []string {
	cols := append([]string(nil), eventBaseCols...)
	if c == nil {
		return cols
	}
	if c.netCols {
		cols = append(cols, columnsOf("events", waveNet)...)
	}
	if c.clickCols {
		cols = append(cols, columnsOf("events", waveClick)...)
	}
	if c.costCols {
		cols = append(cols, columnsOf("events", waveCost)...)
	}
	if c.extEvents {
		cols = append(cols, columnsOf("events", waveExt)...)
	}
	if c.modelEvents {
		cols = append(cols, columnsOf("events", waveModel)...)
	}
	if c.stepEvents {
		cols = append(cols, columnsOf("events", waveStep)...)
	}
	if originEvents {
		cols = append(cols, columnsOf("events", waveOrigin)...)
	}
	return cols
}

// eventValues returns one batch row in cols order.
// cols is the INSERT list for this batch, computed once by the caller.
// e is the click. The length matches len(cols). It does not fail.
func eventValues(cols []string, e logbuf.Event) []any {
	vals := make([]any, len(cols))
	for i, col := range cols {
		vals[i] = eventValue(e, col)
	}
	return vals
}

// eventValue returns the Go value of one base events column.
// e is the click. col is the physical name. sub_id_N reads Subs.
// A column after postback goes to eventWaveValue. An unknown column returns "".
func eventValue(e logbuf.Event, col string) any {
	if strings.HasPrefix(col, "sub_id_") {
		n, err := strconv.Atoi(strings.TrimPrefix(col, "sub_id_"))
		if err != nil || n < 1 || n > len(e.Subs) {
			return ""
		}
		return e.Subs[n-1]
	}
	switch col {
	case "ts":
		return e.Ts
	case "group_id":
		return e.GroupID
	case "group_name":
		return e.GroupName
	case "stream_name":
		return e.Stream
	case "out":
		return e.Out
	case "keyword":
		return e.Keyword
	case "redirect":
		return e.Redirect
	case "device":
		return e.Device
	case "operator":
		return e.Operator
	case "country":
		return e.Country
	case "city":
		return e.City
	case "region":
		return e.Region
	case "lang":
		return e.Lang
	case "uniq":
		return e.Uniq
	case "bot":
		return e.Bot
	case "ip":
		return e.IP
	case "referer":
		return e.Referer
	case "useragent":
		return e.UserAgent
	case "domain":
		return e.Domain
	case "page":
		return e.Page
	case "se":
		return e.SE
	case "os":
		return e.OS
	case "os_version":
		return e.OSVersion
	case "browser":
		return e.Browser
	case "browser_ver":
		return e.BrowserV
	case "brand":
		return e.Brand
	case "counter":
		return e.Counter
	case "cid":
		return e.CID
	case "postback":
		return e.Postback
	default:
		return eventWaveValue(e, col)
	}
}

// eventWaveValue returns the Go value of one wave column on events.
// e is the click. col is the physical name. An unknown column returns "".
func eventWaveValue(e logbuf.Event, col string) any {
	switch col {
	case "asn":
		return e.ASN
	case "org":
		return e.Org
	case "timezone":
		return e.Timezone
	case "via":
		return e.Via
	case "offer_id":
		return e.OfferID
	case "landing_id":
		return e.LandingID
	case "source_id":
		return e.SourceID
	case "network_id":
		return e.NetworkID
	case "cost_cents":
		return e.CostCents
	case "cost_cur":
		return e.CostCur
	case "site":
		return e.Site
	case "ad_campaign_id":
		return e.AdCampaignID
	case "creative_id":
		return e.CreativeID
	case "external_id":
		return e.ExternalID
	case "xrw":
		return e.XRW
	case "device_model":
		return e.DeviceModel
	case "step":
		return e.Step
	case "origin_group_id":
		if e.OriginID != "" {
			return e.OriginID
		}
		return e.GroupID
	case "origin_group_name":
		if e.OriginName != "" {
			return e.OriginName
		}
		return e.GroupName
	default:
		return ""
	}
}

// InsertEvents writes events in one batch.
// ctx bounds the request. An empty slice returns nil and does not use the
// connection. Column names follow the waves this client has probed.
// Returns an error when the batch cannot be prepared, appended or sent.
func (c *CH) InsertEvents(ctx context.Context, events []logbuf.Event) error {
	if len(events) == 0 {
		return nil
	}
	c.maybeRefreshOriginWave()
	originEvents := c.originEvents.Load()
	names := eventColumns(c, originEvents)
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO events ("+strings.Join(names, ", ")+")")
	if err != nil {
		return fmt.Errorf("clickhouse: prepare batch: %w", err)
	}
	for _, e := range events {
		if err := batch.Append(eventValues(names, e)...); err != nil {
			return fmt.Errorf("clickhouse: append: %w", err)
		}
	}
	return batch.Send()
}
