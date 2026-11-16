package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// Event loss alert. Every engine writes its metrics to a file; the cron service
// reads them all and tells Telegram and the admin panel when click events stop
// reaching ClickHouse. Cron is the only sender.
const (
	// DefaultLossAlertRepeat is the reminder period while the alert stays raised.
	DefaultLossAlertRepeat = 6 * time.Hour
	// DefaultLossRecoverAfter is how long events must be written without a new
	// loss before the alert is cleared.
	DefaultLossRecoverAfter = 10 * time.Minute
	// LossStale is how old a metrics file may be and still count. An older one
	// belongs to a stopped engine: its numbers are ignored, and an alert that is
	// already raised stays raised until a fresh engine has been healthy for
	// DefaultLossRecoverAfter, so a dead engine never clears it silently.
	LossStale = 5 * time.Minute
	// LossStateFresh is how old a check may be and still raise the admin notice;
	// an older one means the service has stopped checking.
	LossStateFresh = 10 * time.Minute
	// lossFailingAfter is how long inserts may keep failing before the alert is
	// raised even though no loss has been counted yet.
	lossFailingAfter = 60 * time.Second
	// lossCheckEvery is the period of the metrics check.
	lossCheckEvery = 30 * time.Second
	// lossFileMax caps the size of one metrics file read.
	lossFileMax = 1 << 20
	// LossSilentAfter is how long no engine may report before the cron log gets a
	// warning; no Telegram message is sent for it.
	LossSilentAfter = 10 * time.Minute
	// lossSilentWarnEvery is the period of that warning while the silence goes on.
	lossSilentWarnEvery = time.Hour
)

// LossAlert configures the event loss alert.
// Dir holds the engine metrics files. Repeat reminds while the alert stays
// raised; 0 never reminds. RecoverAfter is the quiet time that clears the alert.
// Lang is the Telegram language: ru, uk or en.
type LossAlert struct {
	Dir          string
	Repeat       time.Duration
	RecoverAfter time.Duration
	Lang         string
}

// LossAlertFromEnv returns the alert settings: the metrics directory from
// EMTDS_METRICS_DIR (default /var/lib/emtds/metrics) and the language from
// EMTDS_ALERT_LANG (ru, uk or en, default en). The reminder period and the
// recovery time are the defaults above. It does not fail.
func LossAlertFromEnv() LossAlert {
	a := LossAlert{Dir: config.MetricsDir(), Repeat: DefaultLossAlertRepeat, RecoverAfter: DefaultLossRecoverAfter, Lang: "en"}
	if l := strings.ToLower(strings.TrimSpace(config.Env("EMTDS_ALERT_LANG"))); lossTexts[l] != nil {
		a.Lang = l
	}
	return a
}

// LossBase is what the last check knew about one engine instance: the process
// it was (StartedAt) and its loss counters then. The next check counts the
// difference against them while StartedAt is the same. DownSince is when the
// process was first seen configured for ClickHouse but not connected; it is zero
// while connected.
type LossBase struct {
	StartedAt time.Time         `json:"started_at"`
	Lost      config.LostEvents `json:"lost"`
	DownSince time.Time         `json:"down_since,omitempty"`
}

// LossState is the last event loss check, kept in the status file.
// Alerted stays true from the alert until events have been written without a new
// loss for RecoverAfter. QuietSince is when the checks last became healthy, with
// a fresh file, no new loss and no failing insert streak; it is zero while the
// trouble goes on and while no engine reports. Lost is the number of events lost
// since the alert was raised; it stays readable after the alert clears, until the
// next one. Instances are the engines named in the last check that saw trouble.
// Bases holds the counters of each instance seen. SilentSince is when the checks
// first found no fresh metrics file (zero while one exists) and SilentWarnedAt
// the last time that silence was written to the log.
type LossState struct {
	CheckedAt      time.Time           `json:"checked_at"`
	Alerted        bool                `json:"alerted"`
	AlertedAt      time.Time           `json:"alerted_at,omitempty"`
	QuietSince     time.Time           `json:"quiet_since,omitempty"`
	Lost           int64               `json:"lost"`
	Instances      []string            `json:"instances,omitempty"`
	Bases          map[string]LossBase `json:"bases,omitempty"`
	SilentSince    time.Time           `json:"silent_since,omitempty"`
	SilentWarnedAt time.Time           `json:"silent_warned_at,omitempty"`
}

// silentWarnDue reports whether the check that produced s asks for the "no
// metrics" warning in the log.
func (s LossState) silentWarnDue() bool {
	return !s.SilentWarnedAt.IsZero() && s.SilentWarnedAt.Equal(s.CheckedAt)
}

// diffLost returns cur minus base per cause. A cause that went down is treated
// as no change: the counters only grow inside one process.
func diffLost(cur, base config.LostEvents) config.LostEvents {
	d := func(c, b int64) int64 { return max(0, c-b) }
	return config.LostEvents{Full: d(cur.Full, base.Full), Queue: d(cur.Queue, base.Queue), Insert: d(cur.Insert, base.Insert), Late: d(cur.Late, base.Late), SpoolFull: d(cur.SpoolFull, base.SpoolFull)}
}

// instanceLoss returns the events one fresh instance lost since base, the state
// of the last check. With the same StartedAt it is the growth of the counters.
// After a restart it is everything the new process has counted (its base is
// zero) plus the tail of the process before it: m.Previous against base when
// base is that very process, or all of Previous when the check never saw it.
// A Previous older than base was counted before and adds nothing. It does not fail.
func instanceLoss(base LossBase, m config.EngineMetrics) int64 {
	if base.StartedAt.Equal(m.StartedAt) {
		return diffLost(m.Events.Lost, base.Lost).Total()
	}
	n := m.Events.Lost.Total()
	if p := m.Previous; p != nil && !p.StartedAt.Before(base.StartedAt) {
		var seen config.LostEvents
		if p.StartedAt.Equal(base.StartedAt) {
			seen = base.Lost
		}
		n += diffLost(p.Lost, seen).Total()
	}
	return n
}

// decideLoss computes the state after one check and what to send.
// prev is the last state, nil before the first check. metrics is every file read
// (Instance is the file name); now is the time of the check.
//
// Per instance, a file older than LossStale is ignored and its base is kept. For
// a fresh file, an instance never seen before only sets its base from the
// current counters. A known instance with the same StartedAt loses what its
// counters grew by since the base. A known instance with a new StartedAt (the
// engine restarted) starts from a zero base: everything the new process counts
// was lost after its start. The tail of the old process comes in the file as
// Previous (see instanceLoss) and is counted once, because the base then moves
// on to the new process.
//
// The check is hot when any instance lost events, has had inserts failing for
// at least a minute (FailingSince against UpdatedAt) or has been configured for
// ClickHouse without a connection for at least a minute (DownSince against
// UpdatedAt). It is quiet when at least one instance reported and no instance
// lost events, has a failing insert streak or is disconnected; QuietSince keeps
// when the checks became quiet. A check with no fresh file for LossSilentAfter
// is reported through SilentWarnedAt. The state is calm when
// it has been quiet for a.RecoverAfter. A check with no fresh file is neither
// hot nor quiet and restarts the wait, so a raised alert stays raised while the
// engine is dead and clears only after a live engine has been healthy for the
// whole window. decideAlert turns hot and calm into raise, remind or clear.
// Bases of instances without a file are dropped. It does not fail.
func decideLoss(prev *LossState, metrics []config.EngineMetrics, a LossAlert, now time.Time) (LossState, alertVerdict) {
	next := LossState{CheckedAt: now, Bases: map[string]LossBase{}}
	var old map[string]LossBase
	flag := alertFlag{}
	if prev != nil {
		old = prev.Bases
		flag = alertFlag{On: prev.Alerted, At: prev.AlertedAt}
		next.QuietSince, next.Lost, next.Instances = prev.QuietSince, prev.Lost, prev.Instances
		next.SilentSince, next.SilentWarnedAt = prev.SilentSince, prev.SilentWarnedAt
	}
	var lost int64
	var hit []string
	fresh, failing := 0, false
	for _, m := range metrics {
		name := m.Instance
		base, known := old[name]
		if now.Sub(m.UpdatedAt) > LossStale {
			if known {
				next.Bases[name] = base
			}
			continue
		}
		fresh++
		bad := false
		cur := LossBase{StartedAt: m.StartedAt, Lost: m.Events.Lost}
		if known {
			if n := instanceLoss(base, m); n > 0 {
				lost += n
				bad = true
			}
		}
		if f := m.Events.FailingSince; f != nil {
			failing = true
			if m.UpdatedAt.Sub(*f) >= lossFailingAfter {
				bad = true
			}
		}
		if m.Events.Configured && !m.Events.Connected {
			failing = true
			cur.DownSince = m.UpdatedAt
			if known && base.StartedAt.Equal(m.StartedAt) && !base.DownSince.IsZero() {
				cur.DownSince = base.DownSince
			}
			if m.UpdatedAt.Sub(cur.DownSince) >= lossFailingAfter {
				bad = true
			}
		}
		next.Bases[name] = cur
		if bad {
			hit = append(hit, name)
		}
	}
	switch {
	case fresh > 0:
		next.SilentSince, next.SilentWarnedAt = time.Time{}, time.Time{}
	case next.SilentSince.IsZero():
		next.SilentSince = now
	case now.Sub(next.SilentSince) >= LossSilentAfter && (next.SilentWarnedAt.IsZero() || now.Sub(next.SilentWarnedAt) >= lossSilentWarnEvery):
		next.SilentWarnedAt = now
	}
	sort.Strings(hit)
	hot := len(hit) > 0
	if hot {
		next.Instances = hit
	}
	switch {
	case fresh == 0 || hot || failing:
		next.QuietSince = time.Time{}
	case next.QuietSince.IsZero():
		next.QuietSince = now
	}
	calm := !next.QuietSince.IsZero() && now.Sub(next.QuietSince) >= a.RecoverAfter
	flag, v := decideAlert(flag, hot, calm, a.Repeat, now)
	next.Alerted, next.AlertedAt = flag.On, flag.At
	switch {
	case v == alertRaise:
		next.Lost = lost
	case hot:
		next.Lost += lost
	}
	return next, v
}

// lossText is one language of the Telegram messages.
// raise and clear take the instances and the number lost; raiseNone and
// clearNone take only the instances and speak when nothing is counted as lost
// yet: inserts fail or ClickHouse is not connected.
type lossText struct{ raise, clear, raiseNone, clearNone string }

// lossTexts holds the Telegram messages by language.
var lossTexts = map[string]*lossText{
	"en": {
		raise: "⚠️ EmTDS: click events are not reaching ClickHouse (%s): %d lost so far. Visitors still get their answers, " +
			"but the clicks are not recorded and postbacks cannot find them. Check ClickHouse and the engine log.",
		clear: "✅ EmTDS: click events are written again (%s). Lost during the incident: %d.",
		raiseNone: "⚠️ EmTDS: click events cannot be written to ClickHouse (%s): inserts fail or there is no connection. " +
			"Nothing is counted as lost yet: events wait in the disk queue and are lost only when it reaches its size ceiling. " +
			"Check ClickHouse and the engine log.",
		clearNone: "✅ EmTDS: click events are written again (%s). Nothing was lost.",
	},
	"ru": {
		raise: "⚠️ EmTDS: события кликов не доходят до ClickHouse (%s): потеряно %d. Посетители получают ответ, " +
			"но клики не записываются, и постбэки их не находят. Проверьте ClickHouse и журнал engine.",
		clear: "✅ EmTDS: события кликов снова записываются (%s). Потеряно за время сбоя: %d.",
		raiseNone: "⚠️ EmTDS: события кликов не удаётся записать в ClickHouse (%s): вставка не проходит или нет соединения. " +
			"Потерь пока не насчитано: события ждут в очереди на диске и пропадут, только когда она дойдёт до потолка по объёму. " +
			"Проверьте ClickHouse и журнал engine.",
		clearNone: "✅ EmTDS: события кликов снова записываются (%s). Потерь не было.",
	},
	"uk": {
		raise: "⚠️ EmTDS: події кліків не доходять до ClickHouse (%s): втрачено %d. Відвідувачі отримують відповідь, " +
			"але кліки не записуються, і постбеки їх не знаходять. Перевірте ClickHouse і журнал engine.",
		clear: "✅ EmTDS: події кліків знову записуються (%s). Втрачено за час збою: %d.",
		raiseNone: "⚠️ EmTDS: події кліків не вдається записати в ClickHouse (%s): вставка не проходить або немає з'єднання. " +
			"Втрат поки не пораховано: події чекають у черзі на диску й зникнуть, лише коли вона досягне стелі за обсягом. " +
			"Перевірте ClickHouse і журнал engine.",
		clearNone: "✅ EmTDS: події кліків знову записуються (%s). Втрат не було.",
	},
}

// lossMessage returns the Telegram text for v in lang, or "" for alertQuiet.
// st supplies the instances and the count; with a count of 0 the text names the
// failing inserts or the missing connection instead of a number. An unknown lang
// is English. It does not fail.
func lossMessage(v alertVerdict, st LossState, lang string) string {
	t := lossTexts[lang]
	if t == nil {
		t = lossTexts["en"]
	}
	who := strings.Join(st.Instances, ", ")
	none := st.Lost == 0
	switch {
	case v == alertRaise || v == alertRemind:
		if none {
			return fmt.Sprintf(t.raiseNone, who)
		}
		return fmt.Sprintf(t.raise, who, st.Lost)
	case v == alertClear:
		if none {
			return fmt.Sprintf(t.clearNone, who)
		}
		return fmt.Sprintf(t.clear, who, st.Lost)
	}
	return ""
}

// LossNotice returns the admin notice line for a raised event loss alert.
// st is the cron status and now the current time. ok is false when no alert is
// raised or the last check is older than LossStateFresh. The line is
// "<RFC3339 time> event loss <N> (<instances>)", N being the events lost since
// the alert and the instances comma-separated; the admin panel parses it for its banner.
func LossNotice(st Status, now time.Time) (line string, ok bool) {
	l := st.Loss
	if l == nil || !l.Alerted || now.Sub(l.CheckedAt) > LossStateFresh {
		return "", false
	}
	return fmt.Sprintf("%s event loss %d (%s)", l.CheckedAt.UTC().Format(time.RFC3339), l.Lost, strings.Join(l.Instances, ",")), true
}

// readEngineMetrics reads every <instance>.json in dir. The instance name is the
// file name; a name outside [a-z0-9-], a file over 1 MiB and a file that is not
// a metrics JSON are skipped. A missing directory is an empty list. Any other
// failure to list the directory is returned.
func readEngineMetrics(dir string) ([]config.EngineMetrics, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []config.EngineMetrics
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !config.ValidInstance(name) {
			continue
		}
		if fi, err := e.Info(); err != nil || fi.Size() > lossFileMax {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m config.EngineMetrics
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		m.Instance = name
		out = append(out, m)
	}
	return out, nil
}

// WatchLoss turns on the event loss check. a sets the metrics directory and the
// thresholds. Call it before Run.
func (r *Runner) WatchLoss(a LossAlert) {
	r.lossOn, r.lossAlert = true, a
}

// maybeCheckLoss starts one metrics check when it is due and none is running.
// t is where alerts go. The check runs in its own goroutine.
func (r *Runner) maybeCheckLoss(ctx context.Context, t Telegram) {
	if !r.lossOn {
		return
	}
	r.launchCheck(&r.lossNext, &r.lossBusy, lossCheckEvery, func() { r.checkLoss(ctx, t) })
}

// checkLoss reads the metrics files once, stores the state and sends what
// decideLoss asks for. A directory that cannot be listed is logged and leaves
// the state as it was.
func (r *Runner) checkLoss(ctx context.Context, t Telegram) {
	metrics, err := readEngineMetrics(r.lossAlert.Dir)
	if err != nil {
		r.log.Warn("cron: engine metrics not read", "dir", r.lossAlert.Dir, "err", err)
		return
	}
	var next LossState
	var spool SpoolState
	var v, sv alertVerdict
	r.state(func(st *Status) {
		now := r.now()
		next, v = decideLoss(st.Loss, metrics, r.lossAlert, now)
		st.Loss = &next
		spool, sv = decideSpool(st.Spool, metrics, r.lossAlert, now)
		st.Spool = &spool
	})
	r.save()
	if next.silentWarnDue() {
		r.log.Warn("cron: no engine metrics file is fresh", "dir", r.lossAlert.Dir, "since", next.SilentSince, "files", len(metrics))
	}
	if msg := lossMessage(v, next, r.lossAlert.Lang); msg != "" {
		r.log.Warn("cron: event loss alert", "lost", next.Lost, "alerted", next.Alerted, "instances", next.Instances)
		if suffix := r.notifyIfConfigured(ctx, t, msg); suffix != "" {
			r.log.Info("cron: event loss alert not delivered" + suffix)
		}
	}
	if msg := spoolMessage(sv, spool, r.lossAlert.Lang); msg != "" {
		r.log.Warn("cron: event spool alert", "reasons", spool.Reasons, "alerted", spool.Alerted, "instances", spool.Instances)
		if suffix := r.notifyIfConfigured(ctx, t, msg); suffix != "" {
			r.log.Info("cron: event spool alert not delivered" + suffix)
		}
	}
}
