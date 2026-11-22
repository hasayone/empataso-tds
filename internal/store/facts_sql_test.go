package store

import (
	"strings"
	"testing"
)

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
