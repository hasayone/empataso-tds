package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestBreakdownSQL checks the dimension, the filter fragment and GROUP BY.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestBreakdownSQL(t *testing.T) {
	sql := breakdownSQL("country", " AND device IN (?)")
	if !strings.Contains(sql, "country") || !strings.Contains(sql, " AND device IN (?)") || !strings.Contains(sql, "GROUP BY k") {
		t.Fatalf("sql: %s", sql)
	}
}

// TestBreakdownFilteredSkipUnready returns an empty slice when a condition
// names a wave that is not ready. t is the test handle. c has no connection;
// a query would panic. The error is nil and the slice is empty.
func TestBreakdownFilteredSkipUnready(t *testing.T) {
	c := &CH{}
	got, err := c.BreakdownFiltered(context.Background(), time.Now().Add(-time.Hour), time.Now(), "country", 10, []Cond{{
		Field: "timezone", Op: "eq", Values: []string{"UTC"},
	}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

// TestBreakdownSubIDNotReady returns ErrFieldNotReady when the click wave
// is absent. t is the test handle. c has no connection and no ready flags;
// a query would panic. The error is ErrFieldNotReady.
func TestBreakdownSubIDNotReady(t *testing.T) {
	c := &CH{}
	_, err := c.Breakdown(context.Background(), time.Now().Add(-time.Hour), time.Now(), "sub_id_1", 10)
	if !errors.Is(err, ErrFieldNotReady) {
		t.Fatalf("err: %v", err)
	}
}
