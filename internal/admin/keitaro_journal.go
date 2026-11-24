package admin

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"time"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/atomicfile"
)

// keitaroJournalSaveEvery is the shortest gap between two journal writes of running progress.
// Stage changes, the finish and log completion are written at once.
const keitaroJournalSaveEvery = 5 * time.Second

// keitaroInterruptedMsg is the error of a run whose admin process stopped before it finished.
const keitaroInterruptedMsg = "interrupted: admin restarted"

// keitaroLogMark is how far one tracker log of a run got.
// Rows counts the rows read through the last written batch, over every attempt of the run.
// Through is the tracker time (UTC, "2006-01-02 15:04:05") of the last written click; a resumed
// click log continues from it. Done is true once the whole log of the window is written.
type keitaroLogMark struct {
	Rows    int    `json:"rows"`
	Through string `json:"through,omitempty"`
	Done    bool   `json:"done"`
}

// keitaroJournal is keitaro-job.json: the last manual import as it went, on the state volume.
// It holds the selection flags, the stage, the counts, the masked losses and the log marks,
// and never the tracker origin, the key or a password.
// Running is true only while a live process writes it; a file read with Running true belongs
// to a run that the process lost. LastStage is the stage a failed run stopped in.
type keitaroJournal struct {
	Stage        string                    `json:"stage"`
	LastStage    string                    `json:"last_stage,omitempty"`
	Done         int                       `json:"done"`
	Total        int                       `json:"total"`
	Counts       map[string]int            `json:"counts"`
	Losses       []ktimport.Loss           `json:"losses"`
	Error        string                    `json:"error"`
	StartedAt    string                    `json:"started_at"`
	FinishedAt   string                    `json:"finished_at"`
	UpdatedAt    string                    `json:"updated_at"`
	Running      bool                      `json:"running"`
	Interrupted  bool                      `json:"interrupted"`
	CampaignID   int                       `json:"campaign_id,omitempty"`
	Request      keitaroImportReq          `json:"request"`
	CatalogsDone bool                      `json:"catalogs_done"`
	Logs         map[string]keitaroLogMark `json:"logs"`
	Resumes      int                       `json:"resumes"`
	Resumable    bool                      `json:"resumable"`
	// Pickups counts the continuations admin made by itself after a restart cut; a click resets it.
	Pickups int `json:"pickups,omitempty"`
}

// keitaroPickupMax is how many times admin continues a restart-cut import by itself.
// A run that kills the process each time stops after this many and waits for the button.
const keitaroPickupMax = 5

// keitaroResumeRun is what a resumed import continues with.
// req holds only the work still left: catalog flags are cleared once the catalogs passed,
// and a finished log is cleared. clicksFrom is the tracker time the click log continues from,
// empty to read the window from its start. clicksRows is the click rows already counted.
// counts and losses are those of the earlier attempts.
type keitaroResumeRun struct {
	req        keitaroImportReq
	clicksFrom string
	clicksRows int
	counts     map[string]int
	losses     []ktimport.Loss
}

// idleJournal returns the journal of a job that never ran: stage idle, empty counts, losses and log marks.
func idleJournal() keitaroJournal {
	return keitaroJournal{Stage: "idle", Counts: map[string]int{}, Losses: []ktimport.Loss{}, Logs: map[string]keitaroLogMark{}}
}

// workLeft reports whether this journal still has a full import to finish.
// It is false for a one-campaign dosync, for filters_only and for parameters_only.
// It is true when the catalogs were selected and did not pass, or when the selected
// clicks or conversions are not done. It does not return an error.
func (r keitaroJournal) workLeft() bool {
	if r.Request.FiltersOnly || r.Request.ParametersOnly || r.Request.CampaignID != 0 || r.CampaignID != 0 {
		return false
	}
	if r.Request.catalog() && !r.CatalogsDone {
		return true
	}
	return (r.Request.Clicks && !r.Logs["clicks"].Done) || (r.Request.Conversions && !r.Logs["conversions"].Done)
}

// canResume reports whether a resumed run has work left for this journal.
// It is true for a failed full import (not a one-campaign dosync, not filters_only and not parameters_only)
// whose catalogs did not pass, or whose selected clicks or conversions are not done.
// It does not return an error.
func (r keitaroJournal) canResume() bool {
	return r.Stage == "failed" && r.workLeft()
}

// resumeRun builds the continuation of this journal.
// Catalog flags are dropped when the catalogs passed; a done log is dropped. The click log
// continues from its mark, and conversions are read again from the window start, because the
// tracker range of that log is not its sort order; rows written twice collapse in the warehouse.
// It does not check canResume and does not return an error.
func (r keitaroJournal) resumeRun() keitaroResumeRun {
	req := r.Request
	req.Resume = false
	if r.CatalogsDone {
		req.Domains, req.Networks, req.Offers, req.Landings = false, false, false, false
		req.Sources, req.Campaigns, req.Users = false, false, false
	}
	if r.Logs["clicks"].Done {
		req.Clicks = false
	}
	if r.Logs["conversions"].Done {
		req.Conversions = false
	}
	out := keitaroResumeRun{req: req, counts: copyCounts(r.Counts), losses: append([]ktimport.Loss{}, r.Losses...)}
	if req.Clicks {
		out.clicksFrom = r.Logs["clicks"].Through
		if out.clicksFrom != "" {
			out.clicksRows = r.Logs["clicks"].Rows
		}
	}
	return out
}

// normalize fills nil maps and slices of a journal read from disk.
func (r *keitaroJournal) normalize() {
	if r.Stage == "" {
		r.Stage = "idle"
	}
	if r.Counts == nil {
		r.Counts = map[string]int{}
	}
	if r.Losses == nil {
		r.Losses = []ktimport.Loss{}
	}
	if r.Logs == nil {
		r.Logs = map[string]keitaroLogMark{}
	}
}

// loadKeitaroJob returns a job whose journal is the file at path.
// A missing file gives an idle job, so nothing is shown before the first import.
// A journal written by a run that never finished becomes failed and interrupted, keeps its stage
// in last_stage and is written back at once. warn receives a read or parse failure; it may be nil.
// An empty path gives an idle job that writes nothing.
func loadKeitaroJob(path string, warn func(string, ...any)) *keitaroJob {
	j := newKeitaroJob()
	j.file = path
	j.warn = warn
	if path == "" {
		return j
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			j.warnf("keitaro job journal unreadable", "err", err)
		}
		return j
	}
	rec := idleJournal()
	if err := json.Unmarshal(b, &rec); err != nil {
		j.warnf("keitaro job journal unreadable", "err", err)
		return j
	}
	rec.normalize()
	lost := rec.Running
	if lost {
		rec.Running = false
		rec.Interrupted = true
		rec.LastStage = rec.Stage
		rec.Stage = "failed"
		rec.Error = keitaroInterruptedMsg
		rec.FinishedAt = rec.UpdatedAt
	}
	rec.Resumable = rec.canResume()
	j.rec = rec
	if lost {
		j.save(true)
	}
	return j
}

// warnf passes a message to the warn hook when one is set.
func (j *keitaroJob) warnf(msg string, args ...any) {
	if j.warn != nil {
		j.warn(msg, args...)
	}
}

// save writes the journal file of the job.
// force writes at once; otherwise a write happens at most every keitaroJournalSaveEvery.
// updated_at is set on every call. A job without a file only updates updated_at.
// A write failure goes to the warn hook; the import itself does not stop for it.
func (j *keitaroJob) save(force bool) {
	j.saveMu.Lock()
	defer j.saveMu.Unlock()
	j.mu.Lock()
	now := time.Now()
	j.rec.UpdatedAt = now.UTC().Format(time.RFC3339)
	if j.file == "" || (!force && now.Sub(j.savedAt) < keitaroJournalSaveEvery) {
		j.mu.Unlock()
		return
	}
	j.savedAt = now
	rec := j.rec
	rec.Running = j.running
	b, err := json.MarshalIndent(rec, "", "  ")
	j.mu.Unlock()
	if err != nil {
		j.warnf("keitaro job journal not saved", "err", err)
		return
	}
	if err := atomicfile.Write(j.file, append(b, '\n'), 0o600); err != nil {
		j.warnf("keitaro job journal not saved", "err", err)
	}
}

// setRequest records the request of the run that beginImport just marked running.
// req is the accepted body with board_id resolved; it holds no secret. The journal is written at once.
func (j *keitaroJob) setRequest(req keitaroImportReq) {
	j.mu.Lock()
	j.rec.Request = req
	j.rec.CampaignID = req.CampaignID
	j.mu.Unlock()
	j.save(true)
}

// resumeRequest returns the request a resume would run and whether the last run can be resumed.
// It only reads the journal; beginResume takes the job.
func (j *keitaroJob) resumeRequest() (keitaroImportReq, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running || !j.rec.Resumable {
		return keitaroImportReq{}, false
	}
	return j.rec.resumeRun().req, true
}

// continuesLast reports whether a new import body asks for the very import that stopped last.
// req is the accepted body with board_id resolved. It is true when the last run can be resumed,
// nothing is running and req equals the body of that run; starting it again then continues it
// and does not read the tracker from the start. Any other body, and a run that finished, start anew.
func (j *keitaroJob) continuesLast(req keitaroImportReq) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return !j.running && j.rec.Resumable && reflect.DeepEqual(j.rec.Request, req)
}

// beginResume marks the resumed run of the last failed import running.
// The counts, losses, catalog pass and log marks of the earlier attempts stay; the error is cleared.
// It returns the continuation and an empty message, or the same refusals as beginImport, or
// keitaroNothingToResumeMsg when the last run cannot be resumed.
func (j *keitaroJob) beginResume() (keitaroResumeRun, string) {
	j.mu.Lock()
	if msg := j.refuseLocked(); msg != "" {
		j.mu.Unlock()
		return keitaroResumeRun{}, msg
	}
	if !j.rec.Resumable {
		j.mu.Unlock()
		return keitaroResumeRun{}, keitaroNothingToResumeMsg
	}
	run := j.rec.resumeRun()
	j.running = true
	j.rec.Stage = "idle"
	j.rec.LastStage = ""
	j.rec.Done, j.rec.Total = 0, 0
	j.rec.Error = ""
	j.rec.Interrupted = false
	j.rec.Pickups = 0
	j.rec.Resumable = false
	j.rec.FinishedAt = ""
	j.rec.StartedAt = time.Now().UTC().Format(time.RFC3339)
	j.rec.Resumes++
	j.mu.Unlock()
	j.save(true)
	return run, ""
}

// keitaroNothingToResumeMsg is the refusal of a resume when the last run finished or was not a full import.
const keitaroNothingToResumeMsg = "nothing to resume"

// catalogsPassed records that every catalog stage of the running import is saved. The journal is written at once.
func (j *keitaroJob) catalogsPassed() {
	j.mu.Lock()
	if !j.running {
		j.mu.Unlock()
		return
	}
	j.rec.CatalogsDone = true
	j.mu.Unlock()
	j.dropStreams()
	j.save(true)
}

// markLog records how far one log of the running import got.
// stream is clicks or conversions. rows is the cumulative row count of the run. through is the
// sort time of the last written row; a zero time keeps the earlier mark. done marks the log finished
// and is written at once; other marks are written at most every keitaroJournalSaveEvery.
func (j *keitaroJob) markLog(stream string, rows int, through time.Time, done bool) {
	j.mu.Lock()
	if !j.running {
		j.mu.Unlock()
		return
	}
	m := j.rec.Logs[stream]
	m.Rows = rows
	if !through.IsZero() {
		m.Through = through.UTC().Format(keitaroTimeLayout)
	}
	m.Done = done
	j.rec.Logs[stream] = m
	j.mu.Unlock()
	j.save(done)
}

// dropResume makes the last run not resumable, after its imported data was purged. The journal is written at once.
func (j *keitaroJob) dropResume() {
	j.mu.Lock()
	changed := j.rec.Resumable
	j.rec.Resumable = false
	j.mu.Unlock()
	j.dropStreams()
	if changed {
		j.save(true)
	}
}
