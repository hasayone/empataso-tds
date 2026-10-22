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

	"github.com/egerkuzma/kuztds/internal/security"
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
	if code != http.StatusOK || len(got) != 0 {
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
	if len(got) != 1 || string(got["flows"]) == "" {
		t.Fatalf("expected only flows: %+v", got)
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

func TestViewsPageKeyRemovedOnPartialPut(t *testing.T) {
	_, base := viewsServer(t, filepath.Join(t.TempDir(), "views.json"))
	c, csrf := authedClient(t, base)

	code, _ := viewsPut(t, c, base, csrf, map[string]any{
		"range": map[string]string{"id": "today"},
		"flows": map[string]string{"q": "keep"},
	})
	if code != http.StatusOK {
		t.Fatalf("initial PUT: %d", code)
	}

	code, got := viewsPut(t, c, base, csrf, map[string]any{
		"flows": map[string]string{"q": "keep"},
	})
	if code != http.StatusOK {
		t.Fatalf("partial PUT: %d", code)
	}
	if _, ok := got["range"]; ok {
		t.Fatalf("range must be removed: %+v", got)
	}
	if string(got["flows"]) == "" {
		t.Fatalf("flows must remain: %+v", got)
	}
}

func TestViewsEmptyPutDeletesUser(t *testing.T) {
	viewsPath := filepath.Join(t.TempDir(), "views.json")
	_, base := viewsServer(t, viewsPath)
	c, csrf := authedClient(t, base)

	code, _ := viewsPut(t, c, base, csrf, map[string]any{"flows": map[string]string{"q": "x"}})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	code, got := viewsPut(t, c, base, csrf, map[string]any{})
	if code != http.StatusOK || len(got) != 0 {
		t.Fatalf("empty PUT: %d %+v", code, got)
	}
	code, got = viewsGet(t, c, base, csrf)
	if code != http.StatusOK || len(got) != 0 {
		t.Fatalf("GET after delete: %d %+v", code, got)
	}
	disk, _ := os.ReadFile(viewsPath)
	if strings.Contains(string(disk), "env:admin") {
		t.Fatalf("user entry must be gone: %s", disk)
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
