package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// keywordSortExpr maps a sort key of the keyword table to its expression over the grouped union.
// The ratio is taken from the sums, never from per-row ratios.
var keywordSortExpr = map[string]string{
	"keyword": "k",
	"hits":    "sum(hits)",
	"uniq":    "sum(uniq_n)",
	"conv":    "sum(conv)",
	"leads":   "sum(leads)",
	"sales":   "sum(sales)",
	"rej":     "sum(rejected)",
	"cr":      "sum(conv) / greatest(sum(hits), 1)",
	"revenue": "sum(profit_s)",
	"depsum":  "sum(dep_s)",
}

// KeywordSortKeys are the keyword table columns a page can be sorted by, in the order of the table.
var KeywordSortKeys = []string{"keyword", "hits", "uniq", "conv", "leads", "sales", "rej", "cr", "revenue", "depsum"}

// KeywordDefaultSort is the keyword table order when the request names none: payout, then clicks.
const KeywordDefaultSort = "revenue"

// keywordPageOrder is the ORDER BY clause of a keyword page.
// key is a KeywordSortKeys entry or empty for keywordOrder; desc reverses that column only.
// keywordOrder follows as the tie-break, so the keyword decides the last tie and a page
// boundary falls on the same row on every request. ok is false for an unknown key.
func keywordPageOrder(key string, desc bool) (string, bool) {
	if key == "" {
		return keywordOrder, true
	}
	expr, ok := keywordSortExpr[key]
	if !ok {
		return "", false
	}
	dir := " ASC"
	if desc {
		dir = " DESC"
	}
	return "ORDER BY " + expr + dir + ", " + strings.TrimPrefix(keywordOrder, "ORDER BY "), true
}

// keywordTotalsSQL counts the keywords of union and sums every column over them.
// The SQL does not fail.
func keywordTotalsSQL(union string) string {
	return `SELECT uniqExact(k), sum(hits), sum(uniq_n), sum(conv), sum(leads), sum(sales), sum(rejected),
		sum(profit_s), sum(dep_s) FROM (
		` + union + `
		)`
}

// KeywordTotals is the total row of the keyword table: the sums of every matching keyword.
// Ratios are not here: they are taken from these sums.
type KeywordTotals struct {
	Hits     int64   `json:"hits"`
	Unique   int64   `json:"unique"`
	Conv     int64   `json:"conv"`
	Leads    int64   `json:"leads"`
	Sales    int64   `json:"sales"`
	Rejected int64   `json:"rejected"`
	Profit   float64 `json:"profit"`
	DepSum   float64 `json:"depsum"`
}

// KeywordPage is one page of the keyword table.
// Total is the number of keywords of the whole set and Totals their sums. Sort and Dir
// are the order applied. Truncated is always false: a page never cuts the set.
// ConvReady is as on KeywordStats.
type KeywordPage struct {
	Rows      []KeywordRow  `json:"rows"`
	Total     int           `json:"total"`
	Offset    int           `json:"offset"`
	Limit     int           `json:"limit"`
	Sort      string        `json:"sort"`
	Dir       string        `json:"dir"`
	Totals    KeywordTotals `json:"totals"`
	Truncated bool          `json:"truncated"`
	ConvReady bool          `json:"conv_ready"`
}

// KeywordStatsPage is one page of the keyword table, sorted on the server, with the totals of every keyword.
// ctx bounds both queries. from, to, conds and mode are those of KeywordStats.
// q.Sort is a KeywordSortKeys entry or empty for the payout order; q.Limit is 1..PageMaxRows.
// The first query counts and sums the keywords; the page query runs only when the offset is inside the set.
// Errors are ErrUnknownField and ErrBadCond (a bad range, sort or limit included) and any query or scan error.
func (c *CH) KeywordStatsPage(ctx context.Context, from, to time.Time, conds []Cond, mode KeywordMode, q PageQuery) (KeywordPage, error) {
	order, ok := keywordPageOrder(q.Sort, q.Desc)
	if !ok || q.Limit < 1 || q.Limit > PageMaxRows || q.Offset < 0 {
		return KeywordPage{}, fmt.Errorf("%w: bad page", ErrBadCond)
	}
	if !KeywordRangeOK(from, to) {
		return KeywordPage{}, fmt.Errorf("%w: bad range", ErrBadCond)
	}
	p, skip, err := c.planKeywords(conds, mode)
	if err != nil {
		return KeywordPage{}, err
	}
	out := KeywordPage{Rows: []KeywordRow{}, Offset: q.Offset, Limit: q.Limit, Sort: q.Sort, Dir: q.Dir()}
	if q.Sort == "" {
		out.Sort, out.Dir = KeywordDefaultSort, "desc"
	}
	if skip {
		return out, nil
	}
	out.ConvReady = p.convReady
	ctx = c.keywordCtx(ctx)
	union, args := c.keywordUnion(p, from, to)
	if out.Total, out.Totals, err = c.keywordTotals(ctx, keywordTotalsSQL(union), args); err != nil {
		return KeywordPage{}, err
	}
	if q.Offset >= out.Total {
		return out, nil
	}
	sel := `k, sum(hits), sum(uniq_n), sum(conv), sum(leads), sum(sales), sum(rejected), sum(profit_s), sum(dep_s)`
	if out.Rows, _, err = c.keywordRows(ctx, keywordOrderedSQL(sel, union, order, q.Limit, q.Offset), args, q.Limit); err != nil {
		return KeywordPage{}, err
	}
	return out, nil
}

// keywordTotals runs keywordTotalsSQL and returns the keyword count and the sums.
// Any query or scan error is returned.
func (c *CH) keywordTotals(ctx context.Context, q string, args []any) (int, KeywordTotals, error) {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return 0, KeywordTotals{}, err
	}
	defer rows.Close()
	var t KeywordTotals
	var n, hits, uniq, conv, leads, sales, rejected uint64
	if rows.Next() {
		if err := rows.Scan(&n, &hits, &uniq, &conv, &leads, &sales, &rejected, &t.Profit, &t.DepSum); err != nil {
			return 0, KeywordTotals{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, KeywordTotals{}, err
	}
	t.Hits, t.Unique, t.Conv = int64(hits), int64(uniq), int64(conv)
	t.Leads, t.Sales, t.Rejected = int64(leads), int64(sales), int64(rejected)
	return int(n), t, nil
}
