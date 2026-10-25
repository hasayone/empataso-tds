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

// SliceMeta is SliceFiltered that also says whether the visit query hit SliceLimit.
// ctx bounds both queries. from and to are the window. conds filter both sides.
// It returns the rows, truncated true when more than SliceLimit visit groups exist (only the first SliceLimit by hits are kept), and the same errors as SliceFiltered.
// Conversion groups outside the kept visit groups still come back as rows with Hits 0, so a truncated report is partial on both sides.
func (c *CH) SliceMeta(ctx context.Context, from, to time.Time, conds []Cond) ([]SliceRow, bool, error) {
	evSQL, evArgs, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return nil, false, err
	}
	if skip {
		return []SliceRow{}, false, nil
	}
	convOK := true
	var cvSQL string
	var cvArgs []any
	for _, cond := range conds {
		f, ok := fieldByName(cond.Field)
		if !ok || f.Conv == "" {
			convOK = false
			break
		}
	}
	if convOK {
		var skipC bool
		cvSQL, cvArgs, skipC, err = buildWhere(c, "conversions", conds)
		if err != nil {
			return nil, false, err
		}
		if skipC {
			convOK = false
		}
	}
	q := sliceEventsSQL(c.clickCols, c.uniqCount(conds, sliceDims(c.clickCols)...), c.firstStepUniq(), evSQL)
	rows, err := c.conn.Query(ctx, q, append([]any{from, to}, evArgs...)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	type key struct{ group, stream, country, offer, source string }
	var out []SliceRow
	index := map[key]int{}
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
		index[key{row.Group, row.Stream, row.Country, row.Offer, row.Source}] = len(out)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
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
		var profit float64
		if err := pbs.Scan(&group, &stream, &country, &offerID, &sourceID, &conv, &profit); err != nil {
			return out, truncated, nil
		}
		i, ok := index[key{group, stream, country, offerID, sourceID}]
		if !ok {
			i = len(out)
			out = append(out, SliceRow{Group: group, Stream: stream, Country: country, Offer: offerID, Source: sourceID})
		}
		out[i].Conv, out[i].Profit = int64(conv), profit
	}
	if out == nil {
		out = []SliceRow{}
	}
	return out, truncated, nil
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
// The string is a full SELECT. It does not fail.
func sliceConvSQL(c *CH, cvSQL string) string {
	offer, source, extra := "''", "''", ""
	click, ext, model, origin := false, false, false, false
	if c != nil {
		click, ext, model, origin = c.clickCols, c.extConv, c.modelConv, c.originConv.Load()
		if c.clickCols {
			offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
		}
	}
	return fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s, count() AS conv, sum(profit) AS profit_sum
		FROM (
		%s
		) WHERE ts >= ? AND ts < ?%s%s
		GROUP BY group_name, stream_name, country%s`,
		offer, source, conversionUnion(click, ext, model, origin), voidedConfirmedSQL("cid", "status"), cvSQL, extra)
}
