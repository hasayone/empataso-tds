package store

import (
	"context"
	"fmt"
	"log"
	"time"
)

// SliceLimit is the most visit groups a period report keeps. The visit query asks for one more, so a full
// answer tells a truncated report from one that fits.
const SliceLimit = 2000

// SliceRow is one cell of the period report.
// It is not merged into the dashboard performance rows.
// Unique follows uniqCount: with the offer column grouped on, a row counts its
// own uniques, so a landing row and an offer row each have theirs. UniqFirst
// counts first-step uniques only; unlike Unique it can be summed over rows, and
// the sum is the campaign's uniques, as in the dashboard summary.
type SliceRow struct {
	Group     string  `json:"group"`
	Stream    string  `json:"stream"`
	Country   string  `json:"country"`
	Offer     string  `json:"offer"`
	Source    string  `json:"source"`
	Hits      int64   `json:"hits"`
	Unique    int64   `json:"unique"`
	UniqFirst int64   `json:"uniq_first"`
	Bots      int64   `json:"bots"`
	Conv      int64   `json:"conv"`
	Profit    float64 `json:"profit"`
	// DepSum is the sum of deposits. It is not the payout in Profit.
	DepSum float64 `json:"depsum"`
	// Cost is the click cost of the row in cents by currency code, as on PerfRow.
	// An empty code is the tracker currency of imported clicks. It is absent when no click has a price.
	Cost map[string]int64 `json:"cost,omitempty"`
}

// Slice groups visits for the report page.
// Offer and source stay empty when the click columns are not on the table.
// It is SliceFiltered with no extra conditions. Errors are the same.
func (c *CH) Slice(ctx context.Context, from, to time.Time) ([]SliceRow, error) {
	return c.SliceFiltered(ctx, from, to, nil)
}

// SliceFiltered groups visits and conversions for the report page.
// ctx bounds both queries. from and to are the window. conds filter both
// sides. A condition on a field the conversions table does not have yields
// visits only: conversions stay zero and that is not an error.
// A condition on a wave that is not ready on events returns an empty slice
// without a query. ErrUnknownField and ErrBadCond come from the events side.
// A failed conversion query is logged, keeps the visit rows and returns a
// nil error, matching the unfiltered report.
func (c *CH) SliceFiltered(ctx context.Context, from, to time.Time, conds []Cond) ([]SliceRow, error) {
	rows, _, err := c.SliceMeta(ctx, from, to, conds)
	return rows, err
}

// convWhere is the conversions side of conds for a period query.
// c supplies wave readiness. conds are the conditions of the whole query.
// sql is the buildWhere fragment (empty or starting with " AND ") and args its placeholder values.
// ok is false when a condition names a field the conversions table does not have, or a wave
// that is not ready there: the caller then reads visits only, and that is not an error.
// err is ErrUnknownField or ErrBadCond from buildWhere.
func convWhere(c *CH, conds []Cond) (sql string, args []any, ok bool, err error) {
	for _, cond := range conds {
		f, found := fieldByName(cond.Field)
		if !found || f.Conv == "" {
			return "", nil, false, nil
		}
	}
	sql, args, skip, err := buildWhere(c, "conversions", conds)
	if err != nil {
		return "", nil, false, err
	}
	if skip {
		return "", nil, false, nil
	}
	return sql, args, true, nil
}

// SliceTotals is the total row of the period report: the sums of the counts and money of
// every matching cell. Unique sums first-step uniques, the summable count of SliceRow.UniqFirst,
// as the report total did on the client. Ratios are not here: they are taken from these sums.
// Cost is cents by currency code, as on SliceRow; it is absent when no click has a price.
type SliceTotals struct {
	Hits   int64            `json:"hits"`
	Unique int64            `json:"unique"`
	Bots   int64            `json:"bots"`
	Conv   int64            `json:"conv"`
	Profit float64          `json:"profit"`
	DepSum float64          `json:"depsum"`
	Cost   map[string]int64 `json:"cost,omitempty"`
}

// SliceMeta is SliceFiltered that also says whether the visit query hit SliceLimit.
// ctx bounds both queries. from and to are the window. conds filter both sides.
// It returns the rows, truncated true when more than SliceLimit visit groups exist (only the first SliceLimit by hits are kept), and the same errors as SliceFiltered.
// Conversion groups outside the kept visit groups still come back as rows with Hits 0, so a truncated report is partial on both sides.
func (c *CH) SliceMeta(ctx context.Context, from, to time.Time, conds []Cond) ([]SliceRow, bool, error) {
	rows, _, truncated, err := c.sliceRows(ctx, from, to, conds, SliceLimit, false)
	return rows, truncated, err
}

// SliceAll is the period report for paging: every cell up to PageMaxRows visit groups,
// and the totals of the whole filtered period.
// ctx bounds every query. from and to are the window. conds filter both sides, as in SliceFiltered.
// It returns the rows in no set order, the totals, truncated true when more than PageMaxRows
// visit groups exist (then only the first PageMaxRows by hits are rows, while the totals still
// count every visit), and the errors of SliceFiltered; a failed visit totals query is an error too.
func (c *CH) SliceAll(ctx context.Context, from, to time.Time, conds []Cond) ([]SliceRow, SliceTotals, bool, error) {
	return c.sliceRows(ctx, from, to, conds, PageMaxRows, true)
}

// sliceRows reads the report cells: visits, then click cost, then conversions.
// ctx bounds every query. from and to are the window and conds filter both sides.
// limit is the most visit groups kept, by hits. exact asks for the visit totals of every group
// when the visit groups were cut; without it those totals count only the kept groups.
// The cost and conversion totals always count every group, kept or not.
// It returns the rows, the totals, whether the visit groups were cut, and the errors of SliceMeta.
// A failed cost or conversion query is logged and keeps what was read, as SliceMeta always did.
func (c *CH) sliceRows(ctx context.Context, from, to time.Time, conds []Cond, limit int, exact bool) ([]SliceRow, SliceTotals, bool, error) {
	ctx = c.analyticsCtx(ctx, 0)
	var tot SliceTotals
	evSQL, evArgs, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return nil, tot, false, err
	}
	if skip {
		return []SliceRow{}, tot, false, nil
	}
	cvSQL, cvArgs, convOK, err := convWhere(c, conds)
	if err != nil {
		return nil, tot, false, err
	}
	evAll := append([]any{from, to}, evArgs...)
	q := sliceEventsSQL(c.clickCols, c.uniqCount(conds, sliceDims(c.clickCols)...), c.firstStepUniq(), evSQL, limit)
	out, index, truncated, err := c.sliceVisits(ctx, q, evAll, limit, &tot)
	if err != nil {
		return nil, tot, false, err
	}
	if truncated && exact {
		if err := c.sliceVisitTotals(ctx, sliceEventTotalsSQL(c.firstStepUniq(), evSQL), evAll, &tot); err != nil {
			return nil, tot, false, err
		}
	}
	if c.costCols {
		set := sliceSet{rows: out, index: index, truncated: truncated, tot: &tot}
		if err := c.addSliceCost(ctx, sliceCostSQL(c.clickCols, evSQL), evAll, &set); err != nil {
			log.Printf("clickhouse: slice cost: %v", err)
		}
		out = set.rows
	}
	if !convOK {
		if out == nil {
			out = []SliceRow{}
		}
		return out, tot, truncated, nil
	}
	cq := sliceConvSQL(c, cvSQL)
	pbs, err := c.conn.Query(ctx, cq, append([]any{from, to}, cvArgs...)...)
	if err != nil {
		log.Printf("clickhouse: slice conversions: %v", err)
		return out, tot, truncated, nil
	}
	defer pbs.Close()
	for pbs.Next() {
		var group, stream, country, offerID, sourceID string
		var conv uint64
		var profit, dep float64
		if err := pbs.Scan(&group, &stream, &country, &offerID, &sourceID, &conv, &profit, &dep); err != nil {
			return out, tot, truncated, nil
		}
		i, ok := index[sliceKey{group, stream, country, offerID, sourceID}]
		if !ok {
			i = len(out)
			out = append(out, SliceRow{Group: group, Stream: stream, Country: country, Offer: offerID, Source: sourceID})
		}
		out[i].Conv, out[i].Profit, out[i].DepSum = int64(conv), profit, dep
		tot.Conv += int64(conv)
		tot.Profit += profit
		tot.DepSum += dep
	}
	if out == nil {
		out = []SliceRow{}
	}
	return out, tot, truncated, nil
}

// sliceVisits runs the visit query q with args and keeps at most limit groups.
// tot gets the hits, first-step uniques and bots of the kept groups.
// It returns the rows, their index by cell, truncated true when the query had one group more,
// and any query or scan error.
func (c *CH) sliceVisits(ctx context.Context, q string, args []any, limit int, tot *SliceTotals) ([]SliceRow, map[sliceKey]int, bool, error) {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	var out []SliceRow
	index := map[sliceKey]int{}
	for rows.Next() {
		if len(out) >= limit {
			return out, index, true, nil
		}
		var row SliceRow
		var hits, uniq, first, bots uint64
		if err := rows.Scan(&row.Group, &row.Stream, &row.Country, &row.Offer, &row.Source, &hits, &uniq, &first, &bots); err != nil {
			return nil, nil, false, err
		}
		row.Hits, row.Unique, row.UniqFirst, row.Bots = int64(hits), int64(uniq), int64(first), int64(bots)
		index[sliceKey{row.Group, row.Stream, row.Country, row.Offer, row.Source}] = len(out)
		out = append(out, row)
		tot.Hits += row.Hits
		tot.Unique += row.UniqFirst
		tot.Bots += row.Bots
	}
	return out, index, false, rows.Err()
}

// sliceVisitTotals replaces the visit part of tot with the totals of every group.
// q is sliceEventTotalsSQL and args its placeholders. Any query or scan error is returned.
func (c *CH) sliceVisitTotals(ctx context.Context, q string, args []any, tot *SliceTotals) error {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var hits, first, bots uint64
	if rows.Next() {
		if err := rows.Scan(&hits, &first, &bots); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tot.Hits, tot.Unique, tot.Bots = int64(hits), int64(first), int64(bots)
	return nil
}

// sliceKey is one report cell: campaign, stream, country, offer and source.
type sliceKey struct{ group, stream, country, offer, source string }

// sliceSet is the report being built: its rows, their index by cell, whether the visit
// rows were cut at the limit, and the totals of the whole set.
type sliceSet struct {
	rows      []SliceRow
	index     map[sliceKey]int
	truncated bool
	tot       *SliceTotals
}

// addSliceCost adds the click cost of the period to the report rows of set, cents by currency.
// ctx bounds the query. args are from, to and the events filter args of sliceCostSQL.
// A cell that has no visit row is appended, unless set.truncated says the visit rows were cut:
// then it is dropped, so the answer stays bounded. set.tot.Cost gets the cost of every cell,
// dropped ones included. It returns a query or scan error; what was added before it stays in set.
func (c *CH) addSliceCost(ctx context.Context, q string, args []any, set *sliceSet) error {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k sliceKey
		var cur string
		var cents int64
		if err := rows.Scan(&k.group, &k.stream, &k.country, &k.offer, &k.source, &cur, &cents); err != nil {
			return err
		}
		if set.tot.Cost == nil {
			set.tot.Cost = map[string]int64{}
		}
		set.tot.Cost[cur] += cents
		i, ok := set.index[k]
		if !ok && set.truncated {
			continue
		}
		if !ok {
			i = len(set.rows)
			set.index[k] = i
			set.rows = append(set.rows, SliceRow{Group: k.group, Stream: k.stream, Country: k.country, Offer: k.offer, Source: k.source})
		}
		if set.rows[i].Cost == nil {
			set.rows[i].Cost = map[string]int64{}
		}
		set.rows[i].Cost[cur] += cents
	}
	return rows.Err()
}

// sliceCostSQL is the click cost half of the period report, grouped like sliceEventsSQL plus the currency.
// clickCols adds the offer and source columns. evSQL is the buildWhere fragment, or empty.
// Placeholders are from, to, then the filter args. Clicks with no price are left out. It does not fail.
func sliceCostSQL(clickCols bool, evSQL string) string {
	offer, source, extra := "''", "''", ""
	if clickCols {
		offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
	}
	return fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s, cost_cur, sum(cost_cents)
		FROM events WHERE ts >= ? AND ts < ?`, offer, source) + evSQL + fmt.Sprintf(` AND cost_cents > 0
		GROUP BY group_name, stream_name, country%s, cost_cur`, extra)
}

// sliceDims lists the id fields the report groups events by.
// clickCols says the offer and source columns exist and are grouped on.
func sliceDims(clickCols bool) []string {
	if clickCols {
		return []string{"offer_id", "source_id"}
	}
	return nil
}

// sliceEventsSQL is the visit half of the period report.
// clickCols adds the offer and source columns to the select and the grouping.
// uniq is the aggregate of the row's own uniques (uniqCount, which looks at the
// grouping) and first the aggregate of first-step uniques, summable over rows.
// evSQL is the buildWhere fragment, or empty. Placeholders are from, to, then
// the filter args. limit is the most groups the caller keeps. The string is a full
// SELECT that asks for limit+1 groups by hits; the extra one only tells the caller
// the report is cut. It does not fail.
func sliceEventsSQL(clickCols bool, uniq, first, evSQL string, limit int) string {
	offer, source, extra := "''", "''", ""
	if clickCols {
		offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
	}
	return fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s,
		count() AS hits, %s AS uniq_hits, %s AS uniq_first, countIf(bot != '' AND bot != '-') AS bots
		FROM events WHERE ts >= ? AND ts < ?`, offer, source, uniq, first) + evSQL + fmt.Sprintf(`
		GROUP BY group_name, stream_name, country%s
		ORDER BY hits DESC LIMIT %d`, extra, limit+1)
}

// sliceEventTotalsSQL is the visit total of the period report over every group:
// hits, first-step uniques and bots, the sums of the same columns of sliceEventsSQL.
// first is the first-step unique aggregate and evSQL the buildWhere fragment, or empty.
// Placeholders are from, to, then the filter args. It does not fail.
func sliceEventTotalsSQL(first, evSQL string) string {
	return `SELECT count() AS hits, ` + first + ` AS uniq_first, countIf(bot != '' AND bot != '-') AS bots
		FROM events WHERE ts >= ? AND ts < ?` + evSQL
}

// sliceConvSQL is the conversion half of the period report.
// c selects the click, ext and model waves of conversionUnion, so the outer WHERE
// sees every conversions column, including ones the postbacks table lacks.
// cvSQL is the extra AND fragment from buildWhere, or empty.
// The payout sum is named profit_sum so a profit filter stays the column.
// The deposit sum is named depsum_sum. A closed wave uses a typed zero and
// does not name the column. The string is a full SELECT. It does not fail.
func sliceConvSQL(c *CH, cvSQL string) string {
	offer, source, extra := "''", "''", ""
	click, ext, model, origin, root := false, false, false, false, false
	depSum := "toFloat64(0) AS depsum_sum"
	if c != nil {
		click, ext, model, origin, root = c.clickCols, c.extConv, c.modelConv, c.originConv.Load(), c.ready("conversions", waveRoot)
		if c.clickCols {
			offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
		}
		if c.depsumConv {
			depSum = "sum(depsum) AS depsum_sum"
		}
	}
	return fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s, count() AS conv, sum(profit) AS profit_sum, %s
		FROM (
		%s
		) WHERE ts >= ? AND ts < ?%s%s
		GROUP BY group_name, stream_name, country%s`,
		offer, source, depSum, conversionUnion(click, ext, model, conversionLateWaves{origin: origin, root: root}, c != nil && c.subConv, c != nil && c.depsumConv), voidedConfirmedSQL("cid", "status"), cvSQL, extra)
}
