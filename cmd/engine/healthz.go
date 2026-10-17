package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/egerkuzma/kuztds/internal/logbuf"
)

// healthzHandler answers the liveness probe.
// logs may be nil; the loss headers are then omitted.
// The body is always "ok". X-Kuztds-Host is the request Host.
// X-Kuztds-Probe echoes the probe query when it is 1 to 64 lowercase hex digits.
// The handler does not return an error; it always writes status 200.
func healthzHandler(logs *logbuf.Buffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Body stays exactly "ok" for existing probes; the loss counter rides
		// along in a header so it can be scraped without a metrics endpoint.
		if logs != nil {
			l := logs.Losses()
			w.Header().Set("X-Events-Lost", strconv.FormatInt(l.Total(), 10))
			w.Header().Set("X-Events-Lost-Detail", fmt.Sprintf("full=%d queue=%d insert=%d late=%d", l.Full, l.Queue, l.Insert, l.Late))
		}
		w.Header().Set("X-Kuztds-Host", r.Host)
		if probe := r.URL.Query().Get("probe"); validProbe(probe) {
			w.Header().Set("X-Kuztds-Probe", probe)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

// validProbe reports whether probe matches ^[0-9a-f]{1,64}$.
// probe is the raw query value. It returns false when probe is empty, longer than 64, or not lowercase hex.
func validProbe(probe string) bool {
	if len(probe) == 0 || len(probe) > 64 {
		return false
	}
	for i := 0; i < len(probe); i++ {
		c := probe[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
