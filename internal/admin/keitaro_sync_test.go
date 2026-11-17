package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/logbuf"
)

func syncTarget(s *Server, creds ktCreds) keitaroSyncTarget {
	return keitaroSyncTarget{
		syncPath:      s.cfg.KeitaroSyncFile,
		board:         config.MainBoardID,
		allBoards:     true,
		creds:         creds,
		manifestStore: s.cfg.Manifest,
	}
}

func syncTestServer(t *testing.T, base, key string, sink *fakeSink, syncPath, cronPath string, now time.Time) (*Server, *httptest.Server) {
	t.Helper()
	s, srv := newKeitaroServers(t, base, key, &memGroups{list: []config.Group{{ID: "g1", Name: "G"}}}, sink, nil)
	s.cfg.KeitaroSyncFile = syncPath
	s.cfg.CronFile = cronPath
	s.syncNow = func() time.Time { return now }
	return s, srv
}

func writeCron(t *testing.T, path string, enabled bool, every int) {
	t.Helper()
	cfg := cron.Default()
	cfg.KeitaroSync.Enabled = enabled
	cfg.KeitaroSync.EveryMinutes = every
	if err := cron.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func syncLogHandler(t *testing.T, rows string, logCalls *atomic.Int32, ranges *[]struct{ from, to string }) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/log") {
			logCalls.Add(1)
			if ranges != nil {
				*ranges = append(*ranges, struct{ from, to string }{r.URL.Query().Get("range[from]"), r.URL.Query().Get("range[to]")})
			}
			_, _ = w.Write([]byte(rows))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}
}

func TestKeitaroSyncNeedFullImport(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, true, 15)
	s.keitaroSyncOnce(context.Background())
	st, ok, err := s.loadKeitaroSync()
	if err != nil || !ok || st.Error != keitaroSyncNeedImport || st.NextRun.Sub(now) != 15*time.Minute {
		t.Fatalf("state ok=%v err=%v st=%+v", ok, err, st)
	}
	if s.syncFailStreak != 0 {
		t.Fatalf("streak=%d", s.syncFailStreak)
	}
}

func TestKeitaroSyncBootstrapWatermark(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	wm := now.Add(-2 * time.Hour)
	sink := &fakeSink{wmClick: wm, wmConv: wm}
	s, _ := syncTestServer(t, "http://tracker.example", "key", sink, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, true, 15)
	var calls atomic.Int32
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[],"total":0}`, &calls, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	s.keitaroSyncOnce(context.Background())
	st, ok, err := s.loadKeitaroSync()
	if err != nil || !ok || !st.OK {
		t.Fatalf("state %+v err=%v", st, err)
	}
	if st.ClicksTo != wm && !st.ClicksTo.After(wm.Add(-time.Second)) {
		t.Fatalf("clicks cursor %v want near %v", st.ClicksTo, wm)
	}
}

func TestKeitaroSyncBootstrapEmptyManifestHost(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	wm := now.Add(-time.Hour)
	sink := &fakeSink{wmClick: wm, wmConv: wm}
	s, _ := syncTestServer(t, "http://tracker.example", "key", sink, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, true, 15)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	s.keitaroSyncOnce(context.Background())
	st, ok, _ := s.loadKeitaroSync()
	if !ok || st.Error != keitaroSyncNeedImport {
		t.Fatalf("st=%+v", st)
	}
}

func TestKeitaroSyncFutureTimezoneOnce(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, true, 15)
	st := keitaroSyncFile{Host: "tracker.example", ClicksTo: now.Add(time.Hour), ConversionsTo: now}
	if err := s.saveKeitaroSync(st); err != nil {
		t.Fatal(err)
	}
	s.keitaroSyncOnce(context.Background())
	out, ok, _ := s.loadKeitaroSync()
	if !ok || out.Error != keitaroSyncTimezone {
		t.Fatalf("out=%+v", out)
	}
}

func TestKeitaroSyncWarehouseNil(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", nil, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	s.cfg.Warehouse = nil
	writeCron(t, s.cfg.CronFile, true, 15)
	s.keitaroSyncOnce(context.Background())
	st, ok, _ := s.loadKeitaroSync()
	if !ok || st.Error != "clickhouse unavailable" {
		t.Fatalf("st=%+v", st)
	}
	_, _, _, err := s.importLogWindow(context.Background(), s.keitaroCreds(), "2026-01-01 00:00:00", "2026-01-01 01:00:00", logImportOpts{clicks: true, syncMode: true})
	if err == nil || err.Error() != "clickhouse unavailable" {
		t.Fatalf("err=%v", err)
	}
}

func TestKeitaroSyncWindowsAndChunks(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	cursor := now.Add(-10 * time.Hour)
	upper := now.Add(-keitaroSyncLag)
	winFrom, winTo := syncWindowBounds(cursor, upper, keitaroSyncClickBack)
	if !winFrom.Equal(cursor.Add(-keitaroSyncClickBack)) {
		t.Fatalf("from=%v", winFrom)
	}
	wantTo := cursor.Add(keitaroSyncMaxPerRun)
	if wantTo.After(upper) {
		wantTo = upper
	}
	if !winTo.Equal(wantTo) {
		t.Fatalf("to=%v want %v", winTo, wantTo)
	}
}

func TestKeitaroSyncConversionsCursorForward(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	var ranges []struct{ from, to string }
	var calls atomic.Int32
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[],"total":0}`, &calls, &ranges))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	convCursor := now.Add(-2 * time.Hour)
	st := keitaroSyncFile{Host: host, ClicksTo: now.Add(-keitaroSyncLag - time.Minute), ConversionsTo: convCursor}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), &st, true)
	if !res.OK {
		t.Fatal("pass failed")
	}
	if st.ConversionsTo.Before(convCursor) {
		t.Fatalf("conversions cursor went back: was %v now %v", convCursor, st.ConversionsTo)
	}
	for _, rg := range ranges {
		if rg.from == "" {
			continue
		}
		f, _ := time.ParseInLocation(keitaroTimeLayout, rg.from, time.UTC)
		if f.Before(convCursor.Add(-keitaroSyncConvBack - time.Second)) {
			t.Fatalf("conversion window starts too early: %s", rg.from)
		}
	}
}

func TestKeitaroSyncEightDayLagAdvances24h(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[],"total":0}`, &atomic.Int32{}, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	start := now.Add(-8 * 24 * time.Hour)
	st := keitaroSyncFile{Host: host, ClicksTo: start, ConversionsTo: start}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), &st, true)
	if !res.OK {
		t.Fatal("pass failed")
	}
	if st.ClicksTo.Sub(start) != keitaroSyncMaxPerRun {
		t.Fatalf("clicks moved %v want 24h", st.ClicksTo.Sub(start))
	}
}

func TestKeitaroSyncCursorAdvancesOnEmpty(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[],"total":0}`, &atomic.Int32{}, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	cursor := now.Add(-10 * time.Hour)
	st := keitaroSyncFile{Host: host, ClicksTo: cursor, ConversionsTo: cursor}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), &st, true)
	if !res.OK || !st.ClicksTo.After(cursor) {
		t.Fatalf("ok=%v clicks=%v", res.OK, st.ClicksTo)
	}
}

func TestKeitaroSyncCursorStaysOnError(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	cursor := now.Add(-5 * time.Hour)
	st := keitaroSyncFile{Host: "tracker.example", ClicksTo: cursor, ConversionsTo: cursor}
	dir := t.TempDir()
	s := &Server{cfg: Config{Warehouse: &fakeSink{}, Manifest: NewFileManifest(filepath.Join(dir, "keitaro-import.json"))}, ktJob: newKeitaroJob()}
	s.syncNow = func() time.Time { return now }
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: "tracker.example", Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	creds := ktCreds{Base: "http://tracker.example", Key: "k"}
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, creds), &st, true)
	if res.OK || res.ErrText == "" || st.ClicksTo != cursor {
		t.Fatalf("ok=%v err=%q cursor=%v", res.OK, res.ErrText, st.ClicksTo)
	}
}

func TestKeitaroSyncDedupExistingCID(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-2 * time.Hour)
	dir := t.TempDir()
	s, _ := syncTestServer(t, "http://tracker.example", "key", nil, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[{"sub_id":"abc","datetime":"`+ts.Format(keitaroTimeLayout)+`","campaign_id":1,"campaign":"C"}],"total":1}`, &atomic.Int32{}, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	sink := &fakeSink{events: []logbuf.Event{{Ts: ts, Domain: host, CID: "abc"}}}
	s.cfg.Warehouse = sink
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	st := keitaroSyncFile{Host: host, ClicksTo: ts, ConversionsTo: ts}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	before := len(sink.events)
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), &st, true)
	if !res.OK || res.Clicks != 0 || len(sink.events) != before {
		t.Fatalf("ok=%v clicks=%d events=%d", res.OK, res.Clicks, len(sink.events))
	}
}

// TestKeitaroSyncClickCost checks the sync asks the click log for cost and stores it on the click
// in cents with no currency, a click without a price stays at zero, and the clicks cursor moves on.
func TestKeitaroSyncClickCost(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-2 * time.Hour)
	sink := &fakeSink{}
	dir := t.TempDir()
	s, _ := syncTestServer(t, "http://tracker.example", "key", sink, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	rows := `{"rows":[{"sub_id":"priced","datetime":"` + ts.Format(keitaroTimeLayout) + `","campaign_id":1,"cost":"1.26"},` +
		`{"sub_id":"free","datetime":"` + ts.Format(keitaroTimeLayout) + `","campaign_id":1}],"total":2}`
	var asked atomic.Bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/clicks/log") {
			var body struct {
				Columns []string `json:"columns"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, c := range body.Columns {
				if c == "cost" {
					asked.Store(true)
				}
			}
			_, _ = w.Write([]byte(rows))
			return
		}
		if strings.Contains(r.URL.Path, "/log") {
			_, _ = w.Write([]byte(`{"rows":[],"total":0}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	cursor := ts.Add(-time.Minute)
	st := keitaroSyncFile{Host: host, ClicksTo: cursor, ConversionsTo: cursor}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), &st, true)
	if !res.OK || res.Clicks != 2 || !st.ClicksTo.After(ts) {
		t.Fatalf("ok=%v err=%q clicks=%d cursor=%v", res.OK, res.ErrText, res.Clicks, st.ClicksTo)
	}
	if !asked.Load() {
		t.Fatal("the click log was not asked for cost")
	}
	cost := map[string]int64{}
	for _, ev := range sink.events {
		if ev.CostCur != "" {
			t.Fatalf("imported cost has a currency: %+v", ev)
		}
		cost[ev.CID] = ev.CostCents
	}
	if len(cost) != 2 || cost["priced"] != 126 || cost["free"] != 0 {
		t.Fatalf("cost by click %v", cost)
	}
}

func TestKeitaroSyncSkipsBadRows(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-time.Hour)
	sink := &fakeSink{}
	dir := t.TempDir()
	s, _ := syncTestServer(t, "http://tracker.example", "key", sink, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	from, to := formatKeitaroWindow(ts.Add(-time.Minute), ts.Add(time.Minute))
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[{"sub_id":"","datetime":"`+ts.Format(keitaroTimeLayout)+`","campaign_id":1},{"sub_id":"x","datetime":"","campaign_id":1}],"total":2}`, &atomic.Int32{}, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	_, _, _, err := s.importLogWindow(context.Background(), s.keitaroCreds(), from, to, logImportOpts{clicks: true, syncMode: true})
	if err != nil || len(sink.events) != 0 {
		t.Fatalf("err=%v events=%d", err, len(sink.events))
	}
}

func TestKeitaroSyncHostChangeResetsCursor(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	wm := now.Add(-3 * time.Hour)
	sink := &fakeSink{wmClick: wm, wmConv: wm}
	s, _ := syncTestServer(t, "http://tracker.example", "key", sink, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, true, 15)
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[],"total":0}`, &atomic.Int32{}, nil))
	t.Cleanup(fake.Close)
	newHost := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: newHost, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	st := keitaroSyncFile{Host: "old.example", ClicksTo: now.Add(-time.Hour), ConversionsTo: now.Add(-time.Hour)}
	if err := s.saveKeitaroSync(st); err != nil {
		t.Fatal(err)
	}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	s.keitaroSyncOnce(context.Background())
	out, ok, _ := s.loadKeitaroSync()
	if !ok || out.Host != newHost || !out.OK {
		t.Fatalf("out=%+v", out)
	}
}

func TestKeitaroSyncHTTP409DuringSync(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, admin := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, true, 15)
	if msg := s.ktJob.beginSync(); msg != "" {
		t.Fatal(msg)
	}
	defer s.ktJob.finishSync()
	_, code := keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{"offers": true})
	if code != http.StatusConflict {
		t.Fatalf("import %d", code)
	}
	_, code = keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusConflict {
		t.Fatalf("purge %d", code)
	}
	_, code = keitaroPOST(t, admin, "/api/cron/run", map[string]string{"job": cron.JobKeitaroSync})
	if code != http.StatusConflict {
		t.Fatalf("cron run %d", code)
	}
}

func TestKeitaroSyncRunNowDisabled(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, false, 15)
	if err := s.runKeitaroSyncNow(); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[],"total":0}`, &calls, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	st := keitaroSyncFile{Host: host, ClicksTo: now.Add(-2 * time.Hour), ConversionsTo: now.Add(-2 * time.Hour)}
	if err := s.saveKeitaroSync(st); err != nil {
		t.Fatal(err)
	}
	s.keitaroSyncOnce(context.Background())
	if calls.Load() == 0 {
		t.Fatal("expected run-now to sync while disabled in config")
	}
}

func TestKeitaroSyncBackoffExact(t *testing.T) {
	if got := keitaroSyncBackoff(15*time.Minute, 2); got != 30*time.Minute {
		t.Fatalf("got %v", got)
	}
	if got := keitaroSyncBackoff(15*time.Minute, 10); got != keitaroSyncMaxBackoff {
		t.Fatalf("cap got %v", got)
	}
}

func TestKeitaroSyncParallelOnceSingleImport(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := syncTestServer(t, "http://tracker.example", "key", &fakeSink{}, filepath.Join(dir, "sync.json"), filepath.Join(dir, "cron.json"), now)
	writeCron(t, s.cfg.CronFile, true, 15)
	var calls atomic.Int32
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[],"total":0}`, &calls, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	almostCaughtUp := now.Add(-keitaroSyncLag - time.Minute)
	st := keitaroSyncFile{Host: host, ClicksTo: almostCaughtUp, ConversionsTo: almostCaughtUp}
	if err := s.saveKeitaroSync(st); err != nil {
		t.Fatal(err)
	}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	var ready sync.WaitGroup
	var begin sync.WaitGroup
	ready.Add(6)
	begin.Add(1)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			ready.Wait()
			begin.Wait()
			s.keitaroSyncOnce(context.Background())
		}()
	}
	ready.Wait()
	begin.Done()
	wg.Wait()
	if s.syncPassCount.Load() != 1 {
		t.Fatalf("sync passes=%d want 1", s.syncPassCount.Load())
	}
	if calls.Load() < 2 {
		t.Fatalf("log calls=%d", calls.Load())
	}
}

func TestKeitaroPurgeRemovesSyncFileEarly(t *testing.T) {
	dir := t.TempDir()
	syncPath := filepath.Join(dir, "keitaro-sync.json")
	if err := os.WriteFile(syncPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, admin := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, &keitaroFiles{dir: dir})
	s.cfg.KeitaroSyncFile = syncPath
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: "tracker.example", Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	_, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge %d", code)
	}
	if _, err := os.Stat(syncPath); !os.IsNotExist(err) {
		t.Fatalf("sync file still present: %v", err)
	}
}

func TestKeitaroPurgeEmptyManifestKeepsSyncFile(t *testing.T) {
	dir := t.TempDir()
	syncPath := filepath.Join(dir, "keitaro-sync.json")
	if err := os.WriteFile(syncPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, admin := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, &keitaroFiles{dir: dir})
	s.cfg.KeitaroSyncFile = syncPath
	if _, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{}); code != http.StatusOK {
		t.Fatalf("purge %d", code)
	}
	if _, err := os.Stat(syncPath); err != nil {
		t.Fatalf("sync file: %v", err)
	}
}

func TestKeitaroPurgeLeavesBoardSyncFile(t *testing.T) {
	dir := t.TempDir()
	syncPath := filepath.Join(dir, "keitaro-sync.json")
	boardPath := filepath.Join(dir, "keitaro-sync-seo.json")
	for _, path := range []string{syncPath, boardPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, admin := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, &keitaroFiles{dir: dir})
	s.cfg.KeitaroSyncFile = syncPath
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: "tracker.example", Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	if _, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{}); code != http.StatusOK {
		t.Fatalf("purge %d", code)
	}
	if _, err := os.Stat(syncPath); !os.IsNotExist(err) {
		t.Fatalf("global sync: %v", err)
	}
	if _, err := os.Stat(boardPath); err != nil {
		t.Fatalf("board sync: %v", err)
	}
}

func TestKeitaroPurgeNamesErrorKeepsManifest(t *testing.T) {
	s, admin := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, nil)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: "tracker.example", Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	s.cfg.Lists = errNamesLists{}
	if _, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{}); code != http.StatusInternalServerError {
		t.Fatalf("purge %d", code)
	}
	manifest, err := s.cfg.Manifest.Load()
	if err != nil || manifest.Empty() || manifest.Campaigns["1"] != "g1" {
		t.Fatalf("manifest %+v err %v", manifest, err)
	}
}

type errNamesLists struct{}

func (errNamesLists) Names() ([]string, error)    { return nil, errors.New("lists unreadable") }
func (errNamesLists) Read(string) (string, error) { return "", os.ErrNotExist }
func (errNamesLists) Write(string, string) error  { return nil }
func (errNamesLists) Delete(string) error         { return nil }

func TestManualImportClickCounts(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-time.Hour)
	dir := t.TempDir()
	s, _ := syncTestServer(t, "http://tracker.example", "key", nil, filepath.Join(dir, "s.json"), filepath.Join(dir, "c.json"), now)
	fake := httptest.NewServer(syncLogHandler(t, `{"rows":[{"sub_id":"dup","datetime":"`+ts.Format(keitaroTimeLayout)+`","campaign_id":1,"campaign":"C"},{"sub_id":"new","datetime":"`+ts.Format(keitaroTimeLayout)+`","campaign_id":1,"campaign":"C"}],"total":2}`, &atomic.Int32{}, nil))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	sink := &fakeSink{events: []logbuf.Event{{Ts: ts, Domain: host, CID: "dup"}}}
	s.cfg.Warehouse = sink
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"1": "g1"}}); err != nil {
		t.Fatal(err)
	}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	from, to := formatKeitaroWindow(ts.Add(-time.Minute), ts.Add(time.Minute))
	counts, _, _, err := s.importLogWindow(context.Background(), s.keitaroCreds(), from, to, logImportOpts{clicks: true})
	if err != nil {
		t.Fatal(err)
	}
	if counts["clicks"] != 2 || counts["clicks_duplicates"] != 1 || len(sink.events) != 2 {
		t.Fatalf("counts=%+v events=%d", counts, len(sink.events))
	}
}

func TestKeitaroSyncBlocksManualImport(t *testing.T) {
	s := &Server{ktJob: newKeitaroJob()}
	if msg := s.ktJob.beginSync(); msg != "" {
		t.Fatal(msg)
	}
	if msg := s.ktJob.beginImport(); msg != keitaroSyncBusyMsg {
		t.Fatalf("import %q", msg)
	}
	s.ktJob.finishSync()
}

func TestClearKeitaroSyncRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.json")
	st := keitaroSyncFile{Running: true, Host: "h"}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{KeitaroSyncFile: path}}
	s.clearKeitaroSyncRunning()
	out, ok, err := s.loadKeitaroSync()
	if err != nil || !ok || out.Running {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestSyncWindowBoundsFromURL(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	cursor := now.Add(-8 * 24 * time.Hour)
	upper := now.Add(-keitaroSyncLag)
	from, to := syncWindowBounds(cursor, upper, keitaroSyncConvBack)
	if to.Sub(cursor) != keitaroSyncMaxPerRun {
		t.Fatalf("span=%v", to.Sub(cursor))
	}
	_ = from
	_ = url.Values{}
}
