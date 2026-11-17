package store

import (
	"os"
	"strings"
	"testing"
)

// TestDepSumField is the conversions-only deposit column and its ALTER.
// t is the test handle. It reads the field list and the migration file.
// It does not touch ClickHouse and does not fail the process.
func TestDepSumField(t *testing.T) {
	f, ok := fieldByName("depsum")
	if !ok || f.Dim || f.Events != "" || f.Conv != "depsum" || f.Type != "Float64" || f.ConvWave != waveDep || f.Default != "0" {
		t.Fatalf("field %+v ok=%v", f, ok)
	}
	if got := alterSQL("conversions", waveDep); got != "ADD COLUMN IF NOT EXISTS depsum Float64 DEFAULT 0" {
		t.Fatalf("alter %q", got)
	}
	if alterSQL("events", waveDep) != "" {
		t.Fatal("events must not gain depsum")
	}
	raw, err := os.ReadFile("../../migrations/clickhouse/012_depsum.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "ALTER TABLE emtds.conversions") || !strings.Contains(text, "ADD COLUMN IF NOT EXISTS depsum Float64") {
		t.Fatalf("migration %s", text)
	}
	if strings.Contains(strings.ToUpper(text), "UPDATE") || strings.Contains(text, "postbacks") || strings.Contains(text, "events") {
		t.Fatalf("migration rewrites the wrong table: %s", text)
	}
	schema, err := os.ReadFile("schema.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(schema)
	if !strings.Contains(body, `schemaAlter("conversions", waveDep)`) {
		t.Fatal("ensureSchema must add depsum")
	}
	if strings.Contains(body, `schemaAlter("events", waveDep)`) {
		t.Fatal("ensureSchema must not alter events for the deposit")
	}
	conv, err := os.ReadFile("conversions.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(conv)
	fnAt := strings.Index(src, "func (c *CH) writeConversion")
	if fnAt < 0 {
		t.Fatal("writeConversion missing")
	}
	fn := src[fnAt:]
	head := fn[:strings.Index(fn, "if c.clickCols")]
	if strings.Contains(head, "depsum") {
		t.Fatalf("depsum is in the base insert list: %s", head)
	}
	if !strings.Contains(fn, "if c.depsumConv") {
		t.Fatal("deposit column is not conditional")
	}
	imp, err := os.ReadFile("import.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(imp), "depsum") {
		t.Fatal("keitaro import must not write depsum")
	}
}

// TestDepSumSQL keeps the deposit out of SQL until the wave is ready.
// t is the test handle. It builds SQL only.
// It does not touch ClickHouse and does not fail the process.
func TestDepSumSQL(t *testing.T) {
	if got := postbackSumSelect(false); got != "sum(profit), toFloat64(0), count()" || strings.Contains(got, "depsum") {
		t.Fatalf("closed totals %q", got)
	}
	if got := postbackSumSelect(true); got != "sum(profit), sum(depsum), count()" {
		t.Fatalf("open totals %q", got)
	}
	closed := conversionUnion(true, true, true, conversionLateWaves{origin: true}, true, false)
	if strings.Contains(closed, "depsum") {
		t.Fatalf("union names depsum while the wave is closed:\n%s", closed)
	}
	open := conversionUnion(false, false, false, conversionLateWaves{}, false, true)
	if !strings.Contains(open, "toFloat64(0) AS depsum") || !strings.Contains(open, ", depsum FROM conversions") {
		t.Fatalf("open union:\n%s", open)
	}
	off := sliceConvSQL(&CH{}, "")
	if strings.Contains(off, "sum(depsum)") || strings.Contains(off, ", depsum") {
		t.Fatalf("slice names the column: %s", off)
	}
	if !strings.Contains(off, "toFloat64(0) AS depsum_sum") || !strings.Contains(off, "sum(profit) AS profit_sum") {
		t.Fatalf("slice sums: %s", off)
	}
	on := sliceConvSQL(&CH{depsumConv: true}, "")
	if !strings.Contains(on, "sum(depsum) AS depsum_sum") || !strings.Contains(on, "toFloat64(0) AS depsum") {
		t.Fatalf("open slice: %s", on)
	}
	c := &CH{depsumConv: true}
	sel, extra := c.postbackColumns(false, false)
	if len(extra) != 1 || extra[0] != "depsum" || !strings.HasSuffix(sel, ", depsum") {
		t.Fatalf("journal select %q %v", sel, extra)
	}
	var row PostbackRow
	dest := c.postbackDest(&row, nil, extra)
	if len(dest) != 12 {
		t.Fatalf("scan targets %d", len(dest))
	}
	if p, ok := dest[len(dest)-1].(*float64); !ok || p != &row.DepSum {
		t.Fatal("depsum scan target is not the deposit")
	}
}
