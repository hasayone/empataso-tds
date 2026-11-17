package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/store"
)

// eventWait polls cond for up to ten seconds.
func eventWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("not met within 10s: %s", what)
}

// testSpoolQueue opens the events queue of an instance in a temporary root.
func testSpoolQueue(t *testing.T) *logbuf.SpoolQueue {
	t.Helper()
	sp, err := logbuf.OpenSpool(logbuf.SpoolConfig{
		Root: t.TempDir(), Instance: "engine", MaxBytes: 1 << 30, MinFreePct: 1,
		Free: func(string) (uint64, uint64, error) { return 900, 1000, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sp.Close)
	q, err := sp.Queue(logbuf.EventsKind)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// Before ClickHouse answers the buffer already takes events and keeps them on
// the spool, /healthz sees it, and the step wave is held back.
func TestEventBufferExistsBeforeClickHouse(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	q := testSpoolQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	buf := d.startEventLog(ctx, chLink{
		addr: "ch:9000",
		open: func(context.Context) (*store.CH, error) {
			attempts.Add(1)
			return nil, errors.New("connection refused")
		},
		pause: 10 * time.Millisecond,
	}, q)
	t.Cleanup(func() {
		cancel()
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = buf.Close(cctx)
	})

	if d.logs != buf {
		t.Fatal("startEventLog must set d.logs")
	}
	if d.clickhouse() != nil || !d.noStepWave.Load() {
		t.Fatal("before the connection there is no store and the step wave is held back")
	}
	for i := 0; i < 5; i++ {
		buf.Push(logbuf.Event{CID: "c", Stream: "s"})
	}
	eventWait(t, "events parked on the spool", func() bool { return buf.Stats().Spool.Events == 5 })
	if s := buf.Stats(); s.Connected || s.Pending != 5 || s.Losses.Total() != 0 {
		t.Fatalf("stats %+v: not connected, 5 pending, nothing lost", s)
	}
	eventWait(t, "connection attempts go on", func() bool { return attempts.Load() >= 3 })

	rec := httptest.NewRecorder()
	healthzHandler(d.logs)(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Events-Lost-Detail"); !strings.Contains(got, "spool_full=0") {
		t.Fatalf("X-Events-Lost-Detail %q lacks spool_full", got)
	}
}

// A connection that comes late is kept, logged once, and the step wave is
// recomputed from it; the attempts before it did not stop the loop.
func TestConnectClickHouseLate(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	d.noStepWave.Store(true)
	var attempts atomic.Int32
	var seen atomic.Pointer[store.CH]
	done := make(chan struct{})
	go func() {
		d.connectClickHouse(context.Background(), chLink{
			addr: "ch:9000",
			open: func(context.Context) (*store.CH, error) {
				if attempts.Add(1) < 3 {
					return nil, errors.New("connection refused")
				}
				return &store.CH{}, nil
			},
			pause:     5 * time.Millisecond,
			onConnect: func(ch *store.CH) { seen.Store(ch) },
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connectClickHouse did not finish")
	}
	if attempts.Load() != 3 || d.clickhouse() == nil || seen.Load() != d.clickhouse() {
		t.Fatalf("attempts=%d store=%v: want a third attempt kept and reported", attempts.Load(), d.clickhouse())
	}
	if !d.noStepWave.Load() {
		t.Fatal("a store without the step column keeps the step wave held back")
	}
}

// The loop stops with its context instead of trying for ever.
func TestConnectClickHouseStopsWithContext(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.connectClickHouse(ctx, chLink{
			open:  func(context.Context) (*store.CH, error) { return nil, errors.New("down") },
			pause: time.Hour,
		})
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop ignored its context")
	}
	if d.clickhouse() != nil {
		t.Fatal("no connection was made")
	}
}

// An attempt that is stuck inside open ends with the context, not after the
// minute the dial would wait, and the cut-off is not reported as a failure to
// connect.
func TestConnectClickHouseStopsInsideAnAttempt(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		d.connectClickHouse(ctx, chLink{
			open: func(c context.Context) (*store.CH, error) {
				close(entered)
				<-c.Done()
				return nil, c.Err()
			},
			pause: time.Hour,
		})
		close(done)
	}()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop waited for the attempt instead of the context")
	}
	if d.clickhouse() != nil {
		t.Fatal("no connection was made")
	}
}

// OpenCHWaitingCtx does not start a dial once its context has ended.
func TestOpenCHWaitingCtxHonoursTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.OpenCHWaitingCtx(ctx, "127.0.0.1:1", "db", "u", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLazyInserterWithoutConnection(t *testing.T) {
	err := lazyInserter{&engineDeps{}}.InsertEvents(context.Background(), []logbuf.Event{{CID: "a"}})
	if !errors.Is(err, logbuf.ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}

func TestAwaitRedisCallsOnUpOnce(t *testing.T) {
	var pings, ups atomic.Int32
	done := make(chan struct{})
	go func() {
		awaitRedis(context.Background(), func(context.Context) error {
			if pings.Add(1) < 3 {
				return errors.New("refused")
			}
			return nil
		}, 5*time.Millisecond, func() { ups.Add(1) })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRedis did not finish")
	}
	if pings.Load() != 3 || ups.Load() != 1 {
		t.Fatalf("pings=%d ups=%d, want 3 and 1", pings.Load(), ups.Load())
	}
}

func TestAwaitRedisStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ups atomic.Int32
	done := make(chan struct{})
	go func() {
		awaitRedis(ctx, func(context.Context) error { return errors.New("refused") }, time.Hour, func() { ups.Add(1) })
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRedis ignored its context")
	}
	if ups.Load() != 0 {
		t.Fatal("onUp must not run")
	}
}

// A spool directory that was asked for and cannot be used stops the start; a
// second process on the same instance does too.
func TestOpenEventSpool(t *testing.T) {
	stubMount(t, true)
	root := t.TempDir()
	t.Setenv("EMTDS_SPOOL_DIR", root)
	t.Setenv("EMTDS_INSTANCE", "engine-t")
	sp, q, err := openEventSpool(discardLog())
	if err != nil || sp == nil || q == nil {
		t.Fatalf("openEventSpool: %v %v %v", sp, q, err)
	}
	defer sp.Close()

	if _, _, err := openEventSpool(discardLog()); !errors.Is(err, logbuf.ErrSpoolLocked) {
		t.Fatalf("second process: err = %v, want ErrSpoolLocked", err)
	}

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMTDS_SPOOL_DIR", filepath.Join(blocker, "spool"))
	if _, _, err := openEventSpool(discardLog()); err == nil {
		t.Fatal("an explicit directory that cannot be used must be an error")
	}
}

// stubMount makes openEventSpool see every directory as a mount point (or as
// none) for the rest of the test.
func stubMount(t *testing.T, mount bool) {
	t.Helper()
	old := spoolIsMount
	spoolIsMount = func(string) (bool, error) { return mount, nil }
	t.Cleanup(func() { spoolIsMount = old })
}

// An explicit spool directory that is a plain directory of the container's
// layer does not stop the start: the engine opens the spool and only warns.
func TestOpenEventSpoolWarnsAboutAPlainDirectory(t *testing.T) {
	stubMount(t, false)
	t.Setenv("EMTDS_SPOOL_DIR", t.TempDir())
	t.Setenv("EMTDS_INSTANCE", "engine-t")
	sp, q, err := openEventSpool(discardLog())
	if err != nil || sp == nil || q == nil {
		t.Fatalf("a plain directory must still open: %v %v %v", sp, q, err)
	}
	sp.Close()
}
