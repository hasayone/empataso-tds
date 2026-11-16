package cron

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// updateDir returns the update sub-directory for a runner built with newEnv.
func testUpdateDir(e *env) string {
	return filepath.Join(e.dir, "update")
}

// prepareUpdateDir creates the update directory and writes check and/or state
// files, returning the directory path.
func prepareUpdateDir(t *testing.T, e *env) string {
	t.Helper()
	dir := testUpdateDir(e)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func makeCheck(now time.Time) CheckFile {
	return CheckFile{
		Schema:  1,
		At:      now.Add(-time.Hour),
		Channel: "release",
		Current: CheckRef{Commit: "aabbccddee1122334455667788990011aabbccdd", Version: "v1.0.0"},
		Target:  CheckRef{Commit: "bbccddee1122334455667788990011aabbccddee", Tag: "v1.1.0"},
		Ahead:   3,
		Signed:  true,
		Commits: []CheckCommit{
			{SHA: "bbccddee1122334455667788990011aabbccddee", Subject: "feat: add update support"},
		},
	}
}

func makeState(now time.Time) StateFile {
	finished := now.Add(-30 * time.Minute)
	return StateFile{
		Schema:     1,
		ID:         "0123456789abcdef",
		Action:     "apply",
		From:       "aabbccddee1122334455667788990011aabbccdd",
		To:         "bbccddee1122334455667788990011aabbccddee",
		StartedAt:  finished.Add(-5 * time.Minute),
		FinishedAt: &finished,
		Result:     StateOK,
	}
}

// TestCheckUpdatesNoDoubleTarget verifies that reading the same check.json
// twice does not send a second Telegram message.
func TestCheckUpdatesNoDoubleTarget(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), makeCheck(now)); err != nil {
		t.Fatal(err)
	}

	e.r.checkUpdates(context.Background(), tg)
	e.r.checkUpdates(context.Background(), tg)

	if m := e.messages(); len(m) != 1 {
		t.Errorf("want 1 message, got %d: %q", len(m), m)
	}
}

// TestCheckUpdatesNoDoubleResult verifies that a completed run result is
// announced exactly once even when checkUpdates is called repeatedly.
func TestCheckUpdatesNoDoubleResult(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	if err := WriteUpdateJSON(filepath.Join(dir, UpdateStateFile), makeState(now)); err != nil {
		t.Fatal(err)
	}

	e.r.checkUpdates(context.Background(), tg)
	e.r.checkUpdates(context.Background(), tg)

	if m := e.messages(); len(m) != 1 {
		t.Errorf("want 1 message, got %d: %q", len(m), m)
	}
}

// TestCheckUpdatesDedupSurvivesRestart verifies that announced.json prevents
// duplicate notifications even after a new Runner is created (simulating
// cron restart after a rollback that overwrites the status file).
func TestCheckUpdatesDedupSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), makeCheck(now)); err != nil {
		t.Fatal(err)
	}

	e.r.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 1 {
		t.Fatalf("want 1 message after first check, got %d", len(m))
	}

	// Simulate rollback: a new Runner is created (new process, status file cleared).
	r2 := New(e.r.paths, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r2.now = func() time.Time { return now }
	r2.telegramBase = e.r.telegramBase

	r2.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 1 {
		t.Errorf("after restart, announced.json must prevent second message: got %d messages", len(m))
	}
}

// TestCheckUpdatesSymlinkIgnored verifies that a symlink in place of
// check.json does not trigger a notification (ReadUpdateJSON rejects symlinks).
func TestCheckUpdatesSymlinkIgnored(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	realPath := filepath.Join(e.dir, "real_check.json")
	if err := WriteUpdateJSON(realPath, makeCheck(now)); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, UpdateCheckFile)
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}

	e.r.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 0 {
		t.Errorf("symlink check.json must not trigger notification: got %d messages: %q", len(m), m)
	}
}

// TestCheckUpdatesSubjectEscaped verifies that HTML-special characters in a
// commit subject are escaped before the message is sent to Telegram.
func TestCheckUpdatesSubjectEscaped(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	check := makeCheck(now)
	check.Commits = []CheckCommit{{
		SHA:     "bbccddee1122334455667788990011aabbccddee",
		Subject: `feat: add <script>alert(1)</script>`,
	}}
	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), check); err != nil {
		t.Fatal(err)
	}

	e.r.checkUpdates(context.Background(), tg)

	m := e.messages()
	if len(m) == 0 {
		t.Fatal("expected a message")
	}
	if strings.Contains(m[0], "<script>") {
		t.Errorf("message contains unescaped <script>: %q", m[0])
	}
}

// TestCheckUpdatesSkipsErrorAndZeroAhead verifies that check.json with a
// non-empty Error field or Ahead=0 does not trigger a notification.
func TestCheckUpdatesSkipsErrorAndZeroAhead(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	base := makeCheck(now)

	withError := base
	withError.Error = "git fetch failed"
	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), withError); err != nil {
		t.Fatal(err)
	}
	e.r.checkUpdates(context.Background(), tg)

	withZero := base
	withZero.Ahead = 0
	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), withZero); err != nil {
		t.Fatal(err)
	}
	e.r.checkUpdates(context.Background(), tg)

	if m := e.messages(); len(m) != 0 {
		t.Errorf("error or ahead=0 must not trigger notification, got %d: %q", len(m), m)
	}
}

// TestCheckUpdatesLangRu verifies that the target notification is sent in
// Russian when EMTDS_ALERT_LANG=ru.
func TestCheckUpdatesLangRu(t *testing.T) {
	t.Setenv("EMTDS_ALERT_LANG", "ru")

	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), makeCheck(now)); err != nil {
		t.Fatal(err)
	}

	e.r.checkUpdates(context.Background(), tg)

	m := e.messages()
	if len(m) != 1 {
		t.Fatalf("want 1 message, got %d", len(m))
	}
	if !strings.Contains(m[0], "доступно обновление") {
		t.Errorf("want Russian text, got: %q", m[0])
	}
	if !strings.Contains(m[0], "коммит") {
		t.Errorf("want Russian commit count in text, got: %q", m[0])
	}
}

// TestCheckUpdatesNoGroupsPath verifies that maybeCheckUpdates is a no-op
// when Groups is not set.
func TestCheckUpdatesNoGroupsPath(t *testing.T) {
	e := newEnv(t)
	e.r.paths.Groups = ""
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	e.r.maybeCheckUpdates(context.Background(), tg)
	e.r.Wait()
	if m := e.messages(); len(m) != 0 {
		t.Errorf("empty Groups must not send anything: %q", m)
	}
}

// TestCheckUpdatesResultMessage verifies the result message fields (from12→to12,
// reason, cron_lagging, events_lost).
func TestCheckUpdatesResultMessage(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	finished := now.Add(-10 * time.Minute)
	state := StateFile{
		Schema:      1,
		ID:          "0123456789abcdef",
		Action:      "apply",
		From:        "aabbccddee1122334455667788990011aabbccdd",
		To:          "bbccddee1122334455667788990011aabbccddee",
		StartedAt:   finished.Add(-5 * time.Minute),
		FinishedAt:  &finished,
		Result:      StateFailed,
		Reason:      "health check failed",
		CronLagging: true,
		EventsLost:  3,
	}
	if err := WriteUpdateJSON(filepath.Join(dir, UpdateStateFile), state); err != nil {
		t.Fatal(err)
	}

	e.r.checkUpdates(context.Background(), tg)

	m := e.messages()
	if len(m) != 1 {
		t.Fatalf("want 1 message, got %d", len(m))
	}
	msg := m[0]
	if !strings.Contains(msg, "aabbccddee11") {
		t.Errorf("want from12 in message: %q", msg)
	}
	if !strings.Contains(msg, "bbccddee1122") {
		t.Errorf("want to12 in message: %q", msg)
	}
	if !strings.Contains(msg, "health check failed") {
		t.Errorf("want reason in message: %q", msg)
	}
}
