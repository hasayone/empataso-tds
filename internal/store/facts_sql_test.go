package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestGroupStatusJoinsOnlyConvertedClicks keeps the joined click side to the
// clicks that have a conversion in the window. Imported clicks each carry their
// own cid, so a year of them in one hash table broke the analytics memory limit
// and the facts answer failed: the campaign table and the dashboard cards then
// showed zeros. The placeholders must match the arguments in their order.
func TestGroupStatusJoinsOnlyConvertedClicks(t *testing.T) {
	q := groupStatusSQL(" AND country = ?", false)
	sub := q[strings.Index(q, "INNER JOIN"):strings.Index(q, ") AS e ON")]
	if !strings.Contains(sub, "AND country = ?\n\t\t\t\tAND cid IN (SELECT cid FROM conversions WHERE ts >= ? AND ts < ?)") {
		t.Fatalf("click side is not limited to converted cids:\n%s", q)
	}
	conn := &fakeConn{results: []*fakeRows{{data: [][]any{{"alloffers", "lead", uint64(2), 1.5, 0.0, 0.0}}}}}
	c := readyClient()
	c.conn = conn
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(1, 0, 0)
	got, err := c.groupStatus(context.Background(), from, to, " AND country = ?", []any{"DE"})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(conn.queries[0], "?"); n != len(conn.args[0]) {
		t.Fatalf("%d placeholders, %d args", n, len(conn.args[0]))
	}
	want := []any{from, to, "DE", from, to, from, to}
	if !reflect.DeepEqual(conn.args[0], want) {
		t.Fatalf("args %#v, want %#v", conn.args[0], want)
	}
	if len(got) != 1 || got[0].GroupID != "alloffers" || got[0].N != 2 || got[0].Profit != 1.5 {
		t.Fatalf("rows %+v", got)
	}
}

// TestStatusFactsSQLAlias keeps the folded status from being named status.
// ClickHouse substitutes a SELECT alias for the column of the same name, so
// AS status makes WHERE status and the voided subquery read the aggregate.
// The query is then rejected and the catalog facts answer fails, which the
// panel shows as zeros for clicks, conversions and revenue together.
func TestStatusFactsSQLAlias(t *testing.T) {
	for _, dep := range []bool{false, true} {
		q := statusFactsSQL("", dep)
		if strings.Contains(q, "AS status") {
			t.Fatalf("dep=%v: alias status shadows the column:\n%s", dep, q)
		}
		if !strings.Contains(q, "AS folded") || !strings.Contains(q, "GROUP BY offer_id, network_id, folded") {
			t.Fatalf("dep=%v: folded status is not grouped:\n%s", dep, q)
		}
		if !strings.Contains(q, "status = 'lead'") || !strings.Contains(q, "startsWith(status, 'sale:')") {
			t.Fatalf("dep=%v: the column status is no longer filtered:\n%s", dep, q)
		}
	}
	filtered := statusFactsSQL(" AND country = ?", true)
	if !strings.Contains(filtered, "cid IN (SELECT cid FROM events WHERE ts >= ? AND ts < ? AND cid != '' AND country = ?)") {
		t.Fatalf("filter: %s", filtered)
	}
	if strings.Contains(statusFactsSQL("", false), "sum(depsum)") || !strings.Contains(statusFactsSQL("", true), "sum(depsum)") {
		t.Fatal("deposit columns follow the wave")
	}
}
