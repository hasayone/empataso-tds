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

func setupServer(t *testing.T, hash, dir string) *Server {
	t.Helper()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		PasswordFile: filepath.Join(dir, "admin.hash"),
		Sessions:     security.NewMemoryStore(),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		PublicIP:     "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetupRequiredWhenNoPassword(t *testing.T) {
	dir := t.TempDir()
	s := setupServer(t, "", dir)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	res, err := srv.Client().Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/me: %d", res.StatusCode)
	}
	res, err = srv.Client().Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/health: %d", res.StatusCode)
	}
	res, err = srv.Client().Get(srv.URL + "/api/brand")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/brand: %d", res.StatusCode)
	}
	res, err = srv.Client().Get(srv.URL + "/api/setup")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var view setupView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if !view.Needed || view.PublicIP != "203.0.113.10" {
		t.Fatalf("setup view: %+v", view)
	}
}

func TestSetupWritesHashAndEmailAndLogsIn(t *testing.T) {
	dir := t.TempDir()
	s := setupServer(t, "", dir)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := srv.Client()
	client.Jar = jar

	body := map[string]string{"login": "owner", "password": "secret1", "email": "owner@example.com"}
	raw, _ := json.Marshal(body)
	res, err := client.Post(srv.URL+"/api/setup", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	respBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/setup: %d %s", res.StatusCode, respBody)
	}
	for _, name := range []string{"admin.hash", "admin.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", name, info.Mode().Perm())
		}
	}
	profile, err := os.ReadFile(filepath.Join(dir, "admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(profile), "secret1") || !strings.Contains(string(profile), "owner@example.com") {
		t.Fatalf("profile: %s", profile)
	}
	res, err = client.Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	meRaw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/me: %d %s", res.StatusCode, meRaw)
	}
	var me meResponse
	if err := json.Unmarshal(meRaw, &me); err != nil {
		t.Fatal(err)
	}
	if me.User != "owner" || me.Email != "owner@example.com" {
		t.Fatalf("me: %+v", me)
	}
	res, err = client.Post(srv.URL+"/api/setup", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("second POST: %d", res.StatusCode)
	}
}

func TestSetupRejectsBadFields(t *testing.T) {
	dir := t.TempDir()
	s := setupServer(t, "", dir)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	cases := []map[string]string{
		{"login": "owner", "password": "secret1", "email": "nope"},
		{"login": "owner", "password": "short", "email": "owner@example.com"},
		{"login": "owner", "password": "admin", "email": "owner@example.com"},
		{"login": "a", "password": "secret1", "email": "owner@example.com"},
		{"login": "a b", "password": "secret1", "email": "owner@example.com"},
	}
	for _, body := range cases {
		raw, _ := json.Marshal(body)
		res, err := srv.Client().Post(srv.URL+"/api/setup", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("%v: %d", body, res.StatusCode)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "admin.hash")); !os.IsNotExist(err) {
		t.Fatal("hash file was written")
	}
}

func TestSetupSkippedWhenPasswordSet(t *testing.T) {
	dir := t.TempDir()
	s := setupServer(t, goodHash(t), dir)
	if s.setup.Load() {
		t.Fatal("password from the environment must not open setup")
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	res, err := srv.Client().Get(srv.URL + "/api/setup")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var view setupView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Needed {
		t.Fatal("needed")
	}
	raw, _ := json.Marshal(map[string]string{"login": "owner", "password": "secret1", "email": "owner@example.com"})
	res, err = srv.Client().Post(srv.URL+"/api/setup", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("POST: %d", res.StatusCode)
	}
}
