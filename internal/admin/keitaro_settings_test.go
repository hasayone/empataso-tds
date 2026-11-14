package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/kuztds/bridge/ktimport"
)

// settingsEnv is a running admin server prepared for the tracker settings tests.
type settingsEnv struct {
	s    *Server
	srv  *httptest.Server
	dir  string
	file string
}

// newSettingsEnv starts an admin server with keitaro.json and API keys enabled.
// base and key are the start values from the environment. t is the test handle.
func newSettingsEnv(t *testing.T, base, key string) settingsEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "keitaro.json")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour,
		Networks:    NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		Manifest:    NewFileManifest(filepath.Join(dir, "keitaro-import.json")),
		KeitaroBase: base, KeitaroKey: key, KeitaroFile: file,
		APIKeysFile: filepath.Join(dir, "api-keys.json"),
		UsersFile:   filepath.Join(dir, "users.json"),
		Log:         noopLog{},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return settingsEnv{s: s, srv: srv, dir: dir, file: file}
}

// do sends one cookie-session request as the root administrator.
// It returns the status and the body.
func (e settingsEnv) do(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	c := newClient(t)
	code, csrf := login(t, c, e.srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	resp := csrfDo(t, c, method, e.srv.URL+path, csrf, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func decodeSettings(t *testing.T, raw string) keitaroSettingsView {
	t.Helper()
	var v keitaroSettingsView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("settings body %q: %v", raw, err)
	}
	return v
}

func TestKeitaroSettingsGetHidesKey(t *testing.T) {
	e := newSettingsEnv(t, "https://tracker.example/admin", "env-secret-key")
	code, raw := e.do(t, http.MethodGet, "/api/keitaro/settings", nil)
	if code != http.StatusOK {
		t.Fatalf("get %d %s", code, raw)
	}
	if strings.Contains(raw, "env-secret-key") || strings.Contains(raw, "api_key") {
		t.Fatalf("body leaks the key: %s", raw)
	}
	v := decodeSettings(t, raw)
	if v.BaseURL != "https://tracker.example/admin" || v.Host != "tracker.example" || !v.HasKey || v.Source != "env" {
		t.Fatalf("view %+v", v)
	}
	bare := newSettingsEnv(t, "", "")
	_, raw = bare.do(t, http.MethodGet, "/api/keitaro/settings", nil)
	if v := decodeSettings(t, raw); v.HasKey || v.Source != "none" || v.BaseURL != "" {
		t.Fatalf("empty view %+v", v)
	}
}

func TestKeitaroSettingsPutKeepsKeyOnMask(t *testing.T) {
	e := newSettingsEnv(t, "", "")
	code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "https://one.example/", "api_key": "first-key"})
	if code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	if strings.Contains(raw, "first-key") {
		t.Fatalf("put echoes the key: %s", raw)
	}
	if v := decodeSettings(t, raw); v.BaseURL != "https://one.example" || !v.HasKey || v.Source != "file" {
		t.Fatalf("view %+v", v)
	}
	for _, mask := range []string{"", "***", "••••••", "  "} {
		code, raw = e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "https://one.example", "api_key": mask})
		if code != http.StatusOK {
			t.Fatalf("mask %q: %d %s", mask, code, raw)
		}
		if got := e.s.keitaroCreds().Key; got != "first-key" {
			t.Fatalf("mask %q replaced the key: %q", mask, got)
		}
	}
	code, _ = e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "https://one.example", "api_key": "second-key"})
	if code != http.StatusOK || e.s.keitaroCreds().Key != "second-key" {
		t.Fatalf("new key not stored: %d", code)
	}
}

func TestKeitaroSettingsFileMode(t *testing.T) {
	e := newSettingsEnv(t, "", "")
	if code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://t.example", "api_key": "k1"}); code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	info, err := os.Stat(e.file)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	var body keitaroFileBody
	raw, _ := os.ReadFile(e.file)
	if err := json.Unmarshal(raw, &body); err != nil || body.BaseURL != "http://t.example" || body.APIKey != "k1" {
		t.Fatalf("file %s", raw)
	}
}

func TestKeitaroSettingsFileWinsOverEnv(t *testing.T) {
	e := newSettingsEnv(t, "http://env.example", "env-key")
	if code, _ := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://file.example", "api_key": "file-key"}); code != http.StatusOK {
		t.Fatal("put failed")
	}
	next, err := New(Config{
		AdminUser: "admin", PasswordHash: e.s.currentHash(), Sessions: security.NewMemoryStore(),
		Limiter: allowAll{}, KeitaroBase: "http://env.example", KeitaroKey: "env-key", KeitaroFile: e.file,
	})
	if err != nil {
		t.Fatal(err)
	}
	creds, source := next.keitaroState()
	if creds.Base != "http://file.example" || creds.Key != "file-key" || source != "file" {
		t.Fatalf("restart state %+v %s", creds, source)
	}
}

func TestKeitaroSettingsBrokenFileStopsStart(t *testing.T) {
	e := newSettingsEnv(t, "", "")
	if err := os.WriteFile(e.file, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{AdminUser: "admin", PasswordHash: e.s.currentHash(), Sessions: security.NewMemoryStore(), Limiter: allowAll{}, KeitaroFile: e.file})
	if err == nil {
		t.Fatal("a broken keitaro.json must not fall back to the environment")
	}
}

func TestKeitaroSettingsValidation(t *testing.T) {
	e := newSettingsEnv(t, "", "")
	bad := []string{
		"", "tracker.example", "ftp://tracker.example", "http://", "https://user:pw@tracker.example",
		"https://tracker.example/?a=1", "https://tracker.example/#frag", "https://tracker.example?", "javascript:alert(1)",
	}
	for _, base := range bad {
		code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": base, "api_key": "k"})
		if code != http.StatusBadRequest {
			t.Errorf("base %q: got %d %s", base, code, raw)
		}
	}
	if code, _ := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "https://ok.example"}); code != http.StatusBadRequest {
		t.Errorf("no key and no stored key: got %d", code)
	}
	if code, _ := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "https://ok.example", "api_key": "a\nb"}); code != http.StatusBadRequest {
		t.Errorf("newline in key: got %d", code)
	}
	if _, err := os.Stat(e.file); err == nil {
		t.Error("a refused PUT created the file")
	}
	if code, _ := e.do(t, http.MethodPut, "/api/keitaro/settings", "not an object"); code != http.StatusBadRequest {
		t.Errorf("bad json: got %d", code)
	}
}

func TestKeitaroSettingsPutUnavailableWithoutFile(t *testing.T) {
	_, srv := newKeitaroServers(t, "http://t.example", "k", nil, nil, nil)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/keitaro/settings", csrf, map[string]string{"base_url": "http://x.example", "api_key": "k"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestKeitaroSettingsBusyJob(t *testing.T) {
	e := newSettingsEnv(t, "http://t.example", "k")
	put := map[string]string{"base_url": "http://t.example", "api_key": "k2"}

	if msg := e.s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	if code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", put); code != http.StatusConflict {
		t.Fatalf("during import: %d %s", code, raw)
	}
	e.s.ktJob.finish("", nil, nil, "k")

	if msg := e.s.ktJob.beginPurge(); msg != "" {
		t.Fatal(msg)
	}
	if code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", put); code != http.StatusConflict {
		t.Fatalf("during purge: %d %s", code, raw)
	}
	e.s.ktJob.endPurge()

	if code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", put); code != http.StatusOK {
		t.Fatalf("idle: %d %s", code, raw)
	}
	if e.s.keitaroCreds().Key != "k2" {
		t.Fatal("key was not stored")
	}
}

func TestKeitaroSettingsManifestHost(t *testing.T) {
	e := newSettingsEnv(t, "http://old.example", "k")
	if err := e.s.cfg.Manifest.Save(ktimport.Manifest{Host: "old.example", Groups: []string{"g1"}}); err != nil {
		t.Fatal(err)
	}
	code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://new.example", "api_key": "k"})
	if code != http.StatusConflict || !strings.Contains(raw, "manifest_host") {
		t.Fatalf("host change with manifest: %d %s", code, raw)
	}
	if e.s.keitaroCreds().Base != "http://old.example" {
		t.Fatal("refused PUT changed the state")
	}
	if _, err := os.Stat(e.file); err == nil {
		t.Fatal("refused PUT wrote the file")
	}
	code, raw = e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://old.example", "api_key": "new-token"})
	if code != http.StatusOK {
		t.Fatalf("token change on the same host: %d %s", code, raw)
	}
	if err := e.s.cfg.Manifest.Save(ktimport.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if code, raw = e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://new.example", "api_key": "k"}); code != http.StatusOK {
		t.Fatalf("host change with an empty manifest: %d %s", code, raw)
	}
}

func TestKeitaroSettingsAPIKeyForbidden(t *testing.T) {
	e := newSettingsEnv(t, "http://t.example", "k")
	e.s.keyMu.Lock()
	raw, _, err := e.s.keys.Generate("t", []string{security.ScopeAdmin})
	e.s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req, _ := http.NewRequest(method, e.srv.URL+"/api/keitaro/settings", strings.NewReader(`{"base_url":"http://x.example","api_key":"k"}`))
		req.Header.Set("Api-Key", raw)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with Api-Key: got %d", method, resp.StatusCode)
		}
	}
	if e.s.keitaroCreds().Base != "http://t.example" {
		t.Fatal("API session changed the settings")
	}
}

func TestKeitaroSettingsNonAdminForbidden(t *testing.T) {
	e := newSettingsEnv(t, "http://t.example", "k")
	if _, err := e.s.createUser(userDraft{
		Login: "buyer", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: sectionStrings(userSections), Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}
	c := newClient(t)
	_, csrf := login(t, c, e.srv.URL, "buyer", "secret1")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		resp := csrfDo(t, c, method, e.srv.URL+"/api/keitaro/settings", csrf, map[string]string{"base_url": "http://x.example", "api_key": "k"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s as USER: got %d", method, resp.StatusCode)
		}
	}
}

func TestNormalizeKeitaroBase(t *testing.T) {
	good := map[string]string{
		" https://t.example/ ":       "https://t.example",
		"http://127.0.0.1:8080":      "http://127.0.0.1:8080",
		"https://t.example/panel///": "https://t.example/panel",
	}
	for in, want := range good {
		got, err := normalizeKeitaroBase(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
}

// TestKeitaroImportKeepsStartSnapshot changes the stored settings in the middle of a run.
// Every tracker request of that run must still carry the key taken at the start,
// and the manifest must name the host of the start.
func TestKeitaroImportKeepsStartSnapshot(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	var e settingsEnv
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Api-Key"))
		first := len(keys) == 1
		mu.Unlock()
		if first {
			e.s.setKeitaroState(ktCreds{Base: "http://other.example", Key: "new-key"}, keitaroSourceFile)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(fake.Close)
	e = newSettingsEnv(t, fake.URL, "old-key")

	if msg := e.s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	e.s.runKeitaroImport(context.Background(), keitaroImportReq{Networks: true}, "", e.s.keitaroCreds(), config.MainBoardID, e.s.cfg.Manifest)
	job := e.s.ktJob.snapshot()
	if job.Stage != "done" || job.Error != "" {
		t.Fatalf("job %+v", job)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) < 2 {
		t.Fatalf("requests %v", keys)
	}
	for _, k := range keys {
		if k != "old-key" {
			t.Fatalf("a request used %q instead of the key from the start: %v", k, keys)
		}
	}
	m, err := e.s.cfg.Manifest.Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := keitaroHost(fake.URL); m.Host != want {
		t.Fatalf("manifest host %q want %q", m.Host, want)
	}
}

// TestKeitaroStageMasksSnapshotKey checks that a stage masks the key of the run
// even after the stored key changed.
func TestKeitaroStageMasksSnapshotKey(t *testing.T) {
	s := &Server{ktJob: newKeitaroJob()}
	s.setKeitaroState(ktCreds{Base: "http://t.example", Key: "new-key"}, keitaroSourceFile)
	if msg := s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	st := &importState{counts: map[string]int{}, losses: []ktimport.Loss{}, creds: ktCreds{Base: "http://t.example", Key: "old-key"}}
	ok := s.runStage(st, "lists", "lists", func() (int, []ktimport.Loss, error) {
		return 1, []ktimport.Loss{{Kind: "skip", Ref: "r", Detail: "failed with old-key inside"}}, nil
	})
	if !ok {
		t.Fatal("stage failed")
	}
	job := s.ktJob.snapshot()
	if len(job.Losses) != 1 || strings.Contains(job.Losses[0].Detail, "old-key") || !strings.Contains(job.Losses[0].Detail, "***") {
		t.Fatalf("losses %+v", job.Losses)
	}
}

// TestKeitaroRedirectToOtherHostDropsKey checks the client never sends the key to another host.
func TestKeitaroRedirectToOtherHostDropsKey(t *testing.T) {
	var hits int
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		leaked = r.Header.Get("Api-Key")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(other.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	s := &Server{ktJob: newKeitaroJob()}
	_, err := s.keitaroClient(ktCreds{Base: origin.URL, Key: "secret-key"}).CountCampaigns(context.Background())
	if err == nil {
		t.Fatal("a redirect to another host must fail")
	}
	if hits != 0 || leaked != "" {
		t.Fatalf("other host got %d requests, key %q", hits, leaked)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error leaks the key: %v", err)
	}
}

// TestKeitaroRedirectSameHostFollowed checks a redirect inside the same host still works.
func TestKeitaroRedirectSameHostFollowed(t *testing.T) {
	var got []string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+"|"+r.Header.Get("Api-Key"))
		if r.URL.Path == "/admin_api/v1/campaigns" {
			http.Redirect(w, r, "/admin_api/v1/campaigns/", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte(`[{},{}]`))
	}))
	t.Cleanup(origin.Close)
	s := &Server{ktJob: newKeitaroJob()}
	body, err := s.keitaroClient(ktCreds{Base: origin.URL, Key: "k"}).Get(context.Background(), "/admin_api/v1/campaigns")
	if err != nil {
		t.Fatalf("same-host redirect: %v (%v)", err, got)
	}
	if string(body) != `[{},{}]` || len(got) != 2 || got[1] != "/admin_api/v1/campaigns/|k" {
		t.Fatalf("body %s requests %v", body, got)
	}
}

func TestKeitaroRedirectRule(t *testing.T) {
	mk := func(u string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, u, nil)
		return r
	}
	if err := keitaroRedirect(mk("https://a.example/x"), []*http.Request{mk("https://a.example/y")}); err != nil {
		t.Errorf("same host: %v", err)
	}
	if err := keitaroRedirect(mk("https://b.example/x"), []*http.Request{mk("https://a.example/y")}); err == nil {
		t.Error("other host followed")
	}
	if err := keitaroRedirect(mk("http://a.example/x"), []*http.Request{mk("https://a.example/y")}); err == nil {
		t.Error("https to http followed")
	}
	via := make([]*http.Request, keitaroRedirectLimit)
	for i := range via {
		via[i] = mk("https://a.example/y")
	}
	if err := keitaroRedirect(mk("https://a.example/x"), via); err == nil {
		t.Error("redirect limit ignored")
	}
}

// warnLog records the text of every warning so a test can look for leaked secrets.
type warnLog struct {
	mu   sync.Mutex
	text []string
}

func (w *warnLog) Warn(msg string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.text = append(w.text, fmt.Sprint(append([]any{msg}, args...)...))
}

func (w *warnLog) Info(string, ...any) {}

func (w *warnLog) joined() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.text, "\n")
}

// TestKeitaroSettingsHostChangeNeedsNewKey checks the stored key never moves to another host.
func TestKeitaroSettingsHostChangeNeedsNewKey(t *testing.T) {
	e := newSettingsEnv(t, "http://old.example", "old-key")
	for _, mask := range []string{"", "***", "••••"} {
		code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://new.example", "api_key": mask})
		if code != http.StatusBadRequest || !strings.Contains(raw, "api_key required for new host") {
			t.Fatalf("mask %q: %d %s", mask, code, raw)
		}
	}
	if c := e.s.keitaroCreds(); c.Base != "http://old.example" || c.Key != "old-key" {
		t.Fatalf("refused PUT changed the state: %+v", c)
	}
	if _, err := os.Stat(e.file); err == nil {
		t.Fatal("refused PUT wrote the file")
	}
	code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://OLD.example:8080", "api_key": ""})
	if code != http.StatusOK {
		t.Fatalf("same host, other port and case: %d %s", code, raw)
	}
	if c := e.s.keitaroCreds(); c.Key != "old-key" || c.Base != "http://old.example:8080" {
		t.Fatalf("state %+v", c)
	}
	code, raw = e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://new.example", "api_key": "new-key"})
	if code != http.StatusOK || e.s.keitaroCreds().Key != "new-key" {
		t.Fatalf("new host with a new key: %d %s", code, raw)
	}
}

// TestKeitaroSettingsEnvAddress checks the address from the environment goes through the same rules as a PUT.
func TestKeitaroSettingsEnvAddress(t *testing.T) {
	e := newSettingsEnv(t, "HTTPS://Tracker.Example/admin/", "env-key")
	_, raw := e.do(t, http.MethodGet, "/api/keitaro/settings", nil)
	if v := decodeSettings(t, raw); v.BaseURL != "https://tracker.example/admin" || v.Host != "tracker.example" || v.Source != "env" {
		t.Fatalf("view %+v", v)
	}

	for _, bad := range []string{"https://user:pw-secret@tracker.example", "https://tracker.example/?token=q-secret", "tracker.example", "ftp://tracker.example"} {
		log := &warnLog{}
		hash, err := security.HashPassword("p@ss")
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(Config{AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(), Limiter: allowAll{}, KeitaroBase: bad, KeitaroKey: "env-key", Log: log})
		if err != nil {
			t.Fatal(err)
		}
		c, source := s.keitaroState()
		if c.configured() || source != keitaroSourceNone || c.Base != "" {
			t.Errorf("%q: state %+v %s", bad, c, source)
		}
		view := s.keitaroSettingsSnapshot()
		if view.BaseURL != "" || view.Host != "" || view.HasKey || view.Source != keitaroSourceNone {
			t.Errorf("%q: view %+v", bad, view)
		}
		if text := log.joined(); text == "" || strings.Contains(text, "pw-secret") || strings.Contains(text, "q-secret") || strings.Contains(text, "tracker.example") || strings.Contains(text, "env-key") {
			t.Errorf("%q: log %q", bad, text)
		}
	}
}

// TestKeitaroSettingsSnapshotNeverShowsUserinfo checks GET drops userinfo and query even from a state set in memory.
func TestKeitaroSettingsSnapshotNeverShowsUserinfo(t *testing.T) {
	e := newSettingsEnv(t, "", "")
	e.s.setKeitaroState(ktCreds{Base: "https://user:pw-secret@tracker.example/?token=q-secret", Key: "k"}, keitaroSourceEnv)
	_, raw := e.do(t, http.MethodGet, "/api/keitaro/settings", nil)
	if strings.Contains(raw, "pw-secret") || strings.Contains(raw, "q-secret") || strings.Contains(raw, "user") {
		t.Fatalf("body leaks: %s", raw)
	}
}

// TestKeitaroSettingsManifestWithoutHost checks an import whose manifest has no host blocks a host change.
func TestKeitaroSettingsManifestWithoutHost(t *testing.T) {
	e := newSettingsEnv(t, "http://old.example", "k")
	if err := e.s.cfg.Manifest.Save(ktimport.Manifest{Groups: []string{"g1"}}); err != nil {
		t.Fatal(err)
	}
	code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://new.example", "api_key": "k2"})
	if code != http.StatusConflict || !strings.Contains(raw, "manifest_host") {
		t.Fatalf("got %d %s", code, raw)
	}
	if code, raw = e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://old.example", "api_key": ""}); code != http.StatusOK {
		t.Fatalf("same host: %d %s", code, raw)
	}
}

// TestKeitaroSettingsManifestHostCaseInsensitive checks an old manifest in another letter case still matches.
func TestKeitaroSettingsManifestHostCaseInsensitive(t *testing.T) {
	e := newSettingsEnv(t, "http://old.example", "k")
	if err := e.s.cfg.Manifest.Save(ktimport.Manifest{Host: "NEW.Example", Groups: []string{"g1"}}); err != nil {
		t.Fatal(err)
	}
	code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "http://new.example", "api_key": "k2"})
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
}

// TestKeitaroHostIsLowerCase checks the host written to the manifest and the warehouse is lower-case.
func TestKeitaroHostIsLowerCase(t *testing.T) {
	if got := keitaroHost("https://Tracker.EXAMPLE:8443/x"); got != "tracker.example" {
		t.Fatalf("host %q", got)
	}
	if got, err := normalizeKeitaroBase("HTTP://Tracker.Example:80/Panel/"); err != nil || got != "http://tracker.example:80/Panel" {
		t.Fatalf("base %q %v", got, err)
	}
}

// TestKeitaroSettingsPutKeepsConnectionList checks a list file keeps every connection and board_id.
func TestKeitaroSettingsPutKeepsConnectionList(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "keitaro.json")
	body := `[{"base_url":"https://a.example/admin","api_key":"k1","board_id":"seo"},{"base_url":"https://b.example/admin","api_key":"k2","board_id":"buy"}]`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour,
		Networks:    NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		Manifest:    NewFileManifest(filepath.Join(dir, "keitaro-import.json")),
		KeitaroFile: file,
		APIKeysFile: filepath.Join(dir, "api-keys.json"),
		UsersFile:   filepath.Join(dir, "users.json"),
		Log:         noopLog{},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	e := settingsEnv{s: s, srv: srv, dir: dir, file: file}
	code, raw := e.do(t, http.MethodPut, "/api/keitaro/settings", map[string]string{"base_url": "https://a.example/admin", "api_key": "k9"})
	if code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	saved, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var entries []keitaroFileEntry
	if err := json.Unmarshal(saved, &entries); err != nil {
		t.Fatalf("file %s: %v", saved, err)
	}
	if len(entries) != 2 || entries[0].BoardID != "seo" || entries[0].APIKey != "k9" || entries[0].BaseURL != "https://a.example/admin" {
		t.Fatalf("first %+v", entries)
	}
	if entries[1].BoardID != "buy" || entries[1].APIKey != "k2" || entries[1].BaseURL != "https://b.example/admin" {
		t.Fatalf("second %+v", entries)
	}
}

// TestPurgeUsesRecordedManifestHost checks purge removes the warehouse rows under the host exactly as the import recorded it.
func TestPurgeUsesRecordedManifestHost(t *testing.T) {
	sink := &fakeSink{}
	s, admin := newKeitaroServers(t, "http://other.example", "fixture-key", nil, sink, nil)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: "Old.Tracker.Example", Landings: []string{"701"}}); err != nil {
		t.Fatal(err)
	}
	raw, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge %d %s", code, raw)
	}
	if len(sink.purged) != 1 || sink.purged[0] != "Old.Tracker.Example" {
		t.Fatalf("purged %+v", sink.purged)
	}
}
