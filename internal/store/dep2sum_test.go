package store

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestDep2SumField checks the second deposit column: a conversions-only Float64 of the deposit wave, added by the
// same ALTER ... IF NOT EXISTS as the first one, and written down in the existing migration file.
func TestDep2SumField(t *testing.T) {
	f, ok := fieldByName("dep2sum")
	if !ok || f.Dim || f.Events != "" || f.Conv != "dep2sum" || f.Type != "Float64" || f.ConvWave != waveDep || f.Default != "0" {
		t.Fatalf("field %+v ok=%v", f, ok)
	}
	if !strings.Contains(alterSQL("conversions", waveDep), "ADD COLUMN IF NOT EXISTS dep2sum Float64 DEFAULT 0") {
		t.Fatalf("alter %q", alterSQL("conversions", waveDep))
	}
	if alterSQL("events", waveDep) != "" {
		t.Fatal("events must not gain dep2sum")
	}
	raw, err := os.ReadFile("../../migrations/clickhouse/012_depsum.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "ADD COLUMN IF NOT EXISTS dep2sum Float64") || !strings.Contains(text, "ALTER TABLE emtds.conversions") {
		t.Fatalf("migration %s", text)
	}
}

// TestWriteConversionDep2Sum checks the insert: the postback deposit is written to depsum,
// a passed Dep2Sum is not stored, a first row does not write 0 into dep2sum, and a row that
// already has dep2sum keeps that number. A closed wave names neither column.
func TestWriteConversionDep2Sum(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	c, conn := newBatchCH()
	c.depsumConv = true
	if err := c.writeConversion(context.Background(), clickSnap{gname: "g"}, PostbackIn{CID: "c1", Status: "sale", Profit: 3, DepSum: 2, Dep2Sum: 11.5, At: at}, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conn.queries[0], ", depsum)") || strings.Contains(conn.queries[0], "dep2sum") {
		t.Fatalf("statement %s", conn.queries[0])
	}
	row := conn.batch.rows[0]
	if row[len(row)-1] != any(2.0) || slices.Contains(row, any(11.5)) {
		t.Fatalf("depsum cell %v, want only the deposit 2", row[len(row)-1])
	}

	c, conn = newBatchCH()
	c.depsumConv = true
	conn.keepDep2, conn.keepDep2OK = 4.25, true
	if err := c.writeConversion(context.Background(), clickSnap{gname: "g"}, PostbackIn{CID: "c2", Status: "sale", DepSum: 9, At: at}, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conn.queries[0], ", depsum, dep2sum)") {
		t.Fatalf("statement %s", conn.queries[0])
	}
	row = conn.batch.rows[0]
	n := len(row)
	if row[n-2] != any(9.0) || row[n-1] != any(4.25) {
		t.Fatalf("cells %v %v, want deposit 9 and the stored dep2sum 4.25", row[n-2], row[n-1])
	}

	c, conn = newBatchCH()
	if err := c.writeConversion(context.Background(), clickSnap{gname: "g"}, PostbackIn{CID: "c3", Status: "sale", DepSum: 2, Dep2Sum: 5, At: at}, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conn.queries[0], "dep2sum") || strings.Contains(conn.queries[0], "depsum") || slices.Contains(conn.batch.rows[0], any(5.0)) {
		t.Fatalf("a closed wave must not write the deposit columns: %s %v", conn.queries[0], conn.batch.rows[0])
	}
}

// TestDep2SumFactsSQL checks that the per-offer, per-network and per-campaign status facts sum the second deposit
// beside the first, and use a typed zero without naming the column while the wave is closed.
func TestDep2SumFactsSQL(t *testing.T) {
	on := groupStatusSQL("", true)
	if !strings.Contains(on, "sum(c.depsum), sum(c.dep2sum)") {
		t.Fatalf("open campaign facts: %s", on)
	}
	off := groupStatusSQL("", false)
	if strings.Contains(off, "dep2sum") || !strings.Contains(off, "toFloat64(0), toFloat64(0)") {
		t.Fatalf("closed campaign facts: %s", off)
	}
}

// TestFoldStatusRowsKeepDeposits keeps the two deposits apart in the StatusFact the interface reads: they are
// separate JSON keys and the second is not part of the first or of the payout.
func TestFoldStatusRowsKeepDeposits(t *testing.T) {
	raw, err := json.Marshal(StatusFact{Status: "sale", N: 2, Profit: 10, DepSum: 3, Dep2Sum: 4})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["profit"] != 10.0 || got["depsum"] != 3.0 || got["dep2sum"] != 4.0 {
		t.Fatalf("keys %v", got)
	}
}
