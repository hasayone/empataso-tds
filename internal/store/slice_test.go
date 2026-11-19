package store

import (
	"strings"
	"testing"
)

// TestSliceConvSQLDevice checks a device filter is named in both UNION branches.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestSliceConvSQLDevice(t *testing.T) {
	for _, c := range []*CH{{}, {clickCols: true, extConv: true}} {
		cvSQL, _, skip, err := buildWhere(c, "conversions", []Cond{{
			Field: "device", Op: "eq", Values: []string{"phone"},
		}})
		if err != nil || skip {
			t.Fatalf("where: err=%v skip=%v", err, skip)
		}
		sql := sliceConvSQL(c, cvSQL, false)
		parts := strings.Split(sql, "UNION ALL")
		if len(parts) != 2 {
			t.Fatalf("branches=%d\n%s", len(parts), sql)
		}
		conv := parts[1]
		if i := strings.Index(conv, " WHERE "); i >= 0 {
			conv = conv[:i]
		}
		if !strings.Contains(parts[0], "device") || !strings.Contains(conv, "device") {
			t.Fatalf("device missing from a branch:\n%s", sql)
		}
		if !strings.Contains(sql, "device IN (?)") {
			t.Fatalf("filter missing:\n%s", sql)
		}
	}
}
