package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/cron"
)

// reUpdateUser validates a user login for inclusion in a request file.
var reUpdateUser = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// errRequestPending is returned by writeUpdateRequest when the destination file already
// exists or is a symlink/directory, mapping to HTTP 409 "request_pending".
var errRequestPending = errors.New("request_pending")

// applyRequest is the JSON body of POST /api/update/apply.
type applyRequest struct {
	Target  string `json:"target"`
	Confirm string `json:"confirm"`
}

// rollbackRequest is the JSON body of POST /api/update/rollback.
type rollbackRequest struct {
	Confirm string `json:"confirm"`
}

// handleUpdateApply handles POST /api/update/apply.
// Validates all preconditions, then atomically writes a request file to the inbox.
// Returns 202 {id} on success; 409 {error} with the failing condition code on conflict.
// Requires a cookie session and administrator privileges; API keys are rejected.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isAdmin(sess) || sess.API {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	var req applyRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}

	now := time.Now()
	updateDir := s.cfg.UpdateDir
	inboxDir := s.cfg.UpdateInboxDir

	agent, check, state := readUpdateFiles(updateDir)
	keitaroBusy := s.ktJob.busyOrWriting()
	user := cleanUpdateUser(sess.User)

	s.updateWriteMu.Lock()
	snap := updateSnapshot{
		Now:            now,
		Agent:          agent,
		Check:          check,
		State:          state,
		InboxDir:       inboxDir,
		KeitaroBusy:    keitaroBusy,
		RequestPending: requestFileExists(inboxDir),
	}
	applyCode := s.applyPreconditions(snap, req.Target, req.Confirm)
	if applyCode != "" {
		s.updateWriteMu.Unlock()
		writeErr(w, http.StatusConflict, applyCode)
		return
	}
	id, genErr := newUpdateID()
	if genErr != nil {
		s.updateWriteMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	updateReq := cron.UpdateRequest{
		Action: "apply",
		Target: req.Target,
		ID:     id,
		User:   user,
		At:     now.Unix(),
	}
	reqWriteErr := writeUpdateRequest(inboxDir, updateReq)
	s.updateWriteMu.Unlock()

	if reqWriteErr != nil {
		if errors.Is(reqWriteErr, errRequestPending) {
			writeErr(w, http.StatusConflict, "request_pending")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	s.recordAudit(now, auditEntry{
		TS:       now.UTC().Format(time.RFC3339Nano),
		Method:   http.MethodPost,
		Route:    "POST /api/update/apply",
		Status:   http.StatusAccepted,
		MS:       time.Since(now).Milliseconds(),
		IP:       s.clientIP(r),
		UID:      sessionUID(sess),
		Source:   "update",
		ChangeID: req.Target,
	})

	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// applyPreconditions checks all preconditions for POST /api/update/apply in order.
// Returns the code of the first failing check, or "" on success.
// target and confirm are the request body values matched against check.json.
func (s *Server) applyPreconditions(snap updateSnapshot, target, confirm string) string {
	if snap.InboxDir == "" {
		return "inbox_absent"
	}
	if _, err := os.Stat(snap.InboxDir); err != nil {
		return "inbox_absent"
	}
	if config.Build().IsDev() {
		return "dev_build"
	}
	if snap.Agent == nil || !snap.Agent.Fresh(snap.Now, updateAgentMaxAge) || !snap.Agent.Inbox {
		return "updater_absent"
	}
	if !snap.Agent.Signing {
		return "unsigned"
	}
	if snap.Check == nil || !snap.Check.Fresh(snap.Now, updateCheckFreshAge) || snap.Check.Error != "" || snap.Check.Ahead == 0 {
		return "no_update"
	}
	if target != snap.Check.Target.Commit {
		return "target_mismatch"
	}
	if !snap.Check.Signed {
		return "unsigned"
	}
	if len(snap.Check.ManualSteps) > 0 {
		return "manual_steps"
	}
	if len(target) >= 7 && confirm != target[:7] {
		return "confirm_mismatch"
	}
	if snap.KeitaroBusy || s.ktJob.updatingBool() {
		return "keitaro_running"
	}
	if snap.RequestPending {
		return "request_pending"
	}
	if snap.State != nil && snap.State.Result == cron.StateRunning && snap.State.FinishedAt == nil {
		return "running"
	}
	return ""
}

// handleUpdateRollback handles POST /api/update/rollback.
// Requires confirm="rollback". Validates preconditions and writes a rollback request to the inbox.
// Returns 202 {id} on success; 409 {error} on conflict.
func (s *Server) handleUpdateRollback(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isAdmin(sess) || sess.API {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	var req rollbackRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.Confirm != "rollback" {
		writeErr(w, http.StatusBadRequest, "confirm must be rollback")
		return
	}

	now := time.Now()
	updateDir := s.cfg.UpdateDir
	inboxDir := s.cfg.UpdateInboxDir

	agent, _, state := readUpdateFiles(updateDir)
	keitaroBusy := s.ktJob.busyOrWriting()
	user := cleanUpdateUser(sess.User)

	s.updateWriteMu.Lock()
	snap := updateSnapshot{
		Now:            now,
		Agent:          agent,
		State:          state,
		InboxDir:       inboxDir,
		KeitaroBusy:    keitaroBusy,
		RequestPending: requestFileExists(inboxDir),
	}
	rollCode := s.computeRollbackBlock(snap)
	if rollCode != "" {
		s.updateWriteMu.Unlock()
		writeErr(w, http.StatusConflict, rollCode)
		return
	}
	id, genErr := newUpdateID()
	if genErr != nil {
		s.updateWriteMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	updateReq := cron.UpdateRequest{
		Action: "rollback",
		Target: "",
		ID:     id,
		User:   user,
		At:     now.Unix(),
	}
	reqWriteErr := writeUpdateRequest(inboxDir, updateReq)
	s.updateWriteMu.Unlock()

	if reqWriteErr != nil {
		if errors.Is(reqWriteErr, errRequestPending) {
			writeErr(w, http.StatusConflict, "request_pending")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	s.recordAudit(now, auditEntry{
		TS:     now.UTC().Format(time.RFC3339Nano),
		Method: http.MethodPost,
		Route:  "POST /api/update/rollback",
		Status: http.StatusAccepted,
		MS:     time.Since(now).Milliseconds(),
		IP:     s.clientIP(r),
		UID:    sessionUID(sess),
		Source: "update",
	})

	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// handleUpdateCheck handles POST /api/update/check.
// Rate-limited to once per 5 minutes (in-memory and check.At). No signing required.
// Returns 202 {id} on success; 429 on rate limit; 409 on other conflicts.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isAdmin(sess) || sess.API {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	// Accept an empty body or a JSON object with no unknown fields.
	if r.ContentLength > 0 {
		dec := json.NewDecoder(io.LimitReader(r.Body, 1024))
		dec.DisallowUnknownFields()
		var empty struct{}
		if err := dec.Decode(&empty); err != nil {
			writeErr(w, http.StatusBadRequest, "bad json")
			return
		}
	}

	now := time.Now()
	updateDir := s.cfg.UpdateDir
	inboxDir := s.cfg.UpdateInboxDir

	agent, check, state := readUpdateFiles(updateDir)
	user := cleanUpdateUser(sess.User)

	s.updateWriteMu.Lock()
	snap := updateSnapshot{
		Now:            now,
		Agent:          agent,
		Check:          check,
		State:          state,
		InboxDir:       inboxDir,
		RequestPending: requestFileExists(inboxDir),
	}
	checkCode := s.checkPreconditions(snap)
	if checkCode != "" {
		s.updateWriteMu.Unlock()
		if checkCode == "rate_limited" {
			writeErr(w, http.StatusTooManyRequests, checkCode)
			return
		}
		writeErr(w, http.StatusConflict, checkCode)
		return
	}
	// Mark in-memory rate limit before writing the request.
	s.updateCheckMu.Lock()
	s.updateCheckAt = now
	s.updateCheckMu.Unlock()
	id, genErr := newUpdateID()
	if genErr != nil {
		s.updateWriteMu.Unlock()
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	updateReq := cron.UpdateRequest{
		Action: "check",
		Target: "",
		ID:     id,
		User:   user,
		At:     now.Unix(),
	}
	reqWriteErr := writeUpdateRequest(inboxDir, updateReq)
	s.updateWriteMu.Unlock()

	if reqWriteErr != nil {
		if errors.Is(reqWriteErr, errRequestPending) {
			writeErr(w, http.StatusConflict, "request_pending")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	s.recordAudit(now, auditEntry{
		TS:     now.UTC().Format(time.RFC3339Nano),
		Method: http.MethodPost,
		Route:  "POST /api/update/check",
		Status: http.StatusAccepted,
		MS:     time.Since(now).Milliseconds(),
		IP:     s.clientIP(r),
		UID:    sessionUID(sess),
		Source: "update",
	})

	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// checkPreconditions checks preconditions for POST /api/update/check in order.
// Returns the code of the first failing check, or "" on success.
// rate_limited is returned (with 429 HTTP status) when too recent.
func (s *Server) checkPreconditions(snap updateSnapshot) string {
	if snap.InboxDir == "" {
		return "inbox_absent"
	}
	if _, err := os.Stat(snap.InboxDir); err != nil {
		return "inbox_absent"
	}
	if snap.Agent == nil || !snap.Agent.Fresh(snap.Now, updateAgentMaxAge) || !snap.Agent.Inbox {
		return "updater_absent"
	}
	if snap.RequestPending {
		return "request_pending"
	}
	if snap.State != nil && snap.State.Result == cron.StateRunning && snap.State.FinishedAt == nil {
		return "running"
	}
	// Rate limit: in-memory.
	s.updateCheckMu.Lock()
	lastCheck := s.updateCheckAt
	s.updateCheckMu.Unlock()
	if !lastCheck.IsZero() && snap.Now.Sub(lastCheck) < updateCheckWindow {
		return "rate_limited"
	}
	// Rate limit: check.At from the last run.
	if snap.Check != nil && !snap.Check.At.IsZero() && snap.Now.Sub(snap.Check.At) < updateCheckWindow {
		return "rate_limited"
	}
	return ""
}

// newUpdateID generates a 16 lowercase hex character run identifier using crypto/rand.
// Returns the id and an error when the random source fails.
func newUpdateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// cleanUpdateUser returns login if it matches the valid user pattern, otherwise "admin".
func cleanUpdateUser(login string) string {
	if reUpdateUser.MatchString(login) {
		return login
	}
	return "admin"
}

// writeUpdateRequest formats req and publishes it to inboxDir/request via a hard link.
// A temporary file named .request.<rand>.tmp is created with O_EXCL and mode 0600.
// os.Link(tmp, dest) is used instead of os.Rename so that a concurrent writer whose
// file already occupies dest causes EEXIST, returned as errRequestPending.
// If dest is a symlink or directory it is likewise treated as errRequestPending.
// The temporary file is removed after the link attempt regardless of outcome.
func writeUpdateRequest(inboxDir string, req cron.UpdateRequest) error {
	data, err := cron.FormatUpdateRequest(req)
	if err != nil {
		return err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	tmpPath := filepath.Join(inboxDir, ".request."+hex.EncodeToString(b)+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, werr := f.Write(data); werr != nil {
		f.Close()
		os.Remove(tmpPath)
		return werr
	}
	if cerr := f.Close(); cerr != nil {
		os.Remove(tmpPath)
		return cerr
	}
	dest := filepath.Join(inboxDir, cron.UpdateRequestFile)
	// Reject dest when it is a symlink or directory; os.Link would return EPERM for a
	// directory and EEXIST for a symlink, but we handle both uniformly here.
	if fi, lerr := os.Lstat(dest); lerr == nil {
		if fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() {
			os.Remove(tmpPath)
			return errRequestPending
		}
	}
	linkErr := os.Link(tmpPath, dest)
	os.Remove(tmpPath)
	if linkErr != nil {
		if errors.Is(linkErr, os.ErrExist) {
			return errRequestPending
		}
		return linkErr
	}
	return nil
}
