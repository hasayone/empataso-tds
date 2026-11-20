// Package admin — update API handlers for GET /api/version, /api/update, /api/update/log
// and POST /api/update/apply, /api/update/rollback, /api/update/check.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
)

// updateCheckWindow is the minimum interval between /api/update/check requests.
const updateCheckWindow = 5 * time.Minute

// updateAgentMaxAge is the maximum age of agent.json for the agent to be considered fresh.
const updateAgentMaxAge = 10 * time.Minute

// updateCheckFreshAge is the maximum age of check.json for the check to be considered fresh.
const updateCheckFreshAge = 24 * time.Hour

// updateFetchFailed is the check.json error update.sh writes when git fetch of the update source fails.
const updateFetchFailed = "git fetch не удался"

// Repository states of GET /api/update (repo_state); the same codes refuse an apply while the source cannot be read.
const (
	// updateRepoPaused: no repository is set, or the product repository is not public.
	updateRepoPaused = "repo_paused"
	// updateRepoNotFound: another repository is set and git cannot read it (deleted or no longer public).
	updateRepoNotFound = "repo_not_found"
)

// productRepoRe matches the product repository in every address form update.sh accepts.
var productRepoRe = regexp.MustCompile(`(?i)^(?:https://github\.com/|ssh://git@github\.com/|git@github\.com:)hasayone/empataso-tds(?:\.git)?/?$`)

// updateHTTPClient is used by fetchEngineVersion. Timeout is 2 s; redirects are not
// followed — a 3xx response is returned as-is and treated as a bad response by the caller.
var updateHTTPClient = &http.Client{
	Timeout: 2 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// versionView is the response body of GET /api/version.
type versionView struct {
	Admin       config.BuildInfo  `json:"admin"`
	Engine      *config.BuildInfo `json:"engine"`
	EngineError string            `json:"engine_error,omitempty"`
	Cron        *cronVersionView  `json:"cron"`
	Images      *cron.AgentImages `json:"images"`
	Mismatch    bool              `json:"mismatch"`
}

// cronVersionView holds the version, commit and environment fields extracted from the raw cron status JSON.
// The view is set only when Version or Commit is non-empty in the status file; Env is empty for a cron
// that predates the field.
type cronVersionView struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Env     string `json:"env,omitempty"`
}

// handleVersion serves GET /api/version.
// Returns build info of admin, engine and cron, plus agent images and mismatch flag.
// Only administrators may call this route.
// Engine is null and engine_error is set when the engine cannot be reached or returns
// unexpected data; the error text never includes network addresses or exception text.
// Cron is null when the status file is absent or unreadable.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isAdmin(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	writeJSON(w, http.StatusOK, s.collectVersions(r.Context()))
}

// collectVersions gathers the builds of admin, engine and cron, the images and the mismatch flag.
// ctx bounds the call to the engine. The engine is nil with a short engine_error when it does not
// answer, and cron is nil without a readable status file; neither is an error for the caller.
func (s *Server) collectVersions(ctx context.Context) versionView {
	adminBuild := config.Build()
	out := versionView{Admin: adminBuild}

	engineBuild, engineErr := s.fetchEngineVersion(ctx)
	out.Engine = engineBuild
	if engineErr != "" {
		out.EngineError = engineErr
	}

	// Read cron version/commit from the status file by unmarshalling raw JSON.
	if s.cfg.CronFile != "" {
		if b, err := os.ReadFile(cron.StatusPath(s.cfg.CronFile)); err == nil {
			var cv cronVersionView
			if json.Unmarshal(b, &cv) == nil && (cv.Version != "" || cv.Commit != "") {
				out.Cron = &cv
			}
		}
	}

	// Images and agent.mismatch from agent.json.
	if s.cfg.UpdateDir != "" {
		var agent cron.AgentFile
		if cron.ReadUpdateJSON(filepath.Join(s.cfg.UpdateDir, cron.UpdateAgentFile), cron.UpdateMaxJSON, &agent) == nil {
			out.Images = &agent.Images
			if agent.Mismatch {
				out.Mismatch = true
			}
		}
	}

	// Commit mismatch: any two known non-empty commits differ.
	if !out.Mismatch {
		commits := make([]string, 0, 3)
		if adminBuild.Commit != "" {
			commits = append(commits, adminBuild.Commit)
		}
		if engineBuild != nil && engineBuild.Commit != "" {
			commits = append(commits, engineBuild.Commit)
		}
		if out.Cron != nil && out.Cron.Commit != "" {
			commits = append(commits, out.Cron.Commit)
		}
		for i := 1; i < len(commits); i++ {
			if commits[i] != commits[0] {
				out.Mismatch = true
				break
			}
		}
	}

	return out
}

// fetchEngineVersion gets the engine's build info from GET /internal/version.
// Returns (info, "") on success; (nil, brief description) on any error.
// The description never contains network addresses or exception text.
// Timeout: 2 seconds. Body limit: 4 KiB.
func (s *Server) fetchEngineVersion(ctx context.Context) (*config.BuildInfo, string) {
	base := strings.TrimRight(s.cfg.EngineURL, "/")
	if base == "" {
		return nil, ""
	}
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, base+"/internal/version", nil)
	if err != nil {
		return nil, "request error"
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return nil, "unreachable"
	}
	defer resp.Body.Close()
	var b config.BuildInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&b); err != nil {
		return nil, "bad response"
	}
	return &b, ""
}

// updateView is the response body of GET /api/update.
type updateView struct {
	Build          config.BuildInfo `json:"build"`
	Agent          *cron.AgentFile  `json:"agent"`
	AgentFresh     bool             `json:"agent_fresh"`
	Check          *cron.CheckFile  `json:"check"`
	State          *cron.StateFile  `json:"state"`
	Inbox          bool             `json:"inbox"`
	RequestPending bool             `json:"request_pending"`
	KeitaroBusy    bool             `json:"keitaro_busy"`
	// CheckStale is true when check.json was made for another build than the running one.
	CheckStale bool `json:"check_stale"`
	// RepoState is updateRepoPaused or updateRepoNotFound while the updater cannot read its source, otherwise empty.
	RepoState     string   `json:"repo_state,omitempty"`
	CanApply      bool     `json:"can_apply"`
	ApplyBlock    string   `json:"apply_block,omitempty"`
	CanRollback   bool     `json:"can_rollback"`
	RollbackBlock string   `json:"rollback_block,omitempty"`
	CLI           []string `json:"cli"`
}

// updateRepoState tells why the updater cannot read its update source.
//
// Parameters: agent is agent.json and check is check.json, each nil when absent.
// Returns updateRepoPaused when the agent has no repository, or when the fetch failed and the repository
// is the product one; updateRepoNotFound when the fetch of another repository failed; "" otherwise,
// including a check made for another build. It does not return an error.
func updateRepoState(agent *cron.AgentFile, check *cron.CheckFile) string {
	if agent == nil {
		return ""
	}
	repo := strings.TrimSpace(agent.Repo)
	if repo == "" {
		return updateRepoPaused
	}
	if check == nil || check.Error != updateFetchFailed || checkForOtherBuild(check) {
		return ""
	}
	if productRepoRe.MatchString(repo) {
		return updateRepoPaused
	}
	return updateRepoNotFound
}

// updateSnapshot holds the shared runtime state used by precondition checks
// for apply, rollback and check operations.
type updateSnapshot struct {
	// Now is the timestamp of the current request.
	Now time.Time
	// Agent is the contents of agent.json; nil when absent or unreadable.
	Agent *cron.AgentFile
	// Check is the contents of check.json; nil when absent or unreadable.
	Check *cron.CheckFile
	// State is the contents of state.json; nil when absent or unreadable.
	State *cron.StateFile
	// InboxDir is the path to the inbox directory; empty means inbox is not configured.
	InboxDir string
	// KeitaroBusy is true when keitaro is busy with a write or an import job.
	KeitaroBusy bool
	// RequestPending is true when inbox/request already exists on disk.
	RequestPending bool
}

// handleUpdate serves GET /api/update.
// Returns the full update state including check, agent and state files, computed flags
// and CLI command strings. Only administrators may call this route.
// Corrupted or symlinked files are treated as null.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isAdmin(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	now := time.Now()
	out := updateView{Build: config.Build()}

	updateDir := s.cfg.UpdateDir
	inboxDir := s.cfg.UpdateInboxDir

	out.Agent, out.Check, out.State = readUpdateFiles(updateDir)

	out.AgentFresh = out.Agent != nil && out.Agent.Fresh(now, updateAgentMaxAge)

	// Inbox is active when UpdateInboxDir is set, the directory exists and agent.Inbox is true.
	if inboxDir != "" {
		if info, err := os.Stat(inboxDir); err == nil && info.IsDir() {
			if out.Agent != nil && out.Agent.Inbox {
				out.Inbox = true
			}
		}
	}

	// Request pending: the request file exists in inbox.
	if inboxDir != "" {
		if _, err := os.Stat(filepath.Join(inboxDir, cron.UpdateRequestFile)); err == nil {
			out.RequestPending = true
		}
	}

	out.KeitaroBusy = s.ktJob.busyOrWriting()
	out.CheckStale = checkForOtherBuild(out.Check)
	out.RepoState = updateRepoState(out.Agent, out.Check)

	snap := updateSnapshot{
		Now:            now,
		Agent:          out.Agent,
		Check:          out.Check,
		State:          out.State,
		InboxDir:       inboxDir,
		KeitaroBusy:    out.KeitaroBusy,
		RequestPending: out.RequestPending,
	}

	out.ApplyBlock = s.computeApplyBlock(snap)
	out.CanApply = out.ApplyBlock == ""

	out.RollbackBlock = s.computeRollbackBlock(snap)
	out.CanRollback = out.RollbackBlock == ""

	target := ""
	unsigned := false
	if out.Check != nil {
		target = out.Check.Target.Commit
		unsigned = out.Agent != nil && out.Agent.Signing &&
			out.Check.Ahead > 0 && out.Check.Error == "" && !out.Check.Signed
	}
	out.CLI = updateCLI(target, unsigned)

	writeJSON(w, http.StatusOK, out)
}

// computeApplyBlock returns the code of the first condition that blocks an apply, or "".
// The POST-time-only checks (target_mismatch, confirm_mismatch) are not evaluated here.
func (s *Server) computeApplyBlock(snap updateSnapshot) string {
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
	if checkForOtherBuild(snap.Check) {
		return "check_stale"
	}
	if rs := updateRepoState(snap.Agent, snap.Check); rs != "" {
		return rs
	}
	if runningCommitIsTarget(snap.Check) {
		return "no_update"
	}
	if snap.Check == nil || !snap.Check.Fresh(snap.Now, updateCheckFreshAge) || snap.Check.Error != "" || snap.Check.Ahead == 0 {
		return "no_update"
	}
	if block := signatureBlock(snap); block != "" {
		return block
	}
	if len(snap.Check.ManualSteps) > 0 {
		return "manual_steps"
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

// checkForOtherBuild reports whether check.json was made for another commit than the running build.
// Such a check lists the running commit as an update, so it must not offer an apply.
//
// Parameters: check is the parsed check.json, nil when absent.
// Returns true for a non-development build whose commit differs from check.current.commit.
// It does not return an error.
func checkForOtherBuild(check *cron.CheckFile) bool {
	b := config.Build()
	return check != nil && !b.IsDev() && check.Current.Commit != b.Commit
}

// runningCommitIsTarget reports whether the check names the running build as the commit to install.
// A check written before the new build came up can still list that commit with commits ahead.
//
// Parameters: check is the parsed check.json, nil when absent.
// Returns true when the running build is not a development build and its commit is check.target.commit.
// It does not return an error.
func runningCommitIsTarget(check *cron.CheckFile) bool {
	if check == nil {
		return false
	}
	b := config.Build()
	return !b.IsDev() && check.Target.Commit == b.Commit
}

// computeRollbackBlock returns the code of the first condition that blocks a rollback, or "".
// The updater rolls back one step only: after a successful rollback the last history entry is
// the rollback itself, so state.json with action rollback and result ok yields "no_previous".
// Any later run or refusal rewrites state.json, and the updater stays the final judge then.
func (s *Server) computeRollbackBlock(snap updateSnapshot) string {
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
	if snap.KeitaroBusy || s.ktJob.updatingBool() {
		return "keitaro_running"
	}
	if snap.RequestPending {
		return "request_pending"
	}
	if snap.State != nil && snap.State.Result == cron.StateRunning && snap.State.FinishedAt == nil {
		return "running"
	}
	if snap.State != nil && snap.State.Action == "rollback" && snap.State.Result == cron.StateOK {
		return "no_previous"
	}
	return ""
}

// signatureBlock returns "unsigned" when the signature policy blocks an apply, or "".
// The policy is on when agent.json says signing is enabled (allowed_signers holds a key); the
// target must then carry a verified signature in check.json. With the policy off nothing is
// blocked here. A rollback never calls it: the updater verifies the signature of the previous version.
// snap is the shared state; a nil Agent means no policy and returns "".
func signatureBlock(snap updateSnapshot) string {
	if snap.Agent != nil && snap.Agent.Signing && (snap.Check == nil || !snap.Check.Signed) {
		return "unsigned"
	}
	return ""
}

// updateCLI returns the four CLI command strings for copying.
// target is the commit SHA from check.json; "<sha>" is used when empty. A target that is not a
// 40-hex commit never reaches a command: the result is an empty list.
// When unsigned is true (signing is enabled, check exists, ahead > 0, no error, not signed)
// the --unsigned flag is appended to the --channel and --to commands so that
// update.sh does not reject an unsigned target.
func updateCLI(target string, unsigned bool) []string {
	targetArg := target
	if targetArg == "" {
		targetArg = "<sha>"
	} else if !cron.ValidCommit(targetArg) {
		return []string{}
	}
	unsignedSuffix := ""
	if unsigned {
		unsignedSuffix = " --unsigned"
	}
	return []string{
		"sudo /usr/local/lib/emtds/update.sh --check",
		"sudo /usr/local/lib/emtds/update.sh --channel" + unsignedSuffix,
		"sudo /usr/local/lib/emtds/update.sh --to " + targetArg + unsignedSuffix,
		"sudo /usr/local/lib/emtds/update.sh --rollback",
	}
}

// handleUpdateLog serves GET /api/update/log.
// Returns the log text from log.txt and the id of the current state.
// Only administrators may call this route.
func (s *Server) handleUpdateLog(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.isAdmin(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	updateDir := s.cfg.UpdateDir
	logText := ""
	stateID := ""

	if updateDir != "" {
		if text, err := cron.ReadUpdateText(filepath.Join(updateDir, cron.UpdateLogFile), cron.UpdateMaxLog); err == nil {
			logText = text
		}
		var state cron.StateFile
		if cron.ReadUpdateJSON(filepath.Join(updateDir, cron.UpdateStateFile), cron.UpdateMaxJSON, &state) == nil {
			stateID = state.ID
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"log":      logText,
		"state_id": stateID,
	})
}

// updateNotice returns the update available notice line for the admin panel.
// Returns ("", false) when no update is available, the check is old or made for another
// build than the running one, or data is invalid.
// The line is built only from validated values.
func (s *Server) updateNotice(now time.Time) (string, bool) {
	if s.cfg.UpdateDir == "" {
		return "", false
	}
	var check cron.CheckFile
	if err := cron.ReadUpdateJSON(filepath.Join(s.cfg.UpdateDir, cron.UpdateCheckFile), cron.UpdateMaxJSON, &check); err != nil {
		return "", false
	}
	if !check.Fresh(now, updateCheckFreshAge) || check.Error != "" || check.Ahead <= 0 || checkForOtherBuild(&check) || runningCommitIsTarget(&check) {
		return "", false
	}
	if check.Validate() != nil {
		return "", false
	}
	cur := check.Current.Commit
	if len(cur) < 12 || !cron.ValidCommit(cur) {
		return "", false
	}
	cur12 := cur[:12]
	target := ""
	if cron.ValidTag(check.Target.Tag) {
		target = check.Target.Tag
	} else if cron.ValidCommit(check.Target.Commit) {
		target = check.Target.Commit[:12]
	}
	if target == "" {
		return "", false
	}
	return fmt.Sprintf("%s update available %s %s %s",
		check.At.UTC().Format(time.RFC3339), check.Channel, cur12, target), true
}

// readUpdateFiles reads agent.json, check.json and state.json from updateDir.
// Returns nil for each file that is absent, unreadable or a symlink.
// check is also nil when it fails cron.CheckFile.Validate: the volume is writable by the
// engine process, so a file with a foreign commit, tag or channel is not trusted and the
// callers see "no update" instead of a target that could reach a command line.
// When updateDir is empty all three pointers are nil.
func readUpdateFiles(updateDir string) (agent *cron.AgentFile, check *cron.CheckFile, state *cron.StateFile) {
	if updateDir == "" {
		return nil, nil, nil
	}
	agent = readUpdateFileOrNil[cron.AgentFile](filepath.Join(updateDir, cron.UpdateAgentFile), cron.UpdateMaxJSON)
	check = readUpdateFileOrNil[cron.CheckFile](filepath.Join(updateDir, cron.UpdateCheckFile), cron.UpdateMaxJSON)
	if check != nil && check.Validate() != nil {
		check = nil
	}
	state = readUpdateFileOrNil[cron.StateFile](filepath.Join(updateDir, cron.UpdateStateFile), cron.UpdateMaxJSON)
	return
}

// readUpdateFileOrNil reads and JSON-decodes the update file at path into a new T.
// Returns nil on any error including os.ErrNotExist and symlink/size errors,
// matching the brief's "повреждённый/симлинк-файл — как null".
func readUpdateFileOrNil[T any](path string, max int64) *T {
	var v T
	if err := cron.ReadUpdateJSON(path, max, &v); err != nil {
		return nil
	}
	return &v
}

// requestFileExists reports whether the request file exists in inboxDir.
// Returns false when inboxDir is empty.
func requestFileExists(inboxDir string) bool {
	if inboxDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(inboxDir, cron.UpdateRequestFile))
	return err == nil
}
