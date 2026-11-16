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

// SliceMeta is SliceFiltered that also says whether the visit query hit SliceLimit.
// ctx bounds both queries. from and to are the window. conds filter both sides.
// It returns the rows, truncated true when more than SliceLimit visit groups exist (only the first SliceLimit by hits are kept), and the same errors as SliceFiltered.
// Conversion groups outside the kept visit groups still come back as rows with Hits 0, so a truncated report is partial on both sides.
func (c *CH) SliceMeta(ctx context.Context, from, to time.Time, conds []Cond) ([]SliceRow, bool, error) {
	ctx = c.analyticsCtx(ctx, 0)
	evSQL, evArgs, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return nil, false, err
	}
	if skip {
		return []SliceRow{}, false, nil
	}
	cvSQL, cvArgs, convOK, err := convWhere(c, conds)
	if err != nil {
		return nil, false, err
	}
	q := sliceEventsSQL(c.clickCols, c.uniqCount(conds, sliceDims(c.clickCols)...), c.firstStepUniq(), evSQL)
	rows, err := c.conn.Query(ctx, q, append([]any{from, to}, evArgs...)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []SliceRow
	index := map[sliceKey]int{}
	truncated := false
	for rows.Next() {
		if len(out) >= SliceLimit {
			truncated = true
			break
		}
		var row SliceRow
		var hits, uniq, first, bots uint64
		if err := rows.Scan(&row.Group, &row.Stream, &row.Country, &row.Offer, &row.Source, &hits, &uniq, &first, &bots); err != nil {
			return nil, false, err
		}
		row.Hits, row.Unique, row.UniqFirst, row.Bots = int64(hits), int64(uniq), int64(first), int64(bots)
		index[sliceKey{row.Group, row.Stream, row.Country, row.Offer, row.Source}] = len(out)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if c.costCols {
		out, err = c.addSliceCost(ctx, sliceCostSQL(c.clickCols, evSQL), append([]any{from, to}, evArgs...), out, index, truncated)
		if err != nil {
			log.Printf("clickhouse: slice cost: %v", err)
		}
	}
	if !convOK {
		if out == nil {
			out = []SliceRow{}
		}
		return out, truncated, nil
	}
	cq := sliceConvSQL(c, cvSQL)
	pbs, err := c.conn.Query(ctx, cq, append([]any{from, to}, cvArgs...)...)
	if err != nil {
		log.Printf("clickhouse: slice conversions: %v", err)
		return out, truncated, nil
	}
	defer pbs.Close()
	for pbs.Next() {
		var group, stream, country, offerID, sourceID string
		var conv uint64
		var profit, dep float64
		if err := pbs.Scan(&group, &stream, &country, &offerID, &sourceID, &conv, &profit, &dep); err != nil {
			return out, truncated, nil
		}
		i, ok := index[sliceKey{group, stream, country, offerID, sourceID}]
		if !ok {
			i = len(out)
			out = append(out, SliceRow{Group: group, Stream: stream, Country: country, Offer: offerID, Source: sourceID})
		}
		out[i].Conv, out[i].Profit, out[i].DepSum = int64(conv), profit, dep
	}
	if out == nil {
		out = []SliceRow{}
	}
	return out, truncated, nil
}

// sliceKey is one report cell: campaign, stream, country, offer and source.
type sliceKey struct{ group, stream, country, offer, source string }

// addSliceCost adds the click cost of the period to the report rows, cents by currency.
// ctx bounds the query. args are from, to and the events filter args of sliceCostSQL.
// out and index are the rows built so far. A cell that has no visit row is appended, unless
// truncated says the visit rows were cut: then it is dropped, so the answer stays bounded.
// It returns the rows, with what was added before a query or scan error, and that error.
func (c *CH) addSliceCost(ctx context.Context, q string, args []any, out []SliceRow, index map[sliceKey]int, truncated bool) ([]SliceRow, error) {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k sliceKey
		var cur string
		var cents int64
		if err := rows.Scan(&k.group, &k.stream, &k.country, &k.offer, &k.source, &cur, &cents); err != nil {
			return out, err
		}
		i, ok := index[k]
		if !ok && truncated {
			continue
		}
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, SliceRow{Group: k.group, Stream: k.stream, Country: k.country, Offer: k.offer, Source: k.source})
		}
		if out[i].Cost == nil {
			out[i].Cost = map[string]int64{}
		}
		out[i].Cost[cur] += cents
	}
	return out, rows.Err()
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
// the filter args. The string is a full SELECT that asks for SliceLimit+1 groups,
// the extra one only tells SliceMeta the report is cut. It does not fail.
func sliceEventsSQL(clickCols bool, uniq, first, evSQL string) string {
	offer, source, extra := "''", "''", ""
	if clickCols {
		offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
	}
	return fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s,
		count() AS hits, %s AS uniq_hits, %s AS uniq_first, countIf(bot != '' AND bot != '-') AS bots
		FROM events WHERE ts >= ? AND ts < ?`, offer, source, uniq, first) + evSQL + fmt.Sprintf(`
		GROUP BY group_name, stream_name, country%s
		ORDER BY hits DESC LIMIT %d`, extra, SliceLimit+1)
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
