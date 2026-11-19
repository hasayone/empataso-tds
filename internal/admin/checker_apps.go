package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/hasayone/empataso-tds/internal/config"
)

// States of an application record in checker-apps.json.
const (
	// checkerStDraft is a record whose fields are saved and not yet sent.
	checkerStDraft = "draft"
	// checkerStQueued is a record waiting for the queue.
	checkerStQueued = "queued"
	// checkerStSending is a record the queue works on; a restart turns it back into queued.
	checkerStSending = "sending"
	// checkerStLinked is a record tied to an application of the checker.
	checkerStLinked = "linked"
	// checkerStError is a record whose creation failed for good.
	checkerStError = "error"
)

const (
	// checkerFileVersion is the version of checker-apps.json.
	checkerFileVersion = 1
	// checkerMaxQueued is the most records waiting or being sent at once.
	checkerMaxQueued = 200
	// checkerMaxPosts is the most applications the queue creates per hour.
	checkerMaxPosts = 30
	// checkerRefreshGap is the least time between two status refreshes of one campaign.
	checkerRefreshGap = 60 * time.Second
	// checkerConsolesTTL is how long the list of consoles is kept.
	checkerConsolesTTL = 10 * time.Minute
	// checkerOrphanAge is how long a record of a deleted campaign is kept.
	checkerOrphanAge = 30 * 24 * time.Hour
	// checkerMaxConsoleID and checkerMaxConsoleName bound the console fields of a record.
	checkerMaxConsoleID   = 100
	checkerMaxConsoleName = 255
	// checkerAppBody is the largest body of a record request.
	checkerAppBody = 1 << 16
	// checkerStoreStatus is the status a new application starts with in the checker.
	checkerStoreStatus = "store_watch"
	// msgCheckerQueueFull is the answer when 200 records already wait.
	msgCheckerQueueFull = "очередь заполнена"
)

// checkerApp is the checker application of one campaign: the input of the user, the state of
// the queue and what the checker answered. Tenant fingerprints the connection the record is
// bound to. StatusAt, NextAttemptAt, CreatedAt and UpdatedAt are RFC 3339 times or empty.
// Existed marks an application that was in the checker already: it was linked and no keyword
// was sent.
type checkerApp struct {
	Keyword       string `json:"keyword"`
	BundleID      string `json:"bundle_id"`
	Name          string `json:"name"`
	NotionURL     string `json:"notion_url"`
	ConsoleID     string `json:"console_id"`
	ConsoleName   string `json:"console_name"`
	State         string `json:"state"`
	AppID         string `json:"app_id"`
	Status        string `json:"status"`
	StatusAt      string `json:"status_at"`
	Tenant        string `json:"tenant"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at"`
	LastError     string `json:"last_error"`
	KeywordsSent  int    `json:"keywords_sent"`
	Existed       bool   `json:"existed"`
	CreatedBy     string `json:"created_by"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// checkerAppsFile is the layout of checker-apps.json: the records by campaign id.
type checkerAppsFile struct {
	Version int                   `json:"version"`
	Apps    map[string]checkerApp `json:"apps"`
}

// checkerState is everything the checker integration keeps in memory.
// cfgMu guards cfg and cfgErr, the settings of checker.json. mu is the single checkerMu lock: it
// guards apps, appsErr, refresh and posts and is held for every change of a record and the
// write of checker-apps.json, never during a call to the checker. paused is set after the
// checker refused the token. kick wakes the queue. gate paces the requests. The consoles cache
// has its own lock.
type checkerState struct {
	cfgMu  sync.RWMutex
	cfg    checkerSettings
	cfgErr string

	mu      sync.Mutex
	apps    map[string]checkerApp
	appsErr string
	refresh map[string]time.Time
	posts   []time.Time

	paused atomic.Bool
	kick   chan struct{}
	gate   checkerGate
	// saves counts the writes of checker-apps.json.
	saves atomic.Int64

	consolesMu  sync.Mutex
	consolesKey string
	consolesAt  time.Time
	consoles    []checkerConsole
}

// checkerStamp formats t as an RFC 3339 UTC time.
func checkerStamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// checkerParseTime reads an RFC 3339 time; an empty or bad value gives the zero time.
func checkerParseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// checkerNow returns the current time of the integration.
func (s *Server) checkerNow() time.Time { return s.checker.gate.now() }

// checkerKick wakes the queue worker; a wake-up already waiting is enough.
func (s *Server) checkerKick() {
	select {
	case s.checker.kick <- struct{}{}:
	default:
	}
}

// loadChecker reads checker.json and checker-apps.json at start.
// A record in sending is turned back into queued, its next attempt time kept. A damaged
// checker-apps.json is not replaced: the reason is kept and every write is refused until the
// file is fixed, so the records in it are not lost.
func (s *Server) loadChecker() {
	c := &s.checker
	c.apps = map[string]checkerApp{}
	c.refresh = map[string]time.Time{}
	c.kick = make(chan struct{}, 1)
	s.loadCheckerSettings()
	if s.cfg.CheckerAppsFile == "" {
		return
	}
	raw, err := os.ReadFile(s.cfg.CheckerAppsFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var f checkerAppsFile
	switch {
	case err != nil:
		c.appsErr = "checker-apps.json cannot be read"
	case json.Unmarshal(raw, &f) != nil:
		c.appsErr = "checker-apps.json is not valid JSON"
	case f.Version != 0 && f.Version != checkerFileVersion:
		c.appsErr = "checker-apps.json has an unknown version"
	}
	if c.appsErr != "" {
		s.logWarn("checker queue disabled", "reason", c.appsErr)
		return
	}
	for id, a := range f.Apps {
		if a.State == checkerStSending {
			a.State = checkerStQueued
		}
		c.apps[id] = a
	}
}

// checkerSaveLocked writes checker-apps.json atomically with mode 0600.
// The caller holds checkerMu. It returns the write error; a server with no file keeps the
// records in memory only.
func (s *Server) checkerSaveLocked() error {
	if s.cfg.CheckerAppsFile == "" {
		return nil
	}
	raw, err := json.Marshal(checkerAppsFile{Version: checkerFileVersion, Apps: s.checker.apps})
	if err != nil {
		return err
	}
	s.checker.saves.Add(1)
	return atomicfile.Write(s.cfg.CheckerAppsFile, append(raw, '\n'), 0o600)
}

// checkerCommitLocked stores rec as the record of campaign id and writes the file.
// The caller holds checkerMu. When the write fails the previous record is put back and the
// error is returned.
func (s *Server) checkerCommitLocked(id string, rec checkerApp) error {
	prev, had := s.checker.apps[id]
	s.checker.apps[id] = rec
	if err := s.checkerSaveLocked(); err != nil {
		if had {
			s.checker.apps[id] = prev
		} else {
			delete(s.checker.apps, id)
		}
		return err
	}
	return nil
}

// checkerQueueCounts counts the records waiting or being sent, the records in error and the
// applications created in the last hour.
func (s *Server) checkerQueueCounts() checkerQueueView {
	now := s.checkerNow()
	s.checker.mu.Lock()
	defer s.checker.mu.Unlock()
	v := checkerQueueView{}
	for _, a := range s.checker.apps {
		switch a.State {
		case checkerStQueued, checkerStSending:
			v.Queued++
		case checkerStError:
			v.Errors++
		}
	}
	for _, t := range s.checker.posts {
		if now.Sub(t) < time.Hour {
			v.CreatedHour++
		}
	}
	return v
}

// checkerPrefill is what the campaign page offers before the user types: the keyword and the
// bundle id from the record or from the campaign name, the application name, the Keitaro link of
// the campaign and the Google Play link of the bundle.
type checkerPrefill struct {
	Keyword      string `json:"keyword"`
	BundleID     string `json:"bundle_id"`
	Name         string `json:"name"`
	KeitaroURL   string `json:"keitaro_url"`
	PlayStoreURL string `json:"play_store_url"`
}

// checkerAppAnswer is the body of the campaign routes.
// Foreign is true when the record is bound to another connection. AppURL is the finished link of
// the application page, empty when there is none. RetryAfter is the seconds until a refresh is allowed.
type checkerAppAnswer struct {
	Integration checkerIntegration `json:"integration"`
	App         *checkerApp        `json:"app"`
	Prefill     checkerPrefill     `json:"prefill"`
	Foreign     bool               `json:"foreign"`
	AppURL      string             `json:"app_url"`
	RetryAfter  int                `json:"retry_after"`
}

// checkerForeign reports whether a record is bound to another connection than cur.
func checkerForeign(a checkerApp, cur string) bool {
	return a.Tenant != "" && cur != "" && a.Tenant != cur
}

// checkerAnswer builds the answer of the campaign routes for g.
// set is the saved settings. The record is copied under the lock.
func (s *Server) checkerAnswer(g config.Group, set checkerSettings) checkerAppAnswer {
	now := s.checkerNow()
	s.checker.mu.Lock()
	rec, has := s.checker.apps[g.ID]
	last := s.checker.refresh[g.ID]
	s.checker.mu.Unlock()
	ans := checkerAppAnswer{Integration: checkerIntegration{Enabled: set.Enabled, CreateApps: set.CreateApps}}
	pre := checkerPrefill{Keyword: checkerKeywordFromName(g.Name), BundleID: checkerBundleFromName(g.Name)}
	if has {
		ans.App = &rec
		ans.Foreign = checkerForeign(rec, set.tenantKey())
		if rec.Keyword != "" {
			pre.Keyword = rec.Keyword
		}
		if rec.BundleID != "" {
			pre.BundleID = rec.BundleID
		}
		if rec.AppID != "" && set.AppURL != "" && !ans.Foreign {
			ans.AppURL = strings.ReplaceAll(set.AppURL, "{id}", url.PathEscape(rec.AppID))
		}
	}
	pre.Name = checkerAppName(g.Num, pre.Keyword)
	if set.AdminURL != "" {
		pre.KeitaroURL = set.AdminURL + "/#/flows/" + url.PathEscape(g.ID)
	}
	pre.PlayStoreURL = checkerPlayURL(pre.BundleID)
	ans.Prefill = pre
	if wait := last.Add(checkerRefreshGap).Sub(now); wait > 0 {
		ans.RetryAfter = int((wait + time.Second - 1) / time.Second)
	}
	return ans
}

// checkerCampaign loads the campaign named by the path and checks the caller against it.
// write demands the right to change the campaign, the same one PUT /api/groups/{id} demands.
// It returns the campaign, the full campaign list and the viewer. When it returns false it has
// written the answer: 503 without a groups store, 500 when the list fails, 404 for a campaign that
// is missing or hidden from the caller, 403 for a campaign the caller may read but not change.
func (s *Server) checkerCampaign(w http.ResponseWriter, r *http.Request, write bool) (config.Group, []config.Group, viewer, bool) {
	var none config.Group
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return none, nil, viewer{}, false
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return none, nil, viewer{}, false
	}
	g, ok := findGroup(groups, r.PathValue("group"))
	v := s.viewerFrom(r.Context())
	if !ok || !v.seesBoard(g.BoardID) || (!v.admin && !v.canSee(resCampaigns, campaignRef(g))) {
		writeErr(w, http.StatusNotFound, "not found")
		return none, nil, viewer{}, false
	}
	if write && !v.admin && !v.canWrite(resCampaigns, campaignRef(g)) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return none, nil, viewer{}, false
	}
	return g, groups, v, true
}

// checkerWritable reports whether the records can be changed.
// It writes 503 and returns false when no records file is configured or the file is damaged.
func (s *Server) checkerWritable(w http.ResponseWriter) bool {
	if s.cfg.CheckerAppsFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "checker state unavailable")
		return false
	}
	s.checker.mu.Lock()
	reason := s.checker.appsErr
	s.checker.mu.Unlock()
	if reason != "" {
		writeErr(w, http.StatusServiceUnavailable, reason)
		return false
	}
	return true
}

// handleCheckerAppGet returns the record of a campaign with the prefill and the integration state.
// The caller must see the campaign. A missing or hidden campaign is 404. No call leaves the server.
func (s *Server) handleCheckerAppGet(w http.ResponseWriter, r *http.Request) {
	g, _, _, ok := s.checkerCampaign(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.checkerAnswer(g, s.checkerSettings()))
}

// checkerAppPut is the body of PUT /api/checker/apps/{group}: the fields the user types.
type checkerAppPut struct {
	Keyword     string `json:"keyword"`
	BundleID    string `json:"bundle_id"`
	NotionURL   string `json:"notion_url"`
	ConsoleID   string `json:"console_id"`
	ConsoleName string `json:"console_name"`
}

// checkerFieldsProblem trims the fields of a body and checks each of them.
// It returns the trimmed fields and an empty message, or the client message of the first bad
// field. Empty fields are allowed here; create demands the keyword and the bundle itself.
func checkerFieldsProblem(b checkerAppPut) (checkerAppPut, string) {
	b.Keyword, b.BundleID = strings.TrimSpace(b.Keyword), strings.TrimSpace(b.BundleID)
	b.NotionURL, b.ConsoleID = strings.TrimSpace(b.NotionURL), strings.TrimSpace(b.ConsoleID)
	b.ConsoleName = strings.TrimSpace(b.ConsoleName)
	if msg := checkerKeywordProblem(b.Keyword); msg != "" {
		return b, msg
	}
	if b.BundleID != "" {
		if msg := checkerBundleProblem(b.BundleID); msg != "" {
			return b, msg
		}
	}
	if msg := checkerHTTPSProblem("notion_url", b.NotionURL, false); msg != "" {
		return b, msg
	}
	if utf8.RuneCountInString(b.ConsoleID) > checkerMaxConsoleID {
		return b, "console_id is too long"
	}
	if utf8.RuneCountInString(b.ConsoleName) > checkerMaxConsoleName {
		return b, "console_name is too long"
	}
	return b, ""
}

// checkerBundleTaken looks for another live record that holds bundle.
// id is the campaign asking and groups the campaign list; a record of a deleted campaign is not live.
// The caller holds checkerMu. It returns the number of the campaign that holds the bundle and true.
func (s *Server) checkerBundleTaken(id, bundle string, groups []config.Group) (int, bool) {
	if bundle == "" {
		return 0, false
	}
	for _, g := range groups {
		if a, ok := s.checker.apps[g.ID]; ok && g.ID != id && a.BundleID == bundle {
			return g.Num, true
		}
	}
	return 0, false
}

// checkerAppTaken looks for another live record linked to the checker application appID.
// It works like checkerBundleTaken.
func (s *Server) checkerAppTaken(id, appID string, groups []config.Group) (int, bool) {
	if appID == "" {
		return 0, false
	}
	for _, g := range groups {
		if a, ok := s.checker.apps[g.ID]; ok && g.ID != id && a.AppID == appID {
			return g.Num, true
		}
	}
	return 0, false
}

// checkerBundleTakenMsg is the 409 text for a bundle held by campaign num.
func checkerBundleTakenMsg(num int) string {
	return "bundle уже у кампании " + strconv.Itoa(num)
}

// handleCheckerAppPut saves the fields of a campaign record without sending anything.
// The caller needs the right to change the campaign. The body is {keyword, bundle_id, notion_url,
// console_id, console_name} and replaces the stored fields. A campaign without a record gets a draft.
// Answers: 400 for a bad field (a keyword with ":" or a line break, a bundle outside
// ^[a-z0-9.]+$ with a dot), 409 when the record is linked or being sent or the bundle belongs to
// another campaign, 503 when the records cannot be written, 500 "save error" on a write failure.
// Success is 200 with the body of GET.
func (s *Server) handleCheckerAppPut(w http.ResponseWriter, r *http.Request) {
	g, groups, v, ok := s.checkerCampaign(w, r, true)
	if !ok || !s.checkerWritable(w) {
		return
	}
	var body checkerAppPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkerAppBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	body, msg := checkerFieldsProblem(body)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	now := checkerStamp(s.checkerNow())
	s.checker.mu.Lock()
	rec, had := s.checker.apps[g.ID]
	if had && (rec.State == checkerStLinked || rec.State == checkerStSending) {
		s.checker.mu.Unlock()
		writeErr(w, http.StatusConflict, "application is "+rec.State+", fields are closed")
		return
	}
	if num, taken := s.checkerBundleTaken(g.ID, body.BundleID, groups); taken {
		s.checker.mu.Unlock()
		writeErr(w, http.StatusConflict, checkerBundleTakenMsg(num))
		return
	}
	if !had {
		rec = checkerApp{State: checkerStDraft, CreatedBy: v.uid, CreatedAt: now}
	}
	rec.Keyword, rec.BundleID, rec.NotionURL = body.Keyword, body.BundleID, body.NotionURL
	rec.ConsoleID, rec.ConsoleName, rec.UpdatedAt = body.ConsoleID, body.ConsoleName, now
	rec.Name = ""
	if rec.Keyword != "" {
		rec.Name = checkerAppName(g.Num, rec.Keyword)
	}
	err := s.checkerCommitLocked(g.ID, rec)
	s.checker.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, s.checkerAnswer(g, s.checkerSettings()))
}

// checkerFillFromName completes an empty keyword and an empty bundle id of rec from the campaign name.
// name is the campaign name. It returns rec with the fields it could read.
func checkerFillFromName(rec checkerApp, name string) checkerApp {
	if rec.Keyword == "" {
		rec.Keyword = checkerKeywordFromName(name)
	}
	if rec.BundleID == "" {
		rec.BundleID = checkerBundleFromName(name)
	}
	return rec
}

// checkerCreateProblem checks a record before it is queued.
// It returns the HTTP status and message of the first problem, or zero: the keyword and the bundle
// id are required and valid.
func checkerCreateProblem(rec checkerApp) (int, string) {
	if rec.Keyword == "" {
		return http.StatusBadRequest, "keyword is required"
	}
	if msg := checkerKeywordProblem(rec.Keyword); msg != "" {
		return http.StatusBadRequest, msg
	}
	if rec.BundleID == "" {
		return http.StatusBadRequest, "bundle_id is required"
	}
	if msg := checkerBundleProblem(rec.BundleID); msg != "" {
		return http.StatusBadRequest, msg
	}
	return 0, ""
}

// handleCheckerAppCreate queues the creation of the application of a campaign.
// The caller needs the right to change the campaign. An empty keyword or bundle id is read from
// the campaign name and saved. Nothing is sent here: the queue sends later.
// Answers: 409 "integration disabled" while the integration is off (no record is made); 400 for a
// missing or bad keyword or bundle; 409 when another campaign holds the bundle; 429 "очередь
// заполнена" when 200 records already wait; 503 when the records cannot be written. A record that is
// queued, being sent or linked to the current connection is returned as it is with 200. A draft, a
// record in error, and a record of another connection become queued with 202; the body is the one of GET.
func (s *Server) handleCheckerAppCreate(w http.ResponseWriter, r *http.Request) {
	g, groups, v, ok := s.checkerCampaign(w, r, true)
	if !ok || !s.checkerWritable(w) {
		return
	}
	set := s.checkerSettings()
	if !set.Enabled {
		writeErr(w, http.StatusConflict, msgCheckerDisabled)
		return
	}
	cur := set.tenantKey()
	nowT := s.checkerNow()
	now := checkerStamp(nowT)
	s.checker.mu.Lock()
	rec, had := s.checker.apps[g.ID]
	if had && !checkerForeign(rec, cur) && (rec.State == checkerStQueued || rec.State == checkerStSending || rec.State == checkerStLinked) {
		s.checker.mu.Unlock()
		writeJSON(w, http.StatusOK, s.checkerAnswer(g, set))
		return
	}
	if !had {
		rec = checkerApp{CreatedBy: v.uid, CreatedAt: now}
	}
	rec = checkerFillFromName(rec, g.Name)
	if code, msg := checkerCreateProblem(rec); code != 0 {
		s.checker.mu.Unlock()
		writeErr(w, code, msg)
		return
	}
	if num, taken := s.checkerBundleTaken(g.ID, rec.BundleID, groups); taken {
		s.checker.mu.Unlock()
		writeErr(w, http.StatusConflict, checkerBundleTakenMsg(num))
		return
	}
	if s.checkerQueuedLocked(g.ID) >= checkerMaxQueued {
		s.checker.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, msgCheckerQueueFull)
		return
	}
	rec.Name = checkerAppName(g.Num, rec.Keyword)
	rec.State, rec.Attempts, rec.NextAttemptAt, rec.Tenant, rec.LastError = checkerStQueued, 0, now, cur, ""
	rec.AppID, rec.Status, rec.StatusAt, rec.KeywordsSent, rec.Existed = "", "", "", 0, false
	rec.UpdatedAt = now
	err := s.checkerCommitLocked(g.ID, rec)
	s.checker.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	log.Printf("checker: campaign=%s bundle=%s result=queued", g.ID, rec.BundleID)
	s.checkerKick()
	writeJSON(w, http.StatusAccepted, s.checkerAnswer(g, set))
}

// checkerQueuedLocked counts the records waiting or being sent, except the one of campaign skip.
// The caller holds checkerMu.
func (s *Server) checkerQueuedLocked(skip string) int {
	n := 0
	for id, a := range s.checker.apps {
		if id != skip && (a.State == checkerStQueued || a.State == checkerStSending) {
			n++
		}
	}
	return n
}

// handleCheckerAppRefresh reads the status of a linked application from the checker.
// The caller needs the right to change the campaign. One GET /api/apps/{id} is sent, and one
// refresh of a campaign is allowed per minute.
// Answers: 409 "integration disabled" while the integration is off; 409 when the record is not
// linked to the current connection; 429 with retry_after (seconds, in the body and in Retry-After)
// inside the minute; 502 with the text of the checker when the call fails. Success is 200 with the
// body of GET; status and status_at of the record are updated.
func (s *Server) handleCheckerAppRefresh(w http.ResponseWriter, r *http.Request) {
	g, _, _, ok := s.checkerCampaign(w, r, true)
	if !ok || !s.checkerWritable(w) {
		return
	}
	set := s.checkerSettings()
	if !set.Enabled || !set.ready() {
		writeErr(w, http.StatusConflict, msgCheckerDisabled)
		return
	}
	now := s.checkerNow()
	s.checker.mu.Lock()
	rec, has := s.checker.apps[g.ID]
	if !has || rec.State != checkerStLinked || rec.AppID == "" || checkerForeign(rec, set.tenantKey()) {
		s.checker.mu.Unlock()
		writeErr(w, http.StatusConflict, "application is not linked")
		return
	}
	if wait := s.checker.refresh[g.ID].Add(checkerRefreshGap).Sub(now); wait > 0 {
		s.checker.mu.Unlock()
		writeRetryLater(w, msgTooManyRequests, wait)
		return
	}
	s.checker.refresh[g.ID] = now
	s.checker.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), checkerTestTimeout)
	defer cancel()
	info, err := s.newCheckerClient(set).appByID(ctx, prioUser, rec.AppID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	stamp := checkerStamp(s.checkerNow())
	s.checker.mu.Lock()
	cur, still := s.checker.apps[g.ID]
	var serr error
	if still && cur.State == checkerStLinked && cur.AppID == rec.AppID {
		cur.Status, cur.StatusAt, cur.UpdatedAt = info.Status, stamp, stamp
		serr = s.checkerCommitLocked(g.ID, cur)
	}
	s.checker.mu.Unlock()
	if serr != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, s.checkerAnswer(g, set))
}

// handleCheckerConsoles lists the consoles of the checker as [{id, name}].
// The list is read with GET /api/consoles?per_page=200 and kept for ten minutes per connection.
// Answers: 409 "integration disabled" while the integration is off; 502 with the text of the
// checker when the call fails. Success is a JSON array.
func (s *Server) handleCheckerConsoles(w http.ResponseWriter, r *http.Request) {
	set := s.checkerSettings()
	if !set.Enabled || !set.ready() {
		writeErr(w, http.StatusConflict, msgCheckerDisabled)
		return
	}
	c := &s.checker
	c.consolesMu.Lock()
	defer c.consolesMu.Unlock()
	if key := set.tenantKey(); c.consoles != nil && c.consolesKey == key && s.checkerNow().Sub(c.consolesAt) < checkerConsolesTTL {
		writeJSON(w, http.StatusOK, c.consoles)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkerTestTimeout)
	defer cancel()
	list, err := s.newCheckerClient(set).consoles(ctx, prioUser)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	c.consoles, c.consolesKey, c.consolesAt = list, set.tenantKey(), s.checkerNow()
	writeJSON(w, http.StatusOK, list)
}

// checkerPruneOrphans drops the records of deleted campaigns that were last changed more than 30
// days ago and writes the file when it dropped any.
// live is the set of campaign ids that exist. A write error is logged and the records stay in memory.
func (s *Server) checkerPruneOrphans(live map[string]bool) {
	cutoff := s.checkerNow().Add(-checkerOrphanAge)
	s.checker.mu.Lock()
	defer s.checker.mu.Unlock()
	dropped := false
	for id, a := range s.checker.apps {
		if !live[id] && checkerParseTime(a.UpdatedAt).Before(cutoff) {
			delete(s.checker.apps, id)
			dropped = true
		}
	}
	if dropped {
		if err := s.checkerSaveLocked(); err != nil {
			log.Printf("checker: cannot write the records file: %v", err)
		}
	}
}
