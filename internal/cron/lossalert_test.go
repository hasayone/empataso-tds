package cron

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

var testLossAlert = LossAlert{Repeat: 6 * time.Hour, RecoverAfter: 10 * time.Minute, Lang: "en"}

var lossT0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// engineMetrics builds the metrics of one engine process.
// started is the process start, updated the file time and lost the loss counters.
func engineMetrics(name string, started, updated time.Time, lost config.LostEvents) config.EngineMetrics {
	return config.EngineMetrics{Instance: name, StartedAt: started, UpdatedAt: updated, Events: config.EventMetrics{Connected: true, Lost: lost}}
}

// TestDecideAlertRule checks the shared rule: raise, silence, remind, keep, clear.
func TestDecideAlertRule(t *testing.T) {
	now := lossT0
	f, v := decideAlert(alertFlag{}, false, false, time.Hour, now)
	if v != alertQuiet || f.On {
		t.Fatalf("nothing: %+v %v", f, v)
	}
	f, v = decideAlert(f, true, false, time.Hour, now)
	if v != alertRaise || !f.On || !f.At.Equal(now) {
		t.Fatalf("raise: %+v %v", f, v)
	}
	f, v = decideAlert(f, true, false, time.Hour, now.Add(time.Minute))
	if v != alertQuiet || !f.At.Equal(now) {
		t.Fatalf("still hot inside the repeat period: %+v %v", f, v)
	}
	f, v = decideAlert(f, false, false, time.Hour, now.Add(2*time.Minute))
	if v != alertQuiet || !f.On {
		t.Fatalf("neither hot nor calm keeps the flag: %+v %v", f, v)
	}
	f, v = decideAlert(f, true, false, time.Hour, now.Add(time.Hour))
	if v != alertRemind || !f.At.Equal(now.Add(time.Hour)) {
		t.Fatalf("remind: %+v %v", f, v)
	}
	if _, v = decideAlert(f, true, false, 0, now.Add(100*time.Hour)); v != alertQuiet {
		t.Fatalf("repeat 0 never reminds, got %v", v)
	}
	f, v = decideAlert(f, false, true, time.Hour, now.Add(3*time.Hour))
	if v != alertClear || f.On || !f.At.IsZero() {
		t.Fatalf("clear: %+v %v", f, v)
	}
	if _, v = decideAlert(f, false, true, time.Hour, now); v != alertQuiet {
		t.Fatalf("calm without an alert sends nothing, got %v", v)
	}
}

// TestDecideLossRaiseRemindClear walks one instance through the whole life of an alert.
func TestDecideLossRaiseRemindClear(t *testing.T) {
	a := testLossAlert
	started := lossT0.Add(-time.Hour)
	at := func(d time.Duration, lost config.LostEvents) ([]config.EngineMetrics, time.Time) {
		now := lossT0.Add(d)
		return []config.EngineMetrics{engineMetrics("engine", started, now, lost)}, now
	}

	m, now := at(0, config.LostEvents{})
	st, v := decideLoss(nil, m, a, now)
	if v != alertQuiet || st.Alerted || st.Bases["engine"].StartedAt != started {
		t.Fatalf("the first look only sets the base: %+v %v", st, v)
	}
	m, now = at(time.Minute, config.LostEvents{Queue: 40, Insert: 2})
	st, v = decideLoss(&st, m, a, now)
	if v != alertRaise || !st.Alerted || st.Lost != 42 || len(st.Instances) != 1 || st.Instances[0] != "engine" {
		t.Fatalf("a loss raises: %+v %v", st, v)
	}
	m, now = at(2*time.Minute, config.LostEvents{Queue: 50, Insert: 2})
	st, v = decideLoss(&st, m, a, now)
	if v != alertQuiet || !st.Alerted || st.Lost != 52 {
		t.Fatalf("more loss inside the repeat period stays quiet: %+v %v", st, v)
	}
	m, now = at(5*time.Minute, config.LostEvents{Queue: 50, Insert: 2})
	st, v = decideLoss(&st, m, a, now)
	if v != alertQuiet || !st.Alerted {
		t.Fatalf("no growth but not yet quiet long enough: %+v %v", st, v)
	}
	m, now = at(5*time.Minute+a.RecoverAfter-time.Minute, config.LostEvents{Queue: 50, Insert: 2})
	if _, v = decideLoss(&st, m, a, now); v != alertQuiet {
		t.Fatalf("one minute short of the recovery time: %v", v)
	}
	m, now = at(5*time.Minute+a.RecoverAfter, config.LostEvents{Queue: 50, Insert: 2})
	st, v = decideLoss(&st, m, a, now)
	if v != alertClear || st.Alerted || st.Lost != 52 {
		t.Fatalf("recovery: %+v %v", st, v)
	}
	m, now = at(7*time.Hour, config.LostEvents{Queue: 60, Insert: 2})
	st, v = decideLoss(&st, m, a, now)
	if v != alertRaise || st.Lost != 10 {
		t.Fatalf("a new loss after recovery alerts again from zero: %+v %v", st, v)
	}
	m, now = at(14*time.Hour, config.LostEvents{Queue: 70, Insert: 2})
	st, v = decideLoss(&st, m, a, now)
	if v != alertRemind || st.Lost != 20 {
		t.Fatalf("a loss that goes on after the repeat period reminds: %+v %v", st, v)
	}
}

// firstLook runs the first check over one healthy file of a process that started
// at started with the given counters and returns the state.
func firstLook(t *testing.T, started time.Time, lost config.LostEvents) LossState {
	t.Helper()
	m := []config.EngineMetrics{engineMetrics("engine", started, lossT0, lost)}
	st, v := decideLoss(nil, m, testLossAlert, lossT0)
	if v != alertQuiet || st.Alerted {
		t.Fatalf("the first look must not alert: %+v %v", st, v)
	}
	return st
}

// TestDecideLossRestartCountsLossesAfterStart checks that a restarted process
// starts from a zero base: what it has lost since its start is a loss, a restart
// with nothing lost is not, and the counters after that are measured against
// the new process.
func TestDecideLossRestartCountsLossesAfterStart(t *testing.T) {
	a := testLossAlert
	old := lossT0.Add(-2 * time.Hour)
	restarted := lossT0.Add(time.Minute)
	now := restarted.Add(30 * time.Second)

	st := firstLook(t, old, config.LostEvents{Queue: 500, Insert: 100})
	clean := []config.EngineMetrics{engineMetrics("engine", restarted, now, config.LostEvents{})}
	if st2, v := decideLoss(&st, clean, a, now); v != alertQuiet || st2.Alerted || !st2.Bases["engine"].StartedAt.Equal(restarted) {
		t.Fatalf("a restart without losses: %+v %v", st2, v)
	}

	lossy := []config.EngineMetrics{engineMetrics("engine", restarted, now, config.LostEvents{Queue: 3})}
	st, v := decideLoss(&st, lossy, a, now)
	if v != alertRaise || !st.Alerted || st.Lost != 3 || st.Bases["engine"].Lost.Queue != 3 {
		t.Fatalf("losses after the start of a restarted process: %+v %v", st, v)
	}
	now = now.Add(time.Minute)
	same := []config.EngineMetrics{engineMetrics("engine", restarted, now, config.LostEvents{Queue: 3})}
	if st, v = decideLoss(&st, same, a, now); v != alertQuiet || st.Lost != 3 {
		t.Fatalf("same counters after the restart: %+v %v", st, v)
	}
	now = now.Add(time.Minute)
	more := []config.EngineMetrics{engineMetrics("engine", restarted, now, config.LostEvents{Queue: 8})}
	if st, v = decideLoss(&st, more, a, now); v != alertQuiet || st.Lost != 8 {
		t.Fatalf("growth inside the alert: %+v %v", st, v)
	}
}

// TestDecideLossPreviousCountedOnce checks the tail of the process before a
// restart: what it lost after the last check counts, once; a Previous that the
// base already covers, or one older than the base, adds nothing; a process the
// check never saw counts in full.
func TestDecideLossPreviousCountedOnce(t *testing.T) {
	a := testLossAlert
	procA := lossT0.Add(-3 * time.Hour)
	procB := lossT0.Add(-2 * time.Hour)
	procC := lossT0.Add(-time.Hour)
	restart := func(started time.Time, now time.Time, prev *config.PreviousRun) []config.EngineMetrics {
		m := engineMetrics("engine", started, now, config.LostEvents{})
		m.Previous = prev
		return []config.EngineMetrics{m}
	}

	// The check knew A at 100; A ended with 130; the restarted B has lost nothing.
	st := firstLook(t, procA, config.LostEvents{Queue: 100})
	now := lossT0.Add(time.Minute)
	prev := &config.PreviousRun{StartedAt: procA, Lost: config.LostEvents{Queue: 130}}
	st, v := decideLoss(&st, restart(procB, now, prev), a, now)
	if v != alertRaise || st.Lost != 30 {
		t.Fatalf("the tail of the old process: %+v %v", st, v)
	}
	// The same file again, then B growing: the Previous is not counted a second time.
	now = now.Add(time.Minute)
	if st, v = decideLoss(&st, restart(procB, now, prev), a, now); v != alertQuiet || st.Lost != 30 {
		t.Fatalf("Previous counted twice: %+v %v", st, v)
	}
	// A second restart whose Previous is B: B was the base, so only its growth since counts.
	now = now.Add(time.Minute)
	prevB := &config.PreviousRun{StartedAt: procB, Lost: config.LostEvents{}}
	if st, v = decideLoss(&st, restart(procC, now, prevB), a, now); v != alertQuiet || st.Lost != 30 {
		t.Fatalf("a Previous without growth: %+v %v", st, v)
	}

	// A process the check never saw (two restarts between checks) counts in full.
	fresh := firstLook(t, procA, config.LostEvents{Queue: 100})
	unseen := &config.PreviousRun{StartedAt: procB, Lost: config.LostEvents{Insert: 7}}
	if st2, v := decideLoss(&fresh, restart(procC, now, unseen), a, now); v != alertRaise || st2.Lost != 7 {
		t.Fatalf("an unseen previous process: %+v %v", st2, v)
	}
	// A Previous older than the base was counted before.
	older := &config.PreviousRun{StartedAt: procA, Lost: config.LostEvents{Insert: 50}}
	base := firstLook(t, procB, config.LostEvents{})
	if st2, v := decideLoss(&base, restart(procC, now, older), a, now); v != alertQuiet || st2.Alerted {
		t.Fatalf("an old Previous: %+v %v", st2, v)
	}
}

// TestDecideLossFirstRunWithCounters checks that a cron that sees an engine for
// the first time never alerts on what is already counted, Previous included.
func TestDecideLossFirstRunWithCounters(t *testing.T) {
	m := engineMetrics("engine", lossT0.Add(-time.Hour), lossT0, config.LostEvents{Queue: 900, Late: 4})
	m.Previous = &config.PreviousRun{StartedAt: lossT0.Add(-2 * time.Hour), Lost: config.LostEvents{Insert: 70}}
	st, v := decideLoss(nil, []config.EngineMetrics{m}, testLossAlert, lossT0)
	if v != alertQuiet || st.Alerted || st.Bases["engine"].Lost.Queue != 900 {
		t.Fatalf("first run: %+v %v", st, v)
	}
	now := lossT0.Add(time.Minute)
	m.UpdatedAt = now
	if st, v = decideLoss(&st, []config.EngineMetrics{m}, testLossAlert, now); v != alertQuiet || st.Alerted {
		t.Fatalf("the same counters on the second check: %+v %v", st, v)
	}
}

// TestDecideLossNotConnected checks the third trigger: an engine with a
// ClickHouse address that has no connection for a minute is trouble even with
// nothing lost, and holds the alert open.
func TestDecideLossNotConnected(t *testing.T) {
	a := testLossAlert
	started := lossT0.Add(-time.Hour)
	down := func(now time.Time) []config.EngineMetrics {
		m := engineMetrics("engine", started, now, config.LostEvents{})
		m.Events.Configured, m.Events.Connected = true, false
		return []config.EngineMetrics{m}
	}
	st := firstLook(t, started, config.LostEvents{})

	now := lossT0.Add(10 * time.Second)
	st, v := decideLoss(&st, down(now), a, now)
	if v != alertQuiet || st.Alerted || !st.QuietSince.IsZero() || !st.Bases["engine"].DownSince.Equal(now) {
		t.Fatalf("first sight of the outage: %+v %v", st, v)
	}
	now = now.Add(lossFailingAfter - time.Second)
	if st, v = decideLoss(&st, down(now), a, now); v != alertQuiet || st.Alerted {
		t.Fatalf("59 s without a connection: %+v %v", st, v)
	}
	now = now.Add(time.Second)
	st, v = decideLoss(&st, down(now), a, now)
	if v != alertRaise || st.Lost != 0 || len(st.Instances) != 1 {
		t.Fatalf("60 s without a connection: %+v %v", st, v)
	}
	if msg := lossMessage(v, st, "en"); !strings.Contains(msg, "cannot be written") || strings.Contains(msg, "%!") {
		t.Fatalf("message for no loss counted: %q", msg)
	}
	// Still down after the recovery time: the alert stays and nothing is sent again.
	now = now.Add(a.RecoverAfter + time.Minute)
	if st, v = decideLoss(&st, down(now), a, now); v != alertQuiet || !st.Alerted || !st.QuietSince.IsZero() {
		t.Fatalf("a long outage must hold the alert: %+v %v", st, v)
	}
	// Connected again: clear only after the quiet window.
	healthy := func(now time.Time) []config.EngineMetrics {
		m := engineMetrics("engine", started, now, config.LostEvents{})
		m.Events.Configured = true
		return []config.EngineMetrics{m}
	}
	now = now.Add(time.Second)
	if st, v = decideLoss(&st, healthy(now), a, now); v != alertQuiet || !st.Alerted || !st.Bases["engine"].DownSince.IsZero() {
		t.Fatalf("connected, quiet window not over: %+v %v", st, v)
	}
	now = now.Add(a.RecoverAfter)
	st, v = decideLoss(&st, healthy(now), a, now)
	if v != alertClear || st.Alerted {
		t.Fatalf("clear: %+v %v", st, v)
	}
	if msg := lossMessage(v, st, "en"); !strings.Contains(msg, "Nothing was lost") {
		t.Fatalf("clear message for no loss: %q", msg)
	}
	// An engine without a ClickHouse address is not an outage.
	m := engineMetrics("engine", started, now, config.LostEvents{})
	m.Events.Connected = false
	if st, v = decideLoss(nil, []config.EngineMetrics{m}, a, now); v != alertQuiet || !st.Bases["engine"].DownSince.IsZero() {
		t.Fatalf("not configured: %+v %v", st, v)
	}
}

// TestDecideLossSilence checks that no fresh file for LossSilentAfter asks for
// one log warning, repeated once an hour, never an alert, and that a fresh file
// ends it.
func TestDecideLossSilence(t *testing.T) {
	a := testLossAlert
	st, v := decideLoss(nil, nil, a, lossT0)
	if v != alertQuiet || st.silentWarnDue() || !st.SilentSince.Equal(lossT0) {
		t.Fatalf("start of the silence: %+v %v", st, v)
	}
	now := lossT0.Add(LossSilentAfter - time.Second)
	if st, _ = decideLoss(&st, nil, a, now); st.silentWarnDue() {
		t.Fatal("warning before the silence is long enough")
	}
	now = lossT0.Add(LossSilentAfter)
	st, v = decideLoss(&st, nil, a, now)
	if v != alertQuiet || st.Alerted || !st.silentWarnDue() {
		t.Fatalf("warning after the silence: %+v %v", st, v)
	}
	now = now.Add(lossSilentWarnEvery - time.Minute)
	if st, _ = decideLoss(&st, nil, a, now); st.silentWarnDue() {
		t.Fatal("the warning must not repeat inside the hour")
	}
	now = now.Add(time.Minute)
	if st, _ = decideLoss(&st, nil, a, now); !st.silentWarnDue() {
		t.Fatal("the warning repeats after an hour")
	}
	// A stale file is silence too.
	stale := []config.EngineMetrics{engineMetrics("engine", lossT0, lossT0, config.LostEvents{})}
	if st, _ = decideLoss(&st, stale, a, now.Add(time.Minute)); st.SilentSince.IsZero() {
		t.Fatal("a stale file ends nothing")
	}
	now = now.Add(2 * time.Minute)
	fresh := []config.EngineMetrics{engineMetrics("engine", lossT0, now, config.LostEvents{})}
	if st, _ = decideLoss(&st, fresh, a, now); !st.SilentSince.IsZero() || !st.SilentWarnedAt.IsZero() {
		t.Fatalf("a fresh file ends the silence: %+v", st)
	}
}

// TestLossCheckLogsSilence checks that the runner writes the silence warning to
// its log and sends no Telegram message.
func TestLossCheckLogsSilence(t *testing.T) {
	e := newEnv(t)
	now := lossT0
	e.r.now = func() time.Time { return now }
	var logged strings.Builder
	e.r.log = slog.New(slog.NewTextHandler(&logged, nil))
	a := testLossAlert
	a.Dir = t.TempDir()
	e.r.WatchLoss(a)
	e.r.checkLoss(context.Background(), tg)
	now = now.Add(LossSilentAfter)
	e.r.checkLoss(context.Background(), tg)
	now = now.Add(time.Minute)
	e.r.checkLoss(context.Background(), tg)
	if n := strings.Count(logged.String(), "no engine metrics file is fresh"); n != 1 {
		t.Fatalf("want one warning, got %d: %s", n, logged.String())
	}
	if m := e.messages(); len(m) != 0 {
		t.Fatalf("silence must not reach Telegram: %q", m)
	}
}

// TestDecideLossFailingInserts checks the second trigger: inserts that keep
// failing, with no loss counted yet, alert after a minute and not before, and
// a failing streak holds the alert even when nothing new is lost.
func TestDecideLossFailingInserts(t *testing.T) {
	a := testLossAlert
	started := lossT0.Add(-time.Hour)
	failingFor := func(now time.Time, d time.Duration) []config.EngineMetrics {
		m := engineMetrics("engine", started, now, config.LostEvents{})
		since := now.Add(-d)
		m.Events.FailingSince = &since
		return []config.EngineMetrics{m}
	}
	st, _ := decideLoss(nil, []config.EngineMetrics{engineMetrics("engine", started, lossT0, config.LostEvents{})}, a, lossT0)

	now := lossT0.Add(10 * time.Second)
	if st, v := decideLoss(&st, failingFor(now, 59*time.Second), a, now); v != alertQuiet || st.Alerted {
		t.Fatalf("59 s of failure: %+v %v", st, v)
	}
	now = now.Add(time.Second)
	st, v := decideLoss(&st, failingFor(now, time.Minute), a, now)
	if v != alertRaise || st.Lost != 0 || len(st.Instances) != 1 {
		t.Fatalf("60 s of failure: %+v %v", st, v)
	}
	now = now.Add(a.RecoverAfter + time.Minute)
	if st2, v := decideLoss(&st, failingFor(now, a.RecoverAfter+time.Minute), a, now); v != alertQuiet || !st2.Alerted {
		t.Fatalf("a streak that goes on keeps the alert and sends no second message: %+v %v", st2, v)
	}
	// A streak that is younger than a minute is not trouble yet, but it is not
	// quiet either: it must neither alert nor start the recovery wait.
	if st2, v := decideLoss(&st, failingFor(now, 30*time.Second), a, now); v != alertQuiet || !st2.Alerted || !st2.QuietSince.IsZero() {
		t.Fatalf("a young failing streak: %+v %v", st2, v)
	}
}

// TestDecideLossStaleFilesAndRemovedInstances checks that a file older than
// LossStale is ignored, that a raised alert survives a dead engine instead of
// clearing silently, and that the base of an instance without a file is dropped.
func TestDecideLossStaleFilesAndRemovedInstances(t *testing.T) {
	a := testLossAlert
	started := lossT0.Add(-time.Hour)
	st, _ := decideLoss(nil, []config.EngineMetrics{
		engineMetrics("engine", started, lossT0, config.LostEvents{}),
		engineMetrics("engine2", started, lossT0, config.LostEvents{}),
	}, a, lossT0)
	now := lossT0.Add(time.Minute)
	st, v := decideLoss(&st, []config.EngineMetrics{
		engineMetrics("engine", started, now, config.LostEvents{Insert: 9}),
		engineMetrics("engine2", started, lossT0, config.LostEvents{}),
	}, a, now)
	if v != alertRaise || len(st.Instances) != 1 || st.Instances[0] != "engine" {
		t.Fatalf("raise by engine: %+v %v", st, v)
	}

	// Both files go stale: the alert stays, however long it takes.
	now = lossT0.Add(2 * time.Hour)
	stale := []config.EngineMetrics{
		engineMetrics("engine", started, lossT0.Add(time.Minute), config.LostEvents{Insert: 99}),
		engineMetrics("engine2", started, lossT0, config.LostEvents{}),
	}
	st, v = decideLoss(&st, stale, a, now)
	if v != alertQuiet || !st.Alerted || st.Bases["engine"].Lost.Insert != 9 {
		t.Fatalf("a dead engine keeps the alert and its base: %+v %v", st, v)
	}

	// engine2 comes back healthy, engine has no file any more: quiet long enough, so clear.
	now = now.Add(time.Minute)
	st, _ = decideLoss(&st, []config.EngineMetrics{engineMetrics("engine2", started, now, config.LostEvents{})}, a, now)
	if _, ok := st.Bases["engine"]; ok {
		t.Fatalf("the base of a removed instance stays: %+v", st.Bases)
	}
	if !st.Alerted {
		t.Fatalf("the alert is cleared by the first fresh healthy check, it must wait: %+v", st)
	}
	now = now.Add(a.RecoverAfter)
	st, v = decideLoss(&st, []config.EngineMetrics{engineMetrics("engine2", started, now, config.LostEvents{})}, a, now)
	if v != alertClear || st.Alerted {
		t.Fatalf("clear after a quiet window: %+v %v", st, v)
	}
}

// TestLossMessagesAndNotice checks the Telegram texts in three languages and the admin notice line.
func TestLossMessagesAndNotice(t *testing.T) {
	st := LossState{CheckedAt: lossT0, Alerted: true, Lost: 42, Instances: []string{"engine", "engine2"}}
	for lang, word := range map[string]string{"en": "not reaching ClickHouse", "ru": "не доходят до ClickHouse", "uk": "не доходять до ClickHouse", "xx": "not reaching"} {
		raise := lossMessage(alertRaise, st, lang)
		if !strings.Contains(raise, word) || !strings.Contains(raise, "engine, engine2") || !strings.Contains(raise, "42") || strings.Contains(raise, "%!") {
			t.Errorf("%s raise: %q", lang, raise)
		}
		if remind := lossMessage(alertRemind, st, lang); remind != raise {
			t.Errorf("%s remind differs from raise: %q", lang, remind)
		}
		if clear := lossMessage(alertClear, st, lang); clear == "" || strings.Contains(clear, "%!") || !strings.Contains(clear, "42") {
			t.Errorf("%s clear: %q", lang, clear)
		}
	}
	if lossMessage(alertQuiet, st, "en") != "" {
		t.Error("quiet sends nothing")
	}

	none := LossState{CheckedAt: lossT0, Alerted: true, Instances: []string{"engine"}}
	for lang, word := range map[string]string{"en": "cannot be written", "ru": "не удаётся записать", "uk": "не вдається записати", "xx": "cannot be written"} {
		raise := lossMessage(alertRaise, none, lang)
		if !strings.Contains(raise, word) || !strings.Contains(raise, "(engine)") || strings.Contains(raise, "%!") || strings.Contains(raise, " 0 ") {
			t.Errorf("%s raise without a count: %q", lang, raise)
		}
		if clear := lossMessage(alertClear, none, lang); clear == "" || strings.Contains(clear, "%!") || strings.Contains(clear, "0") {
			t.Errorf("%s clear without a count: %q", lang, clear)
		}
	}

	line, ok := LossNotice(Status{Loss: &st}, lossT0)
	if !ok || line != "2026-11-16T12:00:00Z event loss 42 (engine,engine2)" {
		t.Fatalf("notice = %q %v", line, ok)
	}
	if _, ok := LossNotice(Status{Loss: &st}, lossT0.Add(LossStateFresh+time.Minute)); ok {
		t.Error("a stale check must not raise the notice")
	}
	st.Alerted = false
	if _, ok := LossNotice(Status{Loss: &st}, lossT0); ok {
		t.Error("no notice without an alert")
	}
	if _, ok := LossNotice(Status{}, lossT0); ok {
		t.Error("no notice before the first check")
	}
}

// TestLossAlertFromEnv checks the metrics directory and the language settings.
func TestLossAlertFromEnv(t *testing.T) {
	t.Setenv("EMTDS_METRICS_DIR", "/m")
	t.Setenv("EMTDS_ALERT_LANG", "UK")
	a := LossAlertFromEnv()
	if a.Dir != "/m" || a.Lang != "uk" || a.Repeat != DefaultLossAlertRepeat || a.RecoverAfter != DefaultLossRecoverAfter {
		t.Fatalf("got %+v", a)
	}
	t.Setenv("EMTDS_ALERT_LANG", "de")
	if got := LossAlertFromEnv().Lang; got != "en" {
		t.Fatalf("unknown language %q", got)
	}
}

// writeEngineFile stores m as <dir>/<name>.json, the way an engine does.
func writeEngineFile(t *testing.T, dir, name string, m config.EngineMetrics) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadEngineMetrics checks that only valid metrics files are read, named by
// their file, and that a missing directory is an empty list.
func TestReadEngineMetrics(t *testing.T) {
	dir := t.TempDir()
	writeEngineFile(t, dir, "engine", engineMetrics("liar", lossT0, lossT0, config.LostEvents{}))
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Bad Name.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := readEngineMetrics(dir)
	if err != nil || len(got) != 1 || got[0].Instance != "engine" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := readEngineMetrics(filepath.Join(dir, "missing")); err != nil || len(got) != 0 {
		t.Fatalf("missing directory: %+v, %v", got, err)
	}
}

// TestLossCheckSendsOnceAndClearsOnce drives the runner over real files: one
// Telegram message on the way up, none while it goes on, one on recovery, the
// notice in the status file, and no alert after an engine restart.
func TestLossCheckSendsOnceAndClearsOnce(t *testing.T) {
	e := newEnv(t)
	now := lossT0
	e.r.now = func() time.Time { return now }
	metrics := t.TempDir()
	a := testLossAlert
	a.Dir = metrics
	e.r.WatchLoss(a)
	check := func() { e.r.checkLoss(context.Background(), tg) }
	started := lossT0.Add(-time.Hour)
	put := func(lost config.LostEvents) {
		writeEngineFile(t, metrics, "engine", engineMetrics("engine", started, now, lost))
	}

	put(config.LostEvents{})
	check()
	now = now.Add(time.Minute)
	put(config.LostEvents{Queue: 120})
	check()
	now = now.Add(30 * time.Second)
	put(config.LostEvents{Queue: 150})
	check()
	check()
	if m := e.messages(); len(m) != 1 || !strings.Contains(m[0], "not reaching ClickHouse (engine): 120 lost") {
		t.Fatalf("a loss must alert once: %q", m)
	}
	line, ok := LossNotice(e.status(), now)
	if !ok || !strings.HasSuffix(line, "event loss 150 (engine)") {
		t.Fatalf("notice = %q %v", line, ok)
	}

	now = now.Add(a.RecoverAfter + time.Minute)
	put(config.LostEvents{Queue: 150})
	check()
	check()
	if m := e.messages(); len(m) != 2 || !strings.Contains(m[1], "written again") || !strings.Contains(m[1], "150") {
		t.Fatalf("recovery must be announced once: %q", m)
	}
	if _, ok := LossNotice(e.status(), now); ok {
		t.Fatal("no notice after recovery")
	}

	// The engine restarts without losing anything: no alert.
	now = now.Add(time.Minute)
	started = now
	put(config.LostEvents{})
	check()
	if m := e.messages(); len(m) != 2 {
		t.Fatalf("a restart without losses must not alert: %q", m)
	}
	if st := e.status(); st.Loss == nil || st.Loss.Alerted {
		t.Fatalf("state after the restart: %+v", st.Loss)
	}

	// The restarted process then loses events: that is a loss.
	now = now.Add(time.Minute)
	put(config.LostEvents{Queue: 2})
	check()
	if m := e.messages(); len(m) != 3 || !strings.Contains(m[2], "2 lost") {
		t.Fatalf("a loss after the restart must alert: %q", m)
	}
}

// TestLossCheckSchedule checks that Tick runs the check at most every 30
// seconds and not at all until WatchLoss turns it on.
func TestLossCheckSchedule(t *testing.T) {
	e := newEnv(t)
	now := lossT0
	e.r.now = func() time.Time { return now }
	metrics := t.TempDir()
	e.r.Tick(context.Background())
	e.r.Wait()
	if e.status().Loss != nil {
		t.Fatal("not watched, no check")
	}
	a := testLossAlert
	a.Dir = metrics
	e.r.WatchLoss(a)
	e.r.Tick(context.Background())
	e.r.Wait()
	first := e.status().Loss
	if first == nil || !first.CheckedAt.Equal(now) {
		t.Fatalf("first check: %+v", first)
	}
	now = now.Add(lossCheckEvery - time.Second)
	e.r.Tick(context.Background())
	e.r.Wait()
	if got := e.status().Loss; !got.CheckedAt.Equal(first.CheckedAt) {
		t.Fatalf("a second check inside the period: %+v", got)
	}
	now = now.Add(time.Second)
	e.r.Tick(context.Background())
	e.r.Wait()
	if got := e.status().Loss; !got.CheckedAt.Equal(now) {
		t.Fatalf("check after the period: %+v", got)
	}
}
