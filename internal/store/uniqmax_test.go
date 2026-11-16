package store

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

// TestParseUniqMax covers the variable: empty and 0 are the default, a number is
// seconds, anything else is the default with an error.
func TestParseUniqMax(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultUniqMax, false},
		{"  ", DefaultUniqMax, false},
		{"0", DefaultUniqMax, false},
		{"3600", time.Hour, false},
		{" 7200 ", 2 * time.Hour, false},
		{"-5", DefaultUniqMax, true},
		{"abc", DefaultUniqMax, true},
		{"1.5", DefaultUniqMax, true},
		{"99999999999999", maxUniqSeconds * time.Second, false},
	}
	for _, tc := range cases {
		got, err := ParseUniqMax(tc.raw)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("ParseUniqMax(%q) = %v, %v; want %v, err=%v", tc.raw, got, err, tc.want, tc.wantErr)
		}
	}
}

// TestUniqMaxFromEnv reads the variable, with the KUZTDS_ fallback of config.Env.
func TestUniqMaxFromEnv(t *testing.T) {
	t.Setenv(UniqMaxEnv, "")
	t.Setenv("KUZTDS_UNIQ_MAX_SECONDS", "")
	if got, err := UniqMaxFromEnv(); got != DefaultUniqMax || err != nil {
		t.Fatalf("unset: %v %v", got, err)
	}
	t.Setenv(UniqMaxEnv, "600")
	if got, err := UniqMaxFromEnv(); got != 10*time.Minute || err != nil {
		t.Fatalf("600: %v %v", got, err)
	}
	t.Setenv(UniqMaxEnv, "junk")
	if got, err := UniqMaxFromEnv(); got != DefaultUniqMax || err == nil {
		t.Fatalf("junk: %v %v", got, err)
	}
}

// TestCapWindow checks min(window, ceiling), the day for a missing window and no ceiling for max <= 0.
func TestCapWindow(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct{ ttl, max, want time.Duration }{
		{30 * 24 * time.Hour, day, day},
		{time.Hour, day, time.Hour},
		{0, 2 * day, day},
		{0, time.Hour, time.Hour},
		{-time.Second, day, day},
		{7 * day, 0, 7 * day},
	}
	for _, tc := range cases {
		if got := capWindow(tc.ttl, tc.max); got != tc.want {
			t.Errorf("capWindow(%v, %v) = %v, want %v", tc.ttl, tc.max, got, tc.want)
		}
	}
}

// TestEffectiveUniqWindow is the window the admin shows beside the campaign field.
func TestEffectiveUniqWindow(t *testing.T) {
	if got := EffectiveUniqWindow(2592000, DefaultUniqMax); got != 86400 {
		t.Errorf("30 days under a day ceiling = %d", got)
	}
	if got := EffectiveUniqWindow(600, DefaultUniqMax); got != 600 {
		t.Errorf("a short window must stay: %d", got)
	}
	if got := EffectiveUniqWindow(0, 3*DefaultUniqMax); got != 86400 {
		t.Errorf("no window is a day: %d", got)
	}
}

// TestUniqueTTLUnderCeiling is the stop-test: a campaign window above the
// ceiling gives a key that lives for the ceiling, on both uniqueness prefixes.
func TestUniqueTTLUnderCeiling(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")

	if got := c.UniqMax(); got != DefaultUniqMax {
		t.Fatalf("default ceiling = %v", got)
	}
	month := 30 * 24 * time.Hour
	if ok, _ := c.Unique(ctx, "g1", ip, month); !ok {
		t.Fatal("first visit must be unique")
	}
	if ok, _ := c.UniqueOffer(ctx, "g1", ip, month); !ok {
		t.Fatal("first offer visit must be unique")
	}
	if ttl := mr.TTL("uniq:g1:1.2.3.4"); ttl != DefaultUniqMax {
		t.Errorf("uniq: ttl = %v, want the ceiling", ttl)
	}
	if ttl := mr.TTL("uniqo:g1:1.2.3.4"); ttl != DefaultUniqMax {
		t.Errorf("uniqo: ttl = %v, want the ceiling", ttl)
	}

	// A window below the ceiling is kept; an empty window is a day.
	c.Unique(ctx, "short", ip, time.Minute)
	if ttl := mr.TTL("uniq:short:1.2.3.4"); ttl != time.Minute {
		t.Errorf("short window ttl = %v", ttl)
	}
	c.Unique(ctx, "none", ip, 0)
	if ttl := mr.TTL("uniq:none:1.2.3.4"); ttl != 24*time.Hour {
		t.Errorf("empty window ttl = %v", ttl)
	}
}

// TestSetUniqMax moves the ceiling and resets it with a non-positive value.
func TestSetUniqMax(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")

	c.SetUniqMax(time.Hour)
	c.Unique(ctx, "g1", ip, 48*time.Hour)
	if ttl := mr.TTL("uniq:g1:1.2.3.4"); ttl != time.Hour {
		t.Errorf("ttl = %v, want 1h", ttl)
	}
	c.SetUniqMax(0)
	if c.UniqMax() != DefaultUniqMax {
		t.Errorf("0 must restore the default, got %v", c.UniqMax())
	}
}

// TestStickySetTTLUnderCeiling checks the sticky key follows the same ceiling and still returns its token.
func TestStickySetTTLUnderCeiling(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("5.6.7.8")

	if err := c.StickySet(ctx, "g1", ip, "tok", 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL(stickyKey("g1", ip)); ttl != DefaultUniqMax {
		t.Errorf("sticky ttl = %v, want the ceiling", ttl)
	}
	if got, _ := c.StickyGet(ctx, "g1", ip); got != "tok" {
		t.Errorf("token = %q", got)
	}
	c.SetUniqMax(time.Minute)
	if err := c.StickySet(ctx, "g2", ip, "t2", 0); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL(stickyKey("g2", ip)); ttl != time.Minute {
		t.Errorf("sticky ttl without window = %v, want 1m", ttl)
	}
}
