package admin

import (
	"net/http"

	"github.com/egerkuzma/kuztds/internal/config"
)

// handleHealthz answers GET /healthz for liveness probes without authentication.
// The body is always ok. X-Kuztds-Host is the request Host, X-Kuztds-Service is admin,
// and X-Kuztds-Probe echoes a valid probe query parameter.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Kuztds-Host", r.Host)
	w.Header().Set("X-Kuztds-Service", "admin")
	if probe := r.URL.Query().Get("probe"); config.ValidHealthzProbe(probe) {
		w.Header().Set("X-Kuztds-Probe", probe)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
