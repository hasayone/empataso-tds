package admin

import (
	"net/http"
	"time"

	"github.com/hasayone/empataso-tds/internal/geo"
)

// healthGeoDB is the state of one geo database in the answer of GET /api/health.
// Built is the build date from the database metadata, empty while not loaded.
type healthGeoDB struct {
	Loaded bool   `json:"loaded"`
	Built  string `json:"built,omitempty"`
}

// healthState is the answer of GET /api/health: Status is always ok while the
// process answers, Geo reports the databases the admin has loaded.
type healthState struct {
	Status string `json:"status"`
	Geo    struct {
		City healthGeoDB `json:"city"`
	} `json:"geo"`
}

// handleHealth answers GET /api/health without authentication. It is 200 with
// status ok whenever the process is up, with or without the country database:
// the container probe must not restart the admin because a download is pending.
// The geo.city field says whether the city database is loaded and when it was built.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := healthState{Status: "ok"}
	if s.cfg.Geo != nil {
		for _, in := range s.cfg.Geo.Databases() {
			if in.Kind == geo.KindCity && in.Loaded {
				st.Geo.City = healthGeoDB{Loaded: true, Built: in.Built.UTC().Format(time.RFC3339)}
			}
		}
	}
	writeJSON(w, http.StatusOK, st)
}
