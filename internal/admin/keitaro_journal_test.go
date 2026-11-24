package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emtds/bridge/ktimport"
)

// TestKeitaroJournalMissingFileStaysIdle checks that a job without a journal file shows no run and writes nothing.
func TestKeitaroJournalMissingFileStaysIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keitaro-job.json")
	j := loadKeitaroJob(path, nil)
	v := j.snapshot()
	if v.Stage != "idle" || v.Running || v.Resumable || v.StartedAt != "" || v.Error != "" || len(v.Logs) != 0 {
		t.Fatalf("view %+v", v)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("journal written for an idle job: %v", err)
	}
}

// TestKeitaroJournalSurvivesRestart checks that a run cut by a restart comes back failed,
// interrupted and resumable, and that the resume skips passed catalogs and continues clicks from the mark.
func TestKeitaroJournalSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keitaro-job.json")
	j := loadKeitaroJob(path, nil)
	if msg := j.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	j.setRequest(keitaroImportReq{Offers: true, Campaigns: true, Clicks: true, Conversions: true, From: "2026-10-01", To: "2026-10-31", Board: "main"})
	j.publish(map[string]int{"offers": 5}, nil, "")
	j.catalogsPassed()
	j.progress("clicks", 3, 10)
	through := time.Date(2026, 10, 5, 8, 15, 0, 0, time.UTC)
	j.markLog("clicks", 2000, through, false)
	j.save(true)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"running": true`) {
		t.Fatalf("live journal %s", raw)
	}

	back := loadKeitaroJob(path, nil)
	v := back.snapshot()
	if v.Running || v.Stage != "failed" || v.LastStage != "clicks" || !v.Interrupted || !v.Resumable || v.Error != keitaroInterruptedMsg {
		t.Fatalf("restored %+v", v)
	}
	if v.Counts["offers"] != 5 || v.Logs["clicks"].Rows != 2000 || v.Logs["clicks"].Done || v.StartedAt == "" || v.FinishedAt == "" {
		t.Fatalf("restored counts %+v logs %+v", v.Counts, v.Logs)
	}
	run, msg := back.beginResume()
	if msg != "" {
		t.Fatal(msg)
	}
	if run.req.Offers || run.req.Campaigns || !run.req.Clicks || !run.req.Conversions || run.req.Board != "main" || run.req.From != "2026-10-01" {
		t.Fatalf("resume req %+v", run.req)
	}
	if run.clicksFrom != "2026-10-05 08:15:00" || run.clicksRows != 2000 || run.counts["offers"] != 5 {
		t.Fatalf("resume run %+v", run)
	}
	if _, msg := back.beginResume(); msg != "import already running" {
		t.Fatalf("second resume %q", msg)
	}
	v = back.snapshot()
	if !v.Running || v.Resumable || v.Interrupted || v.Error != "" || v.Resumes != 1 {
		t.Fatalf("resumed %+v", v)
	}
}

// TestKeitaroJournalNotResumable checks the runs a resume refuses: a finished run, a one-campaign dosync and a purge.
func TestKeitaroJournalNotResumable(t *testing.T) {
	cases := map[string]keitaroJournal{
		"done":     {Stage: "done", Request: keitaroImportReq{Clicks: true}},
		"campaign": {Stage: "failed", CampaignID: 7, Request: keitaroImportReq{CampaignID: 7, Offers: true}},
		"filters":  {Stage: "failed", Request: keitaroImportReq{FiltersOnly: true, AllImported: true}},
		"all done": {Stage: "failed", CatalogsDone: true, Request: keitaroImportReq{Offers: true, Clicks: true}, Logs: map[string]keitaroLogMark{"clicks": {Done: true}}},
	}
	for name, rec := range cases {
		if rec.canResume() {
			t.Fatalf("%s resumable", name)
		}
	}
	j := newKeitaroJob()
	j.rec = keitaroJournal{Stage: "failed", Request: keitaroImportReq{Clicks: true}, Resumable: true}
	j.dropResume()
	if _, msg := j.beginResume(); msg != keitaroNothingToResumeMsg {
		t.Fatalf("after purge %q", msg)
	}
}

// clickLogFake serves a click log of n rows, three per second from 2026-10-01 00:00:00, in pages of 1000.
// It honours range.from, fails the first page at offset 2000 once with 500, and records every range.from.
type clickLogFake struct {
	n      int
	broken atomic.Bool
	mu     sync.Mutex
	froms  []string
}

// handler answers the tracker paths the click import calls.
func (f *clickLogFake) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/admin_api/v1/clicks/log" {
		_, _ = w.Write([]byte(`[]`))
		return
	}
	var body struct {
		Range  struct{ From string } `json:"range"`
		Offset int                   `json:"offset"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.froms = append(f.froms, body.Range.From)
	f.mu.Unlock()
	if body.Offset >= 2000 && f.broken.CompareAndSwap(true, false) {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	from, _ := time.Parse(keitaroTimeLayout, body.Range.From)
	var rows []map[string]any
	for i := 0; i < f.n; i++ {
		ts := start.Add(time.Duration(i/3) * time.Second)
		if ts.Before(from) {
			continue
		}
		rows = append(rows, map[string]any{"sub_id": fmt.Sprintf("c%04d", i), "datetime": ts.Format(keitaroTimeLayout), "device_type": "desktop"})
	}
	total := len(rows)
	end := body.Offset + 1000
	if end > total {
		end = total
	}
	page := []map[string]any{}
	if body.Offset < total {
		page = rows[body.Offset:end]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"rows": page, "total": total})
}

// TestKeitaroResumeContinuesClicks breaks a click import after two written batches and resumes it:
// the resume reads from the last written click time, every click lands once, and the journal holds no key.
func TestKeitaroResumeContinuesClicks(t *testing.T) {
	key := "fixture-key"
	fake := &clickLogFake{n: 2500}
	fake.broken.Store(true)
	tracker := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tracker.Close)
	sink := &fakeSink{}
	path := filepath.Join(t.TempDir(), "keitaro-job.json")
	s, admin := newKeitaroServers(t, tracker.URL, key, nil, sink, &keitaroFiles{jobFile: path})

	raw, code := keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{"resume": true})
	if code != http.StatusConflict || !strings.Contains(string(raw), keitaroNothingToResumeMsg) {
		t.Fatalf("resume before any run %d %s", code, raw)
	}
	postKeitaroImport(t, admin, map[string]any{"clicks": true, "from": "2026-10-01", "to": "2026-10-01"})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "failed" {
		t.Fatalf("first run %+v", job)
	}
	view := s.ktJob.snapshot()
	if !view.Resumable || view.LastStage != "clicks" || view.Logs["clicks"].Rows != 2000 || view.Logs["clicks"].Through != "2026-10-01 00:11:06" {
		t.Fatalf("after break %+v", view)
	}
	if len(sink.events) != 2000 {
		t.Fatalf("events after break %d", len(sink.events))
	}
	raw, code = keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{"resume": true, "clicks": true})
	if code != http.StatusBadRequest {
		t.Fatalf("resume with flags %d %s", code, raw)
	}
	postKeitaroImport(t, admin, map[string]any{"resume": true})
	job = waitKeitaroJob(t, admin)
	if job.Stage != "done" || job.Error != "" {
		t.Fatalf("resumed run %+v", job)
	}
	fake.mu.Lock()
	last := fake.froms[len(fake.froms)-1]
	fake.mu.Unlock()
	if last != "2026-10-01 00:11:06" {
		t.Fatalf("resume range from %q", last)
	}
	seen := map[string]int{}
	for _, ev := range sink.events {
		seen[ev.CID]++
	}
	if len(seen) != 2500 || len(sink.events) != 2500 {
		t.Fatalf("unique %d events %d", len(seen), len(sink.events))
	}
	view = s.ktJob.snapshot()
	if view.Resumable || view.Resumes != 1 || !view.Logs["clicks"].Done || view.Counts["clicks"] < 2500 {
		t.Fatalf("after resume %+v", view)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), key) || strings.Contains(string(b), tracker.URL) || !strings.Contains(string(b), `"stage": "done"`) {
		t.Fatalf("journal %s", b)
	}
}

// timeoutNetErr is a network timeout for keitaroDeadlineErr.
type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "read tcp 10.0.0.1:443: i/o timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }

// withKeitaroDeadlineBudget shortens one attempt and the pause between attempts for a test.
// limit is the context of one attempt. pause is the wait before the same import continues.
func withKeitaroDeadlineBudget(t *testing.T, limit, pause time.Duration) {
	t.Helper()
	prevLimit, prevPause := keitaroJobTimeout, keitaroDeadlinePause
	keitaroJobTimeout = limit
	keitaroDeadlinePause = pause
	t.Cleanup(func() {
		keitaroJobTimeout = prevLimit
		keitaroDeadlinePause = prevPause
	})
}

// TestKeitaroDeadlineIsNotCancel checks that a deadline and a network timeout continue,
// and that a cancel and an admin restart do not.
func TestKeitaroDeadlineIsNotCancel(t *testing.T) {
	if !keitaroDeadlineErr(context.DeadlineExceeded) {
		t.Fatal("deadline")
	}
	if !keitaroDeadlineErr(timeoutNetErr{}) {
		t.Fatal("i/o timeout")
	}
	if keitaroDeadlineErr(context.Canceled) || keitaroTimeoutText(keitaroInterruptedMsg) || keitaroTimeoutText("context canceled") {
		t.Fatal("cancel or restart counted as a timeout")
	}
	prod := "keitaro: POST /admin_api/v1/clicks/log не выполнен: context deadline exceeded"
	if !keitaroTimeoutText(prod) {
		t.Fatal("prod text")
	}
	j := newKeitaroJob()
	j.rec = failedClickJournal("context canceled")
	if _, ok := j.claimStartupResume(); ok {
		t.Fatal("cancel claimed")
	}
	j.rec = failedClickJournal(prod)
	j.rec.Interrupted = true
	if _, ok := j.claimStartupResume(); ok {
		t.Fatal("interrupted claimed")
	}
	if msg := j.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	j.setRequest(keitaroImportReq{Clicks: true, From: "2026-10-01", To: "2026-10-01"})
	j.progress("clicks", 1, 2)
	j.rec.Interrupted = true
	if j.keepAfterDeadline(prod, true) {
		t.Fatal("interrupted run kept")
	}
}

// failedClickJournal is a stopped click import with the given error.
func failedClickJournal(errMsg string) keitaroJournal {
	return keitaroJournal{
		Stage: "failed", LastStage: "clicks", Error: errMsg, Resumable: true,
		StartedAt: "2026-11-22T12:31:00Z", FinishedAt: "2026-11-22T14:31:00Z",
		Request: keitaroImportReq{Clicks: true, From: "2026-10-01", To: "2026-10-01"},
		Counts:  map[string]int{}, Losses: nil, Logs: map[string]keitaroLogMark{"clicks": {Rows: 10, Through: "2026-10-01 00:00:03"}},
	}
}

// TestKeitaroDeadlineHoldStaysRunning checks that a timeout leaves the same import running
// and not finished, and that a second claim does not start another one.
func TestKeitaroDeadlineHoldStaysRunning(t *testing.T) {
	prod := "keitaro: POST /admin_api/v1/clicks/log не выполнен: context deadline exceeded"
	j := newKeitaroJob()
	if msg := j.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	j.setRequest(keitaroImportReq{Clicks: true, From: "2026-10-01", To: "2026-10-01"})
	j.progress("clicks", 1, 2)
	through := time.Date(2026, 10, 1, 0, 11, 6, 0, time.UTC)
	j.markLog("clicks", 1000, through, false)
	if !j.keepAfterDeadline(prod, true) {
		t.Fatal("deadline not kept")
	}
	v := j.snapshot()
	if !v.Running || v.Stage == "failed" || v.Stage == "done" || v.Error == "" || v.Resumable {
		t.Fatalf("while retrying %+v", v)
	}
	next, ok := j.deadlineContinuation()
	if !ok || next.clicksFrom != "2026-10-01 00:11:06" || next.clicksRows != 1000 {
		t.Fatalf("continuation %+v %v", next, ok)
	}
	if _, ok := j.claimStartupResume(); ok {
		t.Fatal("second claim while running")
	}
	if j.keepAfterDeadline("context canceled", false) {
		t.Fatal("cancel kept")
	}
}

// deadlineClickFake serves n clicks and then holds the next blocks click reads until the caller
// context ends, stop is closed, or the hold passes. The hold is long enough for the import
// context to die first. stop lets the test close the server without waiting on that hold.
type deadlineClickFake struct {
	n      int
	blocks atomic.Int32
	served atomic.Int32
	stop   <-chan struct{}
}

func (f *deadlineClickFake) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/admin_api/v1/clicks/log" {
		_, _ = w.Write([]byte(`[]`))
		return
	}
	if f.served.Load() > 0 && f.blocks.Load() > 0 {
		f.blocks.Add(-1)
		timer := time.NewTimer(8 * time.Second)
		select {
		case <-r.Context().Done():
			timer.Stop()
		case <-f.stop:
			timer.Stop()
		case <-timer.C:
		}
		w.WriteHeader(http.StatusGatewayTimeout)
		return
	}
	f.served.Add(1)
	var body struct {
		Range  struct{ From string } `json:"range"`
		Offset int                   `json:"offset"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	from, _ := time.Parse(keitaroTimeLayout, body.Range.From)
	var rows []map[string]any
	for i := 0; i < f.n; i++ {
		ts := start.Add(time.Duration(i/3) * time.Second)
		if !from.IsZero() && ts.Before(from) {
			continue
		}
		rows = append(rows, map[string]any{"sub_id": fmt.Sprintf("c%04d", i), "datetime": ts.Format(keitaroTimeLayout), "device_type": "desktop"})
	}
	total := len(rows)
	end := body.Offset + 1000
	if end > total {
		end = total
	}
	page := []map[string]any{}
	if body.Offset < total {
		page = rows[body.Offset:end]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"rows": page, "total": total})
}

// TestKeitaroDeadlineContinuesClicks checks that a context deadline on clicks/log does not
// finish the import, does not stop it, and does not write a click twice.
func TestKeitaroDeadlineContinuesClicks(t *testing.T) {
	withKeitaroDeadlineBudget(t, 5*time.Second, 20*time.Millisecond)
	const n = 1500
	stop := make(chan struct{})
	fake := &deadlineClickFake{n: n, stop: stop}
	fake.blocks.Store(2)
	tracker := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tracker.Close)
	t.Cleanup(func() { close(stop) })
	sink := &fakeSink{}
	s, admin := newKeitaroServers(t, tracker.URL, "fixture-key", nil, sink, nil)
	postKeitaroImport(t, admin, map[string]any{"clicks": true, "from": "2026-10-01", "to": "2026-10-01"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stopped atomic.Bool
	go func() {
		seen := false
		for {
			if ctx.Err() != nil {
				return
			}
			v := s.ktJob.snapshot()
			if v.Running {
				seen = true
			}
			if seen && !v.Running && v.Stage != "done" {
				stopped.Store(true)
				return
			}
			if !v.Running && v.Stage == "done" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	job := waitKeitaroJobFor(t, admin, 45*time.Second)
	if stopped.Load() || job.Stage != "done" || job.Error != "" || job.Running {
		t.Fatalf("job %+v stopped %v", job, stopped.Load())
	}
	if fake.blocks.Load() != 0 {
		t.Fatalf("deadlines left %d", fake.blocks.Load())
	}
	seen := map[string]int{}
	for _, ev := range sink.events {
		seen[ev.CID]++
	}
	if len(seen) != n || len(sink.events) != n {
		t.Fatalf("unique %d events %d", len(seen), len(sink.events))
	}
	for cid, n := range seen {
		if n != 1 {
			t.Fatalf("click %s written %d times", cid, n)
		}
	}
	view := s.ktJob.snapshot()
	if view.Resumes < 2 || view.Stage != "done" {
		t.Fatalf("resumes %+v", view)
	}
}

// TestKeitaroDeadlinePickup continues a journal already stopped by a deadline when admin starts,
// and leaves an interrupted one stopped.
func TestKeitaroDeadlinePickup(t *testing.T) {
	prod := "keitaro: POST /admin_api/v1/clicks/log не выполнен: context deadline exceeded"
	dir := t.TempDir()
	path := filepath.Join(dir, "keitaro-job.json")
	writeDeadlineJournal(t, path, false, prod)
	fake := &clickLogFake{n: 3}
	tracker := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tracker.Close)
	sink := &fakeSink{}
	s, admin := newKeitaroServers(t, tracker.URL, "fixture-key", nil, sink, &keitaroFiles{dir: dir, jobFile: path})
	job := waitKeitaroJobFor(t, admin, 15*time.Second)
	if job.Stage != "done" || job.Error != "" || job.Running {
		t.Fatalf("picked up %+v", job)
	}
	if len(sink.events) != 3 {
		t.Fatalf("events %d", len(sink.events))
	}
	seen := map[string]int{}
	for _, ev := range sink.events {
		seen[ev.CID]++
	}
	if len(seen) != 3 {
		t.Fatalf("unique %d", len(seen))
	}
	if s.ktJob.snapshot().Resumes != 1 {
		t.Fatalf("resumes %+v", s.ktJob.snapshot())
	}

	cutPath := filepath.Join(t.TempDir(), "keitaro-job.json")
	writeDeadlineJournal(t, cutPath, true, prod)
	cutFake := &clickLogFake{n: 3}
	cutTracker := httptest.NewServer(http.HandlerFunc(cutFake.handler))
	t.Cleanup(cutTracker.Close)
	cutSink := &fakeSink{}
	cut, _ := newKeitaroServers(t, cutTracker.URL, "fixture-key", nil, cutSink, &keitaroFiles{jobFile: cutPath})
	v := cut.ktJob.snapshot()
	if v.Running || v.Stage != "failed" || !v.Interrupted || len(cutSink.events) != 0 {
		t.Fatalf("interrupted started %+v events %d", v, len(cutSink.events))
	}
}

// TestCarriedImportLossesDropsCatalogCopies checks that a resumed catalog map does not keep the
// previous loss report, and that a click continuation does.
func TestCarriedImportLossesDropsCatalogCopies(t *testing.T) {
	prev := []ktimport.Loss{{Kind: "approximated", Ref: "a", Detail: "фильтр isp перенесён приближённо"}}
	if got := carriedImportLosses(keitaroImportReq{Campaigns: true, Offers: true}, prev); got != nil {
		t.Fatalf("catalog resume kept %v", got)
	}
	got := carriedImportLosses(keitaroImportReq{Clicks: true}, prev)
	if len(got) != 1 || got[0].Ref != "a" {
		t.Fatalf("click resume %v", got)
	}
}

// TestProgressSnapshotKeepsGroupCursor checks that a snapshot probe of 0 of 0 does not wipe
// the campaign-write cursor, and that a resumed run keeps the ids already written.
func TestProgressSnapshotKeepsGroupCursor(t *testing.T) {
	j := newKeitaroJob()
	j.running = true
	j.progress("groups", 4, 10)
	j.progress("snapshot", 0, 0)
	if v := j.snapshot(); v.Stage != "snapshot" || v.Done != 4 || v.Total != 10 {
		t.Fatalf("cursor reset %+v", v)
	}
	j.noteGroupsSaved([]string{"a"}, 5, 10)
	j.mu.Lock()
	j.rec.Request.Campaigns = true
	j.rec.Losses = []ktimport.Loss{{Kind: "approximated", Detail: "снова"}}
	run := j.rec.resumeRun()
	j.mu.Unlock()
	if v := j.snapshot(); v.Done != 5 || v.Total != 10 || v.Stage != "groups" {
		t.Fatalf("after save %+v", v)
	}
	ids := j.savedGroupIDs()
	if len(ids) != 1 || ids[0] != "a" || len(run.groupsSaved) != 1 || run.groupsSaved[0] != "a" || run.losses != nil {
		t.Fatalf("resume ids %v losses %v", run.groupsSaved, run.losses)
	}
}

// writeDeadlineJournal writes a stopped click import. interrupted marks an admin restart.
func writeDeadlineJournal(t *testing.T, path string, interrupted bool, errMsg string) {
	t.Helper()
	rec := failedClickJournal(errMsg)
	rec.Interrupted = interrupted
	rec.Running = false
	rec.Logs = map[string]keitaroLogMark{"clicks": {}}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
