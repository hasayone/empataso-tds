package admin

import (
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
