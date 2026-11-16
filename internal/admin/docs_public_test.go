package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

// docsPublicPaths lists the routes the docs setting opens and closes.
var docsPublicPaths = []string{"/docs", "/api/docs/openapi.json", "/api/docs/postman.json", "/api/docs.md", "/api/docs.md?lang=ru", "/llms.txt"}

// privateAddr matches a loopback or RFC 1918 IPv4 address.
var privateAddr = regexp.MustCompile(`\b(127\.0\.0\.1|10\.\d+\.\d+\.\d+|192\.168\.\d+\.\d+|172\.(1[6-9]|2\d|3[01])\.\d+\.\d+)\b`)

// startDocsApp starts an admin with a key file and a docs settings file in a temp dir.
// It returns the server and the raw secret of one issued key. A setup error fails the test.
func startDocsApp(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		APIKeysFile:  filepath.Join(dir, "keys.json"),
		DocsFile:     filepath.Join(dir, "docs.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := s.keys.Generate("docs", []string{security.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, raw
}

// TestPublicDocs covers the public reference: it answers without a session, lists
// every route including cookie-only ones, holds no key or private address, is cached,
// and answers 404 on every path once an administrator turns it off.
func TestPublicDocs(t *testing.T) {
	srv, raw := startDocsApp(t)
	for _, p := range docsPublicPaths {
		code, body := doReq(t, nil, http.MethodGet, srv.URL+p, "", "", "")
		if code != http.StatusOK {
			t.Fatalf("%s without a session: %d %s", p, code, body)
		}
		if p == "/docs" {
			if string(body) != string(indexHTML) {
				t.Fatal("/docs serves the SPA shell that / already serves")
			}
			continue
		}
		if strings.Contains(string(body), raw) || privateAddr.Match(body) {
			t.Fatalf("%s leaks a secret or a private address", p)
		}
	}

	resp, err := http.Get(srv.URL + "/api/docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	err = json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Cache-Control"); got != docsCacheControl {
		t.Fatalf("Cache-Control %q", got)
	}
	if doc.Paths["/api/api-keys"]["get"]["x-emtds-cookie-only"] != true {
		t.Fatal("the public catalog lists cookie-only routes with their mark")
	}
	if doc.Paths["/api/keitaro/purge"]["post"]["x-emtds-action"] != security.ActionKeitaroPurge {
		t.Fatal("the public catalog lists gated routes with their tick")
	}
	etag := resp.Header.Get("ETag")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/docs/openapi.json", nil)
	req.Header.Set("If-None-Match", etag)
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if etag == "" || again.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation: etag %q status %d", etag, again.StatusCode)
	}

	_, md := doReq(t, nil, http.MethodGet, srv.URL+"/api/docs.md?lang=ru", "", "", "")
	for _, want := range []string{"# API админки emtds", "### `GET /api/offers`", "Галочка ключа: `keitaro_purge`."} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("docs.md misses %q", want)
		}
	}

	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login status %d", code)
	}
	if code, body := doReq(t, c, http.MethodGet, srv.URL+"/api/docs/settings", "", "", ""); code != http.StatusOK || !strings.Contains(string(body), `"public":true`) {
		t.Fatalf("default setting: %d %s", code, body)
	}
	if code, _ := doReq(t, nil, http.MethodPut, srv.URL+"/api/docs/settings", "", raw, `{"public":false}`); code != http.StatusForbidden {
		t.Fatalf("a key may not change the setting: %d", code)
	}
	if code, body := doReq(t, c, http.MethodPut, srv.URL+"/api/docs/settings", csrf, "", `{"public":false}`); code != http.StatusOK {
		t.Fatalf("turn off: %d %s", code, body)
	}
	for _, p := range docsPublicPaths {
		if code, _ := doReq(t, nil, http.MethodGet, srv.URL+p, "", "", ""); code != http.StatusNotFound {
			t.Fatalf("%s while off: %d", p, code)
		}
	}
	if code, _ := doReq(t, nil, http.MethodGet, srv.URL+"/api/openapi.json", "", raw, ""); code != http.StatusOK {
		t.Fatalf("the key catalog stays on: %d", code)
	}
	if code, _ := doReq(t, nil, http.MethodGet, srv.URL+"/api/postman.json", "", raw, ""); code != http.StatusOK {
		t.Fatalf("the key postman collection stays on: %d", code)
	}
}

// TestReadDocsSettingsDefaultsToPublic requires a missing or empty file to keep the docs open
// and invalid JSON to fail.
func TestReadDocsSettingsDefaultsToPublic(t *testing.T) {
	dir := t.TempDir()
	if v, err := readDocsSettings(filepath.Join(dir, "none.json")); err != nil || !v.Public {
		t.Fatalf("missing file: %+v %v", v, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := writeFileAtomic(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDocsSettings(bad); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}
