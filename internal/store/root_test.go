package store

import (
	"os"
	"strings"
	"testing"
)

func TestRootMigrationMatchesFields(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/clickhouse/015_root.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := "ALTER TABLE emtds.events " + alterSQL("events", waveRoot) + "; ALTER TABLE emtds.conversions " + alterSQL("conversions", waveRoot) + ";"
	if normSQL(migrationSQLBody(string(raw))) != normSQL(want) {
		t.Fatalf("migration\n%s\nwant\n%s", normSQL(migrationSQLBody(string(raw))), normSQL(want))
	}
}

func TestRootWaveColumns(t *testing.T) {
	if cols := columnsOf("events", waveRoot); len(cols) != 1 || cols[0] != "root_cid" {
		t.Fatalf("events root wave = %v", cols)
	}
	cols := columnsOf("conversions", waveRoot)
	want := []string{"root_cid", "origin_stream_name", "origin_keyword"}
	if strings.Join(cols, ",") != strings.Join(want, ",") {
		t.Fatalf("conversions root wave = %v", cols)
	}
}

func TestRootConversionUnionLegacyPostbacks(t *testing.T) {
	with := conversionUnion(false, false, false, conversionLateWaves{root: true}, false, false)
	for _, stub := range []string{"'' AS root_cid", "'' AS origin_stream_name", "'' AS origin_keyword"} {
		if !strings.Contains(with, stub) {
			t.Fatalf("postbacks stub %q missing:\n%s", stub, with)
		}
	}
	if !strings.Contains(with, "root_cid, origin_stream_name, origin_keyword FROM conversions") {
		t.Fatalf("conversions branch:\n%s", with)
	}
}

func TestRootColumnsStayClosedWithoutWave(t *testing.T) {
	postbackSQL, _ := (&CH{}).postbackColumns(false, false)
	for name, sql := range map[string]string{
		"postbacks": postbackSQL,
		"slice":     sliceConvSQL(&CH{}, ""),
	} {
		for _, col := range []string{"root_cid", "origin_stream_name", "origin_keyword"} {
			if strings.Contains(sql, col) {
				t.Fatalf("%s contains closed root column %s:\n%s", name, col, sql)
			}
		}
	}
}
