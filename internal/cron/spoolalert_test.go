package cron

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

// spoolEngine builds the metrics of a healthy engine with a disk queue: ClickHouse
// connected, the table deduplicating, the queue enabled and empty. mutate changes
// the Events part before it is returned.
func spoolEngine(name string, updated time.Time, mutate func(*config.EventMetrics)) config.EngineMetrics {
	m := config.EngineMetrics{Instance: name, StartedAt: lossT0.Add(-time.Hour), UpdatedAt: updated}
	m.Events = config.EventMetrics{Configured: true, Connected: true, DedupReady: true, Spool: config.SpoolMetrics{Enabled: true}}
	if mutate != nil {
		mutate(&m.Events)
	}
	return m
}

// TestSpoolBacklogIsAlertedOnlyWhenConnectedAndOld checks that a queue draining
// while ClickHouse is away is silent, and a queue that stays with ClickHouse
// connected for over ten minutes raises, then clears after the quiet time.
func TestSpoolBacklogIsAlertedOnlyWhenConnectedAndOld(t *testing.T) {
	a := testLossAlert
	now := lossT0
	aged := func(age time.Duration, connected bool) []config.EngineMetrics {
		return []config.EngineMetrics{spoolEngine("engine", now, func(e *config.EventMetrics) {
			e.Connected = connected
			e.Spool.Segments = 3
			e.Spool.OldestAgeSeconds = int64(age / time.Second)
		})}
	}
	st, v := decideSpool(nil, aged(time.Hour, false), a, now)
	if v != alertQuiet || st.Alerted {
		t.Fatalf("an outage parks events on purpose: %+v %v", st, v)
	}
	now = now.Add(time.Minute)
	if st, v = decideSpool(&st, aged(9*time.Minute, true), a, now); v != alertQuiet || st.Alerted {
		t.Fatalf("a young queue must stay silent: %+v %v", st, v)
	}
	now = now.Add(time.Minute)
	st, v = decideSpool(&st, aged(11*time.Minute, true), a, now)
	if v != alertRaise || !st.Alerted || len(st.Reasons) != 1 || st.Reasons[0] != spoolReasonBacklog {
		t.Fatalf("a stuck queue must raise: %+v %v", st, v)
	}
	if msg := spoolMessage(v, st, "en"); !strings.Contains(msg, "more than 10 minutes") || strings.Contains(msg, "%!") {
		t.Fatalf("message = %q", msg)
	}
	now = now.Add(time.Minute)
	if st, v = decideSpool(&st, aged(12*time.Minute, true), a, now); v != alertQuiet || !st.Alerted {
		t.Fatalf("the alert must not repeat at once: %+v %v", st, v)
	}

	now = now.Add(time.Minute)
	healthy := []config.EngineMetrics{spoolEngine("engine", now, nil)}
	if st, v = decideSpool(&st, healthy, a, now); v != alertQuiet || !st.Alerted {
		t.Fatalf("the alert holds until the quiet time ends: %+v %v", st, v)
	}
	now = now.Add(a.RecoverAfter)
	healthy = []config.EngineMetrics{spoolEngine("engine", now, nil)}
	st, v = decideSpool(&st, healthy, a, now)
	if v != alertClear || st.Alerted {
		t.Fatalf("the alert must clear: %+v %v", st, v)
	}
	if msg := spoolMessage(v, st, "ru"); !strings.Contains(msg, "снова в порядке") {
		t.Fatalf("clear message = %q", msg)
	}
}

// TestSpoolRejectedGrowthAgainstTheBase checks that a first sight only sets the
// base, growth raises, a lower count (the directory was cleaned) lowers the base
// without an alert, and a restart keeps the base.
func TestSpoolRejectedGrowthAgainstTheBase(t *testing.T) {
	a := testLossAlert
	now := lossT0
	with := func(n int64) []config.EngineMetrics {
		return []config.EngineMetrics{spoolEngine("engine", now, func(e *config.EventMetrics) { e.Spool.Rejected = n })}
	}
	st, v := decideSpool(nil, with(4), a, now)
	if v != alertQuiet || st.Alerted || st.Bases["engine"].Rejected != 4 {
		t.Fatalf("rejected files from before must only set the base: %+v %v", st, v)
	}
	now = now.Add(30 * time.Second)
	if st, v = decideSpool(&st, with(4), a, now); v != alertQuiet {
		t.Fatalf("no growth, no alert: %v", v)
	}
	now = now.Add(30 * time.Second)
	st, v = decideSpool(&st, with(5), a, now)
	if v != alertRaise || len(st.Reasons) != 1 || st.Reasons[0] != spoolReasonRejected || st.Bases["engine"].Rejected != 5 {
		t.Fatalf("growth must raise: %+v %v", st, v)
	}
	now = now.Add(a.RecoverAfter + time.Minute)
	if st, v = decideSpool(&st, with(5), a, now); v != alertQuiet || !st.Alerted {
		t.Fatalf("calm needs a window without growth: %+v %v", st, v)
	}
	now = now.Add(a.RecoverAfter)
	if st, v = decideSpool(&st, with(0), a, now); v != alertClear {
		t.Fatalf("a cleaned directory is not growth and clears after the window: %+v %v", st, v)
	}
	if st.Bases["engine"].Rejected != 0 {
		t.Fatalf("the base must follow the lower count: %+v", st.Bases)
	}
}

// TestSpoolSafetyNetsNeedAMinute checks that a missing dedup or a missing disk
// queue is only alerted once it has lasted lossFailingAfter, and that a single
// bad file does not hide a healthy one.
func TestSpoolSafetyNetsNeedAMinute(t *testing.T) {
	a := testLossAlert
	now := lossT0
	bad := func(mutate func(*config.EventMetrics)) []config.EngineMetrics {
		return []config.EngineMetrics{spoolEngine("engine", now, mutate), spoolEngine("engine2", now, nil)}
	}
	cases := []struct {
		name   string
		mutate func(*config.EventMetrics)
		reason string
	}{
		{"nodedup", func(e *config.EventMetrics) { e.DedupReady = false }, spoolReasonNoDedup},
		{"nospool", func(e *config.EventMetrics) { e.Spool = config.SpoolMetrics{} }, spoolReasonNoSpool},
	}
	for _, c := range cases {
		now = lossT0
		st, v := decideSpool(nil, bad(c.mutate), a, now)
		if v != alertQuiet || st.Alerted {
			t.Fatalf("%s: a fresh sight must wait: %+v %v", c.name, st, v)
		}
		now = now.Add(lossFailingAfter - time.Second)
		if st, v = decideSpool(&st, bad(c.mutate), a, now); v != alertQuiet || st.Alerted || !st.QuietSince.IsZero() {
			t.Fatalf("%s: under a minute: %+v %v", c.name, st, v)
		}
		now = now.Add(time.Second)
		st, v = decideSpool(&st, bad(c.mutate), a, now)
		if v != alertRaise || len(st.Reasons) != 1 || st.Reasons[0] != c.reason || len(st.Instances) != 1 || st.Instances[0] != "engine" {
			t.Fatalf("%s: after a minute: %+v %v", c.name, st, v)
		}
		if msg := spoolMessage(v, st, "uk"); strings.Contains(msg, "%!") || !strings.Contains(msg, "engine") {
			t.Fatalf("%s: message = %q", c.name, msg)
		}
	}
}

// TestSpoolIgnoresWhatIsNotConfigured checks that an engine without ClickHouse,
// or with ClickHouse down, raises neither the no-dedup nor the no-spool alert,
// and that a stale file or no file at all is neither hot nor calm.
func TestSpoolIgnoresWhatIsNotConfigured(t *testing.T) {
	a := testLossAlert
	now := lossT0
	none := config.EngineMetrics{Instance: "engine", UpdatedAt: now}
	down := spoolEngine("engine", now, func(e *config.EventMetrics) { e.Connected, e.DedupReady = false, false })
	st, _ := decideSpool(nil, []config.EngineMetrics{none}, a, now)
	now = now.Add(5 * time.Minute)
	none.UpdatedAt, down.UpdatedAt = now, now
	if st, v := decideSpool(&st, []config.EngineMetrics{none}, a, now); v != alertQuiet || st.Alerted {
		t.Fatalf("an engine without ClickHouse: %+v %v", st, v)
	}
	if st, v := decideSpool(&st, []config.EngineMetrics{down}, a, now); v != alertQuiet || st.Alerted || len(st.Reasons) != 0 {
		t.Fatalf("ClickHouse down is the loss alert's business: %+v %v", st, v)
	}
	if st2, v := decideSpool(&st, nil, a, now); v != alertQuiet || !st2.QuietSince.IsZero() {
		t.Fatalf("no file restarts the wait: %+v %v", st2, v)
	}
}

// TestSpoolNotice checks the admin line and when it is hidden.
func TestSpoolNotice(t *testing.T) {
	st := SpoolState{CheckedAt: lossT0, Alerted: true, Reasons: []string{spoolReasonBacklog, spoolReasonNoDedup}, Instances: []string{"engine", "engine2"}}
	line, ok := SpoolNotice(Status{Spool: &st}, lossT0)
	if !ok || line != "2026-11-16T12:00:00Z event spool backlog,nodedup (engine,engine2)" {
		t.Fatalf("notice = %q %v", line, ok)
	}
	if _, ok := SpoolNotice(Status{Spool: &st}, lossT0.Add(LossStateFresh+time.Minute)); ok {
		t.Error("a stale check must not raise the notice")
	}
	if _, ok := SpoolNotice(Status{}, lossT0); ok {
		t.Error("no notice before the first check")
	}
	st.Alerted = false
	if _, ok := SpoolNotice(Status{Spool: &st}, lossT0); ok {
		t.Error("no notice without an alert")
	}
}

// TestSpoolMessagesInEveryLanguage checks that every language has a text for
// every reason, and that raise and clear are complete sentences.
func TestSpoolMessagesInEveryLanguage(t *testing.T) {
	all := spoolReasons
	st := SpoolState{Reasons: all, Instances: []string{"engine"}}
	for lang, text := range spoolTexts {
		for _, r := range all {
			if text.reason[r] == "" {
				t.Errorf("%s: no text for %s", lang, r)
			}
		}
		raise := spoolMessage(alertRaise, st, lang)
		if raise == "" || strings.Contains(raise, "%!") || spoolMessage(alertRemind, st, lang) != raise {
			t.Errorf("%s: raise = %q", lang, raise)
		}
		if clear := spoolMessage(alertClear, st, lang); clear == "" || strings.Contains(clear, "%!") {
			t.Errorf("%s: clear = %q", lang, clear)
		}
	}
	if spoolMessage(alertQuiet, st, "en") != "" {
		t.Error("a quiet check says nothing")
	}
}

// TestSpoolCheckSendsOnceAndKeepsTheLossAlertApart drives the runner over real
// files: a stuck queue gives one Telegram message and one notice, a repeated
// check none, and recovery one message; the loss state is untouched.
func TestSpoolCheckSendsOnceAndKeepsTheLossAlertApart(t *testing.T) {
	e := newEnv(t)
	now := lossT0
	e.r.now = func() time.Time { return now }
	dir := t.TempDir()
	a := testLossAlert
	a.Dir = dir
	e.r.WatchLoss(a)
	check := func() { e.r.checkLoss(context.Background(), tg) }
	put := func(age time.Duration) {
		m := spoolEngine("engine", now, func(ev *config.EventMetrics) {
			if age > 0 {
				ev.Spool.Segments, ev.Spool.OldestAgeSeconds = 2, int64(age/time.Second)
			}
		})
		writeEngineFile(t, dir, "engine", m)
	}

	put(0)
	check()
	now = now.Add(time.Minute)
	put(15 * time.Minute)
	check()
	check()
	if m := e.messages(); len(m) != 1 || !strings.Contains(m[0], "disk queue") {
		t.Fatalf("a stuck queue must alert once: %q", m)
	}
	st := e.status()
	if line, ok := SpoolNotice(st, now); !ok || !strings.HasSuffix(line, "event spool backlog (engine)") {
		t.Fatalf("notice = %q %v", line, ok)
	}
	if _, ok := LossNotice(st, now); ok {
		t.Fatal("the loss alert must not follow the spool alert")
	}

	now = now.Add(a.RecoverAfter + time.Minute)
	put(0)
	check()
	now = now.Add(a.RecoverAfter)
	put(0)
	check()
	check()
	if m := e.messages(); len(m) != 2 || !strings.Contains(m[1], "in order again") {
		t.Fatalf("recovery must be announced once: %q", m)
	}
	if _, ok := SpoolNotice(e.status(), now); ok {
		t.Fatal("no notice after recovery")
	}
}

// pbCheck runs decideSpool once over one healthy engine whose metrics mutate
// changes, and returns the state and the verdict. started is the process start.
func pbCheck(prev *SpoolState, started, now time.Time, mutate func(*config.EngineMetrics)) (SpoolState, alertVerdict) {
	m := spoolEngine("engine", now, nil)
	m.StartedAt = started
	m.Spool.Postbacks.Enabled, m.Spool.Outbound.Enabled = true, true
	if mutate != nil {
		mutate(&m)
	}
	return decideSpool(prev, []config.EngineMetrics{m}, testLossAlert, now)
}

// TestPostbackLossCountersRaiseOnGrowth checks that growth of the lost journal
// lines, the dropped outbound jobs and the written-off postbacks each raises with
// its own reason, that a first sight and an unchanged counter stay silent, and
// that the alert is announced once.
func TestPostbackLossCountersRaiseOnGrowth(t *testing.T) {
	started := lossT0.Add(-time.Hour)
	for _, c := range []struct {
		name   string
		reason string
		set    func(*config.EngineMetrics, int64)
	}{
		{"journal", spoolReasonPostbackLog, func(m *config.EngineMetrics, n int64) { m.Spool.PostbackLogLost = n }},
		{"outbound", spoolReasonOutboundDrop, func(m *config.EngineMetrics, n int64) { m.Outbound.Dropped = n }},
		{"expired", spoolReasonRetryExpired, func(m *config.EngineMetrics, n int64) { m.Postbacks.RetryExpired = n }},
	} {
		now := lossT0
		st, v := pbCheck(nil, started, now, func(m *config.EngineMetrics) { c.set(m, 7) })
		if v != alertQuiet || st.Alerted {
			t.Fatalf("%s: a first sight with old losses raised: %+v %v", c.name, st, v)
		}
		now = now.Add(time.Minute)
		if st, v = pbCheck(&st, started, now, func(m *config.EngineMetrics) { c.set(m, 7) }); v != alertQuiet {
			t.Fatalf("%s: an unchanged counter raised: %+v %v", c.name, st, v)
		}
		now = now.Add(time.Minute)
		st, v = pbCheck(&st, started, now, func(m *config.EngineMetrics) { c.set(m, 8) })
		if v != alertRaise || len(st.Reasons) != 1 || st.Reasons[0] != c.reason {
			t.Fatalf("%s: growth must raise with %s: %+v %v", c.name, c.reason, st, v)
		}
		now = now.Add(time.Minute)
		if st, v = pbCheck(&st, started, now, func(m *config.EngineMetrics) { c.set(m, 8) }); v != alertQuiet || !st.Alerted {
			t.Fatalf("%s: the raised alert must not repeat at once: %+v %v", c.name, st, v)
		}
		now = now.Add(testLossAlert.RecoverAfter + time.Minute)
		pbCheck(&st, started, now, func(m *config.EngineMetrics) { c.set(m, 8) })
		now = now.Add(testLossAlert.RecoverAfter)
		if st, v = pbCheck(&st, started, now, func(m *config.EngineMetrics) { c.set(m, 8) }); v != alertClear || st.Alerted {
			t.Fatalf("%s: the alert must clear after the quiet time: %+v %v", c.name, st, v)
		}
	}
}

// TestPostbackLossCountersAcrossRestart checks that a restart of the engine is
// not an alarm by itself (counters start from zero again), that what the old
// process lost after the last check comes once through Previous, and that losses
// of the new process count from zero.
func TestPostbackLossCountersAcrossRestart(t *testing.T) {
	old := lossT0.Add(-time.Hour)
	fresh := lossT0.Add(time.Minute)
	now := lossT0
	st, _ := pbCheck(nil, old, now, func(m *config.EngineMetrics) {
		m.Spool.PostbackLogLost, m.Outbound.Dropped, m.Postbacks.RetryExpired = 4, 4, 4
	})
	now = now.Add(2 * time.Minute)
	restarted := func(prev config.QueueDrops, cur config.QueueDrops) func(*config.EngineMetrics) {
		return func(m *config.EngineMetrics) {
			m.Spool.PostbackLogLost, m.Outbound.Dropped, m.Postbacks.RetryExpired = cur.PostbackLog, cur.Outbound, cur.RetryExpired
			m.Previous = &config.PreviousRun{StartedAt: old, Drops: prev}
		}
	}
	quiet, v := pbCheck(&st, fresh, now, restarted(config.QueueDrops{PostbackLog: 4, Outbound: 4, RetryExpired: 4}, config.QueueDrops{}))
	if v != alertQuiet || quiet.Alerted {
		t.Fatalf("a restart with nothing lost raised: %+v %v", quiet, v)
	}
	tail, v := pbCheck(&st, fresh, now, restarted(config.QueueDrops{PostbackLog: 4, Outbound: 6, RetryExpired: 4}, config.QueueDrops{}))
	if v != alertRaise || len(tail.Reasons) != 1 || tail.Reasons[0] != spoolReasonOutboundDrop {
		t.Fatalf("the tail of the old process must raise once: %+v %v", tail, v)
	}
	again, v := pbCheck(&tail, fresh, now.Add(time.Minute), restarted(config.QueueDrops{PostbackLog: 4, Outbound: 6, RetryExpired: 4}, config.QueueDrops{}))
	if v != alertQuiet || !again.Alerted {
		t.Fatalf("the tail must be counted once: %+v %v", again, v)
	}
	fromZero, v := pbCheck(&quiet, fresh, now.Add(time.Minute), restarted(config.QueueDrops{PostbackLog: 4, Outbound: 4, RetryExpired: 4}, config.QueueDrops{PostbackLog: 1}))
	if v != alertRaise || len(fromZero.Reasons) != 1 || fromZero.Reasons[0] != spoolReasonPostbackLog {
		t.Fatalf("a loss of the new process must raise: %+v %v", fromZero, v)
	}
	unseen, v := pbCheck(&quiet, fresh.Add(time.Hour), now.Add(time.Hour), restarted(config.QueueDrops{PostbackLog: 9}, config.QueueDrops{}))
	if v != alertQuiet {
		t.Fatalf("a Previous older than the last check's process adds nothing: %+v %v", unseen, v)
	}
}

// TestPostbackQueueStuckAndRejected checks the queue reasons: an old segment in
// the outbound queue raises at once, one in the postback queue only while
// ClickHouse is connected, growth of a rejected directory raises, a first sight
// and a base written before these figures existed raise nothing.
func TestPostbackQueueStuckAndRejected(t *testing.T) {
	started := lossT0.Add(-time.Hour)
	old := int64(11 * 60)
	now := lossT0
	if st, v := pbCheck(nil, started, now, func(m *config.EngineMetrics) { m.Spool.Outbound.Segments, m.Spool.Outbound.OldestAgeSeconds = 2, old }); v != alertRaise || st.Reasons[0] != spoolReasonPostbackQ {
		t.Fatalf("a stuck outbound queue must raise: %+v %v", st, v)
	}
	if st, v := pbCheck(nil, started, now, func(m *config.EngineMetrics) { m.Spool.Outbound.Segments, m.Spool.Outbound.OldestAgeSeconds = 2, 9*60 }); v != alertQuiet {
		t.Fatalf("a young outbound queue raised: %+v %v", st, v)
	}
	parked := func(connected bool) func(*config.EngineMetrics) {
		return func(m *config.EngineMetrics) {
			m.Events.Connected = connected
			m.Spool.Postbacks.Segments, m.Spool.Postbacks.OldestAgeSeconds = 3, old
		}
	}
	if st, v := pbCheck(nil, started, now, parked(false)); v != alertQuiet {
		t.Fatalf("postbacks waiting for a store that is away raised: %+v %v", st, v)
	}
	if st, v := pbCheck(nil, started, now, parked(true)); v != alertRaise {
		t.Fatalf("postbacks stuck with a connected store must raise: %+v %v", st, v)
	}

	first, v := pbCheck(nil, started, now, func(m *config.EngineMetrics) { m.Spool.Postbacks.Rejected, m.Spool.Outbound.Rejected = 3, 2 })
	if v != alertQuiet {
		t.Fatalf("rejected files found at the first sight raised: %+v %v", first, v)
	}
	now = now.Add(time.Minute)
	if _, v = pbCheck(&first, started, now, func(m *config.EngineMetrics) { m.Spool.Postbacks.Rejected, m.Spool.Outbound.Rejected = 3, 2 }); v != alertQuiet {
		t.Fatalf("an unchanged rejected count raised: %v", v)
	}
	for _, grow := range []func(*config.EngineMetrics){
		func(m *config.EngineMetrics) { m.Spool.Postbacks.Rejected, m.Spool.Outbound.Rejected = 4, 2 },
		func(m *config.EngineMetrics) { m.Spool.Postbacks.Rejected, m.Spool.Outbound.Rejected = 3, 3 },
	} {
		if st, v := pbCheck(&first, started, now, grow); v != alertRaise || st.Reasons[0] != spoolReasonPostbackQ {
			t.Fatalf("a growing rejected directory must raise: %+v %v", st, v)
		}
	}
	legacy := &SpoolState{Bases: map[string]SpoolBase{"engine": {Rejected: 0}}}
	if st, v := pbCheck(legacy, started, now, func(m *config.EngineMetrics) {
		m.Spool.Postbacks.Rejected, m.Outbound.Dropped = 5, 5
	}); v != alertQuiet {
		t.Fatalf("a base without the new figures raised: %+v %v", st, v)
	}
}
