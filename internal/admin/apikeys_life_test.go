package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// startKeyApp is startKeyServer that also returns the admin server, so a test
// can call FlushKeyUse. path is the key file. A setup error fails the test.
func startKeyApp(t *testing.T, path string) (*Server, *httptest.Server) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Offers:       NewJSONStore[config.Offer](filepath.Join(t.TempDir(), "offers.json")),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		APIKeysFile:  path,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

// TestAPIKeyLifetime covers ttl_days on create and update, the key_expired
// answer, the recorded last use and its write to the key file.
func TestAPIKeyLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	app, srv := startKeyApp(t, path)
	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login status %d", code)
	}
	if code, body := doReq(t, c, http.MethodPost, srv.URL+"/api/api-keys", csrf, "", `{"name":"bad","scopes":["admin"],"ttl_days":14}`); code != http.StatusBadRequest {
		t.Fatalf("ttl_days 14: %d %s", code, body)
	}
	code, body := doReq(t, c, http.MethodPost, srv.URL+"/api/api-keys", csrf, "", `{"name":"month","scopes":["admin"],"ttl_days":30}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created apiKeyCreated
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Item.ExpiresAt == nil || created.Item.ExpiresAt.Sub(time.Now()) < 29*24*time.Hour {
		t.Fatalf("expires_at after ttl_days 30: %v", created.Item.ExpiresAt)
	}
	if created.Item.LastUsedAt != nil {
		t.Fatal("a new key has no use")
	}

	if code, _ := doReq(t, nil, http.MethodGet, srv.URL+"/api/openapi.json", "", created.Key, ""); code != http.StatusOK {
		t.Fatalf("live key: %d", code)
	}
	listed := keyByID(t, c, srv.URL, created.Item.ID)
	if listed.LastUsedAt == nil || listed.LastUsedIP != "127.0.0.x" {
		t.Fatalf("last use after a request: %+v", listed)
	}
	app.FlushKeyUse()
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"last_used_at"`) {
		t.Fatalf("flushed key file: %s %v", data, err)
	}

	past := time.Now().Add(-time.Minute)
	if _, err := app.keys.SetExpiry(created.Item.ID, &past); err != nil {
		t.Fatal(err)
	}
	code, body = doReq(t, nil, http.MethodGet, srv.URL+"/api/openapi.json", "", created.Key, "")
	if code != http.StatusUnauthorized || !strings.Contains(string(body), `"code":"key_expired"`) {
		t.Fatalf("expired key: %d %s", code, body)
	}

	if code, body := doReq(t, c, http.MethodPut, srv.URL+"/api/api-keys/"+created.Item.ID, csrf, "", `{"ttl_days":0}`); code != http.StatusOK {
		t.Fatalf("extend: %d %s", code, body)
	}
	if got := keyByID(t, c, srv.URL, created.Item.ID); got.ExpiresAt != nil || got.Prefix != created.Item.Prefix {
		t.Fatalf("after extend to never: %+v", got)
	}
	if code, _ := doReq(t, nil, http.MethodGet, srv.URL+"/api/openapi.json", "", created.Key, ""); code != http.StatusOK {
		t.Fatalf("extended key keeps its secret: %d", code)
	}
}

// keyByID returns the listed key id from GET /api/api-keys. A missing key fails the test.
func keyByID(t *testing.T, c *http.Client, base, id string) security.APIKey {
	t.Helper()
	code, body := doReq(t, c, http.MethodGet, base+"/api/api-keys", "", "", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	var list []security.APIKey
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	for _, k := range list {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("key %s not listed", id)
	return security.APIKey{}
}
