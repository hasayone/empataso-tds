package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

// denyLogin refuses every login attempt.
//
// Allow returns false and a nil error. key is the client address. API key
// requests must not call it.
type denyLogin struct{}

// Allow reports that this login is refused. key is ignored. The error is nil.
func (denyLogin) Allow(context.Context, string) (bool, error) { return false, nil }

// startKeyServer starts an admin HTTP server whose API keys are stored at path.
//
// An empty path disables keys. lim nil allows every login. The offers catalog
// is an empty file in a temp directory. A bad password hash fails the test.
func startKeyServer(t *testing.T, path string, lim LoginLimiter) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	if lim == nil {
		lim = allowAll{}
	}
	dir := t.TempDir()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Limiter:      lim,
		SessionTTL:   time.Hour,
		CookieSecure: false,
		APIKeysFile:  path,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// doReq performs one HTTP request and returns the status and the body.
//
// c nil sends no cookies. An empty csrf or apiKey omits that header. body may
// be empty. A transport error fails the test.
func doReq(t *testing.T, c *http.Client, method, url, csrf, apiKey, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if apiKey != "" {
		req.Header.Set("Api-Key", apiKey)
	}
	if c == nil {
		c = &http.Client{}
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

// errText returns the JSON error field. Malformed JSON fails the test.
func errText(t *testing.T, b []byte) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out.Error
}

// spoilKey flips the last byte of a secret taken from a create response.
// The result is not a stored key. There is no error.
func spoilKey(raw string) string {
	b := []byte(raw)
	b[len(b)-1] ^= 1
	return string(b)
}

// TestAPIKeyCookieAndHeader checks create, list, file, header auth and revocation.
func TestAPIKeyCookieAndHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	srv := startKeyServer(t, path, nil)
	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK || csrf == "" {
		t.Fatalf("login status %d", code)
	}

	createdBody, _ := json.Marshal(map[string]any{
		"name": "panel", "scopes": []string{security.ScopeAdmin},
	})
	code, respBody := doReq(t, c, http.MethodPost, srv.URL+"/api/api-keys", csrf, "", string(createdBody))
	if code != http.StatusCreated {
		t.Fatalf("create status %d", code)
	}
	if bytes.Contains(respBody, []byte(`"hash"`)) {
		t.Fatal("create response contains a hash field")
	}
	var created struct {
		Key  string          `json:"key"`
		Item security.APIKey `json:"item"`
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		t.Fatal(err)
	}
	raw := created.Key
	if !strings.HasPrefix(raw, "kz_") || len(raw) < 11 || raw[3:11] != created.Item.Prefix || created.Item.Hash != "" {
		t.Fatal("create response key shape")
	}
	if created.Item.ID == "" || !created.Item.HasScope(security.ScopeAdmin) {
		t.Fatal("create response item")
	}

	code, listBody := doReq(t, c, http.MethodGet, srv.URL+"/api/api-keys", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("list status %d", code)
	}
	if bytes.Contains(listBody, []byte(raw)) || bytes.Contains(listBody, []byte(`"hash"`)) {
		t.Fatal("list contains the raw key or a hash")
	}
	assertFileHides(t, path, raw)

	locked := startKeyServer(t, path, denyLogin{})
	code, _ = doReq(t, nil, http.MethodGet, locked.URL+"/api/offers", "", raw, "")
	if code != http.StatusOK {
		t.Fatalf("api key GET status %d", code)
	}
	if code, _ = login(t, newClient(t), locked.URL, "admin", "p@ss"); code != http.StatusTooManyRequests {
		t.Fatalf("login limiter status %d", code)
	}

	code, offerBody := doReq(t, nil, http.MethodPost, srv.URL+"/api/offers", "", raw, `{}`)
	if code == http.StatusForbidden {
		t.Fatalf("api key POST forbidden: %s", errText(t, offerBody))
	}
	code, offerBody = doReq(t, c, http.MethodPost, srv.URL+"/api/offers", "", "", `{}`)
	if code != http.StatusForbidden || errText(t, offerBody) != "csrf" {
		t.Fatalf("cookie POST without csrf status %d", code)
	}

	code, denied := doReq(t, nil, http.MethodPost, srv.URL+"/api/api-keys", "", raw, string(createdBody))
	if code != http.StatusForbidden || errText(t, denied) != "cookie session required" {
		t.Fatalf("create via api key status %d", code)
	}
	code, denied = doReq(t, nil, http.MethodDelete, srv.URL+"/api/api-keys/"+created.Item.ID, "", raw, "")
	if code != http.StatusForbidden || errText(t, denied) != "cookie session required" {
		t.Fatalf("delete via api key status %d", code)
	}
	code, _ = doReq(t, nil, http.MethodGet, srv.URL+"/api/offers", "", raw, "")
	if code != http.StatusOK {
		t.Fatalf("key after refused delete status %d", code)
	}

	pw := `{"old":"p@ss","new":"n3w-p@ss"}`
	code, denied = doReq(t, nil, http.MethodPost, srv.URL+"/api/password", "", raw, pw)
	if code != http.StatusForbidden || errText(t, denied) != "cookie session required" {
		t.Fatalf("password via api key status %d", code)
	}
	code, denied = doReq(t, nil, http.MethodGet, srv.URL+"/api/me", "", raw, "")
	if code != http.StatusForbidden || errText(t, denied) != "cookie session required" {
		t.Fatalf("me via api key status %d", code)
	}
	code, denied = doReq(t, nil, http.MethodPost, srv.URL+"/api/logout", "", raw, "")
	if code != http.StatusForbidden || errText(t, denied) != "cookie session required" {
		t.Fatalf("logout via api key status %d", code)
	}
	code, _ = doReq(t, c, http.MethodGet, srv.URL+"/api/me", "", raw, "")
	if code != http.StatusForbidden {
		t.Fatalf("cookie plus api key on /api/me status %d", code)
	}

	engBody, _ := json.Marshal(map[string]any{
		"name": "worker", "scopes": []string{security.ScopeEngine},
	})
	code, engResp := doReq(t, c, http.MethodPost, srv.URL+"/api/api-keys", csrf, "", string(engBody))
	if code != http.StatusCreated {
		t.Fatalf("engine key status %d", code)
	}
	if bytes.Contains(engResp, []byte(`"hash"`)) {
		t.Fatal("engine key response contains a hash field")
	}
	var eng struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(engResp, &eng); err != nil {
		t.Fatal(err)
	}
	code, _ = doReq(t, nil, http.MethodGet, srv.URL+"/api/offers", "", eng.Key, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("engine scope status %d", code)
	}
	code, listBody = doReq(t, c, http.MethodGet, srv.URL+"/api/api-keys", "", "", "")
	if code != http.StatusOK || bytes.Contains(listBody, []byte(raw)) || bytes.Contains(listBody, []byte(eng.Key)) || bytes.Contains(listBody, []byte(`"hash"`)) {
		t.Fatal("list contains a raw key or a hash")
	}

	code, _ = doReq(t, nil, http.MethodGet, srv.URL+"/api/offers", "", spoilKey(raw), "")
	if code != http.StatusUnauthorized {
		t.Fatalf("foreign key status %d", code)
	}
	code, _ = doReq(t, c, http.MethodGet, srv.URL+"/api/offers", "", spoilKey(raw), "")
	if code != http.StatusUnauthorized {
		t.Fatalf("foreign key with cookie status %d", code)
	}
	code, _ = doReq(t, nil, http.MethodGet, srv.URL+"/api/offers", "", "", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("empty auth status %d", code)
	}

	bad, _ := json.Marshal(map[string]any{
		"name": " ", "scopes": []string{security.ScopeAdmin},
	})
	code, badBody := doReq(t, c, http.MethodPost, srv.URL+"/api/api-keys", csrf, "", string(bad))
	if code != http.StatusBadRequest || !strings.Contains(errText(t, badBody), "name") {
		t.Fatalf("bad name status %d", code)
	}
	code, miss := doReq(t, c, http.MethodDelete, srv.URL+"/api/api-keys/missing", csrf, "", "")
	if code != http.StatusNotFound || errText(t, miss) != "not found" {
		t.Fatalf("missing key status %d", code)
	}

	code, _ = doReq(t, c, http.MethodDelete, srv.URL+"/api/api-keys/"+created.Item.ID, csrf, "", "")
	if code != http.StatusNoContent {
		t.Fatalf("delete status %d", code)
	}
	code, _ = doReq(t, nil, http.MethodGet, srv.URL+"/api/offers", "", raw, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked key status %d", code)
	}
	assertFileHides(t, path, raw, eng.Key)

	if code, _ = login(t, newClient(t), srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("password unchanged status %d", code)
	}
}

// TestAPIKeysDisabled checks an empty APIKeysFile: list, create, and the header.
func TestAPIKeysDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	on := startKeyServer(t, path, nil)
	c := newClient(t)
	code, csrf := login(t, c, on.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login status %d", code)
	}
	body, _ := json.Marshal(map[string]any{
		"name": "panel", "scopes": []string{security.ScopeAdmin},
	})
	code, respBody := doReq(t, c, http.MethodPost, on.URL+"/api/api-keys", csrf, "", string(body))
	if code != http.StatusCreated {
		t.Fatalf("create status %d", code)
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		t.Fatal(err)
	}

	off := startKeyServer(t, "", nil)
	c2 := newClient(t)
	code, csrf = login(t, c2, off.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("disabled login status %d", code)
	}
	code, listBody := doReq(t, c2, http.MethodGet, off.URL+"/api/api-keys", "", "", "")
	if code != http.StatusOK || strings.TrimSpace(string(listBody)) != "[]" {
		t.Fatalf("disabled list status %d body %s", code, strings.TrimSpace(string(listBody)))
	}
	code, postBody := doReq(t, c2, http.MethodPost, off.URL+"/api/api-keys", csrf, "", string(body))
	if code != http.StatusServiceUnavailable || errText(t, postBody) != "api keys disabled" {
		t.Fatalf("disabled create status %d", code)
	}
	code, _ = doReq(t, c2, http.MethodGet, off.URL+"/api/offers", "", created.Key, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("disabled header status %d", code)
	}
	code, _ = doReq(t, c2, http.MethodGet, off.URL+"/api/offers", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("cookie without header status %d", code)
	}
}

// assertFileHides fails when path contains any raw secret.
func assertFileHides(t *testing.T, path string, raws ...string) {
	t.Helper()
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range raws {
		if raw != "" && bytes.Contains(disk, []byte(raw)) {
			t.Fatal("key file contains a raw key")
		}
	}
}
