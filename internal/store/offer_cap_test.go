package store

import (
	"context"
	"testing"
	"time"
)

// TestOfferConversionCounter checks that the counter starts at zero, counts per
// offer, resets with the UTC day and carries a lifetime.
func TestOfferConversionCounter(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 21, 23, 59, 0, 0, time.UTC)
	c.now = func() time.Time { return day }

	if n, err := c.OfferConversions(ctx, "o1", ""); err != nil || n != 0 {
		t.Fatalf("before any conversion: %d %v", n, err)
	}
	for i := 1; i <= 3; i++ {
		if n, err := c.NoteOfferConversion(ctx, "o1", ""); err != nil || n != int64(i) {
			t.Fatalf("conversion %d: count %d %v", i, n, err)
		}
	}
	if _, err := c.NoteOfferConversion(ctx, "o2", ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.OfferConversions(ctx, "o1", ""); n != 3 {
		t.Fatalf("o1 = %d, want 3", n)
	}
	if n, _ := c.OfferConversions(ctx, "o2", ""); n != 1 {
		t.Fatalf("o2 = %d, want 1", n)
	}
	if ttl := mr.TTL("cap:o:o1:20261121"); ttl <= 0 {
		t.Fatalf("the day counter has no lifetime: %v", ttl)
	}
	c.now = func() time.Time { return day.Add(2 * time.Minute) }
	if n, _ := c.OfferConversions(ctx, "o1", ""); n != 0 {
		t.Fatalf("a new UTC day starts at %d, want 0", n)
	}
	if n, err := c.NoteOfferConversion(ctx, "", ""); err != nil || n != 0 {
		t.Fatalf("an empty offer id counted: %d %v", n, err)
	}
}

// TestOfferCapZoneDay checks that the daily counter uses the offer zone.
// 23:30 UTC on 21 Nov 2026 is already 22 Nov in Europe/Warsaw, so the UTC key stays empty.
func TestOfferCapZoneDay(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	c.now = func() time.Time { return time.Date(2026, 11, 21, 23, 30, 0, 0, time.UTC) }
	if n, err := c.NoteOfferConversion(ctx, "o1", "Europe/Warsaw"); err != nil || n != 1 {
		t.Fatalf("warsaw note: %d %v", n, err)
	}
	if n, err := c.OfferConversions(ctx, "o1", ""); err != nil || n != 0 {
		t.Fatalf("utc day counted the warsaw sale: %d %v", n, err)
	}
	if n, err := c.OfferConversions(ctx, "o1", "Europe/Warsaw"); err != nil || n != 1 {
		t.Fatalf("warsaw day: %d %v", n, err)
	}
	if !mr.Exists("cap:o:o1:20261122") {
		t.Fatal("the warsaw day key is missing")
	}
	if mr.Exists("cap:o:o1:20261121") {
		t.Fatal("the utc day key was written")
	}
}

// TestCapLimitTotalCounter checks the all-time counter: it stays missing until it is seeded,
// then counts, and seeding it does not touch the daily counter.
func TestCapLimitTotalCounter(t *testing.T) {
	c, _ := newCounters(t)
	ctx := context.Background()
	if _, err := c.NoteOfferConversion(ctx, "o1", ""); err != nil {
		t.Fatal(err)
	}
	if _, known, err := c.NoteOfferTotal(ctx, "o1"); err != nil || known {
		t.Fatalf("an unseeded total counted: known %v err %v", known, err)
	}
	wrote, err := c.SeedOfferTotal(ctx, "o1", 5)
	if err != nil || !wrote {
		t.Fatalf("seed = %v %v", wrote, err)
	}
	if n, _ := c.OfferConversions(ctx, "o1", ""); n != 1 {
		t.Fatalf("seeding the total cleared the daily counter: %d", n)
	}
	if n, known, err := c.OfferTotal(ctx, "o1"); err != nil || !known || n != 5 {
		t.Fatalf("seeded total = %d %v %v", n, known, err)
	}
	if n, known, err := c.NoteOfferTotal(ctx, "o1"); err != nil || !known || n != 6 {
		t.Fatalf("after one more = %d %v %v", n, known, err)
	}
	if again, err := c.SeedOfferTotal(ctx, "o1", 0); err != nil || again {
		t.Fatalf("a second seed rewrote the counter: %v %v", again, err)
	}
}
