package admin

import (
	"context"
	"encoding/json"
	"errors"
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
	syncing    bool
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
	Running     bool            `json:"running"`
	SyncRunning bool            `json:"sync_running"`
	Stage       string          `json:"stage"`
	Done        int             `json:"done"`
	Total       int             `json:"total"`
	Counts      map[string]int  `json:"counts"`
	Losses      []ktimport.Loss `json:"losses"`
	Error       string          `json:"error"`
	StartedAt   string          `json:"started_at"`
	FinishedAt  string          `json:"finished_at"`
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

const keitaroSyncBusyMsg = "идёт актуализация"

// beginImport marks an import running when nothing else holds the job.
// It clears the previous error, counts and losses, and sets started_at.
// It returns "import already running" or "purge already running",
// or an empty string when the import was marked running.
// It does not return an error.
func (j *keitaroJob) beginImport() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.syncing {
		return keitaroSyncBusyMsg
	}
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
	if j.syncing {
		return keitaroSyncBusyMsg
	}
	if j.running {
		return "import already running"
	}
	if j.busy {
		return "purge already running"
	}
	j.busy = true
	return ""
}

// beginSync marks log catch-up running when manual import and purge are idle.
// It returns keitaroSyncBusyMsg when catch-up already runs, or the import or purge message.
func (j *keitaroJob) beginSync() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.syncing {
		return keitaroSyncBusyMsg
	}
	if j.running {
		return "import already running"
	}
	if j.busy {
		return "purge already running"
	}
	j.syncing = true
	return ""
}

// finishSync clears the catch-up flag taken by beginSync.
func (j *keitaroJob) finishSync() {
	j.mu.Lock()
	j.syncing = false
	j.mu.Unlock()
}

// syncActive reports whether log catch-up holds the job lock.
func (j *keitaroJob) syncActive() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.syncing
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
		Running:     j.running,
		SyncRunning: j.syncing,
		Stage:       j.stage,
		Done:        j.done,
		Total:       j.total,
		Counts:      copyCounts(j.counts),
		Losses:      losses,
		Error:       j.errText,
		StartedAt:   j.startedAt,
		FinishedAt:  j.finishedAt,
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
	board := importBoardFrom(req, r.Context())
	creds, manifestStore := s.keitaroImportBundle(board)
	if req.needsTracker() && !creds.configured() {
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
	// Settings cannot change while the job holds the lock, so this copy is the
	// one for the whole run. Settings can only be replaced, never cleared.
	ctx, cancel := context.WithTimeout(context.Background(), keitaroJobTimeout)
	owner := sessionFrom(r.Context()).UID
	go func() {
		defer cancel()
		s.runKeitaroImport(ctx, req, owner, creds, board, manifestStore)
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
// creds is the tracker origin and key copied when the job started.
type importState struct {
	counts        map[string]int
	losses        []ktimport.Loss
	prev          ktimport.Manifest
	res           ktimport.Result
	owner         string
	fail          string
	creds         ktCreds
	snapCampaigns []ktimport.Campaign
	board         string
	manifestStore ManifestStore
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
	if err := s.saveRunManifestStore(st.manifestStore, st.prev, st.res, st.creds.host()); err != nil {
		st.fail = "save error"
		return false
	}
	s.ktJob.publish(st.counts, st.losses, st.creds.Key)
	return true
}

// runKeitaroImport performs one import and stores the outcome on the job.
// ctx bounds the tracker calls. req is the body already accepted by the handler.
// creds is the origin and key copied at the start; every stage, mask and manifest
// host of this run comes from it.
// On failure the stage is failed and the error is safe to show. Counts and losses
// collected before the failure stay in the state. It does not return an error.
// It logs one line with the counts and the duration. The key and URLs are not logged.
func (s *Server) runKeitaroImport(ctx context.Context, req keitaroImportReq, owner string, creds ktCreds, board string, manifestStore ManifestStore) {
	started := time.Now()
	st := &importState{
		counts: map[string]int{}, losses: []ktimport.Loss{}, owner: owner, creds: creds,
		board: board, manifestStore: manifestStore,
	}
	defer func() {
		s.logKeitaro("keitaro import", st.counts, time.Since(started))
		s.ktJob.finish(st.fail, st.counts, st.losses, creds.Key)
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
	if req.catalog() {
		snap, err := s.keitaroClient(creds).FetchSnapshot(ctx)
		if err != nil {
			st.fail = sanitize(err, creds.Key)
			return
		}
		st.snapCampaigns = snap.Campaigns
		st.prev, err = loadManifestStore(st.manifestStore)
		if err != nil {
			st.fail = "save error"
			return
		}
		sel := req.selection()
		sel.Known = st.prev.Has
		st.res = ktimport.Map(snap, sel)
		st.losses = append(st.losses, st.res.Losses...)
		s.ktJob.setStage("catalogs")
		if err := s.importCatalogs(ctx, req, st); err != nil {
			st.fail = "save error"
			return
		}
		if err := s.saveRunManifestStore(st.manifestStore, st.prev, st.res, creds.host()); err != nil {
			st.fail = "save error"
			return
		}
		s.ktJob.publish(st.counts, st.losses, creds.Key)
		if req.Landings && !s.runStage(st, "landing_files", "landing_files", func() (int, []ktimport.Loss, error) {
			return s.importLandingFiles(ctx, creds, st.res)
		}) {
			return
		}
		if req.Campaigns {
			ok := s.runStage(st, "lists", "lists", func() (int, []ktimport.Loss, error) {
				return s.importLists(&st.res)
			}) && s.runStage(st, "pages", "pages", func() (int, []ktimport.Loss, error) {
				return s.importPages(&st.res)
			}) && s.runStage(st, "groups", "groups", func() (int, []ktimport.Loss, error) {
				return s.importGroups(ctx, st.res.Groups, st.prev, owner, &st.res.Manifest, st.creds.host(), st.board, st.manifestStore)
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
		counts, extra, _, err := s.importLogWindow(ctx, creds, from, to, logImportOpts{
			clicks: req.Clicks, conversions: req.Conversions,
			manifest: st.prev, res: st.res.Manifest, snapCampaigns: st.snapCampaigns,
			mapScope: campaignMapScope{board: st.board}, manifestStore: st.manifestStore,
		})
		for k, v := range counts {
			st.counts[k] = v
		}
		st.losses = append(st.losses, extra...)
		if err != nil {
			st.fail = sanitize(err, creds.Key)
		}
	}
}

// logImportOpts selects which logs to import and supplies manifest context.
type logImportOpts struct {
	clicks        bool
	conversions   bool
	syncMode      bool
	manifest      ktimport.Manifest
	res           ktimport.Manifest
	snapCampaigns []ktimport.Campaign
	skip          *logSkipTally
	mapScope      campaignMapScope
	manifestStore ManifestStore
}

// logFetchStats holds the latest row timestamps seen in one log fetch.
type logFetchStats struct {
	maxClick time.Time
	maxConv  time.Time
}

// importLogWindow imports clicks and/or conversions for one tracker time range.
// from and to are tracker strings "2006-01-02 15:04:05". opts selects the streams.
// It returns per-stream counts, losses, fetch stats and a tracker or warehouse error.
func (s *Server) importLogWindow(ctx context.Context, creds ktCreds, from, to string, opts logImportOpts) (map[string]int, []ktimport.Loss, logFetchStats, error) {
	counts := map[string]int{}
	var losses []ktimport.Loss
	var stats logFetchStats
	req := keitaroImportReq{Clicks: opts.clicks, Conversions: opts.conversions}
	if s.cfg.Warehouse == nil || creds.host() == "" {
		if opts.syncMode {
			return counts, losses, stats, errors.New("clickhouse unavailable")
		}
		if req.Clicks {
			counts["clicks"] = 0
			losses = append(losses, ktimport.Loss{Kind: "skipped", Ref: "clicks", Detail: "clickhouse unavailable"})
		}
		if req.Conversions {
			counts["conversions"] = 0
			losses = append(losses, ktimport.Loss{Kind: "skipped", Ref: "conversions", Detail: "clickhouse unavailable"})
		}
		return counts, losses, stats, nil
	}
	manifest := opts.manifest
	store := opts.manifestStore
	if store == nil {
		store = s.cfg.Manifest
	}
	if manifest.Empty() {
		var err error
		manifest, err = loadManifestStore(store)
		if err != nil {
			return counts, losses, stats, err
		}
	}
	manifest = manifest.Merge(opts.res)
	groups, err := s.listKeitaroGroups(ctx)
	if err != nil {
		return counts, losses, stats, err
	}
	resolved, backfill, err := s.resolveLogCampaignMap(ctx, creds, manifest, opts.snapCampaigns, groups, opts.mapScope)
	if err != nil {
		return counts, losses, stats, err
	}
	if err := s.saveCampaignMapBackfill(store, manifest, backfill, creds.host()); err != nil {
		return counts, losses, stats, err
	}
	li := logImport{from: from, to: to, host: creds.host(), creds: creds, aliases: resolved, groups: groups}
	stats, err = s.importLogs(ctx, req, li, counts, &losses, opts)
	if err != nil {
		return counts, losses, stats, err
	}
	return counts, losses, stats, nil
}
