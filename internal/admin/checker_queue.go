package admin

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// checkerTick is the interval of the queue worker between two runs.
	checkerTick = 30 * time.Second
	// checkerMaxAttempts is the number of failed tries after which a record goes to error.
	checkerMaxAttempts = 3
	// checkerCheckFrequency is the check_frequency of a new application in minutes.
	checkerCheckFrequency = 360
	// checkerPostWindow is the window the hourly limit of creations is counted over.
	checkerPostWindow = time.Hour
)

// checkerRetrySteps are the waits after the first, the second and any later failed try.
var checkerRetrySteps = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// WatchChecker runs the queue of the checker integration until ctx ends.
// The queue works every checkerTick and at once after a wake-up of checkerKick. It sends nothing
// unless the integration and the creation are on, the settings are complete and sound and the
// queue is not paused after a refused token. It does nothing when no records file is configured.
// At start it also brings the postback templates of the s2s catalog in line with the saved
// settings; a failure is logged and the next save of the settings repeats it.
func (s *Server) WatchChecker(ctx context.Context) {
	if err := s.syncCheckerPostbacks(ctx); err != nil {
		log.Printf("checker: cannot sync the postback templates: %v", err)
	}
	if s.cfg.CheckerAppsFile == "" {
		return
	}
	ticker := time.NewTicker(checkerTick)
	defer ticker.Stop()
	s.checkerRun(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.checker.kick:
		}
		s.checkerRun(ctx)
	}
}

// checkerQueueSettings returns the settings the queue may work with.
// ok is false while the integration or the creation is off, the settings are incomplete or
// damaged, or the queue is paused.
func (s *Server) checkerQueueSettings() (checkerSettings, bool) {
	set := s.checkerSettings()
	ok := s.checkerLoadError() == "" && set.Enabled && set.CreateApps && set.ready() && !s.checker.paused.Load()
	return set, ok
}

// checkerRun sends the records that are due, one after the other, until none is left.
// ctx ends the run between two records and inside a request.
func (s *Server) checkerRun(ctx context.Context) {
	for ctx.Err() == nil {
		set, ok := s.checkerQueueSettings()
		if !ok || s.cfg.Groups == nil {
			return
		}
		groups, err := s.cfg.Groups.List(ctx)
		if err != nil {
			log.Printf("checker: cannot read the campaigns: %v", err)
			return
		}
		live := make(map[string]bool, len(groups))
		for _, g := range groups {
			live[g.ID] = true
		}
		s.checkerPruneOrphans(live)
		id, rec, ok := s.checkerClaim(set, groups)
		if !ok {
			return
		}
		s.checkerStep(ctx, set, id, rec, groups)
	}
}

// checkerClaim takes the oldest due record of the current connection whose campaign exists and
// marks it sending.
// groups is the campaign list. It returns the campaign id and the record as it was taken, or
// false when nothing is due or the file cannot be written.
func (s *Server) checkerClaim(set checkerSettings, groups []config.Group) (string, checkerApp, bool) {
	now := s.checkerNow()
	cur := set.tenantKey()
	s.checker.mu.Lock()
	defer s.checker.mu.Unlock()
	var best checkerApp
	bestID := ""
	for _, g := range groups {
		rec, has := s.checker.apps[g.ID]
		if !has || rec.State != checkerStQueued || rec.Tenant != cur || checkerParseTime(rec.NextAttemptAt).After(now) {
			continue
		}
		if bestID == "" || checkerParseTime(rec.NextAttemptAt).Before(checkerParseTime(best.NextAttemptAt)) {
			best, bestID = rec, g.ID
		}
	}
	if bestID == "" {
		return "", checkerApp{}, false
	}
	taken := best
	taken.State, taken.UpdatedAt = checkerStSending, checkerStamp(now)
	if err := s.checkerCommitLocked(bestID, taken); err != nil {
		log.Printf("checker: cannot write the records file: %v", err)
		return "", checkerApp{}, false
	}
	return bestID, best, true
}

// checkerSettle changes the record of campaign id while it is still sending and writes the file.
// change runs under checkerMu and edits the record. A record that is no longer sending, because
// the user acted meanwhile, is left as it is. It returns whether the change was stored.
func (s *Server) checkerSettle(id string, change func(a *checkerApp)) bool {
	s.checker.mu.Lock()
	defer s.checker.mu.Unlock()
	cur, has := s.checker.apps[id]
	if !has || cur.State != checkerStSending {
		return false
	}
	change(&cur)
	cur.UpdatedAt = checkerStamp(s.checkerNow())
	if err := s.checkerCommitLocked(id, cur); err != nil {
		log.Printf("checker: cannot write the records file: %v", err)
		return false
	}
	return true
}

// checkerStep works one claimed record: it looks the bundle up, and when the checker has no such
// application it creates it with the keyword on every locale.
// set is the settings of the run, id the campaign and rec the record as it was claimed, groups the
// campaign list read for the run. The outcome is stored on the record and logged without the token.
func (s *Server) checkerStep(ctx context.Context, set checkerSettings, id string, rec checkerApp, groups []config.Group) {
	if code, msg := checkerCreateProblem(rec); code != 0 {
		s.checkerFinish(id, rec, "error", func(a *checkerApp) { a.State, a.LastError = checkerStError, msg })
		return
	}
	cl := s.newCheckerClient(set)
	found, err := cl.findByBundle(ctx, prioQueue, rec.BundleID)
	if err != nil {
		s.checkerFailed(ctx, id, rec, err)
		return
	}
	if found != nil {
		s.checkerLink(id, rec, groups, found, true, 0)
		return
	}
	g, exists := s.checkerLiveCampaign(ctx, id)
	if !exists {
		s.checkerFinish(id, rec, "campaign gone, not sent", func(a *checkerApp) { a.State = checkerStQueued })
		return
	}
	if wait := s.checkerPostWait(); wait > 0 {
		next := checkerStamp(s.checkerNow().Add(wait))
		s.checkerFinish(id, rec, "creation limit, waits", func(a *checkerApp) { a.State, a.NextAttemptAt = checkerStQueued, next })
		return
	}
	locales := set.localeCodes()
	if len(locales) == 0 {
		s.checkerFinish(id, rec, "error", func(a *checkerApp) { a.State, a.LastError = checkerStError, "no locales configured" })
		return
	}
	name := rec.Name
	if name == "" {
		name = checkerAppName(g.Num, rec.Keyword)
	}
	req := checkerCreateRequest{
		Name: name, BundleID: rec.BundleID, PlayStoreURL: checkerPlayURL(rec.BundleID), NotionURL: rec.NotionURL,
		Status: checkerStoreStatus, CheckFreq: checkerCheckFrequency, ConsoleDevID: rec.ConsoleID,
		KeywordsData: checkerKeywordsData(rec.Keyword, locales),
	}
	if set.AdminURL != "" {
		req.KeitaroURL = set.AdminURL + "/#/flows/" + url.PathEscape(id)
	}
	s.checkerNotePost()
	info, existed, err := cl.createApp(ctx, prioQueue, req)
	if err != nil {
		s.checkerFailed(ctx, id, rec, err)
		return
	}
	sent := len(locales)
	if existed {
		sent = 0
	}
	s.checkerLink(id, rec, groups, info, existed, sent)
}

// checkerKeywordsData builds keywords_data: one "keyword:locale" line per locale, joined by a line break.
func checkerKeywordsData(keyword string, locales []string) string {
	lines := make([]string, len(locales))
	for i, l := range locales {
		lines[i] = keyword + ":" + l
	}
	return strings.Join(lines, "\n")
}

// checkerLiveCampaign reads the campaign list again and returns campaign id when it still exists.
// A list error counts as missing: nothing is sent on a doubt.
func (s *Server) checkerLiveCampaign(ctx context.Context, id string) (config.Group, bool) {
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return config.Group{}, false
	}
	return findGroup(groups, id)
}

// checkerPostWait returns how long the hourly limit of creations asks to wait, or zero when a
// creation may go now. Creations older than an hour leave the window.
func (s *Server) checkerPostWait() time.Duration {
	now := s.checkerNow()
	s.checker.mu.Lock()
	defer s.checker.mu.Unlock()
	keep := s.checker.posts[:0]
	for _, t := range s.checker.posts {
		if now.Sub(t) < checkerPostWindow {
			keep = append(keep, t)
		}
	}
	s.checker.posts = keep
	if len(keep) < checkerMaxPosts {
		return 0
	}
	return keep[0].Add(checkerPostWindow).Sub(now)
}

// checkerNotePost counts one creation request in the hourly window.
func (s *Server) checkerNotePost() {
	now := s.checkerNow()
	s.checker.mu.Lock()
	s.checker.posts = append(s.checker.posts, now)
	s.checker.mu.Unlock()
}

// checkerLink ties the record to the checker application info.
// existed tells that the application was there already; sent is the number of keywords sent.
// groups is the campaign list. An application that another live record holds already makes the
// record an error, so one application is never linked twice.
func (s *Server) checkerLink(id string, rec checkerApp, groups []config.Group, info *checkerAppInfo, existed bool, sent int) {
	result := "linked"
	if existed {
		result = "linked, existed"
	}
	stamp := checkerStamp(s.checkerNow())
	s.checkerFinish(id, rec, result, func(a *checkerApp) {
		if num, taken := s.checkerAppTaken(id, info.ID, groups); taken {
			a.State, a.LastError = checkerStError, "приложение уже связано с кампанией "+strconv.Itoa(num)
			result = "error"
			return
		}
		a.State, a.AppID, a.Status, a.StatusAt = checkerStLinked, info.ID, info.Status, stamp
		a.Existed, a.KeywordsSent, a.Attempts, a.NextAttemptAt, a.LastError = existed, sent, 0, "", ""
	})
}

// checkerFinish stores the outcome of a step and writes the log line of it.
// result names the outcome in the log; change edits the record under the lock.
// When the record was changed by the user meanwhile the outcome is dropped and logged as such.
func (s *Server) checkerFinish(id string, rec checkerApp, result string, change func(a *checkerApp)) {
	if !s.checkerSettle(id, change) {
		result = "dropped, record changed meanwhile"
	}
	log.Printf("checker: campaign=%s bundle=%s result=%s", id, rec.BundleID, result)
}

// checkerFailed handles a failed call of a step.
// err is the failure of the client. A shutdown puts the record back in the queue untouched. A
// refused token (401, 403) makes the record an error and pauses the queue until the settings are
// saved. A call that may be repeated (no answer, 5xx, 429) counts a try and waits one minute, then
// five, and after the third failed try makes the record an error; a 429 waits at least its
// Retry-After, never more than 30 minutes. Any other answer, 422 included, is an error at once.
func (s *Server) checkerFailed(ctx context.Context, id string, rec checkerApp, err error) {
	var ce *checkerError
	if !errors.As(err, &ce) {
		ce = &checkerError{Msg: "checker call failed"}
	}
	switch {
	case ctx.Err() != nil:
		s.checkerFinish(id, rec, "stopped, queued again", func(a *checkerApp) { a.State = checkerStQueued })
	case ce.refused():
		s.checker.paused.Store(true)
		s.checkerFinish(id, rec, "error, queue paused: "+ce.Error(), func(a *checkerApp) {
			a.State, a.LastError = checkerStError, ce.Error()
		})
	case ce.transient():
		s.checkerRetry(id, rec, ce)
	default:
		s.checkerFinish(id, rec, "error: "+ce.Error(), func(a *checkerApp) {
			a.State, a.LastError = checkerStError, ce.Error()
		})
	}
}

// checkerRetry counts a failed try of a record and schedules the next one, or ends in error.
// ce is the transient failure.
func (s *Server) checkerRetry(id string, rec checkerApp, ce *checkerError) {
	attempts := rec.Attempts + 1
	if attempts >= checkerMaxAttempts {
		s.checkerFinish(id, rec, "error after "+strconv.Itoa(attempts)+" tries: "+ce.Error(), func(a *checkerApp) {
			a.State, a.Attempts, a.LastError = checkerStError, attempts, ce.Error()
		})
		return
	}
	wait := min(max(checkerRetrySteps[attempts-1], ce.RetryAfter), checkerMaxRetryAfter)
	next := checkerStamp(s.checkerNow().Add(wait))
	s.checkerFinish(id, rec, "retry "+strconv.Itoa(attempts)+": "+ce.Error(), func(a *checkerApp) {
		a.State, a.Attempts, a.NextAttemptAt, a.LastError = checkerStQueued, attempts, next, ce.Error()
	})
}
