package admin

import (
	"net/http"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// adminHostMiddleware rejects requests to admin.* hosts that are not allowed parked domains.
// Every path, including GET /healthz, is checked when Host is admin.*.
func (s *Server) adminHostMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := config.NormalizeHTTPHost(r.Host)
		if strings.HasPrefix(host, "admin.") {
			if s.cfg.Domains == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			list, err := s.cfg.Domains.List(r.Context())
			if err != nil {
				writeErr(w, http.StatusServiceUnavailable, "store unavailable")
				return
			}
			if !config.AdminHostAllowed(list, host) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// originMiddleware rejects unsafe requests whose Origin header does not match the request Host.
func originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" {
				host := r.Host
				if !strings.EqualFold(origin, "https://"+host) && !strings.EqualFold(origin, "http://"+host) {
					writeErr(w, http.StatusForbidden, "origin")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
