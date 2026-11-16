package cron

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/store"
)

// fakeRedisMem returns the reading it holds, or err.
type fakeRedisMem struct {
	mu  sync.Mutex
	m   store.RedisMemory
	err error
}

func (f *fakeRedisMem) Memory(context.Context) (store.RedisMemory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m, f.err
}

func (f *fakeRedisMem) set(usedMB, maxMB uint64) {
	f.mu.Lock()
	f.m = store.RedisMemory{Used: usedMB << 20, Max: maxMB << 20}
	f.mu.Unlock()
}

var testRedisAlert = RedisAlert{Percent: 80, Recover: 70, Repeat: 6 * time.Hour, Lang: "en"}

func TestDecideRedisThresholds(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	mem := func(used uint64) store.RedisMemory { return store.RedisMemory{Used: used, Max: 100} }
	st, v := decideRedis(nil, mem(79), testRedisAlert, now)
	if v != redisQuiet || st.Alerted || st.Percent != 79 {
		t.Fatalf("below threshold: %+v %v", st, v)
	}
	st, v = decideRedis(&st, mem(80), testRedisAlert, now)
	if v != redisRaise || !st.Alerted || !st.AlertedAt.Equal(now) {
		t.Fatalf("at threshold: %+v %v", st, v)
	}
	st, v = decideRedis(&st, mem(95), testRedisAlert, now.Add(time.Hour))
	if v != redisQuiet || !st.Alerted {
		t.Fatalf("still high within repeat: %+v %v", st, v)
	}
	st, v = decideRedis(&st, mem(75), testRedisAlert, now.Add(2*time.Hour))
	if v != redisQuiet || !st.Alerted {
		t.Fatalf("between recover and alert keeps the alert: %+v %v", st, v)
	}
	st, v = decideRedis(&st, mem(90), testRedisAlert, now.Add(6*time.Hour))
	if v != redisRemind || !st.AlertedAt.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("reminder after repeat: %+v %v", st, v)
	}
	st, v = decideRedis(&st, mem(69), testRedisAlert, now.Add(7*time.Hour))
	if v != redisClear || st.Alerted {
		t.Fatalf("recovery: %+v %v", st, v)
	}
	st, v = decideRedis(&st, mem(81), testRedisAlert, now.Add(8*time.Hour))
	if v != redisRaise {
		t.Fatalf("a new rise after recovery alerts again: %+v %v", st, v)
	}
	noRepeat := testRedisAlert
	noRepeat.Repeat = 0
	if _, v = decideRedis(&st, mem(99), noRepeat, now.Add(100*time.Hour)); v != redisQuiet {
		t.Fatalf("repeat 0 never reminds, got %v", v)
	}
	st, v = decideRedis(&st, store.RedisMemory{Used: 1 << 30}, testRedisAlert, now)
	if v != redisQuiet || st.Alerted || st.Percent != 0 {
		t.Fatalf("maxmemory 0 is skipped: %+v %v", st, v)
	}
}

func TestRedisMessages(t *testing.T) {
	st := RedisMemState{Percent: 85, UsedBytes: 435 << 20, MaxBytes: 512 << 20, OOMErrors: 3}
	for lang, want := range map[string]string{"en": "435 MiB of 512 MiB", "ru": "435 MiB из 512 MiB", "uk": "435 MiB з 512 MiB", "xx": "of"} {
		msg := redisMessage(redisRaise, st, lang)
		if !strings.Contains(msg, "85%") || !strings.Contains(msg, want) {
			t.Errorf("%s raise: %q", lang, msg)
		}
		if !strings.Contains(msg, "OOM") || strings.Contains(msg, "%!") {
			t.Errorf("%s counters: %q", lang, msg)
		}
		if clear := redisMessage(redisClear, st, lang); clear == "" || strings.Contains(clear, "%!") {
			t.Errorf("%s clear: %q", lang, clear)
		}
	}
	if redisMessage(redisQuiet, st, "en") != "" {
		t.Error("quiet sends nothing")
	}
}

func TestRedisAlertFromEnv(t *testing.T) {
	t.Setenv("EMTDS_REDIS_ALERT_PERCENT", "90")
	t.Setenv("EMTDS_REDIS_RECOVER_PERCENT", "95")
	t.Setenv("EMTDS_REDIS_ALERT_REPEAT", "0")
	t.Setenv("EMTDS_ALERT_LANG", "RU")
	a := RedisAlertFromEnv()
	if a.Percent != 90 || a.Recover != 80 || a.Repeat != 0 || a.Lang != "ru" {
		t.Fatalf("got %+v", a)
	}
	t.Setenv("EMTDS_REDIS_ALERT_PERCENT", "500")
	t.Setenv("EMTDS_REDIS_RECOVER_PERCENT", "x")
	t.Setenv("EMTDS_REDIS_ALERT_REPEAT", "soon")
	t.Setenv("EMTDS_ALERT_LANG", "de")
	a = RedisAlertFromEnv()
	want := RedisAlert{Percent: DefaultRedisAlertPercent, Recover: DefaultRedisRecoverPercent, Repeat: DefaultRedisAlertRepeat, Lang: "en"}
	if a != want {
		t.Fatalf("invalid values: %+v, want %+v", a, want)
	}
}

// TestRedisCheckSendsOnceAndRecovers drives the runner: one Telegram message
// on the way up, none while it stays high, one on recovery, and the state
// with the admin notice in the status file.
func TestRedisCheckSendsOnceAndRecovers(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }
	src := &fakeRedisMem{}
	e.r.WatchRedis(src, testRedisAlert)
	check := func() {
		e.r.checkRedis(context.Background(), tg)
	}
	src.set(300, 512)
	check()
	src.set(450, 512)
	check()
	check()
	check()
	if m := e.messages(); len(m) != 1 || !strings.Contains(m[0], "Redis memory is 87% full") {
		t.Fatalf("high memory must alert once: %q", m)
	}
	st, err := ReadStatus(StatusPath(e.r.paths.Config))
	if err != nil {
		t.Fatal(err)
	}
	line, ok := RedisNotice(st, now)
	if !ok || !strings.HasSuffix(line, "redis memory 87% (450 MiB of 512 MiB)") {
		t.Fatalf("notice = %q %v", line, ok)
	}
	if _, ok := RedisNotice(st, now.Add(RedisStateFresh+time.Minute)); ok {
		t.Fatal("a stale check must not raise the notice")
	}
	src.set(100, 512)
	check()
	check()
	if m := e.messages(); len(m) != 2 || !strings.Contains(m[1], "back to 19%") {
		t.Fatalf("recovery must be announced once: %q", m)
	}
	st, _ = ReadStatus(StatusPath(e.r.paths.Config))
	if _, ok := RedisNotice(st, now); ok {
		t.Fatal("no notice after recovery")
	}
	src.mu.Lock()
	src.err = errors.New("down")
	src.mu.Unlock()
	check()
	st2, _ := ReadStatus(StatusPath(e.r.paths.Config))
	if st2.Redis == nil || st2.Redis.Percent != 19 {
		t.Fatalf("a failed read keeps the last state: %+v", st2.Redis)
	}
}

// TestRedisCheckSchedule checks that Tick runs the check at most once a
// minute and not at all without a source.
func TestRedisCheckSchedule(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }
	e.r.Tick(context.Background())
	e.r.Wait()
	if st, _ := ReadStatus(StatusPath(e.r.paths.Config)); st.Redis != nil {
		t.Fatal("no source, no check")
	}
	src := &fakeRedisMem{}
	src.set(10, 100)
	e.r.WatchRedis(src, testRedisAlert)
	e.r.Tick(context.Background())
	e.r.Wait()
	src.set(20, 100)
	e.r.Tick(context.Background())
	e.r.Wait()
	st, _ := ReadStatus(StatusPath(e.r.paths.Config))
	if st.Redis == nil || st.Redis.Percent != 10 {
		t.Fatalf("second tick within a minute must not check: %+v", st.Redis)
	}
	now = now.Add(redisCheckEvery)
	e.r.Tick(context.Background())
	e.r.Wait()
	st, _ = ReadStatus(StatusPath(e.r.paths.Config))
	if st.Redis == nil || st.Redis.Percent != 20 {
		t.Fatalf("check after a minute: %+v", st.Redis)
	}
}
