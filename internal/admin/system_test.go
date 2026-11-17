package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/security"
)

// fakePing is a Stats store that answers Ping with err after delay, or until ctx ends when block is set.
type fakePing struct {
	fakeStats
	err   error
	delay time.Duration
	block bool
}

func (f *fakePing) Ping(ctx context.Context) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	time.Sleep(f.delay)
	return f.err
}

// fakePingSessions is a session store that answers Ping with err.
type fakePingSessions struct {
	security.SessionStore
	err error
}

func (f *fakePingSessions) Ping(context.Context) error { return f.err }

// newSystemServer starts an admin with a cookie administrator, the update directory settings of
// newUpdateServer and mutate applied to the config. It returns the server, its HTTP test server,
// and a client signed in as admin.
func newSystemServer(t *testing.T, mutate func(*Config)) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		UsersFile:    filepath.Join(dir, "users.json"),
		APIKeysFile:  filepath.Join(dir, "api-keys.json"),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c, _ := loginAdmin(t, srv.URL)
	return s, srv, c
}

// getSystem calls GET /api/system and returns the body decoded as a generic object.
func getSystem(t *testing.T, c *http.Client, base string) map[string]any {
	t.Helper()
	code, body := callJSON(t, c, http.MethodGet, base+"/api/system", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/system: %d %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return out
}

// sub returns the object under key of m, failing the test when it is missing or not an object.
func sub(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%q is not an object in %v", key, m)
	}
	return v
}

// writeProc writes the files of a fake /proc tree; an empty text leaves the file out.
func writeProc(t *testing.T, root, loadavg, meminfo, uptime string) {
	t.Helper()
	for name, text := range map[string]string{"loadavg": loadavg, "meminfo": meminfo, "uptime": uptime} {
		if text == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSystemHostFromProc checks that the figures come from the fake proc tree and the disk
// function, with sizes in bytes, and that the disk of the state directory is asked for.
func TestSystemHostFromProc(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, "0.12 0.30 0.25 1/234 5678\n",
		"MemTotal:        8388608 kB\nMemFree:          100000 kB\nMemAvailable:    4194304 kB\n",
		"86400.55 172800.10\n")
	stateDir := t.TempDir()
	var asked string
	s, srv, c := newSystemServer(t, func(cfg *Config) { cfg.CronFile = filepath.Join(stateDir, "cron.json") })
	s.procRoot = root
	s.diskStat = func(path string) (uint64, uint64, error) {
		asked = path
		return 53687091200, 107374182400, nil
	}
	host := sub(t, getSystem(t, c, srv.URL), "host")
	want := map[string]any{
		"cpus": float64(runtime.NumCPU()), "load": []any{0.12, 0.3, 0.25},
		"mem_total": float64(8589934592), "mem_available": float64(4294967296),
		"uptime_s": float64(86400), "disk_total": float64(107374182400), "disk_free": float64(53687091200),
	}
	if !reflect.DeepEqual(host, want) {
		t.Fatalf("host = %v, want %v", host, want)
	}
	if asked != stateDir {
		t.Fatalf("disk asked for %q, want %q", asked, stateDir)
	}
}

// TestSystemOmitsFieldsWithoutData checks that a source that cannot be read leaves its fields out
// of the answer instead of sending zeros.
func TestSystemOmitsFieldsWithoutData(t *testing.T) {
	s, srv, c := newSystemServer(t, nil)
	s.procRoot = filepath.Join(t.TempDir(), "absent")
	s.diskStat = func(string) (uint64, uint64, error) { return 0, 0, errors.New("no disk") }
	out := getSystem(t, c, srv.URL)
	if host := sub(t, out, "host"); !reflect.DeepEqual(host, map[string]any{"cpus": float64(runtime.NumCPU())}) {
		t.Fatalf("host without /proc = %v", host)
	}
	if _, has := sub(t, out, "services")["cron"]; has {
		t.Fatal("cron is present without a status file")
	}

	root := t.TempDir()
	writeProc(t, root, "not numbers here\n", "MemTotal: 1024 kB\nMemAvailable: lots kB\n", "-3 0\n")
	s.procRoot = root
	s.diskStat = func(string) (uint64, uint64, error) { return 10, 0, nil }
	host := sub(t, getSystem(t, c, srv.URL), "host")
	if !reflect.DeepEqual(host, map[string]any{"cpus": float64(runtime.NumCPU()), "mem_total": float64(1024 * 1024)}) {
		t.Fatalf("host with damaged files = %v", host)
	}
}

// TestSystemStateDir checks the order of the paths the disk figure follows.
func TestSystemStateDir(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"default", Config{}, defaultStateDir},
		{"cron file", Config{CronFile: filepath.Join("var", "cron.json")}, "var"},
		{"password file first", Config{PasswordFile: filepath.Join("state", "admin.hash"), CronFile: filepath.Join("var", "cron.json")}, "state"},
	}
	for _, c := range cases {
		if got := (&Server{cfg: c.cfg}).stateDir(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// TestSystemServiceStates checks ok and down with their time, and that a store without a Ping,
// a missing store and a nil pointer inside the interface are all not_configured with no time.
// The session store cannot be nil: sign-in needs it to open the test session.
func TestSystemServiceStates(t *testing.T) {
	memory := func() security.SessionStore { return security.NewMemoryStore() }
	boom := errors.New("boom")
	cases := []struct {
		name      string
		stats     StatsProvider
		sessions  security.SessionStore
		wantCH    string
		wantRedis string
	}{
		{"both ok", &fakePing{}, &fakePingSessions{SessionStore: memory()}, "ok", "ok"},
		{"both down", &fakePing{err: boom}, &fakePingSessions{SessionStore: memory(), err: boom}, "down", "down"},
		{"no ping method", fakeStats{}, memory(), "not_configured", "not_configured"},
		{"typed nil", (*fakePing)(nil), memory(), "not_configured", "not_configured"},
		{"nil interface", nil, memory(), "not_configured", "not_configured"},
		{"mixed", &fakePing{}, memory(), "ok", "not_configured"},
	}
	for _, c := range cases {
		_, srv, cl := newSystemServer(t, func(cfg *Config) {
			cfg.Stats = c.stats
			cfg.Sessions = c.sessions
		})
		services := sub(t, getSystem(t, cl, srv.URL), "services")
		for name, want := range map[string]string{"clickhouse": c.wantCH, "redis": c.wantRedis} {
			svc := sub(t, services, name)
			if svc["state"] != want {
				t.Errorf("%s %s: state %v, want %s", c.name, name, svc["state"], want)
			}
			if _, hasMs := svc["ms"]; hasMs != (want != "not_configured") {
				t.Errorf("%s %s: ms present = %v for state %s", c.name, name, hasMs, want)
			}
		}
	}
}

// TestSystemPingsRunInParallelWithinTimeout checks that a service that never answers is down
// after the timeout and does not hold the other one back, and that both run at the same time.
func TestSystemPingsRunInParallelWithinTimeout(t *testing.T) {
	old := systemPingTimeout
	systemPingTimeout = 150 * time.Millisecond
	t.Cleanup(func() { systemPingTimeout = old })
	_, srv, c := newSystemServer(t, func(cfg *Config) {
		cfg.Stats = &fakePing{block: true}
		cfg.Sessions = &fakePingSessions{SessionStore: security.NewMemoryStore()}
	})
	start := time.Now()
	services := sub(t, getSystem(t, c, srv.URL), "services")
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("answer took %v", took)
	}
	ch, redis := sub(t, services, "clickhouse"), sub(t, services, "redis")
	if ch["state"] != "down" || redis["state"] != "ok" {
		t.Fatalf("clickhouse %v, redis %v", ch, redis)
	}
	if ms, _ := ch["ms"].(float64); ms < 100 {
		t.Fatalf("down after %v ms, want the timeout to pass", ms)
	}

	systemPingTimeout = 2 * time.Second
	_, srv, c = newSystemServer(t, func(cfg *Config) {
		cfg.Stats = &fakePing{delay: 300 * time.Millisecond}
		cfg.Sessions = &fakeSlowSessions{SessionStore: security.NewMemoryStore(), delay: 300 * time.Millisecond}
	})
	start = time.Now()
	getSystem(t, c, srv.URL)
	if took := time.Since(start); took >= 550*time.Millisecond {
		t.Fatalf("two 300 ms pings took %v: they ran one after the other", took)
	}
}

// fakeSlowSessions is a session store whose Ping answers after delay.
type fakeSlowSessions struct {
	security.SessionStore
	delay time.Duration
}

func (f *fakeSlowSessions) Ping(context.Context) error {
	time.Sleep(f.delay)
	return nil
}

// TestSystemCron checks the cron object: absent without a status file, with the heartbeat age
// and busy flag from it, and without the age while the heartbeat was never written.
func TestSystemCron(t *testing.T) {
	cronFile := filepath.Join(t.TempDir(), "cron.json")
	_, srv, c := newSystemServer(t, func(cfg *Config) { cfg.CronFile = cronFile })
	if _, has := sub(t, getSystem(t, c, srv.URL), "services")["cron"]; has {
		t.Fatal("cron is present without a status file")
	}

	write := func(st cron.Status) {
		t.Helper()
		b, _ := json.Marshal(st)
		if err := os.WriteFile(cron.StatusPath(cronFile), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(cron.Status{Heartbeat: time.Now().Add(-30 * time.Second),
		Jobs: map[string]cron.JobStatus{cron.JobGeoDB: {Running: true}}})
	got := sub(t, sub(t, getSystem(t, c, srv.URL), "services"), "cron")
	if age, _ := got["heartbeat_age_s"].(float64); age < 30 || age > 40 || got["busy"] != true {
		t.Fatalf("cron = %v", got)
	}

	write(cron.Status{Jobs: map[string]cron.JobStatus{}})
	got = sub(t, sub(t, getSystem(t, c, srv.URL), "services"), "cron")
	if _, has := got["heartbeat_age_s"]; has || got["busy"] != false {
		t.Fatalf("cron without a heartbeat = %v", got)
	}
}

// TestSystemCarriesNoVersions checks that the versions stay in GET /api/version.
func TestSystemCarriesNoVersions(t *testing.T) {
	_, srv, c := newSystemServer(t, nil)
	_, body := callJSON(t, c, http.MethodGet, srv.URL+"/api/system", "", "")
	for _, word := range []string{"version", "commit", "images", "mismatch"} {
		if strings.Contains(string(body), word) {
			t.Errorf("GET /api/system answers %q: %s", word, body)
		}
	}
}

// TestSystemAdminSessionOnly checks that an administrator API key, a user API key and a regular
// user's session all get 403, and that a request with no session is refused with 401.
func TestSystemAdminSessionOnly(t *testing.T) {
	s, srv, _ := newSystemServer(t, nil)
	s.keyMu.Lock()
	adminKey, _, err := s.keys.Generate("ak", []string{security.ScopeAdmin})
	userKey, _, err2 := s.keys.GenerateWith("uk", []string{security.ScopeAdmin}, security.KeyAccess{Type: security.KeyTypeUser})
	s.keyMu.Unlock()
	if err != nil || err2 != nil {
		t.Fatal(err, err2)
	}
	for name, key := range map[string]string{"admin key": adminKey, "user key": userKey} {
		if code, _ := doReq(t, nil, http.MethodGet, srv.URL+"/api/system", "", key, ""); code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", name, code)
		}
	}
	if _, err := s.createUser(userDraft{
		Login: "user1", Password: "pw1234", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: sectionStrings(userSections), Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}
	user := newClient(t)
	login(t, user, srv.URL, "user1", "pw1234")
	if code, _ := doReq(t, user, http.MethodGet, srv.URL+"/api/system", "", "", ""); code != http.StatusForbidden {
		t.Errorf("regular user: want 403, got %d", code)
	}
	if code, _ := doReq(t, nil, http.MethodGet, srv.URL+"/api/system", "", "", ""); code != http.StatusUnauthorized {
		t.Errorf("no session: want 401, got %d", code)
	}
}

// TestSystemInPublicDocs checks that the OpenAPI document, the Postman collection, the Markdown
// reference and /llms.txt all list GET /api/system, with the sample key and no private address.
func TestSystemInPublicDocs(t *testing.T) {
	srv, raw := startDocsApp(t)
	for path, want := range map[string]string{
		"/api/docs/openapi.json": `"/api/system"`,
		"/api/docs/postman.json": "api/system",
		"/api/docs.md":           "### `GET /api/system`",
		"/llms.txt":              "### `GET /api/system`",
	} {
		code, body := doReq(t, nil, http.MethodGet, srv.URL+path, "", "", "")
		if code != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d, missing %q", path, code, want)
		}
		if strings.Contains(string(body), raw) || privateAddr.Match(body) {
			t.Errorf("%s leaks a secret or a private address", path)
		}
	}
}
