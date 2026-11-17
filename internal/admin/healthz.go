package admin

import (
	"net/http"

	"github.com/hasayone/empataso-tds/internal/config"
)

// handleHealthz answers GET /healthz for liveness probes without authentication.
// The body is always ok. X-Emtds-Host is the request Host, X-Emtds-Service is admin,
// and X-Emtds-Probe echoes a valid probe query parameter.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Emtds-Host", r.Host)
	w.Header().Set("X-Emtds-Service", "admin")
	if probe := r.URL.Query().Get("probe"); config.ValidHealthzProbe(probe) {
		w.Header().Set("X-Emtds-Probe", probe)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
