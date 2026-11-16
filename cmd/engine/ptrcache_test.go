package main

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// ptrTestAddr returns a distinct IPv4 address for index i.
func ptrTestAddr(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
}

// name returns the first PTR name of ip, running the check in the caller when
// it is not cached. It is "" when the lookup found nothing or failed.
func (c *ptrCache) name(ip netip.Addr) string {
	ip = ip.Unmap()
	if ent, ok := c.cached(ip); ok {
		return ent.name
	}
	return c.lookup(ip).name
}

// countingPTR builds a ptrCache whose resolver counts calls and answers "host.test".
func countingPTR(calls *atomic.Int64) *ptrCache {
	return &ptrCache{resolve: func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return []string{"host.test"}, nil
	}}
}

// expirePTR sets the expiry of ip in both generations to at.
func expirePTR(c *ptrCache, ip netip.Addr, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range []map[netip.Addr]ptrEntry{c.cur, c.prev} {
		if ent, ok := m[ip]; ok {
			ent.expires = at
			m[ip] = ent
		}
	}
}

// TestPTRCacheTTL checks that a hit is served from the cache and an expired entry is resolved again.
func TestPTRCacheTTL(t *testing.T) {
	var calls atomic.Int64
	c := countingPTR(&calls)
	ip := ptrTestAddr(1)
	if got := c.name(ip); got != "host.test" {
		t.Fatalf("name = %q", got)
	}
	c.name(ip)
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 inside TTL", calls.Load())
	}
	expirePTR(c, ip, time.Now().Add(-time.Second))
	c.name(ip)
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 after expiry", calls.Load())
	}
}

// TestPTRCacheNegativeTTL checks that a failed lookup returns "" and is kept for the shorter negative TTL.
func TestPTRCacheNegativeTTL(t *testing.T) {
	var calls atomic.Int64
	c := &ptrCache{resolve: func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return nil, errors.New("no record")
	}}
	ip := ptrTestAddr(2)
	before := time.Now()
	if got := c.name(ip); got != "" {
		t.Fatalf("name = %q, want empty", got)
	}
	c.name(ip)
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	c.mu.Lock()
	exp := c.cur[ip].expires
	c.mu.Unlock()
	if exp.After(before.Add(ptrNegativeTTL + time.Second)) {
		t.Fatalf("negative entry lives until %v, want about %v", exp, ptrNegativeTTL)
	}
}

// TestPTRCacheBounded checks that the cache holds at most two generations and drops the oldest.
func TestPTRCacheBounded(t *testing.T) {
	var calls atomic.Int64
	c := countingPTR(&calls)
	total := ptrCacheGen*3 + 10
	for i := 0; i < total; i++ {
		c.name(ptrTestAddr(i))
	}
	c.mu.Lock()
	size := len(c.cur) + len(c.prev)
	_, oldest := c.prev[ptrTestAddr(0)]
	_, oldestCur := c.cur[ptrTestAddr(0)]
	_, newest := c.cur[ptrTestAddr(total-1)]
	c.mu.Unlock()
	if size > 2*ptrCacheGen {
		t.Fatalf("cache holds %d entries, bound is %d", size, 2*ptrCacheGen)
	}
	if oldest || oldestCur {
		t.Fatal("oldest address must be evicted")
	}
	if !newest {
		t.Fatal("newest address must be cached")
	}
}

// TestPTRCachePromotesPrevHit checks that a live entry of the previous generation survives the next rotation.
func TestPTRCachePromotesPrevHit(t *testing.T) {
	var calls atomic.Int64
	c := countingPTR(&calls)
	hot := ptrTestAddr(0)
	c.name(hot)
	for i := 1; i < ptrCacheGen+1; i++ {
		c.name(ptrTestAddr(i))
	}
	c.mu.Lock()
	_, inPrev := c.prev[hot]
	c.mu.Unlock()
	if !inPrev {
		t.Fatal("hot address must be in the previous generation after one rotation")
	}
	before := calls.Load()
	c.name(hot)
	if calls.Load() != before {
		t.Fatal("prev hit must not resolve again")
	}
	for i := ptrCacheGen + 1; i < 2*ptrCacheGen+1; i++ {
		c.name(ptrTestAddr(i))
	}
	before = calls.Load()
	c.name(hot)
	if calls.Load() != before {
		t.Fatal("promoted entry must survive the next rotation")
	}
}
