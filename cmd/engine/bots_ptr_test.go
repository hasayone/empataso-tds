package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/ipindex"
)

const (
	// googleHost is a PTR name inside a Google crawler domain.
	googleHost = "crawl-66-249-66-1.googlebot.com"
	// uaBingBot is the user agent of Bingbot.
	uaBingBot = "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)"
)

// ptrNamed returns a reverse resolver that answers with names.
func ptrNamed(names ...string) func(context.Context, string) ([]string, error) {
	return func(context.Context, string) ([]string, error) { return names, nil }
}

// fwdTo returns a forward resolver that answers with addrs.
func fwdTo(addrs ...netip.Addr) func(context.Context, string) ([]netip.Addr, error) {
	return func(context.Context, string) ([]netip.Addr, error) { return addrs, nil }
}

// stubPTR installs a ptrCache with the given resolvers as ptrNames until the test ends.
// resolve and forward replace the reverse and forward DNS lookups.
func stubPTR(t testing.TB, resolve func(context.Context, string) ([]string, error), forward func(context.Context, string) ([]netip.Addr, error)) *ptrCache {
	t.Helper()
	c := &ptrCache{resolve: resolve, forward: forward}
	prev := ptrNames
	ptrNames = c
	t.Cleanup(func() { ptrNames = prev })
	return c
}

// waitPTR waits until the background check of ip stored its entry and left the
// in-flight set, and returns the entry. It fails the test after two seconds.
func waitPTR(t testing.TB, c *ptrCache, ip netip.Addr) ptrEntry {
	t.Helper()
	ip = ip.Unmap()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		_, busy := c.inflight[ip]
		c.mu.Unlock()
		if ent, ok := c.cached(ip); ok && !busy {
			return ent
		}
		if time.Now().After(deadline) {
			t.Fatalf("check of %v did not finish", ip)
		}
		time.Sleep(time.Millisecond)
	}
}

// ptrBotEnv returns an empty IP index and signatures over a fresh data directory.
func ptrBotEnv(t *testing.T) (*ipindex.Set, *detect.Signatures, string) {
	t.Helper()
	dir := t.TempDir()
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	return lists, detect.NewSignatures(dir, log), dir
}

// botVisit builds the visitor facts of a request from ip with user agent ua.
func botVisit(ip netip.Addr, ua string) botInput {
	return botInput{IP: ip, UA: ua, Ref: "-", Lang: "en", Device: detect.Phone}
}

// TestPTRConfirmed checks that a crawler PTR name confirmed by the forward lookup is cached as confirmed.
func TestPTRConfirmed(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	c := &ptrCache{resolve: ptrNamed(googleHost + "."), forward: fwdTo(ip)}
	if _, ok := c.peek(ip, ptrClassCrawler); ok {
		t.Fatal("the first peek must not answer")
	}
	ent := waitPTR(t, c, ip)
	if ent.crawler != detect.BotGoogle || !ent.confirmed || ent.name != googleHost {
		t.Fatalf("entry = %+v", ent)
	}
	if got, ok := c.peek(ip, ptrClassCrawler); !ok || got != ent {
		t.Fatalf("second peek = %+v, %v", got, ok)
	}
}

// TestPTRForeignNameSkipsForward checks that a PTR name outside crawler domains is cached without a forward lookup.
func TestPTRForeignNameSkipsForward(t *testing.T) {
	var forwards atomic.Int64
	c := &ptrCache{resolve: ptrNamed("host.example.net."), forward: func(context.Context, string) ([]netip.Addr, error) {
		forwards.Add(1)
		return nil, nil
	}}
	ip := netip.MustParseAddr("203.0.113.9")
	c.peek(ip, ptrClassCrawler)
	ent := waitPTR(t, c, ip)
	if ent.crawler != "" || ent.confirmed || ent.name != "host.example.net" {
		t.Fatalf("entry = %+v", ent)
	}
	if forwards.Load() != 0 {
		t.Fatalf("forward lookups = %d, want 0", forwards.Load())
	}
}

// TestPTRSpoofedNameNotConfirmed checks that a crawler PTR name whose forward lookup does not return the address stays unconfirmed.
func TestPTRSpoofedNameNotConfirmed(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", IsNotFound: true}
	cases := map[string]struct {
		forward func(context.Context, string) ([]netip.Addr, error)
		failed  bool
	}{
		"no record":     {fwdTo(), false},
		"other address": {fwdTo(netip.MustParseAddr("66.249.66.2")), false},
		"forward NXDOMAIN": {func(context.Context, string) ([]netip.Addr, error) {
			return nil, notFound
		}, false},
		"forward SERVFAIL": {func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("servfail")
		}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &ptrCache{resolve: ptrNamed(googleHost + "."), forward: tc.forward}
			ip := netip.MustParseAddr("203.0.113.9")
			c.peek(ip, ptrClassCrawler)
			ent := waitPTR(t, c, ip)
			if ent.crawler != detect.BotGoogle || ent.confirmed || ent.failed != tc.failed {
				t.Fatalf("entry = %+v, want unconfirmed google with failed=%v", ent, tc.failed)
			}
		})
	}
}

// TestPTRFailedAndFinalTTL checks the lifetime of a temporary error, a final "no name" answer and a found name.
func TestPTRFailedAndFinalTTL(t *testing.T) {
	cases := []struct {
		name    string
		resolve func(context.Context, string) ([]string, error)
		failed  bool
		ttl     time.Duration
	}{
		{"SERVFAIL", func(context.Context, string) ([]string, error) { return nil, errors.New("servfail") }, true, ptrFailedTTL},
		{"NXDOMAIN", func(context.Context, string) ([]string, error) {
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		}, false, ptrNegativeTTL},
		{"found name", ptrNamed("host.example.net."), false, ptrPositiveTTL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &ptrCache{resolve: tc.resolve}
			ip := netip.MustParseAddr("203.0.113.9")
			before := time.Now()
			c.peek(ip, ptrClassCrawler)
			ent := waitPTR(t, c, ip)
			if ent.failed != tc.failed {
				t.Fatalf("failed = %v, want %v", ent.failed, tc.failed)
			}
			if d := ent.expires.Sub(before); d < tc.ttl || d > tc.ttl+5*time.Second {
				t.Fatalf("entry lives %v, want about %v", d, tc.ttl)
			}
		})
	}
}

// TestPTRMultipleNamesAndTrailingDot checks that the first crawler name wins, the dot is cut and only that name is resolved forward.
func TestPTRMultipleNamesAndTrailingDot(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	var host atomic.Value
	var forwards atomic.Int64
	c := &ptrCache{
		resolve: ptrNamed("a.example.com.", "crawl.googlebot.com.", "evil.googlebot.com."),
		forward: func(_ context.Context, h string) ([]netip.Addr, error) {
			host.Store(h)
			forwards.Add(1)
			return []netip.Addr{ip}, nil
		},
	}
	c.peek(ip, ptrClassCrawler)
	ent := waitPTR(t, c, ip)
	if ent.crawler != detect.BotGoogle || !ent.confirmed || ent.name != "a.example.com" {
		t.Fatalf("entry = %+v", ent)
	}
	if got, _ := host.Load().(string); got != "crawl.googlebot.com" || forwards.Load() != 1 {
		t.Fatalf("forward host = %q (%d calls), want crawl.googlebot.com once", got, forwards.Load())
	}
}

// TestPTRMappedAddress checks that an IPv4-mapped IPv6 address in the forward answer or the query matches the IPv4 address.
func TestPTRMappedAddress(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	mapped := netip.MustParseAddr("::ffff:66.249.66.1")
	c := &ptrCache{resolve: ptrNamed(googleHost + "."), forward: fwdTo(mapped)}
	c.peek(mapped, ptrClassCrawler)
	ent := waitPTR(t, c, ip)
	if !ent.confirmed {
		t.Fatalf("entry = %+v, want confirmed", ent)
	}
	if got, ok := c.peek(ip, ptrClassCrawler); !ok || got != ent {
		t.Fatalf("peek of the plain address = %+v, %v", got, ok)
	}
}

// TestPTRIPv6Crawler checks the check of an IPv6 crawler address.
func TestPTRIPv6Crawler(t *testing.T) {
	ip := netip.MustParseAddr("2001:4860:4801:10::1")
	c := &ptrCache{resolve: ptrNamed("crawl-2001-4860-4801-10--1.googlebot.com."), forward: fwdTo(ip)}
	c.peek(ip, ptrClassCrawler)
	ent := waitPTR(t, c, ip)
	if ent.crawler != detect.BotGoogle || !ent.confirmed {
		t.Fatalf("entry = %+v", ent)
	}
}

// TestPTRDeadlineShared checks that the reverse and the forward lookup share one deadline.
func TestPTRDeadlineShared(t *testing.T) {
	var mu sync.Mutex
	var deadlines []time.Time
	note := func(ctx context.Context) {
		d, _ := ctx.Deadline()
		mu.Lock()
		deadlines = append(deadlines, d)
		mu.Unlock()
	}
	c := &ptrCache{
		timeout: 150 * time.Millisecond,
		resolve: func(ctx context.Context, _ string) ([]string, error) {
			note(ctx)
			time.Sleep(50 * time.Millisecond)
			return []string{googleHost + "."}, nil
		},
		forward: func(ctx context.Context, _ string) ([]netip.Addr, error) {
			note(ctx)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ip := netip.MustParseAddr("66.249.66.1")
	start := time.Now()
	c.peek(ip, ptrClassCrawler)
	ent := waitPTR(t, c, ip)
	elapsed := time.Since(start)
	mu.Lock()
	defer mu.Unlock()
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("deadlines = %v, want one shared deadline", deadlines)
	}
	if elapsed < 140*time.Millisecond || elapsed > time.Second {
		t.Fatalf("check took %v, want about the 150ms timeout", elapsed)
	}
	if ent.crawler != detect.BotGoogle || ent.confirmed {
		t.Fatalf("entry = %+v, want unconfirmed google", ent)
	}
}

// TestPTRTimeoutBound checks that a resolver that never answers is cut at the timeout.
func TestPTRTimeoutBound(t *testing.T) {
	c := &ptrCache{timeout: 50 * time.Millisecond, resolve: func(ctx context.Context, _ string) ([]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ip := netip.MustParseAddr("203.0.113.9")
	start := time.Now()
	c.peek(ip, ptrClassCrawler)
	ent := waitPTR(t, c, ip)
	if elapsed := time.Since(start); elapsed < 45*time.Millisecond || elapsed > time.Second {
		t.Fatalf("check took %v, want about 50ms", elapsed)
	}
	if ent.name != "" || ent.crawler != "" || ent.confirmed || !ent.failed {
		t.Fatalf("entry = %+v, want empty and failed", ent)
	}
}

// fillSlots takes every slot of the semaphore of class and returns how many it took.
func fillSlots(c *ptrCache, class ptrClass) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sem[class] = make(chan struct{}, ptrSlots[class])
	for i := 0; i < ptrSlots[class]; i++ {
		c.sem[class] <- struct{}{}
	}
	return ptrSlots[class]
}

// TestPTRSemaphoreFull checks that with every slot of a class busy peek starts nothing and caches nothing.
func TestPTRSemaphoreFull(t *testing.T) {
	for _, class := range []ptrClass{ptrClassCrawler, ptrClassPTR} {
		var calls atomic.Int64
		c := &ptrCache{resolve: func(context.Context, string) ([]string, error) {
			calls.Add(1)
			return []string{"host.test"}, nil
		}}
		n := fillSlots(c, class)
		for i := 0; i < 50; i++ {
			if _, ok := c.peek(ptrTestAddr(i), class); ok {
				t.Fatal("peek must not answer for an unchecked address")
			}
		}
		time.Sleep(20 * time.Millisecond)
		c.mu.Lock()
		cached, running := len(c.cur)+len(c.prev), len(c.inflight)
		c.mu.Unlock()
		if calls.Load() != 0 || cached != 0 || running != 0 {
			t.Fatalf("class %d: calls=%d cached=%d inflight=%d, want all zero", class, calls.Load(), cached, running)
		}
		<-c.sem[class]
		ip := ptrTestAddr(99)
		c.peek(ip, class)
		if ent := waitPTR(t, c, ip); ent.name != "host.test" {
			t.Fatalf("entry = %+v", ent)
		}
		for len(c.sem[class]) != n-1 {
			time.Sleep(time.Millisecond)
		}
	}
}

// TestPTRClassesAreIndependent checks that a full ch_ptr semaphore does not stop a search-engine check, and the other way round.
func TestPTRClassesAreIndependent(t *testing.T) {
	if ptrSlots[ptrClassCrawler] != 16 || ptrSlots[ptrClassPTR] != 8 {
		t.Fatalf("slots = %v, want 16 and 8", ptrSlots)
	}
	for _, full := range []ptrClass{ptrClassPTR, ptrClassCrawler} {
		other := ptrClassCrawler
		if full == ptrClassCrawler {
			other = ptrClassPTR
		}
		c := &ptrCache{resolve: ptrNamed("host.test.")}
		fillSlots(c, full)
		ip := ptrTestAddr(5)
		c.peek(ip, other)
		if ent := waitPTR(t, c, ip); ent.name != "host.test" {
			t.Fatalf("class %d blocked by full class %d: entry = %+v", other, full, ent)
		}
	}
}

// TestPTRConcurrentPeeksResolveOnce checks that 100 simultaneous peeks of one address run one reverse and one forward lookup.
func TestPTRConcurrentPeeksResolveOnce(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	var reverse, forward atomic.Int64
	release := make(chan struct{})
	c := &ptrCache{
		resolve: func(context.Context, string) ([]string, error) {
			reverse.Add(1)
			<-release
			return []string{googleHost + "."}, nil
		},
		forward: func(context.Context, string) ([]netip.Addr, error) {
			forward.Add(1)
			return []netip.Addr{ip}, nil
		},
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.peek(ip, ptrClassCrawler)
		}()
	}
	wg.Wait()
	for reverse.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	waitPTR(t, c, ip)
	if reverse.Load() != 1 || forward.Load() != 1 {
		t.Fatalf("reverse=%d forward=%d, want 1 and 1", reverse.Load(), forward.Load())
	}
}

// TestDetectBotConfirmedGoogle checks that a Googlebot user agent from a DNS-confirmed address is google.
func TestDetectBotConfirmedGoogle(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	c := stubPTR(t, ptrNamed(googleHost+"."), fwdTo(ip))
	lists, sigs, dir := ptrBotEnv(t)
	cfg := config.Bots{CheckUA: true}
	if got := detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), cfg); got != "google" {
		t.Fatalf("first click = %q, want google by user agent", got)
	}
	waitPTR(t, c, ip)
	if got := detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), cfg); got != "google" {
		t.Fatalf("confirmed = %q, want google", got)
	}
}

// TestDetectBotFakeLabels checks the fake label for a foreign PTR, a spoofed PTR, NXDOMAIN and a crawler of another engine.
func TestDetectBotFakeLabels(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.9")
	cases := []struct {
		name    string
		ua      string
		resolve func(context.Context, string) ([]string, error)
		forward func(context.Context, string) ([]netip.Addr, error)
		want    string
	}{
		{"foreign PTR", uaGoogleBot, ptrNamed("host.example.net."), fwdTo(), "fake_google"},
		{"spoofed PTR without forward record", uaGoogleBot, ptrNamed(googleHost + "."), fwdTo(), "fake_google"},
		{"NXDOMAIN", uaGoogleBot, func(context.Context, string) ([]string, error) {
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		}, fwdTo(), "fake_google"},
		{"forward NXDOMAIN", uaGoogleBot, ptrNamed(googleHost + "."), func(context.Context, string) ([]netip.Addr, error) {
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		}, "fake_google"},
		{"Google crawler claims to be Bing", uaBingBot, ptrNamed(googleHost + "."), fwdTo(ip), "fake_bing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := stubPTR(t, tc.resolve, tc.forward)
			lists, sigs, dir := ptrBotEnv(t)
			cfg := config.Bots{CheckUA: true}
			if got := detectBot(lists, sigs, dir, botVisit(ip, tc.ua), cfg); got != detect.BotGoogle && got != detect.BotBing {
				t.Fatalf("first click = %q, want the claimed engine", got)
			}
			waitPTR(t, c, ip)
			if got := detectBot(lists, sigs, dir, botVisit(ip, tc.ua), cfg); got != tc.want {
				t.Fatalf("after the check = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectBotListedIPSkipsDNS checks that an address in ip_<label> confirms the engine without any lookup.
func TestDetectBotListedIPSkipsDNS(t *testing.T) {
	var calls atomic.Int64
	stubPTR(t, func(context.Context, string) ([]string, error) {
		calls.Add(1)
		return nil, nil
	}, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ip_google.dat"), []byte("66.249.66.0/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lists := ipindex.NewSet(dir, discardLog())
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, discardLog())
	got := detectBot(lists, sigs, dir, botVisit(netip.MustParseAddr("66.249.66.7"), uaGoogleBot), config.Bots{CheckUA: true})
	if got != "google" {
		t.Fatalf("listed address = %q, want google", got)
	}
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("reverse DNS was asked %d times for a listed address", calls.Load())
	}
}

// TestDetectBotFirstClickDoesNotWait checks that detectBot answers by user agent while DNS is still blocked.
func TestDetectBotFirstClickDoesNotWait(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	release := make(chan struct{})
	c := stubPTR(t, func(context.Context, string) ([]string, error) {
		<-release
		return []string{googleHost + "."}, nil
	}, fwdTo(ip))
	lists, sigs, dir := ptrBotEnv(t)
	start := time.Now()
	got := detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), config.Bots{CheckUA: true})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("detectBot waited %v for DNS", elapsed)
	}
	if got != "google" {
		t.Fatalf("first click = %q, want google", got)
	}
	close(release)
	waitPTR(t, c, ip)
}

// TestDetectBotChPTR checks the ch_ptr toggle with a human user agent: confirmed, fake, and no label for other names.
func TestDetectBotChPTR(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	cases := []struct {
		name    string
		resolve func(context.Context, string) ([]string, error)
		forward func(context.Context, string) ([]netip.Addr, error)
		want    string
	}{
		{"confirmed crawler", ptrNamed(googleHost + "."), fwdTo(ip), "google"},
		{"unconfirmed crawler", ptrNamed(googleHost + "."), fwdTo(), "fake_google"},
		{"ordinary host", ptrNamed("host.example.net."), fwdTo(), detect.BotNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := stubPTR(t, tc.resolve, tc.forward)
			lists, sigs, dir := ptrBotEnv(t)
			cfg := config.Bots{PTR: true}
			if got := detectBot(lists, sigs, dir, botVisit(ip, uaWinChrome), cfg); got != detect.BotNone {
				t.Fatalf("first click = %q, want none", got)
			}
			waitPTR(t, c, ip)
			if got := detectBot(lists, sigs, dir, botVisit(ip, uaWinChrome), cfg); got != tc.want {
				t.Fatalf("after the check = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIsProxyPrivateRelay checks ip_proxy, the second organization and the iCloud Private Relay exception.
func TestIsProxyPrivateRelay(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"ip_proxy.dat":         "203.0.113.0/24\n",
		"ip_private_relay.dat": "203.0.113.5\n198.51.100.77\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lists := ipindex.NewSet(dir, discardLog())
	lists.Load("ip_proxy", "ip_private_relay")
	d := &engineDeps{lists: lists, sigs: detect.NewSignatures(dir, discardLog())}
	cases := []struct {
		name         string
		ip, org, alt string
		want         bool
	}{
		{"proxy list", "203.0.113.6", "-", "-", true},
		{"proxy list and private relay", "203.0.113.5", "-", "-", false},
		{"organization", "198.51.100.1", "Acme Hosting", "-", true},
		{"second organization", "198.51.100.1", "-", "Some VPN Ltd", true},
		{"second organization empty", "198.51.100.1", "-", "", false},
		{"organization and private relay", "198.51.100.77", "Acme Hosting", "-", false},
		{"private relay only", "198.51.100.77", "-", "-", false},
		{"ordinary visitor", "198.51.100.1", "Home ISP", "Home ISP", false},
	}
	for _, tc := range cases {
		if got := isProxy(d, netip.MustParseAddr(tc.ip), tc.org, tc.alt); got != tc.want {
			t.Errorf("%s: isProxy = %v, want %v", tc.name, got, tc.want)
		}
	}
}
