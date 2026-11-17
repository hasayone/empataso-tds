package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

// newLimitClock returns a manual clock and a limiter that reads it.
func newLimitClock() (*fakeClock, *keyLimiter) {
	clk := &fakeClock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	return clk, newKeyLimiter(clk.now)
}

// newUnthrottledLimiter returns a limiter whose clock runs one second ahead on
// every reading, so a test that sends many requests with one key never meets
// the rate limit.
func newUnthrottledLimiter() *keyLimiter {
	clk := &fakeClock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	return newKeyLimiter(func() time.Time {
		clk.advance(time.Second)
		return clk.now()
	})
}

func TestKeyLimitBurstThenRefill(t *testing.T) {
	clk, lim := newLimitClock()
	for i := 0; i < int(keyBurst); i++ {
		release, den := lim.acquire("k1", false)
		if den != nil {
			t.Fatalf("request %d refused: %+v", i, den)
		}
		release()
	}
	_, den := lim.acquire("k1", false)
	if den == nil || den.msg != msgTooManyRequests || den.retry != 1 {
		t.Fatalf("request over burst: %+v", den)
	}
	if _, other := lim.acquire("k2", false); other != nil {
		t.Fatal("another key must have its own bucket")
	}
	clk.advance(150 * time.Millisecond)
	if _, den := lim.acquire("k1", false); den != nil {
		t.Fatalf("one token after 150ms: %+v", den)
	}
	if _, den := lim.acquire("k1", false); den == nil {
		t.Fatal("only one token was refilled")
	}
	clk.advance(time.Hour)
	allowed := 0
	for {
		if _, den := lim.acquire("k1", false); den != nil {
			break
		}
		allowed++
	}
	if allowed != int(keyBurst) {
		t.Fatalf("burst after an idle hour: %d", allowed)
	}
}

func TestKeyLimitRetrySeconds(t *testing.T) {
	for wait, want := range map[float64]int{0.05: 1, 1: 1, 1.01: 2, 4.5: 5} {
		if got := retrySeconds(wait); got != want {
			t.Errorf("retrySeconds(%v) = %d, want %d", wait, got, want)
		}
	}
}

func TestKeyLimitHeavyCap(t *testing.T) {
	_, lim := newLimitClock()
	r1, d1 := lim.acquire("k1", true)
	r2, d2 := lim.acquire("k1", true)
	if d1 != nil || d2 != nil {
		t.Fatalf("two heavy requests: %+v %+v", d1, d2)
	}
	_, den := lim.acquire("k1", true)
	if den == nil || den.msg != msgTooManyHeavy || den.retry != 1 {
		t.Fatalf("third heavy request: %+v", den)
	}
	if rl, d := lim.acquire("k1", false); d != nil {
		t.Fatalf("plain request while heavy slots are full: %+v", d)
	} else {
		rl()
	}
	r1()
	r1()
	if _, den := lim.acquire("k1", true); den != nil {
		t.Fatalf("heavy slot after a release: %+v", den)
	}
	if _, den := lim.acquire("k1", true); den == nil {
		t.Fatal("a double release must free one slot only")
	}
	r2()
}

func TestKeyLimitPrunesIdleEntries(t *testing.T) {
	clk, lim := newLimitClock()
	lim.acquire("old", false)
	release, _ := lim.acquire("busy", true)
	clk.advance(keyIdleTTL + keyPruneEvery)
	lim.acquire("fresh", false)
	if lim.size() != 2 {
		t.Fatalf("entries: %d, want busy and fresh", lim.size())
	}
	release()
	clk.advance(keyIdleTTL + keyPruneEvery)
	lim.acquire("fresh2", false)
	if lim.size() != 1 {
		t.Fatalf("entries after release and idle: %d", lim.size())
	}
}

func TestKeyLimitMiddlewareReturns429(t *testing.T) {
	_, lim := newLimitClock()
	s := &Server{keyLim: lim}
	h := s.keyLimit(routeRule{}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < int(keyBurst); i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, apiRequest(http.MethodGet, "/x"))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, apiRequest(http.MethodGet, "/x"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d", rec.Code)
	}
	if n, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || n < 1 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if body := rec.Body.String(); !strings.Contains(body, `"error":"too many requests"`) ||
		!strings.Contains(body, `"code":"too_many_requests"`) {
		t.Fatalf("body %s", body)
	}
}

func TestKeyLimitHeavyMiddlewareHoldsSlot(t *testing.T) {
	_, lim := newLimitClock()
	s := &Server{keyLim: lim}
	entered := make(chan struct{}, 2)
	hold := make(chan struct{})
	h := s.keyLimit(routeRule{heavy: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-hold
		w.WriteHeader(http.StatusOK)
	}))
	var wg sync.WaitGroup
	for i := 0; i < keyHeavyMax; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), apiRequest(http.MethodGet, "/heavy"))
		}()
	}
	for i := 0; i < keyHeavyMax; i++ {
		<-entered
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, apiRequest(http.MethodGet, "/heavy"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" ||
		!strings.Contains(rec.Body.String(), "too many heavy requests") {
		t.Fatalf("third heavy request: %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	close(hold)
	wg.Wait()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, apiRequest(http.MethodGet, "/heavy"))
	if rec.Code != http.StatusOK {
		t.Fatalf("after the slots were freed: %d", rec.Code)
	}
}

func TestKeyLimitCookieSessionUnlimited(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, lim := newLimitClock()
	rig.s.keyLim = lim
	for i := 0; i < int(keyBurst)*2; i++ {
		if code, _ := rig.asAdmin(t, http.MethodGet, "/api/offers", nil); code != http.StatusOK {
			t.Fatalf("cookie request %d: %d", i, code)
		}
	}
	if lim.size() != 0 {
		t.Fatalf("a cookie session created %d limiter entries", lim.size())
	}
}

func TestKeyLimitHTTPBurstAndUnverifiedKey(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, lim := newLimitClock()
	rig.s.keyLim = lim
	_, raw := rig.newKey(t, security.KeyAccess{})
	for i := 0; i < int(keyBurst); i++ {
		if code, _ := rig.call(t, raw, http.MethodGet, "/api/offers", nil); code != http.StatusOK {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	code, body := rig.call(t, raw, http.MethodGet, "/api/offers", nil)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "too many requests") {
		t.Fatalf("over burst: %d %s", code, body)
	}
	before := lim.size()
	for i := 0; i < 5; i++ {
		if code, _ := rig.call(t, spoilKey(raw), http.MethodGet, "/api/offers", nil); code != http.StatusUnauthorized {
			t.Fatalf("spoiled key: %d", code)
		}
	}
	if lim.size() != before {
		t.Fatal("an unverified key created a limiter entry")
	}
}

func TestKeyLimitRevokeAndDeleteClearEntry(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, lim := newLimitClock()
	rig.s.keyLim = lim
	id, raw := rig.newKey(t, security.KeyAccess{})
	if code, _ := rig.call(t, raw, http.MethodGet, "/api/offers", nil); code != http.StatusOK {
		t.Fatalf("first call: %d", code)
	}
	if lim.size() != 1 {
		t.Fatalf("entries after a call: %d", lim.size())
	}
	if code, body := rig.asAdmin(t, http.MethodDelete, "/api/api-keys/"+id, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", code, body)
	}
	if lim.size() != 0 {
		t.Fatalf("entries after revoke: %d", lim.size())
	}
	lim.acquire(id, false)
	if code, body := rig.asAdmin(t, http.MethodDelete, "/api/api-keys/"+id+"?hard=1", nil); code != http.StatusNoContent {
		t.Fatalf("hard delete: %d %s", code, body)
	}
	if lim.size() != 0 {
		t.Fatalf("entries after hard delete: %d", lim.size())
	}
}

func TestKeyLimitCookieSessionIgnoresHeavyRoutes(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, lim := newLimitClock()
	rig.s.keyLim = lim
	var wg sync.WaitGroup
	codes := make(chan string, len(heavyRoutes)*(keyHeavyMax+2))
	for _, pattern := range heavyRoutes {
		path := strings.TrimPrefix(pattern, "GET ")
		if strings.HasPrefix(path, "/api/keywords/") {
			// These routes have their own two-slot gate, which answers 429 to a burst from any session.
			continue
		}
		for i := 0; i < keyHeavyMax+2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, _, err := doJSON(rig.admin, http.MethodGet, rig.srv.URL+path, rig.csrf, nil)
				if err != nil || code == http.StatusTooManyRequests {
					codes <- path + ": " + strconv.Itoa(code) + " " + fmt.Sprint(err)
				}
			}()
		}
	}
	wg.Wait()
	close(codes)
	for refused := range codes {
		t.Errorf("cookie request refused: %s", refused)
	}
	if lim.size() != 0 {
		t.Fatalf("a cookie session created %d limiter entries", lim.size())
	}
}

func TestKeyHeavyRoutesCoverClickHouseReads(t *testing.T) {
	for _, pattern := range []string{
		"GET /api/report", "GET /api/logs", "GET /api/log-values", "GET /api/postbacks",
		"GET /api/stats/summary", "GET /api/stats/timeseries", "GET /api/stats/breakdown",
		"GET /api/stats/performance", "GET /api/stats/facts", "GET /api/keywords/stats", "GET /api/keywords/trend",
		"GET /api/logs/filters",
	} {
		if !routeAccess[pattern].heavy {
			t.Errorf("%s reads ClickHouse ranges and must take a heavy slot of an API key", pattern)
		}
	}
	for _, pattern := range []string{"GET /api/offers", "GET /api/groups"} {
		if routeAccess[pattern].heavy {
			t.Errorf("%s is a cheap read and must not be heavy", pattern)
		}
	}
}
