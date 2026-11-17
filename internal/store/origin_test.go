package store

import (
	"os"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

func TestOriginMigrationMatchesFields(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/clickhouse/010_origin.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := "ALTER TABLE emtds.events " + alterSQL("events", waveOrigin) + "; ALTER TABLE emtds.conversions " + alterSQL("conversions", waveOrigin) + ";"
	if normSQL(migrationSQLBody(string(raw))) != normSQL(want) {
		t.Fatalf("migration\n%s\nwant\n%s", normSQL(migrationSQLBody(string(raw))), normSQL(want))
	}
}

func TestOriginWaveColumns(t *testing.T) {
	for _, table := range []string{"events", "conversions"} {
		cols := columnsOf(table, waveOrigin)
		if len(cols) != 2 || cols[0] != "origin_group_name" || cols[1] != "origin_group_id" {
			t.Fatalf("%s origin wave = %v", table, cols)
		}
	}
}

func TestEventColumnsOriginWave(t *testing.T) {
	e := logbuf.Event{GroupID: "g2", GroupName: "G2", OriginID: "g1", OriginName: "G1"}
	for _, on := range []bool{false, true} {
		c := &CH{}
		c.originEvents.Store(on)
		cols := eventColumns(c, on)
		vals := eventValues(cols, e)
		if len(cols) != len(vals) {
			t.Fatalf("origin=%v: %d columns, %d values", on, len(cols), len(vals))
		}
		idxName, idxID := -1, -1
		for i, name := range cols {
			switch name {
			case "origin_group_name":
				idxName = i
			case "origin_group_id":
				idxID = i
			}
		}
		if on != (idxName >= 0 && idxID >= 0) {
			t.Fatalf("origin=%v: columns %v", on, cols)
		}
		if on {
			if vals[idxName] != "G1" || vals[idxID] != "g1" {
				t.Fatalf("origin values %v %v", vals[idxName], vals[idxID])
			}
		}
	}
}

func TestEventColumnsOriginFallback(t *testing.T) {
	e := logbuf.Event{GroupID: "g2", GroupName: "G2"}
	c := &CH{}
	c.originEvents.Store(true)
	cols := eventColumns(c, true)
	vals := eventValues(cols, e)
	for i, col := range cols {
		switch col {
		case "origin_group_name":
			if vals[i] != "G2" {
				t.Fatalf("name fallback %v", vals[i])
			}
		case "origin_group_id":
			if vals[i] != "g2" {
				t.Fatalf("id fallback %v", vals[i])
			}
		}
	}
}

func TestConversionUnionOriginLegacyPostbacks(t *testing.T) {
	with := conversionUnion(false, false, false, conversionLateWaves{origin: true}, false, false)
	if !strings.Contains(with, "group_name AS origin_group_name") {
		t.Fatalf("postbacks stub:\n%s", with)
	}
	if !strings.Contains(with, "origin_group_name, origin_group_id FROM conversions") {
		t.Fatalf("conversions branch:\n%s", with)
	}
	if without := conversionUnion(true, true, true, conversionLateWaves{}, false, false); strings.Contains(without, "origin_group") {
		t.Fatalf("origin leaked without flag:\n%s", without)
	}
}

func TestLogSelectOriginStubs(t *testing.T) {
	c := &CH{netCols: true, clickCols: true}
	sel := logSelect(c, nil, false)
	if !strings.Contains(sel, "'' AS origin_group_name") {
		t.Fatalf("missing origin stub: %s", sel)
	}
}
