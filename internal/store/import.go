package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/hasayone/empataso-tds/internal/config"
)

// ConversionInsert is one row written into the conversions table.
// TS is the row time; a zero TS is stored as the current UTC time.
// Status is stored lowercased and trimmed.
// OfferID, LandingID, SourceID, NetworkID and Subs are written only when
// the click columns exist. Subs is sub_id_1 through sub_id_12, in that order.
type ConversionInsert struct {
	TS                                            time.Time
	Domain, Page, Device, Operator, Country, City string
	Profit                                        float64
	GroupName, StreamName, CID, Status            string
	OfferID, LandingID, SourceID, NetworkID       string
	Subs                                          [config.MaxSubID]string
	// GroupID through XRW are the ext-wave columns. They are written only
	// when that wave is present. A zero ClickTS is stored as the Unix epoch.
	GroupID                                                   string
	ClickTS                                                   time.Time
	OriginalStatus, PrevStatus, TID                           string
	IP, Region, Lang, OS, OSVersion, Browser, BrowserV, Brand string
	UserAgent, Referer, SE, Keyword                           string
	ASN                                                       uint32
	Org, Site, AdCampaignID, CreativeID, ExternalID, XRW      string
	// DeviceModel is written only when the model wave is present.
	DeviceModel string
	// OriginGroupID and OriginGroupName are written only when the origin wave is present.
	OriginGroupID, OriginGroupName string
}

// conversionColumns returns the INSERT column list for conversions and how
// many values each row must supply.
// clickCols adds offer_id, landing_id, source_id, network_id and
// sub_id_1 through sub_id_12. Ext columns are not included; use
// conversionColumnsExt for those. cols is a comma-separated list; n is its
// length. The function does not touch the database.
func conversionColumns(clickCols bool) (cols string, n int) {
	return conversionColumnsExt(clickCols, false)
}

// conversionColumnsExt returns the INSERT column list and its length.
// click adds the click-wave columns. ext adds the ext-wave columns after
// them, so they follow sub_id_12 when click is set. cols is comma-separated.
// n is the number of names. The function does not touch the database.
func conversionColumnsExt(click, ext bool) (cols string, n int) {
	cols = "ts, domain, page, device, operator, country, city, profit, group_name, stream_name, cid, status"
	if click {
		cols += ", offer_id, landing_id, source_id, network_id, " + subColList()
	}
	if ext {
		cols += ", " + strings.Join(columnsOf("conversions", waveExt), ", ")
	}
	n = strings.Count(cols, ",") + 1
	return cols, n
}

// conversionValues builds one batch row in conversionColumns order.
// row supplies the fields. clickCols appends the entity ids and then Subs.
// A zero TS is replaced with the current UTC time. Status is lowercased
// and trimmed. Returns the values for batch.Append.
func conversionValues(row ConversionInsert, clickCols bool) []any {
	ts := row.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	vals := []any{
		ts, row.Domain, row.Page, row.Device, row.Operator, row.Country, row.City,
		row.Profit, row.GroupName, row.StreamName, row.CID,
		strings.ToLower(strings.TrimSpace(row.Status)),
	}
	if clickCols {
		vals = append(vals, row.OfferID, row.LandingID, row.SourceID, row.NetworkID)
		for i := 0; i < clickWaveSubs && i < len(row.Subs); i++ {
			vals = append(vals, row.Subs[i])
		}
	}
	return vals
}

// conversionValuesExt builds one batch row in conversionColumnsExt order.
// row supplies the fields. click and ext select the same waves as
// conversionColumnsExt. A zero TS becomes the current UTC time. A zero
// ClickTS becomes the Unix epoch. Status is lowercased and trimmed.
// Returns the values for batch.Append.
func conversionValuesExt(row ConversionInsert, click, ext bool) []any {
	vals := conversionValues(row, click)
	if !ext {
		return vals
	}
	for _, col := range columnsOf("conversions", waveExt) {
		vals = append(vals, conversionExtValue(row, col))
	}
	return vals
}

// conversionColumnsModel is conversionColumnsExt plus the model-wave columns.
// click and ext are as in conversionColumnsExt. model appends device_model
// after every other column. n counts every name. It does not touch the database.
func conversionColumnsModel(click, ext, model, origin bool) (cols string, n int) {
	cols, n = conversionColumnsExt(click, ext)
	if model {
		extra := columnsOf("conversions", waveModel)
		cols += ", " + strings.Join(extra, ", ")
		n += len(extra)
	}
	if origin {
		extra := columnsOf("conversions", waveOrigin)
		if len(extra) > 0 {
			cols += ", " + strings.Join(extra, ", ")
			n += len(extra)
		}
	}
	return cols, n
}

// conversionValuesModel builds one batch row in conversionColumnsModel order.
// row supplies the fields and the flags select the same waves as
// conversionColumnsModel. Returns the values for batch.Append.
func conversionValuesModel(row ConversionInsert, click, ext, model, origin bool) []any {
	vals := conversionValuesExt(row, click, ext)
	if model {
		for _, col := range columnsOf("conversions", waveModel) {
			vals = append(vals, conversionExtValue(row, col))
		}
	}
	if origin {
		for _, col := range columnsOf("conversions", waveOrigin) {
			vals = append(vals, conversionOriginValue(row, col))
		}
	}
	return vals
}

// conversionOriginValue returns one origin-wave column from row.
// col is origin_group_id or origin_group_name. Empty origin fields fall back
// to the serving campaign. An unknown column returns "".
func conversionOriginValue(row ConversionInsert, col string) any {
	switch col {
	case "origin_group_id":
		if row.OriginGroupID != "" {
			return row.OriginGroupID
		}
		return row.GroupID
	case "origin_group_name":
		if row.OriginGroupName != "" {
			return row.OriginGroupName
		}
		return row.GroupName
	default:
		return ""
	}
}

// conversionExtValue returns one ext-wave or model-wave value from row.
// col is the physical conversions column. A zero ClickTS is the Unix epoch,
// because ClickHouse DateTime cannot store year 1. An unknown column returns "".
func conversionExtValue(row ConversionInsert, col string) any {
	switch col {
	case "device_model":
		return row.DeviceModel
	case "group_id":
		return row.GroupID
	case "click_ts":
		if row.ClickTS.IsZero() {
			return time.Unix(0, 0).UTC()
		}
		return row.ClickTS
	case "original_status":
		return row.OriginalStatus
	case "prev_status":
		return row.PrevStatus
	case "tid":
		return row.TID
	case "ip":
		return row.IP
	case "region":
		return row.Region
	case "lang":
		return row.Lang
	case "os":
		return row.OS
	case "os_version":
		return row.OSVersion
	case "browser":
		return row.Browser
	case "browser_ver":
		return row.BrowserV
	case "brand":
		return row.Brand
	case "useragent":
		return row.UserAgent
	case "referer":
		return row.Referer
	case "se":
		return row.SE
	case "keyword":
		return row.Keyword
	case "asn":
		return row.ASN
	case "org":
		return row.Org
	case "site":
		return row.Site
	case "ad_campaign_id":
		return row.AdCampaignID
	case "creative_id":
		return row.CreativeID
	case "external_id":
		return row.ExternalID
	case "xrw":
		return row.XRW
	default:
		return ""
	}
}

// InsertConversions writes rows into conversions in one batch.
// ctx bounds the request. rows is the batch; an empty slice returns nil
// and does not use the connection. A nil receiver returns an error.
// Each status is lowercased and trimmed. A zero TS becomes the current UTC time.
// Offer, landing, source, network and subs are included only when click
// columns exist. Ext columns are included only when that wave is present.
// Returns an error when the batch cannot be prepared, appended or sent.
func (c *CH) InsertConversions(ctx context.Context, rows []ConversionInsert) error {
	if c == nil {
		return fmt.Errorf("clickhouse: nil client")
	}
	if len(rows) == 0 {
		return nil
	}
	if c.conn == nil {
		return fmt.Errorf("clickhouse: nil client")
	}
	c.maybeRefreshOriginWave()
	originConv := c.originConv.Load()
	cols, n := conversionColumnsModel(c.clickCols, c.extConv, c.modelConv, originConv)
	if c.clickCols && c.subConv {
		cols += ", " + subTailList()
		n += config.MaxSubID - clickWaveSubs
	}
	batch, err := c.conn.PrepareBatch(insertCtx(ctx), "INSERT INTO conversions ("+cols+")")
	if err != nil {
		return fmt.Errorf("clickhouse: prepare batch: %w", err)
	}
	for _, row := range rows {
		vals := conversionValuesModel(row, c.clickCols, c.extConv, c.modelConv, originConv)
		if c.clickCols && c.subConv {
			for i := clickWaveSubs; i < len(row.Subs); i++ {
				vals = append(vals, row.Subs[i])
			}
		}
		if len(vals) != n {
			return fmt.Errorf("clickhouse: conversion row has %d values, want %d", len(vals), n)
		}
		if err := batch.Append(vals...); err != nil {
			return fmt.Errorf("clickhouse: append: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("clickhouse: send: %w", err)
	}
	return nil
}

// PurgeImported deletes imported events and conversions for one tracker host.
// ctx bounds the request. domain is matched against the domain column.
// An empty or blank domain returns an error and runs no query.
// Both deletes are mutations and wait until they finish (mutations_sync = 1).
// Returns an error when domain is empty, the client has no connection, or
// either delete fails. If the events delete fails, conversions may already
// be gone; repeating the call removes whatever remains.
func (c *CH) PurgeImported(ctx context.Context, domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("clickhouse: empty domain")
	}
	if c == nil || c.conn == nil {
		return fmt.Errorf("clickhouse: nil client")
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"mutations_sync": 1,
	}))
	if err := c.conn.Exec(ctx, "ALTER TABLE conversions DELETE WHERE domain = ?", domain); err != nil {
		return fmt.Errorf("clickhouse: purge conversions: %w", err)
	}
	if err := c.conn.Exec(ctx, "ALTER TABLE events DELETE WHERE domain = ?", domain); err != nil {
		return fmt.Errorf("clickhouse: purge events: %w", err)
	}
	return nil
}

// CountByDomain counts events and conversions for one tracker host.
// ctx bounds the request. domain is matched against the domain column.
// A blank domain returns an error. A nil client returns an error.
// Returns the events count and the conversions count.
// If the events query fails, both counts are zero. If only the conversions
// query fails, the events count is still returned.
func (c *CH) CountByDomain(ctx context.Context, domain string) (events, conversions int64, err error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return 0, 0, fmt.Errorf("clickhouse: empty domain")
	}
	if c == nil || c.conn == nil {
		return 0, 0, fmt.Errorf("clickhouse: nil client")
	}
	var n uint64
	if err = c.conn.QueryRow(ctx, "SELECT count() FROM events WHERE domain = ?", domain).Scan(&n); err != nil {
		return 0, 0, fmt.Errorf("clickhouse: count events: %w", err)
	}
	events = int64(n)
	if err = c.conn.QueryRow(ctx, "SELECT count() FROM conversions FINAL WHERE domain = ?", domain).Scan(&n); err != nil {
		return events, 0, fmt.Errorf("clickhouse: count conversions: %w", err)
	}
	conversions = int64(n)
	return events, conversions, nil
}

// InitialWatermark returns the latest event and conversion timestamps for one
// tracker host within the last eight days.
// ctx bounds the queries. domain is matched against the domain column; blank
// domain skips both queries and returns zero times with nil error.
// A nil client or missing connection returns an error. When a table has no
// matching rows, that side stays zero.
func (c *CH) InitialWatermark(ctx context.Context, domain string) (clicks, conversions time.Time, err error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return time.Time{}, time.Time{}, nil
	}
	if c == nil || c.conn == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("clickhouse: nil client")
	}
	const window = "ts >= now() - INTERVAL 8 DAY"
	if err = c.conn.QueryRow(ctx,
		"SELECT maxOrNull(ts) FROM events WHERE domain = ? AND "+window, domain,
	).Scan(&clicks); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("clickhouse: watermark events: %w", err)
	}
	if err = c.conn.QueryRow(ctx,
		"SELECT maxOrNull(ts) FROM conversions WHERE domain = ? AND "+window, domain,
	).Scan(&conversions); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("clickhouse: watermark conversions: %w", err)
	}
	return normalizeCHDateTime(clicks), normalizeCHDateTime(conversions), nil
}

// normalizeCHDateTime maps ClickHouse zero/epoch timestamps to time.Time{}.
func normalizeCHDateTime(t time.Time) time.Time {
	if t.Unix() <= 0 {
		return time.Time{}
	}
	return t
}

const existingCIDsBatch = 1000

// ExistingCIDs returns click ids already stored for one host in a time window.
// ctx bounds the query. domain and cids must be non-empty; otherwise an empty
// map is returned without querying. from and to bound ts inclusively on both
// ends. A nil client returns an error. More than 1000 ids are queried in batches.
func (c *CH) ExistingCIDs(ctx context.Context, domain string, from, to time.Time, cids []string) (map[string]struct{}, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" || len(cids) == 0 {
		return map[string]struct{}{}, nil
	}
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse: nil client")
	}
	out := make(map[string]struct{}, len(cids))
	for i := 0; i < len(cids); i += existingCIDsBatch {
		end := i + existingCIDsBatch
		if end > len(cids) {
			end = len(cids)
		}
		part, err := c.existingCIDsBatch(ctx, domain, from, to, cids[i:end])
		if err != nil {
			return nil, err
		}
		for id := range part {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

func (c *CH) existingCIDsBatch(ctx context.Context, domain string, from, to time.Time, cids []string) (map[string]struct{}, error) {
	q := "SELECT cid FROM events WHERE domain = ? AND ts >= ? AND ts <= ? AND cid IN (" + placeholders(len(cids)) + ")"
	args := make([]any, 0, 3+len(cids))
	args = append(args, domain, from, to)
	for _, id := range cids {
		args = append(args, id)
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"max_execution_time": 30,
	}))
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: existing cids: %w", err)
	}
	defer rows.Close()
	out := make(map[string]struct{}, len(cids))
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, fmt.Errorf("clickhouse: scan cid: %w", err)
		}
		if cid != "" {
			out[cid] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clickhouse: existing cids rows: %w", err)
	}
	return out, nil
}
