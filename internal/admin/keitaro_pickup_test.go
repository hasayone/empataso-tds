package admin

import (
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

// holdFake is a tracker of five campaigns that keeps the first streams request of campaign 5 open
// until hold is closed, so a test can stop "the process" while the last campaign is being read.
// It counts the streams requests per campaign.
type holdFake struct {
	mu   sync.Mutex
	hits map[string]int
	hold chan struct{}
	held atomic.Bool
}

// handler answers the paths of a catalog import. After hold is closed the held request answers 502.
func (f *holdFake) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/admin_api/v1/campaigns":
		_, _ = w.Write([]byte(`[{"id":1,"alias":"a","name":"A"},{"id":2,"alias":"b","name":"B"},{"id":3,"alias":"c","name":"C"},{"id":4,"alias":"d","name":"D"},{"id":5,"alias":"e","name":"E"}]`))
	case strings.HasPrefix(r.URL.Path, "/admin_api/v1/campaigns/") && strings.HasSuffix(r.URL.Path, "/streams"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin_api/v1/campaigns/"), "/streams")
		f.mu.Lock()
		f.hits[id]++
		f.mu.Unlock()
		if id == "5" && f.held.CompareAndSwap(false, true) {
			select {
			case <-f.hold:
			case <-r.Context().Done():
			}
			http.Error(w, "gateway", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	case r.URL.Path == "/admin_api/v1/offers":
		_, _ = w.Write([]byte(`[{"id":4,"name":"Offer","state":"active","action_type":"http","action_payload":"https://offer.example/a"}]`))
	default:
		_, _ = w.Write([]byte(`[]`))
	}
}

// snapshotHits returns a copy of the per-campaign request counts.
func (f *holdFake) snapshotHits() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for id, n := range f.hits {
		out[id] = n
	}
	return out
}

// copyFile copies one state file for the "process that died" of a test.
func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestKeitaroRestartContinuesStreamsByItself stops the first process while the last campaign is being
// read and starts a second one on the files the first left: the second continues the same import
// with no click, asks the tracker only for the campaign that was not saved and lands the offer once.
func TestKeitaroRestartContinuesStreamsByItself(t *testing.T) {
	fake := &holdFake{hits: map[string]int{}, hold: make(chan struct{})}
	tracker := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tracker.Close)
	t.Cleanup(func() { close(fake.hold) })

	dir1 := t.TempDir()
	path1 := filepath.Join(dir1, "keitaro-job.json")
	s1, admin1 := newKeitaroServers(t, tracker.URL, "fixture-key", nil, &fakeSink{}, &keitaroFiles{dir: dir1, jobFile: path1})
	postKeitaroImport(t, admin1, map[string]any{"offers": true})

	streams1 := keitaroStreamsPath(path1)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(streams1); err == nil && len(savedStreamIDs(t, streams1)) == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("streams not saved: %+v", s1.ktJob.snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if v := s1.ktJob.snapshot(); !v.Running || v.Stage != "streams" {
		t.Fatalf("first process %+v", v)
	}

	dir2 := t.TempDir()
	path2 := filepath.Join(dir2, "keitaro-job.json")
	copyFile(t, path1, path2)
	copyFile(t, streams1, keitaroStreamsPath(path2))
	before := fake.snapshotHits()

	s2, admin2 := newKeitaroServers(t, tracker.URL, "fixture-key", nil, &fakeSink{}, &keitaroFiles{dir: dir2, jobFile: path2})
	job := waitKeitaroJob(t, admin2)
	if job.Stage != "done" || job.Error != "" {
		t.Fatalf("second process %+v", job)
	}
	after := fake.snapshotHits()
	for _, id := range []string{"1", "2", "3", "4"} {
		if after[id] != before[id] || after[id] != 1 {
			t.Fatalf("campaign %s asked again: %d -> %d", id, before[id], after[id])
		}
	}
	if after["5"] != 2 {
		t.Fatalf("campaign 5 hits %d", after["5"])
	}
	view := s2.ktJob.snapshot()
	if view.Resumes != 1 || view.Resumable || view.Interrupted || view.Running {
		t.Fatalf("after pickup %+v", view)
	}
	var n int
	for _, o := range readOffers(t, filepath.Join(dir2, "offers.json")) {
		if o.ID == "4" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("offer 4 stored %d times", n)
	}
	if _, err := os.Stat(keitaroStreamsPath(path2)); !os.IsNotExist(err) {
		t.Fatalf("streams file kept after done: %v", err)
	}
}

// restartCutJournal is a journal that a dead process left running at the given stage.
func restartCutJournal(pickups int) keitaroJournal {
	rec := idleJournal()
	rec.Stage = "failed"
	rec.LastStage = "streams"
	rec.Error = keitaroInterruptedMsg
	rec.Interrupted = true
	rec.Pickups = pickups
	rec.Request = keitaroImportReq{Offers: true, Board: "main"}
	rec.Resumable = true
	return rec
}

// TestKeitaroStartupClaim checks which stops admin continues by itself: a restart cut and a timeout,
// not a cancel, not another failure, and not a cut that already used keitaroPickupMax pickups.
// While an update is pending nothing starts, and the claim goes through once the gate opens.
func TestKeitaroStartupClaim(t *testing.T) {
	j := newKeitaroJob()
	j.rec = restartCutJournal(0)
	j.setUpdating(true)
	if _, ok := j.claimStartupResume(); ok || !j.pickupWaiting() {
		t.Fatalf("claimed under update gate or not waiting: %v", j.pickupWaiting())
	}
	j.setUpdating(false)
	run, ok := j.claimStartupResume()
	if !ok || !run.req.Offers || j.pickupWaiting() {
		t.Fatalf("claim after the gate opened: %+v %v", run, ok)
	}
	v := j.snapshot()
	if !v.Running || v.Interrupted || v.Error != "" || v.Stage != "streams" || v.Resumes != 1 {
		t.Fatalf("claimed %+v", v)
	}
	if j.rec.Pickups != 1 {
		t.Fatalf("pickups %d", j.rec.Pickups)
	}
	if _, ok := j.claimStartupResume(); ok {
		t.Fatal("second claim while running")
	}

	cases := map[string]keitaroJournal{
		"limit":  restartCutJournal(keitaroPickupMax),
		"cancel": failedClickJournal("context canceled"),
		"other":  failedClickJournal("keitaro: GET /admin_api/v1/campaigns вернул 403"),
	}
	for name, rec := range cases {
		other := newKeitaroJob()
		other.rec = rec
		if _, ok := other.claimStartupResume(); ok {
			t.Fatalf("%s claimed", name)
		}
		if other.pickupWaiting() {
			t.Fatalf("%s waits", name)
		}
	}
	timeout := newKeitaroJob()
	timeout.rec = failedClickJournal("keitaro: POST /admin_api/v1/clicks/log не выполнен: context deadline exceeded")
	if _, ok := timeout.claimStartupResume(); !ok || timeout.rec.Pickups != 0 {
		t.Fatalf("timeout claim %v pickups %d", ok, timeout.rec.Pickups)
	}
}
