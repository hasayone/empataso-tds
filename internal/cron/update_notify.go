package cron

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

const (
	// updateCheckEvery is the minimum period between consecutive update checks.
	updateCheckEvery = time.Minute
	// updateFreshAge is the maximum age of check.json for it to trigger a notification.
	updateFreshAge = 24 * time.Hour
	// updateResultMaxAge is the maximum age of a finished state to be announced.
	updateResultMaxAge = 24 * time.Hour
	// updateSubjectLimit is the maximum number of commit subjects in the target message.
	updateSubjectLimit = 5
	// updateSubjectLen is the maximum length of one commit subject in runes.
	updateSubjectLen = 80
	// updateReasonLen is the maximum length of the state reason field in runes.
	updateReasonLen = 200
)

// updateMsgT holds the Telegram message templates for one language.
type updateMsgT struct {
	// newUpdate is the target-notification header; args: channel, curLabel, tgtLabel, ahead.
	newUpdate string
	// commits is appended for each commit subject; arg: escaped subject.
	commits string
	// manualSteps is appended when ManualSteps is non-empty.
	manualSteps string
	// signed is appended when the target is signed.
	signed string
	// result* are the first line of the result notification for each outcome.
	resultOK      string
	resultRolled  string
	resultFailed  string
	resultRefused string
	// fromTo is appended to the result prefix; args: from12, to12.
	fromTo string
	// reason is appended when Reason is non-empty; arg: escaped reason.
	reason string
	// cronLagging is appended when CronLagging is true.
	cronLagging string
	// eventsLost is appended when EventsLost > 0; arg: count.
	eventsLost string
}

// updateLangs holds the Telegram message templates by language code.
var updateLangs = map[string]*updateMsgT{
	"en": {
		newUpdate:     "🆕 EmTDS: update available · channel %s\n%s → %s · %d commit(s)",
		commits:       "\n— %s",
		manualSteps:   "\n⚠️ manual steps required",
		signed:        " · signed ✓",
		resultOK:      "✅ EmTDS: update applied",
		resultRolled:  "⬅️ EmTDS: rolled back",
		resultFailed:  "❌ EmTDS: update failed",
		resultRefused: "🚫 EmTDS: update refused",
		fromTo:        " (%s → %s)",
		reason:        "\nreason: %s",
		cronLagging:   "\n⚠️ cron lagging behind target",
		eventsLost:    "\n⚠️ %d event(s) lost during update",
	},
	"ru": {
		newUpdate:     "🆕 EmTDS: доступно обновление · канал %s\n%s → %s · %d коммит(ов)",
		commits:       "\n— %s",
		manualSteps:   "\n⚠️ требуются ручные шаги",
		signed:        " · подписано ✓",
		resultOK:      "✅ EmTDS: обновление применено",
		resultRolled:  "⬅️ EmTDS: откат выполнен",
		resultFailed:  "❌ EmTDS: обновление не удалось",
		resultRefused: "🚫 EmTDS: обновление отклонено",
		fromTo:        " (%s → %s)",
		reason:        "\nпричина: %s",
		cronLagging:   "\n⚠️ cron отстаёт от цели",
		eventsLost:    "\n⚠️ потеряно событий: %d",
	},
	"uk": {
		newUpdate:     "🆕 EmTDS: доступне оновлення · канал %s\n%s → %s · %d коміт(ів)",
		commits:       "\n— %s",
		manualSteps:   "\n⚠️ потрібні ручні кроки",
		signed:        " · підписано ✓",
		resultOK:      "✅ EmTDS: оновлення застосовано",
		resultRolled:  "⬅️ EmTDS: відкат виконано",
		resultFailed:  "❌ EmTDS: оновлення не вдалося",
		resultRefused: "🚫 EmTDS: оновлення відхилено",
		fromTo:        " (%s → %s)",
		reason:        "\nпричина: %s",
		cronLagging:   "\n⚠️ cron відстає від цілі",
		eventsLost:    "\n⚠️ втрачено подій: %d",
	},
}

// WatchUpdates enables update notifications for Runner. Called from maybeCheckUpdates
// on the first tick when r.paths.Groups is set; there is no separate opt-in like
// WatchRedis because updates use the same Groups path that is always configured.

// maybeCheckUpdates starts one update-notification check when it is due and
// none is running. t is where alerts go.
// Does nothing when r.paths.Groups is empty (update directory unknown).
// The check runs in its own goroutine.
func (r *Runner) maybeCheckUpdates(ctx context.Context, t Telegram) {
	if r.paths.Groups == "" {
		return
	}
	now := r.now()
	r.mu.Lock()
	if r.updateBusy || now.Before(r.updateNext) {
		r.mu.Unlock()
		return
	}
	r.updateBusy, r.updateNext = true, now.Add(updateCheckEvery)
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.checkUpdates(ctx, t)
		r.mu.Lock()
		r.updateBusy = false
		r.mu.Unlock()
	}()
}

// checkUpdates reads check.json and state.json from the update directory and
// sends at most one Telegram message for a new available target and one for a
// completed run result. Deduplication is tracked exclusively in announced.json.
// When Telegram is not configured, entries are still marked as announced so
// that enabling it later does not flood the channel with stale notifications.
func (r *Runner) checkUpdates(ctx context.Context, t Telegram) {
	lang := strings.ToLower(strings.TrimSpace(config.Env("EMTDS_ALERT_LANG")))
	if updateLangs[lang] == nil {
		lang = "en"
	}
	msgs := updateLangs[lang]
	dir := UpdateDir(r.paths.Groups)
	now := r.now()

	var check CheckFile
	checkErr := ReadUpdateJSON(filepath.Join(dir, UpdateCheckFile), UpdateMaxJSON, &check)
	if checkErr != nil && !errors.Is(checkErr, os.ErrNotExist) {
		r.log.Warn("cron: update check: cannot read check.json", "err", checkErr)
	}

	var state StateFile
	stateErr := ReadUpdateJSON(filepath.Join(dir, UpdateStateFile), UpdateMaxJSON, &state)
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		r.log.Warn("cron: update check: cannot read state.json", "err", stateErr)
	}

	var announced Announced
	annErr := ReadUpdateJSON(filepath.Join(dir, UpdateAnnouncedFile), UpdateMaxJSON, &announced)
	if annErr != nil && !errors.Is(annErr, os.ErrNotExist) {
		r.log.Warn("cron: update check: cannot read announced.json", "err", annErr)
	}

	changed := false

	// Announce new available target.
	if checkErr == nil && shouldAnnounceTarget(check, announced, now) {
		msg := buildTargetMsg(check, msgs)
		if !t.Configured() {
			announced.Target = check.Target.Commit
			announced.At = now
			changed = true
		} else if err := r.notify(ctx, t, msg); err != nil {
			r.log.Warn("cron: update target notification not sent", "err", err)
		} else {
			announced.Target = check.Target.Commit
			announced.At = now
			changed = true
			r.log.Info("cron: update target announced", "target", check.Target.Commit[:12])
		}
	}

	// Announce completed run result.
	if stateErr == nil && shouldAnnounceResult(state, announced, now) {
		msg := buildResultMsg(state, msgs)
		if !t.Configured() {
			announced.ResultID = state.ID
			announced.At = now
			changed = true
		} else if err := r.notify(ctx, t, msg); err != nil {
			r.log.Warn("cron: update result notification not sent", "err", err)
		} else {
			announced.ResultID = state.ID
			announced.At = now
			changed = true
			r.log.Info("cron: update result announced", "id", state.ID, "result", state.Result)
		}
	}

	if changed {
		if err := WriteUpdateJSON(filepath.Join(dir, UpdateAnnouncedFile), announced); err != nil {
			r.log.Warn("cron: update: announced.json not saved", "err", err)
		}
	}
}

// shouldAnnounceTarget reports whether check contains a new target that has
// not yet been announced. All six conditions from the brief must hold.
func shouldAnnounceTarget(check CheckFile, announced Announced, now time.Time) bool {
	return check.Fresh(now, updateFreshAge) &&
		check.Error == "" &&
		check.Ahead > 0 &&
		ValidCommit(check.Target.Commit) &&
		check.Target.Commit != announced.Target
}

// shouldAnnounceResult reports whether state describes a finished run whose
// result has not yet been announced and is at most updateResultMaxAge old.
func shouldAnnounceResult(state StateFile, announced Announced, now time.Time) bool {
	if state.FinishedAt == nil {
		return false
	}
	switch state.Result {
	case StateOK, StateRolledBack, StateFailed, StateRefused:
	default:
		return false
	}
	return ValidUpdateID(state.ID) &&
		state.Action != "check" &&
		state.ID != announced.ResultID &&
		now.Sub(*state.FinishedAt) <= updateResultMaxAge
}

// buildTargetMsg composes the Telegram message for a newly available update target.
func buildTargetMsg(check CheckFile, msgs *updateMsgT) string {
	cur := refLabel(check.Current)
	tgt := refLabel(check.Target)
	var sb strings.Builder
	fmt.Fprintf(&sb, msgs.newUpdate, check.Channel, cur, tgt, check.Ahead)
	if check.Signed {
		sb.WriteString(msgs.signed)
	}
	if len(check.ManualSteps) > 0 {
		sb.WriteString(msgs.manualSteps)
	}
	for i, c := range check.Commits {
		if i >= updateSubjectLimit {
			break
		}
		subj := escNotify(c.Subject)
		runes := []rune(subj)
		if len(runes) > updateSubjectLen {
			runes = runes[:updateSubjectLen]
		}
		fmt.Fprintf(&sb, msgs.commits, string(runes))
	}
	return sb.String()
}

// buildResultMsg composes the Telegram message for a completed update run.
func buildResultMsg(state StateFile, msgs *updateMsgT) string {
	var prefix string
	switch state.Result {
	case StateOK:
		prefix = msgs.resultOK
	case StateRolledBack:
		prefix = msgs.resultRolled
	case StateFailed:
		prefix = msgs.resultFailed
	case StateRefused:
		prefix = msgs.resultRefused
	default:
		prefix = state.Result
	}
	var sb strings.Builder
	sb.WriteString(prefix)
	fmt.Fprintf(&sb, msgs.fromTo, sha12(state.From), sha12(state.To))
	if state.Reason != "" {
		reason := escNotify(state.Reason)
		runes := []rune(reason)
		if len(runes) > updateReasonLen {
			runes = runes[:updateReasonLen]
		}
		fmt.Fprintf(&sb, msgs.reason, string(runes))
	}
	if state.CronLagging {
		sb.WriteString(msgs.cronLagging)
	}
	if state.EventsLost > 0 {
		fmt.Fprintf(&sb, msgs.eventsLost, state.EventsLost)
	}
	return sb.String()
}

// refLabel returns the most human-readable identifier for a CheckRef.
// Prefers Tag (for release-channel targets), then Version, then the first
// 12 characters of Commit.
func refLabel(ref CheckRef) string {
	if ref.Tag != "" {
		return ref.Tag
	}
	if ref.Version != "" {
		return ref.Version
	}
	if len(ref.Commit) >= 12 {
		return ref.Commit[:12]
	}
	return ref.Commit
}

// sha12 returns the first 12 characters of a SHA string, or the whole string
// when it is shorter than 12 characters.
func sha12(s string) string {
	if len(s) < 12 {
		return s
	}
	return s[:12]
}

// escNotify escapes HTML special characters in s so that user-supplied text
// from JSON files cannot inject markup. Applied to commit subjects, state
// reasons and any other field originating outside the binary.
func escNotify(s string) string {
	return html.EscapeString(s)
}
