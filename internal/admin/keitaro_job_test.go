package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/emtds/bridge/ktimport"
)

func TestKeitaroImportConflict(t *testing.T) {
	key := "fixture-key"
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/log") {
			_, _ = w.Write([]byte(`{"rows":[],"total":0}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(fake.Close)
	admin := newKeitaroAdmin(t, fake.URL, key, nil, nil, nil)
	t.Cleanup(free)
	raw := postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	if job := readKeitaroJob(t, admin); !job.Running {
		t.Fatalf("expected a running job, got %+v", job)
	}
	raw, code := keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{"campaigns": true})
	if code != http.StatusConflict || !strings.Contains(string(raw), "import already running") || strings.Contains(string(raw), key) {
		t.Fatalf("second import %d %s", code, raw)
	}
	raw, code = keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{"campaign_id": 7})
	if code != http.StatusConflict || !strings.Contains(string(raw), "import already running") {
		t.Fatalf("campaign dosync during import %d %s", code, raw)
	}
	raw, code = keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusConflict || !strings.Contains(string(raw), "import already running") || strings.Contains(string(raw), key) {
		t.Fatalf("purge %d %s", code, raw)
	}
	free()
	job := waitKeitaroJob(t, admin)
	if job.Running || job.Stage != "done" || strings.Contains(job.Error, key) {
		t.Fatalf("after release %+v", job)
	}
}

func TestKeitaroImportFailed(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin_api/v1/campaigns" {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(fake.Close)
	admin := newKeitaroAdmin(t, fake.URL, key, nil, nil, nil)
	raw := postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	job := waitKeitaroJob(t, admin)
	if job.Running || job.Stage != "failed" || job.Error == "" || strings.Contains(job.Error, key) {
		t.Fatalf("job %+v", job)
	}
}

func TestKeitaroBadDatesStayIdle(t *testing.T) {
	key := "fixture-key"
	admin := newKeitaroAdmin(t, "http://tracker.example", key, nil, nil, nil)
	raw, code := keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{
		"clicks": true, "from": "yesterday", "to": "2026-10-15",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad range %d %s", code, raw)
	}
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	job := readKeitaroJob(t, admin)
	if job.Running || job.Stage != "idle" || job.Error != "" || job.StartedAt != "" || job.FinishedAt != "" {
		t.Fatalf("job %+v", job)
	}
}

func TestKeitaroPurgeBusy(t *testing.T) {
	key := "fixture-key"
	s, admin := newKeitaroServers(t, "http://tracker.example", key, nil, nil, nil)
	if msg := s.ktJob.beginPurge(); msg != "" {
		t.Fatal(msg)
	}
	raw, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusConflict || !strings.Contains(string(raw), "purge already running") || strings.Contains(string(raw), key) {
		t.Fatalf("purge %d %s", code, raw)
	}
	raw, code = keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{"offers": true})
	if code != http.StatusConflict || !strings.Contains(string(raw), "purge already running") || strings.Contains(string(raw), key) {
		t.Fatalf("import %d %s", code, raw)
	}
	s.ktJob.endPurge()
	raw, code = keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK || strings.Contains(string(raw), key) {
		t.Fatalf("purge after unlock %d %s", code, raw)
	}
	job := readKeitaroJob(t, admin)
	if job.Running || job.Stage != "idle" {
		t.Fatalf("job %+v", job)
	}
}

// TestRunStageStepErrorKeepsLosses checks a step that returns an error.
// t is the test handle. The step loss is kept and the counts key stays unset.
// The helper returns false and st.fail is "save error". It expects no failure of the test itself.
func TestRunStageStepErrorKeepsLosses(t *testing.T) {
	s := &Server{ktJob: newKeitaroJob()}
	if msg := s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	st := &importState{
		counts: map[string]int{"offers": 2},
		losses: []ktimport.Loss{{Kind: "drop", Ref: "old", Detail: "kept"}},
	}
	stepLoss := ktimport.Loss{Kind: "skip", Ref: "offer-1", Detail: "disk"}
	ok := s.runStage(st, "lists", "lists", func() (int, []ktimport.Loss, error) {
		return 4, []ktimport.Loss{stepLoss}, errors.New("disk")
	})
	if ok {
		t.Fatal("expected false")
	}
	if st.fail != "save error" {
		t.Fatalf("fail %q", st.fail)
	}
	if _, has := st.counts["lists"]; has || st.counts["offers"] != 2 {
		t.Fatalf("counts %+v", st.counts)
	}
	if len(st.losses) != 2 || st.losses[0].Ref != "old" || st.losses[1] != stepLoss {
		t.Fatalf("losses %+v", st.losses)
	}
	job := s.ktJob.snapshot()
	if job.Stage != "lists" || job.Error != "" || len(job.Counts) != 0 || len(job.Losses) != 0 {
		t.Fatalf("job %+v", job)
	}
}

// TestRunStageSuccessPublishesCount checks a step that saves.
// t is the test handle. The second step must see the result written by the first.
// The helper returns true, sets both counts, and publishes masked losses.
// It expects no failure of the test itself.
func TestKeitaroJobSyncRunningField(t *testing.T) {
	s, admin := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, nil)
	if msg := s.ktJob.beginSync(); msg != "" {
		t.Fatal(msg)
	}
	defer s.ktJob.finishSync()
	job := readKeitaroJob(t, admin)
	if !job.SyncRunning || job.Running {
		t.Fatalf("job %+v", job)
	}
}

func TestRunStageSuccessPublishesCount(t *testing.T) {
	s := &Server{ktJob: newKeitaroJob()}
	if msg := s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	st := &importState{counts: map[string]int{}, losses: []ktimport.Loss{}, creds: ktCreds{Key: "stage-key"}}
	ok := s.runStage(st, "lists", "lists", func() (int, []ktimport.Loss, error) {
		st.res.Lists = map[string]string{"a": "1"}
		return 1, []ktimport.Loss{{Kind: "skip", Ref: "a", Detail: "stage-key"}}, nil
	}) && s.runStage(st, "pages", "pages", func() (int, []ktimport.Loss, error) {
		if st.res.Lists["a"] != "1" {
			return 0, nil, errors.New("stale res")
		}
		return 2, nil, nil
	})
	if !ok || st.fail != "" || st.counts["lists"] != 1 || st.counts["pages"] != 2 {
		t.Fatalf("ok %v fail %q counts %+v", ok, st.fail, st.counts)
	}
	job := s.ktJob.snapshot()
	if job.Stage != "pages" || job.Counts["lists"] != 1 || job.Counts["pages"] != 2 {
		t.Fatalf("job %+v", job)
	}
	if len(job.Losses) != 1 || job.Losses[0].Detail != "***" {
		t.Fatalf("losses %+v", job.Losses)
	}
}
