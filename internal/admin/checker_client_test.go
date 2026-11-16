package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

const (
	// chkToken and chkPBToken are the only token values the checker tests use.
	chkToken   = "test-token"
	chkPBToken = "test-pb-token"
	chkTenant  = "tenant1"
	chkBase    = "https://acme.checkmy.store"
	chkAdmin   = "https://tds.example.com"
)

// testClock is the clock of the checker gate in tests. Sleep records the wait and moves the clock.
type testClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

// newTestClock returns a clock set to a fixed day.
func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
}

// Now returns the clock time.
func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock by d.
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Sleep records d, moves the clock by d and returns nil.
func (c *testClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

// Slept returns a copy of the recorded waits.
func (c *testClock) Slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// fcReq is one request the fake checker received.
type fcReq struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
}

// fcApp is an application the fake checker holds. Hidden hides it from the list but not from POST.
type fcApp struct {
	ID, Bundle, Status string
	Hidden             bool
}

// fakeChecker stands in for the checker API; the tests never reach a real host.
// script maps "METHOD /path" to the statuses of the next calls (zero means a normal answer).
// dropNext stores the next application and cuts the connection before the answer. conflict, when
// set, is the application every POST answers with as existing_app. onList runs at the start of
// every list call. htmlMe makes /api/auth/me answer text/toon. tenant is the X-Tenant-ID the
// fake accepts; empty means chkTenant.
type fakeChecker struct {
	t          *testing.T
	srv        *httptest.Server
	mu         sync.Mutex
	reqs       []fcReq
	apps       []fcApp
	script     map[string][]int
	retryAfter string
	dropNext   bool
	conflict   *fcApp
	onList     func()
	htmlMe     bool
	statusOf   string
	tenant     string
	inflight   int
	maxFlight  int
}

// newFakeChecker starts a fake checker that stops with the test.
func newFakeChecker(t *testing.T) *fakeChecker {
	t.Helper()
	f := &fakeChecker{t: t, script: map[string][]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// reply writes v as a JSON answer with the given status.
func (f *fakeChecker) reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// nextScript pops the scripted status of key, or returns zero.
func (f *fakeChecker) nextScript(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.script[key]
	if len(q) == 0 {
		return 0
	}
	f.script[key] = q[1:]
	return q[0]
}

// find returns the application with bundle, or nil.
func (f *fakeChecker) find(bundle string) *fcApp {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.apps {
		if f.apps[i].Bundle == bundle {
			a := f.apps[i]
			return &a
		}
	}
	return nil
}

// handle answers one request the way the checker does: the headers and format=json are checked,
// the list needs the two show flags, and a duplicate bundle is a 409 with existing_app.
func (f *fakeChecker) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.reqs = append(f.reqs, fcReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: string(raw)})
	f.inflight++
	f.maxFlight = max(f.maxFlight, f.inflight)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()
	q := r.URL.Query()
	tenant := f.tenant
	if tenant == "" {
		tenant = chkTenant
	}
	if r.Header.Get("Authorization") != "Bearer "+chkToken || r.Header.Get("X-Tenant-ID") != tenant ||
		r.Header.Get("Accept") != "application/json" || q.Get("format") != "json" {
		f.reply(w, http.StatusBadRequest, map[string]any{"message": "headers or format are wrong"})
		return
	}
	if code := f.nextScript(key); code != 0 {
		switch {
		case code >= 300 && code < 400:
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(code)
		case code == http.StatusUnprocessableEntity:
			f.reply(w, code, map[string]any{"message": "The given data was invalid.", "errors": map[string]any{"bundle_id": []string{"The bundle id is invalid."}}})
		default:
			if code == http.StatusTooManyRequests && f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			f.reply(w, code, map[string]any{"message": fmt.Sprintf("scripted failure %d", code)})
		}
		return
	}
	switch {
	case key == "GET /api/auth/me":
		if f.htmlMe {
			w.Header().Set("Content-Type", "text/toon")
			_, _ = w.Write([]byte("user: Tester"))
			return
		}
		f.reply(w, http.StatusOK, map[string]any{"data": map[string]any{"name": "Tester", "email": "tester@example.com", "api_token": "must-not-leak"}})
	case key == "GET /api/apps":
		if f.onList != nil {
			f.onList()
		}
		bundle := q.Get("bundle_id")
		if bundle == "" || q.Get("show_banned") != "true" || q.Get("show_archived") != "true" {
			f.reply(w, http.StatusBadRequest, map[string]any{"message": "show_banned and show_archived are required"})
			return
		}
		out := []map[string]any{}
		f.mu.Lock()
		for _, a := range f.apps {
			if a.Bundle == bundle && !a.Hidden {
				out = append(out, map[string]any{"id": a.ID, "bundle_id": a.Bundle, "status": a.Status})
			}
		}
		f.mu.Unlock()
		f.reply(w, http.StatusOK, map[string]any{"data": out})
	case key == "POST /api/apps":
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bundle, _ := body["bundle_id"].(string)
		existing := f.conflict
		if existing == nil {
			existing = f.find(bundle)
		}
		if existing != nil {
			f.reply(w, http.StatusConflict, map[string]any{"error": "existing_app", "message": "The application exists.",
				"existing_app": map[string]any{"id": existing.ID, "bundle_id": existing.Bundle, "status": existing.Status}})
			return
		}
		f.mu.Lock()
		app := fcApp{ID: fmt.Sprintf("app-%d", len(f.apps)+1), Bundle: bundle, Status: "store_watch"}
		f.apps = append(f.apps, app)
		drop := f.dropNext
		f.dropNext = false
		f.mu.Unlock()
		if drop {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		f.reply(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": app.ID, "bundle_id": app.Bundle, "status": app.Status}})
	case strings.HasPrefix(r.URL.Path, "/api/apps/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(r.URL.Path, "/api/apps/")
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, a := range f.apps {
			if a.ID == id {
				st := a.Status
				if f.statusOf != "" {
					st = f.statusOf
				}
				f.reply(w, http.StatusOK, map[string]any{"data": map[string]any{"id": a.ID, "bundle_id": a.Bundle, "status": st}})
				return
			}
		}
		f.reply(w, http.StatusNotFound, map[string]any{"message": "not found"})
	case key == "GET /api/consoles":
		if q.Get("per_page") != "200" {
			f.reply(w, http.StatusBadRequest, map[string]any{"message": "per_page must be 200"})
			return
		}
		f.reply(w, http.StatusOK, map[string]any{"data": []map[string]any{
			{"developer_id": "dev-1", "name": "Console One"},
			{"developer_id": nil, "name": "No id", "url": ""},
			{"developer_id": "", "name": "Link console", "url": "https://play.google.com/console/u/0/developers/777"},
		}})
	default:
		f.reply(w, http.StatusNotFound, map[string]any{"message": "unknown route"})
	}
}

// count returns how many requests with the method and path arrived.
func (f *fakeChecker) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// total returns how many requests arrived.
func (f *fakeChecker) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// requests returns a copy of the requests with the method and path.
func (f *fakeChecker) requests(method, path string) []fcReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fcReq
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// syncBuf is a log sink safe for concurrent writers.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends p.
func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns everything written.
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rigSpec shapes a checker test server. dir and fc and clock reuse the parts of an earlier rig
// (a restart); groups is the campaign list written first (nil keeps an existing file or the
// default list); files are written into dir before the server starts.
type rigSpec struct {
	dir    string
	fc     *fakeChecker
	clock  *testClock
	groups []config.Group
	files  map[string]string
}

// checkerRig is an admin server wired to a fake checker, with an administrator signed in.
type checkerRig struct {
	t            *testing.T
	s            *Server
	web          *httptest.Server
	c            *http.Client
	csrf         string
	fc           *fakeChecker
	clock        *testClock
	dir          string
	groups       *FileGroups
	gfile        string
	logs         *syncBuf
	groupsEdited bool
	gBytes       []byte
	gMod         time.Time
}

// defaultCheckerGroups returns the campaigns of a standard rig: two with a keyword and a bundle
// in the name, one without a keyword and one whose bundle holds an underscore.
func defaultCheckerGroups() []config.Group {
	return []config.Group{
		{ID: "promo", Num: 11381, Name: "❌ SpinDog - Play Cobra - com.playcobra.fasterthink"},
		{ID: "betclic", Num: 11382, Name: "❌ Betclic - Digit Twins (Digit Twins) - com.findgamestore.findpairapp"},
		{ID: "plain", Num: 11383, Name: "❄️ Win"},
		{ID: "under", Num: 11384, Name: "❌ Num Blast! (Num Blast!) - com.bapp.application2003_1 - #7"},
	}
}

// newCheckerRig starts the admin server of a checker test. The gate runs on a test clock, so no
// test waits for a real second. The std logger and the server logger write into rig.logs.
// At the end the test checks that groups.json was neither rewritten nor changed, unless it
// called setGroups.
func newCheckerRig(t *testing.T, sp rigSpec) *checkerRig {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	r := &checkerRig{t: t, fc: sp.fc, clock: sp.clock, dir: sp.dir, logs: &syncBuf{}}
	if r.dir == "" {
		r.dir = t.TempDir()
	}
	if r.fc == nil {
		r.fc = newFakeChecker(t)
	}
	if r.clock == nil {
		r.clock = newTestClock()
	}
	for name, body := range sp.files {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.gfile = filepath.Join(r.dir, "groups.json")
	r.groups = NewFileGroups(r.gfile)
	if _, err := os.Stat(r.gfile); err != nil || sp.groups != nil {
		list := sp.groups
		if list == nil {
			list = defaultCheckerGroups()
		}
		if err := r.groups.Save(context.Background(), list); err != nil {
			t.Fatal(err)
		}
	}
	r.snapshotGroups()
	log.SetOutput(r.logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: r.groups, S2S: NewJSONStore[config.OutboundPostback](filepath.Join(r.dir, "s2s.json")),
		Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		UsersFile:   filepath.Join(r.dir, "users.json"),
		CheckerFile: filepath.Join(r.dir, "checker.json"), CheckerAppsFile: filepath.Join(r.dir, "checker-apps.json"),
		CheckerURL: r.fc.srv.URL, Log: slog.New(slog.NewTextHandler(r.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.checker.gate.nowFn, s.checker.gate.sleepFn = r.clock.Now, r.clock.Sleep
	r.s = s
	r.web = httptest.NewServer(s.Handler())
	t.Cleanup(r.web.Close)
	r.c, r.csrf = boardLogin(t, r.web.URL, "admin", "p@ss")
	t.Cleanup(func() {
		if r.groupsEdited {
			return
		}
		b, err := os.ReadFile(r.gfile)
		st, serr := os.Stat(r.gfile)
		if err != nil || serr != nil || !bytes.Equal(b, r.gBytes) || !st.ModTime().Equal(r.gMod) {
			t.Error("groups.json was rewritten by the checker code")
		}
	})
	return r
}

// snapshotGroups remembers the content and the mtime of groups.json.
func (r *checkerRig) snapshotGroups() {
	b, err := os.ReadFile(r.gfile)
	if err != nil {
		r.t.Fatal(err)
	}
	st, err := os.Stat(r.gfile)
	if err != nil {
		r.t.Fatal(err)
	}
	r.gBytes, r.gMod = b, st.ModTime()
}

// setGroups replaces the campaign list; the test stops guarding groups.json.
func (r *checkerRig) setGroups(list []config.Group) {
	r.t.Helper()
	r.groupsEdited = true
	if err := r.groups.Save(context.Background(), list); err != nil {
		r.t.Fatal(err)
	}
}

// do sends a JSON request as the administrator and returns the status and the body.
func (r *checkerRig) do(method, path string, body any) (int, []byte) {
	r.t.Helper()
	return r.doWith(r.c, r.csrf, method, path, body)
}

// doWith sends a JSON request with the given session and returns the status and the body.
func (r *checkerRig) doWith(c *http.Client, csrf, method, path string, body any) (int, []byte) {
	r.t.Helper()
	resp := csrfDo(r.t, c, method, r.web.URL+path, csrf, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// settings saves a settings patch and fails the test unless the answer is 200.
func (r *checkerRig) settings(patch map[string]any) {
	r.t.Helper()
	if code, raw := r.do(http.MethodPut, "/api/checker/settings", patch); code != http.StatusOK {
		r.t.Fatalf("PUT settings %d %s", code, raw)
	}
}

// enable switches the integration and the creation on with the fake connection.
func (r *checkerRig) enable() {
	r.t.Helper()
	r.settings(map[string]any{
		"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": true,
		"create_apps": true, "admin_url": chkAdmin,
	})
}

// user creates a regular user with the given access and returns its session.
func (r *checkerRig) user(login string, access security.Access) (*http.Client, string) {
	r.t.Helper()
	if _, err := r.s.createUser(userDraft{Login: login, Password: "secret1", Type: security.UserRegular, Enabled: true, Access: access}); err != nil {
		r.t.Fatal(err)
	}
	return boardLogin(r.t, r.web.URL, login, "secret1")
}

// rec returns the stored record of a campaign.
func (r *checkerRig) rec(id string) (checkerApp, bool) {
	r.s.checker.mu.Lock()
	defer r.s.checker.mu.Unlock()
	a, ok := r.s.checker.apps[id]
	return a, ok
}

// run lets the queue work every due record once.
func (r *checkerRig) run() {
	r.s.checkerRun(context.Background())
}

// answer decodes the body of a campaign route.
func (r *checkerRig) answer(raw []byte) checkerAppAnswer {
	r.t.Helper()
	var a checkerAppAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		r.t.Fatalf("bad answer %s: %v", raw, err)
	}
	return a
}

// noSecrets fails when a token value shows in text.
func noSecrets(t *testing.T, what, text string) {
	t.Helper()
	for _, secret := range []string{chkToken, chkPBToken} {
		if strings.Contains(text, secret) {
			t.Errorf("%s holds a token", what)
		}
	}
}

// testClient builds a client of the fake with the given token.
func testClient(t *testing.T, r *checkerRig, token string) *checkerClient {
	t.Helper()
	return r.s.newCheckerClient(checkerSettings{BaseURL: chkBase, TenantID: chkTenant, Token: token})
}

func TestCheckerClientSendsHeadersAndFormat(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	u, err := testClient(t, r, chkToken).me(context.Background(), prioUser)
	if err != nil || u.Name != "Tester" || u.Email != "tester@example.com" {
		t.Fatalf("me = %+v, %v", u, err)
	}
	if reqs := r.fc.requests("GET", "/api/auth/me"); len(reqs) != 1 || reqs[0].Query.Get("format") != "json" {
		t.Fatalf("requests %+v", reqs)
	}
}

func TestCheckerClientRefusesNonJSONAndRedirects(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.fc.htmlMe = true
	_, err := testClient(t, r, chkToken).me(context.Background(), prioUser)
	ce, ok := err.(*checkerError)
	if !ok || !ce.NotJSON || ce.transient() {
		t.Fatalf("text/toon: %v", err)
	}
	r.fc.htmlMe = false
	r.fc.script["GET /api/auth/me"] = []int{http.StatusFound}
	_, err = testClient(t, r, chkToken).me(context.Background(), prioUser)
	if ce, ok = err.(*checkerError); !ok || !ce.NotJSON || !strings.Contains(err.Error(), "не JSON") {
		t.Fatalf("redirect: %v", err)
	}
	if n := r.fc.count("GET", "/elsewhere"); n != 0 {
		t.Fatalf("the redirect was followed %d times", n)
	}
}

func TestCheckerClientRetryAfterWaits(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.fc.script["GET /api/auth/me"] = []int{http.StatusTooManyRequests}
	r.fc.retryAfter = "2"
	cl := testClient(t, r, chkToken)
	_, err := cl.me(context.Background(), prioUser)
	ce, ok := err.(*checkerError)
	if !ok || ce.Status != http.StatusTooManyRequests || ce.RetryAfter != 2*time.Second || !ce.transient() {
		t.Fatalf("429: %v", err)
	}
	if _, err := cl.me(context.Background(), prioUser); err != nil {
		t.Fatal(err)
	}
	var longest time.Duration
	for _, d := range r.clock.Slept() {
		longest = max(longest, d)
	}
	if longest < 2*time.Second {
		t.Fatalf("the next request waited %v, want at least 2s", longest)
	}
}

func TestCheckerGateSerializesWithAPause(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	cl := testClient(t, r, chkToken)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cl.me(context.Background(), prioQueue); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if r.fc.maxFlight != 1 {
		t.Fatalf("%d requests ran at once", r.fc.maxFlight)
	}
	pauses := 0
	for _, d := range r.clock.Slept() {
		if d >= time.Second {
			pauses++
		}
	}
	if pauses < 5 {
		t.Fatalf("only %d pauses of a second for 6 requests: %v", pauses, r.clock.Slept())
	}
}

func TestCheckerGateServesUserBeforeQueue(t *testing.T) {
	var g checkerGate
	g.sleepFn = func(context.Context, time.Duration) error { return nil }
	if err := g.acquire(context.Background(), prioUser); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	start := func(name string, p checkerPrio) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.acquire(context.Background(), p); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			g.done(0, true)
		}()
	}
	start("queue", prioQueue)
	waitWaiters := func(n int) {
		for i := 0; i < 1000; i++ {
			g.mu.Lock()
			got := len(g.user) + len(g.bg)
			g.mu.Unlock()
			if got == n {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("waiters never reached %d", n)
	}
	waitWaiters(1)
	start("user", prioUser)
	waitWaiters(2)
	g.done(0, true)
	wg.Wait()
	if len(order) != 2 || order[0] != "user" || order[1] != "queue" {
		t.Fatalf("order %v", order)
	}
}

func TestCheckerClientErrorsCarryNoSecret(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	cl := testClient(t, r, chkToken)
	long := strings.Repeat("x", 500)
	if got := cl.scrub("bad " + chkToken + " and\n" + long); strings.Contains(got, chkToken) || strings.Contains(got, "\n") || len([]rune(got)) > checkerMsgMax {
		t.Fatalf("scrub = %q", got)
	}
	r.fc.script["GET /api/auth/me"] = []int{http.StatusUnprocessableEntity}
	_, err := cl.me(context.Background(), prioUser)
	if err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "The bundle id is invalid.") {
		t.Fatalf("422 error = %v", err)
	}
	noSecrets(t, "error", err.Error())
}

func TestCheckerClientBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("a", checkerMaxBody+10) + `"}`))
	}))
	defer srv.Close()
	r := newCheckerRig(t, rigSpec{})
	cl := r.s.newCheckerClient(checkerSettings{BaseURL: chkBase, TenantID: chkTenant, Token: chkToken})
	cl.base = srv.URL
	if _, err := cl.me(context.Background(), prioUser); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckerBaseProblem(t *testing.T) {
	good := map[string]string{
		"https://acme.checkmy.store":   "https://acme.checkmy.store",
		" https://ACME.checkmy.store/": "https://acme.checkmy.store",
	}
	for in, want := range good {
		if got, msg := checkerBaseProblem(in); got != want || msg != "" {
			t.Errorf("%q = %q, %q", in, got, msg)
		}
	}
	for _, in := range []string{
		"http://acme.checkmy.store", "https://acme.example.com", "https://checkmy.store", "https://evil.com/.checkmy.store",
		"https://acme.checkmy.store/api", "https://acme.checkmy.store?x=1", "https://acme.checkmy.store:8443",
		"https://user@acme.checkmy.store", "", "acme.checkmy.store",
	} {
		if got, msg := checkerBaseProblem(in); got != "" || msg == "" {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestCheckerFindByBundleIgnoresOtherBundles(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.fc.apps = []fcApp{{ID: "app-9", Bundle: "com.other.app", Status: "live"}}
	cl := testClient(t, r, chkToken)
	r.fc.onList = func() {}
	got, err := cl.findByBundle(context.Background(), prioQueue, "com.wanted.app")
	if err != nil || got != nil {
		t.Fatalf("found %+v, %v", got, err)
	}
	q := r.fc.requests("GET", "/api/apps")[0].Query
	if q.Get("show_banned") != "true" || q.Get("show_archived") != "true" || q.Get("bundle_id") != "com.wanted.app" {
		t.Fatalf("query %v", q)
	}
}

func TestCheckerConsolesMapIDs(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	list, err := testClient(t, r, chkToken).consoles(context.Background(), prioUser)
	if err != nil || len(list) != 2 {
		t.Fatalf("consoles = %+v, %v", list, err)
	}
	if list[0] != (checkerConsole{ID: "dev-1", Name: "Console One"}) ||
		list[1] != (checkerConsole{ID: "https://play.google.com/console/u/0/developers/777", Name: "Link console"}) {
		t.Fatalf("consoles = %+v", list)
	}
}
