package admin

import (
	"bufio"
	"encoding/json"
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

// TestKeitaroStreamsStoreRoundTrip checks that streams written by one attempt are read by the next,
// and that the file holds no tracker host or key.
func TestKeitaroStreamsStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keitaro-job-streams.jsonl")
	st := openKeitaroStreams(path, "tracker.example", time.Now(), nil)
	if len(st.known) != 0 {
		t.Fatalf("fresh store %v", st.known)
	}
	st.add("7", []byte(`[{"id":3,"name":"main","filters":[{"name":"country","payload":["ZA"]}]}]`))
	st.add("8", []byte(`[]`))
	st.close()

	back := openKeitaroStreams(path, "tracker.example", time.Now(), nil)
	defer back.close()
	if len(back.known) != 2 || len(back.known["7"]) != 1 || back.known["7"][0].Name != "main" || len(back.known["7"][0].Filters) != 1 {
		t.Fatalf("known %+v", back.known)
	}
	if _, ok := back.known["8"]; !ok {
		t.Fatal("empty streams of a campaign lost")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tracker.example") {
		t.Fatalf("host in file %s", raw)
	}
}

// TestKeitaroStreamsStoreDiscardsForeignAndOld checks that a file of another host or older
// than keitaroStreamsMaxAge is not reused and is removed.
func TestKeitaroStreamsStoreDiscardsForeignAndOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keitaro-job-streams.jsonl")
	st := openKeitaroStreams(path, "tracker.example", time.Now(), nil)
	st.add("7", []byte(`[]`))
	st.close()

	other := openKeitaroStreams(path, "other.example", time.Now(), nil)
	if len(other.known) != 0 {
		t.Fatalf("foreign host reused %v", other.known)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("foreign file kept: %v", err)
	}

	st = openKeitaroStreams(path, "tracker.example", time.Now(), nil)
	st.add("7", []byte(`[]`))
	st.close()
	old := openKeitaroStreams(path, "tracker.example", time.Now().Add(keitaroStreamsMaxAge+time.Hour), nil)
	if len(old.known) != 0 {
		t.Fatalf("old file reused %v", old.known)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("old file kept: %v", err)
	}
}

// TestKeitaroStreamsStoreCutTail checks that a line cut by a crash is dropped, the whole lines
// before it are kept, and the next write starts on a clean line.
func TestKeitaroStreamsStoreCutTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keitaro-job-streams.jsonl")
	st := openKeitaroStreams(path, "tracker.example", time.Now(), nil)
	st.add("1", []byte(`[]`))
	st.add("2", []byte(`[]`))
	st.close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"3","streams":[{"id"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	back := openKeitaroStreams(path, "tracker.example", time.Now(), nil)
	if len(back.known) != 2 {
		t.Fatalf("known after cut %v", back.known)
	}
	back.add("3", []byte(`[]`))
	back.close()
	again := openKeitaroStreams(path, "tracker.example", time.Now(), nil)
	defer again.close()
	if len(again.known) != 3 {
		t.Fatalf("known after rewrite %v", again.known)
	}
}

// TestKeitaroStreamsDroppedByJob checks that a new import, passed catalogs, a finished run and a
// purge remove the streams file, and that a failed resumable run keeps it.
func TestKeitaroStreamsDroppedByJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keitaro-job.json")
	j := loadKeitaroJob(path, nil)
	streams := keitaroStreamsPath(path)
	put := func() {
		t.Helper()
		st := openKeitaroStreams(streams, "tracker.example", time.Now(), nil)
		st.add("1", []byte(`[]`))
		st.close()
	}
	exists := func() bool { _, err := os.Stat(streams); return err == nil }

	put()
	if msg := j.beginImport(); msg != "" || exists() {
		t.Fatalf("new import kept the file: %q %v", msg, exists())
	}
	j.setRequest(keitaroImportReq{Offers: true, Clicks: true, From: "2026-10-01", To: "2026-10-01"})
	put()
	j.finish("keitaro: GET /admin_api/v1/campaigns/1/streams не выполнен: i/o timeout", nil, nil, "")
	if !j.snapshot().Resumable || !exists() {
		t.Fatalf("failed resumable run lost the file: %+v", j.snapshot())
	}
	if _, msg := j.beginResume(); msg != "" {
		t.Fatal(msg)
	}
	j.catalogsPassed()
	if exists() {
		t.Fatal("file kept after catalogs passed")
	}
	put()
	j.finish("", nil, nil, "")
	if exists() {
		t.Fatal("file kept after the run finished")
	}
	put()
	j.dropResume()
	if exists() {
		t.Fatal("file kept after purge")
	}
}

// streamsFake is a tracker with five campaigns whose last one fails its streams once.
// It counts the streams requests per campaign.
type streamsFake struct {
	mu     sync.Mutex
	hits   map[string]int
	broken atomic.Bool
}

// handler answers the paths of a catalog import. The streams of campaign 5 wait a moment, so the
// other campaigns finish first, and then answer 502 while broken is set.
func (f *streamsFake) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/admin_api/v1/campaigns":
		_, _ = w.Write([]byte(`[{"id":1,"alias":"a","name":"A"},{"id":2,"alias":"b","name":"B"},{"id":3,"alias":"c","name":"C"},{"id":4,"alias":"d","name":"D"},{"id":5,"alias":"e","name":"E"}]`))
	case strings.HasPrefix(r.URL.Path, "/admin_api/v1/campaigns/") && strings.HasSuffix(r.URL.Path, "/streams"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/admin_api/v1/campaigns/"), "/streams")
		f.mu.Lock()
		f.hits[id]++
		f.mu.Unlock()
		if id == "5" && f.broken.CompareAndSwap(true, false) {
			time.Sleep(150 * time.Millisecond)
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

// savedStreamIDs lists the campaign ids in a streams file.
func savedStreamIDs(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		var line keitaroStreamsLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		out[line.ID] = true
	}
	return out
}

// TestKeitaroSnapshotResumeKeepsStreams breaks the read of the last campaign streams and starts the
// same import again: the campaigns already read are not requested a second time, the offer lands once,
// the run is the same run (one resume, not a second job) and the streams file goes away once the catalogs pass.
func TestKeitaroSnapshotResumeKeepsStreams(t *testing.T) {
	fake := &streamsFake{hits: map[string]int{}}
	fake.broken.Store(true)
	tracker := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tracker.Close)
	path := filepath.Join(t.TempDir(), "keitaro-job.json")
	dir := t.TempDir()
	s, admin := newKeitaroServers(t, tracker.URL, "fixture-key", nil, &fakeSink{}, &keitaroFiles{dir: dir, jobFile: path})
	body := map[string]any{"offers": true}

	postKeitaroImport(t, admin, body)
	job := waitKeitaroJob(t, admin)
	if job.Stage != "failed" {
		t.Fatalf("first run %+v", job)
	}
	view := s.ktJob.snapshot()
	if !view.Resumable || view.LastStage != "streams" {
		t.Fatalf("after break %+v", view)
	}
	saved := savedStreamIDs(t, keitaroStreamsPath(path))
	if len(saved) != 4 || saved["5"] {
		t.Fatalf("saved streams %v", saved)
	}
	fake.mu.Lock()
	before := map[string]int{}
	for id, n := range fake.hits {
		before[id] = n
	}
	fake.mu.Unlock()

	postKeitaroImport(t, admin, body)
	job = waitKeitaroJob(t, admin)
	if job.Stage != "done" || job.Error != "" {
		t.Fatalf("second run %+v", job)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for id := range saved {
		if fake.hits[id] != before[id] {
			t.Fatalf("campaign %s asked again: %d -> %d", id, before[id], fake.hits[id])
		}
	}
	if fake.hits["5"] != 2 {
		t.Fatalf("campaign 5 hits %d", fake.hits["5"])
	}
	view = s.ktJob.snapshot()
	if view.Resumes != 1 || view.Resumable {
		t.Fatalf("after resume %+v", view)
	}
	if _, err := os.Stat(keitaroStreamsPath(path)); !os.IsNotExist(err) {
		t.Fatalf("streams file kept after done: %v", err)
	}
	offers := readOffers(t, filepath.Join(dir, "offers.json"))
	var n int
	for _, o := range offers {
		if o.ID == "4" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("offer 4 stored %d times: %+v", n, offers)
	}
}
