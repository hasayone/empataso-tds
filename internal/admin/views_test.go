package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

func viewsServer(t *testing.T, viewsPath string) (*httptest.Server, string) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
		ViewsPath:    viewsPath,
		UsersFile:    filepath.Join(dir, "users.json"),
		APIKeysFile:  filepath.Join(dir, "api-keys.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, srv.URL
}

func viewsGet(t *testing.T, c *http.Client, base, csrf string) (int, map[string]json.RawMessage) {
	t.Helper()
	resp := csrfDo(t, c, http.MethodGet, base+"/api/me/views", csrf, nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var got map[string]json.RawMessage
	if resp.StatusCode == http.StatusOK && len(raw) > 0 {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode views: %v %s", err, raw)
		}
	}
	return resp.StatusCode, got
}

func withoutRev(got map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(got))
	for k, v := range got {
		if k == "rev" {
			continue
		}
		out[k] = v
	}
	return out
}

func viewsRevOf(t *testing.T, got map[string]json.RawMessage) int64 {
	t.Helper()
	var n int64
	if err := json.Unmarshal(got["rev"], &n); err != nil {
		t.Fatalf("rev: %v %s", err, got["rev"])
	}
	return n
}

func viewsPut(t *testing.T, c *http.Client, base, csrf string, body any) (int, map[string]json.RawMessage) {
	t.Helper()
	resp := csrfDo(t, c, http.MethodPut, base+"/api/me/views", csrf, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var got map[string]json.RawMessage
	if resp.StatusCode == http.StatusOK && len(raw) > 0 {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode put views: %v %s", err, raw)
		}
	}
	return resp.StatusCode, got
}

func TestViewsRoundTripEnvAdmin(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	c, csrf := authedClient(t, base)

	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"range": map[string]string{"id": "today"},
		"flows": map[string]any{"sort": map[string]any{"k": "name", "dir": 1}},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	_, hasRange := got["range"]
	_, hasFlows := got["flows"]
	if !hasRange || !hasFlows {
		t.Fatalf("saved: %+v", got)
	}

	code, got = viewsGet(t, c, base, csrf)
	if code != http.StatusOK {
		t.Fatalf("GET: %d", code)
	}
	if string(got["range"]) == "" {
		t.Fatalf("range missing: %+v", got)
	}

	disk, err := os.ReadFile(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disk), `"env:admin"`) {
		t.Fatalf("expected env:admin key: %s", disk)
	}
}

func TestViewsTwoUsersIsolated(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	adminC, adminCSRF := authedClient(t, base)

	code, _ := viewsPut(t, adminC, base, adminCSRF, map[string]any{
		"flows": map[string]string{"q": "admin-only"},
	})
	if code != http.StatusOK {
		t.Fatalf("admin PUT: %d", code)
	}

	code, _ = userJSON(t, adminC, http.MethodPost, base+"/api/users", adminCSRF,
		`{"login":"bob","password":"secret1","type":"USER","enabled":true,"access":{"sections":["dashboard"],"grants":{}}}`)
	if code != http.StatusCreated {
		t.Fatalf("create bob: %d", code)
	}
	jar, _ := cookiejar.New(nil)
	bobC := &http.Client{Jar: jar}
	_, bobCSRF := login(t, bobC, base, "bob", "secret1")

	code, got := viewsGet(t, bobC, base, bobCSRF)
	if code != http.StatusOK || len(withoutRev(got)) != 0 {
		t.Fatalf("bob must not see admin views: %d %+v", code, got)
	}

	code, _ = viewsPut(t, bobC, base, bobCSRF, map[string]any{
		"dashboard": map[string]string{"sort": "name"},
	})
	if code != http.StatusOK {
		t.Fatalf("bob PUT: %d", code)
	}

	code, got = viewsGet(t, adminC, base, adminCSRF)
	if code != http.StatusOK || string(got["flows"]) == "" {
		t.Fatalf("admin flows lost: %+v", got)
	}
	code, got = viewsGet(t, bobC, base, bobCSRF)
	if code != http.StatusOK || string(got["dashboard"]) == "" {
		t.Fatalf("bob dashboard: %+v", got)
	}
}

func TestViewsAutomationPageAccepted(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"automation": map[string]any{"order": []string{"telegram", "geo_db"}},
		"secret":     map[string]string{"x": "y"},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	if _, ok := got["secret"]; ok {
		t.Fatalf("unknown page key must be dropped: %+v", got)
	}
	if len(got["automation"]) == 0 {
		t.Fatalf("automation page must be stored: %+v", got)
	}
	if !strings.Contains(string(got["automation"]), `"telegram"`) {
		t.Fatalf("automation order lost: %s", got["automation"])
	}
}

// TestViewsIntegrationsPageAccepted checks that the integrations the user added to the side list are kept on the account.
// t is the test handle. It returns nothing and fails when the integrations page key is dropped.
func TestViewsIntegrationsPageAccepted(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	code, _ := viewsPut(t, c, base, csrf, map[string]any{
		"integrations": map[string]any{"added": []string{"namecheap"}},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	code, got := viewsGet(t, c, base, csrf)
	if code != http.StatusOK || !strings.Contains(string(got["integrations"]), `"namecheap"`) {
		t.Fatalf("integrations page must be stored: %d %+v", code, got)
	}
}

// TestViewsRatingAccepted checks that the rating window state is kept on the account,
// so the active time, the closed flag and the score survive a new browser.
// t is the test handle. It returns nothing and fails when the rating key is dropped.
func TestViewsRatingAccepted(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	code, _ := viewsPut(t, c, base, csrf, map[string]any{
		"rating": map[string]any{"active": 3600000, "closed": true, "score": 5, "at": "2026-11-16T12:00:00Z"},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	code, got := viewsGet(t, c, base, csrf)
	raw := string(got["rating"])
	if code != http.StatusOK || !strings.Contains(raw, `"closed":true`) || !strings.Contains(raw, `"score":5`) || !strings.Contains(raw, `3600000`) {
		t.Fatalf("rating must be stored: %d %+v", code, got)
	}
}

// TestViewsKeywordStatsPageAccepted checks the keyword statistics view is stored on the account
// next to the keywords key, which keeps the API-key table sort.
func TestViewsKeywordStatsPageAccepted(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	code, _ := viewsPut(t, c, base, csrf, map[string]any{
		"keyword_stats": map[string]any{"sort": map[string]any{"k": "profit", "dir": -1}, "campaign": "c1"},
		"keywords":      map[string]any{"sort": map[string]any{"k": "key", "dir": 1}},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	code, got := viewsGet(t, c, base, csrf)
	if code != http.StatusOK || !strings.Contains(string(got["keyword_stats"]), `"profit"`) || !strings.Contains(string(got["keyword_stats"]), `"c1"`) {
		t.Fatalf("keyword_stats page must be stored: %d %+v", code, got)
	}
	if !strings.Contains(string(got["keywords"]), `"key"`) {
		t.Fatalf("keywords page lost: %+v", got)
	}
}

func TestViewsUnknownPageDropped(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"flows":  map[string]string{"q": "x"},
		"secret": map[string]string{"x": "y"},
		"range":  "not-an-object",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	if _, ok := got["secret"]; ok || string(got["range"]) != "" || string(got["flows"]) == "" {
		t.Fatalf("expected only flows: %+v", got)
	}
}

// TestViewsAccountTablesStored checks that the users, roles and keywords sorts and the column sets
// inside the page objects are kept as sent, that an unknown page key is dropped, and that a second
// account with its own sort and columns neither sees nor overwrites the first one.
func TestViewsAccountTablesStored(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	adminC, adminCSRF := authedClient(t, base)

	body := map[string]any{
		"flows": map[string]any{
			"sort": map[string]any{"k": "name", "dir": 1},
			"cols": []map[string]any{{"id": "name", "on": true}, {"id": "hits", "on": false}},
			"view": "grid", "compact": true, "page": 250,
		},
		"logs":     map[string]any{"cols": []map[string]any{{"id": "ts", "on": true}}, "page": 500, "pblogSort": map[string]any{"k": "ts", "dir": -1}},
		"users":    map[string]any{"sort": map[string]any{"k": "login", "dir": 1}},
		"roles":    map[string]any{"sort": map[string]any{"k": "name", "dir": -1}},
		"keywords": map[string]any{"sort": map[string]any{"k": "key", "dir": 1}},
		"tokens":   map[string]any{"sort": map[string]any{"k": "x", "dir": 1}},
	}
	code, got := viewsPut(t, adminC, base, adminCSRF, body)
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	if _, ok := got["tokens"]; ok {
		t.Fatalf("unknown page key must be dropped: %+v", got)
	}
	for _, key := range []string{"flows", "logs", "users", "roles", "keywords"} {
		if len(got[key]) == 0 {
			t.Fatalf("page %q not stored: %+v", key, got)
		}
	}

	code, got = viewsGet(t, adminC, base, adminCSRF)
	if code != http.StatusOK {
		t.Fatalf("GET: %d", code)
	}
	var flows struct {
		Sort    map[string]any   `json:"sort"`
		Cols    []map[string]any `json:"cols"`
		View    string           `json:"view"`
		Compact bool             `json:"compact"`
		Page    int              `json:"page"`
	}
	if err := json.Unmarshal(got["flows"], &flows); err != nil {
		t.Fatalf("decode flows: %v", err)
	}
	if flows.Sort["k"] != "name" || len(flows.Cols) != 2 || flows.View != "grid" || !flows.Compact || flows.Page != 250 {
		t.Fatalf("flows view changed on the way: %s", got["flows"])
	}
	if !strings.Contains(string(got["users"]), `"login"`) || !strings.Contains(string(got["keywords"]), `"key"`) {
		t.Fatalf("users or keywords sort lost: %+v", got)
	}

	code, _ = userJSON(t, adminC, http.MethodPost, base+"/api/users", adminCSRF,
		`{"login":"carol","password":"secret1","type":"USER","enabled":true,"access":{"sections":["dashboard"],"grants":{}}}`)
	if code != http.StatusCreated {
		t.Fatalf("create carol: %d", code)
	}
	jar, _ := cookiejar.New(nil)
	carolC := &http.Client{Jar: jar}
	_, carolCSRF := login(t, carolC, base, "carol", "secret1")

	code, got = viewsGet(t, carolC, base, carolCSRF)
	if code != http.StatusOK || len(withoutRev(got)) != 0 {
		t.Fatalf("carol must start empty: %d %+v", code, got)
	}
	code, _ = viewsPut(t, carolC, base, carolCSRF, map[string]any{
		"flows": map[string]any{"sort": map[string]any{"k": "hits", "dir": -1}, "cols": []map[string]any{{"id": "num", "on": true}}},
		"users": map[string]any{"sort": map[string]any{"k": "role", "dir": -1}},
	})
	if code != http.StatusOK {
		t.Fatalf("carol PUT: %d", code)
	}

	_, got = viewsGet(t, adminC, base, adminCSRF)
	if err := json.Unmarshal(got["flows"], &flows); err != nil {
		t.Fatalf("decode admin flows: %v", err)
	}
	if flows.Sort["k"] != "name" || len(flows.Cols) != 2 || !strings.Contains(string(got["users"]), `"login"`) {
		t.Fatalf("admin views overwritten by carol: %+v", got)
	}
	_, got = viewsGet(t, carolC, base, carolCSRF)
	if !strings.Contains(string(got["flows"]), `"hits"`) || strings.Contains(string(got["flows"]), `"name"`) || len(got["roles"]) != 0 {
		t.Fatalf("carol views mixed with admin: %+v", got)
	}
}

func TestViewsBodyTooLarge(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	padding := strings.Repeat("x", viewsMaxBody)
	body := map[string]any{"flows": map[string]string{"q": padding}}
	resp := csrfDo(t, c, http.MethodPut, base+"/api/me/views", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func TestViewsCorruptFileNotOverwritten(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	if err := os.WriteFile(viewsPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	_, base := viewsServer(t, viewsPath)
	c, csrf := authedClient(t, base)

	code, _ := viewsPut(t, c, base, csrf, map[string]any{"flows": map[string]string{"q": "x"}})
	if code != http.StatusInternalServerError {
		t.Fatalf("PUT corrupt file: want 500, got %d", code)
	}
	after, err := os.ReadFile(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("corrupt views file was modified")
	}

	code, _ = viewsGet(t, c, base, csrf)
	if code != http.StatusInternalServerError {
		t.Fatalf("GET corrupt file: want 500, got %d", code)
	}
}

func TestViewsPartialPutKeepsOtherPages(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"rev":   0,
		"range": map[string]string{"id": "today"},
		"flows": map[string]string{"q": "keep"},
	})
	if code != http.StatusOK {
		t.Fatalf("initial PUT: %d", code)
	}
	rev := viewsRevOf(t, got)
	if rev != 1 || string(got["range"]) == "" {
		t.Fatalf("first save: %+v", got)
	}

	code, got = viewsPut(t, c, base, csrf, map[string]any{
		"rev":   rev,
		"flows": map[string]string{"q": "next"},
	})
	if code != http.StatusOK {
		t.Fatalf("partial PUT: %d", code)
	}
	if !strings.Contains(string(got["range"]), "today") {
		t.Fatalf("range must stay: %+v", got)
	}
	if !strings.Contains(string(got["flows"]), "next") {
		t.Fatalf("flows must be replaced: %+v", got)
	}
	if viewsRevOf(t, got) != 2 {
		t.Fatalf("rev: %+v", got["rev"])
	}
}

func TestViewsEmptyPutKeepsUser(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	c, csrf := authedClient(t, base)

	code, got := viewsPut(t, c, base, csrf, map[string]any{"flows": map[string]string{"q": "x"}})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	rev := viewsRevOf(t, got)
	code, got = viewsPut(t, c, base, csrf, map[string]any{"rev": rev})
	if code != http.StatusOK || string(got["flows"]) == "" {
		t.Fatalf("empty PUT must keep flows: %d %+v", code, got)
	}
	if viewsRevOf(t, got) != rev {
		t.Fatalf("empty PUT must not bump rev: %s", got["rev"])
	}
	disk, err := os.ReadFile(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disk), "env:admin") || !strings.Contains(string(disk), `"q"`) {
		t.Fatalf("user entry must stay: %s", disk)
	}
}

func TestViewsRequiresAuth(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c := newClient(t)

	resp := csrfDo(t, c, http.MethodGet, base+"/api/me/views", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET unauth: %d", resp.StatusCode)
	}
	resp = csrfDo(t, c, http.MethodPut, base+"/api/me/views", "", map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PUT unauth: %d", resp.StatusCode)
	}
}

func TestViewsEmptyPath(t *testing.T) {
	_, base := viewsServer(t, "")
	c, csrf := authedClient(t, base)

	code, got := viewsGet(t, c, base, csrf)
	if code != http.StatusOK || len(got) != 0 {
		t.Fatalf("GET empty path: %d %+v", code, got)
	}
	resp := csrfDo(t, c, http.MethodPut, base+"/api/me/views", csrf, map[string]any{"flows": map[string]string{}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("PUT empty path: %d", resp.StatusCode)
	}
}

func TestViewsBadJSONBody(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)
	code, _ := userJSON(t, c, http.MethodPut, base+"/api/me/views", csrf, `"array"`)
	if code != http.StatusBadRequest {
		t.Fatalf("non-object body: want 400, got %d", code)
	}
}

func TestViewsFileMode(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	c, csrf := authedClient(t, base)
	code, _ := viewsPut(t, c, base, csrf, map[string]any{"flows": map[string]string{"q": "x"}})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	info, err := os.Stat(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("views mode %o, want 0600", info.Mode().Perm())
	}
}

func TestViewsStaleRevConflict(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	c, csrf := authedClient(t, base)
	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"rev":   0,
		"flows": map[string]string{"q": "first"},
	})
	if code != http.StatusOK || viewsRevOf(t, got) != 1 {
		t.Fatalf("first PUT: %d %+v", code, got)
	}
	before, err := os.ReadFile(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	code, _ = viewsPut(t, c, base, csrf, map[string]any{
		"rev":   0,
		"flows": map[string]string{"q": "stale"},
	})
	if code != http.StatusConflict {
		t.Fatalf("stale rev: want 409, got %d", code)
	}
	after, err := os.ReadFile(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("stale put changed the file")
	}
	code, got = viewsGet(t, c, base, csrf)
	if code != http.StatusOK || !strings.Contains(string(got["flows"]), "first") || viewsRevOf(t, got) != 1 {
		t.Fatalf("stored views: %d %+v", code, got)
	}
}

func TestViewsOwnerMustBeSession(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	c, csrf := authedClient(t, base)
	code, _ := viewsPut(t, c, base, csrf, map[string]any{
		"rev":   0,
		"owner": "someone-else",
		"flows": map[string]string{"q": "nope"},
	})
	if code != http.StatusForbidden {
		t.Fatalf("foreign owner: want 403, got %d", code)
	}
	if _, err := os.Stat(viewsPath); !os.IsNotExist(err) {
		t.Fatalf("foreign owner must not create the file: %v", err)
	}
	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"rev":   0,
		"owner": "env:admin",
		"flows": map[string]string{"q": "mine"},
	})
	if code != http.StatusOK || !strings.Contains(string(got["flows"]), "mine") {
		t.Fatalf("own key: %d %+v", code, got)
	}
}

func TestViewsUIPageKeepsAccent(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)
	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"rev":   0,
		"flows": map[string]string{"q": "stay"},
	})
	if code != http.StatusOK {
		t.Fatalf("flows PUT: %d", code)
	}
	code, got = viewsPut(t, c, base, csrf, map[string]any{
		"rev": viewsRevOf(t, got),
		"ui": map[string]any{
			"theme": "dark", "accent": "#112233", "layout": "wide",
			"menu":      map[string]any{"order": []string{"flows"}, "hidden": map[string]bool{"logs": true}},
			"cols":      map[string]any{"flows\nname": map[string]int{"name": 180}},
			"dock":      map[string]int{"x": 12, "y": 40},
			"assistant": "docs",
			"token":     "sekret", "password": "sekret-pw", "lang": "ru", "tz": "UTC", "board": "main",
		},
	})
	if code != http.StatusOK {
		t.Fatalf("ui PUT: %d", code)
	}
	raw := string(got["ui"])
	for _, want := range []string{`"theme":"dark"`, `"accent":"#112233"`, `"layout":"wide"`, `"assistant":"docs"`, `"x":12`, "flows"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("ui missing %s: %s", want, raw)
		}
	}
	for _, banned := range []string{"sekret", `"lang"`, `"tz"`, `"board"`, "token", "password"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("ui kept %s: %s", banned, raw)
		}
	}
	if !strings.Contains(string(got["flows"]), "stay") {
		t.Fatalf("flows page lost: %+v", got)
	}
	code, got = viewsGet(t, c, base, csrf)
	if code != http.StatusOK || !strings.Contains(string(got["ui"]), `"#112233"`) || !strings.Contains(string(got["flows"]), "stay") {
		t.Fatalf("GET ui: %d %+v", code, got)
	}
}

func TestViewsDeleteUserDropsKey(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	adminC, adminCSRF := authedClient(t, base)
	code, raw := userJSON(t, adminC, http.MethodPost, base+"/api/users", adminCSRF,
		`{"login":"dave","password":"secret1","type":"USER","enabled":true,"access":{"sections":["dashboard"],"grants":{}}}`)
	if code != http.StatusCreated {
		t.Fatalf("create dave: %d %s", code, raw)
	}
	var dave security.User
	if err := json.Unmarshal(raw, &dave); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	daveC := &http.Client{Jar: jar}
	_, daveCSRF := login(t, daveC, base, "dave", "secret1")
	code, _ = viewsPut(t, daveC, base, daveCSRF, map[string]any{
		"owner": dave.ID,
		"flows": map[string]string{"q": "dave-only"},
	})
	if code != http.StatusOK {
		t.Fatalf("dave PUT: %d", code)
	}
	code, got := viewsPut(t, adminC, base, adminCSRF, map[string]any{"flows": map[string]string{"q": "admin"}})
	if code != http.StatusOK {
		t.Fatalf("admin PUT: %d", code)
	}
	code, _ = userJSON(t, adminC, http.MethodDelete, base+"/api/users/"+dave.ID, adminCSRF, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete dave: %d", code)
	}
	disk, err := os.ReadFile(viewsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), dave.ID) || strings.Contains(string(disk), "dave-only") {
		t.Fatalf("deleted user still in views: %s", disk)
	}
	if !strings.Contains(string(disk), "env:admin") || !strings.Contains(string(disk), "admin") {
		t.Fatalf("admin views lost: %s", disk)
	}
	code, got = viewsGet(t, adminC, base, adminCSRF)
	if code != http.StatusOK || !strings.Contains(string(got["flows"]), "admin") {
		t.Fatalf("admin GET: %d %+v", code, got)
	}
}
