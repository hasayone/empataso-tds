package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLocalHost covers the Host values the local-machine signal reads.
func TestLocalHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8090":       true,
		"127.0.0.1":            true,
		"localhost":            true,
		"LOCALHOST:8090":       true,
		"[::1]:8090":           true,
		"[::1]":                true,
		"::1":                  true,
		"192.168.1.20:8090":    true,
		"10.0.0.5":             true,
		"172.18.0.2:8090":      true,
		"169.254.1.1":          true,
		"100.64.0.9":           true,
		"emtds-admin:8090":     true,
		"admin.localhost":      true,
		"mac.local":            true,
		"host.docker.internal": true,
		"203.0.113.10":         false,
		"8.8.8.8:8090":         false,
		"tds.example.com":      false,
		"admin.example.com:80": false,
		"":                     false,
	}
	for host, want := range cases {
		if got := localHost(host); got != want {
			t.Errorf("localHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestLocalAccessNeedsNoPublicIP checks that a configured public IP or a nil request is never local.
func TestLocalAccessNeedsNoPublicIP(t *testing.T) {
	loopback := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090/api/cloudflare", nil)
	named := httptest.NewRequest(http.MethodGet, "http://tds.example.com/api/cloudflare", nil)
	cases := []struct {
		name string
		ip   string
		req  *http.Request
		want bool
	}{
		{"loopback without public ip", "", loopback, true},
		{"loopback with public ip", "203.0.113.10", loopback, false},
		{"public name without public ip", "", named, false},
		{"nil request", "", nil, false},
	}
	for _, c := range cases {
		s := &Server{cfg: Config{PublicIP: c.ip}}
		if got := s.localAccess(c.req); got != c.want {
			t.Errorf("%s: localAccess = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSettingsReportLocal checks that GET /api/cloudflare and GET /api/registrars/namecheap
// carry "local" while the admin has no public IP and is reached on loopback.
func TestSettingsReportLocal(t *testing.T) {
	paths := []string{"/api/cloudflare", "/api/registrars/namecheap"}
	for _, tc := range []struct {
		name string
		tune func(*Config)
		want bool
	}{
		{"no public ip", func(c *Config) { c.PublicIP = "" }, true},
		{"public ip set", nil, false},
	} {
		e := newCFEnv(t, tc.tune)
		for _, p := range paths {
			code, raw := e.do(t, http.MethodGet, p, nil)
			if code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", tc.name, p, code, raw)
			}
			var v struct {
				Local bool `json:"local"`
			}
			if err := json.Unmarshal([]byte(raw), &v); err != nil || v.Local != tc.want {
				t.Fatalf("%s %s: local = %v (%v), want %v: %s", tc.name, p, v.Local, err, tc.want, raw)
			}
		}
	}
}
