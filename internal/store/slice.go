package store

import (
	"context"
	"fmt"
	"log"
	"time"
)

// SliceRow is one cell of the period report.
// It is not merged into the dashboard performance rows.
type SliceRow struct {
	Group   string  `json:"group"`
	Stream  string  `json:"stream"`
	Country string  `json:"country"`
	Offer   string  `json:"offer"`
	Source  string  `json:"source"`
	Hits    int64   `json:"hits"`
	Bots    int64   `json:"bots"`
	Conv    int64   `json:"conv"`
	Profit  float64 `json:"profit"`
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
	evSQL, evArgs, skip, err := buildWhere(c, "events", conds)
	if err != nil {
		return nil, err
	}
	if skip {
		return []SliceRow{}, nil
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
			return nil, err
		}
		if skipC {
			convOK = false
		}
	}
	offer, source, extra := "''", "''", ""
	if c.clickCols {
		offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
	}
	q := fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s,
		count() AS hits, countIf(bot != '' AND bot != '-') AS bots
		FROM events WHERE ts >= ? AND ts < ?`, offer, source) + evSQL + fmt.Sprintf(`
		GROUP BY group_name, stream_name, country%s
		ORDER BY hits DESC LIMIT 2000`, extra)
	rows, err := c.conn.Query(ctx, q, append([]any{from, to}, evArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ group, stream, country, offer, source string }
	var out []SliceRow
	index := map[key]int{}
	for rows.Next() {
		var row SliceRow
		var hits, bots uint64
		if err := rows.Scan(&row.Group, &row.Stream, &row.Country, &row.Offer, &row.Source, &hits, &bots); err != nil {
			return nil, err
		}
		row.Hits, row.Bots = int64(hits), int64(bots)
		index[key{row.Group, row.Stream, row.Country, row.Offer, row.Source}] = len(out)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !convOK {
		if out == nil {
			out = []SliceRow{}
		}
		return out, nil
	}
	cq := sliceConvSQL(c, cvSQL)
	pbs, err := c.conn.Query(ctx, cq, append([]any{from, to}, cvArgs...)...)
	if err != nil {
		log.Printf("clickhouse: slice conversions: %v", err)
		return out, nil
	}
	defer pbs.Close()
	for pbs.Next() {
		var group, stream, country, offerID, sourceID string
		var conv uint64
		var profit float64
		if err := pbs.Scan(&group, &stream, &country, &offerID, &sourceID, &conv, &profit); err != nil {
			return out, nil
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
	return out, nil
}

// sliceConvSQL is the conversion half of the period report.
// c selects the click and ext waves of conversionFrom, so the outer WHERE
// sees every conversions column, including ones the postbacks table lacks.
// cvSQL is the extra AND fragment from buildWhere, or empty.
// The payout sum is named profit_sum so a profit filter stays the column.
// The string is a full SELECT. It does not fail.
func sliceConvSQL(c *CH, cvSQL string) string {
	offer, source, extra := "''", "''", ""
	click, ext := false, false
	if c != nil {
		click, ext = c.clickCols, c.extConv
		if c.clickCols {
			offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
		}
	}
	return fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s, count() AS conv, sum(profit) AS profit_sum
		FROM (
		%s
		) WHERE ts >= ? AND ts < ?%s
		GROUP BY group_name, stream_name, country%s`,
		offer, source, conversionFrom(click, ext), cvSQL, extra)
}
