package store

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func TestStickyRoundTrip(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")

	if got, err := c.StickyGet(ctx, "g1", ip); got != "" || err != nil {
		t.Fatalf("empty store: %q %v", got, err)
	}
	if err := c.StickySet(ctx, "g1", ip, "stream a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.StickyGet(ctx, "g1", ip); got != "stream a" {
		t.Errorf("get = %q", got)
	}
	if got, _ := c.StickyGet(ctx, "g2", ip); got != "" {
		t.Errorf("another group sees %q", got)
	}
	if got, _ := c.StickyGet(ctx, "g1", netip.MustParseAddr("5.6.7.8")); got != "" {
		t.Errorf("another ip sees %q", got)
	}
	if ttl := mr.TTL("st:g1:1.2.3.4"); ttl != time.Hour {
		t.Errorf("ttl = %v, want 1h", ttl)
	}
	mr.FastForward(2 * time.Hour)
	if got, _ := c.StickyGet(ctx, "g1", ip); got != "" {
		t.Errorf("after the window: %q", got)
	}
}

func TestStickyDefaultsTTLAndFailsOpen(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")
	if err := c.StickySet(ctx, "g1", ip, "a", 0); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL("st:g1:1.2.3.4"); ttl != 24*time.Hour {
		t.Errorf("default ttl = %v, want 24h", ttl)
	}
	mr.Close()
	if got, err := c.StickyGet(ctx, "g1", ip); got != "" || err == nil {
		t.Errorf("dead Redis: %q %v, want an empty name and an error", got, err)
	}
}
