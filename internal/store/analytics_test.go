package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

var errFakeConn = errors.New("fake conn")

// recordConn is a driver.Conn that records the context of every read and
// async insert and fails them. Other methods are not implemented.
type recordConn struct {
	driver.Conn
	mu        sync.Mutex
	ctxs      []context.Context
	asyncWait []bool
}

func (f *recordConn) note(ctx context.Context) {
	f.mu.Lock()
	f.ctxs = append(f.ctxs, ctx)
	f.mu.Unlock()
}

func (f *recordConn) Query(ctx context.Context, _ string, _ ...any) (driver.Rows, error) {
	f.note(ctx)
	return nil, errFakeConn
}

func (f *recordConn) QueryRow(ctx context.Context, _ string, _ ...any) driver.Row {
	f.note(ctx)
	return errRow{}
}

func (f *recordConn) Select(ctx context.Context, _ any, _ string, _ ...any) error {
	f.note(ctx)
	return errFakeConn
}

func (f *recordConn) AsyncInsert(ctx context.Context, _ string, wait bool, _ ...any) error {
	f.note(ctx)
	f.mu.Lock()
	f.asyncWait = append(f.asyncWait, wait)
	f.mu.Unlock()
	return nil
}

// errRow is a driver.Row whose every read fails with errFakeConn.
type errRow struct{}

func (errRow) Err() error           { return errFakeConn }
func (errRow) Scan(...any) error    { return errFakeConn }
func (errRow) ScanStruct(any) error { return errFakeConn }

func testLimits() AnalyticsLimits {
	return AnalyticsLimits{MaxExecution: 55 * time.Second, Priority: 10, MaxThreads: 4, MaxMemory: 1 << 30}
}

func newRecordCH(t *testing.T) (*CH, *recordConn) {
	t.Helper()
	f := &recordConn{}
	c := &CH{conn: f, limits: testLimits()}
	c.originEvents.Store(true)
	c.originConv.Store(true)
	c.rootEvents.Store(true)
	c.rootConv.Store(true)
	return c, f
}

func TestAnalyticsSettings(t *testing.T) {
	s := testLimits().settings(0)
	want := clickhouse.Settings{
		"max_execution_time":                 55,
		"priority":                           10,
		"max_threads":                        4,
		"max_memory_usage":                   int64(1 << 30),
		"max_bytes_before_external_group_by": int64(1 << 29),
		"max_bytes_before_external_sort":     int64(1 << 29),
	}
	if fmt.Sprint(s) != fmt.Sprint(want) {
		t.Fatalf("settings = %v, want %v", s, want)
	}
	if got := testLimits().settings(10 * time.Second)["max_execution_time"]; got != 10 {
		t.Fatalf("tighter ceiling = %v, want 10", got)
	}
	if got := testLimits().settings(90 * time.Second)["max_execution_time"]; got != 55 {
		t.Fatalf("looser ceiling must not raise the limit, got %v", got)
	}
	if got := (AnalyticsLimits{}).settings(0); len(got) != 0 {
		t.Fatalf("zero limits = %v, want no settings", got)
	}
	if got := (AnalyticsLimits{}).settings(1500 * time.Millisecond)["max_execution_time"]; got != 2 {
		t.Fatalf("partial seconds round up, got %v", got)
	}
}

func TestAnalyticsLimitsFromEnv(t *testing.T) {
	t.Setenv("EMTDS_CH_ANALYTICS_MAX_EXECUTION", "40s")
	t.Setenv("EMTDS_CH_ANALYTICS_PRIORITY", "3")
	t.Setenv("EMTDS_CH_ANALYTICS_MAX_THREADS", "0")
	t.Setenv("EMTDS_CH_ANALYTICS_MAX_MEMORY_MB", "512")
	l := AnalyticsLimitsFromEnv()
	if l.MaxExecution != 40*time.Second || l.Priority != 3 || l.MaxThreads != 0 || l.MaxMemory != 512<<20 {
		t.Fatalf("limits = %+v", l)
	}
	t.Setenv("EMTDS_CH_ANALYTICS_MAX_EXECUTION", "-1s")
	t.Setenv("EMTDS_CH_ANALYTICS_PRIORITY", "x")
	t.Setenv("EMTDS_CH_ANALYTICS_MAX_THREADS", "-2")
	t.Setenv("EMTDS_CH_ANALYTICS_MAX_MEMORY_MB", "")
	l, def := AnalyticsLimitsFromEnv(), DefaultAnalyticsLimits()
	if l != def {
		t.Fatalf("bad values must keep defaults: %+v, want %+v", l, def)
	}
	if def.MaxThreads < 1 || def.Priority <= insertPriority {
		t.Fatalf("defaults = %+v", def)
	}
}

func TestAnalyticsCtxNested(t *testing.T) {
	c := &CH{limits: testLimits()}
	tight := c.analyticsCtx(context.Background(), journalSortMaxExecution)
	again := c.analyticsCtx(tight, 0)
	s, ok := analyticsSettingsFrom(again)
	if !ok || s["max_execution_time"] != 20 {
		t.Fatalf("nested call reset the tighter limit: %v %v", s, ok)
	}
}

// TestAnalyticsLimitsOnEveryQuery runs every statistics entry point against a
// recording connection and requires each query context to carry the limits.
func TestAnalyticsLimitsOnEveryQuery(t *testing.T) {
	from, to := time.Now().Add(-time.Hour), time.Now()
	calls := map[string]func(c *CH, ctx context.Context){
		"Summary":     func(c *CH, ctx context.Context) { _, _ = c.Summary(ctx, from, to) },
		"TimeSeries":  func(c *CH, ctx context.Context) { _, _ = c.TimeSeries(ctx, from, to, 3600) },
		"Performance": func(c *CH, ctx context.Context) { _, _ = c.Performance(ctx, from, to) },
		"Breakdown":   func(c *CH, ctx context.Context) { _, _ = c.Breakdown(ctx, from, to, "country", 10) },
		"Values": func(c *CH, ctx context.Context) {
			_, _ = c.Values(ctx, ValuesQuery{Table: "events", Field: "country", From: from, To: to})
		},
		"ClickFacts":     func(c *CH, ctx context.Context) { _, _ = c.ClickFacts(ctx, from, to) },
		"ClickBuckets":   func(c *CH, ctx context.Context) { _, _ = c.ClickBuckets(ctx, "g1", from, to, false) },
		"Slice":          func(c *CH, ctx context.Context) { _, _ = c.Slice(ctx, from, to) },
		"Logs":           func(c *CH, ctx context.Context) { _, _, _ = c.Logs(ctx, LogFilter{From: from, To: to}) },
		"QueryPostbacks": func(c *CH, ctx context.Context) { _, _, _ = c.QueryPostbacks(ctx, PostbackQuery{From: from, To: to}) },
		"EachPostback": func(c *CH, ctx context.Context) {
			_, _ = c.EachPostback(ctx, PostbackQuery{From: from, To: to}, 10, nil, func(PostbackRow) error { return nil })
		},
		"PostbackLog": func(c *CH, ctx context.Context) { _, _ = c.PostbackLog(ctx, from, to, 10) },
		"CatalogRefs": func(c *CH, ctx context.Context) { _, _ = c.CatalogRefs(ctx, "example.com") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c, f := newRecordCH(t)
			call(c, context.Background())
			if len(f.ctxs) == 0 {
				t.Fatal("no query was sent")
			}
			for i, ctx := range f.ctxs {
				s, ok := analyticsSettingsFrom(ctx)
				if !ok {
					t.Fatalf("query %d runs without analytics limits", i)
				}
				if s["priority"] != 10 || s["max_threads"] != 4 || s["max_memory_usage"] != int64(1<<30) {
					t.Fatalf("query %d settings = %v", i, s)
				}
			}
		})
	}
}

// TestInsertsUseInsertPriority requires every PrepareBatch call of the package to take insertCtx,
// so no insert queues behind analytics reads.
func TestInsertsUseInsertPriority(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "PrepareBatch(") {
				continue
			}
			found++
			if !strings.Contains(line, "PrepareBatch(insertCtx(ctx)") {
				t.Errorf("%s:%d: PrepareBatch without insertCtx: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if found < 3 {
		t.Fatalf("found %d PrepareBatch calls, want at least 3", found)
	}
}

func TestIsQueryTooHeavy(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("boom"), false},
		{context.DeadlineExceeded, true},
		{fmt.Errorf("wrap: %w", &clickhouse.Exception{Code: chTimeoutExceeded}), true},
		{&clickhouse.Exception{Code: chTooSlow}, true},
		{&clickhouse.Exception{Code: chMemoryLimitExceeded}, true},
		{&clickhouse.Exception{Code: 60}, false},
	}
	for _, tc := range cases {
		if got := IsQueryTooHeavy(tc.err); got != tc.want {
			t.Errorf("IsQueryTooHeavy(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
