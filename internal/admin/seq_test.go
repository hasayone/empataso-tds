package admin

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

func TestSeqGrowsPastDelete(t *testing.T) {
	seq := NewIDSeq(filepath.Join(t.TempDir(), "network_seq.json"))
	seq.mu.Lock()
	defer seq.mu.Unlock()
	a, err := seq.take(1)
	if err != nil || a != 1 {
		t.Fatalf("first %d %v", a, err)
	}
	b, err := seq.take(1)
	if err != nil || b != 2 {
		t.Fatalf("second %d %v", b, err)
	}
	c, err := seq.take(numericFloor([]string{"9", "net"}))
	if err != nil || c != 10 {
		t.Fatalf("floor %d %v", c, err)
	}
	seq.note("4")
	d, err := seq.take(1)
	if err != nil || d != 11 {
		t.Fatalf("after note %d %v", d, err)
	}
}

func TestNoteAll(t *testing.T) {
	seq := NewIDSeq(filepath.Join(t.TempDir(), "offer_seq.json"))
	seq.noteAll([]string{"10", "x", "3"})
	seq.mu.Lock()
	n, err := seq.take(1)
	seq.mu.Unlock()
	if err != nil || n != 11 {
		t.Fatalf("after noteAll %d %v", n, err)
	}
	seq.noteAll([]string{"4"})
	seq.mu.Lock()
	n, err = seq.take(1)
	seq.mu.Unlock()
	if err != nil || n != 12 {
		t.Fatalf("noteAll did not keep the counter %d %v", n, err)
	}
	var none *idSeq
	none.noteAll([]string{"99"})
}

func TestBuildSpendRejects(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if _, msg := buildSpend(spendBody{GroupID: "g", Zone: "Europe/Madrid", From: "2026-10-13T00:00", To: "2026-10-14T00:00", Amount: "1.00", Currency: "USD"}, now); msg != "" {
		t.Fatal(msg)
	}
	if _, msg := buildSpend(spendBody{GroupID: "g", Zone: "Europe/Madrid", From: "2026-10-13T00:00", To: "2026-10-14T23:00", Amount: "1", Currency: "USD"}, now); msg == "" {
		t.Fatal("future end accepted")
	}
	if _, msg := buildSpend(spendBody{GroupID: "g", Zone: "Europe/Madrid", From: "2026-09-01T00:00", To: "2026-09-02T00:00", Amount: "1", Currency: "USD"}, now); msg == "" {
		t.Fatal("old start accepted")
	}
	if _, msg := buildSpend(spendBody{GroupID: "g", Zone: "Europe/Madrid", From: "2026-10-13T00:00", To: "2026-10-14T00:00", Amount: "1", Currency: "BTC"}, now); msg == "" {
		t.Fatal("currency accepted")
	}
	if _, msg := buildSpend(spendBody{GroupID: "g", Zone: "Europe/Madrid", From: "2026-10-13T00:00", To: "2026-10-14T00:00", Amount: "12,50", Currency: "USD"}, now); msg != "" {
		t.Fatal(msg)
	}
}

func TestDropSpendKeepsOtherBuckets(t *testing.T) {
	list := []config.SpendEntry{
		{ID: "a", Cents: 100},
		{ID: "b", Cents: 200, Buckets: []config.SpendBucket{{Cents: 200, Stream: "desktop"}}},
	}
	next, ok := dropSpend(list, "a")
	if !ok || len(next) != 1 || next[0].ID != "b" || next[0].Buckets[0].Cents != 200 {
		t.Fatalf("kept %+v", next)
	}
	if _, ok := dropSpend(next, "missing"); ok {
		t.Fatal("missing id removed a row")
	}
}
