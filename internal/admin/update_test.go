package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/cron"
	"github.com/egerkuzma/emtds/internal/security"
)

// --- helpers ---

// newUpdateServer creates a test server with UpdateDir and UpdateInboxDir set.
// updateDir and inboxDir may be empty to test the absent-inbox code path.
func newUpdateServer(t *testing.T, updateDir, inboxDir string) (*Server, *httptest.Server) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := New(Config{
		AdminUser:      "admin",
		PasswordHash:   hash,
		Sessions:       security.NewMemoryStore(),
		Stats:          fakeStats{},
		Groups:         &fakeGroups{},
		Limiter:        allowAll{},
		SessionTTL:     time.Hour,
		CookieSecure:   false,
		UpdateDir:      updateDir,
		UpdateInboxDir: inboxDir,
		UsersFile:      filepath.Join(dir, "users.json"),
		APIKeysFile:    filepath.Join(dir, "api-keys.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

// loginAdmin logs in as admin and returns the CSRF token and a cookie client.
func loginAdmin(t *testing.T, base string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	_, csrf := login(t, c, base, "admin", "p@ss")
	return c, csrf
}

// callJSON performs a JSON request and returns status and body bytes.
func callJSON(t *testing.T, c *http.Client, method, url, csrf, body string) (int, []byte) {
	t.Helper()
	var br *strings.Reader
	if body != "" {
		br = strings.NewReader(body)
	} else {
		br = strings.NewReader("{}")
	}
	req, err := http.NewRequest(method, url, br)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := make([]byte, 0, 256)
	buf := make([]byte, 256)
	for {
		n, readErr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if readErr != nil {
			break
		}
	}
	return resp.StatusCode, out
}

// getUpdate calls GET /api/update and decodes the response.
func getUpdate(t *testing.T, c *http.Client, base string) updateView {
	t.Helper()
	code, body := callJSON(t, c, http.MethodGet, base+"/api/update", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/update: %d %s", code, body)
	}
	var v updateView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// writeAgentFile writes an agent.json to the update directory.
func writeAgentFile(t *testing.T, dir string, a cron.AgentFile) {
	t.Helper()
	if err := cron.WriteUpdateJSON(filepath.Join(dir, cron.UpdateAgentFile), a); err != nil {
		t.Fatal(err)
	}
}

// writeCheckFile writes a check.json to the update directory.
func writeCheckFile(t *testing.T, dir string, ch cron.CheckFile) {
	t.Helper()
	if err := cron.WriteUpdateJSON(filepath.Join(dir, cron.UpdateCheckFile), ch); err != nil {
		t.Fatal(err)
	}
}

// writePendingFile writes a pending file with id to the update directory.
func writePendingFile(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, cron.UpdatePendingFile), []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validCommit is a syntactically valid 40-hex commit for use in tests.
const validCommit = "aabbccdd11223344556677889900aabbccdd1122"

// anotherCommit is a second valid commit, different from validCommit.
const anotherCommit = "1122334455667788990011223344556677889900"

// freshAgent returns an AgentFile that is fresh, has Inbox and Signing set.
func freshAgent() cron.AgentFile {
	return cron.AgentFile{
		Schema:        1,
		At:            time.Now(),
		ScriptVersion: "1.0",
		Channel:       "main",
		Signing:       true,
		DockerOK:      true,
		Inbox:         true,
	}
}

// freshCheck returns a CheckFile with Ahead>0, Signed=true, no Error, no ManualSteps.
func freshCheck() cron.CheckFile {
	return cron.CheckFile{
		Schema:  1,
		At:      time.Now(),
		Channel: "main",
		Current: cron.CheckRef{Commit: validCommit, Version: "v1.0.0"},
		Target:  cron.CheckRef{Commit: anotherCommit, Version: "v1.1.0"},
		Ahead:   3,
		Signed:  true,
	}
}

// --- auth tests ---

// TestUpdateRoutesRequire403ForNonAdmin verifies that non-admin sessions
// (including a regular user with all user sections) get 403 on all six update routes.
func TestUpdateRoutesRequire403ForNonAdmin(t *testing.T) {
	dir := t.TempDir()
	s, srv := newUpdateServer(t, dir, dir)

	// Create a regular user with all user-visible sections — the closest to admin
	// without actually being one.
	if _, err := s.createUser(userDraft{
		Login:    "user1",
		Password: "pw1234",
		Type:     security.UserRegular,
		Enabled:  true,
		Access:   security.Access{Sections: sectionStrings(userSections), Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	_, csrf := login(t, c, srv.URL, "user1", "pw1234")

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/version"},
		{http.MethodGet, "/api/update"},
		{http.MethodGet, "/api/update/log"},
		{http.MethodPost, "/api/update/apply"},
		{http.MethodPost, "/api/update/rollback"},
		{http.MethodPost, "/api/update/check"},
	}
	for _, r := range routes {
		code, _ := callJSON(t, c, r.method, srv.URL+r.path, csrf, "{}")
		if code != http.StatusForbidden {
			t.Errorf("%s %s: want 403, got %d", r.method, r.path, code)
		}
	}
}

// TestUpdateRoutesRequire403ForUserKey verifies that USER API keys
// get 403 on all six update routes.
func TestUpdateRoutesRequire403ForUserKey(t *testing.T) {
	dir := t.TempDir()
	s, srv := newUpdateServer(t, dir, dir)
	s.keyMu.Lock()
	rawKey, _, err := s.keys.GenerateWith("uk", []string{security.ScopeAdmin}, security.KeyAccess{
		Type: security.KeyTypeUser,
	})
	s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/version"},
		{http.MethodGet, "/api/update"},
		{http.MethodGet, "/api/update/log"},
		{http.MethodPost, "/api/update/apply"},
		{http.MethodPost, "/api/update/rollback"},
		{http.MethodPost, "/api/update/check"},
	}
	for _, r := range routes {
		req, _ := http.NewRequest(r.method, srv.URL+r.path, strings.NewReader("{}"))
		req.Header.Set("Api-Key", rawKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err2 := http.DefaultClient.Do(req)
		if err2 != nil {
			t.Fatal(err2)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s (user key): want 403, got %d", r.method, r.path, resp.StatusCode)
		}
	}
}

// TestUpdateApplyRollbackCheckRequire403ForAdminKey verifies that ADMIN API keys
// (not cookie sessions) get 403 on the three POST routes.
func TestUpdateApplyRollbackCheckRequire403ForAdminKey(t *testing.T) {
	dir := t.TempDir()
	s, srv := newUpdateServer(t, dir, dir)
	s.keyMu.Lock()
	rawKey, _, err := s.keys.Generate("ak", []string{security.ScopeAdmin})
	s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/update/apply", "/api/update/rollback", "/api/update/check"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader("{}"))
		req.Header.Set("Api-Key", rawKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err2 := http.DefaultClient.Do(req)
		if err2 != nil {
			t.Fatal(err2)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s (admin key): want 403, got %d", path, resp.StatusCode)
		}
	}
}

// --- notices tests ---

// TestUpdateNoticeNotForNonAdmin verifies that the update notice line does not
// appear in GET /api/notices for non-admin sessions.
func TestUpdateNoticeNotForNonAdmin(t *testing.T) {
	dir := t.TempDir()
	s, srv := newUpdateServer(t, dir, "")

	// Write a fresh check.json with an available update.
	writeCheckFile(t, dir, freshCheck())

	if _, err := s.createUser(userDraft{
		Login:    "user1",
		Password: "pw1234",
		Type:     security.UserRegular,
		Enabled:  true,
		Access:   security.Access{Sections: sectionStrings(userSections), Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	login(t, c, srv.URL, "user1", "pw1234")

	resp, err := c.Get(srv.URL + "/api/notices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var lines []string
	_ = json.NewDecoder(resp.Body).Decode(&lines)
	for _, l := range lines {
		if strings.Contains(l, "update available") {
			t.Error("update notice appeared for non-admin")
		}
	}
}

// TestUpdateNoticeForAdmin verifies that an update available notice appears
// for the admin when a fresh check.json has Ahead>0.
func TestUpdateNoticeForAdmin(t *testing.T) {
	dir := t.TempDir()
	_, srv := newUpdateServer(t, dir, "")
	writeCheckFile(t, dir, freshCheck())

	c, _ := loginAdmin(t, srv.URL)
	resp, err := c.Get(srv.URL + "/api/notices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var lines []string
	_ = json.NewDecoder(resp.Body).Decode(&lines)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "update available") {
			found = true
		}
	}
	if !found {
		t.Errorf("update notice missing for admin; lines = %v", lines)
	}
}

// --- keitaro gate tests ---

// TestUpdatePendingGateBlocksBeginImport verifies that begin* returns update_pending
// when the keitaro gate is set.
func TestUpdatePendingGateBlocksBeginImport(t *testing.T) {
	j := newKeitaroJob()
	j.setUpdating(true)

	if msg := j.beginImport(); !strings.Contains(msg, "update_pending") {
		t.Errorf("beginImport: want update_pending, got %q", msg)
	}
	if msg := j.beginPurge(); !strings.Contains(msg, "update_pending") {
		t.Errorf("beginPurge: want update_pending, got %q", msg)
	}
	if msg := j.beginSync(); !strings.Contains(msg, "update_pending") {
		t.Errorf("beginSync: want update_pending, got %q", msg)
	}
}

// TestUpdatePendingGateBlocksHoldWrite verifies that holdWrite returns errUpdatePending
// when the keitaro gate is set.
func TestUpdatePendingGateBlocksHoldWrite(t *testing.T) {
	j := newKeitaroJob()
	j.setUpdating(true)
	if err := j.holdWrite(); err != errUpdatePending {
		t.Errorf("holdWrite: want errUpdatePending, got %v", err)
	}
}

// TestUpdatePendingGateClearedAfterPendingRemoval verifies that the gate is released
// after the pending file is removed.
func TestUpdatePendingGateClearedAfterPendingRemoval(t *testing.T) {
	dir := t.TempDir()
	s, _ := newUpdateServer(t, dir, "")

	pendingID := "aabb1122ccdd3344"
	writePendingFile(t, dir, pendingID)
	s.checkPendingOnce()
	if !s.ktJob.updatingBool() {
		t.Fatal("gate not set after writing pending file")
	}

	if err := os.Remove(filepath.Join(dir, cron.UpdatePendingFile)); err != nil {
		t.Fatal(err)
	}
	s.checkPendingOnce()
	if s.ktJob.updatingBool() {
		t.Error("gate not cleared after removing pending file")
	}
}

// TestUpdatePendingGateSetOnServerStart verifies that WatchUpdatePending raises the
// gate immediately when a pending file already exists on startup.
func TestUpdatePendingGateSetOnServerStart(t *testing.T) {
	dir := t.TempDir()
	s, _ := newUpdateServer(t, dir, "")

	pendingID := "aabb1122ccdd3344"
	writePendingFile(t, dir, pendingID)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.WatchUpdatePending(ctx)
	}()

	// Wait for the first tick to run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.ktJob.updatingBool() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	if !s.ktJob.updatingBool() {
		t.Error("gate not raised on startup with existing pending file")
	}
}

// TestUpdatePendingStaleNotBlocking verifies that a pending file older than 1 hour
// does not block begin* operations.
func TestUpdatePendingStaleNotBlocking(t *testing.T) {
	dir := t.TempDir()
	s, _ := newUpdateServer(t, dir, "")

	pendingID := "aabb1122ccdd3344"
	pendingPath := filepath.Join(dir, cron.UpdatePendingFile)
	if err := os.WriteFile(pendingPath, []byte(pendingID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate the mtime by 2 hours.
	pastTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(pendingPath, pastTime, pastTime); err != nil {
		t.Fatal(err)
	}

	s.checkPendingOnce()
	if s.ktJob.updatingBool() {
		t.Error("stale pending file should not activate the gate")
	}
}

// TestUpdatePendingFinishedStateNotBlocking verifies that a pending with the same id
// as a completed state does not block.
func TestUpdatePendingFinishedStateNotBlocking(t *testing.T) {
	dir := t.TempDir()
	s, _ := newUpdateServer(t, dir, "")

	pendingID := "aabb1122ccdd3344"
	writePendingFile(t, dir, pendingID)
	now := time.Now()
	finished := cron.StateFile{
		Schema:     1,
		ID:         pendingID,
		Result:     cron.StateOK,
		FinishedAt: &now,
	}
	if err := cron.WriteUpdateJSON(filepath.Join(dir, cron.UpdateStateFile), finished); err != nil {
		t.Fatal(err)
	}

	s.checkPendingOnce()
	if s.ktJob.updatingBool() {
		t.Error("finished state with same id should not activate gate")
	}
}

// TestUpdateReadyNotWrittenWithWriters verifies that ready.json is NOT written
// when keitaro writers > 0.
func TestUpdateReadyNotWrittenWithWriters(t *testing.T) {
	dir := t.TempDir()
	s, _ := newUpdateServer(t, dir, "")

	pendingID := "aabb1122ccdd3344"
	writePendingFile(t, dir, pendingID)

	// Acquire a write hold.
	if err := s.ktJob.holdWrite(); err != nil {
		t.Fatal(err)
	}
	// Set updating manually to bypass the hold-check.
	s.ktJob.setUpdating(true)

	s.checkPendingOnce()

	readyPath := filepath.Join(dir, cron.UpdateReadyFile)
	if _, err := os.Stat(readyPath); err == nil {
		t.Error("ready.json was written while writers > 0")
	}

	s.ktJob.releaseWrite()
}

// TestUpdatePendingGateFailClosed verifies that if state.json exists but cannot be
// read (bad JSON), the gate remains raised (fail-closed).
func TestUpdatePendingGateFailClosed(t *testing.T) {
	dir := t.TempDir()
	s, _ := newUpdateServer(t, dir, "")

	pendingID := "aabb1122ccdd3344"
	writePendingFile(t, dir, pendingID)

	// Write a syntactically invalid state.json.
	if err := os.WriteFile(filepath.Join(dir, cron.UpdateStateFile), []byte("NOT JSON{{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.checkPendingOnce()
	if !s.ktJob.updatingBool() {
		t.Error("gate should be up when state.json is unreadable (fail-closed)")
	}
}

// TestUpdatePendingGateSymlinkState verifies that a symlinked state.json keeps
// the gate raised (ReadUpdateJSON rejects symlinks).
func TestUpdatePendingGateSymlinkState(t *testing.T) {
	dir := t.TempDir()
	s, _ := newUpdateServer(t, dir, "")

	pendingID := "aabb1122ccdd3344"
	writePendingFile(t, dir, pendingID)

	// Point state.json at a valid file via symlink — ReadUpdateJSON rejects symlinks.
	target := filepath.Join(t.TempDir(), "state.json")
	now := time.Now()
	finished := cron.StateFile{
		Schema:     1,
		ID:         pendingID,
		Result:     cron.StateOK,
		FinishedAt: &now,
	}
	if err := cron.WriteUpdateJSON(target, finished); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, cron.UpdateStateFile)); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	s.checkPendingOnce()
	if !s.ktJob.updatingBool() {
		t.Error("gate should be up when state.json is a symlink (fail-closed)")
	}
}

// TestUpdatePostRoutesRejectAdminAPIKey verifies that the three POST update routes
// return 403 for admin API keys (cookie-only, defense in depth).
func TestUpdatePostRoutesRejectAdminAPIKey(t *testing.T) {
	dir := t.TempDir()
	s, srv := newUpdateServer(t, dir, dir)

	s.keyMu.Lock()
	rawKey, _, err := s.keys.Generate("admin-key", []string{security.ScopeAdmin})
	s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/update/apply", "/api/update/rollback", "/api/update/check"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader("{}"))
		req.Header.Set("Api-Key", rawKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err2 := http.DefaultClient.Do(req)
		if err2 != nil {
			t.Fatal(err2)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s (admin API key): want 403, got %d", path, resp.StatusCode)
		}
	}
}

// --- apply precondition tests ---

// TestUpdateApply409Codes verifies every precondition code for POST /api/update/apply.
func TestUpdateApply409Codes(t *testing.T) {
	dir := t.TempDir()
	inboxDir := t.TempDir()
	_, srv := newUpdateServer(t, dir, inboxDir)
	c, csrf := loginAdmin(t, srv.URL)

	// inbox_absent: server with no inbox dir.
	_, srvNoInbox := newUpdateServer(t, dir, "")
	cNoInbox, csrfNoInbox := loginAdmin(t, srvNoInbox.URL)
	code, body := callJSON(t, cNoInbox, http.MethodPost, srvNoInbox.URL+"/api/update/apply", csrfNoInbox,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "inbox_absent") {
		t.Errorf("inbox_absent: %d %s", code, body)
	}

	// dev_build: config.Build() returns dev when commit is empty (default in tests).
	// This is the default state; inbox is configured.
	// Since we cannot easily override IsDev here without SetBuildForTest,
	// we set up a non-dev build to test the subsequent conditions,
	// and test dev_build by restoring.
	restore := config.SetBuildForTest(config.BuildInfo{
		Version: "dev", Commit: "", CommitDate: "", Channel: "dev",
	})
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	restore()
	if code != http.StatusConflict || !strings.Contains(string(body), "dev_build") {
		t.Errorf("dev_build: %d %s", code, body)
	}

	// Set a non-dev build for the remaining tests.
	restoreNonDev := config.SetBuildForTest(config.BuildInfo{
		Version: "v1.0.0", Commit: validCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
	})
	t.Cleanup(restoreNonDev)

	// updater_absent: no agent.json.
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "updater_absent") {
		t.Errorf("updater_absent: %d %s", code, body)
	}

	// unsigned (agent.Signing=false).
	writeAgentFile(t, dir, cron.AgentFile{Schema: 1, At: time.Now(), Inbox: true, Signing: false})
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "unsigned") {
		t.Errorf("unsigned (agent): %d %s", code, body)
	}

	// no_update: no check.json.
	writeAgentFile(t, dir, freshAgent())
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "no_update") {
		t.Errorf("no_update: %d %s", code, body)
	}

	// target_mismatch.
	writeCheckFile(t, dir, freshCheck())
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"0000000000000000000000000000000000000000","confirm":"0000000"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "target_mismatch") {
		t.Errorf("target_mismatch: %d %s", code, body)
	}

	// unsigned (check.Signed=false).
	ch := freshCheck()
	ch.Signed = false
	writeCheckFile(t, dir, ch)
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "unsigned") {
		t.Errorf("unsigned (check): %d %s", code, body)
	}
	writeCheckFile(t, dir, freshCheck())

	// manual_steps.
	ch2 := freshCheck()
	ch2.ManualSteps = []string{"migrations/clickhouse/001.sql"}
	writeCheckFile(t, dir, ch2)
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "manual_steps") {
		t.Errorf("manual_steps: %d %s", code, body)
	}
	writeCheckFile(t, dir, freshCheck())

	// confirm_mismatch.
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"WRONGXX"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "confirm_mismatch") {
		t.Errorf("confirm_mismatch: %d %s", code, body)
	}

	// running: state with running result.
	writeCheckFile(t, dir, freshCheck())
	var running cron.StateFile
	running.ID = "aaaa1111bbbb2222"
	running.Result = cron.StateRunning
	running.FinishedAt = nil
	if err := cron.WriteUpdateJSON(filepath.Join(dir, cron.UpdateStateFile), running); err != nil {
		t.Fatal(err)
	}
	code, body = callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "running") {
		t.Errorf("running: %d %s", code, body)
	}
	os.Remove(filepath.Join(dir, cron.UpdateStateFile))
}

// TestUpdateApplySuccess verifies that a successful apply writes a request file
// that ParseUpdateRequest can read and that the audit entry is created.
func TestUpdateApplySuccess(t *testing.T) {
	dir := t.TempDir()
	inboxDir := t.TempDir()
	_, srv := newUpdateServer(t, dir, inboxDir)
	c, csrf := loginAdmin(t, srv.URL)

	restore := config.SetBuildForTest(config.BuildInfo{
		Version: "v1.0.0", Commit: validCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
	})
	defer restore()

	writeAgentFile(t, dir, freshAgent())
	writeCheckFile(t, dir, freshCheck())

	code, body := callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", code, body)
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.ID == "" {
		t.Fatalf("response: %v %s", err, body)
	}
	if !cron.ValidUpdateID(resp.ID) {
		t.Errorf("invalid id %q", resp.ID)
	}

	// Verify request file.
	reqPath := filepath.Join(inboxDir, cron.UpdateRequestFile)
	data, err := os.ReadFile(reqPath)
	if err != nil {
		t.Fatalf("request file: %v", err)
	}
	req, err := cron.ParseUpdateRequest(data)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if req.Action != "apply" {
		t.Errorf("action = %q", req.Action)
	}
	if req.Target != anotherCommit {
		t.Errorf("target = %q", req.Target)
	}
	if req.ID != resp.ID {
		t.Errorf("id mismatch: request=%q resp=%q", req.ID, resp.ID)
	}
}

// TestUpdateApplySecondRequestPending verifies that a second apply returns request_pending.
func TestUpdateApplySecondRequestPending(t *testing.T) {
	dir := t.TempDir()
	inboxDir := t.TempDir()
	_, srv := newUpdateServer(t, dir, inboxDir)
	c, csrf := loginAdmin(t, srv.URL)

	restore := config.SetBuildForTest(config.BuildInfo{
		Version: "v1.0.0", Commit: validCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
	})
	defer restore()

	writeAgentFile(t, dir, freshAgent())
	writeCheckFile(t, dir, freshCheck())

	// First apply succeeds.
	code, _ := callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusAccepted {
		t.Fatal("first apply failed")
	}

	// Second apply should fail with request_pending.
	code, body := callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
		`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "request_pending") {
		t.Errorf("second apply: %d %s", code, body)
	}
}

// TestUpdateCheckRateLimited verifies that a second check within 5 minutes is rejected.
func TestUpdateCheckRateLimited(t *testing.T) {
	dir := t.TempDir()
	inboxDir := t.TempDir()
	s, srv := newUpdateServer(t, dir, inboxDir)
	c, csrf := loginAdmin(t, srv.URL)

	writeAgentFile(t, dir, cron.AgentFile{Schema: 1, At: time.Now(), Inbox: true})

	// Seed the in-memory rate limit to simulate a recent check.
	s.updateCheckMu.Lock()
	s.updateCheckAt = time.Now()
	s.updateCheckMu.Unlock()

	code, body := callJSON(t, c, http.MethodPost, srv.URL+"/api/update/check", csrf, "{}")
	if code != http.StatusTooManyRequests || !strings.Contains(string(body), "rate_limited") {
		t.Errorf("rate limited: %d %s", code, body)
	}
}

// TestUpdateVersionWithFakeEngine verifies GET /api/version with a fake engine
// and mismatch detection.
func TestUpdateVersionWithFakeEngine(t *testing.T) {
	adminCommit := validCommit
	engineCommit := anotherCommit // different from admin → mismatch

	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/version" {
			b := config.BuildInfo{
				Version: "v1.0.0", Commit: engineCommit,
				CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
			}
			json.NewEncoder(w).Encode(b) //nolint:errcheck
		}
	}))
	defer engine.Close()

	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	vDir := t.TempDir()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &fakeGroups{},
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
		EngineURL:    engine.URL,
		UsersFile:    filepath.Join(vDir, "users.json"),
		APIKeysFile:  filepath.Join(vDir, "api-keys.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	restore := config.SetBuildForTest(config.BuildInfo{
		Version: "v1.0.0", Commit: adminCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
	})
	defer restore()

	c, _ := loginAdmin(t, srv.URL)
	code, body := callJSON(t, c, http.MethodGet, srv.URL+"/api/version", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/version: %d %s", code, body)
	}

	var v versionView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v %s", err, body)
	}
	if v.Admin.Commit != adminCommit {
		t.Errorf("admin commit = %q", v.Admin.Commit)
	}
	if v.Engine == nil || v.Engine.Commit != engineCommit {
		t.Errorf("engine = %+v", v.Engine)
	}
	if !v.Mismatch {
		t.Error("mismatch should be true when admin and engine commits differ")
	}
}

// TestUpdateApplyParallelRequests verifies that N concurrent POST /api/update/apply
// calls produce exactly one 202 and all others return 409 request_pending.
// The request file must carry the id of the successful call.
func TestUpdateApplyParallelRequests(t *testing.T) {
	dir := t.TempDir()
	inboxDir := t.TempDir()
	_, srv := newUpdateServer(t, dir, inboxDir)

	restore := config.SetBuildForTest(config.BuildInfo{
		Version: "v1.0.0", Commit: validCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
	})
	defer restore()

	writeAgentFile(t, dir, freshAgent())
	writeCheckFile(t, dir, freshCheck())

	const N = 12
	type result struct {
		code int
		body []byte
	}
	results := make([]result, N)
	clients := make([]*http.Client, N)
	csrfs := make([]string, N)
	for i := range clients {
		clients[i], csrfs[i] = loginAdmin(t, srv.URL)
	}

	var wg sync.WaitGroup
	wg.Add(N)
	for i := range results {
		i := i
		go func() {
			defer wg.Done()
			results[i].code, results[i].body = callJSON(
				t, clients[i], http.MethodPost, srv.URL+"/api/update/apply", csrfs[i],
				`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`,
			)
		}()
	}
	wg.Wait()

	successCount := 0
	var successID string
	for _, r := range results {
		switch r.code {
		case http.StatusAccepted:
			successCount++
			var resp struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(r.body, &resp); err == nil {
				successID = resp.ID
			}
		case http.StatusConflict:
			if !strings.Contains(string(r.body), "request_pending") {
				t.Errorf("conflict without request_pending: %s", r.body)
			}
		default:
			t.Errorf("unexpected status %d: %s", r.code, r.body)
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d", successCount)
	}

	// The request file id must match the successful response.
	reqData, err := os.ReadFile(filepath.Join(inboxDir, cron.UpdateRequestFile))
	if err != nil {
		t.Fatalf("read request file: %v", err)
	}
	req, err := cron.ParseUpdateRequest(reqData)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if req.ID != successID {
		t.Errorf("request id = %q, want %q (from successful apply)", req.ID, successID)
	}
}

// TestUpdateEngineRedirectIsError verifies that a 3xx response from the engine is treated
// as an error: engine must be null and engine_error must be non-empty.
func TestUpdateEngineRedirectIsError(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/version" {
			http.Redirect(w, r, "http://127.0.0.1:0/nowhere", http.StatusFound)
		}
	}))
	defer engine.Close()

	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	vDir := t.TempDir()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &fakeGroups{},
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
		EngineURL:    engine.URL,
		UsersFile:    filepath.Join(vDir, "users.json"),
		APIKeysFile:  filepath.Join(vDir, "api-keys.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	c, _ := loginAdmin(t, srv.URL)
	code, body := callJSON(t, c, http.MethodGet, srv.URL+"/api/version", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/version: %d %s", code, body)
	}

	var v versionView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v %s", err, body)
	}
	if v.Engine != nil {
		t.Errorf("engine should be null when upstream redirects, got %+v", v.Engine)
	}
	if v.EngineError == "" {
		t.Error("engine_error should be non-empty when upstream redirects")
	}
}

// TestUpdateCLIUnsigned verifies that updateCLI appends --unsigned to the
// --channel and --to commands when unsigned is true, and omits it when false.
func TestUpdateCLIUnsigned(t *testing.T) {
	target := anotherCommit

	signed := updateCLI(target, false)
	for _, cmd := range signed {
		if strings.Contains(cmd, "--unsigned") {
			t.Errorf("signed CLI should not contain --unsigned: %q", cmd)
		}
	}
	if !strings.Contains(signed[2], "--to "+target) {
		t.Errorf("signed CLI[2] = %q; want --to %s", signed[2], target)
	}

	unsigned := updateCLI(target, true)
	if !strings.Contains(unsigned[1], "--channel --unsigned") {
		t.Errorf("unsigned CLI[1] = %q; want --channel --unsigned", unsigned[1])
	}
	if !strings.Contains(unsigned[2], "--to "+target+" --unsigned") {
		t.Errorf("unsigned CLI[2] = %q; want --to %s --unsigned", unsigned[2], target)
	}
	if strings.Contains(unsigned[0], "--unsigned") {
		t.Errorf("--check should not carry --unsigned: %q", unsigned[0])
	}
	if strings.Contains(unsigned[3], "--unsigned") {
		t.Errorf("--rollback should not carry --unsigned: %q", unsigned[3])
	}
}

// TestUpdateCheckSymlinkIsNull verifies that a symlinked check.json is treated as null.
func TestUpdateCheckSymlinkIsNull(t *testing.T) {
	dir := t.TempDir()
	_, srv := newUpdateServer(t, dir, "")
	c, _ := loginAdmin(t, srv.URL)

	restore := config.SetBuildForTest(config.BuildInfo{
		Version: "v1.0.0", Commit: validCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
	})
	defer restore()

	writeAgentFile(t, dir, freshAgent())

	// Create a real target file and a symlink pointing to it.
	target := filepath.Join(t.TempDir(), "target.json")
	ch := freshCheck()
	if err := cron.WriteUpdateJSON(target, ch); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, cron.UpdateCheckFile)
	if err := os.Symlink(target, symlink); err != nil {
		t.Skip("symlinks not supported:", err)
	}

	code, body := callJSON(t, c, http.MethodGet, srv.URL+"/api/update", "", "")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var v updateView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if v.Check != nil {
		t.Error("symlinked check.json should be treated as null")
	}
}
