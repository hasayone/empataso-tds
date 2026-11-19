package store

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSliceDaySQL checks the day item sits in the select list and the grouping of all three
// report queries only when the report is cut by day, and that the zone is a placeholder.
// t is the test handle. It does not touch ClickHouse.
func TestSliceDaySQL(t *testing.T) {
	const item = "toString(toDate(ts, ?)) AS day_key"
	queries := map[string]func(day bool) string{
		"visits": func(day bool) string { return squash(sliceEventsSQL(true, "uniq()", "uniqIf()", "", SliceLimit, day)) },
		"cost":   func(day bool) string { return squash(sliceCostSQL(true, "", day)) },
		"conv":   func(day bool) string { return squash(sliceConvSQL(&CH{clickCols: true}, "", day)) },
	}
	for name, build := range queries {
		on, off := build(true), build(false)
		if !strings.Contains(on, item) || !strings.Contains(on, ", day_key") {
			t.Errorf("%s: day missing:\n%s", name, on)
		}
		if strings.Contains(off, "toDate(ts") || strings.HasSuffix(off, ", day_key") || strings.Contains(off, ", day_key,") {
			t.Errorf("%s: day grouped while off:\n%s", name, off)
		}
		if !strings.Contains(off, "'' AS day_key") {
			t.Errorf("%s: off must select an empty day:\n%s", name, off)
		}
	}
}

// TestSliceAllDaysCutsByDay checks a day report passes the zone first in every query, reads the
// day into the rows (conversions join the visit row of the same day), and takes the totals of the
// whole period even though the visit groups were not cut. Without a zone the answer has no day
// and asks no totals query.
func TestSliceAllDaysCutsByDay(t *testing.T) {
	from, to := time.Now().Add(-48*time.Hour), time.Now()
	conn := &fakeConn{results: []*fakeRows{
		{data: [][]any{
			{"g1", "s", "us", "", "", "2026-11-18", uint64(5), uint64(5), uint64(4), uint64(0)},
			{"g1", "s", "us", "", "", "2026-11-19", uint64(3), uint64(3), uint64(3), uint64(0)},
		}},
		{data: [][]any{{uint64(8), uint64(5), uint64(0)}}},
		{data: [][]any{{"g1", "s", "us", "", "", "2026-11-19", uint64(1), 2.5, 0.0}}},
	}}
	c := &CH{conn: conn}
	rows, tot, cut, err := c.SliceAllDays(context.Background(), from, to, nil, "Europe/Berlin")
	if err != nil || cut || len(rows) != 2 {
		t.Fatalf("err=%v cut=%v rows=%+v", err, cut, rows)
	}
	if rows[0].Day != "2026-11-18" || rows[1].Day != "2026-11-19" || rows[1].Conv != 1 || rows[0].Conv != 0 {
		t.Fatalf("day rows %+v", rows)
	}
	if tot.Hits != 8 || tot.Unique != 5 || tot.Conv != 1 {
		t.Fatalf("totals %+v: first-step uniques are of the whole period, not summed over days", tot)
	}
	if len(conn.queries) != 3 {
		t.Fatalf("queries %q", conn.queries)
	}
	for _, i := range []int{0, 2} {
		if got := conn.args[i]; len(got) < 1 || got[0] != "Europe/Berlin" {
			t.Errorf("query %d: zone must come first: %v", i, got)
		}
	}
	if got := conn.args[1]; len(got) != 2 {
		t.Errorf("the totals query has no zone: %v", got)
	}

	plain := &fakeConn{results: []*fakeRows{{data: [][]any{{"g1", "s", "us", "", "", "", uint64(3), uint64(3), uint64(2), uint64(1)}}}, {}}}
	c = &CH{conn: plain}
	rows, _, _, err = c.SliceAllDays(context.Background(), from, to, nil, "")
	if err != nil || len(plain.queries) != 2 || rows[0].Day != "" || strings.Contains(plain.queries[0], "toDate") {
		t.Fatalf("no zone: err=%v queries=%d rows=%+v", err, len(plain.queries), rows)
	}
}

// TestPageSliceSortsByDay checks the day sort orders rows by the day text, both ways, and that
// rows of one day fall back to the cell identity.
func TestPageSliceSortsByDay(t *testing.T) {
	rows := []SliceRow{
		{Group: "b", Day: "2026-11-19"}, {Group: "a", Day: "2026-11-19"}, {Group: "a", Day: "2026-11-17"},
	}
	page := PageSlice(rows, SliceTotals{}, false, PageQuery{Sort: "day", Desc: true, Limit: 10}, nil)
	var got []string
	for _, r := range page.Rows {
		got = append(got, r.Day+" "+r.Group)
	}
	if want := []string{"2026-11-19 a", "2026-11-19 b", "2026-11-17 a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("desc %v, want %v", got, want)
	}
	if !slices.Contains(SliceSortKeys, "day") {
		t.Fatalf("day is not a sort key: %v", SliceSortKeys)
	}
}
