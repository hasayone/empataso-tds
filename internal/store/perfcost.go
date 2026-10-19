package store

import (
	"context"
	"strings"
	"time"
)

// window is one stats query range plus the events filter.
// from and to bound the half-open interval. where is empty or a buildWhere
// fragment that starts with " AND ". args are the placeholders in where. conds
// are the conditions where was built from; they choose the unique aggregate.
type window struct {
	from, to time.Time
	where    string
	args     []any
	conds    []Cond
}

// perfKey is one performance cell: a campaign id or a display name, plus the stream.
type perfKey struct{ group, stream string }

// perfIndex locates a performance cell by campaign id, then by display name.
// byID stores a non-empty group_id and stream. byName stores group_name and stream.
type perfIndex struct {
	byID   map[perfKey]int
	byName map[perfKey]int
}

// at returns the row for this campaign and stream, appending one when it is missing.
// out is the table being built. groupID is the campaign id, empty on a legacy
// conversion. group is the display name. stream is the stream.
// A non-empty groupID is tried first, so a rename still finds the campaign.
// An empty groupID matches by name, and so does a groupID whose stored row has
// no id yet. Two different ids that share a name stay two rows.
// A new row keeps GroupID. It returns the index into out.
func (p *perfIndex) at(out *[]PerfRow, groupID, group, stream string) int {
	if p.byID == nil {
		p.byID = map[perfKey]int{}
	}
	if p.byName == nil {
		p.byName = map[perfKey]int{}
	}
	if groupID != "" {
		if i, ok := p.byID[perfKey{groupID, stream}]; ok {
			return i
		}
	}
	if i, ok := p.byName[perfKey{group, stream}]; ok {
		if groupID == "" || (*out)[i].GroupID == "" || (*out)[i].GroupID == groupID {
			if groupID != "" && (*out)[i].GroupID == "" {
				(*out)[i].GroupID = groupID
				p.byID[perfKey{groupID, stream}] = i
			}
			return i
		}
	}
	i := len(*out)
	*out = append(*out, PerfRow{GroupID: groupID, Group: group, Stream: stream})
	if groupID != "" {
		p.byID[perfKey{groupID, stream}] = i
	}
	if _, taken := p.byName[perfKey{group, stream}]; !taken {
		p.byName[perfKey{group, stream}] = i
	}
	return i
}

// performanceCostSQL is the spend query for Performance.
// where is empty or a buildWhere fragment that starts with " AND ".
// Placeholders are from, to, then the where args. Clicks with no price are
// excluded. The caller merges currencies onto one row. It does not fail.
func performanceCostSQL(where string) string {
	return `SELECT group_id, group_name, stream_name, cost_cur, sum(cost_cents)
		 FROM events WHERE ts >= ? AND ts < ?` + where + ` AND cost_cents > 0
		 GROUP BY group_id, group_name, stream_name, cost_cur`
}

// addCost fills Cost on performance rows from click prices.
// w is the report window and its events filter. out is the table built so far.
// index finds the cell by campaign id first. A missing cell is appended with
// GroupID, Group and Stream. Cents of one currency stay on that row.
// A query or scan error is returned.
func (c *CH) addCost(ctx context.Context, w window, out *[]PerfRow, index *perfIndex) error {
	args := append([]any{w.from, w.to}, w.args...)
	rows, err := c.conn.Query(ctx, performanceCostSQL(w.where), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var groupID, groupName, stream, cur string
		var cents int64
		if err := rows.Scan(&groupID, &groupName, &stream, &cur, &cents); err != nil {
			return err
		}
		i := index.at(out, groupID, groupName, stream)
		if (*out)[i].Cost == nil {
			(*out)[i].Cost = map[string]int64{}
		}
		(*out)[i].Cost[cur] += cents
	}
	return rows.Err()
}

// addSeriesCost adds click prices to chart buckets, cents by currency.
// w is the report window and its events filter. stepSec is the bucket length.
// hits is updated in place. A query or scan error is returned.
func (c *CH) addSeriesCost(ctx context.Context, w window, stepSec int, hits map[int64]seriesHit) error {
	args := append([]any{stepSec, w.from, w.to}, w.args...)
	rows, err := c.conn.Query(ctx, costSeriesSQL(w.where), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts time.Time
		var cur string
		var cents int64
		if err := rows.Scan(&ts, &cur, &cents); err != nil {
			return err
		}
		k := ts.UTC().Unix()
		h := hits[k]
		if h.cost == nil {
			h.cost = map[string]int64{}
		}
		h.cost[cur] += cents
		hits[k] = h
	}
	return rows.Err()
}

// addForwarded adds the visits each linking stream sent on to another group.
// w is the report window and its events filter. index finds the cell by campaign id.
// A missing cell is appended with that id. A query or scan error is returned.
func (c *CH) addForwarded(ctx context.Context, w window, out *[]PerfRow, index *perfIndex) error {
	args := append([]any{w.from, w.to}, w.args...)
	rows, err := c.conn.Query(ctx, forwardedSQL(c.uniqCount(w.conds), w.where), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var via string
		var hits, uniq, bots uint64
		if err := rows.Scan(&via, &hits, &uniq, &bots); err != nil {
			return err
		}
		gid, stream, ok := strings.Cut(via, "/")
		if !ok {
			continue
		}
		i := index.at(out, gid, "", stream)
		p := &(*out)[i]
		p.Hits += int64(hits)
		p.Unique += int64(uniq)
		p.Bots += int64(bots)
		p.Forwarded += int64(hits)
	}
	return rows.Err()
}
