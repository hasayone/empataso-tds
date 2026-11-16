package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// trimFixture is a miniredis with old keys of every prefix, and a trim over it.
type trimFixture struct {
	mr *miniredis.Miniredis
	t  *redisTrim
}

// newTrimFixture seeds long-lived, immortal and short-lived keys of uniq:,
// uniqo: and st:, plus keys of other prefixes that must stay untouched, and
// builds a trim with a one-day ceiling and no pause. n is the number of keys per kind.
func newTrimFixture(t *testing.T, n int) *trimFixture {
	t.Helper()
	mr := miniredis.RunT(t)
	for _, p := range []string{"uniq:g:", "uniqo:g:", "st:g:"} {
		for i := 0; i < n; i++ {
			long, never, short := fmt.Sprintf("%sl%d", p, i), fmt.Sprintf("%sn%d", p, i), fmt.Sprintf("%ss%d", p, i)
			mr.Set(long, "1")
			mr.SetTTL(long, 30*24*time.Hour)
			mr.Set(never, "1")
			mr.Set(short, "1")
			mr.SetTTL(short, time.Hour)
		}
	}
	mr.Set("sess:abc", "x")
	mr.SetTTL("sess:abc", 30*24*time.Hour)
	mr.Set("fw:g:1.1.1.1", "5")
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	tr := newRedisTrim(rdb, 24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.pause = 0
	tr.retryWait = time.Millisecond
	tr.count = 7
	return &trimFixture{mr: mr, t: tr}
}

// scanHook runs fn once, right after the first SCAN answered.
type scanHook struct {
	fn   func()
	once sync.Once
}

func (h *scanHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *scanHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "scan" {
			h.once.Do(h.fn)
		}
		return err
	}
}

func (h *scanHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// afterFirstScan makes fn run between the first SCAN step and its trim, which
// is how a test interrupts the pass at a known point. miniredis answers a SCAN
// in one step whatever COUNT says, so timing cannot do it.
func (f *trimFixture) afterFirstScan(fn func()) {
	f.t.rdb.AddHook(&scanHook{fn: fn})
}

// TestRedisTrimCapsOldKeys is the stop-test of the cleanup: long and immortal
// keys of the three prefixes end at the ceiling, shorter ones and foreign keys stay.
func TestRedisTrimCapsOldKeys(t *testing.T) {
	f := newTrimFixture(t, 25)
	f.t.Run(context.Background())

	for _, p := range []string{"uniq:g:", "uniqo:g:", "st:g:"} {
		for i := 0; i < 25; i++ {
			if ttl := f.mr.TTL(fmt.Sprintf("%sl%d", p, i)); ttl != 24*time.Hour {
				t.Fatalf("%sl%d ttl = %v, want 24h", p, i, ttl)
			}
			if ttl := f.mr.TTL(fmt.Sprintf("%sn%d", p, i)); ttl != 24*time.Hour {
				t.Fatalf("%sn%d without ttl got %v, want 24h", p, i, ttl)
			}
			if ttl := f.mr.TTL(fmt.Sprintf("%ss%d", p, i)); ttl != time.Hour {
				t.Fatalf("%ss%d short ttl changed to %v", p, i, ttl)
			}
		}
	}
	if ttl := f.mr.TTL("sess:abc"); ttl != 30*24*time.Hour {
		t.Errorf("a session key was touched: %v", ttl)
	}
	if ttl := f.mr.TTL("fw:g:1.1.1.1"); ttl != 0 {
		t.Errorf("a firewall key was touched: %v", ttl)
	}

	s := f.t.Stats()
	if !s.Done || s.Error || s.Skipped != "" {
		t.Fatalf("stats = %+v", s)
	}
	if s.Scanned != 3*3*25 || s.Trimmed != 3*2*25 {
		t.Errorf("scanned %d trimmed %d, want %d and %d", s.Scanned, s.Trimmed, 3*3*25, 3*2*25)
	}
	if f.mr.Exists(trimLockKey) {
		t.Error("the lock must be released")
	}
	if v, _ := f.mr.Get(trimDoneKey); v != "86400" {
		t.Errorf("done marker = %q", v)
	}
	if ttl := f.mr.TTL(trimDoneKey); ttl != 0 {
		t.Errorf("the done marker must have no TTL, got %v", ttl)
	}
}

// TestRedisTrimRunsOnceNotDaily: the marker outlives the ceiling, so a restart
// days later with the same ceiling does not walk the keyspace again.
func TestRedisTrimRunsOnceNotDaily(t *testing.T) {
	f := newTrimFixture(t, 3)
	f.t.Run(context.Background())
	f.mr.FastForward(72 * time.Hour)
	if !f.mr.Exists(trimDoneKey) {
		t.Fatal("the done marker expired")
	}
	f.mr.Set("uniq:g:late", "1")
	f.mr.SetTTL("uniq:g:late", 30*24*time.Hour)
	again := newRedisTrim(f.t.rdb, 24*time.Hour, f.t.log)
	again.pause = 0
	again.Run(context.Background())
	if s := again.Stats(); s.Skipped != "done_before" || s.Scanned != 0 {
		t.Fatalf("stats = %+v", s)
	}
	if ttl := f.mr.TTL("uniq:g:late"); ttl != 30*24*time.Hour {
		t.Errorf("the second pass walked the keys, ttl = %v", ttl)
	}
}

// TestRedisTrimSkipsWhenDoneBefore: the second start with the same ceiling does not scan again.
func TestRedisTrimSkipsWhenDoneBefore(t *testing.T) {
	f := newTrimFixture(t, 3)
	f.t.Run(context.Background())

	f.mr.Set("uniq:g:late", "1")
	f.mr.SetTTL("uniq:g:late", 30*24*time.Hour)
	again := newRedisTrim(f.t.rdb, 24*time.Hour, f.t.log)
	again.pause = 0
	again.Run(context.Background())
	if s := again.Stats(); s.Skipped != "done_before" || !s.Done || s.Scanned != 0 {
		t.Fatalf("stats = %+v", s)
	}
	if ttl := f.mr.TTL("uniq:g:late"); ttl != 30*24*time.Hour {
		t.Errorf("the second pass must not scan, ttl = %v", ttl)
	}

	lower := newRedisTrim(f.t.rdb, time.Hour, f.t.log)
	lower.pause = 0
	lower.Run(context.Background())
	if ttl := f.mr.TTL("uniq:g:late"); ttl != time.Hour {
		t.Errorf("a lower ceiling must run again, ttl = %v", ttl)
	}
}

// TestRedisTrimLockHeldByAnother: a held lock means no scan and no error.
func TestRedisTrimLockHeldByAnother(t *testing.T) {
	f := newTrimFixture(t, 3)
	f.mr.Set(trimLockKey, "other-engine")
	f.mr.SetTTL(trimLockKey, time.Minute)
	f.t.Run(context.Background())

	s := f.t.Stats()
	if s.Skipped != "locked" || s.Done || s.Error || s.Scanned != 0 {
		t.Fatalf("stats = %+v", s)
	}
	if ttl := f.mr.TTL("uniq:g:l0"); ttl != 30*24*time.Hour {
		t.Errorf("keys must stay untouched, ttl = %v", ttl)
	}
	if v, _ := f.mr.Get(trimLockKey); v != "other-engine" {
		t.Errorf("the other engine's lock was changed: %q", v)
	}
}

// TestRedisTrimStopsOnContext: a cancelled context ends the pass without an error flag and releases the lock.
func TestRedisTrimStopsOnContext(t *testing.T) {
	f := newTrimFixture(t, 40)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.afterFirstScan(cancel)
	f.t.Run(ctx)
	s := f.t.Stats()
	if s.Done || s.Error || s.Trimmed != 0 {
		t.Fatalf("stats = %+v, a shutdown is neither done nor an error", s)
	}
	if f.mr.Exists(trimLockKey) {
		t.Error("the lock must be released on shutdown")
	}
}

// TestRedisTrimRedisDown: a Redis failure sets the error flag and Run still returns.
func TestRedisTrimRedisDown(t *testing.T) {
	f := newTrimFixture(t, 3)
	f.mr.Close()
	f.t.Run(context.Background())
	if s := f.t.Stats(); !s.Error || s.Done {
		t.Fatalf("stats = %+v", s)
	}
}

// TestRedisTrimLostLock: a lock taken by another owner mid-pass stops the pass with the error flag.
func TestRedisTrimLostLock(t *testing.T) {
	f := newTrimFixture(t, 20)
	f.afterFirstScan(func() { f.mr.Set(trimLockKey, "thief") })
	f.t.Run(context.Background())
	if s := f.t.Stats(); !s.Error || s.Done {
		t.Fatalf("stats = %+v", s)
	}
	if v, _ := f.mr.Get(trimLockKey); v != "thief" {
		t.Errorf("the new owner's lock must stay, got %q", v)
	}
}

// TestRedisTrimNoCeiling: a non-positive ceiling does nothing.
func TestRedisTrimNoCeiling(t *testing.T) {
	f := newTrimFixture(t, 2)
	f.t.ceiling = 0
	f.t.Run(context.Background())
	if s := f.t.Stats(); s.Done || s.Scanned != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

// TestStartRedisTrimWithoutRedis: no connection means no trim and an empty snapshot.
func TestStartRedisTrimWithoutRedis(t *testing.T) {
	activeRedisTrim.Store(nil)
	if startRedisTrim(context.Background(), nil, time.Hour, nil) != nil {
		t.Fatal("a nil connection must start nothing")
	}
	if s := currentRedisTrimStats(); s != (redisTrimStats{}) {
		t.Fatalf("stats = %+v", s)
	}
}

// TestStartRedisTrimRegisters: the started trim shows up in the shared snapshot once it finishes.
func TestStartRedisTrimRegisters(t *testing.T) {
	f := newTrimFixture(t, 2)
	tr := startRedisTrim(context.Background(), f.t.rdb, 24*time.Hour, f.t.log)
	t.Cleanup(func() { activeRedisTrim.Store(nil) })
	deadline := time.Now().Add(5 * time.Second)
	for !tr.Stats().Done && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s := currentRedisTrimStats(); !s.Done || s.Trimmed != 3*2*2 {
		t.Fatalf("stats = %+v", s)
	}
}
