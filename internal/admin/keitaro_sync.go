package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
)

// Keitaro catch-up timing: ticker delays, lag behind "now", look-back windows,
// chunk size, per-run cap, run timeout, failure backoff cap, and future-skew guard.
const (
	keitaroSyncFirst      = 30 * time.Second
	keitaroSyncTick       = 30 * time.Second
	keitaroSyncLag        = 3 * time.Minute
	keitaroSyncClickBack  = 30 * time.Minute
	keitaroSyncConvBack   = 24 * time.Hour
	keitaroSyncChunk      = 6 * time.Hour
	keitaroSyncMaxPerRun  = 24 * time.Hour
	keitaroSyncMaxTimeout = 30 * time.Minute
	keitaroSyncMaxBackoff = 6 * time.Hour
	keitaroSyncFutureSkew = 5 * time.Minute
)

// User-visible sync outcomes (stored in keitaro-sync.json and cron job status).
const (
	keitaroSyncNeedImport = "нужен полный импорт"
	keitaroSyncTimezone   = "часовой пояс"
)

// keitaroSyncPassResult is the outcome of one catch-up pass (clicks then conversions).
type keitaroSyncPassResult struct {
	OK          bool
	Message     string
	ErrText     string
	Clicks      int
	Conversions int
	Skipped     int
	// Campaigns is how many new Keitaro campaigns the pass imported.
	Campaigns int
}

// keitaroSyncFinishIn updates persisted sync state after a pass.
type keitaroSyncFinishIn struct {
	State    *keitaroSyncFile
	Result   keitaroSyncPassResult
	Interval time.Duration
	Began    time.Time
}

// keitaroSyncTarget is one tracker connection for a catch-up pass.
type keitaroSyncTarget struct {
	syncPath      string
	board         string
	allBoards     bool
	creds         ktCreds
	manifestStore ManifestStore
}

// syncStreamOpts selects one log stream (clicks or conversions) for a catch-up window.
type syncStreamOpts struct {
	Creds         ktCreds
	State         *keitaroSyncFile
	syncPath      string
	mapScope      campaignMapScope
	manifestStore ManifestStore
	Cursor        *time.Time
	Back          time.Duration
	Upper         time.Time
	Clicks        bool
	Conversions   bool
	Skip          *logSkipTally
	Catalog       *syncCatalogNotes
}

// syncCatalogMaxLines is how many catalog report lines one sync message keeps.
const syncCatalogMaxLines = 3

// syncCatalogNotes collects the catalog top-up outcome of one sync pass for its message.
// added is the number of new catalog records. lines are distinct report lines in arrival order.
type syncCatalogNotes struct {
	added int
	lines []string
}

// collect adds a top-up count and the catalog lines among losses. A nil receiver does nothing.
func (n *syncCatalogNotes) collect(added int, losses []ktimport.Loss) {
	if n == nil {
		return
	}
	n.added += added
	for _, l := range losses {
		if l.Ref != catalogTopUpRef || slices.Contains(n.lines, l.Detail) {
			continue
		}
		n.lines = append(n.lines, l.Detail)
	}
}

// suffix returns the message tail: «, каталог +N» and up to syncCatalogMaxLines lines after «; ».
// It returns an empty string when nothing was added or reported.
func (n *syncCatalogNotes) suffix() string {
	if n == nil {
		return ""
	}
	out := ""
	if n.added > 0 {
		out = fmt.Sprintf(", каталог +%d", n.added)
	}
	lines := n.lines
	if len(lines) > syncCatalogMaxLines {
		lines = append(append([]string(nil), lines[:syncCatalogMaxLines]...), fmt.Sprintf("и ещё %d", len(n.lines)-syncCatalogMaxLines))
	}
	if len(lines) > 0 {
		out += "; " + strings.Join(lines, "; ")
	}
	return out
}

// keitaroSyncFile is the persisted catch-up cursor and last run status.
type keitaroSyncFile struct {
	Host          string    `json:"host"`
	ClicksTo      time.Time `json:"clicks_to"`
	ConversionsTo time.Time `json:"conversions_to"`
	LastRun       time.Time `json:"last_run"`
	OK            bool      `json:"ok"`
	Message       string    `json:"message"`
	Error         string    `json:"error,omitempty"`
	DurationMS    int64     `json:"duration_ms"`
	NextRun       time.Time `json:"next_run"`
	Running       bool      `json:"running"`
	LagSeconds    int64     `json:"lag_seconds"`
	LastClicks    int       `json:"last_clicks,omitempty"`
	LastConverts  int       `json:"last_conversions,omitempty"`
	Skipped       int       `json:"skipped,omitempty"`
	// CatalogChecked is true once every catalog id stored rows of Host refer to was topped up.
	CatalogChecked bool `json:"catalog_checked,omitempty"`
	// DomainsChecked is true once every domain id that campaigns of the connection refer to was topped up.
	// It is kept apart from CatalogChecked so hosts that already passed the catalog check still get domains.
	DomainsChecked bool `json:"catalog_domains_checked,omitempty"`
	// LastCampaigns is how many new Keitaro campaigns the last pass imported.
	LastCampaigns int `json:"last_campaigns"`
	// CampaignsCursor is the largest Keitaro campaign id the new-campaign step has handled.
	CampaignsCursor int `json:"campaigns_cursor,omitempty"`
	// CampaignsPending is how many new campaigns wait for a later pass.
	CampaignsPending int `json:"campaigns_pending,omitempty"`
	// CampaignsDeleted counts Keitaro campaigns skipped because their group was deleted here.
	CampaignsDeleted int `json:"campaigns_deleted,omitempty"`
	// CampaignsFailed are the campaigns of the last step that were handled but not imported.
	CampaignsFailed []int `json:"campaigns_failed,omitempty"`
	// CampaignsPausedUntil holds the step back after a tracker or store failure; zero means no pause.
	CampaignsPausedUntil time.Time `json:"campaigns_paused_until,omitzero"`
	// CampaignsError is the reason of the current pause.
	CampaignsError string `json:"campaigns_error,omitempty"`
}

// WatchKeitaroSync runs the catch-up ticker until ctx ends.
func (s *Server) WatchKeitaroSync(ctx context.Context) {
	if s.cfg.KeitaroSyncFile == "" {
		return
	}
	s.clearKeitaroSyncRunning()
	first := time.NewTimer(keitaroSyncFirst)
	defer first.Stop()
	ticker := time.NewTicker(keitaroSyncTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-ticker.C:
		}
		s.keitaroSyncOnce(ctx)
	}
}

// runKeitaroSyncNow asks the watcher to run catch-up on the next tick.
func (s *Server) runKeitaroSyncNow() error {
	if !s.keitaroCreds().configured() || s.cfg.Warehouse == nil {
		return errors.New("keitaro not configured")
	}
	if s.ktJob.syncActive() {
		return errors.New(keitaroSyncBusyMsg)
	}
	if !s.syncKick.CompareAndSwap(false, true) {
		return errors.New(keitaroSyncBusyMsg)
	}
	return nil
}

// keitaroSyncClock returns UTC now, or syncNow when the server sets it for tests.
func (s *Server) keitaroSyncClock() time.Time {
	if s.syncNow != nil {
		return s.syncNow().UTC()
	}
	return time.Now().UTC()
}

// clearKeitaroSyncRunning clears Running in every sync state file after an admin restart.
// It logs a warning when a file cannot be saved.
func (s *Server) clearKeitaroSyncRunning() {
	for _, path := range s.keitaroSyncPaths() {
		st, ok, err := s.loadKeitaroSyncAt(path)
		if err != nil || !ok || !st.Running {
			continue
		}
		st.Running = false
		if err := s.saveKeitaroSyncAt(path, st); err != nil {
			s.logWarn("keitaro sync save failed", "err", err)
		}
	}
}

// keitaroSyncPaths returns distinct sync cursor file paths for all connections.
func (s *Server) keitaroSyncPaths() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range s.keitaroSyncTargets() {
		if t.syncPath == "" || seen[t.syncPath] {
			continue
		}
		seen[t.syncPath] = true
		out = append(out, t.syncPath)
	}
	return out
}

// keitaroSyncTargets lists catch-up targets: one per ktConns entry, or a single global fallback.
func (s *Server) keitaroSyncTargets() []keitaroSyncTarget {
	conns := s.listKeitaroConns()
	if len(conns) == 0 {
		if s.cfg.KeitaroSyncFile == "" {
			return nil
		}
		creds, manifest := s.keitaroImportBundle(config.MainBoardID)
		return []keitaroSyncTarget{{
			syncPath:      s.cfg.KeitaroSyncFile,
			board:         config.MainBoardID,
			allBoards:     true,
			creds:         creds,
			manifestStore: manifest,
		}}
	}
	out := make([]keitaroSyncTarget, 0, len(conns))
	for _, c := range conns {
		if c.SyncFile == "" {
			continue
		}
		manifest := c.Manifest
		if manifest == nil {
			manifest = s.cfg.Manifest
		}
		out = append(out, keitaroSyncTarget{
			syncPath:      c.SyncFile,
			board:         c.BoardID,
			allBoards:     c.global,
			creds:         ktCreds{Base: c.Base, Key: c.Key},
			manifestStore: manifest,
		})
	}
	return out
}

// loadKeitaroSync reads the global keitaro-sync.json path from config.
func (s *Server) loadKeitaroSync() (keitaroSyncFile, bool, error) {
	return s.loadKeitaroSyncAt(s.cfg.KeitaroSyncFile)
}

// loadKeitaroSyncAt reads one sync state file. The bool is false when path is empty or missing.
func (s *Server) loadKeitaroSyncAt(path string) (keitaroSyncFile, bool, error) {
	if path == "" {
		return keitaroSyncFile{}, false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return keitaroSyncFile{}, false, nil
		}
		return keitaroSyncFile{}, false, err
	}
	var st keitaroSyncFile
	if err := json.Unmarshal(b, &st); err != nil {
		return keitaroSyncFile{}, false, err
	}
	return st, true, nil
}

// saveKeitaroSync writes st to the global sync path from config.
func (s *Server) saveKeitaroSync(st keitaroSyncFile) error {
	return s.saveKeitaroSyncAt(s.cfg.KeitaroSyncFile, st)
}

// saveKeitaroSyncAt writes st atomically to path. An empty path is a no-op.
func (s *Server) saveKeitaroSyncAt(path string, st keitaroSyncFile) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// keitaroSyncJobStatus maps persisted sync state to the cron panel job row.
func (s *Server) keitaroSyncJobStatus(st keitaroSyncFile) cron.JobStatus {
	return cron.JobStatus{
		LastRun:    st.LastRun,
		OK:         st.OK,
		Message:    st.Message,
		Error:      st.Error,
		DurationMS: st.DurationMS,
		NextRun:    st.NextRun,
		Running:    st.Running,
	}
}

// keitaroSyncOnce runs one scheduled or kick-triggered catch-up when due.
func (s *Server) keitaroSyncOnce(ctx context.Context) {
	if !s.syncBusy.CompareAndSwap(false, true) {
		return
	}
	defer s.syncBusy.Store(false)

	wantKick := s.syncKick.Load()
	cfg, err := s.loadCronConfig()
	if err != nil {
		s.logWarn("keitaro sync cron unreadable", "err", err)
		s.keitaroSyncConsumeKick(wantKick, "cron config unreadable")
		return
	}
	now := s.keitaroSyncClock()
	if wantKick {
		s.syncKick.Store(false)
	}
	kick := wantKick
	if !kick && !cfg.KeitaroSync.Enabled {
		return
	}
	if !kick && !s.keitaroSyncAnyDue(now) {
		return
	}
	s.keitaroSyncExecute(ctx, cfg, kick, now)
}

// keitaroSyncAnyDue reports whether any connection sync file is due or not yet created.
func (s *Server) keitaroSyncAnyDue(now time.Time) bool {
	targets := s.keitaroSyncTargets()
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		st, have, err := s.loadKeitaroSyncAt(t.syncPath)
		if err != nil {
			continue
		}
		if !have || st.NextRun.IsZero() || !now.Before(st.NextRun) {
			return true
		}
	}
	return false
}

// keitaroSyncConsumeKick clears a run-now flag and persists errText on the global sync file when set.
func (s *Server) keitaroSyncConsumeKick(kick bool, errText string) {
	if kick {
		s.syncKick.Store(false)
	}
	if errText == "" || s.cfg.KeitaroSyncFile == "" {
		return
	}
	st, have, err := s.loadKeitaroSync()
	if err != nil && !have {
		st = keitaroSyncFile{}
	}
	st.LastRun = s.keitaroSyncClock()
	st.OK = false
	st.Error = errText
	st.Running = false
	st.NextRun = st.LastRun.Add(cron.Default().Interval(cron.JobKeitaroSync))
	if err := s.saveKeitaroSync(st); err != nil {
		s.logWarn("keitaro sync save failed", "err", err)
	}
}

// keitaroSyncExecute holds the import lock, runs one pass per connection, and writes final status.
func (s *Server) keitaroSyncExecute(ctx context.Context, cfg cron.Config, kick bool, now time.Time) {
	targets := s.keitaroSyncTargets()
	if len(targets) == 0 {
		return
	}
	interval := cfg.Interval(cron.JobKeitaroSync)
	if s.cfg.Warehouse == nil {
		for _, t := range targets {
			st, have, _ := s.loadKeitaroSyncAt(t.syncPath)
			s.keitaroSyncWriteSoftFailAt(t.syncPath, &st, have, now, interval, "clickhouse unavailable")
		}
		return
	}
	if msg := s.ktJob.beginSync(); msg != "" {
		if kick {
			for _, t := range targets {
				st, have, _ := s.loadKeitaroSyncAt(t.syncPath)
				s.keitaroSyncWriteSoftFailAt(t.syncPath, &st, have, now, interval, msg)
			}
		}
		return
	}
	defer s.ktJob.finishSync()

	runCtx, cancel := context.WithTimeout(ctx, keitaroSyncMaxTimeout)
	defer cancel()

	for _, t := range targets {
		if !t.creds.configured() {
			if kick {
				st, have, _ := s.loadKeitaroSyncAt(t.syncPath)
				s.keitaroSyncWriteSoftFailAt(t.syncPath, &st, have, now, interval, "keitaro not configured")
			}
			continue
		}
		st, have, err := s.loadKeitaroSyncAt(t.syncPath)
		if err != nil {
			s.logWarn("keitaro sync state unreadable", "path", t.syncPath, "err", err)
			continue
		}
		st.Host = t.creds.host()
		st.Running = true
		st.LastRun = now
		st.Error = ""
		if err := s.saveKeitaroSyncAt(t.syncPath, st); err != nil {
			s.logWarn("keitaro sync save failed", "err", err)
		}
		began := s.keitaroSyncClock()
		res := s.runKeitaroSyncPass(runCtx, t, &st, have)
		s.keitaroSyncFinishRun(keitaroSyncFinishIn{State: &st, Result: res, Interval: interval, Began: began}, t.syncPath)
	}
}

// keitaroSyncWriteSoftFailAt records a non-retry backoff failure (NextRun = now+interval).
func (s *Server) keitaroSyncWriteSoftFailAt(path string, st *keitaroSyncFile, have bool, now time.Time, interval time.Duration, errText string) {
	st.LastRun = now
	st.OK = false
	st.Error = errText
	st.Message = ""
	st.Running = false
	st.NextRun = now.Add(interval)
	s.syncFailStreak = 0
	if err := s.saveKeitaroSyncAt(path, *st); err != nil {
		s.logWarn("keitaro sync save failed", "err", err)
	}
}

// keitaroSyncFinishRun sets OK, message, error, next run, and counters from in.Result.
func (s *Server) keitaroSyncFinishRun(in keitaroSyncFinishIn, syncPath string) {
	st := in.State
	res := in.Result
	st.Running = false
	st.DurationMS = s.keitaroSyncClock().Sub(in.Began).Milliseconds()
	st.LastClicks = res.Clicks
	st.LastConverts = res.Conversions
	st.Skipped = res.Skipped
	st.LastCampaigns = res.Campaigns
	st.LagSeconds = keitaroSyncLagSeconds(*st, s.keitaroSyncClock())
	if res.OK {
		s.syncFailStreak = 0
		st.OK = true
		st.Message = res.Message
		st.Error = ""
		st.NextRun = s.keitaroSyncClock().Add(in.Interval)
	} else if keitaroSyncSoftFail(res.ErrText) {
		s.syncFailStreak = 0
		st.OK = false
		st.Message = res.Message
		st.Error = res.ErrText
		st.NextRun = s.keitaroSyncClock().Add(in.Interval)
	} else {
		s.syncFailStreak++
		st.OK = false
		st.Message = res.Message
		st.Error = res.ErrText
		st.NextRun = s.keitaroSyncClock().Add(keitaroSyncBackoff(in.Interval, s.syncFailStreak))
	}
	if err := s.saveKeitaroSyncAt(syncPath, *st); err != nil {
		s.logWarn("keitaro sync save failed", "err", err)
	}
	if !res.OK && !keitaroSyncSoftFail(res.ErrText) {
		s.logWarn("keitaro sync failed", "err", res.ErrText)
	}
}

// keitaroSyncSoftFail reports errors that should not increase the failure streak.
func keitaroSyncSoftFail(errText string) bool {
	switch errText {
	case keitaroSyncNeedImport, "keitaro not configured", "clickhouse unavailable":
		return true
	default:
		return false
	}
}

// keitaroSyncBackoff returns min(interval<<shift, keitaroSyncMaxBackoff) with bounded shift.
func keitaroSyncBackoff(interval time.Duration, streak int) time.Duration {
	if streak <= 1 {
		return interval
	}
	shift := streak - 1
	if shift > 30 {
		shift = 30
	}
	mult := int64(1) << shift
	maxMult := int64(keitaroSyncMaxBackoff / interval)
	if maxMult < 1 {
		maxMult = 1
	}
	if mult > maxMult {
		mult = maxMult
	}
	b := time.Duration(mult) * interval
	if b > keitaroSyncMaxBackoff {
		return keitaroSyncMaxBackoff
	}
	return b
}

// keitaroSyncLagSeconds is the larger lag of click and conversion cursors behind upper=now−lag.
func keitaroSyncLagSeconds(st keitaroSyncFile, now time.Time) int64 {
	upper := now.Add(-keitaroSyncLag)
	var lag time.Duration
	if !st.ClicksTo.IsZero() {
		if d := upper.Sub(st.ClicksTo); d > lag {
			lag = d
		}
	}
	if !st.ConversionsTo.IsZero() {
		if d := upper.Sub(st.ConversionsTo); d > lag {
			lag = d
		}
	}
	if lag < 0 {
		return 0
	}
	return int64(lag / time.Second)
}

// loadCronConfig loads CronFile or returns cron.Default when CronFile is unset.
func (s *Server) loadCronConfig() (cron.Config, error) {
	if s.cfg.CronFile == "" {
		return cron.Default(), nil
	}
	return cron.Load(s.cfg.CronFile)
}

// syncWindowBounds returns [cursor−back, min(upper, cursor+keitaroSyncMaxPerRun)].
func syncWindowBounds(cursor, upper time.Time, back time.Duration) (from, to time.Time) {
	from = cursor.Add(-back)
	to = upper
	runCap := cursor.Add(keitaroSyncMaxPerRun)
	if runCap.Before(to) {
		to = runCap
	}
	return from, to
}

// runKeitaroSyncPass bootstraps cursors when needed, imports new campaigns, then syncs clicks and conversions.
// Every GET of the pass shares one budget of keitaroSyncGETCap requests.
func (s *Server) runKeitaroSyncPass(ctx context.Context, target keitaroSyncTarget, st *keitaroSyncFile, have bool) keitaroSyncPassResult {
	s.syncPassCount.Add(1)
	target.creds.gets = newKeitaroGETBudget(keitaroSyncGETCap)
	creds := target.creds
	now := s.keitaroSyncClock()
	upper := now.Add(-keitaroSyncLag)
	host := creds.host()

	if have && st.Host != "" && st.Host != host {
		st.Host = ""
		st.ClicksTo = time.Time{}
		st.ConversionsTo = time.Time{}
		st.CatalogChecked = false
		st.DomainsChecked = false
		st.CampaignsCursor, st.CampaignsPending, st.CampaignsDeleted = 0, 0, 0
		st.CampaignsFailed, st.CampaignsPausedUntil, st.CampaignsError = nil, time.Time{}, ""
		have = false
	}
	if err := keitaroSyncCheckTimes(st.ClicksTo, st.ConversionsTo, now); err != nil {
		return keitaroSyncPassResult{ErrText: err.Error()}
	}
	if !have || st.Host == "" || (st.ClicksTo.IsZero() && st.ConversionsTo.IsZero()) {
		ready, errMsg, err := s.keitaroSyncBootstrap(ctx, creds, st, target.manifestStore)
		if err != nil {
			return keitaroSyncPassResult{ErrText: sanitize(err, creds.Key)}
		}
		if !ready {
			msg := errMsg
			if msg == "" {
				msg = keitaroSyncNeedImport
			}
			return keitaroSyncPassResult{Message: msg, ErrText: msg}
		}
	}

	camps := s.keitaroSyncNewCampaigns(ctx, target, st)
	if err := s.saveKeitaroSyncAt(target.syncPath, *st); err != nil {
		return keitaroSyncPassResult{ErrText: "save error", Campaigns: camps}
	}
	var skip logSkipTally
	notes := &syncCatalogNotes{}
	base := syncStreamOpts{
		Creds: creds, State: st, syncPath: target.syncPath,
		mapScope:      campaignMapScope{board: target.board, allBoards: target.allBoards},
		manifestStore: target.manifestStore, Upper: upper, Skip: &skip, Catalog: notes,
	}
	clickOpts := base
	clickOpts.Cursor = &st.ClicksTo
	clickOpts.Back = keitaroSyncClickBack
	clickOpts.Clicks = true
	clicks, err := s.runSyncStream(ctx, clickOpts)
	if err != nil {
		return keitaroSyncPassResult{ErrText: sanitize(err, creds.Key), Clicks: clicks, Skipped: skip.total(), Campaigns: camps}
	}
	convOpts := base
	convOpts.Cursor = &st.ConversionsTo
	convOpts.Back = keitaroSyncConvBack
	convOpts.Conversions = true
	convs, err := s.runSyncStream(ctx, convOpts)
	if err != nil {
		return keitaroSyncPassResult{ErrText: sanitize(err, creds.Key), Clicks: clicks, Conversions: convs, Skipped: skip.total(), Campaigns: camps}
	}
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	sk := skip.total()
	msg := fmt.Sprintf("clicks %d, conversions %d", clicks, convs)
	if sk > 0 {
		msg += fmt.Sprintf(", skipped %d", sk)
	}
	msg += notes.suffix()
	return keitaroSyncPassResult{OK: true, Message: msg, Clicks: clicks, Conversions: convs, Skipped: sk, Campaigns: camps}
}

// keitaroSyncCatalogCheck tops up the catalog from every id stored rows of the tracker host refer to,
// and the domain catalog from every domain id that campaigns of the connection refer to.
// Each part runs until one pass finishes without a cap cut or a failure, then sets st.CatalogChecked
// or st.DomainsChecked; a host change clears both flags. Later passes rely on the ids of fetched rows.
// A warehouse without CatalogRefs leaves CatalogChecked unset. Failures go to notes and never fail the pass.
// After a failure of the warehouse, the groups, the tracker or a store the check is skipped for
// keitaroCatalogRetry; a cap cut is not a failure and the next pass continues at once.
func (s *Server) keitaroSyncCatalogCheck(ctx context.Context, target keitaroSyncTarget, st *keitaroSyncFile, notes *syncCatalogNotes) {
	key := catalogRetryKey(target)
	now := s.keitaroSyncClock()
	if s.catalogCheckPaused(key, now) {
		return
	}
	failed := false
	defer func() {
		if failed {
			s.pauseCatalogCheck(key, now.Add(keitaroCatalogRetry))
		}
	}()
	refs := newCatalogRefs()
	catalogRead := false
	if !st.CatalogChecked {
		warehouse, ok, err := s.warehouseCatalogRefs(ctx, target.creds.host())
		switch {
		case ok && err != nil:
			failed = true
			s.logWarn("keitaro catalog refs failed", "err", err)
			notes.collect(0, []ktimport.Loss{{Ref: catalogTopUpRef, Detail: "дозагрузка каталога: clickhouse недоступен"}})
		case ok:
			refs, catalogRead = warehouse, true
		}
	}
	domainsRead := false
	if !st.DomainsChecked {
		if err := s.groupDomainRefs(ctx, campaignMapScope{board: target.board, allBoards: target.allBoards}, refs); err != nil {
			failed = true
			s.logWarn("keitaro domain refs failed", "err", err)
			notes.collect(0, []ktimport.Loss{{Ref: catalogTopUpRef, Detail: "дозагрузка каталога: кампании не прочитаны"}})
		} else {
			domainsRead = true
		}
	}
	if !catalogRead && !domainsRead {
		return
	}
	top := s.topUpKeitaroCatalog(ctx, target.creds, target.manifestStore, refs)
	notes.collect(top.total(), top.losses)
	failed = failed || top.failed
	if catalogRead {
		st.CatalogChecked = top.complete
	}
	if domainsRead {
		st.DomainsChecked = top.complete
	}
}

// keitaroCatalogRetry is the wait before a catalog check that failed runs again.
const keitaroCatalogRetry = time.Hour

// catalogRetryKey names the sync target and tracker host a catalog check pause belongs to.
func catalogRetryKey(target keitaroSyncTarget) string {
	return target.syncPath + "|" + target.creds.host()
}

// catalogCheckPaused reports whether the check under key failed less than keitaroCatalogRetry ago.
// now is the sync clock. An expired pause is forgotten.
func (s *Server) catalogCheckPaused(key string, now time.Time) bool {
	s.catalogRetryMu.Lock()
	defer s.catalogRetryMu.Unlock()
	until, ok := s.catalogRetry[key]
	if ok && !now.Before(until) {
		delete(s.catalogRetry, key)
		return false
	}
	return ok
}

// pauseCatalogCheck blocks the check under key until the given time.
func (s *Server) pauseCatalogCheck(key string, until time.Time) {
	s.catalogRetryMu.Lock()
	defer s.catalogRetryMu.Unlock()
	if s.catalogRetry == nil {
		s.catalogRetry = map[string]time.Time{}
	}
	s.catalogRetry[key] = until
}

// runSyncStream fetches Keitaro log chunks in [cursor−back, upper] and advances cursor.
func (s *Server) runSyncStream(ctx context.Context, o syncStreamOpts) (int, error) {
	if o.Cursor == nil {
		return 0, nil
	}
	runStart := *o.Cursor
	winFrom, winTo := syncWindowBounds(runStart, o.Upper, o.Back)
	if !winTo.After(winFrom) {
		return 0, nil
	}
	inserted := 0
	for chunkStart := winFrom; chunkStart.Before(winTo); {
		if err := ctx.Err(); err != nil {
			return inserted, err
		}
		chunkEnd := chunkStart.Add(keitaroSyncChunk)
		if chunkEnd.After(winTo) {
			chunkEnd = winTo
		}
		if !chunkEnd.After(chunkStart) {
			break
		}
		fromS, toS := formatKeitaroWindow(chunkStart, chunkEnd)
		counts, losses, stats, err := s.importLogWindow(ctx, o.Creds, fromS, toS, logImportOpts{
			clicks: o.Clicks, conversions: o.Conversions, syncMode: true, skip: o.Skip,
			mapScope: o.mapScope, manifestStore: o.manifestStore,
		})
		o.Catalog.collect(counts["catalog_added"], losses)
		if err != nil {
			return inserted, err
		}
		var chunkMax time.Time
		if o.Clicks {
			inserted += counts["clicks"]
			chunkMax = stats.maxClick
		} else {
			inserted += counts["conversions"]
			chunkMax = stats.maxConv
		}
		if !chunkMax.IsZero() && chunkMax.After(chunkEnd.Add(keitaroSyncFutureSkew)) {
			return inserted, errors.New(keitaroSyncTimezone)
		}
		if chunkEnd.After(*o.Cursor) {
			*o.Cursor = chunkEnd
		}
		if err := s.saveKeitaroSyncAt(o.syncPath, *o.State); err != nil {
			return inserted, errors.New("save error")
		}
		chunkStart = chunkEnd
	}
	return inserted, nil
}

// keitaroSyncCheckTimes rejects cursors more than keitaroSyncFutureSkew ahead of now.
func keitaroSyncCheckTimes(clicks, convs time.Time, now time.Time) error {
	limit := now.Add(keitaroSyncFutureSkew)
	if !clicks.IsZero() && clicks.After(limit) {
		return errors.New(keitaroSyncTimezone)
	}
	if !convs.IsZero() && convs.After(limit) {
		return errors.New(keitaroSyncTimezone)
	}
	return nil
}

// keitaroSyncBootstrap sets cursors from manifest and warehouse watermarks when a full import exists.
// ready is false with errMsg when a manual import is still required. err is a load or watermark failure.
func (s *Server) keitaroSyncBootstrap(ctx context.Context, creds ktCreds, st *keitaroSyncFile, manifestStore ManifestStore) (ready bool, errMsg string, err error) {
	manifest, err := loadManifestStore(manifestStore)
	if err != nil {
		return false, "", err
	}
	host := creds.host()
	if host == "" || len(manifest.Campaigns) == 0 {
		return false, keitaroSyncNeedImport, nil
	}
	mhost := strings.TrimSpace(manifest.Host)
	if mhost == "" || !strings.EqualFold(mhost, host) {
		return false, keitaroSyncNeedImport, nil
	}
	clicks, convs, err := s.cfg.Warehouse.InitialWatermark(ctx, host)
	if err != nil {
		return false, "", err
	}
	now := s.keitaroSyncClock()
	if err := keitaroSyncCheckTimes(clicks, convs, now); err != nil {
		return false, "", err
	}
	st.Host = host
	st.ClicksTo = clicks
	st.ConversionsTo = convs
	if st.ClicksTo.IsZero() && !st.ConversionsTo.IsZero() {
		st.ClicksTo = st.ConversionsTo
	}
	if st.ConversionsTo.IsZero() && !st.ClicksTo.IsZero() {
		st.ConversionsTo = st.ClicksTo
	}
	if st.ClicksTo.IsZero() && st.ConversionsTo.IsZero() {
		return false, keitaroSyncNeedImport, nil
	}
	return true, "", nil
}

// formatKeitaroWindow formats UTC bounds for Keitaro log API query parameters.
func formatKeitaroWindow(from, to time.Time) (string, string) {
	return from.UTC().Format(keitaroTimeLayout), to.UTC().Format(keitaroTimeLayout)
}

// removeKeitaroSyncFile deletes the sync state file; missing path or file is ignored.
func removeKeitaroSyncFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}
