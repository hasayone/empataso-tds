package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/emtds/bridge/ktimport"
)

const keitaroJobTimeout = 2 * time.Hour

// keitaroJob is one background import and the lock that keeps a purge from
// overlapping it. The mutex guards every field below it except saveMu, file and warn.
type keitaroJob struct {
	mu      sync.Mutex
	running bool
	syncing bool
	// rec is the state of the running or the last import; save writes it to file.
	rec     keitaroJournal
	purging bool
	// writers counts holders of holdWrite: catalog or campaign writes in progress.
	writers int
	// updating is true while an update/pending file is active; begin* and holdWrite refuse when set.
	updating bool
	// savedAt is the time of the last journal write.
	savedAt time.Time
	// saveMu orders journal writes, so a later state is never overwritten by an earlier one.
	saveMu sync.Mutex
	// file is keitaro-job.json on the state volume; empty keeps the journal in memory only.
	file string
	// warn receives journal read and write failures; nil drops them.
	warn func(string, ...any)
}

// keitaroUpdatePendingMsg is the refusal of begin* and holdWrite while an update is pending.
var keitaroUpdatePendingMsg = errUpdatePending.msg

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
	// CampaignID is the Keitaro campaign of the last dosync of one campaign; omitted for a full import.
	CampaignID int `json:"campaign_id,omitempty"`
	// LastStage is the stage a failed run stopped in; empty otherwise.
	LastStage string `json:"last_stage,omitempty"`
	// UpdatedAt is the time of the last change of the state, RFC3339, or empty before the first run.
	UpdatedAt string `json:"updated_at"`
	// Interrupted is true when the admin process stopped while the run was going.
	Interrupted bool `json:"interrupted"`
	// Resumable is true when POST /api/keitaro/import with resume true can continue the last run.
	Resumable bool `json:"resumable"`
	// Resumes counts how many times the run was continued.
	Resumes int `json:"resumes"`
	// Logs holds per log (clicks, conversions) the rows written so far and whether the log is done.
	Logs map[string]keitaroLogMark `json:"logs"`
}

// newKeitaroJob returns an idle job that keeps its journal in memory only.
// The stage is idle. Counts is an empty map and losses is an empty slice.
// It does not return an error.
func newKeitaroJob() *keitaroJob {
	return &keitaroJob{rec: idleJournal()}
}

const keitaroSyncBusyMsg = "идёт актуализация"

// keitaroWriteBusyMsg is the refusal of beginImport, beginPurge and beginSync while a write
// holds the gate. It carries the keitaro_running code, which errCode reads from the message.
var keitaroWriteBusyMsg = errKeitaroWriting.msg

// setUpdating sets the update gate flag.
// When v is true, all new begin* and holdWrite calls are refused with update_pending.
// It does not return an error.
func (j *keitaroJob) setUpdating(v bool) {
	j.mu.Lock()
	j.updating = v
	j.mu.Unlock()
}

// updatingBool returns the current value of the update gate flag.
func (j *keitaroJob) updatingBool() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.updating
}

// busyOrWriting reports whether an import, purge, sync or a write hold is active.
// It does not include the update gate.
func (j *keitaroJob) busyOrWriting() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running || j.purging || j.syncing || j.writers > 0
}

// idleForUpdate reports whether the job is completely idle (no import, purge, sync or writers).
// It does not check the update gate itself.
func (j *keitaroJob) idleForUpdate() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return !j.running && !j.purging && !j.syncing && j.writers == 0
}

// withWrite runs fn while no import, purge or sync can start: the one entry of the write gate.
// It returns errKeitaroRunning (409 keitaro_running) without calling fn when an import,
// purge or sync is running, otherwise the error of fn. The hold is dropped when fn returns
// or panics. Nothing else calls holdWrite or releaseWrite, except tests.
func (j *keitaroJob) withWrite(fn func() error) error {
	if err := j.holdWrite(); err != nil {
		return err
	}
	defer j.releaseWrite()
	return fn()
}

// holdWrite takes a write hold: while it is held no import, purge or sync can start.
// It returns errUpdatePending when an update is pending, errKeitaroRunning when an import,
// purge or sync is already running, and nil after the hold is counted.
// Every successful call needs one releaseWrite; use withWrite.
func (j *keitaroJob) holdWrite() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.updating {
		return errUpdatePending
	}
	if j.running || j.purging || j.syncing {
		return errKeitaroRunning
	}
	j.writers++
	return nil
}

// releaseWrite drops one hold taken by holdWrite. A call without a hold changes nothing.
func (j *keitaroJob) releaseWrite() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.writers > 0 {
		j.writers--
	}
}

// busy reports whether an import, purge or sync is running.
// A write hold does not count.
func (j *keitaroJob) busy() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running || j.purging || j.syncing
}

// beginImport marks an import running when nothing else holds the job.
// It clears the previous error, counts and losses, and sets started_at.
// It returns keitaroUpdatePendingMsg (code update_pending) while an update is pending,
// keitaroSyncBusyMsg, "import already running", "purge already running",
// keitaroWriteBusyMsg (code keitaro_running) while a withWrite is active,
// or an empty string when the import was marked running.
// It does not return an error.
func (j *keitaroJob) beginImport() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if msg := j.refuseLocked(); msg != "" {
		return msg
	}
	j.running = true
	j.rec = idleJournal()
	j.rec.StartedAt = time.Now().UTC().Format(time.RFC3339)
	return ""
}

// refuseLocked returns why an import cannot start now, or an empty string. j.mu is held.
// The order of the checks is update, catch-up, import, purge, then a write hold.
func (j *keitaroJob) refuseLocked() string {
	if j.updating {
		return keitaroUpdatePendingMsg
	}
	if j.syncing {
		return keitaroSyncBusyMsg
	}
	if j.running {
		return "import already running"
	}
	if j.purging {
		return "purge already running"
	}
	if j.writers > 0 {
		return keitaroWriteBusyMsg
	}
	return ""
}

// beginPurge holds the lock for one synchronous purge.
// It returns keitaroUpdatePendingMsg (code update_pending) while an update is pending,
// keitaroSyncBusyMsg, "import already running", "purge already running",
// keitaroWriteBusyMsg (code keitaro_running) while a withWrite is active,
// or an empty string when this purge holds the lock.
// It does not return an error.
func (j *keitaroJob) beginPurge() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.updating {
		return keitaroUpdatePendingMsg
	}
	if j.syncing {
		return keitaroSyncBusyMsg
	}
	if j.running {
		return "import already running"
	}
	if j.purging {
		return "purge already running"
	}
	if j.writers > 0 {
		return keitaroWriteBusyMsg
	}
	j.purging = true
	return ""
}

// beginSync marks log catch-up running when manual import and purge are idle.
// It returns keitaroUpdatePendingMsg (code update_pending) while an update is pending,
// keitaroSyncBusyMsg when catch-up already runs, the import or purge message,
// or keitaroWriteBusyMsg (code keitaro_running) while a withWrite is active.
func (j *keitaroJob) beginSync() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.updating {
		return keitaroUpdatePendingMsg
	}
	if j.syncing {
		return keitaroSyncBusyMsg
	}
	if j.running {
		return "import already running"
	}
	if j.purging {
		return "purge already running"
	}
	if j.writers > 0 {
		return keitaroWriteBusyMsg
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
// It does not return an error. Calling it without a matching begin clears the purge flag.
func (j *keitaroJob) endPurge() {
	j.mu.Lock()
	j.purging = false
	j.mu.Unlock()
}

// progress records a tracker stage while an import is running.
// stage is snapshot, streams, conversions or clicks. done and total are the
// page or campaign counters from the client. Reports after the import
// finished are ignored. It does not return an error.
func (j *keitaroJob) progress(stage string, done, total int) {
	j.mu.Lock()
	if !j.running {
		j.mu.Unlock()
		return
	}
	moved := j.rec.Stage != stage
	j.rec.Stage = stage
	j.rec.Done = done
	j.rec.Total = total
	j.mu.Unlock()
	j.save(moved)
}

// setStage sets the stage name without touching the counters.
// stage is catalogs, landing_files, lists, pages or groups, which the tracker client does not report.
// The journal is written at once. It does not return an error.
func (j *keitaroJob) setStage(stage string) {
	j.mu.Lock()
	j.rec.Stage = stage
	j.mu.Unlock()
	j.save(true)
}

// publish copies counts and losses into the state while the import is running.
// counts and losses are the values collected so far. key is removed from loss text.
// A call after the import finished does nothing. It does not return an error.
func (j *keitaroJob) publish(counts map[string]int, losses []ktimport.Loss, key string) {
	masked := maskLosses(losses, key)
	copied := copyCounts(counts)
	j.mu.Lock()
	if !j.running {
		j.mu.Unlock()
		return
	}
	j.rec.Counts = copied
	j.rec.Losses = masked
	j.mu.Unlock()
	j.save(true)
}

// finish marks the import stopped and stores the outcome.
// errMsg is empty on success and is already safe to show. counts and losses
// are everything collected, including a partial run. key is removed from loss text.
// The stage becomes done or failed; a failed run keeps the stage it stopped in as last_stage
// and is resumable when canResume allows it. running becomes false, finished_at is set and
// the journal is written at once. It does not return an error.
func (j *keitaroJob) finish(errMsg string, counts map[string]int, losses []ktimport.Loss, key string) {
	masked := maskLosses(losses, key)
	copied := copyCounts(counts)
	j.mu.Lock()
	j.running = false
	j.rec.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	j.rec.Counts = copied
	j.rec.Losses = masked
	j.rec.Error = errMsg
	j.rec.LastStage = ""
	if errMsg != "" {
		j.rec.LastStage = j.rec.Stage
		j.rec.Stage = "failed"
	} else {
		j.rec.Stage = "done"
	}
	j.rec.Resumable = j.rec.canResume()
	j.mu.Unlock()
	j.save(true)
}

// snapshot returns a copy of the job state.
// The caller can encode it without holding the mutex.
// It does not return an error.
func (j *keitaroJob) snapshot() keitaroJobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	losses := append([]ktimport.Loss{}, j.rec.Losses...)
	logs := make(map[string]keitaroLogMark, len(j.rec.Logs))
	for k, v := range j.rec.Logs {
		logs[k] = v
	}
	return keitaroJobView{
		Running:     j.running,
		SyncRunning: j.syncing,
		Stage:       j.rec.Stage,
		Done:        j.rec.Done,
		Total:       j.rec.Total,
		Counts:      copyCounts(j.rec.Counts),
		Losses:      losses,
		Error:       j.rec.Error,
		StartedAt:   j.rec.StartedAt,
		FinishedAt:  j.rec.FinishedAt,
		CampaignID:  j.rec.CampaignID,
		LastStage:   j.rec.LastStage,
		UpdatedAt:   j.rec.UpdatedAt,
		Interrupted: j.rec.Interrupted,
		Resumable:   j.rec.Resumable && !j.running,
		Resumes:     j.rec.Resumes,
		Logs:        logs,
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
// A positive campaign_id runs the dosync of that one Keitaro campaign through the same import;
// a negative id or campaign_id with users, clicks or conversions is 400.
// filters_only with campaign_ids or all_imported rewrites only imported stream filters, and with
// dry_run only reports them; a malformed filters_only body is 400.
// resume true continues the last failed full import (see handleKeitaroResume).
// A running import or a purge in progress is 409. Success is 202 with started true.
// The work uses a two-hour background context, not the request context.
// The key is removed from the job error later. This handler does not return an error value.
func (s *Server) handleKeitaroImport(w http.ResponseWriter, r *http.Request) {
	var req keitaroImportReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.Resume {
		s.handleKeitaroResume(w, r, req)
		return
	}
	if msg := req.forFilters(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if msg := req.forCampaign(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
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
	req.Board = board
	s.ktJob.setRequest(req)
	s.startKeitaroRun(r, keitaroResumeRun{req: req}, creds, board, manifestStore)
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// handleKeitaroResume continues the last failed full import from the journal.
// body is the decoded request; any field besides resume is 400. Catalogs that already passed are
// not imported again, the click log continues from its last written batch and conversions are
// read again; nothing imported before is removed. A run that cannot be resumed is 409 with
// "nothing to resume"; a running import, purge, catch-up or update is 409 as for a new import;
// a missing origin, key or store is 503. Success is 202 with started true.
func (s *Server) handleKeitaroResume(w http.ResponseWriter, r *http.Request, body keitaroImportReq) {
	if !reflect.DeepEqual(body, keitaroImportReq{Resume: true}) {
		writeErr(w, http.StatusBadRequest, "resume does not combine with other fields")
		return
	}
	next, ok := s.ktJob.resumeRequest()
	if !ok {
		if msg := s.ktJob.beginImportRefusal(); msg != "" {
			writeErr(w, http.StatusConflict, msg)
			return
		}
		writeErr(w, http.StatusConflict, keitaroNothingToResumeMsg)
		return
	}
	board := next.Board
	creds, manifestStore := s.keitaroImportBundle(board)
	if next.needsTracker() && !creds.configured() {
		writeErr(w, http.StatusServiceUnavailable, "keitaro not configured")
		return
	}
	if msg := s.keitaroStoreMissing(next); msg != "" {
		writeErr(w, http.StatusServiceUnavailable, msg)
		return
	}
	run, msg := s.ktJob.beginResume()
	if msg != "" {
		writeErr(w, http.StatusConflict, msg)
		return
	}
	s.startKeitaroRun(r, run, creds, board, manifestStore)
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// beginImportRefusal returns why an import cannot start now, or an empty string. It takes nothing.
func (j *keitaroJob) beginImportRefusal() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.refuseLocked()
}

// startKeitaroRun runs one import or resumed import in the background.
// r supplies the session that owns new catalog rows. run is the work and the earlier totals.
// Settings cannot change while the job holds the lock, so creds is the copy for the whole run.
// The work uses a keitaroJobTimeout background context, not the request context.
func (s *Server) startKeitaroRun(r *http.Request, run keitaroResumeRun, creds ktCreds, board string, manifestStore ManifestStore) {
	ctx, cancel := context.WithTimeout(context.Background(), keitaroJobTimeout)
	owner := sessionFrom(r.Context()).UID
	go func() {
		defer cancel()
		s.runKeitaroImportFrom(ctx, run, owner, creds, board, manifestStore)
	}()
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
	s.runKeitaroImportFrom(ctx, keitaroResumeRun{req: req}, owner, creds, board, manifestStore)
}

// runKeitaroImportFrom performs one import, or the rest of a resumed one, as runKeitaroImport.
// run.req is the work left; run.counts and run.losses are the totals of the earlier attempts and
// start the totals of this one. The catalog pass and the log marks go to the job journal as they
// happen; the click log starts at run.clicksFrom when set and counts on from run.clicksRows.
func (s *Server) runKeitaroImportFrom(ctx context.Context, run keitaroResumeRun, owner string, creds ktCreds, board string, manifestStore ManifestStore) {
	req := run.req
	started := time.Now()
	counts := copyCounts(run.counts)
	losses := append([]ktimport.Loss{}, run.losses...)
	st := &importState{
		counts: counts, losses: losses, owner: owner, creds: creds,
		board: board, manifestStore: manifestStore,
	}
	defer func() {
		s.logKeitaro("keitaro import", st.counts, time.Since(started))
		s.ktJob.finish(st.fail, st.counts, st.losses, creds.Key)
	}()
	if req.FiltersOnly {
		s.runRefilter(ctx, req, st)
		return
	}
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
		snap, err := s.fetchImportSnapshot(ctx, creds, req.CampaignID)
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
	s.ktJob.catalogsPassed()
	if req.Conversions || req.Clicks {
		base := map[string]int{"clicks": run.clicksRows}
		counts, extra, _, err := s.importLogWindow(ctx, creds, from, to, logImportOpts{
			clicks: req.Clicks, conversions: req.Conversions,
			manifest: st.prev, res: st.res.Manifest, snapCampaigns: st.snapCampaigns,
			mapScope: campaignMapScope{board: st.board}, manifestStore: st.manifestStore,
			clicksFrom: run.clicksFrom,
			mark: func(stream string, rows int, through time.Time, done bool) {
				s.ktJob.markLog(stream, base[stream]+rows, through, done)
			},
		})
		for k, v := range counts {
			st.counts[k] = v
		}
		if n, ok := counts["clicks"]; ok {
			st.counts["clicks"] = base["clicks"] + n
		}
		st.losses = append(st.losses, extra...)
		if err != nil {
			st.fail = sanitize(err, creds.Key)
		}
	}
}

// fetchImportSnapshot reads the tracker snapshot of one import run.
// creds is the copy the job took. campaignID is the Keitaro campaign of a dosync, or 0 for all.
// It returns the full snapshot, or the snapshot of that campaign and the catalogs it uses.
// The error is a tracker failure or ktimport.ErrCampaignNotFound for an id the tracker lacks.
func (s *Server) fetchImportSnapshot(ctx context.Context, creds ktCreds, campaignID int) (ktimport.Snapshot, error) {
	client := s.keitaroClient(creds)
	if campaignID > 0 {
		return client.FetchCampaignSnapshot(ctx, campaignID)
	}
	return client.FetchSnapshot(ctx)
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
	// clicksFrom is the tracker time a resumed click log starts at; empty starts at the window.
	clicksFrom string
	// mark receives the progress of each log of a manual import: stream is clicks or conversions,
	// rows the rows read through the last written batch, through the sort time of that row, and
	// done true once the log is written. nil reports nothing.
	mark func(stream string, rows int, through time.Time, done bool)
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
