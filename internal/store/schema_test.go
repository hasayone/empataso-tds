package store

import "testing"

// TestReadyFromColumns checks wave readiness from a column set.
// t is the test handle. A full set marks every wave that has columns.
// Dropping one conversions ext column clears only that wave.
// It does not touch ClickHouse and does not fail the process.
func TestReadyFromColumns(t *testing.T) {
	have := map[string]map[string]bool{
		"events":      {},
		"conversions": {},
	}
	for _, table := range []string{"events", "conversions"} {
		for _, w := range []wave{waveBase, waveNet, waveClick, waveCost, waveExt} {
			for _, col := range columnsOf(table, w) {
				have[table][col] = true
			}
		}
	}
	got := readyFromColumns(have)
	for _, table := range []string{"events", "conversions"} {
		for _, w := range []wave{waveBase, waveNet, waveClick, waveCost, waveExt} {
			want := w == waveBase || len(columnsOf(table, w)) > 0
			if got[table][w] != want {
				t.Fatalf("full %s %s ready=%v want %v", table, w, got[table][w], want)
			}
		}
	}
	missing := columnsOf("conversions", waveExt)[0]
	delete(have["conversions"], missing)
	got = readyFromColumns(have)
	if got["conversions"][waveExt] {
		t.Fatal("conversions ext ready without " + missing)
	}
	if !got["events"][waveExt] || !got["events"][waveNet] || !got["events"][waveClick] || !got["events"][waveCost] {
		t.Fatalf("events waves: %#v", got["events"])
	}
	if !got["conversions"][waveClick] || !got["conversions"][waveBase] || !got["events"][waveBase] {
		t.Fatalf("other waves: events %#v conversions %#v", got["events"], got["conversions"])
	}
}
