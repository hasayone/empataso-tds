package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
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
	// costCols: events carry cost_cents and cost_cur. Writes name them only
	// after ensureSchema has seen both, otherwise the whole insert fails.
	costCols bool
	// extEvents: events carry the ext wave (site and the ad identifiers).
	// Writes name those columns only after ensureSchema has seen them.
	extEvents bool
	// extConv: conversions carry the ext wave. Writes and the journal select
	// name those columns only after ensureSchema has seen them.
	extConv bool
	// modelEvents and modelConv: events and conversions carry device_model.
	// Writes and the journal name the column only after ensureSchema has seen
	// it, otherwise the whole insert or select fails.
	modelEvents, modelConv bool
	// depsumConv: conversions carry depsum, the deposit. Writes and sums name
	// the column only after ensureSchema has seen it, otherwise the insert or
	// select fails. It is not the payout column.
	depsumConv bool
	// stepEvents: events carry step (0 first step, 1 the /offer step). Writes
	// name the column, and campaign unique counts filter on it, only after
	// ensureSchema has seen it.
	stepEvents bool
	// subEvents and subConv: events and conversions carry sub_id_13..sub_id_30.
	// Writes and the journal name those columns only after ensureSchema has
	// seen them. ensureSchema does not add the columns.
	subEvents, subConv bool
	// originEvents and originConv: events and conversions carry origin_group_*.
	// Writes and reads name those columns only after ensureSchema has seen them.
	// maybeRefreshOriginWave can set these true later without restarting the process.
	originEvents, originConv atomic.Bool
	// rootEvents and rootConv: events and conversions carry the root wave
	// (root_cid, and on conversions the origin stream and keyword). Writes
	// name those columns only after ensureSchema has seen them.
	// maybeRefreshOriginWave can set these true later without a restart.
	rootEvents, rootConv atomic.Bool
	// pblogDetail: postback_log carries the detail columns (postbackLogDetailCols).
	// Writes and reads name them only after ensureSchema has seen them.
	pblogDetail atomic.Bool
	// rootMissing counts conversions whose root click was not in the lookup window.
	rootMissing atomic.Int64
	originProbeMu   sync.Mutex
	originLastProbe time.Time
	// originColumnReader replaces readColumnSet during maybeRefreshOriginWave
	// when set. Used by tests without ClickHouse.
	originColumnReader func() (map[string]map[string]bool, error)
	// readyFlags is table → wave → columns present. The bools above are
	// derived from it. The origin wave is tracked only in originEvents and
	// originConv after ensureSchema.
	readyFlags map[string]map[wave]bool
	// limits bound the statistics queries; see analyticsCtx.
	limits AnalyticsLimits
	// dedupReady: the events table deduplicates inserts that carry a token.
	// See DedupReady.
	dedupReady atomic.Bool
}

// OpenCH opens a connection to ClickHouse.
func OpenCH(addr, database, username, password string) (*CH, error) {
	c, err := openCHRaw(addr, database, username, password)
	if err != nil {
		return nil, err
	}
	c.ensureSchema()
	return c, nil
}

// openCHRaw dials ClickHouse and pings the server without running ensureSchema.
func openCHRaw(addr, database, username, password string) (*CH, error) {
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
	return &CH{conn: conn, limits: DefaultAnalyticsLimits()}, nil
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

// Ping checks that the ClickHouse server answers on the open connection.
// ctx bounds the wait. It returns nil on success, an error when c is nil or closed, and the
// driver error when the server does not answer.
func (c *CH) Ping(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return errors.New("clickhouse: not connected")
	}
	return c.conn.Ping(ctx)
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
	return c.summary(ctx, from, to, "", nil, nil)
}

// SummaryFiltered returns Summary when conds is empty. Otherwise it counts only
// matching events. A condition on a wave that is not ready yields zeros.
// ErrUnknownField and ErrBadCond come from buildWhere.
func (c *CH) SummaryFiltered(ctx context.Context, from, to time.Time, conds []Cond) (StatsSummary, error) {
	if len(conds) == 0 {
		return c.Summary(ctx, from, to)
	}
	where, args, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return StatsSummary{}, err
	}
	if skip {
		return StatsSummary{}, nil
	}
	return c.summary(ctx, from, to, where, args, conds)
}

// summary aggregates events in [from, to). where is empty or from buildWhere.
// conds are the conditions where came from; they pick the unique aggregate.
func (c *CH) summary(ctx context.Context, from, to time.Time, where string, wargs []any, conds []Cond) (StatsSummary, error) {
	ctx = c.analyticsCtx(ctx, 0)
	var total, uniq, bots uint64
	qargs := append([]any{from, to}, wargs...)
	row := c.conn.QueryRow(ctx, summarySQL(c.uniqCount(conds), where), qargs...)
	if err := row.Scan(&total, &uniq, &bots); err != nil {
		return StatsSummary{}, err
	}
	return StatsSummary{Total: int64(total), Unique: int64(uniq), Bots: int64(bots)}, nil
}

// TSPoint is one chart bucket.
// Total, Unique and Bots come from clicks. Conv and Revenue are confirmed
// lead and sale rows. Cost is click price in cents, keyed by currency code.
// An empty Cost is omitted from JSON.
type TSPoint struct {
	TS      time.Time        `json:"ts"`
	Total   int64            `json:"total"`
	Unique  int64            `json:"unique"`
	Bots    int64            `json:"bots"`
	Conv    int64            `json:"conv"`
	Revenue float64          `json:"revenue"`
	Cost    map[string]int64 `json:"cost,omitempty"`
}

// TimeSeries aggregates clicks and confirmed conversions for a period.
// stepSec is the bucket length in seconds. A step below 3600 is raised, and
// the step grows so the answer has at most 400 points.
// It returns one point per bucket, including quiet buckets. When cost
// columns exist, Cost carries click prices in cents by currency.
// An error from a query is returned as-is.
func (c *CH) TimeSeries(ctx context.Context, from, to time.Time, stepSec int) ([]TSPoint, error) {
	return c.timeSeries(ctx, window{from: from, to: to}, stepSec)
}

// TimeSeriesFiltered returns TimeSeries when conds is empty. Otherwise it
// buckets only matching clicks and their conversions. A wave that is not ready
// yields an empty series. ErrUnknownField and ErrBadCond come from buildWhere.
func (c *CH) TimeSeriesFiltered(ctx context.Context, from, to time.Time, stepSec int, conds []Cond) ([]TSPoint, error) {
	if len(conds) == 0 {
		return c.TimeSeries(ctx, from, to, stepSec)
	}
	where, args, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return nil, err
	}
	if skip {
		return []TSPoint{}, nil
	}
	return c.timeSeries(ctx, window{from: from, to: to, where: where, args: args, conds: conds}, stepSec)
}

// timeSeries aggregates clicks and confirmed conversions per bucket.
// w is the report window; its where is empty or from buildWhere on events.
// stepSec is the bucket length in seconds. When cost columns exist, a third
// query adds click prices in cents by currency. An error from any query is
// returned as-is.
func (c *CH) timeSeries(ctx context.Context, w window, stepSec int) ([]TSPoint, error) {
	ctx = c.analyticsCtx(ctx, 0)
	stepSec = capSeriesStep(w.from, w.to, stepSec)
	hits := map[int64]seriesHit{}
	evArgs := append([]any{stepSec, w.from, w.to}, w.args...)
	rows, err := c.conn.Query(ctx, seriesSQL(c.uniqCount(w.conds), w.where), evArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ts time.Time
		var total, uniq, bots uint64
		if err := rows.Scan(&ts, &total, &uniq, &bots); err != nil {
			return nil, err
		}
		h := hits[ts.UTC().Unix()]
		h.total, h.unique, h.bots = int64(total), int64(uniq), int64(bots)
		hits[ts.UTC().Unix()] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var convSQL string
	var convArgs []any
	if w.where == "" {
		convSQL = confirmedSeriesSQL
		convArgs = []any{stepSec, w.from, w.to}
	} else {
		convSQL = confirmedSeriesFilteredSQL(w.where)
		convArgs = append([]any{stepSec, w.from, w.to, w.from, w.to}, w.args...)
	}
	conv, err := c.conn.Query(ctx, convSQL, convArgs...)
	if err != nil {
		return nil, err
	}
	defer conv.Close()
	for conv.Next() {
		var ts time.Time
		var n uint64
		var revenue float64
		if err := conv.Scan(&ts, &n, &revenue); err != nil {
			return nil, err
		}
		h := hits[ts.UTC().Unix()]
		h.conv, h.revenue = int64(n), revenue
		hits[ts.UTC().Unix()] = h
	}
	if err := conv.Err(); err != nil {
		return nil, err
	}
	if c.costCols {
		if err := c.addSeriesCost(ctx, w, stepSec, hits); err != nil {
			return nil, err
		}
	}
	return fillSeries(w.from, w.to, stepSec, hits), nil
}

// KV — a key/count pair for breakdowns.
type KV struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
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
	// DepSum is the sum of deposits. It is not the payout in Profit.
	DepSum float64 `json:"depsum"`
	// Forwarded — visits this stream handed to another group (type "group").
	// They are logged under the group that served them; here they are added
	// to Hits/Unique/Bots of the stream that sent them on, so a flow's
	// numbers show everything that went through it.
	Forwarded int64 `json:"forwarded"`
	// Cost is click price in cents, keyed by currency code.
	// Currencies share this row. An empty map is omitted from JSON.
	Cost map[string]int64 `json:"cost,omitempty"`
}

// Performance aggregates events and conversions per group and stream over a
// period. Two GROUP BY queries merged in Go rather than one JOIN: conversions
// carry group_id once the ext wave is ready, postbacks do not, and a JOIN
// across two MergeTree tables is slower than two scans plus a map lookup.
// A conversion with group_id joins that campaign even after a rename. A row
// with an empty group_id still joins by name. Rows are ordered by hits,
// descending; a stream with conversions but no events still appears, with zero hits.
func (c *CH) Performance(ctx context.Context, from, to time.Time) ([]PerfRow, error) {
	return c.performance(ctx, window{from: from, to: to})
}

// PerformanceFiltered returns Performance when conds is empty. Otherwise it
// aggregates only matching clicks and their conversions. A wave that is not
// ready yields an empty table. ErrUnknownField and ErrBadCond come from buildWhere.
func (c *CH) PerformanceFiltered(ctx context.Context, from, to time.Time, conds []Cond) ([]PerfRow, error) {
	if len(conds) == 0 {
		return c.Performance(ctx, from, to)
	}
	where, args, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return nil, err
	}
	if skip {
		return []PerfRow{}, nil
	}
	return c.performance(ctx, window{from: from, to: to, where: where, args: args, conds: conds})
}

// performance merges event aggregates with postbacks and conversions.
// w is the report window; its where is empty or from buildWhere on events.
// When cost columns exist, click prices are added on the same row, cents by currency.
// An error from a query is returned as-is.
func (c *CH) performance(ctx context.Context, w window) ([]PerfRow, error) {
	ctx = c.analyticsCtx(ctx, 0)
	evArgs := append([]any{w.from, w.to}, w.args...)
	rows, err := c.conn.Query(ctx, performanceSQL(c.uniqCount(w.conds), w.where), evArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PerfRow
	var idx perfIndex
	for rows.Next() {
		var p PerfRow
		var hits, uniq, bots uint64
		if err := rows.Scan(&p.GroupID, &p.Group, &p.Stream, &hits, &uniq, &bots); err != nil {
			return nil, err
		}
		i := idx.at(&out, p.GroupID, p.Group, p.Stream)
		out[i].Hits += int64(hits)
		out[i].Unique += int64(uniq)
		out[i].Bots += int64(bots)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if c.costCols {
		if err := c.addCost(ctx, w, &out, &idx); err != nil {
			return nil, err
		}
	}
	if c.netCols {
		if err := c.addForwarded(ctx, w, &out, &idx); err != nil {
			return nil, err
		}
	}
	pbSQL, pbArgs := performanceConvSQL(w.from, w.to, w.where, w.args, c.extConv, c.depsumConv)
	pbs, err := c.conn.Query(ctx, pbSQL, append([]any{w.from, w.to}, pbArgs...)...)
	if err != nil {
		return nil, err
	}
	defer pbs.Close()
	for pbs.Next() {
		var groupID, group, stream string
		var conv uint64
		var profit, dep float64
		if err := pbs.Scan(&groupID, &group, &stream, &conv, &profit, &dep); err != nil {
			return nil, err
		}
		i := idx.at(&out, groupID, group, stream)
		out[i].Conv += int64(conv)
		out[i].Profit += profit
		out[i].DepSum += dep
	}
	return out, pbs.Err()
}

// performanceConvSQL is the conversion half of Performance.
// from and to bound the outer WHERE. where is empty or from buildWhere on events.
// haveGroupID is true when conversions carry group_id (the ext wave).
// Postbacks have no such column, so that branch selects an empty group_id.
// Without the column both branches select an empty group_id and the row shape stays one.
// haveDep is true when conversions carry depsum. Postbacks always select a typed
// zero. A closed wave selects a typed zero on conversions too and does not name
// the column. extraArgs follow the outer from and to: when where is set, subquery
// from, to and wargs. It does not fail.
func performanceConvSQL(from, to time.Time, where string, wargs []any, haveGroupID, haveDep bool) (sql string, extraArgs []any) {
	convID := "'' AS group_id"
	if haveGroupID {
		convID = "group_id"
	}
	convDep := "toFloat64(0) AS depsum"
	if haveDep {
		convDep = "depsum"
	}
	if where != "" {
		extraArgs = append([]any{from, to}, wargs...)
	}
	sql = `SELECT group_id, group_name, stream_name, count() AS conv, sum(profit) AS profit, sum(depsum) AS depsum
		 FROM (
		   SELECT '' AS group_id, group_name, stream_name, profit, toFloat64(0) AS depsum, ts, cid, '' AS status FROM postbacks
		   UNION ALL
		   SELECT ` + convID + `, group_name, stream_name, profit, ` + convDep + `, ts, cid, status FROM conversions FINAL
		 ) WHERE ts >= ? AND ts < ?` + voidedConfirmedSQL("cid", "status")
	if where != "" {
		sql += eventsCidInSQL(where)
	}
	sql += `
		 GROUP BY group_id, group_name, stream_name`
	return sql, extraArgs
}

// Breakdown returns the top values of dim over [from, to).
// ctx bounds the query. dim is a field name, or the old alias group.
// limit <= 0 or above 1000 becomes 50. It calls BreakdownFiltered with no
// extra conditions. An empty query result is a nil slice.
// ErrUnknownField, ErrBadCond and ErrFieldNotReady match BreakdownFiltered.
// A query error is returned as-is.
func (c *CH) Breakdown(ctx context.Context, from, to time.Time, dim string, limit int) ([]KV, error) {
	return c.BreakdownFiltered(ctx, from, to, dim, limit, nil)
}

// BreakdownFiltered returns the top values of dim over [from, to) under conds.
// ctx bounds the query. dim is a field name, or the old alias group.
// limit <= 0 or above 1000 becomes 50. conds filter events; nil is no filter.
// A condition on a wave that is not ready returns []KV{} and nil, and does
// not query. An empty query result is a nil slice.
// ErrUnknownField means dim or a condition names an unknown field.
// ErrBadCond means dim is not a dimension, or a condition is invalid.
// ErrFieldNotReady means dim's wave is absent. Those three come from
// dimColumn and buildWhere. A query error is returned as-is.
func (c *CH) BreakdownFiltered(ctx context.Context, from, to time.Time, dim string, limit int, conds []Cond) ([]KV, error) {
	f, ok := fieldByName(dim)
	if !ok {
		return nil, ErrUnknownField
	}
	if !f.Dim {
		return nil, ErrBadCond
	}
	col, err := dimColumn(c, "events", dim)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	where, args, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return nil, err
	}
	if skip {
		return []KV{}, nil
	}
	qargs := make([]any, 0, 2+len(args)+1)
	qargs = append(qargs, from, to)
	qargs = append(qargs, args...)
	qargs = append(qargs, limit)
	rows, err := c.conn.Query(c.analyticsCtx(ctx, 0), breakdownSQL(col, where), qargs...)
	if err != nil {
		return nil, err
	}
	return scanKV(rows)
}

// breakdownSQL builds the events breakdown query.
// col is the dimension expression from dimColumn. where is empty or a
// fragment from buildWhere that starts with " AND ". Placeholders are from,
// to, the where args, then limit. It does not fail.
func breakdownSQL(col, where string) string {
	return `SELECT ` + col + ` AS k, count() AS n FROM events
		 WHERE ts >= ? AND ts < ?` + where + ` GROUP BY k ORDER BY n DESC LIMIT ?`
}

// ValuesQuery selects top dimension values on one journal table over a time range.
type ValuesQuery struct {
	Table string
	Field string
	From  time.Time
	To    time.Time
	Limit int
	Conds []Cond
}

// Values returns the top values of q.Field on q.Table over [q.From, q.To).
// ctx bounds the query, which runs under the analytics limits with a 10s
// ceiling. q.Table is events
// or conversions. q.Field must have Dim. q.Conds restrict rows; a wave that is
// not ready yields an empty list. q.Limit <= 0 or above 500 becomes 500.
// The rows are ordered by count, descending.
// ErrBadCond means the table is unknown or the field is not a dimension.
// ErrUnknownField and ErrFieldNotReady come from dimColumn.
// A query error is returned as-is.
func (c *CH) Values(ctx context.Context, q ValuesQuery) ([]KV, error) {
	if q.Table != "events" && q.Table != "conversions" {
		return nil, ErrBadCond
	}
	c.maybeRefreshOriginWave()
	f, ok := fieldByName(q.Field)
	if !ok {
		return nil, ErrUnknownField
	}
	if !f.Dim {
		return nil, ErrBadCond
	}
	col, err := dimColumn(c, q.Table, q.Field)
	if err != nil {
		return nil, err
	}
	where, args, skip, err := buildWhere(c, q.Table, q.Conds)
	if err != nil {
		return nil, err
	}
	if skip {
		return []KV{}, nil
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	ctx = c.analyticsCtx(ctx, valuesMaxExecution)
	qargs := make([]any, 0, 2+len(args)+1)
	qargs = append(qargs, q.From, q.To)
	qargs = append(qargs, args...)
	qargs = append(qargs, limit)
	rows, err := c.conn.Query(ctx,
		`SELECT `+col+` AS k, count() AS n FROM `+q.Table+`
		 WHERE ts >= ? AND ts < ?`+where+` GROUP BY k ORDER BY count() DESC LIMIT ?`,
		qargs...)
	if err != nil {
		return nil, err
	}
	return scanKV(rows)
}

// scanKV reads key/count pairs from a breakdown query.
// rows is closed before return. A scan error or rows.Err is returned.
// An empty result is a nil slice.
func scanKV(rows driver.Rows) ([]KV, error) {
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
