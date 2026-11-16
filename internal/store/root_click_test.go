package store

import (
	"testing"
	"time"
)

func TestRootClickOrderAndWindow(t *testing.T) {
	if rootClickOrder(true) != "ORDER BY step, ts LIMIT 1" {
		t.Fatalf("step order %q", rootClickOrder(true))
	}
	if rootClickOrder(false) != "ORDER BY ts LIMIT 1" {
		t.Fatalf("time order %q", rootClickOrder(false))
	}
	if rootLookback != 2*time.Hour+5*time.Minute {
		t.Fatalf("window %s", rootLookback)
	}
}
