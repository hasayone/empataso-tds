package server

import (
	"net/http"
	"strings"
)

// IsHTTPS reports whether the visitor reached the engine over HTTPS.
//
// r is the request as the RealIP middleware passed it on. The answer is true
// when the connection itself is TLS, or when the request came through a trusted
// proxy that says X-Forwarded-Proto: https, the same trust the real IP and the
// CDN country header get. From an untrusted peer the header is text the client
// typed, so it is ignored.
func IsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if !ViaTrustedProxy(r.Context()) {
		return false
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
