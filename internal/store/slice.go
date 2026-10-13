package store

import (
	"context"
	"fmt"
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
func (c *CH) Slice(ctx context.Context, from, to time.Time) ([]SliceRow, error) {
	offer, source, extra := "''", "''", ""
	if c.clickCols {
		offer, source, extra = "offer_id", "source_id", ", offer_id, source_id"
	}
	q := fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s,
		count() AS hits, countIf(bot != '' AND bot != '-') AS bots
		FROM events WHERE ts >= ? AND ts < ?
		GROUP BY group_name, stream_name, country%s
		ORDER BY hits DESC LIMIT 2000`, offer, source, extra)
	rows, err := c.conn.Query(ctx, q, from, to)
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
	convOffer, convSource, convExtra := "''", "''", ""
	if c.clickCols {
		convOffer, convSource, convExtra = "offer_id", "source_id", ", offer_id, source_id"
	}
	cq := fmt.Sprintf(`SELECT group_name, stream_name, country, %s, %s, count() AS conv, sum(profit) AS profit
		FROM (
		  SELECT group_name, stream_name, country, '' AS offer_id, '' AS source_id, profit, ts FROM postbacks
		  UNION ALL
		  SELECT group_name, stream_name, country, %s AS offer_id, %s AS source_id, profit, ts FROM conversions
		) WHERE ts >= ? AND ts < ?
		GROUP BY group_name, stream_name, country%s`, convOffer, convSource, convOffer, convSource, convExtra)
	if !c.clickCols {
		cq = `SELECT group_name, stream_name, country, '' , '', count() AS conv, sum(profit) AS profit
		FROM (
		  SELECT group_name, stream_name, country, profit, ts FROM postbacks
		  UNION ALL
		  SELECT group_name, stream_name, country, profit, ts FROM conversions
		) WHERE ts >= ? AND ts < ?
		GROUP BY group_name, stream_name, country`
	}
	pbs, err := c.conn.Query(ctx, cq, from, to)
	if err != nil {
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
