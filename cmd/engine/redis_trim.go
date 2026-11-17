package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/redis/go-redis/v9"
)

const (
	// trimLockKey is held with SET NX while one engine trims old keys, so that
	// several engines starting together do not scan the same keyspace.
	trimLockKey = "trim:lock"
	// trimDoneKey remembers the ceiling the keyspace was last trimmed to. It has no
	// TTL: the pass runs once per ceiling value, not once per day, and the stored
	// value makes a changed EMTDS_UNIQ_MAX_SECONDS start a new pass.
	trimDoneKey = "trim:done"
	// trimLockTTL is the lifetime of the lock; every batch renews it.
	trimLockTTL = 60 * time.Second
	// trimScanCount is the COUNT hint of one SCAN step, small to keep each Redis call short.
	trimScanCount = 200
	// trimPause is the wait between two steps, so the trim never competes with clicks.
	trimPause = 25 * time.Millisecond
	// trimAttempts is how many times one step is tried before the pass stops. The
	// pass shares the engine's Redis client, so the read timeout of that client
	// (200 ms, store.EngineRedisReadTimeout) is the only limit of one call; a slow
	// answer is repeated, not waited for.
	trimAttempts = 3
	// trimRetryWait is the wait before a repeated step.
	trimRetryWait = 500 * time.Millisecond
)

// trimPatterns are the SCAN patterns of the keys that carry a uniqueness window:
// first-step uniqueness, offer-step uniqueness and the sticky stream.
var trimPatterns = []string{"uniq:*", "uniqo:*", "st:*"}

// trimScript caps the lifetime of every key in KEYS at ARGV[1] milliseconds: a
// key with no TTL or a longer one gets PEXPIRE, a shorter or missing one is
// left alone. Check and change are one step, so a key rewritten with a short
// window between SCAN and here keeps it. It returns the number of keys changed.
var trimScript = redis.NewScript(`
local n = 0
local max = tonumber(ARGV[1])
for i = 1, #KEYS do
  local t = redis.call('PTTL', KEYS[i])
  if t == -1 or t > max then
    redis.call('PEXPIRE', KEYS[i], max)
    n = n + 1
  end
end
return n
`)

// trimRenewScript extends the lock only while this engine still owns it.
// KEYS[1] is the lock, ARGV[1] the owner token, ARGV[2] the new TTL in milliseconds.
// It returns 1 when extended and 0 when the lock is gone or taken by another.
var trimRenewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// trimReleaseScript deletes the lock only while this engine still owns it.
// KEYS[1] is the lock and ARGV[1] the owner token. It returns the number of keys deleted.
var trimReleaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// redisTrimStats is the snapshot of the trim, in the form of the metrics file.
type redisTrimStats = config.RedisTrimMetrics

// redisTrim walks uniq:, uniqo: and st: keys once and caps their TTL at ceiling.
// The counters are atomics: Stats may be called while Run works.
type redisTrim struct {
	rdb     redis.UniversalClient
	ceiling time.Duration
	log     *slog.Logger
	count   int64
	pause   time.Duration
	owner   string

	retryWait time.Duration

	scanned atomic.Int64
	trimmed atomic.Int64
	done    atomic.Bool
	failed  atomic.Bool
	skipped atomic.Value // string
}

// activeRedisTrim is the trim started by startRedisTrim, nil without Redis.
var activeRedisTrim atomic.Pointer[redisTrim]

// startRedisTrim starts the one-time trim in the background and registers it for
// currentRedisTrimStats.
//
// ctx stops the walk (engine shutdown). rdb is the connection of the engine's
// counters, ceiling the lifetime cap from store.UniqMaxFromEnv, log receives the
// progress lines. It returns the trim; with a nil rdb it starts nothing and returns nil.
func startRedisTrim(ctx context.Context, rdb redis.UniversalClient, ceiling time.Duration, log *slog.Logger) *redisTrim {
	if rdb == nil {
		return nil
	}
	t := newRedisTrim(rdb, ceiling, log)
	activeRedisTrim.Store(t)
	go t.Run(ctx)
	return t
}

// currentRedisTrimStats returns the snapshot of the started trim, or the zero
// value when Redis is not connected. It is the section "redis_trim" of the metrics.
func currentRedisTrimStats() redisTrimStats {
	if t := activeRedisTrim.Load(); t != nil {
		return t.Stats()
	}
	return redisTrimStats{}
}

// newRedisTrim builds a trim of rdb with the default step and pause.
// ceiling is the lifetime cap; a non-positive one makes Run do nothing.
func newRedisTrim(rdb redis.UniversalClient, ceiling time.Duration, log *slog.Logger) *redisTrim {
	if log == nil {
		log = slog.Default()
	}
	return &redisTrim{
		rdb: rdb, ceiling: ceiling, log: log,
		count: trimScanCount, pause: trimPause, owner: trimOwner(), retryWait: trimRetryWait,
	}
}

// trimOwner returns a random token that names this engine as the lock owner.
// If the system random source fails it falls back to the clock, which is enough
// to tell two engines of one host apart.
func trimOwner() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// Stats returns the progress so far. It is safe to call from any goroutine.
func (t *redisTrim) Stats() redisTrimStats {
	s, _ := t.skipped.Load().(string)
	return redisTrimStats{
		Scanned: t.scanned.Load(), Trimmed: t.trimmed.Load(),
		Done: t.done.Load(), Error: t.failed.Load(), Skipped: s,
	}
}

// Run does one pass and returns when it is finished, skipped or ctx is done.
//
// It skips when the done marker already names this ceiling, or when another
// engine holds the lock. A Redis error or a lost lock stops the pass and sets the
// error flag; a cancelled ctx stops it silently. The lock is released on the way out.
func (t *redisTrim) Run(ctx context.Context) {
	if t.ceiling <= 0 {
		return
	}
	want := strconv.FormatInt(int64(t.ceiling/time.Second), 10)
	if v, err := t.rdb.Get(ctx, trimDoneKey).Result(); err == nil && v == want {
		t.skipped.Store("done_before")
		t.done.Store(true)
		return
	}
	got, err := t.rdb.SetNX(ctx, trimLockKey, t.owner, trimLockTTL).Result()
	if err != nil {
		t.fail(ctx, "lock", err)
		return
	}
	if !got {
		t.skipped.Store("locked")
		t.log.Info("redis trim: another engine holds the lock, skipping")
		return
	}
	defer func() {
		_ = trimReleaseScript.Run(context.WithoutCancel(ctx), t.rdb, []string{trimLockKey}, t.owner).Err()
	}()

	t.log.Info("redis trim: started", "ceiling_seconds", int64(t.ceiling/time.Second))
	for _, pattern := range trimPatterns {
		if err := t.walk(ctx, pattern); err != nil {
			t.fail(ctx, pattern, err)
			return
		}
	}
	if err := t.rdb.Set(ctx, trimDoneKey, want, 0).Err(); err != nil {
		t.fail(ctx, "marker", err)
		return
	}
	t.done.Store(true)
	t.log.Info("redis trim: finished", "scanned", t.scanned.Load(), "trimmed", t.trimmed.Load())
}

// fail records a stopped pass. A cancelled ctx is a shutdown, not an error.
// what names the step for the log, err is the cause.
func (t *redisTrim) fail(ctx context.Context, what string, err error) {
	if ctx.Err() != nil {
		return
	}
	t.failed.Store(true)
	t.log.Warn("redis trim: stopped", "step", what, "err", err,
		"scanned", t.scanned.Load(), "trimmed", t.trimmed.Load())
}

// errTrimLockLost reports that the lock expired or was taken during the pass.
var errTrimLockLost = errors.New("trim lock lost")

// walk scans one pattern to its end, lowering the TTL of each batch.
// ctx stops it between steps. It returns the first Redis error, errTrimLockLost,
// or ctx.Err(); a finished pattern returns nil.
func (t *redisTrim) walk(ctx context.Context, pattern string) error {
	var cursor uint64
	for {
		keys, next, err := t.scanStep(ctx, cursor, pattern)
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			n, err := t.trimBatch(ctx, keys)
			if err != nil {
				return err
			}
			t.scanned.Add(int64(len(keys)))
			t.trimmed.Add(n)
		}
		if err := t.renew(ctx); err != nil {
			return err
		}
		if next == 0 {
			return nil
		}
		cursor = next
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(t.pause):
		}
	}
}

// scanStep runs one SCAN call, repeated by retry when it fails.
// It returns the keys of the step and the next cursor (0 at the end).
func (t *redisTrim) scanStep(ctx context.Context, cursor uint64, pattern string) (keys []string, next uint64, err error) {
	err = t.retry(ctx, func(cctx context.Context) (e error) {
		keys, next, e = t.rdb.Scan(cctx, cursor, pattern, t.count).Result()
		return e
	})
	return keys, next, err
}

// trimBatch caps the TTL of keys at the ceiling and returns how many changed.
func (t *redisTrim) trimBatch(ctx context.Context, keys []string) (n int64, err error) {
	err = t.retry(ctx, func(cctx context.Context) (e error) {
		n, e = trimScript.Run(cctx, t.rdb, keys, t.ceiling.Milliseconds()).Int64()
		return e
	})
	return n, err
}

// retry runs op up to trimAttempts times, waiting t.retryWait between tries.
// The engine's client has a short read timeout meant for clicks (200 ms), so one
// slow answer must not end a pass that is safe to repeat.
// ctx stops the waiting. It returns nil on the first success or the last error.
func (t *redisTrim) retry(ctx context.Context, op func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < trimAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(t.retryWait):
			}
		}
		err = op(ctx)
		if err == nil {
			return nil
		}
	}
	return err
}

// renew extends the lock; it returns errTrimLockLost when another owner has it.
func (t *redisTrim) renew(ctx context.Context) error {
	n, err := trimRenewScript.Run(ctx, t.rdb, []string{trimLockKey}, t.owner, trimLockTTL.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if n == 0 {
		return errTrimLockLost
	}
	return nil
}
