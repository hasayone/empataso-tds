package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/emtds/internal/detect"
)

const (
	// ptrTimeout bounds one whole check: the reverse lookup and the forward lookup share it.
	ptrTimeout = 2 * time.Second
	// ptrPositiveTTL is how long a found PTR name is reused.
	ptrPositiveTTL = 10 * time.Minute
	// ptrNegativeTTL is how long a final "no name" answer is reused.
	ptrNegativeTTL = time.Minute
	// ptrFailedTTL is how long a timeout or a temporary DNS error is reused.
	// It is short so a real crawler is checked again soon, and long enough that
	// one broken resolver is not asked for every request.
	ptrFailedTTL = 15 * time.Second
	// ptrFailedLimit is the number of consecutive failed checks of one address
	// after which the failure counts as a final "not confirmed" answer.
	ptrFailedLimit = 3
	// ptrCacheGen is the number of addresses one generation holds.
	// The cache keeps at most two generations, so twice this many entries.
	ptrCacheGen = 16384
	// ptrMaxInflight is the number of background checks that a search-engine
	// user agent may run at once.
	ptrMaxInflight = 16
	// ptrMaxInflightPTR is the number of background checks that the ch_ptr
	// toggle may run at once for visitors with an ordinary user agent.
	ptrMaxInflightPTR = 8
)

// ptrClass tells which semaphore a background check takes.
type ptrClass int

const (
	// ptrClassCrawler is a check started by a search-engine user agent.
	ptrClassCrawler ptrClass = iota
	// ptrClassPTR is a check started by ch_ptr for an ordinary user agent.
	ptrClassPTR
	ptrClassCount
)

// ptrSlots is the semaphore capacity of each ptrClass.
var ptrSlots = [ptrClassCount]int{ptrMaxInflight, ptrMaxInflightPTR}

var ptrResolver = &net.Resolver{PreferGo: true}

// ptrEntry is one cached check of an address.
// name is the first PTR name without the trailing dot, or "" when none was found.
// crawler is the search-engine label of the first PTR name that belongs to a
// crawler domain, or "". confirmed is true when the forward lookup of that name
// returned the address itself. failed is true when a timeout or a temporary
// DNS error left the answer unknown: such an entry says nothing about the
// address and callers treat it as a missing one. failures is the number of
// consecutive failed checks of the address up to this entry; a check that got
// an answer resets it, and the ptrFailedLimit-th failure becomes a final
// answer with failed false. expires is when the entry stops being used.
type ptrEntry struct {
	name      string
	crawler   string
	confirmed bool
	failed    bool
	failures  int
	expires   time.Time
}

// ptrCache verifies crawler addresses by forward-confirmed reverse DNS in the
// background and remembers the answers by address in two generations.
// A hit in cur or prev that has not expired is returned, and a prev hit moves
// into cur. When cur is full it becomes prev and the old prev is dropped, so
// insertion is O(1) and the size is bounded by two generations.
//
// resolve is the reverse lookup for an address string and forward the forward
// lookup of a host name (nil uses ptrResolver). Both must be safe for
// concurrent use and are called without the mutex held. timeout overrides
// ptrTimeout when positive. mu guards cur, prev, sem and inflight. sem holds
// one semaphore per ptrClass and limits the running background checks of that
// class; inflight holds the addresses being checked, so one address has at
// most one check at a time.
type ptrCache struct {
	resolve  func(ctx context.Context, addr string) ([]string, error)
	forward  func(ctx context.Context, host string) ([]netip.Addr, error)
	timeout  time.Duration
	mu       sync.Mutex
	cur      map[netip.Addr]ptrEntry
	prev     map[netip.Addr]ptrEntry
	sem      [ptrClassCount]chan struct{}
	inflight map[netip.Addr]struct{}
}

// ptrNames is the process-wide PTR cache used by detectBot.
var ptrNames = &ptrCache{resolve: ptrResolver.LookupAddr}

// peek returns the cached check of ip and never waits for DNS.
// ok is true when a live entry exists; it may be a failed one. Otherwise it
// starts a background check when the semaphore of class has a free slot and no
// check of ip is running yet, and returns ok false: the caller treats the
// address as not yet verified. When all slots of the class are busy nothing is
// started and nothing is cached. The cache, the in-flight set and the slot are
// handled under one hold of the mutex.
func (c *ptrCache) peek(ip netip.Addr, class ptrClass) (ent ptrEntry, ok bool) {
	ip = ip.Unmap()
	c.mu.Lock()
	if ent, ok = c.cachedLocked(ip, time.Now()); ok {
		c.mu.Unlock()
		return ent, true
	}
	if _, busy := c.inflight[ip]; busy {
		c.mu.Unlock()
		return ptrEntry{}, false
	}
	if c.sem[class] == nil {
		c.sem[class] = make(chan struct{}, ptrSlots[class])
	}
	select {
	case c.sem[class] <- struct{}{}:
	default:
		c.mu.Unlock()
		return ptrEntry{}, false
	}
	if c.inflight == nil {
		c.inflight = make(map[netip.Addr]struct{})
	}
	c.inflight[ip] = struct{}{}
	c.mu.Unlock()
	go c.run(ip, class)
	return ptrEntry{}, false
}

// run checks ip, stores the answer and frees the slot taken by peek.
func (c *ptrCache) run(ip netip.Addr, class ptrClass) {
	defer func() {
		c.mu.Lock()
		delete(c.inflight, ip)
		c.mu.Unlock()
		<-c.sem[class]
	}()
	c.lookup(ip)
}

// cached returns the entry stored for ip while it has not expired.
// ok is false for an unknown or expired address.
func (c *ptrCache) cached(ip netip.Addr) (ent ptrEntry, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedLocked(ip, time.Now())
}

// cachedLocked is cached for a caller that holds c.mu; now is the current time.
func (c *ptrCache) cachedLocked(ip netip.Addr, now time.Time) (ent ptrEntry, ok bool) {
	ent, ok = c.cur[ip]
	if !ok {
		if ent, ok = c.prev[ip]; ok && now.Before(ent.expires) {
			c.insert(ip, ent)
		}
	}
	if !ok || !now.Before(ent.expires) {
		return ptrEntry{}, false
	}
	return ent, true
}

// dnsUnknown reports whether err leaves the answer unknown: any error except a
// DNS "name not found", which is a final answer.
func dnsUnknown(err error) bool {
	if err == nil {
		return false
	}
	var de *net.DNSError
	return !(errors.As(err, &de) && de.IsNotFound)
}

// lookup checks ip once and stores the answer. The reverse and the forward
// lookup share one deadline. Every PTR name is considered with its trailing dot
// removed; the first one inside a crawler domain becomes the crawler and only
// that name is resolved forward. The entry is confirmed when one of the
// forward addresses equals ip. A timeout or a temporary error of either lookup
// marks the entry failed (ptrFailedTTL) and counts one more consecutive
// failure of the address; the ptrFailedLimit-th one is final: the entry is not
// failed and lives ptrNegativeTTL. A "name not found" answer is final too: no
// PTR name gives ptrNegativeTTL, a crawler name without the address is
// unconfirmed. A found name is kept for ptrPositiveTTL. An answer resets the
// failure count.
func (c *ptrCache) lookup(ip netip.Addr) ptrEntry {
	timeout := ptrTimeout
	if c.timeout > 0 {
		timeout = c.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ent := ptrEntry{}
	names, err := c.resolve(ctx, ip.String())
	if dnsUnknown(err) {
		ent.failed = true
	} else if err == nil {
		host := ""
		for _, n := range names {
			n = strings.TrimSuffix(n, ".")
			if ent.name == "" {
				ent.name = n
			}
			if label := detect.CrawlerByHost(n); label != "" {
				ent.crawler, host = label, n
				break
			}
		}
		if host != "" {
			addrs, ferr := c.forwardLookup(ctx, host)
			for _, a := range addrs {
				if a.Unmap() == ip {
					ent.confirmed = true
					break
				}
			}
			ent.failed = !ent.confirmed && dnsUnknown(ferr)
		}
	}
	ttl := ptrNegativeTTL
	c.mu.Lock()
	defer c.mu.Unlock()
	if ent.failed {
		ent.failures = c.failuresLocked(ip) + 1
		if ent.failures >= ptrFailedLimit {
			ent.failed = false
		} else {
			ttl = ptrFailedTTL
		}
	} else if ent.name != "" {
		ttl = ptrPositiveTTL
	}
	ent.expires = time.Now().Add(ttl)
	c.insert(ip, ent)
	return ent
}

// failuresLocked returns the failure count of the last stored entry of ip,
// even when that entry has expired, or 0 when none is stored.
// The caller holds c.mu.
func (c *ptrCache) failuresLocked(ip netip.Addr) int {
	if ent, ok := c.cur[ip]; ok {
		return ent.failures
	}
	return c.prev[ip].failures
}

// forwardLookup resolves host to its addresses with the forward resolver.
// It returns the lookup error as is.
func (c *ptrCache) forwardLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if c.forward != nil {
		return c.forward(ctx, host)
	}
	return ptrResolver.LookupNetIP(ctx, "ip", host)
}

// insert adds ent for ip to cur, rotating generations when cur is full and
// ip is new to it. The caller holds c.mu.
func (c *ptrCache) insert(ip netip.Addr, ent ptrEntry) {
	if _, ok := c.cur[ip]; !ok && (c.cur == nil || len(c.cur) >= ptrCacheGen) {
		c.prev = c.cur
		c.cur = make(map[netip.Addr]ptrEntry, ptrCacheGen)
	}
	c.cur[ip] = ent
}
