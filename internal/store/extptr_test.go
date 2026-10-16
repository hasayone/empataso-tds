package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestLogExtPtrDistinct checks events ext scan targets are distinct fields.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestLogExtPtrDistinct(t *testing.T) {
	var row LogRow
	assertDistinctRowPtrs(t, columnsOf("events", waveExt), &row, func(col string) any {
		return logExtPtr(&row, col)
	})
}

// TestPostbackExtPtrDistinct checks conversions ext scan targets are distinct fields.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestPostbackExtPtrDistinct(t *testing.T) {
	var row PostbackRow
	assertDistinctRowPtrs(t, columnsOf("conversions", waveExt), &row, func(col string) any {
		return postbackExtPtr(&row, col)
	})
}

// TestClickExtPtrDistinct checks click snapshot ext scan targets are distinct fields.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestClickExtPtrDistinct(t *testing.T) {
	var snap clickSnap
	assertDistinctRowPtrs(t, columnsOf("events", waveExt), &snap, func(col string) any {
		return clickExtPtr(&snap, col)
	})
}

// TestPostbackClickTSJSON checks click_ts is omitted only when the time is zero.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestPostbackClickTSJSON(t *testing.T) {
	zero, err := json.Marshal(PostbackRow{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(zero), "click_ts") {
		t.Fatalf("zero row has click_ts: %s", zero)
	}
	set, err := json.Marshal(PostbackRow{ClickTS: time.Unix(1_700_000_000, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(set), "click_ts") {
		t.Fatalf("nonzero row missing click_ts: %s", set)
	}
}

// assertDistinctRowPtrs fails when a column pointer is shared, outside row, or the dummy.
// t is the test handle. cols are the physical names. row is the struct pointer.
// ptrOf returns the Scan target of one column. It does not return an error.
func assertDistinctRowPtrs(t *testing.T, cols []string, row any, ptrOf func(string) any) {
	t.Helper()
	rv := reflect.ValueOf(row)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		t.Fatalf("row is %T", row)
	}
	start := rv.Pointer()
	end := start + rv.Type().Elem().Size()
	seen := map[uintptr]string{}
	for _, col := range cols {
		p := ptrOf(col)
		pv := reflect.ValueOf(p)
		if pv.Kind() != reflect.Ptr || pv.IsNil() {
			t.Fatalf("%s: %#v", col, p)
		}
		addr := pv.Pointer()
		if addr < start || addr >= end {
			t.Fatalf("%s points at a dummy", col)
		}
		if prev, ok := seen[addr]; ok {
			t.Fatalf("%s and %s share a pointer", prev, col)
		}
		seen[addr] = col
	}
	unknown := reflect.ValueOf(ptrOf("not-a-column"))
	if unknown.Kind() != reflect.Ptr || unknown.IsNil() {
		t.Fatal("unknown column has no pointer")
	}
	if addr := unknown.Pointer(); addr >= start && addr < end {
		t.Fatal("unknown column points into the row")
	}
}
