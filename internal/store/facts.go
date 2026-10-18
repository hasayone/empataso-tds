package store

import (
	"context"
	"time"
)

// ClickFact is one click bucket for a period.
// CostCents is the sum of click prices in CostCur for this bucket.
// One combination of group, stream, offer and network can appear as several
// rows when cost_cur differs. The caller sums Hits itself.
// Without cost columns both cost fields stay zero and empty.
type ClickFact struct {
	OfferID   string `json:"offer_id"`
	LandingID string `json:"landing_id"`
	NetworkID string `json:"network_id"`
	GroupID   string `json:"group_id"`
	Stream    string `json:"stream"`
	Hits      int64  `json:"hits"`
	Uniq      int64  `json:"uniq"`
	CostCents int64  `json:"cost_cents"`
	CostCur   string `json:"cost_cur"`
}

// StatusFact is one conversion status for a period.
// Profit is the sum of payouts for that status. Rejected rows are separate.
type StatusFact struct {
	OfferID   string  `json:"offer_id"`
	NetworkID string  `json:"network_id"`
	GroupID   string  `json:"group_id"`
	Status    string  `json:"status"`
	N         int64   `json:"n"`
	Profit    float64 `json:"profit"`
}

// Facts is the click and conversion breakdown used by the catalog tables.
// Ready is false when the offer and network columns are not on the tables.
// Callers then show a dash instead of a zero for those columns.
// One click key can appear as several Clicks rows when cost_cur differs.
// The caller sums Hits itself.
type Facts struct {
	Ready  bool         `json:"ready"`
	Clicks []ClickFact  `json:"clicks"`
	Status []StatusFact `json:"status"`
}

// BucketCount is the click weight of one stream and offer inside a campaign interval.
type BucketCount struct {
	Stream    string
	OfferID   string
	NetworkID string
	Hits      int64
	Uniq      int64
}

// ClickFacts reads clicks and conversion statuses for the half-open window.
// Conversion rows use FINAL so an unmerged duplicate of the same click and status counts once.
// It returns an error when the query fails.
func (c *CH) ClickFacts(ctx context.Context, from, to time.Time) (Facts, error) {
	return c.clickFactsBundle(ctx, from, to, "", nil)
}

// ClickFactsFiltered returns ClickFacts when conds is empty. Otherwise it
// reads only matching clicks and their conversions. A wave that is not ready
// yields empty facts. ErrUnknownField and ErrBadCond come from buildWhere.
func (c *CH) ClickFactsFiltered(ctx context.Context, from, to time.Time, conds []Cond) (Facts, error) {
	if len(conds) == 0 {
		return c.ClickFacts(ctx, from, to)
	}
	where, args, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return Facts{Clicks: []ClickFact{}, Status: []StatusFact{}}, err
	}
	if skip {
		return Facts{Clicks: []ClickFact{}, Status: []StatusFact{}}, nil
	}
	return c.clickFactsBundle(ctx, from, to, where, args)
}

// clickFactsBundle loads clicks and conversion statuses for the window.
// where is empty or from buildWhere on events.
func (c *CH) clickFactsBundle(ctx context.Context, from, to time.Time, where string, wargs []any) (Facts, error) {
	out := Facts{Clicks: []ClickFact{}, Status: []StatusFact{}}
	if c == nil || c.conn == nil {
		return out, nil
	}
	out.Ready = c.clickCols
	clicks, err := c.clickFacts(ctx, from, to, where, wargs)
	if err != nil {
		return out, err
	}
	out.Clicks = clicks
	if !c.clickCols {
		return out, nil
	}
	status, err := c.statusFacts(ctx, from, to, where, wargs)
	if err != nil {
		return out, err
	}
	group, err := c.groupStatus(ctx, from, to, where, wargs)
	if err != nil {
		return out, err
	}
	out.Status = append(status, group...)
	return out, nil
}

// clickFacts reads click counts for the half-open window.
// where is empty or from buildWhere on events. When cost columns exist,
// each currency is its own row and CostCents is the sum for that currency.
// It returns an error when the query or a scan fails.
func (c *CH) clickFacts(ctx context.Context, from, to time.Time, where string, wargs []any) ([]ClickFact, error) {
	q := clickFactsSQL(c.clickCols, c.costCols, where)
	qargs := append([]any{from, to}, wargs...)
	rows, err := c.conn.Query(ctx, q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClickFact
	for rows.Next() {
		row, err := scanClickFact(rows, c.clickCols, c.costCols)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if out == nil {
		out = []ClickFact{}
	}
	return out, rows.Err()
}

// clickFactsSQL is the click breakdown for one window.
// clickCols adds offer_id, landing_id and network_id. costCols adds cost_cur and
// sum(cost_cents) and groups by cost_cur, so one key can return several rows.
// where is empty or an events filter that starts with " AND ", placed after
// "ts < ?". Placeholders are from, to, then that filter. It does not fail.
func clickFactsSQL(clickCols, costCols bool, where string) string {
	cols := "group_id, stream_name"
	group := "group_id, stream_name"
	if clickCols {
		cols += ", offer_id, landing_id, network_id"
		group += ", offer_id, landing_id, network_id"
	}
	cols += ", count(), countIf(uniq = 1)"
	if costCols {
		cols += ", cost_cur, sum(cost_cents)"
		group += ", cost_cur"
	}
	return "SELECT " + cols + " FROM events WHERE ts >= ? AND ts < ?" + where + " GROUP BY " + group
}

// factRow reads one result row. Scan matches driver.Rows.
type factRow interface {
	Scan(dest ...any) error
}

// scanClickFact reads one clickFacts row.
// rows is the open result. clickCols and costCols select the column set
// from clickFactsSQL. Hits and Uniq come from the counts. Without costCols
// the cost fields stay zero and empty. A scan error is returned.
func scanClickFact(rows factRow, clickCols, costCols bool) (ClickFact, error) {
	var row ClickFact
	var hits, uniq uint64
	var err error
	switch {
	case clickCols && costCols:
		err = rows.Scan(&row.GroupID, &row.Stream, &row.OfferID, &row.LandingID, &row.NetworkID, &hits, &uniq, &row.CostCur, &row.CostCents)
	case clickCols:
		err = rows.Scan(&row.GroupID, &row.Stream, &row.OfferID, &row.LandingID, &row.NetworkID, &hits, &uniq)
	case costCols:
		err = rows.Scan(&row.GroupID, &row.Stream, &hits, &uniq, &row.CostCur, &row.CostCents)
	default:
		err = rows.Scan(&row.GroupID, &row.Stream, &hits, &uniq)
	}
	if err != nil {
		return ClickFact{}, err
	}
	row.Hits, row.Uniq = int64(hits), int64(uniq)
	return row, nil
}

func (c *CH) statusFacts(ctx context.Context, from, to time.Time, where string, wargs []any) ([]StatusFact, error) {
	q := `SELECT offer_id, network_id, status, count(), sum(profit)
		FROM conversions FINAL
		WHERE ts >= ? AND ts < ?`
	qargs := []any{from, to}
	if where != "" {
		q += eventsCidInSQL(where)
		qargs = append(qargs, from, to)
		qargs = append(qargs, wargs...)
	}
	q += ` GROUP BY offer_id, network_id, status`
	rows, err := c.conn.Query(ctx, q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusFact
	for rows.Next() {
		var row StatusFact
		var n uint64
		if err := rows.Scan(&row.OfferID, &row.NetworkID, &row.Status, &n, &row.Profit); err != nil {
			return nil, err
		}
		row.N = int64(n)
		out = append(out, row)
	}
	return out, rows.Err()
}

// groupStatusSQL joins conversions to one campaign id per click.
// where is empty or an events filter that starts with " AND ".
// The aggregate is aliased gid: group_id in where must stay the events column.
// Placeholders are from, to, the where args, then from, to. It does not fail.
func groupStatusSQL(where string) string {
	return `SELECT e.gid, c.status, count(), sum(c.profit)
		FROM conversions AS c FINAL
		INNER JOIN (
			SELECT cid, any(group_id) AS gid
			FROM events
			WHERE ts >= ? AND ts < ? AND cid != ''` + where + `
			GROUP BY cid
		) AS e ON e.cid = c.cid
		WHERE c.ts >= ? AND c.ts < ?
		GROUP BY e.gid, c.status`
}

// groupStatus reads conversion counts per campaign and status.
// where is empty or from buildWhere on events. wargs are its placeholders.
// It returns an error when the query or a scan fails.
func (c *CH) groupStatus(ctx context.Context, from, to time.Time, where string, wargs []any) ([]StatusFact, error) {
	evArgs := append([]any{from, to}, wargs...)
	rows, err := c.conn.Query(ctx, groupStatusSQL(where), append(evArgs, from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusFact
	for rows.Next() {
		var row StatusFact
		var n uint64
		if err := rows.Scan(&row.GroupID, &row.Status, &n, &row.Profit); err != nil {
			return nil, err
		}
		row.N = int64(n)
		out = append(out, row)
	}
	return out, rows.Err()
}

// bucketWhere is the filter for one manual spend window.
// includeBots keeps rows whose bot column is filled. hasCost drops clicks
// that already carry a price, so the window does not pay them again.
func bucketWhere(includeBots, hasCost bool) string {
	where := "group_id = ? AND ts >= ? AND ts < ?"
	if !includeBots {
		where += " AND (bot = '' OR bot = '-')"
	}
	if hasCost {
		where += " AND cost_cents = 0"
	}
	return where
}

// ClickBuckets counts clicks of one campaign in a half-open window.
// includeBots, when false, skips rows whose bot column is filled.
// Clicks that already carry a price are omitted when that column exists.
// It does not follow forwarded visits. An empty offer id stays its own bucket.
// It returns an error when the query fails.
func (c *CH) ClickBuckets(ctx context.Context, groupID string, from, to time.Time, includeBots bool) ([]BucketCount, error) {
	if c == nil || c.conn == nil {
		return nil, nil
	}
	where := bucketWhere(includeBots, c.costCols)
	q := `SELECT stream_name, count(), countIf(uniq = 1) FROM events WHERE ` + where + ` GROUP BY stream_name`
	if c.clickCols {
		q = `SELECT stream_name, offer_id, network_id, count(), countIf(uniq = 1)
			FROM events WHERE ` + where + `
			GROUP BY stream_name, offer_id, network_id`
	}
	rows, err := c.conn.Query(ctx, q, groupID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BucketCount
	for rows.Next() {
		var row BucketCount
		var hits, uniq uint64
		if c.clickCols {
			if err := rows.Scan(&row.Stream, &row.OfferID, &row.NetworkID, &hits, &uniq); err != nil {
				return nil, err
			}
		} else if err := rows.Scan(&row.Stream, &hits, &uniq); err != nil {
			return nil, err
		}
		row.Hits, row.Uniq = int64(hits), int64(uniq)
		out = append(out, row)
	}
	return out, rows.Err()
}
