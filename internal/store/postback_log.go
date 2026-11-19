package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
)

// PostbackLogRow is one inbound pixel or one outbound HTTP result.
// The detail fields (IP to Duplicate) describe an inbound request: the sender
// address, the affiliate network of the click, the method, the path and the
// parameters with every secret already masked by the engine, the User-Agent,
// the campaign of the click and whether the click and status pair was stored
// before. They are empty on outbound lines and on lines replayed from the disk
// queue. The pixel key is never stored.
type PostbackLogRow struct {
	Ts         time.Time `json:"ts"`
	Subid      string    `json:"subid"`
	Status     string    `json:"status"`
	Payout     float64   `json:"payout"`
	Outcome    string    `json:"outcome"`
	Direction  string    `json:"direction"`
	IP         string    `json:"ip,omitempty"`
	Network    string    `json:"network_id,omitempty"`
	Method     string    `json:"method,omitempty"`
	Path       string    `json:"path,omitempty"`
	Params     string    `json:"params,omitempty"`
	UserAgent  string    `json:"useragent,omitempty"`
	CampaignID string    `json:"campaign_id,omitempty"`
	Duplicate  bool      `json:"duplicate,omitempty"`
}

// PostbackLogQuery selects lines of the postback log, newest first.
// From and To bound ts. Outcome, Network, IP, CID and Direction match one value
// each when not empty; Outcome "duplicate" stands for an accepted repeat (ok with
// Duplicate set) and "ok" for a first arrival. Scope, when set, is the campaign
// visibility of the caller: an in or nin list of campaign ids; lines without a
// campaign are then left out. Offset skips lines, Limit caps them (1..1000,
// default 200).
type PostbackLogQuery struct {
	From, To  time.Time
	Outcome   string
	Network   string
	IP        string
	CID       string
	Direction string
	Scope     *Cond
	Offset    int
	Limit     int
}

// Postback log outcomes the engine writes besides the ones of postbackResult and
// the disk queue: rejected requests that never reach the store.
const (
	PostbackOutcomeBadKey    = "bad_key"            // the key is wrong or no secret is set
	PostbackOutcomeInvalid   = "invalid"            // a payout or deposit is NaN or Inf
	PostbackOutcomeMethod    = "method_not_allowed" // neither GET nor POST
	PostbackOutcomeDuplicate = "duplicate"          // filter value: an accepted repeat of a stored pair
)

// postbackLogDetailCols are the detail columns of postback_log, in insert order.
// ensureSchema adds them to a table created before they existed.
var postbackLogDetailCols = []string{"ip", "network_id", "method", "path", "params", "useragent", "campaign_id", "duplicate"}

// postbackLogDetailSQL adds the detail columns to postback_log. It changes
// metadata only and is idempotent.
const postbackLogDetailSQL = `ALTER TABLE postback_log
	ADD COLUMN IF NOT EXISTS ip String DEFAULT '',
	ADD COLUMN IF NOT EXISTS network_id LowCardinality(String) DEFAULT '',
	ADD COLUMN IF NOT EXISTS method LowCardinality(String) DEFAULT '',
	ADD COLUMN IF NOT EXISTS path String DEFAULT '',
	ADD COLUMN IF NOT EXISTS params String DEFAULT '',
	ADD COLUMN IF NOT EXISTS useragent String DEFAULT '',
	ADD COLUMN IF NOT EXISTS campaign_id LowCardinality(String) DEFAULT '',
	ADD COLUMN IF NOT EXISTS duplicate UInt8 DEFAULT 0`

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
		dup := "0"
		if r.Duplicate {
			dup = "1"
		}
		for _, s := range []string{r.Subid, r.Status, r.Outcome, r.Direction,
			r.IP, r.Network, r.Method, r.Path, r.Params, r.UserAgent, r.CampaignID, dup} {
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
// no_store, store_error, queued, retry, bad_key, invalid, method_not_allowed or an
// outbound result. The detail columns are written only when ensureSchema has seen
// them on the table. An empty slice returns nil.
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
	detail := c.pblogDetail.Load()
	ctx = withInsertSettings(ctx, eventInsertSettings(postbackLogToken(rows)))
	cols := "ts, subid, status, payout, outcome, direction"
	if detail {
		cols += ", " + strings.Join(postbackLogDetailCols, ", ")
	}
	batch, err := c.conn.PrepareBatch(insertCtx(ctx), `INSERT INTO postback_log (`+cols+`)`)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, r := range rows {
		ts := r.Ts
		if ts.IsZero() {
			ts = now
		}
		vals := []any{ts, r.Subid, r.Status, r.Payout, r.Outcome, r.Direction}
		if detail {
			var dup uint8
			if r.Duplicate {
				dup = 1
			}
			vals = append(vals, r.IP, r.Network, r.Method, r.Path, r.Params, r.UserAgent, r.CampaignID, dup)
		}
		if err := batch.Append(vals...); err != nil {
			_ = batch.Abort()
			return err
		}
	}
	return batch.Send()
}

// postbackLogSQL builds the SELECT of PostbackLog for q. detail adds the detail
// columns to the select list and allows the filters that need them (Network, IP);
// without them those filters match nothing. It returns the statement and its
// arguments in order.
func postbackLogSQL(q PostbackLogQuery, detail bool) (string, []any) {
	cols := "ts, subid, status, payout, outcome, direction"
	if detail {
		cols += ", " + strings.Join(postbackLogDetailCols, ", ")
	}
	where := []string{"ts >= ?", "ts < ?"}
	args := []any{q.From, q.To}
	eq := func(col, val string) {
		if val != "" {
			where = append(where, col+" = ?")
			args = append(args, val)
		}
	}
	switch q.Outcome {
	case "":
	case PostbackOutcomeDuplicate:
		where = append(where, "outcome = 'ok'")
		if detail {
			where = append(where, "duplicate = 1")
		} else {
			where = append(where, "0")
		}
	case "ok":
		where = append(where, "outcome = 'ok'")
		if detail {
			where = append(where, "duplicate = 0")
		}
	default:
		eq("outcome", q.Outcome)
	}
	eq("subid", q.CID)
	eq("direction", q.Direction)
	if detail {
		eq("network_id", q.Network)
		eq("ip", q.IP)
	} else if q.Network != "" || q.IP != "" || q.Scope != nil {
		where = append(where, "0")
	}
	if q.Scope != nil && detail {
		where = append(where, "campaign_id != ''")
		op := "IN"
		if q.Scope.Op == "nin" {
			op = "NOT IN"
		}
		where = append(where, "campaign_id "+op+" (?)")
		args = append(args, q.Scope.Values)
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args = append(args, limit, max(q.Offset, 0))
	return `SELECT ` + cols + ` FROM postback_log WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ts DESC LIMIT ? OFFSET ?`, args
}

// PostbackLog returns the lines q selects, newest first.
// ctx bounds the read. Without the detail columns the detail fields stay empty
// and a Network, IP or Scope filter returns no lines. It returns the driver error.
func (c *CH) PostbackLog(ctx context.Context, q PostbackLogQuery) ([]PostbackLogRow, error) {
	detail := c.pblogDetail.Load()
	query, args := postbackLogSQL(q, detail)
	rows, err := c.conn.Query(c.analyticsCtx(ctx, 0), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PostbackLogRow{}
	for rows.Next() {
		var row PostbackLogRow
		dest := []any{&row.Ts, &row.Subid, &row.Status, &row.Payout, &row.Outcome, &row.Direction}
		var dup uint8
		if detail {
			dest = append(dest, &row.IP, &row.Network, &row.Method, &row.Path, &row.Params, &row.UserAgent, &row.CampaignID, &dup)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row.Duplicate = dup == 1
		out = append(out, row)
	}
	return out, rows.Err()
}
