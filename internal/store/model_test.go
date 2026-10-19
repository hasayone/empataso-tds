package store

import (
	"os"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/logbuf"
)

func TestModelMigrationMatchesFields(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/clickhouse/007_device_model.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := "ALTER TABLE events " + alterSQL("events", waveModel) + " ALTER TABLE conversions " + alterSQL("conversions", waveModel)
	if normSQL(string(raw)) != normSQL(want) {
		t.Fatalf("migration\n%s\nwant\n%s", normSQL(string(raw)), normSQL(want))
	}
}

func TestModelWaveColumns(t *testing.T) {
	for _, table := range []string{"events", "conversions"} {
		cols := columnsOf(table, waveModel)
		if len(cols) != 1 || cols[0] != "device_model" {
			t.Fatalf("%s model wave = %v", table, cols)
		}
	}
	info := (&CH{modelEvents: true, readyFlags: map[string]map[wave]bool{
		"events": {waveModel: true}, "conversions": {waveModel: false},
	}}).Fields()
	if !info.Ready["events"]["model"] || info.Ready["conversions"]["model"] {
		t.Fatalf("ready flags: %+v", info.Ready)
	}
}

func TestReadyFromColumnsModel(t *testing.T) {
	have := map[string]map[string]bool{"events": {"device_model": true}, "conversions": {}}
	got := readyFromColumns(have)
	if !got["events"][waveModel] || got["conversions"][waveModel] {
		t.Fatalf("model readiness: %+v", got)
	}
}

func TestEventColumnsModelWave(t *testing.T) {
	e := logbuf.Event{DeviceModel: "SM-S918B"}
	for _, on := range []bool{false, true} {
		c := &CH{modelEvents: on}
		cols := eventColumns(c, false)
		vals := eventValues(cols, e)
		if len(cols) != len(vals) {
			t.Fatalf("model=%v: %d columns, %d values", on, len(cols), len(vals))
		}
		idx := -1
		for i, name := range cols {
			if name == "device_model" {
				idx = i
			}
		}
		if on != (idx >= 0) {
			t.Fatalf("model=%v: device_model position %d in %v", on, idx, cols)
		}
		if on && vals[idx] != "SM-S918B" {
			t.Fatalf("device_model value %v", vals[idx])
		}
	}
}

func TestLogSelectMatchesDestWithModel(t *testing.T) {
	for _, on := range []bool{false, true} {
		c := &CH{modelEvents: on, clickCols: true}
		sel := logSelect(c, nil, false)
		if strings.Contains(sel, "device_model") != on {
			t.Fatalf("model=%v: select %s", on, sel)
		}
		var r LogRow
		dest, _ := logDest(c, &r, nil, false)
		if want := strings.Count(sel, ",") + 1; len(dest) != want {
			t.Fatalf("model=%v: %d scan targets for %d columns", on, len(dest), want)
		}
	}
}

func TestConversionUnionModel(t *testing.T) {
	with := conversionUnion(false, false, true, false)
	if !strings.Contains(with, "'' AS device_model") || !strings.Contains(with, ", device_model FROM conversions") {
		t.Fatalf("model union:\n%s", with)
	}
	if without := conversionUnion(true, true, false, false); strings.Contains(without, "device_model") {
		t.Fatalf("model column leaked into the union without the wave:\n%s", without)
	}
	if conversionFrom(true, true) != conversionUnion(true, true, false, false) {
		t.Fatal("conversionFrom must not add the model wave")
	}
}

func TestConversionInsertModel(t *testing.T) {
	row := ConversionInsert{CID: "c", Status: "sale", DeviceModel: "iPhone"}
	for _, model := range []bool{false, true} {
		for _, ext := range []bool{false, true} {
			cols, n := conversionColumnsModel(true, ext, model, false)
			vals := conversionValuesModel(row, true, ext, model, false)
			if len(vals) != n || strings.Count(cols, ",")+1 != n {
				t.Fatalf("ext=%v model=%v: n=%d values=%d cols=%q", ext, model, n, len(vals), cols)
			}
			if model && (!strings.HasSuffix(cols, "device_model") || vals[n-1] != "iPhone") {
				t.Fatalf("model must be last: %q %v", cols, vals[n-1])
			}
		}
	}
}
