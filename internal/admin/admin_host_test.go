package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

func adminHostServer(t *testing.T, domains []config.Domain) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := NewJSONStore[config.Domain](filepath.Join(dir, "domains.json"))
	if domains != nil {
		if err := store.Save(context.Background(), domains); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: allowAll{}, SessionTTL: time.Hour, Domains: store,
		AdminTrustedProxies: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func withHost(t *testing.T, method, url, host string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAdminHostMiddleware(t *testing.T) {
	srv := adminHostServer(t, []config.Domain{{ID: "1", Host: "park.com", Primary: true}})
	resp := withHost(t, http.MethodGet, srv.URL+"/", "admin.park.com", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed admin host: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = withHost(t, http.MethodGet, srv.URL+"/", "admin.other.com", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign admin host: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = withHost(t, http.MethodGet, srv.URL+"/", "127.0.0.1:8090", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ip host: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHealthzForeignAdminHost404(t *testing.T) {
	srv := adminHostServer(t, []config.Domain{{ID: "1", Host: "park.com", Primary: true}})
	resp := withHost(t, http.MethodGet, srv.URL+"/healthz?probe=abc123", "admin.other.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHealthzAllowedAdminHost(t *testing.T) {
	srv := adminHostServer(t, []config.Domain{{ID: "1", Host: "park.com", Primary: true}})
	resp := withHost(t, http.MethodGet, srv.URL+"/healthz?probe=abc123", "admin.park.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Emtds-Service") != "admin" {
		t.Fatalf("service %q", resp.Header.Get("X-Emtds-Service"))
	}
	if resp.Header.Get("X-Emtds-Probe") != "abc123" {
		t.Fatalf("probe %q", resp.Header.Get("X-Emtds-Probe"))
	}
}

func TestClientIPTrustedXFF(t *testing.T) {
	hash, _ := security.HashPassword("p")
	s, err := New(Config{
		AdminUser: "a", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: allowAll{}, AdminTrustedProxies: []string{"203.0.113.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.7")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	if ip := s.clientIP(req); ip != "198.51.100.7" {
		t.Fatalf("ip=%q", ip)
	}
	req.RemoteAddr = "198.51.100.9:443"
	if ip := s.clientIP(req); ip != "198.51.100.9" {
		t.Fatalf("untrusted ip=%q", ip)
	}
}

func TestSecurityHeadersAndOrigin(t *testing.T) {
	srv := adminHostServer(t, nil)
	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatalf("frame headers missing")
	}
	if resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("api cache %q", resp.Header.Get("Cache-Control"))
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"Login":"x","Password":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status %d", resp2.StatusCode)
	}
}

func TestLoginAdminHostWithoutRedisLimiter(t *testing.T) {
	srv := adminHostServer(t, []config.Domain{{ID: "1", Host: "park.com", Primary: true}})
	body := strings.NewReader(`{"Login":"admin","Password":"p@ss"}`)
	resp := withHost(t, http.MethodPost, srv.URL+"/api/login", "admin.park.com", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCookieSecureForwardedProto(t *testing.T) {
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		AdminTrustedProxies: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"Login":"admin","Password":"p@ss"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var secure bool
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			secure = c.Secure
		}
	}
	if !secure {
		t.Fatal("cookie not secure behind trusted https")
	}
}

func TestNewRejectsInvalidTrustedProxy(t *testing.T) {
	hash, err := security.HashPassword("p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{PasswordHash: hash, AdminTrustedProxies: []string{"not-a-prefix"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestClientIPInvalidRightmostXFF(t *testing.T) {
	hash, _ := security.HashPassword("p")
	s, err := New(Config{PasswordHash: hash, AdminTrustedProxies: []string{"203.0.113.1/32"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:443"
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	if ip := s.clientIP(req); ip != "203.0.113.1" {
		t.Fatalf("ip=%q", ip)
	}
}

func TestAdminHostMiddlewareDisabled(t *testing.T) {
	srv := adminHostServer(t, []config.Domain{{ID: "1", Host: "park.com", Primary: true, Disabled: true}})
	resp := withHost(t, http.MethodGet, srv.URL+"/", "admin.park.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAdminHostMiddlewareNilDomains(t *testing.T) {
	hash, _ := security.HashPassword("p")
	s, err := New(Config{PasswordHash: hash, Sessions: security.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp := withHost(t, http.MethodGet, srv.URL+"/", "admin.park.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestOriginOwnHostAllowed(t *testing.T) {
	srv := adminHostServer(t, nil)
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"Login":"x","Password":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+parsed.Host)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("origin rejected for own host")
	}
}

func TestCacheControlSPAAndAssets(t *testing.T) {
	srv := adminHostServer(t, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("root cache %q", resp.Header.Get("Cache-Control"))
	}
	resp, err = http.Get(srv.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK && resp.Header.Get("Cache-Control") != "private, max-age=86400" {
		t.Fatalf("assets cache %q status %d", resp.Header.Get("Cache-Control"), resp.StatusCode)
	}
}

func TestCookieSecureLogout(t *testing.T) {
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		SessionTTL: time.Hour, CookieSecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	loginReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"Login":"admin","Password":"p@ss"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	loginResp, err := client.Do(loginReq)
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()
	logoutReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/logout", nil)
	for _, c := range loginResp.Cookies() {
		logoutReq.AddCookie(c)
	}
	logoutResp, err := client.Do(logoutReq)
	if err != nil {
		t.Fatal(err)
	}
	logoutResp.Body.Close()
	for _, c := range logoutResp.Cookies() {
		if c.Name == cookieName && !c.Secure {
			t.Fatal("logout cookie not secure")
		}
	}
}

func TestCookieSecureUntrustedProto(t *testing.T) {
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		SessionTTL: time.Hour, CookieSecure: false, AdminTrustedProxies: []string{"198.51.100.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"Login":"admin","Password":"p@ss"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == cookieName && c.Secure {
			t.Fatal("untrusted proto must not set secure")
		}
	}
}
