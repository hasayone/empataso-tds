package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
)

func uiServer(t *testing.T, uiPath string) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
		UIPath:       uiPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestUIGetDefaultWithoutFile(t *testing.T) {
	dir := t.TempDir()
	uiPath := filepath.Join(dir, "ui.json")
	srv := uiServer(t, uiPath)
	c, csrf := authedClient(t, srv.URL)

	resp := csrfDo(t, c, http.MethodGet, srv.URL+"/api/ui", csrf, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ui: %d", resp.StatusCode)
	}
	var got uiPrefs
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Company.Name != defaultCompanyName || got.Company.Logo != "" {
		t.Fatalf("company: %+v", got.Company)
	}
	if got.Cols == nil {
		t.Fatal("cols must be non-nil map")
	}
	if len(got.Cols) != 0 {
		t.Fatalf("expected empty cols, got %d keys", len(got.Cols))
	}
}

func TestUIPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	uiPath := filepath.Join(dir, "ui.json")
	srv := uiServer(t, uiPath)
	c, csrf := authedClient(t, srv.URL)

	body := uiPrefs{
		Cols: map[string][]uiCol{
			"flows": {{ID: "url", On: false}},
		},
		Dash:    uiDash{Panels: nil, Dim: ""},
		Company: uiCompany{Name: "Acme", Logo: ""},
	}
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/ui", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/ui: %d", resp.StatusCode)
	}
	if _, err := os.Stat(uiPath); err != nil {
		t.Fatalf("ui file not created: %v", err)
	}

	resp = csrfDo(t, c, http.MethodGet, srv.URL+"/api/ui", csrf, nil)
	defer resp.Body.Close()
	var got uiPrefs
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Cols["flows"]) != 1 || got.Cols["flows"][0].ID != "url" || got.Cols["flows"][0].On {
		t.Fatalf("flows cols: %+v", got.Cols["flows"])
	}
	if got.Company.Name != "Acme" {
		t.Fatalf("company name: %q", got.Company.Name)
	}
}

func TestUIPutEmptyCompanyName(t *testing.T) {
	dir := t.TempDir()
	uiPath := filepath.Join(dir, "ui.json")
	srv := uiServer(t, uiPath)
	c, csrf := authedClient(t, srv.URL)

	body := uiPrefs{
		Cols:    map[string][]uiCol{},
		Company: uiCompany{Name: "   ", Logo: ""},
	}
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/ui", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	var got uiPrefs
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Company.Name != defaultCompanyName {
		t.Fatalf("name: %q", got.Company.Name)
	}
}

func TestUIPutBadLogo(t *testing.T) {
	srv := uiServer(t, filepath.Join(t.TempDir(), "ui.json"))
	c, csrf := authedClient(t, srv.URL)

	body := uiPrefs{
		Cols:    map[string][]uiCol{},
		Company: uiCompany{Name: "X", Logo: "http://x"},
	}
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/ui", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestUIPutBadColsKey(t *testing.T) {
	srv := uiServer(t, filepath.Join(t.TempDir(), "ui.json"))
	c, csrf := authedClient(t, srv.URL)

	body := map[string]any{
		"cols": map[string]any{
			"Bad Key": []uiCol{{ID: "a", On: true}},
		},
		"dash":    map[string]any{"panels": nil, "dim": ""},
		"company": map[string]string{"name": "X", "logo": ""},
	}
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/ui", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestBrandPublicNoCols(t *testing.T) {
	dir := t.TempDir()
	uiPath := filepath.Join(dir, "ui.json")
	srv := uiServer(t, uiPath)
	c := newClient(t)

	resp, err := c.Get(srv.URL + "/api/brand")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/brand: %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control: %q", cc)
	}
	if strings.Contains(string(raw), `"cols"`) {
		t.Fatalf("brand must not expose cols: %s", raw)
	}
	var got uiCompany
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != defaultCompanyName || got.Logo != "" {
		t.Fatalf("brand: %+v", got)
	}
}

func TestUIPutRequiresAuth(t *testing.T) {
	srv := uiServer(t, filepath.Join(t.TempDir(), "ui.json"))
	c := newClient(t)

	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/ui", "", uiPrefs{Cols: map[string][]uiCol{}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
