// Package cron is documented in config.go.
// This file defines the shared contract for all files in the update directory:
// constants, structures, readers, writers, validators and the request format.
package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/atomicfile"
)

// File names inside the update directory.
const (
	UpdateAgentFile     = "agent.json"
	UpdateCheckFile     = "check.json"
	UpdateStateFile     = "state.json"
	UpdateLogFile       = "log.txt"
	UpdatePendingFile   = "pending"
	UpdateReadyFile     = "ready.json"
	UpdateAnnouncedFile = "announced.json"
	UpdateRequestFile   = "request"
)

// Maximum file sizes accepted by Read* functions.
const (
	UpdateMaxJSON    int64 = 512 * 1024 // 512 KiB — agent/check/state/ready/announced
	UpdateMaxLog     int64 = 256 * 1024 // 256 KiB — log.txt
	UpdateMaxRequest int64 = 1024       // 1 KiB   — request
	UpdateMaxPending int64 = 64         // 64 B    — pending
)

// Result values for StateFile.Result.
const (
	StateRunning    = "running"
	StateOK         = "ok"
	StateRolledBack = "rolled_back"
	StateFailed     = "failed"
	StateRefused    = "refused"
)

// UpdateDir returns the path of the update directory for a given groups file.
// The update directory lives next to groups.json inside the same volume directory.
//
// groupsFile is the absolute path to the cron groups configuration file.
// Returns the absolute path to the update sub-directory.
func UpdateDir(groupsFile string) string {
	return filepath.Join(filepath.Dir(groupsFile), "update")
}

// AgentImages holds Docker image IDs written by the updater for each service.
type AgentImages struct {
	Local  string `json:"local"`
	Engine string `json:"engine"`
	Admin  string `json:"admin"`
	Cron   string `json:"cron"`
}

// AgentFile is the heartbeat written by the updater every five minutes.
// Schema must equal 1.
type AgentFile struct {
	Schema        int         `json:"schema"`
	At            time.Time   `json:"at"`
	ScriptVersion string      `json:"script_version"`
	Repo          string      `json:"repo"`
	Channel       string      `json:"channel"`
	Signing       bool        `json:"signing"`
	DockerOK      bool        `json:"docker_ok"`
	Inbox         bool        `json:"inbox"`
	Images        AgentImages `json:"images"`
	Mismatch      bool        `json:"mismatch"`
}

// Fresh reports whether the agent file was written within maxAge of now.
// Returns false for a zero At value.
func (a AgentFile) Fresh(now time.Time, maxAge time.Duration) bool {
	return !a.At.IsZero() && now.Sub(a.At) <= maxAge
}

// CheckRef holds version identifiers for the current or target release.
// Fields omitted in JSON when empty.
type CheckRef struct {
	Commit  string `json:"commit,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Version string `json:"version,omitempty"`
	Date    string `json:"date,omitempty"`
}

// CheckCommit is one entry in the list of commits ahead of the current version.
type CheckCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// CheckFile is the result of the latest update check written by the updater.
// Schema must equal 1. Commits is capped at 200 entries, Subject at 200 chars,
// Notes at 16 KiB.
type CheckFile struct {
	Schema      int           `json:"schema"`
	At          time.Time     `json:"at"`
	Channel     string        `json:"channel"`
	Current     CheckRef      `json:"current"`
	Target      CheckRef      `json:"target"`
	Ahead       int           `json:"ahead"`
	Commits     []CheckCommit `json:"commits"`
	Notes       string        `json:"notes"`
	ManualSteps []string      `json:"manual_steps"`
	Signed      bool          `json:"signed"`
	Signer      string        `json:"signer"`
	Error       string        `json:"error"`
}

// Fresh reports whether the check file was written within maxAge of now.
// Returns false for a zero At value.
func (s CheckFile) Fresh(now time.Time, maxAge time.Duration) bool {
	return !s.At.IsZero() && now.Sub(s.At) <= maxAge
}

// StatePhase records the name, timestamp and outcome of one phase of an update run.
type StatePhase struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
	OK   bool      `json:"ok"`
}

// StateFile is the in-progress or completed state of an update run.
// Schema must equal 1. Result is one of the State* constants.
// FinishedAt is nil while the run is in progress.
type StateFile struct {
	Schema      int          `json:"schema"`
	ID          string       `json:"id"`
	Action      string       `json:"action"`
	From        string       `json:"from"`
	To          string       `json:"to"`
	StartedAt   time.Time    `json:"started_at"`
	FinishedAt  *time.Time   `json:"finished_at,omitempty"`
	Phase       string       `json:"phase"`
	Phases      []StatePhase `json:"phases"`
	Result      string       `json:"result"`
	Reason      string       `json:"reason"`
	EventsLost  int          `json:"events_lost"`
	CronLagging bool         `json:"cron_lagging"`
	User        string       `json:"user"`
}

// ReadyFile is written by admin to acknowledge a pending update request.
// KeitaroIdle true means the import job is not running.
type ReadyFile struct {
	ID          string    `json:"id"`
	KeitaroIdle bool      `json:"keitaro_idle"`
	At          time.Time `json:"at"`
}

// Announced tracks the last update target and run result announced via Telegram.
// Written and read exclusively by cron to deduplicate notifications.
type Announced struct {
	Target   string    `json:"target"`
	ResultID string    `json:"result_id"`
	At       time.Time `json:"at"`
}

// ReadUpdateJSON reads and JSON-decodes the file at path into v.
// Returns os.ErrNotExist when the file does not exist so callers can test with errors.Is.
// Returns an error when path is a symlink, the file is not a regular file,
// or its size exceeds max bytes.
//
// path is the absolute file path. max is the size limit in bytes. v is the target value.
func ReadUpdateJSON(path string, max int64, v any) error {
	if err := checkRegularFile(path, max); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, max+1)).Decode(v)
}

// ReadUpdateText reads up to max bytes of plain text from path and returns it as a string.
// Returns os.ErrNotExist when the file does not exist so callers can test with errors.Is.
// Returns an error when path is a symlink, the file is not a regular file,
// or its size exceeds max bytes.
//
// path is the absolute file path. max is the size limit in bytes.
func ReadUpdateText(path string, max int64) (string, error) {
	if err := checkRegularFile(path, max); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, max)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(buf[:n]), nil
}

// WriteUpdateJSON marshals v as JSON and atomically writes it to path with mode 0644.
// The write is performed through a temporary file in the same directory so a reader
// always sees either the old file or the new one, never a partial write.
//
// path is the absolute file path. v is the value to marshal.
// Returns a JSON marshalling error or a filesystem error.
func WriteUpdateJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0644)
}

// checkRegularFile runs the symlink and size checks shared by Read* functions.
func checkRegularFile(path string, max int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err // preserves os.ErrNotExist
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("updatefiles: %s: not a regular file", path)
	}
	if info.Size() > max {
		return fmt.Errorf("updatefiles: %s: file size %d exceeds limit %d", path, info.Size(), max)
	}
	return nil
}

// Pre-compiled validators.
var (
	reUpdateID = regexp.MustCompile(`^[0-9a-f]{16}$`)
	reCommit   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	reTag      = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	reUser     = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
)

// ValidUpdateID reports whether s is a valid update run identifier (16 lowercase hex chars).
func ValidUpdateID(s string) bool { return reUpdateID.MatchString(s) }

// ValidCommit reports whether s is a valid Git commit SHA (40 lowercase hex chars).
func ValidCommit(s string) bool { return reCommit.MatchString(s) }

// ValidTag reports whether s is a valid semver release tag of the form vX.Y.Z.
func ValidTag(s string) bool { return reTag.MatchString(s) }

// ValidChannel reports whether s is a valid update channel name (release or main).
func ValidChannel(s string) bool { return s == "release" || s == "main" }

// UpdateRequest is a command written by admin to the inbox and read by the updater.
// Action is one of: apply, rollback, check.
// Target is a 40-hex commit SHA, required for apply, empty for rollback and check.
// ID is a 16-hex run identifier generated by admin. User identifies the initiator.
// At is a Unix timestamp in seconds.
type UpdateRequest struct {
	Action string
	Target string
	ID     string
	User   string
	At     int64
}

// reAtValue matches a non-negative decimal integer.
var reAtValue = regexp.MustCompile(`^[0-9]+$`)

// FormatUpdateRequest validates r and serialises it as a key=value text block.
// Lines are written in the fixed order: action, target, id, user, at.
// Each line ends with a newline character.
//
// Returns an error when any field is invalid or the total size would exceed UpdateMaxRequest.
func FormatUpdateRequest(r UpdateRequest) ([]byte, error) {
	if r.Action != "apply" && r.Action != "rollback" && r.Action != "check" {
		return nil, fmt.Errorf("updatefiles: invalid action %q", r.Action)
	}
	if r.Action == "apply" {
		if !ValidCommit(r.Target) {
			return nil, fmt.Errorf("updatefiles: apply requires a valid 40-hex target")
		}
	} else {
		if r.Target != "" {
			return nil, fmt.Errorf("updatefiles: %s must have empty target", r.Action)
		}
	}
	if !ValidUpdateID(r.ID) {
		return nil, fmt.Errorf("updatefiles: invalid id %q", r.ID)
	}
	if !reUser.MatchString(r.User) {
		return nil, fmt.Errorf("updatefiles: invalid user %q", r.User)
	}
	if r.At <= 0 {
		return nil, fmt.Errorf("updatefiles: at must be a positive unix timestamp")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "action=%s\n", r.Action)
	fmt.Fprintf(&sb, "target=%s\n", r.Target)
	fmt.Fprintf(&sb, "id=%s\n", r.ID)
	fmt.Fprintf(&sb, "user=%s\n", r.User)
	fmt.Fprintf(&sb, "at=%d\n", r.At)

	out := []byte(sb.String())
	if int64(len(out)) > UpdateMaxRequest {
		return nil, fmt.Errorf("updatefiles: formatted request size %d exceeds limit %d", len(out), UpdateMaxRequest)
	}
	return out, nil
}

// ParseUpdateRequest parses a key=value text block into an UpdateRequest.
// The block must contain exactly five lines in the fixed order: action, target, id, user, at.
// A trailing newline after the last line is accepted. The total size must not exceed
// UpdateMaxRequest bytes.
//
// Returns an error when the size limit is exceeded, the structure deviates from the
// expected format, any field fails validation, or the action/target combination is invalid.
func ParseUpdateRequest(data []byte) (UpdateRequest, error) {
	if int64(len(data)) > UpdateMaxRequest {
		return UpdateRequest{}, fmt.Errorf("updatefiles: request size %d exceeds limit %d", len(data), UpdateMaxRequest)
	}

	// Normalise CRLF to LF so a request written on Windows parses correctly.
	raw := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(raw, "\n")

	// Accept exactly 5 lines or 5 lines + one trailing empty string from a final \n.
	switch len(lines) {
	case 5:
		// no trailing newline — still valid
	case 6:
		if lines[5] != "" {
			return UpdateRequest{}, fmt.Errorf("updatefiles: expected exactly 5 lines, got extra content after line 5")
		}
		lines = lines[:5]
	default:
		return UpdateRequest{}, fmt.Errorf("updatefiles: expected exactly 5 lines, got %d", len(lines))
	}

	keys := []string{"action", "target", "id", "user", "at"}
	vals := make(map[string]string, 5)
	for i, key := range keys {
		prefix := key + "="
		if !strings.HasPrefix(lines[i], prefix) {
			return UpdateRequest{}, fmt.Errorf("updatefiles: line %d must start with %s=", i+1, key)
		}
		vals[key] = lines[i][len(prefix):]
	}

	action := vals["action"]
	if action != "apply" && action != "rollback" && action != "check" {
		return UpdateRequest{}, fmt.Errorf("updatefiles: invalid action %q", action)
	}

	target := vals["target"]
	if action == "apply" {
		if !ValidCommit(target) {
			return UpdateRequest{}, fmt.Errorf("updatefiles: apply requires a valid 40-hex target, got %q", target)
		}
	} else {
		if target != "" {
			return UpdateRequest{}, fmt.Errorf("updatefiles: %s must have empty target, got %q", action, target)
		}
	}

	id := vals["id"]
	if !ValidUpdateID(id) {
		return UpdateRequest{}, fmt.Errorf("updatefiles: invalid id %q", id)
	}

	user := vals["user"]
	if !reUser.MatchString(user) {
		return UpdateRequest{}, fmt.Errorf("updatefiles: invalid user %q", user)
	}

	atStr := vals["at"]
	if !reAtValue.MatchString(atStr) {
		return UpdateRequest{}, fmt.Errorf("updatefiles: invalid at value %q", atStr)
	}
	at, err := strconv.ParseInt(atStr, 10, 64)
	if err != nil || at <= 0 {
		return UpdateRequest{}, fmt.Errorf("updatefiles: at must be a positive unix timestamp, got %q", atStr)
	}

	return UpdateRequest{
		Action: action,
		Target: target,
		ID:     id,
		User:   user,
		At:     at,
	}, nil
}
