package store

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestUniqIdentity keeps the address-only key and hides the user-agent in a hash.
func TestUniqIdentity(t *testing.T) {
	ip := netip.MustParseAddr("1.2.3.4")
	ua := "SecretAgent/1.0"
	if got := UniqIdentity(ip, ua, false); got != "1.2.3.4" {
		t.Fatalf("ip identity = %q", got)
	}
	a := UniqIdentity(ip, ua, true)
	b := UniqIdentity(ip, ua, true)
	c := UniqIdentity(ip, ua+"x", true)
	if a != b || a == "1.2.3.4" || !strings.HasPrefix(a, "1.2.3.4:") || strings.Contains(a, ua) {
		t.Fatalf("ua identity = %q", a)
	}
	if a == c {
		t.Fatal("a different agent must be a different key")
	}
	if len(a) != len("1.2.3.4:")+16 {
		t.Fatalf("hash length: %q", a)
	}
}

// TestUniqueVisitorTTLUnderCeiling keeps a long campaign window at the Redis ceiling
// for both the address key and the address-and-agent key.
func TestUniqueVisitorTTLUnderCeiling(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")
	month := 30 * 24 * time.Hour
	ua := "SecretAgent/1.0"

	if ok, _ := c.Touch(ctx, "uniq:", "g1", UniqIdentity(ip, ua, false), month); !ok {
		t.Fatal("first ip visit must be unique")
	}
	if ttl := mr.TTL("uniq:g1:1.2.3.4"); ttl != DefaultUniqMax {
		t.Errorf("ip ttl = %v, want the ceiling", ttl)
	}
	if ok, _ := c.Touch(ctx, "uniq:", "g1", UniqIdentity(ip, ua, true), month); !ok {
		t.Fatal("the agent key is a different visitor")
	}
	agentKey := "uniq:g1:" + UniqIdentity(ip, ua, true)
	if ttl := mr.TTL(agentKey); ttl != DefaultUniqMax {
		t.Errorf("agent ttl = %v, want the ceiling", ttl)
	}
	if strings.Contains(agentKey, ua) {
		t.Fatalf("redis key holds the agent: %s", agentKey)
	}
	if ok, _ := c.Touch(ctx, "uniqo:", "g1", UniqIdentity(ip, ua, true), time.Minute); !ok {
		t.Fatal("offer key must be its own key")
	}
	if ttl := mr.TTL("uniqo:g1:" + UniqIdentity(ip, ua, true)); ttl != time.Minute {
		t.Errorf("short window ttl = %v", ttl)
	}
}
