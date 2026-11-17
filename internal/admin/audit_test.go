package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// auditNow is the fixed time of the audit tests.
var auditNow = time.Date(2026, 11, 15, 10, 30, 0, 0, time.UTC)

// auditRig is a key rig whose audit file lives in a temporary data directory
// and whose clock and log are under the test's control.
type auditRig struct {
	*keyRig
	audit string
	log   *recordingLogger
}

// newAuditRig starts a key rig with the audit log on a fixed clock.
func newAuditRig(t *testing.T) *auditRig {
	t.Helper()
	rig := newKeyRig(t, richStats{})
	data := t.TempDir()
	rig.s.audit = newAuditLog(data)
	rig.s.audit.now = func() time.Time { return auditNow }
	lg := &recordingLogger{}
	rig.s.cfg.Log = lg
	return &auditRig{keyRig: rig, audit: filepath.Join(data, auditSubdir), log: lg}
}

// lines returns the audit lines of the November file decoded in order.
func (a *auditRig) lines(t *testing.T) []auditEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(a.audit, "admin-2026-11.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []auditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e auditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestAuditWritesPostByKey(t *testing.T) {
	rig := newAuditRig(t)
	id, raw := rig.newKey(t, security.KeyAccess{})
	offer := config.Offer{ID: "o1", Name: "bodymarker", URL: "https://n.example/", Status: true}
	if code, body := rig.call(t, raw, http.MethodPost, "/api/offers?token=querymarker", offer); code != http.StatusCreated {
		t.Fatalf("create offer: %d %s", code, body)
	}
	got := rig.lines(t)
	if len(got) != 1 {
		t.Fatalf("audit lines: %d", len(got))
	}
	e := got[0]
	if e.KeyID != id || e.Method != http.MethodPost || e.Route != "POST /api/offers" || e.Status != http.StatusCreated {
		t.Fatalf("audit entry: %+v", e)
	}
	if e.IP == "" {
		t.Fatal("audit entry has no ip")
	}
	if ts, err := time.Parse(time.RFC3339Nano, e.TS); err != nil || !ts.Equal(auditNow) {
		t.Fatalf("ts %q: %v", e.TS, err)
	}
	b, err := os.ReadFile(filepath.Join(rig.audit, "admin-2026-11.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"bodymarker", "querymarker", raw, "/api/offers?"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("audit file holds %q: %s", secret, b)
		}
	}
	logged := strings.Join(rig.log.lines(), "\n")
	if !strings.Contains(logged, "api key write") || !strings.Contains(logged, "POST /api/offers") ||
		strings.Contains(logged, raw) || strings.Contains(logged, "querymarker") {
		t.Fatalf("slog mirror: %s", logged)
	}
}

func TestAuditSkipsReadsAndCookie(t *testing.T) {
	rig := newAuditRig(t)
	_, raw := rig.newKey(t, security.KeyAccess{})
	if code, _ := rig.call(t, raw, http.MethodGet, "/api/offers", nil); code != http.StatusOK {
		t.Fatalf("read offers: %d", code)
	}
	offer := config.Offer{ID: "o2", Name: "cookie", URL: "https://n.example/", Status: true}
	if code, body := rig.asAdmin(t, http.MethodPost, "/api/offers", offer); code != http.StatusCreated {
		t.Fatalf("cookie create: %d %s", code, body)
	}
	if got := rig.lines(t); len(got) != 0 {
		t.Fatalf("audit lines for a read and a cookie write: %+v", got)
	}
	if strings.Contains(strings.Join(rig.log.lines(), "\n"), "api key write") {
		t.Fatal("slog line for a read or a cookie session")
	}
}

func TestAuditRecordsRefusedWrite(t *testing.T) {
	rig := newAuditRig(t)
	_, raw := rig.newKey(t, security.KeyAccess{})
	if code, _ := rig.call(t, raw, http.MethodDelete, "/api/logs?group=g1", nil); code != http.StatusForbidden {
		t.Fatalf("delete logs: %d", code)
	}
	got := rig.lines(t)
	if len(got) != 1 || got[0].Route != "DELETE /api/logs" || got[0].Status != http.StatusForbidden {
		t.Fatalf("refused write: %+v", got)
	}
}

func TestAuditFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	rig := newAuditRig(t)
	_, raw := rig.newKey(t, security.KeyAccess{})
	offer := config.Offer{ID: "o3", Name: "n", URL: "https://n.example/", Status: true}
	rig.call(t, raw, http.MethodPost, "/api/offers", offer)
	di, err := os.Stat(rig.audit)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir: %v %v", di, err)
	}
	fi, err := os.Stat(filepath.Join(rig.audit, "admin-2026-11.jsonl"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
}

func TestAuditDisabledWithoutDataDir(t *testing.T) {
	s := &Server{audit: newAuditLog("")}
	if s.audit.enabled() {
		t.Fatal("audit enabled without a data dir")
	}
	rec := httptest.NewRecorder()
	h := s.auditWrites("POST /x", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	h.ServeHTTP(rec, apiRequest(http.MethodPost, "/x"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
}

// apiRequest returns a request whose context holds an API-key session.
func apiRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	sess := security.Session{API: true, KeyID: "key1", KeyType: security.KeyTypeAdmin}
	return req.WithContext(context.WithValue(req.Context(), sessionKey, sess))
}

func TestAuditRetentionRemovesOldFiles(t *testing.T) {
	data := t.TempDir()
	a := newAuditLog(data)
	dir := filepath.Join(data, auditSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{
		"admin-2025-12.jsonl", "admin-2026-04.jsonl", "admin-2026-05.jsonl",
		"admin-2026-11.jsonl", "notes.txt", "admin-bad.jsonl",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := a.prune(auditNow)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed %v: %v", removed, err)
	}
	for n, want := range map[string]bool{
		"admin-2025-12.jsonl": false, "admin-2026-04.jsonl": false, "admin-2026-05.jsonl": true,
		"admin-2026-11.jsonl": true, "notes.txt": true, "admin-bad.jsonl": true,
	} {
		_, err := os.Stat(filepath.Join(dir, n))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", n, err == nil, want)
		}
	}
}

func TestAuditRetentionMissingDir(t *testing.T) {
	a := newAuditLog(t.TempDir())
	if removed, err := a.prune(auditNow); err != nil || len(removed) != 0 {
		t.Fatalf("missing dir: %v %v", removed, err)
	}
}

func TestAuditWatchPrunesAtStartAndStops(t *testing.T) {
	data := t.TempDir()
	s := &Server{audit: newAuditLog(data)}
	s.audit.now = func() time.Time { return auditNow }
	dir := filepath.Join(data, auditSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "admin-2025-01.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.WatchAudit(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old audit file was not removed at start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchAudit did not stop")
	}
}

// flushProbe is a ResponseWriter that is a Flusher and counts flushes.
type flushProbe struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushProbe) Flush() {
	f.flushes++
	f.ResponseRecorder.Flush()
}

func TestAuditStatusWriterFlushesStream(t *testing.T) {
	s := &Server{audit: newAuditLog(t.TempDir())}
	probe := &flushProbe{ResponseRecorder: httptest.NewRecorder()}
	h := s.auditWrites("POST /stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("wrapped writer is not a Flusher")
			return
		}
		_, _ = w.Write([]byte("a,b\n"))
		f.Flush()
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController flush: %v", err)
		}
		_, _ = w.Write([]byte("c,d\n"))
	}))
	h.ServeHTTP(probe, apiRequest(http.MethodPost, "/stream"))
	if probe.flushes != 2 {
		t.Fatalf("flushes reached the underlying writer: %d", probe.flushes)
	}
	if got := probe.Body.String(); got != "a,b\nc,d\n" {
		t.Fatalf("body %q", got)
	}
	b, err := os.ReadFile(s.audit.monthFile(time.Now()))
	if err != nil || !strings.Contains(string(b), `"status":200`) || !strings.Contains(string(b), `"route":"POST /stream"`) {
		t.Fatalf("audit file %q: %v", b, err)
	}
}

func TestAuditStatusWriterRecordsStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	sw.WriteHeader(http.StatusContinue)
	sw.WriteHeader(http.StatusConflict)
	if sw.code() != http.StatusConflict {
		t.Fatalf("code %d", sw.code())
	}
	if (&statusWriter{ResponseWriter: httptest.NewRecorder()}).code() != http.StatusOK {
		t.Fatal("a handler that wrote nothing must count as 200")
	}
	if sw.Unwrap() != http.ResponseWriter(rec) {
		t.Fatal("Unwrap must return the wrapped writer")
	}
}

func TestAuditWritesLineWhenHandlerPanics(t *testing.T) {
	s := &Server{audit: newAuditLog(t.TempDir())}
	h := s.auditWrites("POST /boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	func() {
		defer func() {
			if got := recover(); got != "boom" {
				t.Fatalf("panic did not reach the caller: %v", got)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), apiRequest(http.MethodPost, "/boom"))
	}()
	b, err := os.ReadFile(s.audit.monthFile(time.Now()))
	if err != nil || !strings.Contains(string(b), `"status":500`) || !strings.Contains(string(b), `"route":"POST /boom"`) {
		t.Fatalf("audit file %q: %v", b, err)
	}
}

func TestAuditPanicAfterStatusKeepsStatus(t *testing.T) {
	s := &Server{audit: newAuditLog(t.TempDir())}
	h := s.auditWrites("POST /late", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	}))
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), apiRequest(http.MethodPost, "/late"))
	}()
	b, err := os.ReadFile(s.audit.monthFile(time.Now()))
	if err != nil || !strings.Contains(string(b), `"status":202`) {
		t.Fatalf("audit file %q: %v", b, err)
	}
}

func TestAuditAssistantFieldsAreOptional(t *testing.T) {
	a := newAuditLog(t.TempDir())
	plain := auditEntry{TS: auditNow.Format(time.RFC3339Nano), KeyID: "k1", Method: http.MethodPost, Route: "POST /api/offers", Status: 201, IP: "1.2.3.4"}
	assisted := plain
	assisted.UID, assisted.Source, assisted.ChangeID = "u1", "assistant", "c1"
	if err := a.append(auditNow, plain); err != nil {
		t.Fatal(err)
	}
	if err := a.append(auditNow, assisted); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(a.monthFile(auditNow))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit lines: %q", b)
	}
	for _, key := range []string{`"uid"`, `"source"`, `"change_id"`} {
		if strings.Contains(lines[0], key) {
			t.Errorf("plain line holds %s: %s", key, lines[0])
		}
		if !strings.Contains(lines[1], key) {
			t.Errorf("assistant line lacks %s: %s", key, lines[1])
		}
	}
	var back auditEntry
	if err := json.Unmarshal([]byte(lines[1]), &back); err != nil || back != assisted {
		t.Fatalf("round trip %+v: %v", back, err)
	}
}
