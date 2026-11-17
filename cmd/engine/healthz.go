package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// healthzHandler answers the liveness probe.
// logs may be nil; the loss headers are then omitted.
// The body is always "ok". X-Emtds-Host is the request Host.
// X-Emtds-Probe echoes the probe query when it is 1 to 64 lowercase hex digits.
// The handler does not return an error; it always writes status 200.
func healthzHandler(logs *logbuf.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Body stays exactly "ok" for existing probes; the loss counter rides
		// along in a header so it can be scraped without a metrics endpoint.
		if logs != nil {
			l := logs.Losses()
			w.Header().Set("X-Events-Lost", strconv.FormatInt(l.Total(), 10))
			w.Header().Set("X-Events-Lost-Detail", fmt.Sprintf("full=%d queue=%d insert=%d late=%d spool_full=%d", l.Full, l.Queue, l.Insert, l.Late, l.SpoolFull))
		}
		w.Header().Set("X-Emtds-Host", r.Host)
		w.Header().Set("X-Emtds-Service", "engine")
		if probe := r.URL.Query().Get("probe"); config.ValidHealthzProbe(probe) {
			w.Header().Set("X-Emtds-Probe", probe)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
