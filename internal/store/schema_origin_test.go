package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/logbuf"
)

func originColumnSet(ready bool) map[string]map[string]bool {
	have := map[string]map[string]bool{
		"events":      {},
		"conversions": {},
	}
	if ready {
		for _, table := range []string{"events", "conversions"} {
			for _, col := range columnsOf(table, waveOrigin) {
				have[table][col] = true
			}
		}
	}
	return have
}

func TestOriginRefreshProbeThrottle(t *testing.T) {
	prev := originWaveProbeInterval
	originWaveProbeInterval = 50 * time.Millisecond
	t.Cleanup(func() { originWaveProbeInterval = prev })

	calls := 0
	c := &CH{
		originColumnReader: func() (map[string]map[string]bool, error) {
			calls++
			return originColumnSet(calls >= 2), nil
		},
	}
	c.maybeRefreshOriginWave()
	c.maybeRefreshOriginWave()
	if calls != 1 {
		t.Fatalf("first burst: %d probes, want 1", calls)
	}
	if c.originEvents.Load() {
		t.Fatal("columns not ready yet")
	}
	time.Sleep(originWaveProbeInterval + 10*time.Millisecond)
	c.maybeRefreshOriginWave()
	if calls != 2 {
		t.Fatalf("after interval: %d probes, want 2", calls)
	}
	if !c.originEvents.Load() || !c.originConv.Load() {
		t.Fatal("origin wave not ready after second probe")
	}
}

func TestOriginRefreshProbeErrorKeepsFalse(t *testing.T) {
	prev := originWaveProbeInterval
	originWaveProbeInterval = 50 * time.Millisecond
	t.Cleanup(func() { originWaveProbeInterval = prev })

	calls := 0
	c := &CH{
		originColumnReader: func() (map[string]map[string]bool, error) {
			calls++
			return nil, errors.New("probe failed")
		},
	}
	c.maybeRefreshOriginWave()
	c.maybeRefreshOriginWave()
	if calls != 1 {
		t.Fatalf("errors: %d probes, want 1", calls)
	}
	if c.originEvents.Load() {
		t.Fatal("flag set on error")
	}
	time.Sleep(originWaveProbeInterval + 10*time.Millisecond)
	c.maybeRefreshOriginWave()
	if calls != 2 {
		t.Fatalf("retry after interval: %d probes, want 2", calls)
	}
}

func TestOriginReadyConcurrent(t *testing.T) {
	c := readyClient()
	c.originEvents.Store(false)
	c.originConv.Store(false)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = c.ready("events", waveOrigin)
				_ = c.ready("conversions", waveOrigin)
				on := c.originEvents.Load()
				cols := eventColumns(c, on)
				vals := eventValues(cols, logbuf.Event{GroupID: "g", GroupName: "G"})
				if len(cols) != len(vals) {
					t.Errorf("columns %d values %d", len(cols), len(vals))
				}
				sel := logSelect(c, nil, on)
				var r LogRow
				dest, _ := logDest(c, &r, nil, on)
				if want := stringsCountCommas(sel) + 1; len(dest) != want {
					t.Errorf("select/dest mismatch: %d vs %d", len(dest), want)
				}
			}
		}()
	}
	go func() {
		for j := 0; j < 100; j++ {
			on := j%2 == 0
			c.originEvents.Store(on)
			c.originConv.Store(on)
		}
		c.originEvents.Store(true)
		c.originConv.Store(true)
	}()
	wg.Wait()
}

func TestLogSelectDestOriginSnapshot(t *testing.T) {
	for _, on := range []bool{false, true} {
		c := &CH{netCols: true, clickCols: true}
		sel := logSelect(c, nil, on)
		var r LogRow
		dest, _ := logDest(c, &r, nil, on)
		want := stringsCountCommas(sel) + 1
		if len(dest) != want {
			t.Fatalf("origin=%v: %d scan targets for %d columns", on, len(dest), want)
		}
	}
}

func TestConversionInsertOriginSnapshot(t *testing.T) {
	row := ConversionInsert{GroupID: "g2", GroupName: "G2", OriginGroupID: "g1", OriginGroupName: "G1"}
	for _, on := range []bool{false, true} {
		cols, n := conversionColumnsModel(false, false, false, on)
		vals := conversionValuesModel(row, false, false, false, on)
		if len(vals) != n || len(vals) != stringsCountCommas(cols)+1 {
			t.Fatalf("origin=%v: cols=%q n=%d vals=%d", on, cols, n, len(vals))
		}
	}
}

func stringsCountCommas(s string) int {
	n := 0
	for _, ch := range s {
		if ch == ',' {
			n++
		}
	}
	return n
}
