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

// writeCheck stores check as check.json in dir and fails the test on error.
func writeCheck(t *testing.T, dir string, check CheckFile) {
	t.Helper()
	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), check); err != nil {
		t.Fatal(err)
	}
}

// TestCheckUpdatesMainPause verifies that on the main channel a new target is announced at most once
// per updateMainAnnounceEvery: a second push inside the pause is silent, and once the pause
// has passed the target that is current then is announced.
func TestCheckUpdatesMainPause(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	first := makeCheck(now)
	first.Channel = "main"
	writeCheck(t, dir, first)
	e.r.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 1 {
		t.Fatalf("first target: want 1 message, got %d", len(m))
	}

	second := first
	second.Target = CheckRef{Commit: "ccddee1122334455667788990011aabbccddeeff"}
	now = now.Add(updateMainAnnounceEvery - time.Minute)
	second.At = now
	writeCheck(t, dir, second)
	e.r.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 1 {
		t.Fatalf("second target inside the pause: want still 1 message, got %d", len(m))
	}

	third := second
	third.Target = CheckRef{Commit: "ddeeff1122334455667788990011aabbccddeeff"}
	now = now.Add(2 * time.Minute)
	third.At = now
	writeCheck(t, dir, third)
	e.r.checkUpdates(context.Background(), tg)
	m := e.messages()
	if len(m) != 2 {
		t.Fatalf("target after the pause: want 2 messages, got %d", len(m))
	}
	if !strings.Contains(m[1], "ddeeff112233") {
		t.Errorf("the message after the pause must name the current target: %q", m[1])
	}
	e.r.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 2 {
		t.Errorf("the same target again: want still 2 messages, got %d", len(m))
	}
}

// TestCheckUpdatesReleaseNotPaused verifies that the pause belongs to the main channel only.
func TestCheckUpdatesReleaseNotPaused(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	first := makeCheck(now)
	writeCheck(t, dir, first)
	e.r.checkUpdates(context.Background(), tg)

	second := first
	second.Target = CheckRef{Commit: "ccddee1122334455667788990011aabbccddeeff", Tag: "v1.2.0"}
	now = now.Add(time.Minute)
	second.At = now
	writeCheck(t, dir, second)
	e.r.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 2 {
		t.Errorf("release channel: want 2 messages, got %d", len(m))
	}
}

// TestCheckUpdatesMainResultNotPaused verifies that a run result is announced at once while a
// target of the main channel is paused.
func TestCheckUpdatesMainResultNotPaused(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	check := makeCheck(now)
	check.Channel = "main"
	writeCheck(t, dir, check)
	e.r.checkUpdates(context.Background(), tg)

	for i, result := range []string{StateOK, StateRolledBack, StateFailed} {
		state := makeState(now)
		state.ID = "0123456789abcde" + string(rune('0'+i))
		state.Result = result
		if err := WriteUpdateJSON(filepath.Join(dir, UpdateStateFile), state); err != nil {
			t.Fatal(err)
		}
		e.r.checkUpdates(context.Background(), tg)
		if m := e.messages(); len(m) != 2+i {
			t.Fatalf("result %s: want %d messages, got %d", result, 2+i, len(m))
		}
	}
}

// TestCheckUpdatesSignedSuffix verifies that the signed mark is added for a verified target only.
func TestCheckUpdatesSignedSuffix(t *testing.T) {
	for _, signed := range []bool{true, false} {
		e := newEnv(t)
		dir := prepareUpdateDir(t, e)
		now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
		e.r.now = func() time.Time { return now }

		check := makeCheck(now)
		check.Signed = signed
		writeCheck(t, dir, check)
		e.r.checkUpdates(context.Background(), tg)

		m := e.messages()
		if len(m) != 1 {
			t.Fatalf("signed=%v: want 1 message, got %d", signed, len(m))
		}
		if has := strings.Contains(m[0], "signed ✓"); has != signed {
			t.Errorf("signed=%v: mark present = %v in %q", signed, has, m[0])
		}
	}
}

// TestTargetPaused covers the pause rule directly: the channel, a zero time, the edges of the
// period and a time in the future.
func TestTargetPaused(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		channel  string
		targetAt time.Time
		want     bool
	}{
		{"main, never announced", "main", time.Time{}, false},
		{"main, just announced", "main", now, true},
		{"main, inside the period", "main", now.Add(-updateMainAnnounceEvery + time.Second), true},
		{"main, period over", "main", now.Add(-updateMainAnnounceEvery), false},
		{"main, clock moved back", "main", now.Add(time.Minute), false},
		{"release, just announced", "release", now, false},
	}
	for _, c := range cases {
		got := targetPaused(CheckFile{Channel: c.channel}, Announced{TargetAt: c.targetAt}, now)
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
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

// TestNotifyMessagesEscapeVolumeFields verifies that the fields of check.json and state.json
// that reach a message (channel, version labels, commits, state result) are escaped, so that
// markup written to the shared volume does not become part of the Telegram HTML.
func TestNotifyMessagesEscapeVolumeFields(t *testing.T) {
	const markup = `<a href="//evil">x</a>`
	msgs := updateLangs["en"]

	check := makeCheck(time.Now())
	check.Channel = markup
	check.Current = CheckRef{Version: markup}
	check.Target = CheckRef{Tag: markup}
	if msg := buildTargetMsg(check, msgs); strings.Contains(msg, "<a ") || strings.Contains(msg, "</a>") {
		t.Errorf("target message carries markup: %q", msg)
	}

	state := makeState(time.Now())
	state.From = markup
	state.To = markup
	state.Result = markup
	if msg := buildResultMsg(state, msgs); strings.Contains(msg, "<a ") || strings.Contains(msg, "</a>") {
		t.Errorf("result message carries markup: %q", msg)
	}
}

// TestCheckUpdatesIgnoresInvalidCheck verifies that a check.json with a foreign channel or tag
// sends no message and is not recorded as announced.
func TestCheckUpdatesIgnoresInvalidCheck(t *testing.T) {
	e := newEnv(t)
	dir := prepareUpdateDir(t, e)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return now }

	check := makeCheck(now)
	check.Target.Tag = `v1.1.0"><a href="//evil">x</a>`
	if err := WriteUpdateJSON(filepath.Join(dir, UpdateCheckFile), check); err != nil {
		t.Fatal(err)
	}

	e.r.checkUpdates(context.Background(), tg)
	if m := e.messages(); len(m) != 0 {
		t.Errorf("invalid check.json must not be announced: %q", m)
	}
}
