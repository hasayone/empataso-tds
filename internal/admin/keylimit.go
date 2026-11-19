package admin

import (
	"math"
	"net/http"
	"sync"
	"time"
)

const (
	// keyRate is the sustained requests per second one API key may make.
	keyRate = 10.0
	// keyBurst is the most requests one API key may make at once after an idle time.
	keyBurst = 30.0
	// keyHeavyMax is the most heavy requests one API key may run at the same time.
	keyHeavyMax = 2
	// keyIdleTTL is how long an untouched key entry stays before it is dropped.
	keyIdleTTL = 10 * time.Minute
	// keyPruneEvery is the least time between two sweeps of idle entries.
	keyPruneEvery = time.Minute

	msgTooManyRequests = "too many requests"
	msgTooManyHeavy    = "too many heavy requests"
)

// keyBucket is the limiter state of one API key: the tokens left, the time they
// were last counted, and the heavy requests running now.
type keyBucket struct {
	tokens float64
	last   time.Time
	heavy  int
}

// limitDenial describes a refused request: msg is the error text and retry the
// Retry-After value in whole seconds, at least one.
type limitDenial struct {
	msg   string
	retry int
}

// write answers the refused request with 429, the Retry-After header and retry_after in the body.
// w is the response. There is no error return.
func (d *limitDenial) write(w http.ResponseWriter) {
	writeRetryLater(w, d.msg, time.Duration(d.retry)*time.Second)
}

// keyLimiter applies a token bucket and a heavy-request cap to every verified
// API key. Entries are created only for keys that passed verification, are
// removed by forget when a key is revoked or deleted, and are swept when idle.
type keyLimiter struct {
	mu        sync.Mutex
	now       func() time.Time
	buckets   map[string]*keyBucket
	lastPrune time.Time
}

// newKeyLimiter returns an empty limiter. now is its clock; nil uses time.Now.
func newKeyLimiter(now func() time.Time) *keyLimiter {
	if now == nil {
		now = time.Now
	}
	return &keyLimiter{now: now, buckets: make(map[string]*keyBucket)}
}

// acquire counts one request of key id. heavy marks a route that reads large
// ranges of data and also takes one of the key's heavy slots.
//
// It returns a release function and a nil denial when the request may run; the
// caller must call release once the response is finished. When the key is over
// its rate or over its heavy cap it returns a nil release and the denial,
// and nothing is counted.
func (l *keyLimiter) acquire(id string, heavy bool) (release func(), den *limitDenial) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	b, ok := l.buckets[id]
	if !ok {
		b = &keyBucket{tokens: keyBurst, last: now}
		l.buckets[id] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(keyBurst, b.tokens+elapsed*keyRate)
	}
	b.last = now
	if b.tokens < 1 {
		return nil, &limitDenial{msg: msgTooManyRequests, retry: retrySeconds((1 - b.tokens) / keyRate)}
	}
	if heavy && b.heavy >= keyHeavyMax {
		return nil, &limitDenial{msg: msgTooManyHeavy, retry: 1}
	}
	b.tokens--
	if !heavy {
		return func() {}, nil
	}
	b.heavy++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			b.heavy--
			l.mu.Unlock()
		})
	}, nil
}

// retrySeconds rounds a wait in seconds up to a whole number, at least one.
func retrySeconds(wait float64) int {
	return max(1, int(math.Ceil(wait)))
}

// pruneLocked drops entries idle for keyIdleTTL that run no heavy request. It
// sweeps at most once per keyPruneEvery. The caller holds l.mu.
func (l *keyLimiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < keyPruneEvery {
		return
	}
	l.lastPrune = now
	for id, b := range l.buckets {
		if b.heavy == 0 && now.Sub(b.last) > keyIdleTTL {
			delete(l.buckets, id)
		}
	}
}

// forget removes the entry of key id; a nil limiter does nothing. It is called
// when a key is revoked or deleted.
func (l *keyLimiter) forget(id string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.buckets, id)
	l.mu.Unlock()
}

// size returns the number of entries held, for tests.
func (l *keyLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// keyLimit refuses an API-key request that is over the key's limits.
//
// It runs after auth, so only verified keys create entries. A cookie session
// and a server without a limiter pass to next untouched. The answer is 429
// "too many requests" or, for a heavy route over its cap, 429 "too many heavy
// requests", with Retry-After in whole seconds. A heavy slot is held until next
// returns. There is no error return.
func (s *Server) keyLimit(rule routeRule, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if !sess.API || s.keyLim == nil {
			next.ServeHTTP(w, r)
			return
		}
		release, den := s.keyLim.acquire(sess.KeyID, rule.heavy)
		if den != nil {
			den.write(w)
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}
