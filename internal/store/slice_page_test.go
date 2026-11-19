package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// pageCells is a report set with ties on hits and on cost, used by the page tests.
func pageCells() []SliceRow {
	return []SliceRow{
		{Group: "b", Stream: "s", Country: "us", Hits: 10, Conv: 1, UniqFirst: 7, Unique: 9, Profit: 5},
		{Group: "a", Stream: "s", Country: "us", Hits: 10, Conv: 5, UniqFirst: 8, Unique: 8, Cost: map[string]int64{"EUR": 300}},
		{Group: "c", Stream: "s", Country: "de", Hits: 3, Conv: 3, UniqFirst: 3, Unique: 3, Cost: map[string]int64{"USD": 900}},
		{Group: "a", Stream: "t", Country: "pl", Hits: 0, Conv: 2, Profit: 40, Offer: "o2"},
		{Group: "A", Stream: "s", Country: "fr", Hits: 1, Offer: "o1", Cost: map[string]int64{"USD": 100}},
	}
}

// groups lists the campaign and stream of every row, in order.
func groups(rows []SliceRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Group + "/" + r.Stream
	}
	return out
}

// TestPageSliceDefaultOrderAndBounds checks the default order is the most visits first with the cell
// as the tie-break, the window cuts the whole sorted set, and an offset past the end is an empty page.
func TestPageSliceDefaultOrderAndBounds(t *testing.T) {
	all := PageSlice(pageCells(), SliceTotals{}, false, PageQuery{Limit: 10}, nil)
	if all.Sort != "hits" || all.Dir != "desc" || all.Total != 5 {
		t.Fatalf("default page %+v", all)
	}
	if got, want := groups(all.Rows), []string{"a/s", "b/s", "c/s", "A/s", "a/t"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	second := PageSlice(pageCells(), SliceTotals{}, false, PageQuery{Offset: 2, Limit: 2}, nil)
	if got := groups(second.Rows); !reflect.DeepEqual(got, []string{"c/s", "A/s"}) || second.Total != 5 {
		t.Fatalf("second page %v total %d", got, second.Total)
	}
	past := PageSlice(pageCells(), SliceTotals{}, false, PageQuery{Offset: 50, Limit: 2}, nil)
	if past.Rows == nil || len(past.Rows) != 0 || past.Total != 5 {
		t.Fatalf("past the end %+v", past)
	}
}

// TestPageSliceSortsWholeSet checks every direction keeps the cell tie-break ascending, a ratio
// sorts by conv over hits, text sorts ignore case, and the label namer decides an offer sort.
func TestPageSliceSortsWholeSet(t *testing.T) {
	asc := PageSlice(pageCells(), SliceTotals{}, false, PageQuery{Sort: "hits", Limit: 10}, nil)
	if got := groups(asc.Rows); !reflect.DeepEqual(got, []string{"a/t", "A/s", "c/s", "a/s", "b/s"}) || asc.Dir != "asc" {
		t.Fatalf("hits asc %v", got)
	}
	cr := PageSlice(pageCells(), SliceTotals{}, false, PageQuery{Sort: "cr", Desc: true, Limit: 10}, nil)
	if got := groups(cr.Rows); !reflect.DeepEqual(got, []string{"a/t", "c/s", "a/s", "b/s", "A/s"}) {
		t.Fatalf("cr desc %v", got)
	}
	text := PageSlice(pageCells(), SliceTotals{}, false, PageQuery{Sort: "group", Limit: 10}, nil)
	if got := groups(text.Rows); !reflect.DeepEqual(got, []string{"A/s", "a/s", "a/t", "b/s", "c/s"}) {
		t.Fatalf("group asc %v", got)
	}
	names := map[string]string{"o1": "Zeta", "o2": "alpha"}
	label := func(_, id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	offer := PageSlice(pageCells(), SliceTotals{}, false, PageQuery{Sort: "offer", Desc: true, Limit: 2}, label)
	if got := groups(offer.Rows); !reflect.DeepEqual(got, []string{"A/s", "a/t"}) {
		t.Fatalf("offer by name desc %v", got)
	}
}

// TestPageSliceCostSortsOneCurrency checks the cost sort picks the currency with the largest
// total, names it, and keeps rows without cost in it last in both directions.
func TestPageSliceCostSortsOneCurrency(t *testing.T) {
	tot := SumSlice(pageCells())
	for _, desc := range []bool{true, false} {
		p := PageSlice(pageCells(), tot, false, PageQuery{Sort: "cost", Desc: desc, Limit: 10}, nil)
		if p.SortCur != "USD" {
			t.Fatalf("sort currency %q", p.SortCur)
		}
		want := []string{"c/s", "A/s", "a/s", "a/t", "b/s"}
		if !desc {
			want = []string{"A/s", "c/s", "a/s", "a/t", "b/s"}
		}
		if got := groups(p.Rows); !reflect.DeepEqual(got, want) {
			t.Errorf("desc=%v: %v, want %v", desc, got, want)
		}
	}
	if topCurrency(nil) != "" || topCurrency(map[string]int64{"USD": 5, "EUR": 5}) != "EUR" {
		t.Error("the empty code without cost, the smaller code on a tie")
	}
}

// TestSumSliceKeepsReportMath checks the totals sum counts and money, take uniques from the
// summable first-step count and add cost by currency.
func TestSumSliceKeepsReportMath(t *testing.T) {
	got := SumSlice(pageCells())
	want := SliceTotals{Hits: 24, Unique: 18, Conv: 11, Profit: 45, Cost: map[string]int64{"EUR": 300, "USD": 1000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("totals %+v, want %+v", got, want)
	}
}

// TestSliceRowsTotalsOfCutReport checks a cut visit side asks the visit totals of every group,
// sums cost and conversions of dropped cells into the totals, and that a whole report asks no extra query.
func TestSliceRowsTotalsOfCutReport(t *testing.T) {
	from, to := time.Now().Add(-time.Hour), time.Now()
	conn := &fakeConn{results: []*fakeRows{
		{data: [][]any{
			{"g1", "s", "us", "", "", "", uint64(9), uint64(6), uint64(5), uint64(1)},
			{"g2", "s", "us", "", "", "", uint64(4), uint64(4), uint64(4), uint64(0)},
			{"g3", "s", "us", "", "", "", uint64(2), uint64(2), uint64(2), uint64(0)},
		}},
		{data: [][]any{{uint64(15), uint64(11), uint64(1)}}},
		{data: [][]any{
			{"g1", "s", "us", "", "", "", "USD", int64(50)},
			{"g3", "s", "us", "", "", "", "USD", int64(25)},
		}},
		{data: [][]any{{"g9", "s", "us", "", "", "", uint64(2), 7.5, 0.0}}},
	}}
	c := &CH{conn: conn, costCols: true}
	rows, tot, cut, err := c.sliceRows(context.Background(), from, to, nil, 2, true, "")
	if err != nil || !cut || len(rows) != 3 || rows[2].Group != "g9" || rows[2].Conv != 2 {
		t.Fatalf("err=%v cut=%v rows=%+v", err, cut, rows)
	}
	if len(conn.queries) != 4 || !strings.Contains(conn.queries[0], "LIMIT 3") || strings.Contains(conn.queries[1], "GROUP BY") {
		t.Fatalf("queries %q", conn.queries)
	}
	want := SliceTotals{Hits: 15, Unique: 11, Bots: 1, Conv: 2, Profit: 7.5, Cost: map[string]int64{"USD": 75}}
	if !reflect.DeepEqual(tot, want) {
		t.Fatalf("totals %+v, want %+v", tot, want)
	}

	whole := &fakeConn{results: []*fakeRows{{data: [][]any{{"g1", "s", "us", "", "", "", uint64(3), uint64(3), uint64(2), uint64(1)}}}, {}}}
	c = &CH{conn: whole}
	_, tot, cut, err = c.sliceRows(context.Background(), from, to, nil, PageMaxRows, true, "")
	if err != nil || cut || len(whole.queries) != 2 || tot.Hits != 3 || tot.Unique != 2 || tot.Bots != 1 {
		t.Fatalf("whole report: err=%v cut=%v queries=%d totals=%+v", err, cut, len(whole.queries), tot)
	}
}

// TestSliceEventTotalsSQL checks the visit totals use the same aggregates as the grouped query, without grouping.
func TestSliceEventTotalsSQL(t *testing.T) {
	q := squash(sliceEventTotalsSQL("countIf(uniq = 1 AND step = 0)", " AND country = ?"))
	for _, want := range []string{"count() AS hits", "countIf(uniq = 1 AND step = 0) AS uniq_first", "countIf(bot != '' AND bot != '-') AS bots", "ts < ? AND country = ?"} {
		if !strings.Contains(q, want) {
			t.Errorf("missing %q: %s", want, q)
		}
	}
	if strings.Contains(q, "GROUP BY") || strings.Contains(q, "LIMIT") {
		t.Errorf("totals must cover every group: %s", q)
	}
}
