package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// httpsOf runs IsHTTPS on a request that went through the RealIP middleware
// with 10.0.0.1 as the only trusted proxy.
func httpsOf(t *testing.T, remote, proto string, withTLS bool) bool {
	t.Helper()
	rip, err := NewRealIP([]string{"10.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	h := rip.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = IsHTTPS(r) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	if proto != "" {
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	if withTLS {
		req.TLS = &tls.ConnectionState{}
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestIsHTTPS(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		proto  string
		tls    bool
		want   bool
	}{
		{"plain", "10.0.0.1:5", "", false, false},
		{"trusted proxy https", "10.0.0.1:5", "https", false, true},
		{"trusted proxy HTTPS upper case", "10.0.0.1:5", "HTTPS", false, true},
		{"trusted proxy chain", "10.0.0.1:5", "https, http", false, true},
		{"trusted proxy http", "10.0.0.1:5", "http", false, false},
		{"untrusted peer claims https", "8.8.8.8:5", "https", false, false},
		{"own TLS", "8.8.8.8:5", "", true, true},
		{"own TLS beats the header", "8.8.8.8:5", "http", true, true},
	}
	for _, tc := range cases {
		if got := httpsOf(t, tc.remote, tc.proto, tc.tls); got != tc.want {
			t.Errorf("%s: IsHTTPS = %v, want %v", tc.name, got, tc.want)
		}
	}
	if IsHTTPS(nil) {
		t.Error("a nil request is not HTTPS")
	}
}
