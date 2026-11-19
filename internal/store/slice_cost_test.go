package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestSliceCostSQL checks the cost query groups like the visit query plus the currency,
// keeps the filter and leaves unpriced clicks out.
func TestSliceCostSQL(t *testing.T) {
	sql := sliceCostSQL(true, " AND country = ?")
	for _, want := range []string{"offer_id, source_id, cost_cur, sum(cost_cents)", "ts < ? AND country = ? AND cost_cents > 0",
		"GROUP BY group_name, stream_name, country, offer_id, source_id, cost_cur"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q:\n%s", want, sql)
		}
	}
	if bare := sliceCostSQL(false, ""); strings.Contains(bare, "offer_id") || !strings.Contains(bare, "''") {
		t.Errorf("no click columns:\n%s", bare)
	}
}

// TestSliceMetaAddsCost checks the report row gets the click cost by currency, a row with no
// price keeps no cost key, and a store without the cost wave asks no cost query.
func TestSliceMetaAddsCost(t *testing.T) {
	from, to := time.Now().Add(-time.Hour), time.Now()
	conn := &fakeConn{results: []*fakeRows{
		{data: [][]any{
			{"g1", "s1", "us", "", "", uint64(10), uint64(8), uint64(8), uint64(0)},
			{"g2", "s1", "pl", "", "", uint64(5), uint64(5), uint64(5), uint64(1)},
		}},
		{data: [][]any{
			{"g1", "s1", "us", "", "", "", int64(150)},
			{"g1", "s1", "us", "", "", "EUR", int64(40)},
			{"g1", "s1", "us", "", "", "", int64(10)},
		}},
		{},
	}}
	c := &CH{conn: conn, costCols: true}
	rows, truncated, err := c.SliceMeta(context.Background(), from, to, nil)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if len(conn.queries) != 3 || !strings.Contains(conn.queries[1], "sum(cost_cents)") {
		t.Fatalf("queries: %q", conn.queries)
	}
	if len(rows) != 2 || !reflect.DeepEqual(rows[0].Cost, map[string]int64{"": 160, "EUR": 40}) || rows[1].Cost != nil {
		t.Fatalf("rows %+v", rows)
	}

	cut := &fakeConn{results: []*fakeRows{{data: [][]any{
		{"g1", "s1", "us", "", "", "", int64(5)},
		{"gx", "s1", "us", "", "", "", int64(7)},
	}}}}
	c = &CH{conn: cut}
	kept := []SliceRow{{Group: "g1", Stream: "s1", Country: "us"}}
	var tot SliceTotals
	set := sliceSet{rows: kept, index: map[sliceKey]int{{"g1", "s1", "us", "", ""}: 0}, truncated: true, tot: &tot}
	err = c.addSliceCost(context.Background(), "q", nil, &set)
	if err != nil || len(set.rows) != 1 || set.rows[0].Cost[""] != 5 {
		t.Fatalf("a cut report must not grow: err=%v rows=%+v", err, set.rows)
	}
	if tot.Cost[""] != 12 {
		t.Fatalf("the total cost counts the dropped cell too: %+v", tot.Cost)
	}

	plain := &fakeConn{results: []*fakeRows{{data: [][]any{{"g1", "s1", "us", "", "", uint64(1), uint64(1), uint64(1), uint64(0)}}}, {}}}
	c = &CH{conn: plain}
	rows, _, err = c.SliceMeta(context.Background(), from, to, nil)
	if err != nil || len(plain.queries) != 2 || strings.Contains(strings.Join(plain.queries, "\n"), "cost_cents") || rows[0].Cost != nil {
		t.Fatalf("without the cost wave: err=%v queries=%q rows=%+v", err, plain.queries, rows)
	}
}
