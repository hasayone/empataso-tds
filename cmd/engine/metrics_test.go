package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// roundTripFunc is an http.RoundTripper that answers without the network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// answer returns a RoundTripper that replies with status.
func answer(status int) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: http.NoBody, Request: r}, nil
	}
}

// countingInserter accepts every batch.
type countingInserter struct{}

func (countingInserter) InsertEvents(context.Context, []logbuf.Event) error { return nil }

// TestPostbackStatsNote checks that every class postbackResult can return, and
// the invalid payout, land in their own counter.
func TestPostbackStatsNote(t *testing.T) {
	var p postbackStats
	for _, c := range []struct {
		cid      string
		hasStore bool
		err      error
	}{
		{"", true, nil},
		{"a", false, nil},
		{"a", true, nil},
		{"a", true, sql.ErrNoRows},
		{"a", true, errors.New("boom")},
	} {
		_, outcome, _ := postbackResult(c.cid, c.hasStore, c.err)
		p.note(outcome)
	}
	p.note("invalid")
	p.note("something_else")
	want := config.PostbackMetrics{OK: 1, InvalidPayout: 1, NoSubID: 1, NoStore: 1, ClickNotFound: 1, StoreError: 1}
	if got := p.snapshot(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestPostbackHandlerCountsOutcomes sends postbacks through the handler and
// checks the counters the metrics file shows.
func TestPostbackHandlerCountsOutcomes(t *testing.T) {
	d := &engineDeps{}
	for _, target := range []string{
		"/postback?status=lead",                 // no click id
		"/postback?subid=a&status=lead",         // no store
		"/postback?subid=a&payout=NaN",          // invalid payout
		"/postback?subid=b&status=lead&sum=Inf", // invalid payout
	} {
		rec := httptest.NewRecorder()
		d.postback(rec, httptest.NewRequest(http.MethodGet, target, nil))
	}
	want := config.PostbackMetrics{InvalidPayout: 2, NoSubID: 1, NoStore: 1}
	if got := d.stats.postbacks.snapshot(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestOutboundCounters covers queued, dropped (queue full), sent (answer below
// 400) and failed (blocked address, answer 500).
func TestOutboundCounters(t *testing.T) {
	tpl := func(host string) config.OutboundPostback {
		return config.OutboundPostback{URL: "https://" + host + "/pb?cid={subid}", Method: "GET", Statuses: []string{"sale"}}
	}
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{tpl("a.example"), tpl("b.example")},
	})
	d := &engineDeps{log: discardLog(), groups: g, outbound: make(chan outboundJob, 1)}
	d.fireOutbound("g1", "abc", "", 1, "sale", [config.MaxSubID]string{})
	if got := d.stats.outbound.snapshot(); got.Queued != 1 || got.Dropped != 1 {
		t.Fatalf("queued/dropped: %+v", got)
	}

	job := outboundJob{Method: "GET", URL: "https://pb.example/pb?cid=abc", CID: "abc", Status: "sale"}
	d.sendOutbound(&http.Client{Transport: answer(http.StatusNoContent)}, job)
	d.sendOutbound(&http.Client{Transport: answer(http.StatusInternalServerError)}, job)
	d.sendOutbound(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}, job)
	blocked := job
	blocked.URL = "http://127.0.0.1/pb"
	d.sendOutbound(&http.Client{Transport: answer(http.StatusOK)}, blocked)
	want := config.OutboundMetrics{Queued: 1, Sent: 1, Failed: 3, Dropped: 1}
	if got := d.stats.outbound.snapshot(); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestMetricsSnapshotWithoutClickHouse checks the shape of the snapshot when
// there is no event buffer: connected false, zero figures and null times.
func TestMetricsSnapshotWithoutClickHouse(t *testing.T) {
	started := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	d := &engineDeps{}
	d.stats.postbacks.note("ok")
	raw, err := json.Marshal(d.metricsSnapshot("engine", started, now))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["instance"] != "engine" || got["started_at"] != "2026-11-16T10:00:00Z" || got["updated_at"] != "2026-11-16T10:01:00Z" {
		t.Fatalf("header: %s", raw)
	}
	ev := got["events"].(map[string]any)
	if ev["connected"] != false || ev["pending"] != float64(0) || ev["last_insert_ok"] != nil || ev["failing_since"] != nil {
		t.Fatalf("events: %s", raw)
	}
	for _, k := range []string{"input_len", "input_cap", "queue_len", "queue_cap", "pending", "inserted", "insert_errors", "lost", "dedup_ready", "spool"} {
		if _, ok := ev[k]; !ok {
			t.Errorf("events.%s is missing: %s", k, raw)
		}
	}
	lost := ev["lost"].(map[string]any)
	for _, k := range []string{"full", "queue", "insert", "late", "spool_full"} {
		if _, ok := lost[k]; !ok {
			t.Errorf("events.lost.%s is missing: %s", k, raw)
		}
	}
	if pb := got["postbacks"].(map[string]any); pb["ok"] != float64(1) {
		t.Fatalf("postbacks: %s", raw)
	}
	if ob := got["outbound"].(map[string]any); ob["queued"] != float64(0) {
		t.Fatalf("outbound: %s", raw)
	}
}

// TestMetricsSnapshotWithBuffer checks that the figures of a live buffer reach
// the snapshot and that a successful insert sets the last insert time.
func TestMetricsSnapshotWithBuffer(t *testing.T) {
	logs := logbuf.New(countingInserter{}, 100, 10, 10*time.Millisecond, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go logs.Run(ctx)
	d := &engineDeps{logs: logs}
	for i := 0; i < 25; i++ {
		logs.Push(logbuf.Event{Stream: "s"})
	}
	deadline := time.Now().Add(2 * time.Second)
	var ev config.EventMetrics
	for time.Now().Before(deadline) {
		ev = d.metricsSnapshot("engine", time.Now(), time.Now()).Events
		if ev.Inserted == 25 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ev.Connected || ev.Inserted != 25 || ev.Pending != 0 || ev.InputCap != 100 || ev.QueueCap != 10 {
		t.Fatalf("events: %+v", ev)
	}
	if ev.LastInsertOK == nil || ev.FailingSince != nil || ev.Lost.Total() != 0 {
		t.Fatalf("events: %+v", ev)
	}
}

// TestMetricsWriterWritesAndFinalizes starts the writer in a directory that does
// not exist yet and checks the file, its content, the directory mode, that no
// temporary file is left behind and that Close writes the last figures.
func TestMetricsWriterWritesAndFinalizes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "metrics")
	d := &engineDeps{}
	started := time.Now()
	collect := func() config.EngineMetrics { return d.metricsSnapshot("engine2", started, time.Now()) }
	w := startMetricsWriter(dir, "engine2", collect, discardLog())
	path := filepath.Join(dir, "engine2.json")
	waitForFile(t, path)

	d.stats.postbacks.note("ok")
	w.Close()
	w.Close() // idempotent
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got config.EngineMetrics
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("file is not JSON: %v: %s", err, raw)
	}
	if got.Instance != "engine2" || got.Postbacks.OK != 1 || got.StartedAt.IsZero() || got.UpdatedAt.Before(got.StartedAt) {
		t.Fatalf("final snapshot: %s", raw)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the metrics file", len(entries))
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm()&0o007 != 0 {
		t.Fatalf("directory mode %v, err %v: others must have no access", st.Mode().Perm(), err)
	}
}

// waitForFile waits up to two seconds for path to exist.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s was not written", path)
}

// TestStartMetricsInstanceName checks the environment handling: the default name
// is "engine", a valid name picks its own file, and an invalid name turns the
// file off without touching the endpoint or any other instance's file.
func TestStartMetricsInstanceName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EMTDS_METRICS_DIR", dir)
	d := &engineDeps{}

	t.Setenv("EMTDS_INSTANCE", "")
	collect, w := d.startMetrics(discardLog(), time.Now())
	waitForFile(t, filepath.Join(dir, "engine.json"))
	w.Close()
	if got := collect().Instance; got != "engine" {
		t.Fatalf("default instance %q", got)
	}

	t.Setenv("EMTDS_INSTANCE", "engine-2")
	_, w = d.startMetrics(discardLog(), time.Now())
	waitForFile(t, filepath.Join(dir, "engine-2.json"))
	w.Close()

	other := t.TempDir()
	t.Setenv("EMTDS_METRICS_DIR", other)
	t.Setenv("EMTDS_INSTANCE", "../evil")
	collect, w = d.startMetrics(discardLog(), time.Now())
	if w != nil {
		t.Fatal("an invalid instance name must not start the writer")
	}
	w.Close() // nil-safe
	if got := collect().Instance; got != "../evil" {
		t.Fatalf("endpoint instance %q", got)
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("an invalid instance wrote %d files", len(entries))
	}
}

// TestServeMetrics checks the endpoint: GET returns the JSON snapshot, other
// methods get 405.
func TestServeMetrics(t *testing.T) {
	d := &engineDeps{}
	h := serveMetrics(func() config.EngineMetrics { return d.metricsSnapshot("engine", time.Now(), time.Now()) })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var got config.EngineMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Instance != "engine" {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/internal/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
}

// TestMetricsSnapshotCarriesRedisTrim: the snapshot holds the progress of the Redis
// trim, zero without Redis, and the JSON has the section "redis_trim".
func TestMetricsSnapshotCarriesRedisTrim(t *testing.T) {
	t.Cleanup(func() { activeRedisTrim.Store(nil) })
	d := &engineDeps{}
	now := time.Now()

	activeRedisTrim.Store(nil)
	m := d.metricsSnapshot("engine", now, now)
	if m.RedisTrim != (config.RedisTrimMetrics{}) {
		t.Fatalf("without Redis the section must be zero: %+v", m.RedisTrim)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"redis_trim":{"scanned":0,"trimmed":0,"done":false,"error":false}`) {
		t.Fatalf("json has no zero redis_trim section: %s", b)
	}

	tr := newRedisTrim(nil, time.Hour, nil)
	tr.scanned.Store(120)
	tr.trimmed.Store(45)
	tr.done.Store(true)
	tr.skipped.Store("done_before")
	activeRedisTrim.Store(tr)
	want := config.RedisTrimMetrics{Scanned: 120, Trimmed: 45, Done: true, Skipped: "done_before"}
	if got := d.metricsSnapshot("engine", now, now).RedisTrim; got != want {
		t.Fatalf("redis_trim = %+v, want %+v", got, want)
	}

	tr.failed.Store(true)
	var back config.EngineMetrics
	b, _ = json.Marshal(d.metricsSnapshot("engine", now, now))
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.RedisTrim.Error || back.RedisTrim.Skipped != "done_before" {
		t.Fatalf("round trip lost the section: %+v", back.RedisTrim)
	}
}

// TestMetricsSnapshotConfigured checks that events.configured follows
// EMTDS_CLICKHOUSE_ADDR, with and without an event buffer.
func TestMetricsSnapshotConfigured(t *testing.T) {
	now := time.Now()
	t.Setenv("EMTDS_CLICKHOUSE_ADDR", "")
	if (&engineDeps{}).metricsSnapshot("engine", now, now).Events.Configured {
		t.Fatal("no address: configured must be false")
	}
	t.Setenv("EMTDS_CLICKHOUSE_ADDR", "clickhouse:9000")
	m := (&engineDeps{}).metricsSnapshot("engine", now, now)
	if !m.Events.Configured || m.Events.Connected {
		t.Fatalf("address set, no connection: %+v", m.Events)
	}
	if got := (*engineDeps)(nil).metricsSnapshot("engine", now, now); !got.Events.Configured {
		t.Fatalf("a nil deps still reports the address: %+v", got.Events)
	}
	logs := logbuf.New(countingInserter{}, 10, 2, 10*time.Millisecond, discardLog())
	m = (&engineDeps{logs: logs}).metricsSnapshot("engine", now, now)
	if !m.Events.Configured || !m.Events.Connected {
		t.Fatalf("address set, buffer live: %+v", m.Events)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"configured":true`) {
		t.Fatalf("json: %s", b)
	}
}

// TestReadPreviousRun checks which old files give a Previous: only a metrics
// file of an earlier process, with its start time and loss counters.
func TestReadPreviousRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.json")
	started := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	if readPreviousRun(path, started) != nil {
		t.Fatal("a missing file has no previous run")
	}
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write([]byte("{"))
	if readPreviousRun(path, started) != nil {
		t.Fatal("a broken file has no previous run")
	}
	old := config.EngineMetrics{StartedAt: started.Add(-time.Hour)}
	old.Events.Lost = config.LostEvents{Queue: 5, Insert: 2}
	old.Previous = &config.PreviousRun{StartedAt: started.Add(-2 * time.Hour), Lost: config.LostEvents{Late: 9}}
	b, _ := json.Marshal(old)
	write(b)
	got := readPreviousRun(path, started)
	if got == nil || !got.StartedAt.Equal(old.StartedAt) || got.Lost != old.Events.Lost {
		t.Fatalf("previous run = %+v", got)
	}
	same, _ := json.Marshal(config.EngineMetrics{StartedAt: started})
	write(same)
	if readPreviousRun(path, started) != nil {
		t.Fatal("a file of the same process is not a previous run")
	}
	write([]byte(`{"events":{}}`))
	if readPreviousRun(path, started) != nil {
		t.Fatal("a file without started_at is not a previous run")
	}
}

// TestStartMetricsCarriesPrevious checks that the engine reads its old file
// before the first write, carries it as "previous" in the snapshot and in the
// new file, and that a second start carries only the process right before it.
func TestStartMetricsCarriesPrevious(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EMTDS_METRICS_DIR", dir)
	t.Setenv("EMTDS_INSTANCE", "engine")
	first := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	old := config.EngineMetrics{Instance: "engine", StartedAt: first}
	old.Events.Lost = config.LostEvents{Queue: 11}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, "engine.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	d := &engineDeps{}
	second := time.Now().UTC()
	collect, w := d.startMetrics(discardLog(), second)
	w.Close()
	m := collect()
	if m.Previous == nil || !m.Previous.StartedAt.Equal(first) || m.Previous.Lost.Queue != 11 {
		t.Fatalf("snapshot previous: %+v", m.Previous)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "engine.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk config.EngineMetrics
	if err := json.Unmarshal(raw, &onDisk); err != nil || onDisk.Previous == nil || onDisk.Previous.Lost.Queue != 11 {
		t.Fatalf("file previous: %v: %s", err, raw)
	}

	collect, w = d.startMetrics(discardLog(), second.Add(time.Minute))
	w.Close()
	if p := collect().Previous; p == nil || !p.StartedAt.Equal(second) || p.Lost.Queue != 0 {
		t.Fatalf("the third start must carry the second process: %+v", p)
	}
}
