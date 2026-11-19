package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/geo"
	"github.com/hasayone/empataso-tds/internal/security"
)

// geoDownloadPattern is the route that starts a geo database download.
const geoDownloadPattern = "POST /api/tools/geo/download"

// Reasons of a refused geo download, in the params of the 409 answer.
const (
	geoReasonNeedsKey  = "needs_key"  // only MaxMind sources without a license key fill the slot
	geoReasonNoPath    = "no_path"    // the cron service has no file for this database
	geoReasonSourceSet = "source_set" // a working city source is configured, DB-IP is not switched in
)

// geoToolsDB is one geo database row of the Tools page: what is loaded and how
// the cron config fills its slot. FreeAlt names the free source the panel may
// offer instead of a key ("dbip" for the city slot), or is empty.
type geoToolsDB struct {
	geo.Info
	Source   string `json:"source"`
	Ready    bool   `json:"ready"`
	NeedsKey bool   `json:"needs_key"`
	FreeAlt  string `json:"free_alt,omitempty"`
}

// geoToolsState is the answer of GET /api/tools/geo.
// Cron is true when the admin knows the cron config, Alive when the cron service
// wrote a heartbeat recently. Job is the last geo_db run. CanDownload tells the
// panel whether the caller may start a download.
type geoToolsState struct {
	Databases   []geoToolsDB   `json:"databases"`
	CanDownload bool           `json:"can_download"`
	Cron        bool           `json:"cron"`
	Alive       bool           `json:"alive"`
	Job         cron.JobStatus `json:"job"`
}

// geoReloader is the part of geo.DB that re-reads changed files at once.
type geoReloader interface {
	Reload() []string
}

// handleToolsGeo writes the geo database state of the Tools page. The admin
// resolver re-reads changed files first, so a database the cron service has just
// installed shows as loaded. A missing or unreadable cron config leaves the slots
// empty; it is not an error.
func (s *Server) handleToolsGeo(w http.ResponseWriter, r *http.Request) {
	if rl, ok := s.cfg.Geo.(geoReloader); ok {
		rl.Reload()
	}
	st := geoToolsState{Databases: []geoToolsDB{}, CanDownload: s.canDownloadGeo(sessionFrom(r.Context()))}
	cfg := cron.Default()
	if s.cfg.CronFile != "" {
		st.Cron = true
		if loaded, err := cron.Load(s.cfg.CronFile); err == nil {
			cfg = loaded
		}
		if status, err := cron.ReadStatus(cron.StatusPath(s.cfg.CronFile)); err == nil {
			st.Alive = time.Since(status.Heartbeat) < cronAliveWindow
			st.Job = status.Jobs[cron.JobGeoDB]
		}
	}
	slots := map[string]cron.GeoSlot{}
	for _, slot := range cfg.GeoSlots() {
		slots[slot.Kind] = slot
	}
	if s.cfg.Geo != nil {
		for _, in := range s.cfg.Geo.Databases() {
			slot := slots[in.Kind]
			row := geoToolsDB{Info: in, Source: slot.Source, Ready: slot.Ready, NeedsKey: slot.NeedsKey}
			if in.Kind == geo.KindCity && !slot.Ready {
				row.FreeAlt = cron.GeoSourceDBIP
			}
			st.Databases = append(st.Databases, row)
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// canDownloadGeo reports whether sess may call geoDownloadPattern: the route rule
// allows it and, for an API key, the key holds the cron action.
func (s *Server) canDownloadGeo(sess security.Session) bool {
	rule := routeAccess[geoDownloadPattern]
	if !s.allowed(sess, rule) {
		return false
	}
	return !sess.API || slices.Contains(sess.KeyActions, rule.action)
}

// geoTargetOf returns the file the cron service writes for a database kind, or ""
// for an unknown kind or a kind without a path.
func (s *Server) geoTargetOf(kind string) string {
	switch kind {
	case geo.KindCity:
		return s.cfg.GeoCityPath
	case geo.KindASN:
		return s.cfg.GeoASNPath
	case geo.KindDBIPASN:
		return s.cfg.GeoDBIPPath
	}
	return ""
}

// handleToolsGeoDownload asks the cron service to run geo_db now for one database.
// The body is {"kind": "city"|"asn"|"dbip_asn", "source": ""|"dbip"}. Source dbip
// is for the city database only: the keyless MaxMind city source in cron.json is
// replaced by DB-IP City Lite before the run. The run itself is the geo_db job,
// the same one the schedule starts; the engine and the admin pick the new file up
// on their reload interval.
//
// Answers: 202 queued; 400 bad body or kind; 409 with params.reason needs_key
// (a MaxMind license key is needed), no_path (the cron service has no file for the
// database) or source_set (a working city source is configured); 503 when the cron
// config is not known; 500 when cron.json cannot be read or written.
func (s *Server) handleToolsGeoDownload(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CronFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "cron is not configured (EMTDS_CRON_FILE)")
		return
	}
	var body struct {
		Kind   string `json:"kind"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if body.Kind != geo.KindCity && body.Kind != geo.KindASN && body.Kind != geo.KindDBIPASN {
		writeErr(w, http.StatusBadRequest, "kind must be city, asn or dbip_asn")
		return
	}
	if body.Source != "" && (body.Source != cron.GeoSourceDBIP || body.Kind != geo.KindCity) {
		writeErr(w, http.StatusBadRequest, "source dbip is for the city database only")
		return
	}
	if s.geoTargetOf(body.Kind) == "" {
		writeErrParams(w, http.StatusConflict, "no file path for this database", map[string]any{"reason": geoReasonNoPath})
		return
	}
	cfg, err := cron.Load(s.cfg.CronFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cron config unreadable")
		return
	}
	if body.Source == cron.GeoSourceDBIP {
		next, err := cfg.UseDBIPCity()
		if errors.Is(err, cron.ErrGeoCitySourceSet) {
			writeErrParams(w, http.StatusConflict, err.Error(), map[string]any{"reason": geoReasonSourceSet})
			return
		}
		if err := next.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := cron.Save(s.cfg.CronFile, next); err != nil {
			writeErr(w, http.StatusInternalServerError, "save error")
			return
		}
		cfg = next
	}
	for _, slot := range cfg.GeoSlots() {
		if slot.Kind == body.Kind && !slot.Ready {
			writeErrParams(w, http.StatusConflict, "a MaxMind license key is required", map[string]any{"reason": geoReasonNeedsKey})
			return
		}
	}
	if err := cron.RequestRun(s.cfg.CronFile, cron.JobGeoDB); err != nil {
		writeErr(w, http.StatusInternalServerError, "run request error")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}
