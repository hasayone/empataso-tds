// Package fetch provides HTTP loading of external content with an in-memory TTL cache.
// Used for CURL redirects (curl()) and loading [REMOTE] values (remote_pars()).
// The cache lives in the process's memory.
package fetch

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
	"golang.org/x/sync/singleflight"
)

// cacheBudget bounds the bytes the cache may hold, keys and values together.
//
// The cache keys are URLs after macro expansion, and several macros come from
// the visitor: [IP], [SUBID] and its old name [CID] (fresh on every request), [PAR-1]..[PAR-5] (the raw
// query string). A key built from those is asked for once and never again, and
// a body under it is up to 8 MiB. Without a bound the map is a log of every
// such URL the process has ever seen, for as long as it runs.
//
// Bytes, not entries: an entry cap with 8 MiB bodies is a sign that says
// "cap". Keys are counted as well as values, otherwise a long key with a
// one-byte body is free and the count of entries is unbounded again.
//
// Not configurable: the number has not been measured, so there is nothing to
// tune it against. 64 MiB is hundreds of real pages, and the log line written
// when it is hit says which kind of key filled it.
const cacheBudget = 64 << 20

// minSweep is the smallest map size at which a sweep of expired entries is
// scheduled. Below it the map is too small to be worth walking.
const minSweep = 1024

// fullLogEvery is how often the "cache full" line may be written. One line per
// refusal would write at the speed of the traffic that filled the cache.
const fullLogEvery = time.Minute

// Client is a thread-safe loader with a cache.
type Client struct {
	hc  *http.Client
	ua  string
	log *slog.Logger
	mu  sync.Mutex
	now func() time.Time
	c   map[string]entry
	sf  singleflight.Group

	// size is the bytes held by c: len(key)+len(val) over every entry.
	size int
	// budget is cacheBudget, as a field so tests can shrink it.
	budget int
	// sweepAt is the map size that triggers the next sweep of expired
	// entries. It is reset to twice the size left after each sweep, so the
	// walk is amortised: the map may hold expired entries, but never more of
	// them than of live ones, and the sweep cost is paid once per doubling.
	sweepAt int
	// lastFull is when the last "cache full" line was written.
	lastFull time.Time

	// onStore, when set, is called after an entry has been written and the
	// mutex released. A test seam; nil in production.
	//
	// The ordering carries two things at once. Called while mu is held, a
	// callback that reads the map deadlocks on a non-reentrant mutex — in the
	// very test written to make this reliable. And "after Unlock" is also the
	// honest meaning: the callback says the entry is stored and visible, not
	// that a store is beginning. That is the state a test needs, not the
	// moment. Do not move this call earlier.
	onStore func()
}

type entry struct {
	val string
	exp time.Time
}

// Limits caps the outbound side of the client.
type Limits struct {
	// MaxConnsPerHost bounds simultaneous connections to one host. This is the
	// bulkhead: past it, requests wait on the transport instead of each opening
	// its own socket. Zero means the package default.
	MaxConnsPerHost int
	// MaxIdleConnsPerHost is how many warm connections are kept for reuse.
	// Zero means the package default.
	MaxIdleConnsPerHost int
	// Backstop is the ceiling on a single request when the caller's context
	// carries no deadline of its own. Callers on the hot path are expected to
	// set a tighter one; this only keeps a forgotten call from hanging forever.
	Backstop time.Duration
	// AllowPrivate disables the GuardDial check so the client can connect to
	// loopback and other private addresses. For use in tests only; no env
	// variable or config field sets this.
	AllowPrivate bool
}

// Defaults for Limits. The shape is deliberate; the numbers are a starting
// point, not a measurement.
//
// http.DefaultTransport keeps MaxIdleConnsPerHost at 2, which is fine for a
// program that talks to many hosts occasionally and wrong for this one: every
// visitor whose stream fetches from the same partner competes for those two
// slots, and the rest open a fresh connection — a TCP and TLS handshake per
// request, plus the sockets they leave in TIME_WAIT.
//
// MaxConnsPerHost has no default in net/http at all: unlimited. That is the
// part that takes the engine down rather than merely slowing it. A partner that
// stalls does not fail requests, it holds them, and without a ceiling every
// held request is another connection and another goroutine parked in the
// handler until its deadline.
const (
	defaultMaxConnsPerHost     = 128
	defaultMaxIdleConnsPerHost = 64
	defaultBackstop            = 10 * time.Second
)

// New creates a client with the given User-Agent and the default limits.
// log may be nil.
func New(ua string, log *slog.Logger) *Client { return NewWithLimits(ua, log, Limits{}) }

// NewWithLimits creates a client with explicit outbound limits. log may be nil.
func NewWithLimits(ua string, log *slog.Logger, l Limits) *Client {
	if ua == "" {
		ua = "Mozilla/5.0"
	}
	if log == nil {
		log = slog.Default()
	}
	if l.MaxConnsPerHost <= 0 {
		l.MaxConnsPerHost = defaultMaxConnsPerHost
	}
	if l.MaxIdleConnsPerHost <= 0 {
		l.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}
	if l.MaxIdleConnsPerHost > l.MaxConnsPerHost {
		l.MaxIdleConnsPerHost = l.MaxConnsPerHost
	}
	if l.Backstop <= 0 {
		l.Backstop = defaultBackstop
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	if !l.AllowPrivate {
		dialer.Control = security.GuardDial
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		MaxConnsPerHost:       l.MaxConnsPerHost,
		MaxIdleConnsPerHost:   l.MaxIdleConnsPerHost,
		MaxIdleConns:          l.MaxIdleConnsPerHost * 8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		hc:      &http.Client{Transport: tr, Timeout: l.Backstop},
		ua:      ua,
		log:     log,
		now:     time.Now,
		c:       make(map[string]entry),
		budget:  cacheBudget,
		sweepAt: minSweep,
	}
}

// Get loads the body at URL (without caching). Returns an error on a non-2xx status.
func (c *Client) Get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	// A non-2xx body is never content: it is the upstream's error page. Both
	// callers treat it as a failure, and returning it alongside the error only
	// lets a caller with a sloppy condition render someone else's 502 as a
	// normal response. Drop the body — there is nothing to be right about.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &httpError{resp.StatusCode}
	}
	return string(b), nil
}

// GetCached returns the value for key, loading it through load on a miss and
// caching it for ttl. On a load error the cache is not updated.
//
// Concurrent misses on one key are collapsed into a single load. That is the
// point of the singleflight, and it is also why load runs on a context of its
// own rather than on any caller's: singleflight hands the leader's result to
// every waiter, so a leader whose visitor closed the tab would fail the fetch
// for the whole queue behind it. Today one visitor leaving costs exactly one
// request — its own — and that has to stay true. The detached load is bounded
// by http.Client's own timeout.
//
// Each caller still waits under its own ctx and can walk away without
// disturbing the others. The load outlives all of them if it has to, and
// finishes the job: the entry is written from inside the group, so a fetch
// nobody is waiting for any more still lands in the cache and the next arrival
// gets a hit instead of starting over. Storing in the caller instead would make
// an abandoned fetch ten seconds of work thrown away.
func (c *Client) GetCached(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (string, error)) (string, error) {
	if ttl <= 0 {
		// ttl 0 is the operator saying "answer this one per visitor", not just
		// "do not keep it". A Remote configured with Cache: 0 is a per-visitor
		// decision — capacity, price, which offer is next in the rotation — and
		// a shared key would otherwise hand one draw to a whole burst of
		// arrivals. Collapsing and storing are separate questions: a value we
		// decline to keep because it is too big is still the same value for
		// everyone and should collapse. A value declared per-visitor must not.
		return load(ctx)
	}
	if v, ok := c.lookup(key); ok {
		return v, nil
	}
	ch := c.sf.DoChan(key, func() (any, error) {
		v, err := load(context.Background())
		if err != nil {
			return "", err
		}
		c.store(key, v, ttl)
		return v, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return "", r.Err
		}
		return r.Val.(string), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// lookup returns a cached value that has not expired.
func (c *Client) lookup(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.c[key]
	if !ok || !c.now().Before(e.exp) {
		return "", false
	}
	return e.val, true
}

// store puts val under key if the budget allows.
//
// When it does not, expired entries are swept first. If that is not enough the
// value is not stored — not evicted for, not stored. Nothing live is thrown out
// to make room for a key that may never be asked for again; the caller still
// gets its value, the next caller loads it again, and that is the state the
// cache was in before it existed, not a failure. The refusal is logged, at most
// once a minute, with the key that was refused and the shape of what is in the
// map: a handful of entries adding up to the budget means huge bodies, tens of
// thousands of small ones means one-off keys — a [SUBID] or [PAR-n] in a curl
// URL, i.e. a template to fix rather than code.
func (c *Client) store(key, val string, ttl time.Duration) {
	c.mu.Lock()
	now := c.now()
	if old, ok := c.c[key]; ok {
		c.size -= len(key) + len(old.val)
		delete(c.c, key)
	}
	need := len(key) + len(val)
	if len(c.c) >= c.sweepAt || c.size+need > c.budget {
		c.sweep(now)
	}
	if c.size+need <= c.budget {
		c.c[key] = entry{val: val, exp: now.Add(ttl)}
		c.size += need
		cb := c.onStore
		c.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}
	report := now.Sub(c.lastFull) >= fullLogEvery
	if report {
		c.lastFull = now
	}
	entries, size := len(c.c), c.size
	c.mu.Unlock()
	if report {
		c.log.Warn("fetch: cache full, value not stored",
			"key", head(key, 100), "value_bytes", len(val),
			"entries", entries, "cache_bytes", size, "budget_bytes", c.budget)
	}
}

// sweep drops expired entries and schedules the next sweep at twice the size
// that is left. Called with mu held.
func (c *Client) sweep(now time.Time) {
	for k, e := range c.c {
		if !now.Before(e.exp) {
			c.size -= len(k) + len(e.val)
			delete(c.c, k)
		}
	}
	c.sweepAt = 2 * len(c.c)
	if c.sweepAt < minSweep {
		c.sweepAt = minSweep
	}
}

// head returns the first n bytes of s.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "fetch: http status " + http.StatusText(e.code) }
