package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/emtds/bridge/ktimport"
)

// keitaroJobTimeout is the limit of one import attempt.
// The attempt that follows a timeout gets a new context of this length.
var keitaroJobTimeout = 2 * time.Hour

// keitaroDeadlinePause is the wait before the same import continues after a timeout.
// The wait keeps a tracker that is not answering out of a tight loop.
var keitaroDeadlinePause = 30 * time.Second

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
	// deadlineHold is true after a timeout left this same import running.
	deadlineHold bool
	// pickupWait is true while a stopped import that admin may continue waits for the update gate.
	pickupWait bool
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
	j.dropStreams()
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
	j.deadlineHold = false
	resumable := j.rec.Resumable
	j.mu.Unlock()
	if !resumable {
		j.dropStreams()
	}
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
// parameters_only with campaign_ids or all_imported fills empty parameter names and tokens of
// imported campaigns and does not read clicks or rewrite streams; dry_run only reports the count.
// resume true continues the last failed full import (see handleKeitaroResume).
// A running import or a purge in progress is 409. Success is 202 with started true.
// Each attempt uses a keitaroJobTimeout background context, not the request context.
// A timeout continues the same run from the journal. The key is removed from the job
// error later. This handler does not return an error value.
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
	if msg := req.forParameters(); msg != "" {
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
	req.Board = board
	if s.ktJob.continuesLast(req) {
		s.handleKeitaroResume(w, r, keitaroImportReq{Resume: true})
		return
	}
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
// Each attempt uses a keitaroJobTimeout background context, not the request context.
// A timeout continues that same run; this does not start a second job.
// It does not return an error.
func (s *Server) startKeitaroRun(r *http.Request, run keitaroResumeRun, creds ktCreds, board string, manifestStore ManifestStore) {
	owner := sessionFrom(r.Context()).UID
	go s.driveKeitaroImport(context.Background(), run, owner, creds, board, manifestStore)
}

// driveKeitaroImport runs one import, and after a timeout continues it from the journal.
// parent cancels the run when it is canceled; a timeout of parent does not continue.
// run is the work and the earlier totals. owner is the session UID for new catalog rows.
// creds is the tracker copy for every attempt. board selects the manifest.
// manifestStore receives the manifest of the run.
// A timeout or a deadline starts a new context of keitaroJobTimeout after keitaroDeadlinePause.
// A cancel, an interrupted run and any other error stop. The same job is used throughout.
// It does not return an error.
func (s *Server) driveKeitaroImport(parent context.Context, run keitaroResumeRun, owner string, creds ktCreds, board string, manifestStore ManifestStore) {
	if parent == nil {
		parent = context.Background()
	}
	for {
		ctx, cancel := context.WithTimeout(parent, keitaroJobTimeout)
		again := s.runKeitaroImportFrom(ctx, run, owner, creds, board, manifestStore)
		cancel()
		if !again || parent.Err() != nil {
			if again {
				s.ktJob.finishRunningTimeout()
			}
			return
		}
		timer := time.NewTimer(keitaroDeadlinePause)
		select {
		case <-parent.Done():
			timer.Stop()
			s.ktJob.finishRunningTimeout()
			return
		case <-timer.C:
		}
		next, ok := s.ktJob.deadlineContinuation()
		if !ok {
			s.ktJob.finishRunningTimeout()
			return
		}
		run = next
	}
}

// pickupKeitaroResume continues the import that the last process left unfinished, without a click.
// It runs when admin starts and again from the update watcher while an update gate kept it waiting.
// The journal must be resumable and stopped by a timeout, a deadline or an admin restart, with
// keitaroPickupMax as the limit of restart pickups. A cancel and any other stop stay stopped.
// The run goes on from the saved checkpoint: catalogs that passed are skipped, the campaign
// streams already read come from the streams file and clicks continue from the last written batch.
// A second call finds the job running and does nothing. It does not return an error.
func (s *Server) pickupKeitaroResume() {
	if s == nil || s.ktJob == nil {
		return
	}
	run, ok := s.ktJob.claimStartupResume()
	if !ok {
		return
	}
	creds, store := s.keitaroImportBundle(run.req.Board)
	if (run.req.needsTracker() && !creds.configured()) || s.keitaroStoreMissing(run.req) != "" {
		s.ktJob.finishRunning("keitaro not configured")
		return
	}
	go s.driveKeitaroImport(context.Background(), run, "", creds, run.req.Board, store)
}

// keitaroDeadlineErr reports whether err is a timeout or a context deadline.
// err is the tracker or context failure. A nil error and a cancel return false.
// It returns true for a deadline, a network timeout, and the same phrases in the text.
// It does not return an error.
func keitaroDeadlineErr(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return keitaroTimeoutText(err.Error())
}

// keitaroTimeoutText reports whether msg is a timeout or a context deadline.
// msg is the stored job error or the text of a tracker error.
// It returns true for a deadline, an i/o timeout, a client timeout and a TLS handshake timeout.
// A cancel, an admin restart and any other text return false. It does not return an error.
func keitaroTimeoutText(msg string) bool {
	if msg == "" || msg == keitaroInterruptedMsg {
		return false
	}
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "context cancelled") {
		return false
	}
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "Client.Timeout exceeded") ||
		strings.Contains(msg, "TLS handshake timeout")
}

// keepAfterDeadline keeps the running import going after a timeout.
// errMsg is the safe error text to store. deadline is true when the failure is a timeout
// or a context deadline even if the text does not say so.
// It returns true when the same run should continue: the job stays running, the stage stays
// the stage it stopped in, and errMsg is kept on the journal. The checkpoint is not moved.
// It returns false for a cancel, an interrupted run, a run that is not going, a run with no
// work left, and any other error. The caller then finishes the run. It does not return an error.
func (j *keitaroJob) keepAfterDeadline(errMsg string, deadline bool) bool {
	j.mu.Lock()
	if !j.running || j.rec.Interrupted || !j.rec.workLeft() {
		j.mu.Unlock()
		return false
	}
	if strings.Contains(errMsg, "context canceled") || strings.Contains(errMsg, "context cancelled") {
		j.mu.Unlock()
		return false
	}
	if !deadline && !keitaroTimeoutText(errMsg) {
		j.mu.Unlock()
		return false
	}
	if errMsg == "" {
		errMsg = "context deadline exceeded"
	}
	j.rec.Error = errMsg
	j.rec.FinishedAt = ""
	j.rec.Resumable = false
	j.rec.Resumes++
	j.deadlineHold = true
	j.mu.Unlock()
	j.save(true)
	return true
}

// deadlineContinuation reads the checkpoint of the import that keepAfterDeadline left running.
// It returns that continuation and true when the job is running, the stored error is a timeout,
// and work is left. It returns false when the run should stop. It does not return an error.
func (j *keitaroJob) deadlineContinuation() (keitaroResumeRun, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running || j.rec.Interrupted || !j.rec.workLeft() {
		return keitaroResumeRun{}, false
	}
	if !j.deadlineHold && !keitaroTimeoutText(j.rec.Error) {
		return keitaroResumeRun{}, false
	}
	j.deadlineHold = false
	return j.rec.resumeRun(), true
}

// claimStartupResume takes a stopped import that admin may continue by itself and marks it running.
// It returns the continuation and true when the journal is resumable and the stop is either a timeout
// or a deadline (not interrupted), or the cut of an admin restart (interrupted with
// keitaroInterruptedMsg) that has not used keitaroPickupMax automatic continuations yet.
// The start time and the checkpoint stay; a restart cut clears interrupted and the error, and
// counts one pickup, so a run that kills the process again and again ends up stopped.
// It returns false for a cancel, any other stop, a run already going, and while an update is
// pending; in that case waiting is set when the run is otherwise eligible, so the update watcher
// retries once the gate is released. A second call returns false. It does not return an error.
func (j *keitaroJob) claimStartupResume() (keitaroResumeRun, bool) {
	j.mu.Lock()
	if j.running || j.purging || j.syncing || j.writers > 0 {
		j.mu.Unlock()
		return keitaroResumeRun{}, false
	}
	cut := j.rec.Interrupted && j.rec.Error == keitaroInterruptedMsg && j.rec.Pickups < keitaroPickupMax
	timeout := !j.rec.Interrupted && keitaroTimeoutText(j.rec.Error)
	if !j.rec.canResume() || !(cut || timeout) {
		j.pickupWait = false
		j.mu.Unlock()
		return keitaroResumeRun{}, false
	}
	if j.updating {
		j.pickupWait = true
		j.mu.Unlock()
		return keitaroResumeRun{}, false
	}
	j.pickupWait = false
	run := j.rec.resumeRun()
	j.running = true
	if j.rec.LastStage != "" {
		j.rec.Stage = j.rec.LastStage
	}
	if cut {
		j.rec.Interrupted = false
		j.rec.Error = ""
		j.rec.Pickups++
	}
	j.rec.LastStage = ""
	j.rec.Resumable = false
	j.rec.FinishedAt = ""
	j.rec.Resumes++
	j.mu.Unlock()
	j.save(true)
	return run, true
}

// pickupWaiting reports whether an eligible stopped import is waiting for the update gate to open.
func (j *keitaroJob) pickupWaiting() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.pickupWait
}

// finishRunningTimeout stops an import that was left running but cannot continue.
// Work still left is stored as a failure with the timeout text. No work left is stored as done.
// A job that is not running is left as it is. It does not return an error.
func (j *keitaroJob) finishRunningTimeout() {
	j.finishRunning("")
}

// finishRunning stops an import that was left running but cannot continue.
// reason is the error to store when work is left; empty keeps the stored error, or the timeout
// text when there is none. No work left is stored as done. A job that is not running is left
// as it is. It does not return an error.
func (j *keitaroJob) finishRunning(reason string) {
	j.mu.Lock()
	if !j.running {
		j.mu.Unlock()
		return
	}
	errMsg := j.rec.Error
	if reason != "" {
		errMsg = reason
	}
	if !j.rec.workLeft() {
		errMsg = ""
	} else if errMsg == "" {
		errMsg = "context deadline exceeded"
	}
	counts := copyCounts(j.rec.Counts)
	losses := append([]ktimport.Loss{}, j.rec.Losses...)
	j.mu.Unlock()
	j.finish(errMsg, counts, losses, "")
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
	// deadline is true when fail is a timeout or a context deadline.
	deadline      bool
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
// ctx bounds the tracker calls. A cancel of ctx stops the run. A timeout of ctx continues
// the same run with a new context. req is the body already accepted by the handler.
// creds is the origin and key copied at the start; every stage, mask and manifest
// host of this run comes from it.
// On failure the stage is failed and the error is safe to show. Counts and losses
// collected before the failure stay in the state. It does not return an error.
// It logs one line with the counts and the duration. The key and URLs are not logged.
func (s *Server) runKeitaroImport(ctx context.Context, req keitaroImportReq, owner string, creds ktCreds, board string, manifestStore ManifestStore) {
	s.driveKeitaroImport(ctx, keitaroResumeRun{req: req}, owner, creds, board, manifestStore)
}

// runKeitaroImportFrom performs one import, or the rest of a resumed one, as runKeitaroImport.
// run.req is the work left; run.counts and run.losses are the totals of the earlier attempts and
// start the totals of this one. The catalog pass and the log marks go to the job journal as they
// happen; the click log starts at run.clicksFrom when set and counts on from run.clicksRows.
// It returns true when the attempt stopped on a timeout and the same import stays running.
// It returns false when the attempt finished or stopped for another reason.
// It does not return an error.
func (s *Server) runKeitaroImportFrom(ctx context.Context, run keitaroResumeRun, owner string, creds ktCreds, board string, manifestStore ManifestStore) (again bool) {
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
		if s.ktJob.keepAfterDeadline(st.fail, st.deadline) {
			s.ktJob.publish(st.counts, st.losses, creds.Key)
			again = true
			return
		}
		s.ktJob.finish(st.fail, st.counts, st.losses, creds.Key)
	}()
	if req.FiltersOnly {
		s.runRefilter(ctx, req, st)
		return
	}
	if req.ParametersOnly {
		s.runCampaignParams(ctx, req, st)
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
			st.noteTrackerErr(err, creds.Key)
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
			st.noteTrackerErr(err, creds.Key)
		}
	}
	return
}

// noteTrackerErr stores a tracker failure on the run.
// err is the failure. key is removed from the text. A timeout or a context deadline
// is marked so the same import can continue. It does not return an error.
func (st *importState) noteTrackerErr(err error, key string) {
	st.fail = sanitize(err, key)
	st.deadline = keitaroDeadlineErr(err)
}

// fetchImportSnapshot reads the tracker snapshot of one import run.
// creds is the copy the job took. campaignID is the Keitaro campaign of a dosync, or 0 for all.
// It returns the full snapshot, or the snapshot of that campaign and the catalogs it uses.
// The streams of a full snapshot are kept on the state volume as they are read, and an attempt
// that continues an unfinished import takes them from there instead of asking the tracker again.
// The error is a tracker failure or ktimport.ErrCampaignNotFound for an id the tracker lacks.
func (s *Server) fetchImportSnapshot(ctx context.Context, creds ktCreds, campaignID int) (ktimport.Snapshot, error) {
	client := s.keitaroClient(creds)
	if campaignID > 0 {
		return client.FetchCampaignSnapshot(ctx, campaignID)
	}
	if s.ktJob != nil {
		store := openKeitaroStreams(keitaroStreamsPath(s.ktJob.file), creds.host(), time.Now(), s.ktJob.warnf)
		defer store.close()
		client.StreamsKnown = store.known
		client.StreamsSaved = store.add
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
