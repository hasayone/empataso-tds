package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/egerkuzma/kuztds/internal/logbuf"
)

// CH — a ClickHouse client for batch-inserting events. Implements logbuf.Inserter.
type CH struct {
	conn driver.Conn
	// netCols: the events table has the asn/org/timezone/via columns. They were
	// added after the first release, so a database created before that has
	// them only if the ALTER in ensureSchema went through. Writes and reads
	// both consult this rather than assume — an INSERT naming a column that is
	// not there fails as a whole, and that would cost every event.
	netCols bool
	// clickCols: events and conversions carry sub_id_1..12 and the offer,
	// landing, source and network ids. Writes name those columns only after
	// ensureSchema has seen them, otherwise the whole insert fails.
	clickCols bool
}

// OpenCH opens a connection to ClickHouse.
func OpenCH(addr, database, username, password string) (*CH, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: database, Username: username, Password: password},
		DialTimeout: 5 * time.Second,
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse: open: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse: ping: %w", err)
	}
	c := &CH{conn: conn}
	c.ensureSchema(ctx)
	return c, nil
}

// netColumns — columns added to events after the first release.
const netColumns = `ADD COLUMN IF NOT EXISTS asn UInt32,
	ADD COLUMN IF NOT EXISTS org LowCardinality(String),
	ADD COLUMN IF NOT EXISTS timezone LowCardinality(String),
	ADD COLUMN IF NOT EXISTS via LowCardinality(String)`

// clickAlter adds sub_id_1..12 and the entity ids to events or conversions.
func clickAlter() string {
	parts := make([]string, 0, 16)
	for i := 1; i <= 12; i++ {
		parts = append(parts, fmt.Sprintf("ADD COLUMN IF NOT EXISTS sub_id_%d String", i))
	}
	for _, name := range []string{"offer_id", "landing_id", "source_id", "network_id"} {
		parts = append(parts, "ADD COLUMN IF NOT EXISTS "+name+" LowCardinality(String)")
	}
	return strings.Join(parts, ", ")
}

// clickColIn is the SQL list of those column names, for a system.columns check.
func clickColIn() string {
	parts := make([]string, 0, 16)
	for i := 1; i <= 12; i++ {
		parts = append(parts, fmt.Sprintf("'sub_id_%d'", i))
	}
	for _, name := range []string{"offer_id", "landing_id", "source_id", "network_id"} {
		parts = append(parts, "'"+name+"'")
	}
	return strings.Join(parts, ", ")
}

// ensureSchema brings an older events table up to date and records whether it
// worked. The ALTER is idempotent and metadata-only, so running it on every
// start from both the engine and the admin is harmless. If it is refused (a
// read-only user, say), the client keeps working on the old column set.
func (c *CH) ensureSchema(ctx context.Context) {
	_ = c.conn.Exec(ctx, "ALTER TABLE events "+netColumns)
	_ = c.conn.Exec(ctx, "ALTER TABLE events "+clickAlter())
	_ = c.conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS conversions (
		ts DateTime DEFAULT now(),
		domain String,
		page String,
		device LowCardinality(String),
		operator LowCardinality(String),
		country LowCardinality(String),
		city String,
		profit Float64,
		group_name LowCardinality(String),
		stream_name LowCardinality(String),
		cid String,
		status LowCardinality(String)
	) ENGINE = ReplacingMergeTree(ts)
	PARTITION BY toYYYYMM(ts)
	ORDER BY (cid, status)
	TTL ts + INTERVAL 365 DAY`)
	_ = c.conn.Exec(ctx, "ALTER TABLE conversions "+clickAlter())
	_ = c.conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS postback_log (
		ts DateTime DEFAULT now(),
		subid String,
		status LowCardinality(String),
		payout Float64,
		outcome LowCardinality(String),
		direction LowCardinality(String)
	) ENGINE = MergeTree
	PARTITION BY toYYYYMM(ts)
	ORDER BY ts
	TTL ts + INTERVAL 90 DAY`)
	var n uint64
	err := c.conn.QueryRow(ctx, `SELECT count() FROM system.columns
		WHERE database = currentDatabase() AND table = 'events'
		  AND name IN ('asn', 'org', 'timezone', 'via')`).Scan(&n)
	c.netCols = err == nil && n == 4
	var clicks, convClicks uint64
	err = c.conn.QueryRow(ctx, `SELECT count() FROM system.columns
		WHERE database = currentDatabase() AND table = 'events'
		  AND name IN (`+clickColIn()+`)`).Scan(&clicks)
	errConv := c.conn.QueryRow(ctx, `SELECT count() FROM system.columns
		WHERE database = currentDatabase() AND table = 'conversions'
		  AND name IN (`+clickColIn()+`)`).Scan(&convClicks)
	c.clickCols = err == nil && errConv == nil && clicks == 16 && convClicks == 16
}

// NetworkColumns reports whether events carry asn/org/timezone/via.
func (c *CH) NetworkColumns() bool { return c != nil && c.netCols }

// Close closes the connection.
func (c *CH) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// StatsSummary — aggregates over a period (for the admin dashboard).
type StatsSummary struct {
	Total  int64 `json:"total"`
	Unique int64 `json:"unique"`
	Bots   int64 `json:"bots"`
}

// Summary computes event aggregates over a period. The query is parameterized (no
// concatenation) — unlike the string-built SQL in the old admin panel.
func (c *CH) Summary(ctx context.Context, from, to time.Time) (StatsSummary, error) {
	// count()/countIf() in ClickHouse are UInt64, so we scan into uint64.
	var total, uniq, bots uint64
	row := c.conn.QueryRow(ctx,
		`SELECT count() AS total,
		        countIf(uniq = 1) AS uniq,
		        countIf(bot != '' AND bot != '-') AS bots
		 FROM events
		 WHERE ts >= ? AND ts < ?`,
		from, to,
	)
	if err := row.Scan(&total, &uniq, &bots); err != nil {
		return StatsSummary{}, err
	}
	return StatsSummary{Total: int64(total), Unique: int64(uniq), Bots: int64(bots)}, nil
}

// TSPoint — a time-series point for charts.
type TSPoint struct {
	TS     time.Time `json:"ts"`
	Total  int64     `json:"total"`
	Unique int64     `json:"unique"`
	Bots   int64     `json:"bots"`
}

// TimeSeries — aggregates over time (stepSec-second step) for a period. Across all groups.
func (c *CH) TimeSeries(ctx context.Context, from, to time.Time, stepSec int) ([]TSPoint, error) {
	if stepSec <= 0 {
		stepSec = 3600
	}
	rows, err := c.conn.Query(ctx,
		`SELECT toStartOfInterval(ts, INTERVAL ? second) AS t,
		        count() AS total, countIf(uniq=1) AS uniq,
		        countIf(bot!='' AND bot!='-') AS bots
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY t ORDER BY t`,
		stepSec, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TSPoint
	for rows.Next() {
		var p TSPoint
		var total, uniq, bots uint64
		if err := rows.Scan(&p.TS, &total, &uniq, &bots); err != nil {
			return nil, err
		}
		p.Total, p.Unique, p.Bots = int64(total), int64(uniq), int64(bots)
		out = append(out, p)
	}
	return out, rows.Err()
}

// KV — a key/count pair for breakdowns.
type KV struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// breakdownDims — an allowlist of columns for grouping (protection against injection).
var breakdownDims = map[string]string{
	"device": "device", "country": "country", "os": "os",
	"browser": "browser", "brand": "brand", "operator": "operator",
	"group": "group_name", "stream": "stream_name", "bot": "bot",
	"domain": "domain", "city": "city", "lang": "lang",
	"region": "region", "redirect": "redirect",
}

// networkDims — breakdowns that need the asn/org/timezone columns.
var networkDims = map[string]string{
	"org": "org", "timezone": "timezone",
	"asn": "if(asn = 0, '', concat('AS', toString(asn)))",
}

// DeleteGroupLogs deletes a group's events (ALTER ... DELETE, asynchronous
// mutation). The key is the group's ID — the stable routing identifier the
// admin panel knows — not its display name, which is free text and may be
// empty or shared.
func (c *CH) DeleteGroupLogs(ctx context.Context, group string) error {
	return c.conn.Exec(ctx, "ALTER TABLE events DELETE WHERE group_id = ?", group)
}

// PerfRow — one group/stream cell of the performance table: traffic from
// events, conversions from postbacks. Stream "-" is the group's fallback
// (no stream matched).
type PerfRow struct {
	GroupID string  `json:"group_id"`
	Group   string  `json:"group"`
	Stream  string  `json:"stream"`
	Hits    int64   `json:"hits"`
	Unique  int64   `json:"unique"`
	Bots    int64   `json:"bots"`
	Conv    int64   `json:"conv"`
	Profit  float64 `json:"profit"`
	// Forwarded — visits this stream handed to another group (type "group").
	// They are logged under the group that served them; here they are added
	// to Hits/Unique/Bots of the stream that sent them on, so a flow's
	// numbers show everything that went through it.
	Forwarded int64 `json:"forwarded"`
}

// Performance aggregates events and postbacks per group and stream over a
// period. Two GROUP BY queries merged in Go rather than one JOIN: postbacks
// carry no group_id, and a JOIN across two MergeTree tables on free-text
// names is slower on ClickHouse than two scans plus a map lookup. Rows are
// ordered by hits, descending; a stream with conversions but no events in
// the window still appears, with zero hits.
func (c *CH) Performance(ctx context.Context, from, to time.Time) ([]PerfRow, error) {
	rows, err := c.conn.Query(ctx,
		`SELECT group_id, group_name, stream_name,
		        count() AS hits, countIf(uniq = 1) AS uniq,
		        countIf(bot != '' AND bot != '-') AS bots
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY group_id, group_name, stream_name
		 ORDER BY hits DESC LIMIT 2000`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ group, stream string }
	var out []PerfRow
	index := map[key]int{}
	for rows.Next() {
		var p PerfRow
		var hits, uniq, bots uint64
		if err := rows.Scan(&p.GroupID, &p.Group, &p.Stream, &hits, &uniq, &bots); err != nil {
			return nil, err
		}
		p.Hits, p.Unique, p.Bots = int64(hits), int64(uniq), int64(bots)
		index[key{p.Group, p.Stream}] = len(out)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if c.netCols {
		if err := c.addForwarded(ctx, from, to, &out); err != nil {
			return nil, err
		}
	}
	pbs, err := c.conn.Query(ctx,
		`SELECT group_name, stream_name, count() AS conv, sum(profit) AS profit
		 FROM (
		   SELECT group_name, stream_name, profit, ts FROM postbacks
		   UNION ALL
		   SELECT group_name, stream_name, profit, ts FROM conversions
		 ) WHERE ts >= ? AND ts < ?
		 GROUP BY group_name, stream_name`, from, to)
	if err != nil {
		return nil, err
	}
	defer pbs.Close()
	for pbs.Next() {
		var group, stream string
		var conv uint64
		var profit float64
		if err := pbs.Scan(&group, &stream, &conv, &profit); err != nil {
			return nil, err
		}
		i, ok := index[key{group, stream}]
		if !ok {
			i = len(out)
			index[key{group, stream}] = i
			out = append(out, PerfRow{Group: group, Stream: stream})
		}
		out[i].Conv, out[i].Profit = int64(conv), profit
	}
	return out, pbs.Err()
}

// Breakdown — a breakdown by dimension dim over a period (top limit).
func (c *CH) Breakdown(ctx context.Context, from, to time.Time, dim string, limit int) ([]KV, error) {
	col, ok := breakdownDims[dim]
	if !ok && c.netCols {
		col, ok = networkDims[dim]
	}
	if !ok {
		return nil, fmt.Errorf("store: unknown dimension %q", dim)
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := c.conn.Query(ctx,
		`SELECT `+col+` AS k, count() AS n FROM events
		 WHERE ts >= ? AND ts < ? GROUP BY k ORDER BY n DESC LIMIT ?`,
		from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KV
	for rows.Next() {
		var kv KV
		var n uint64
		if err := rows.Scan(&kv.Key, &n); err != nil {
			return nil, err
		}
		kv.Count = int64(n)
		out = append(out, kv)
	}
	return out, rows.Err()
}

// addForwarded adds the visits each linking stream sent on to another group.
func (c *CH) addForwarded(ctx context.Context, from, to time.Time, out *[]PerfRow) error {
	rows, err := c.conn.Query(ctx,
		`SELECT via, count() AS hits, countIf(uniq = 1) AS uniq,
		        countIf(bot != '' AND bot != '-') AS bots
		 FROM events WHERE ts >= ? AND ts < ? AND via != ''
		 GROUP BY via`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var via string
		var hits, uniq, bots uint64
		if err := rows.Scan(&via, &hits, &uniq, &bots); err != nil {
			return err
		}
		gid, stream, ok := strings.Cut(via, "/")
		if !ok {
			continue
		}
		i := -1
		for k := range *out {
			if (*out)[k].GroupID == gid && (*out)[k].Stream == stream {
				i = k
				break
			}
		}
		if i < 0 {
			*out = append(*out, PerfRow{GroupID: gid, Stream: stream})
			i = len(*out) - 1
		}
		p := &(*out)[i]
		p.Hits += int64(hits)
		p.Unique += int64(uniq)
		p.Bots += int64(bots)
		p.Forwarded += int64(hits)
	}
	return rows.Err()
}

// LogFilter — log-viewing filters (all optional). Categorical fields are
// value lists (multi-select → SQL IN), IP is a single substring.
type LogFilter struct {
	From, To             time.Time
	Group, Stream        []string
	Country, Device      []string
	OS, Browser, Brand   []string
	IP                   string
	BotsOnly, HumansOnly bool
	Limit, Offset        int
}

const (
	// defaultLogRows — one page of the interactive log table.
	defaultLogRows = 100
	// maxLogRows bounds a single Logs call. The ceiling is the size of a CSV
	// export, not of a page: the export legitimately asks for a whole period,
	// and clamping it down to a page size silently truncated every export to
	// defaultLogRows rows. Paginating the interactive endpoint is the admin
	// layer's job (see maxPageRows there).
	maxLogRows = 50000
)

// clampLogLimit bounds a requested row count to something the process can hold.
func clampLogLimit(limit int) int {
	if limit <= 0 {
		return defaultLogRows
	}
	if limit > maxLogRows {
		return maxLogRows
	}
	return limit
}

// LogRow — a log row for the table.
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

	// The rest is for the row's detail view, not the table.
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
}

// Logs returns log rows matching the filter and the total number of matching rows.
// All conditions are parameterized (no concatenation of values).
func (c *CH) Logs(ctx context.Context, f LogFilter) ([]LogRow, int64, error) {
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

	var total uint64
	if err := c.conn.QueryRow(ctx, "SELECT count() FROM events WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := clampLogLimit(f.Limit)
	net := ", 0 AS asn, '' AS org, '' AS timezone"
	if c.netCols {
		net = ", asn, org, timezone"
	}
	q := "SELECT ts, group_name, stream_name, country, city, device, os, browser, brand, bot, uniq, ip, keyword, out," +
		" region, lang, operator, redirect, referer, useragent, domain, cid" + net +
		" FROM events WHERE " + where + " ORDER BY ts DESC LIMIT ? OFFSET ?"
	rows, err := c.conn.Query(ctx, q, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var r LogRow
		if err := rows.Scan(&r.TS, &r.Group, &r.Stream, &r.Country, &r.City, &r.Device,
			&r.OS, &r.Browser, &r.Brand, &r.Bot, &r.Uniq, &r.IP, &r.Keyword, &r.Out,
			&r.Region, &r.Lang, &r.Operator, &r.Redirect, &r.Referer, &r.UserAgent, &r.Domain, &r.CID,
			&r.ASN, &r.Org, &r.Timezone); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, int64(total), rows.Err()
}

// RecordPostback finds an event by cid and writes one conversion.
// status is part of the storage key, so a later sale does not replace an earlier lead.
// The returned group id is the campaign the click belonged to.
// When the click columns exist, sub ids and offer, landing, source and network
// are copied from that click. Older clicks leave those fields empty.
func (c *CH) RecordPostback(ctx context.Context, cid string, profit float64, status string) (string, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	var gid, gname, sname, country, city, device, operator, domain, page string
	sel := `SELECT group_id, group_name, stream_name, country, city, device, operator, domain, page`
	dest := []any{&gid, &gname, &sname, &country, &city, &device, &operator, &domain, &page}
	var offer, landing, source, network string
	subs := make([]string, 12)
	if c.clickCols {
		sel += `, offer_id, landing_id, source_id, network_id, ` + subColList()
		dest = append(dest, &offer, &landing, &source, &network)
		for i := range subs {
			dest = append(dest, &subs[i])
		}
	}
	err := c.conn.QueryRow(ctx, sel+` FROM events WHERE cid = ? ORDER BY ts DESC LIMIT 1`, cid).Scan(dest...)
	if err != nil {
		return "", err
	}
	cols := `domain, page, device, operator, country, city, profit, group_name, stream_name, cid, status`
	row := []any{domain, page, device, operator, country, city, profit, gname, sname, cid, status}
	if c.clickCols {
		cols += `, offer_id, landing_id, source_id, network_id, ` + subColList()
		row = append(row, offer, landing, source, network)
		for _, s := range subs {
			row = append(row, s)
		}
	}
	batch, err := c.conn.PrepareBatch(ctx, `INSERT INTO conversions (`+cols+`)`)
	if err != nil {
		return "", err
	}
	if err := batch.Append(row...); err != nil {
		return "", err
	}
	if err := batch.Send(); err != nil {
		return "", err
	}
	return gid, nil
}

// PostbackRow — a conversion row.
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
}

// PostbackQuery filters the conversion journal.
// CID is the click id, shown in the admin as subid.
// Offer, landing, source and network match only after those columns exist.
type PostbackQuery struct {
	From, To                                                    time.Time
	Group, Status, Stream, Offer, Landing, Source, Network, CID string
	Limit                                                       int
}

// Postbacks returns conversions over a period (optional group filter) and the total profit.
func (c *CH) Postbacks(ctx context.Context, from, to time.Time, group string, limit int) ([]PostbackRow, float64, error) {
	return c.QueryPostbacks(ctx, PostbackQuery{From: from, To: to, Group: group, Limit: limit})
}

// QueryPostbacks returns conversions matching q and the total profit.
// A filter on offer, landing, source or network yields no rows when those
// columns are not on the table yet, instead of failing the query.
func (c *CH) QueryPostbacks(ctx context.Context, q PostbackQuery) ([]PostbackRow, float64, error) {
	if !c.clickCols && (q.Offer != "" || q.Landing != "" || q.Source != "" || q.Network != "") {
		return []PostbackRow{}, 0, nil
	}
	q.Status = strings.ToLower(strings.TrimSpace(q.Status))
	where, args := postbackWhere(q, c.clickCols)
	var total float64
	if err := c.conn.QueryRow(ctx, "SELECT sum(profit) FROM ("+conversionFrom(c.clickCols)+") WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	sel := "ts, group_name, stream_name, domain, page, device, country, city, profit, cid, status"
	if c.clickCols {
		sel += ", offer_id, landing_id, source_id, network_id, " + subColList()
	}
	rows, err := c.conn.Query(ctx,
		"SELECT "+sel+" FROM ("+conversionFrom(c.clickCols)+") WHERE "+where+" ORDER BY ts DESC LIMIT ?", append(args, limit)...)
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
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, err
		}
		if c.clickCols {
			p.Subs = subs
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// postbackWhere builds the period predicate and the optional equality filters.
// click must be true before offer, landing, source or network are included.
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

func subColList() string {
	parts := make([]string, 12)
	for i := 1; i <= 12; i++ {
		parts[i-1] = fmt.Sprintf("sub_id_%d", i)
	}
	return strings.Join(parts, ", ")
}

// conversionFrom is the old postbacks table plus one row per cid and status.
// click adds empty entity columns on the old table so the union lines up.
func conversionFrom(click bool) string {
	base := `ts, domain, page, device, operator, country, city, profit, group_name, stream_name, cid`
	if !click {
		return `SELECT ` + base + `, '' AS status FROM postbacks
		UNION ALL
		SELECT ` + base + `, status FROM conversions`
	}
	emptyParts := []string{"'' AS offer_id", "'' AS landing_id", "'' AS source_id", "'' AS network_id"}
	for i := 1; i <= 12; i++ {
		emptyParts = append(emptyParts, fmt.Sprintf("'' AS sub_id_%d", i))
	}
	empty := strings.Join(emptyParts, ", ")
	ids := `offer_id, landing_id, source_id, network_id, ` + subColList()
	return `SELECT ` + base + `, '' AS status, ` + empty + ` FROM postbacks
		UNION ALL
		SELECT ` + base + `, status, ` + ids + ` FROM conversions`
}

// InsertEvents inserts a batch of events in a single batch request (lock-free).
func (c *CH) InsertEvents(ctx context.Context, events []logbuf.Event) error {
	if len(events) == 0 {
		return nil
	}
	cols := `ts, group_id, group_name, stream_name, out, keyword, redirect, device,
		 operator, country, city, region, lang, uniq, bot, ip, referer, useragent,
		 domain, page, se, os, os_version, browser, browser_ver, brand,
		 counter, cid, postback`
	if c.netCols {
		cols += ", asn, org, timezone, via"
	}
	if c.clickCols {
		cols += ", " + subColList() + ", offer_id, landing_id, source_id, network_id"
	}
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO events ("+cols+")")
	if err != nil {
		return fmt.Errorf("clickhouse: prepare batch: %w", err)
	}
	for _, e := range events {
		row := []any{
			e.Ts, e.GroupID, e.GroupName, e.Stream, e.Out, e.Keyword, e.Redirect,
			e.Device, e.Operator, e.Country, e.City, e.Region, e.Lang, e.Uniq,
			e.Bot, e.IP, e.Referer, e.UserAgent, e.Domain, e.Page, e.SE,
			e.OS, e.OSVersion, e.Browser, e.BrowserV, e.Brand,
			e.Counter, e.CID, e.Postback,
		}
		if c.netCols {
			row = append(row, e.ASN, e.Org, e.Timezone, e.Via)
		}
		if c.clickCols {
			for _, s := range e.Subs {
				row = append(row, s)
			}
			row = append(row, e.OfferID, e.LandingID, e.SourceID, e.NetworkID)
		}
		if err := batch.Append(row...); err != nil {
			return fmt.Errorf("clickhouse: append: %w", err)
		}
	}
	return batch.Send()
}
