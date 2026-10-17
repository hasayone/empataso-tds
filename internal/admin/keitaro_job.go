package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/kuztds/bridge/ktimport"
)

const keitaroJobTimeout = 2 * time.Hour

// keitaroJob is one background import and the lock that keeps a purge from
// overlapping it. The mutex guards every field below it.
type keitaroJob struct {
	mu         sync.Mutex
	running    bool
	stage      string
	done       int
	total      int
	counts     map[string]int
	losses     []ktimport.Loss
	errText    string
	startedAt  string
	finishedAt string
	busy       bool
}

// keitaroJobView is the JSON body of GET /api/keitaro/job.
// Times are RFC3339, or empty when unset. The key is not a field.
type keitaroJobView struct {
	Running    bool            `json:"running"`
	Stage      string          `json:"stage"`
	Done       int             `json:"done"`
	Total      int             `json:"total"`
	Counts     map[string]int  `json:"counts"`
	Losses     []ktimport.Loss `json:"losses"`
	Error      string          `json:"error"`
	StartedAt  string          `json:"started_at"`
	FinishedAt string          `json:"finished_at"`
}

// newKeitaroJob returns an idle job.
// The stage is idle. Counts is an empty map and losses is an empty slice.
// It does not return an error.
func newKeitaroJob() *keitaroJob {
	return &keitaroJob{
		stage:  "idle",
		counts: map[string]int{},
		losses: []ktimport.Loss{},
	}
}

// beginImport marks an import running when nothing else holds the job.
// It clears the previous error, counts and losses, and sets started_at.
// It returns "import already running" or "purge already running",
// or an empty string when the import was marked running.
// It does not return an error.
func (j *keitaroJob) beginImport() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return "import already running"
	}
	if j.busy {
		return "purge already running"
	}
	j.running = true
	j.stage = "idle"
	j.done = 0
	j.total = 0
	j.counts = map[string]int{}
	j.losses = []ktimport.Loss{}
	j.errText = ""
	j.startedAt = time.Now().UTC().Format(time.RFC3339)
	j.finishedAt = ""
	return ""
}

// beginPurge holds the lock for one synchronous purge.
// It returns "import already running" or "purge already running",
// or an empty string when this purge holds the lock.
// It does not return an error.
func (j *keitaroJob) beginPurge() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return "import already running"
	}
	if j.busy {
		return "purge already running"
	}
	j.busy = true
	return ""
}

// endPurge releases the purge lock taken by beginPurge.
// It does not return an error. Calling it without a matching begin clears busy.
func (j *keitaroJob) endPurge() {
	j.mu.Lock()
	j.busy = false
	j.mu.Unlock()
}

// progress records a tracker stage while an import is running.
// stage is snapshot, streams, conversions or clicks. done and total are the
// page or campaign counters from the client. Reports after the import
// finished are ignored. It does not return an error.
func (j *keitaroJob) progress(stage string, done, total int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running {
		return
	}
	j.stage = stage
	j.done = done
	j.total = total
}

// setStage sets the stage name without touching the counters.
// stage is catalogs, landing_files, lists, pages or groups, which the tracker client does not report.
// It does not return an error.
func (j *keitaroJob) setStage(stage string) {
	j.mu.Lock()
	j.stage = stage
	j.mu.Unlock()
}

// publish copies counts and losses into the state while the import is running.
// counts and losses are the values collected so far. key is removed from loss text.
// A call after the import finished does nothing. It does not return an error.
func (j *keitaroJob) publish(counts map[string]int, losses []ktimport.Loss, key string) {
	masked := maskLosses(losses, key)
	copied := copyCounts(counts)
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running {
		return
	}
	j.counts = copied
	j.losses = masked
}

// finish marks the import stopped and stores the outcome.
// errMsg is empty on success and is already safe to show. counts and losses
// are everything collected, including a partial run. key is removed from loss text.
// The stage becomes done or failed. running becomes false and finished_at is set.
// It does not return an error.
func (j *keitaroJob) finish(errMsg string, counts map[string]int, losses []ktimport.Loss, key string) {
	masked := maskLosses(losses, key)
	copied := copyCounts(counts)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	j.finishedAt = time.Now().UTC().Format(time.RFC3339)
	j.counts = copied
	j.losses = masked
	j.errText = errMsg
	if errMsg != "" {
		j.stage = "failed"
		return
	}
	j.stage = "done"
}

// snapshot returns a copy of the job state.
// The caller can encode it without holding the mutex.
// It does not return an error.
func (j *keitaroJob) snapshot() keitaroJobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	losses := append([]ktimport.Loss{}, j.losses...)
	if losses == nil {
		losses = []ktimport.Loss{}
	}
	return keitaroJobView{
		Running:    j.running,
		Stage:      j.stage,
		Done:       j.done,
		Total:      j.total,
		Counts:     copyCounts(j.counts),
		Losses:     losses,
		Error:      j.errText,
		StartedAt:  j.startedAt,
		FinishedAt: j.finishedAt,
	}
}

// copyCounts returns a new map with the same pairs.
// in may be nil. The result is never nil. It does not return an error.
func copyCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// handleKeitaroImport starts a background copy of the selected catalogs and logs.
// The body matches the previous import: bad JSON is 400, a click or conversion
// range that is not YYYY-MM-DD is 400, and a missing origin, key or store is 503.
// A running import or a purge in progress is 409. Success is 202 with started true.
// The work uses a two-hour background context, not the request context.
// The key is removed from the job error later. This handler does not return an error value.
func (s *Server) handleKeitaroImport(w http.ResponseWriter, r *http.Request) {
	var req keitaroImportReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.Conversions || req.Clicks {
		if _, _, err := keitaroRange(req.From, req.To); err != nil {
			writeErr(w, http.StatusBadRequest, "from and to must be YYYY-MM-DD")
			return
		}
	}
	if req.needsTracker() && !s.keitaroConfigured() {
		writeErr(w, http.StatusServiceUnavailable, "keitaro not configured")
		return
	}
	if msg := s.keitaroStoreMissing(req); msg != "" {
		writeErr(w, http.StatusServiceUnavailable, msg)
		return
	}
	if msg := s.ktJob.beginImport(); msg != "" {
		writeErr(w, http.StatusConflict, msg)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), keitaroJobTimeout)
	owner := sessionFrom(r.Context()).UID
	go func() {
		defer cancel()
		s.runKeitaroImport(ctx, req, owner)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// handleKeitaroJob returns a copy of the import state.
// The key is not part of the state. It does not call the tracker.
// A previous failure is the error field, not an HTTP error.
func (s *Server) handleKeitaroJob(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ktJob.snapshot())
}

// importState holds one Keitaro import while stages run.
// counts and losses are what finish stores. prev is the manifest loaded
// before catalogs. res is the mapper output; list and page stages update it.
// owner is the importing session UID for CreatedBy on new catalog rows.
// fail is empty until a stage stops the run, and then it is safe to show.
type importState struct {
	counts map[string]int
	losses []ktimport.Loss
	prev   ktimport.Manifest
	res    ktimport.Result
	owner  string
	fail   string
}

// runStage runs one save stage and publishes the job when that stage succeeds.
// st is the shared run. name is the stage name. key is the counts entry.
// step returns the number saved, the losses of this stage, and a save error.
// Step losses are appended before the error is checked. counts[key] is set
// only when step returns nil. A manifest save then runs on st.prev and st.res.
// It returns false when step or the manifest save fails, and st.fail is
// "save error". It returns true after the job is published.
// It does not return an error value.
func (s *Server) runStage(st *importState, name, key string, step func() (int, []ktimport.Loss, error)) bool {
	s.ktJob.setStage(name)
	n, extra, err := step()
	st.losses = append(st.losses, extra...)
	if err != nil {
		st.fail = "save error"
		return false
	}
	st.counts[key] = n
	if err := s.saveRunManifest(st.prev, st.res); err != nil {
		st.fail = "save error"
		return false
	}
	s.ktJob.publish(st.counts, st.losses, s.cfg.KeitaroKey)
	return true
}

// runKeitaroImport performs one import and stores the outcome on the job.
// ctx bounds the tracker calls. req is the body already accepted by the handler.
// On failure the stage is failed and the error is safe to show. Counts and losses
// collected before the failure stay in the state. It does not return an error.
// It logs one line with the counts and the duration. The key and URLs are not logged.
func (s *Server) runKeitaroImport(ctx context.Context, req keitaroImportReq, owner string) {
	started := time.Now()
	st := &importState{counts: map[string]int{}, losses: []ktimport.Loss{}, owner: owner}
	defer func() {
		s.logKeitaro("keitaro import", st.counts, time.Since(started))
		s.ktJob.finish(st.fail, st.counts, st.losses, s.cfg.KeitaroKey)
	}()
	var from, to string
	if req.Conversions || req.Clicks {
		var err error
		from, to, err = keitaroRange(req.From, req.To)
		if err != nil {
			st.fail = "from and to must be YYYY-MM-DD"
			return
		}
	}
	var aliases map[int]string
	if req.catalog() {
		snap, err := s.keitaroClient().FetchSnapshot(ctx)
		if err != nil {
			st.fail = sanitize(err, s.cfg.KeitaroKey)
			return
		}
		st.prev, err = s.loadManifest()
		if err != nil {
			st.fail = "save error"
			return
		}
		sel := req.selection()
		sel.Known = st.prev.Has
		st.res = ktimport.Map(snap, sel)
		st.losses = append(st.losses, st.res.Losses...)
		if req.Campaigns {
			aliases = campaignAliases(snap)
		}
		s.ktJob.setStage("catalogs")
		if err := s.importCatalogs(ctx, req, st); err != nil {
			st.fail = "save error"
			return
		}
		if err := s.saveRunManifest(st.prev, st.res); err != nil {
			st.fail = "save error"
			return
		}
		s.ktJob.publish(st.counts, st.losses, s.cfg.KeitaroKey)
		if req.Landings && !s.runStage(st, "landing_files", "landing_files", func() (int, []ktimport.Loss, error) {
			return s.importLandingFiles(ctx, st.res)
		}) {
			return
		}
		if req.Campaigns {
			ok := s.runStage(st, "lists", "lists", func() (int, []ktimport.Loss, error) {
				return s.importLists(&st.res)
			}) && s.runStage(st, "pages", "pages", func() (int, []ktimport.Loss, error) {
				return s.importPages(&st.res)
			}) && s.runStage(st, "groups", "groups", func() (int, []ktimport.Loss, error) {
				return s.importGroups(ctx, st.res.Groups, st.prev, owner)
			})
			if !ok {
				return
			}
		}
		if req.Users {
			if !s.runStage(st, "users", "users", func() (int, []ktimport.Loss, error) {
				return s.importUsers(st.res.Users)
			}) {
				return
			}
		}
	}
	if req.Conversions || req.Clicks {
		groups, err := s.listKeitaroGroups(ctx)
		if err != nil {
			st.fail = "groups unavailable"
			return
		}
		li := logImport{from: from, to: to, host: keitaroHost(s.cfg.KeitaroBase), aliases: aliases, groups: groups}
		if err := s.importLogs(ctx, req, li, st.counts, &st.losses); err != nil {
			st.fail = sanitize(err, s.cfg.KeitaroKey)
			return
		}
	}
}
