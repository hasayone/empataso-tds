package cron

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/store"
)

// Redis memory alert defaults. Redis runs with noeviction: when maxmemory is
// reached, new keys are refused, the engine fails open and every click counts
// as unique while antiflood and stream limits stop working.
const (
	DefaultRedisAlertPercent   = 80
	DefaultRedisRecoverPercent = 70
	DefaultRedisAlertRepeat    = 6 * time.Hour
	// redisCheckEvery is the period of the memory check.
	redisCheckEvery = time.Minute
	// redisCheckTimeout bounds one INFO call.
	redisCheckTimeout = 5 * time.Second
	// RedisStateFresh is how old a check may be and still raise the admin notice;
	// an older one means the service has stopped checking.
	RedisStateFresh = 10 * time.Minute
)

// RedisMemSource reads Redis memory figures. *store.RedisProbe implements it.
type RedisMemSource interface {
	Memory(ctx context.Context) (store.RedisMemory, error)
}

// RedisAlert configures the Redis memory alert.
// Percent raises the alert at or above that share of maxmemory; Recover
// clears it below that share. Repeat reminds while the alert stays raised;
// 0 never reminds. Lang is the Telegram language: ru, uk or en.
type RedisAlert struct {
	Percent int
	Recover int
	Repeat  time.Duration
	Lang    string
}

// RedisAlertFromEnv returns the alert settings from EMTDS_REDIS_ALERT_PERCENT
// (1..100), EMTDS_REDIS_RECOVER_PERCENT (below the alert share),
// EMTDS_REDIS_ALERT_REPEAT (Go duration, 0 disables reminders) and
// EMTDS_ALERT_LANG (ru, uk or en). An empty or invalid value keeps the default;
// a recover share at or above the alert share becomes ten points below it.
// It does not fail.
func RedisAlertFromEnv() RedisAlert {
	a := RedisAlert{Percent: DefaultRedisAlertPercent, Recover: DefaultRedisRecoverPercent, Repeat: DefaultRedisAlertRepeat, Lang: "en"}
	if n, err := strconv.Atoi(config.Env("EMTDS_REDIS_ALERT_PERCENT")); err == nil && n >= 1 && n <= 100 {
		a.Percent = n
	}
	if n, err := strconv.Atoi(config.Env("EMTDS_REDIS_RECOVER_PERCENT")); err == nil && n >= 0 {
		a.Recover = n
	}
	if a.Recover >= a.Percent {
		a.Recover = max(0, a.Percent-10)
	}
	if v := strings.TrimSpace(config.Env("EMTDS_REDIS_ALERT_REPEAT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			a.Repeat = d
		}
	}
	if l := strings.ToLower(strings.TrimSpace(config.Env("EMTDS_ALERT_LANG"))); redisTexts[l] != nil {
		a.Lang = l
	}
	return a
}

// RedisMemState is the last Redis memory check, kept in the status file.
// Alerted stays true from the alert until memory falls below the recover share.
type RedisMemState struct {
	CheckedAt time.Time `json:"checked_at"`
	UsedBytes uint64    `json:"used_bytes"`
	MaxBytes  uint64    `json:"max_bytes"`
	Percent   int       `json:"percent"`
	OOMErrors uint64    `json:"oom_errors"`
	Rejected  uint64    `json:"rejected_connections"`
	Alerted   bool      `json:"alerted"`
	AlertedAt time.Time `json:"alerted_at,omitempty"`
}

// The Redis alert speaks the verdicts shared by every alert.
type redisVerdict = alertVerdict

const (
	redisQuiet  = alertQuiet
	redisRaise  = alertRaise
	redisRemind = alertRemind
	redisClear  = alertClear
)

// decideRedis computes the state after one check and what to send.
// prev is the last state, nil before the first check. m is the reading and
// now its time. maxmemory 0 means no limit: the state is cleared and nothing
// is sent. Otherwise decideAlert makes the decision: hot at or above the alert
// share, calm below the recover share. It does not fail.
func decideRedis(prev *RedisMemState, m store.RedisMemory, a RedisAlert, now time.Time) (RedisMemState, redisVerdict) {
	next := RedisMemState{CheckedAt: now, UsedBytes: m.Used, MaxBytes: m.Max, OOMErrors: m.OOMErrors, Rejected: m.Rejected}
	if m.Max == 0 {
		return next, redisQuiet
	}
	next.Percent = int(m.Used * 100 / m.Max)
	var flag alertFlag
	if prev != nil {
		flag = alertFlag{On: prev.Alerted, At: prev.AlertedAt}
	}
	flag, v := decideAlert(flag, next.Percent >= a.Percent, next.Percent < a.Recover, a.Repeat, now)
	next.Alerted, next.AlertedAt = flag.On, flag.At
	return next, v
}

// redisText is one language of the Telegram messages.
type redisText struct{ raise, clear, counters, of string }

// redisTexts holds the Telegram messages by language.
var redisTexts = map[string]*redisText{
	"en": {
		raise: "⚠️ EmTDS: Redis memory is %d%% full (%s). At 100%% new keys are refused: every click counts as unique, " +
			"antiflood and stream limits stop working. Raise maxmemory or free memory.",
		clear:    "✅ EmTDS: Redis memory is back to %d%% (%s).",
		counters: " OOM errors: %d, rejected connections: %d.",
		of:       "of",
	},
	"ru": {
		raise: "⚠️ EmTDS: память Redis заполнена на %d%% (%s). При 100%% новые ключи не пишутся: каждый клик считается " +
			"уникальным, антифлуд и лимиты потоков перестают работать. Увеличьте maxmemory или освободите память.",
		clear:    "✅ EmTDS: память Redis снова %d%% (%s).",
		counters: " Ошибок OOM: %d, отклонённых подключений: %d.",
		of:       "из",
	},
	"uk": {
		raise: "⚠️ EmTDS: пам'ять Redis заповнена на %d%% (%s). При 100%% нові ключі не записуються: кожен клік " +
			"рахується унікальним, антифлуд і ліміти потоків перестають працювати. Збільште maxmemory або звільніть пам'ять.",
		clear:    "✅ EmTDS: пам'ять Redis знову %d%% (%s).",
		counters: " Помилок OOM: %d, відхилених підключень: %d.",
		of:       "з",
	},
}

// redisMessage returns the Telegram text for v in lang, or "" for redisQuiet.
// An unknown lang is English. It does not fail.
func redisMessage(v redisVerdict, st RedisMemState, lang string) string {
	t := redisTexts[lang]
	if t == nil {
		t = redisTexts["en"]
	}
	size := mib(st.UsedBytes) + " " + t.of + " " + mib(st.MaxBytes)
	var msg string
	switch v {
	case redisRaise, redisRemind:
		msg = fmt.Sprintf(t.raise, st.Percent, size)
	case redisClear:
		msg = fmt.Sprintf(t.clear, st.Percent, size)
	default:
		return ""
	}
	if st.OOMErrors > 0 || st.Rejected > 0 {
		msg += fmt.Sprintf(t.counters, st.OOMErrors, st.Rejected)
	}
	return msg
}

// mib formats a byte count in whole MiB.
func mib(b uint64) string { return fmt.Sprintf("%d MiB", b>>20) }

// RedisNotice returns the admin notice line for a raised Redis memory alert.
// st is the cron status and now the current time. ok is false when no alert
// is raised or the last check is older than RedisStateFresh. The line is
// "<RFC3339 time> redis memory <N>% (<used> MiB of <max> MiB)"; the admin
// panel parses it for its banner.
func RedisNotice(st Status, now time.Time) (line string, ok bool) {
	r := st.Redis
	if r == nil || !r.Alerted || r.MaxBytes == 0 || now.Sub(r.CheckedAt) > RedisStateFresh {
		return "", false
	}
	return fmt.Sprintf("%s redis memory %d%% (%s of %s)", r.CheckedAt.UTC().Format(time.RFC3339), r.Percent,
		mib(r.UsedBytes), mib(r.MaxBytes)), true
}

// WatchRedis turns on the Redis memory check. src reads the figures and a
// sets the thresholds. Call it before Run; a nil src leaves the check off.
func (r *Runner) WatchRedis(src RedisMemSource, a RedisAlert) {
	r.redisSrc, r.redisAlert = src, a
}

// maybeCheckRedis starts one memory check when it is due and none is running.
// t is where alerts go. The check runs in its own goroutine.
func (r *Runner) maybeCheckRedis(ctx context.Context, t Telegram) {
	if r.redisSrc == nil {
		return
	}
	r.launchCheck(&r.redisNext, &r.redisBusy, redisCheckEvery, func() { r.checkRedis(ctx, t) })
}

// checkRedis reads Redis memory once, stores the state and sends what
// decideRedis asks for. A failed read is logged and leaves the state as it was.
func (r *Runner) checkRedis(ctx context.Context, t Telegram) {
	cctx, cancel := context.WithTimeout(ctx, redisCheckTimeout)
	m, err := r.redisSrc.Memory(cctx)
	cancel()
	if err != nil {
		r.log.Warn("cron: redis memory not read", "err", err)
		return
	}
	var next RedisMemState
	var v redisVerdict
	r.state(func(st *Status) {
		next, v = decideRedis(st.Redis, m, r.redisAlert, r.now())
		st.Redis = &next
	})
	r.save()
	if msg := redisMessage(v, next, r.redisAlert.Lang); msg != "" {
		r.log.Warn("cron: redis memory alert", "percent", next.Percent, "alerted", next.Alerted)
		if suffix := r.notifyIfConfigured(ctx, t, msg); suffix != "" {
			r.log.Info("cron: redis memory alert not delivered" + suffix)
		}
	}
}
