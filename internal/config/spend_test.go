package config

import (
	"testing"
	"time"
)

func TestParseCentsAndAllocate(t *testing.T) {
	cents, err := ParseCents("10.5")
	if err != nil || cents != 1050 {
		t.Fatalf("cents %d %v", cents, err)
	}
	if _, err := ParseCents("0"); err == nil {
		t.Fatal("zero accepted")
	}
	comma, err := ParseCents("12,50")
	if err != nil || comma != 1250 {
		t.Fatalf("comma cents %d %v", comma, err)
	}
	if _, err := ParseCents("1000000000000"); err == nil {
		t.Fatal("huge amount accepted")
	}
	if _, err := ParseCents("nan"); err == nil {
		t.Fatal("nan accepted")
	}
	rows := []SpendWeight{
		{Stream: "a", OfferID: "1", Hits: 1},
		{Stream: "b", OfferID: "2", Hits: 1},
	}
	parts := Allocate(1, rows, false)
	sum := parts[0].Cents + parts[1].Cents
	if sum != 1 {
		t.Fatalf("parts %v", parts)
	}
	none := Allocate(100, nil, true)
	if len(none) != 1 || !none[0].Unallocated || none[0].Cents != 100 {
		t.Fatalf("unallocated %v", none)
	}
}

func TestSpendWindow(t *testing.T) {
	a := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	b := a.Add(24 * time.Hour)
	c := b.Add(24 * time.Hour)
	if SpendOverlaps(a, b, b, c) {
		t.Fatal("adjacent windows overlap")
	}
	if !SpendOverlaps(a, c, b, c) {
		t.Fatal("nested window missed")
	}
	if SpendCovers(a, c, a, b) {
		t.Fatal("wider entry was sliced into the report")
	}
	if !SpendCovers(a, b, a, c) {
		t.Fatal("contained entry excluded")
	}
}
