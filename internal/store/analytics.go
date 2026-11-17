package store

import (
	"context"
	"errors"
	"maps"
	"math"
	"runtime"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/hasayone/empataso-tds/internal/config"
)

// Admin analytics defaults. A dashboard visit starts several scans of events
// at once; the limits stop a runaway scan and keep CPU and memory for the
// engine's inserts. They never apply to inserts.
const (
	// DefaultAnalyticsMaxExecution stays under the 60s nginx default of the
	// bare-IP admin vhost, so the user gets a clear answer instead of a 504 page.
	DefaultAnalyticsMaxExecution = 55 * time.Second
	// DefaultAnalyticsPriority is below insertPriority (a higher number is a lower priority).
	DefaultAnalyticsPriority = 10
	// DefaultAnalyticsMaxMemory is the per-query memory cap; GROUP BY and ORDER BY
	// spill to disk at half of it, so a large period gets slower, not refused.
	DefaultAnalyticsMaxMemory int64 = 1 << 30
	// insertPriority marks event inserts: ClickHouse ignores priority 0, and
	// priority 1 pauses lower-priority analytics while an insert runs.
	insertPriority = 1
	// valuesMaxExecution bounds the filter value suggestions.
	valuesMaxExecution = 10 * time.Second
	// journalSortMaxExecution bounds a journal page sorted off the primary key.
	journalSortMaxExecution = 20 * time.Second
)

// AnalyticsLimits bounds the admin's statistics queries.
// MaxExecution is max_execution_time; Priority is the ClickHouse priority;
// MaxThreads is max_threads; MaxMemory is max_memory_usage in bytes. A zero
// field leaves that setting at the server default.
type AnalyticsLimits struct {
	MaxExecution time.Duration
	Priority     int
	MaxThreads   int
	MaxMemory    int64
}

// DefaultAnalyticsLimits returns the limits used when no variable overrides them.
// MaxThreads is half of the host CPUs, at least one: ClickHouse runs on the same host.
// It does not fail.
func DefaultAnalyticsLimits() AnalyticsLimits {
	return AnalyticsLimits{
		MaxExecution: DefaultAnalyticsMaxExecution,
		Priority:     DefaultAnalyticsPriority,
		MaxThreads:   max(1, runtime.NumCPU()/2),
		MaxMemory:    DefaultAnalyticsMaxMemory,
	}
}

// AnalyticsLimitsFromEnv returns DefaultAnalyticsLimits with each field replaced by
// EMTDS_CH_ANALYTICS_MAX_EXECUTION (Go duration), EMTDS_CH_ANALYTICS_PRIORITY,
// EMTDS_CH_ANALYTICS_MAX_THREADS or EMTDS_CH_ANALYTICS_MAX_MEMORY_MB when set.
// A duration must be positive; a number may be 0 to leave the server default.
// A malformed or negative value keeps the default. It does not fail.
func AnalyticsLimitsFromEnv() AnalyticsLimits {
	l := DefaultAnalyticsLimits()
	l.MaxExecution = envDuration("EMTDS_CH_ANALYTICS_MAX_EXECUTION", l.MaxExecution)
	l.Priority = envNonNegative("EMTDS_CH_ANALYTICS_PRIORITY", l.Priority)
	l.MaxThreads = envNonNegative("EMTDS_CH_ANALYTICS_MAX_THREADS", l.MaxThreads)
	l.MaxMemory = int64(envNonNegative("EMTDS_CH_ANALYTICS_MAX_MEMORY_MB", int(l.MaxMemory>>20))) << 20
	return l
}

// envNonNegative reads key through config.Env as a whole number.
// def is returned when the value is empty, malformed or negative.
func envNonNegative(key string, def int) int {
	n, err := strconv.Atoi(config.Env(key))
	if err != nil || n < 0 {
		return def
	}
	return n
}

// SetAnalyticsLimits replaces the limits of the statistics queries.
// Call it before the client serves requests.
func (c *CH) SetAnalyticsLimits(l AnalyticsLimits) {
	if c != nil {
		c.limits = l
	}
}

// settings returns the ClickHouse settings for one analytics query.
// maxExec, when positive and tighter than MaxExecution, replaces it.
// An empty map means every setting stays at the server default.
func (l AnalyticsLimits) settings(maxExec time.Duration) clickhouse.Settings {
	s := clickhouse.Settings{}
	exec := l.MaxExecution
	if maxExec > 0 && (exec <= 0 || maxExec < exec) {
		exec = maxExec
	}
	if exec > 0 {
		s["max_execution_time"] = int(math.Ceil(exec.Seconds()))
	}
	if l.Priority > 0 {
		s["priority"] = l.Priority
	}
	if l.MaxThreads > 0 {
		s["max_threads"] = l.MaxThreads
	}
	if l.MaxMemory > 0 {
		s["max_memory_usage"] = l.MaxMemory
		s["max_bytes_before_external_group_by"] = l.MaxMemory / 2
		s["max_bytes_before_external_sort"] = l.MaxMemory / 2
	}
	return s
}

// analyticsKey marks a context that already carries the analytics settings.
type analyticsKey struct{}

// analyticsCtx is the one place the statistics queries get their limits.
// ctx is the request context. maxExec is 0 for the configured ceiling or a
// tighter per-query ceiling. A ctx already wrapped with maxExec 0 is returned
// as is, so nested calls do not reset a tighter limit.
func (c *CH) analyticsCtx(ctx context.Context, maxExec time.Duration) context.Context {
	return c.analyticsCtxWith(ctx, maxExec, nil)
}

// analyticsCtxWith is analyticsCtx with extra ClickHouse settings added to the limits.
// extra wins over a limit of the same name. The driver replaces the whole settings map
// of a context, so the limits and extra travel in one map, also readable by
// analyticsSettingsFrom. With empty extra it is exactly analyticsCtx.
func (c *CH) analyticsCtxWith(ctx context.Context, maxExec time.Duration, extra clickhouse.Settings) context.Context {
	if _, ok := analyticsSettingsFrom(ctx); ok && maxExec <= 0 && len(extra) == 0 {
		return ctx
	}
	s := c.limits.settings(maxExec)
	maps.Copy(s, extra)
	ctx = context.WithValue(ctx, analyticsKey{}, s)
	if len(s) == 0 {
		return ctx
	}
	return clickhouse.Context(ctx, clickhouse.WithSettings(s))
}

// analyticsSettingsFrom returns the settings analyticsCtx put on ctx.
// ok is false when ctx was not wrapped.
func analyticsSettingsFrom(ctx context.Context) (clickhouse.Settings, bool) {
	s, ok := ctx.Value(analyticsKey{}).(clickhouse.Settings)
	return s, ok
}

// insertSettingsKey marks a context that carries extra settings for one insert.
type insertSettingsKey struct{}

// withInsertSettings returns ctx carrying settings that insertCtx adds to the
// insert it prepares. The settings belong to the derived context only: a
// context built from the original ctx, as the conversions insert does, does not
// see them. extra wins over the insert priority of the same name.
func withInsertSettings(ctx context.Context, extra clickhouse.Settings) context.Context {
	return context.WithValue(ctx, insertSettingsKey{}, extra)
}

// insertSettings returns the settings an insert on ctx runs with: the insert
// priority and whatever withInsertSettings put on ctx. A fresh map on every call.
func insertSettings(ctx context.Context) clickhouse.Settings {
	s := clickhouse.Settings{"priority": insertPriority}
	if extra, ok := ctx.Value(insertSettingsKey{}).(clickhouse.Settings); ok {
		maps.Copy(s, extra)
	}
	return s
}

// insertCtx gives an insert the priority that pauses analytics while it runs,
// together with the settings withInsertSettings put on ctx. The driver replaces
// the whole settings map of a context, so both travel in one map.
func insertCtx(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(insertSettings(ctx)))
}

// ClickHouse error codes of a query stopped by its limits.
const (
	chTimeoutExceeded     = 159
	chTooSlow             = 160
	chMemoryLimitExceeded = 241
)

// IsQueryTooHeavy reports whether err is a statistics query stopped by its
// time or memory limit, or by the context deadline.
func IsQueryTooHeavy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ex *clickhouse.Exception
	if !errors.As(err, &ex) {
		return false
	}
	switch ex.Code {
	case chTimeoutExceeded, chTooSlow, chMemoryLimitExceeded:
		return true
	}
	return false
}
