package store

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// eventsDedupWindow is how many recent insert blocks of events the server
// remembers for deduplication. A batch is one block and the buffer writes about
// one a second, so the window covers hours, which is longer than a repeat of
// the same batch can be delayed. It is the value of
// migrations/clickhouse/014_events_dedup.sql.
const eventsDedupWindow = 10000

// eventsDedupSQL is the statement of the migration, run again by ensureSchema.
// It changes table metadata only and is idempotent.
const eventsDedupSQL = `ALTER TABLE events MODIFY SETTING non_replicated_deduplication_window = 10000`

// dedupWindowRe finds the setting in the CREATE statement of a table.
var dedupWindowRe = regexp.MustCompile(`non_replicated_deduplication_window\s*=\s*(\d+)`)

// eventInsertSettings returns the deduplication settings of one events insert.
// token is the idempotency token of the batch. With a token the server is asked
// to deduplicate under it: insert_deduplicate=1 and the token itself. Without
// one deduplication is switched off with insert_deduplicate=0, because with the
// window on the table two unrelated batches with equal bytes (a repeated import
// of the same rows) would otherwise be taken for one and the second dropped.
func eventInsertSettings(token string) clickhouse.Settings {
	if token == "" {
		return clickhouse.Settings{"insert_deduplicate": 0}
	}
	return clickhouse.Settings{"insert_deduplicate": 1, "insert_deduplication_token": token}
}

// dedupWindowOf extracts non_replicated_deduplication_window from the CREATE
// TABLE statement of a table. It returns 0 when the setting is absent.
func dedupWindowOf(createQuery string) int {
	m := dedupWindowRe.FindStringSubmatch(createQuery)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// DedupReady reports whether the events table deduplicates inserts that carry
// a token: its non_replicated_deduplication_window is at least
// eventsDedupWindow. The buffer repeats a batch that may already be stored only
// while this is true; without it a repeat could write the rows twice, and a
// loss counted by cause is better than a silent duplicate. It is false on a nil
// client. The value is set by ensureSchema and EnsureDedup.
func (c *CH) DedupReady() bool { return c != nil && c.dedupReady.Load() }

// badDataCodes are the ClickHouse error codes that say the server could not
// accept the values of a batch: the same rows fail the same way on every
// attempt, so a spool segment that keeps drawing them is poison and not a
// victim of an outage. The list is deliberately short; a code that can also be
// raised by load, the schema or the network is not in it, because setting a
// segment aside is not undone by itself.
//
//	6   CANNOT_PARSE_TEXT                   text that is not the type it should be
//	26  CANNOT_PARSE_QUOTED_STRING          a malformed string literal
//	27  CANNOT_PARSE_INPUT_ASSERTION_FAILED the input is not what the format says
//	38  CANNOT_PARSE_DATE                   a date that does not parse
//	41  CANNOT_PARSE_DATETIME               a timestamp that does not parse
//	53  TYPE_MISMATCH                       a value of the wrong type for its column
//	70  CANNOT_CONVERT_TYPE                 a value that does not convert
//	72  CANNOT_PARSE_NUMBER                 a number that does not parse
//	117 INCORRECT_DATA                      data that violates the column's rules
//
// Left out on purpose, because retrying later can succeed: 159 TIMEOUT_EXCEEDED,
// 202 TOO_MANY_SIMULTANEOUS_QUERIES, 241 MEMORY_LIMIT_EXCEEDED, 252
// TOO_MANY_PARTS, the network codes (209, 210) and the schema codes
// (10, 16, 47), which a migration fixes.
var badDataCodes = map[int32]bool{6: true, 26: true, 27: true, 38: true, 41: true, 53: true, 70: true, 72: true, 117: true}

// badDataOf marks err with logbuf.BadData when it is a ClickHouse exception with
// a code of badDataCodes. Any other error, and nil, is returned as it is.
func badDataOf(err error) error {
	var ex *clickhouse.Exception
	if errors.As(err, &ex) && badDataCodes[ex.Code] {
		return logbuf.BadData(err)
	}
	return err
}

// dedupRecheckEvery is how often WatchDedup reads the setting again after it was
// found in force, so a table whose setting was removed by hand is noticed.
const dedupRecheckEvery = 10 * time.Minute

// EnsureDedup makes sure the events table deduplicates and returns whether it
// is in force. It reads the setting from system.tables first and runs the ALTER
// only when the window is absent or too small, so an engine that starts against
// a table that is already right changes nothing; a refused ALTER (a read-only
// user) is logged and the read after it decides. A read that fails says nothing
// about the table: the value known before is kept and returned. It is safe to
// call again, for instance until it returns true.
func (c *CH) EnsureDedup() bool {
	ok, err := c.readDedup()
	if err == nil && !ok {
		c.schemaExec(eventsDedupSQL)
		ok, err = c.readDedup()
	}
	if err != nil {
		log.Printf("clickhouse: schema: deduplication setting not read: %v", err)
		return c.DedupReady()
	}
	c.dedupReady.Store(ok)
	return ok
}

// readDedup reads the CREATE statement of events from system.tables, the one
// place that shows the setting of this table and not the server default. It
// returns whether the window is at least eventsDedupWindow, or the error of the
// read.
func (c *CH) readDedup() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	var ddl string
	row := c.conn.QueryRow(ctx, `SELECT create_table_query FROM system.tables WHERE database = currentDatabase() AND name = 'events'`)
	if err := row.Scan(&ddl); err != nil {
		return false, err
	}
	return dedupWindowOf(ddl) >= eventsDedupWindow, nil
}

// probeDedup is readDedup that takes a failed read for "not in force".
func (c *CH) probeDedup() bool {
	ok, _ := c.readDedup()
	return ok
}

// WatchDedup keeps DedupReady current until ctx ends. It calls EnsureDedup every
// interval while the window is not in force, and every dedupRecheckEvery once it
// is. The first check belongs to ensureSchema; this covers a ClickHouse that was
// not ready to answer then and a setting changed later. It blocks, so run it in
// a goroutine.
func (c *CH) WatchDedup(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c.DedupReady() && time.Since(last) < dedupRecheckEvery {
				continue
			}
			c.EnsureDedup()
			last = time.Now()
		}
	}
}
