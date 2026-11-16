package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"time"
)

// PostbackLogRow is one inbound pixel or one outbound HTTP result.
// The pixel key and the raw query are not stored.
type PostbackLogRow struct {
	Ts        time.Time `json:"ts"`
	Subid     string    `json:"subid"`
	Status    string    `json:"status"`
	Payout    float64   `json:"payout"`
	Outcome   string    `json:"outcome"`
	Direction string    `json:"direction"`
}

// postbackLogDedupSQL turns on insert deduplication for the postback log. It
// changes table metadata only and is idempotent; ensureSchema runs it after the
// table is created. The window is the number of recent insert blocks the server
// remembers: a batch is one block and the engine writes a few a second at most.
const postbackLogDedupSQL = `ALTER TABLE postback_log MODIFY SETTING non_replicated_deduplication_window = 10000`

// postbackLogToken returns the idempotency token of a batch of log rows: a hash
// of every field of every row in order. The same rows always give the same
// token, so a batch written, parked because the answer was lost and written
// again is stored once; a different batch gets a different token. A batch with
// a row of zero Ts gets no token (an empty string), because that row is stored
// with the time of the write and two such batches would look equal. rows is
// never empty here.
func postbackLogToken(rows []PostbackLogRow) string {
	h := sha256.New()
	var num [8]byte
	for _, r := range rows {
		if r.Ts.IsZero() {
			return ""
		}
		binary.BigEndian.PutUint64(num[:], uint64(r.Ts.UnixNano()))
		h.Write(num[:])
		binary.BigEndian.PutUint64(num[:], math.Float64bits(r.Payout))
		h.Write(num[:])
		for _, s := range []string{r.Subid, r.Status, r.Outcome, r.Direction} {
			binary.BigEndian.PutUint64(num[:], uint64(len(s)))
			h.Write(num[:])
			h.Write([]byte(s))
		}
	}
	return "pl-" + hex.EncodeToString(h.Sum(nil)[:16])
}

// ErrNoStoreConn is returned by InsertPostbackLog when the client has no
// connection, so the caller keeps the rows instead of counting them as written.
var ErrNoStoreConn = errors.New("store: no ClickHouse connection")

// InsertPostbackLog writes rows to the log as one synchronous batch insert and
// returns only after the server accepted them.
// ctx bounds the insert. Each row keeps its own Ts; a zero Ts is the current
// time, and a batch with such a row is written without deduplication. Any other
// batch is written under a token made from its rows (see postbackLogToken), so a
// batch the engine writes twice because the first answer was lost is stored once. direction is "in" or "out". outcome is ok, click_not_found, no_subid,
// no_store, store_error, or an outbound HTTP code. An empty slice returns nil.
// It returns ErrNoStoreConn without a connection and the driver error when the
// batch cannot be prepared, appended or sent; in every error case the caller
// still owns all the rows.
func (c *CH) InsertPostbackLog(ctx context.Context, rows []PostbackLogRow) error {
	if len(rows) == 0 {
		return nil
	}
	if c == nil || c.conn == nil {
		return ErrNoStoreConn
	}
	ctx = withInsertSettings(ctx, eventInsertSettings(postbackLogToken(rows)))
	batch, err := c.conn.PrepareBatch(insertCtx(ctx), `INSERT INTO postback_log (ts, subid, status, payout, outcome, direction)`)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, r := range rows {
		ts := r.Ts
		if ts.IsZero() {
			ts = now
		}
		if err := batch.Append(ts, r.Subid, r.Status, r.Payout, r.Outcome, r.Direction); err != nil {
			_ = batch.Abort()
			return err
		}
	}
	return batch.Send()
}

// PostbackLog returns the newest lines in the window, oldest first.
func (c *CH) PostbackLog(ctx context.Context, from, to time.Time, limit int) ([]PostbackLogRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := c.conn.Query(c.analyticsCtx(ctx, 0), `SELECT ts, subid, status, payout, outcome, direction
		FROM postback_log WHERE ts >= ? AND ts < ? ORDER BY ts DESC LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PostbackLogRow
	for rows.Next() {
		var row PostbackLogRow
		if err := rows.Scan(&row.Ts, &row.Subid, &row.Status, &row.Payout, &row.Outcome, &row.Direction); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if out == nil {
		out = []PostbackLogRow{}
	}
	return out, rows.Err()
}
