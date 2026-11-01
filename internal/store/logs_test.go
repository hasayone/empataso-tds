package store

import (
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/logbuf"
)

func TestEventColumnsMatchValues(t *testing.T) {
	e := logbuf.Event{
		Subs:      [config.MaxSubID]string{11: "s12"},
		OfferID:   "off",
		ASN:       9,
		CostCents: 7,
		XRW:       "XMLHttpRequest",
		Site:      "land",
	}
	for mask := 0; mask < 16; mask++ {
		c := &CH{
			netCols:   mask&1 != 0,
			clickCols: mask&2 != 0,
			costCols:  mask&4 != 0,
			extEvents: mask&8 != 0,
		}
		cols := eventColumns(c, false)
		vals := eventValues(cols, e)
		if len(cols) != len(vals) {
			t.Fatalf("mask %d: %d columns, %d values", mask, len(cols), len(vals))
		}
	}
	c := &CH{netCols: true, clickCols: true, costCols: true, extEvents: true}
	cols := eventColumns(c, false)
	vals := eventValues(cols, e)
	at := map[string]int{}
	for i, name := range cols {
		at[name] = i
	}
	if at["sub_id_1"] > at["offer_id"] {
		t.Fatalf("subs must precede ids: %v", cols)
	}
	if at["cost_cents"] > at["site"] {
		t.Fatalf("cost must precede ext: %v", cols)
	}
	if vals[at["sub_id_12"]] != "s12" || vals[at["offer_id"]] != "off" || vals[at["asn"]] != uint32(9) || vals[at["xrw"]] != "XMLHttpRequest" {
		t.Fatalf("values misplaced: %#v", vals)
	}
}

// TestLogSelectASNStub checks the missing-net asn placeholder scans into uint32.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestLogSelectASNStub(t *testing.T) {
	sql := logSelect(&CH{}, nil, false)
	if !strings.Contains(sql, "toUInt32(0) AS asn") {
		t.Fatalf("asn stub: %s", sql)
	}
}

func TestConversionFromExtStub(t *testing.T) {
	for _, click := range []bool{false, true} {
		sql := conversionFrom(click, true)
		if !strings.Contains(sql, "toDateTime(0) AS click_ts") {
			t.Fatalf("click=%v missing click_ts stub:\n%s", click, sql)
		}
		if !strings.Contains(sql, "toUInt32(0) AS asn") {
			t.Fatalf("click=%v missing asn stub:\n%s", click, sql)
		}
	}
}
