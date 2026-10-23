package store

import (
	"context"
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

// WritePostbackLog appends one line. direction is "in" or "out".
// outcome is ok, click_not_found, no_subid, no_store, store_error, or an outbound HTTP code.
func (c *CH) WritePostbackLog(ctx context.Context, subid, status, outcome, direction string, payout float64) error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Exec(ctx, `INSERT INTO postback_log (subid, status, payout, outcome, direction) VALUES (?, ?, ?, ?, ?)`,
		subid, status, payout, outcome, direction)
}

// PostbackLog returns the newest lines in the window, oldest first.
func (c *CH) PostbackLog(ctx context.Context, from, to time.Time, limit int) ([]PostbackLogRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := c.conn.Query(ctx, `SELECT ts, subid, status, payout, outcome, direction
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
