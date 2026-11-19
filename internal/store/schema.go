package store

import (
	"context"
	"log"
	"time"
)

// schemaTimeout bounds one DDL statement or one system.columns read.
// Each call gets its own timeout so a slow ALTER cannot consume the ping.
const schemaTimeout = 10 * time.Second

// eventsCIDIndexSQL is the events skip index for lookups by cid, the same
// statement as migrations/clickhouse/013_events_cid_index.sql. It changes
// metadata only: new parts carry the index, old parts are not rebuilt.
const eventsCIDIndexSQL = `ALTER TABLE events ADD INDEX IF NOT EXISTS idx_cid cid TYPE bloom_filter(0.01) GRANULARITY 1`

// ensureSchema adds missing columns, the events cid index and the events
// deduplication window, and records which later waves are present. It takes no
// context: every statement uses a fresh 10s background timeout. ALTER, CREATE and the system.columns read log their error and continue,
// so a read-only user does not stop the process. A refused ALTER leaves
// that wave's flag false and later writes omit the columns.
// The base wave is not probed: it ships with the table, and ready reports
// it present even when a probe of a later wave fails.
func (c *CH) ensureSchema() {
	c.ensureSchemaWaves(true)
}

// ensureSchemaWaves adds missing columns and records which later waves are present.
// withOrigin selects whether origin_group_* ALTER runs on events and conversions.
func (c *CH) ensureSchemaWaves(withOrigin bool) {
	c.readyFlags = map[string]map[wave]bool{
		"events":      {},
		"conversions": {},
	}
	c.schemaAlter("events", waveNet)
	c.schemaAlter("events", waveClick)
	c.schemaAlter("events", waveCost)
	c.schemaAlter("events", waveExt)
	c.schemaAlter("events", waveModel)
	c.schemaAlter("events", waveStep)
	if withOrigin {
		c.schemaAlter("events", waveOrigin)
	}
	c.schemaAlter("events", waveRoot)
	c.schemaExec(eventsCIDIndexSQL)
	c.EnsureDedup()
	c.schemaExec(`CREATE TABLE IF NOT EXISTS conversions (
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
	c.schemaAlter("conversions", waveClick)
	c.schemaAlter("conversions", waveExt)
	c.schemaAlter("conversions", waveModel)
	if withOrigin {
		c.schemaAlter("conversions", waveOrigin)
	}
	c.schemaAlter("conversions", waveRoot)
	c.schemaAlter("conversions", waveDep)
	c.schemaExec(`CREATE TABLE IF NOT EXISTS postback_log (
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
	c.schemaExec(postbackLogDedupSQL)
	c.schemaExec(postbackLogDetailSQL)
	c.probeColumns()
	c.netCols = c.ready("events", waveNet)
	c.clickCols = c.ready("events", waveClick) && c.ready("conversions", waveClick)
	c.costCols = c.ready("events", waveCost)
	c.extEvents = c.ready("events", waveExt)
	c.extConv = c.ready("conversions", waveExt)
	c.modelEvents = c.ready("events", waveModel)
	c.modelConv = c.ready("conversions", waveModel)
	c.depsumConv = c.ready("conversions", waveDep)
	c.stepEvents = c.ready("events", waveStep)
	c.subEvents = c.ready("events", waveSub)
	c.subConv = c.ready("conversions", waveSub)
	if rf := c.readyFlags; rf != nil {
		c.originEvents.Store(rf["events"][waveOrigin])
		c.originConv.Store(rf["conversions"][waveOrigin])
		c.rootEvents.Store(rf["events"][waveRoot])
		c.rootConv.Store(rf["conversions"][waveRoot])
	}
}

// schemaAlter runs ADD COLUMN for one wave when that wave has columns.
// table is events or conversions. w selects the generation. An empty wave
// is skipped. Errors are logged inside schemaExec and do not propagate.
func (c *CH) schemaAlter(table string, w wave) {
	body := alterSQL(table, w)
	if body == "" {
		return
	}
	c.schemaExec("ALTER TABLE " + table + " " + body)
}

// schemaExec runs one statement with its own 10s timeout.
// query is the full SQL. A failure is logged and swallowed.
func (c *CH) schemaExec(query string) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	if err := c.conn.Exec(ctx, query); err != nil {
		log.Printf("clickhouse: schema: %v", err)
	}
}

// probeColumns fills readyFlags and pblogDetail from one system.columns read.
// It asks for events, conversions and postback_log together, under one 10s timeout.
// A wave is ready only when every expected column name was returned.
// A query or scan error is logged and leaves every non-base wave unset,
// so those waves stay not ready. Base is not decided here: ready reports
// it present even when this probe fails. It does not return an error.
func (c *CH) probeColumns() {
	have, err := c.readColumnSet()
	if err != nil {
		log.Printf("clickhouse: schema: %v", err)
		return
	}
	c.readyFlags = readyFromColumns(have)
	c.pblogDetail.Store(hasColumns(have["postback_log"], postbackLogDetailCols))
}

// hasColumns reports whether present holds every name of cols. A nil present holds none.
func hasColumns(present map[string]bool, cols []string) bool {
	for _, col := range cols {
		if !present[col] {
			return false
		}
	}
	return len(cols) > 0
}

// readColumnSet lists column names of events, conversions and postback_log.
// One SELECT against system.columns, one timeout. have[table][name] is
// true when that column exists. A query, scan or rows error is returned
// and have is nil.
func (c *CH) readColumnSet() (map[string]map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	rows, err := c.conn.Query(ctx, `SELECT table, name FROM system.columns WHERE database = currentDatabase() AND table IN ('events','conversions','postback_log')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]map[string]bool{}
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			return nil, err
		}
		if have[table] == nil {
			have[table] = map[string]bool{}
		}
		have[table][name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return have, nil
}

// readyFromColumns reports which waves are fully present.
// have[table][column] is true when system.columns listed that name.
// A wave with at least one column is ready when every name is in the set.
// A wave with no columns stays false. Base is always true on events and
// conversions. A nil have marks only the base wave ready.
// It does not touch the database and does not fail.
func readyFromColumns(have map[string]map[string]bool) map[string]map[wave]bool {
	out := map[string]map[wave]bool{
		"events":      {waveBase: true},
		"conversions": {waveBase: true},
	}
	for _, table := range []string{"events", "conversions"} {
		present := have[table]
		for _, w := range allWaves[1:] { // every wave after base
			cols := columnsOf(table, w)
			if len(cols) == 0 {
				continue
			}
			ok := true
			for _, col := range cols {
				if present == nil || !present[col] {
					ok = false
					break
				}
			}
			out[table][w] = ok
		}
	}
	return out
}

// ready reports whether every column of wave w is on table.
// waveBase on events and conversions is always true: those columns come with
// the table, so a nil client, a nil readyFlags or a failed probe cannot turn
// off a country filter or a device breakdown. Any other wave on a nil
// receiver, or a wave that was never probed, returns false.
func (c *CH) ready(table string, w wave) bool {
	if w == waveBase && (table == "events" || table == "conversions") {
		return true
	}
	if w == waveOrigin {
		if c == nil {
			return false
		}
		switch table {
		case "events":
			return c.originEvents.Load()
		case "conversions":
			return c.originConv.Load()
		default:
			return false
		}
	}
	if w == waveRoot {
		if c == nil {
			return false
		}
		fromFlags := c.readyFlags != nil && c.readyFlags[table][w]
		switch table {
		case "events":
			return fromFlags || c.rootEvents.Load()
		case "conversions":
			return fromFlags || c.rootConv.Load()
		default:
			return false
		}
	}
	if c == nil || c.readyFlags == nil {
		return false
	}
	return c.readyFlags[table][w]
}
