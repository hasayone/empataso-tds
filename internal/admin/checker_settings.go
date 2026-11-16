package admin

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// checkerSettingsBody is the largest body of a settings request.
	checkerSettingsBody = 1 << 16
	// checkerTestTimeout bounds the connection test, the wait for the request gate included.
	checkerTestTimeout = 60 * time.Second
	// checkerHostSuffix is the end every checker host must have.
	checkerHostSuffix = ".checkmy.store"
	// checkerMaxLocales is the most locales a settings list may hold.
	checkerMaxLocales = 200
	// checkerMaxTenant is the longest X-Tenant-ID.
	checkerMaxTenant = 100
	// checkerMaxURL is the longest admin_url and app_url.
	checkerMaxURL = 500
	// msgCheckerDisabled is the answer of a route that needs the integration switched on.
	msgCheckerDisabled = "integration disabled"
	// msgCheckerNotReady is the answer when the address, the tenant or the token is not saved.
	msgCheckerNotReady = "base_url, tenant_id and token are required"
)

var (
	// checkerLocaleRe is the shape of a locale code: "af", "JP", "hy-AM", "es-419".
	checkerLocaleRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,3})?$`)
	// checkerTokenRe is the shape of a token: printable ASCII without spaces.
	checkerTokenRe = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
)

//go:embed checker_locales.json
var checkerLocalesJSON []byte

// checkerLocale is one locale of the built-in list: the code the checker takes and its label.
type checkerLocale struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// checkerDefaultLocales returns the built-in list of locales, read once from checker_locales.json.
// It returns an empty list when the embedded file is not valid JSON; a test guards the file.
var checkerDefaultLocales = sync.OnceValue(func() []checkerLocale {
	var list []checkerLocale
	if json.Unmarshal(checkerLocalesJSON, &list) != nil {
		return []checkerLocale{}
	}
	return list
})

// checkerSettings is checker.json: the connection, the switches and the lists.
// Token is the Sanctum token. Enabled switches the whole integration and CreateApps the
// creation of applications by the queue. AdminURL is the address of this admin for the
// Keitaro link of an application and AppURL the template of an application page with {id}.
// Locales is the own list of codes; an empty list means the built-in one.
// PostbackURL is the template of the status postback (empty means the default one built from
// BaseURL), PostbackMethod is GET or POST (empty means GET) and PostbackToken is the tracking
// token that replaces {checker_token} in the template.
type checkerSettings struct {
	BaseURL        string   `json:"base_url"`
	TenantID       string   `json:"tenant_id"`
	Token          string   `json:"token"`
	Enabled        bool     `json:"enabled"`
	CreateApps     bool     `json:"create_apps"`
	AdminURL       string   `json:"admin_url"`
	AppURL         string   `json:"app_url"`
	Locales        []string `json:"locales"`
	PostbackURL    string   `json:"postback_url"`
	PostbackMethod string   `json:"postback_method"`
	PostbackToken  string   `json:"postback_token"`
}

// ready reports whether the connection is complete: an address, a tenant and a token.
func (c checkerSettings) ready() bool {
	return c.BaseURL != "" && c.TenantID != "" && c.Token != ""
}

// tenantKey returns the fingerprint of the connection that a stored application is bound to.
func (c checkerSettings) tenantKey() string {
	if !c.ready() {
		return ""
	}
	return c.BaseURL + "|" + c.TenantID
}

// localeCodes returns the codes the queue sends: the own list, or the built-in one when it is empty.
func (c checkerSettings) localeCodes() []string {
	if len(c.Locales) > 0 {
		return append([]string(nil), c.Locales...)
	}
	def := checkerDefaultLocales()
	out := make([]string, 0, len(def))
	for _, l := range def {
		out = append(out, l.Code)
	}
	return out
}

// checkerBaseProblem normalizes and checks the checker address.
// raw is the address as typed. It returns the address as https://host without a trailing slash and
// an empty message, or an empty address and the client message when the address is not an
// https address of a host that ends in .checkmy.store, or has a port, a path, a query or userinfo.
func checkerBaseProblem(raw string) (string, string) {
	const bad = "base_url must be https://<name>.checkmy.store without a path"
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Port() != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", bad
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, checkerHostSuffix) || len(host) == len(checkerHostSuffix) {
		return "", bad
	}
	return "https://" + host, ""
}

// checkerHTTPSProblem checks an https address field.
// field names the field in the message and v is the trimmed value; an empty value is allowed.
// needID demands the {id} placeholder, which is replaced by a letter for the address check.
// It returns the client message, or an empty string when the value is usable.
func checkerHTTPSProblem(field, v string, needID bool) string {
	if v == "" {
		return ""
	}
	probe := v
	if needID {
		if !strings.Contains(v, "{id}") {
			return field + " must contain {id}"
		}
		probe = strings.ReplaceAll(v, "{id}", "x")
	}
	u, err := url.Parse(probe)
	if len(v) > checkerMaxURL || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return field + " must be an https address of at most 500 characters"
	}
	return ""
}

// validate checks a stored or merged settings value the way PUT does.
// It returns an empty string when the value is usable, otherwise a client message naming the field.
// An empty field is allowed: the integration is then not complete.
func (c checkerSettings) validate() string {
	if c.BaseURL != "" {
		if _, msg := checkerBaseProblem(c.BaseURL); msg != "" {
			return msg
		}
	}
	if c.TenantID != "" && (utf8.RuneCountInString(c.TenantID) > checkerMaxTenant || strings.IndexFunc(c.TenantID, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0) {
		return "tenant_id must be 1 to 100 characters without spaces"
	}
	if c.Token != "" && !checkerTokenRe.MatchString(c.Token) {
		return "token is not valid"
	}
	if c.PostbackToken != "" && !checkerTokenRe.MatchString(c.PostbackToken) {
		return "postback_token is not valid"
	}
	if msg := checkerPostbackProblem(c.PostbackURL, c.PostbackMethod); msg != "" {
		return msg
	}
	if msg := checkerHTTPSProblem("admin_url", c.AdminURL, false); msg != "" {
		return msg
	}
	if msg := checkerHTTPSProblem("app_url", c.AppURL, true); msg != "" {
		return msg
	}
	if len(c.Locales) > checkerMaxLocales {
		return "at most 200 locales"
	}
	seen := make(map[string]bool, len(c.Locales))
	for _, l := range c.Locales {
		if !checkerLocaleRe.MatchString(l) {
			return "locale " + l + " is not valid"
		}
		if seen[strings.ToLower(l)] {
			return "locale " + l + " is listed twice"
		}
		seen[strings.ToLower(l)] = true
	}
	return ""
}

// checkerSettingsPut is the body of PUT and POST test. A missing field keeps the stored value;
// a masked token keeps the stored token, and so does a masked postback token; an empty admin_url,
// app_url, postback_url or locales list clears it.
type checkerSettingsPut struct {
	BaseURL        *string   `json:"base_url"`
	TenantID       *string   `json:"tenant_id"`
	Token          *string   `json:"token"`
	Enabled        *bool     `json:"enabled"`
	CreateApps     *bool     `json:"create_apps"`
	AdminURL       *string   `json:"admin_url"`
	AppURL         *string   `json:"app_url"`
	Locales        *[]string `json:"locales"`
	PostbackURL    *string   `json:"postback_url"`
	PostbackMethod *string   `json:"postback_method"`
	PostbackToken  *string   `json:"postback_token"`
}

// mergeCheckerSettings applies a PUT body on top of the stored settings.
// cur is the stored value. It returns the settings to store, or a client message when the result
// is not valid or switches the integration on without an address, a tenant and a token.
func mergeCheckerSettings(cur checkerSettings, body checkerSettingsPut) (checkerSettings, string) {
	next := cur
	next.Locales = append([]string(nil), cur.Locales...)
	if body.BaseURL != nil && strings.TrimSpace(*body.BaseURL) != "" {
		base, msg := checkerBaseProblem(*body.BaseURL)
		if msg != "" {
			return cur, msg
		}
		next.BaseURL = base
	}
	if body.TenantID != nil && strings.TrimSpace(*body.TenantID) != "" {
		next.TenantID = strings.TrimSpace(*body.TenantID)
	}
	if body.Token != nil && !isKeitaroKeyMask(*body.Token) {
		next.Token = strings.TrimSpace(*body.Token)
	}
	if body.PostbackToken != nil && !isKeitaroKeyMask(*body.PostbackToken) {
		next.PostbackToken = strings.TrimSpace(*body.PostbackToken)
	}
	if body.PostbackURL != nil {
		next.PostbackURL = strings.TrimSpace(*body.PostbackURL)
	}
	if body.PostbackMethod != nil {
		next.PostbackMethod = strings.ToUpper(strings.TrimSpace(*body.PostbackMethod))
	}
	if body.Enabled != nil {
		next.Enabled = *body.Enabled
	}
	if body.CreateApps != nil {
		next.CreateApps = *body.CreateApps
	}
	if body.AdminURL != nil {
		next.AdminURL = strings.TrimRight(strings.TrimSpace(*body.AdminURL), "/")
	}
	if body.AppURL != nil {
		next.AppURL = strings.TrimSpace(*body.AppURL)
	}
	if body.Locales != nil {
		next.Locales = nil
		for _, l := range *body.Locales {
			if l = strings.TrimSpace(l); l != "" {
				next.Locales = append(next.Locales, l)
			}
		}
	}
	if msg := next.validate(); msg != "" {
		return cur, msg
	}
	if next.Enabled && !next.ready() {
		return cur, msgCheckerNotReady
	}
	return next, ""
}

// checkerSettings returns a copy of the stored settings; they are empty after a damaged file.
func (s *Server) checkerSettings() checkerSettings {
	s.checker.cfgMu.RLock()
	defer s.checker.cfgMu.RUnlock()
	c := s.checker.cfg
	c.Locales = append([]string(nil), c.Locales...)
	return c
}

// checkerLoadError returns why checker.json was not used, or an empty string.
func (s *Server) checkerLoadError() string {
	s.checker.cfgMu.RLock()
	defer s.checker.cfgMu.RUnlock()
	return s.checker.cfgErr
}

// loadCheckerSettings reads checker.json at start. A missing file leaves the defaults.
// A file that cannot be read, is not JSON or fails the checks of PUT leaves the integration
// off: the reason is logged and shown by GET, and a PUT writes the file again.
// The token is never part of the reason.
func (s *Server) loadCheckerSettings() {
	if s.cfg.CheckerFile == "" {
		return
	}
	raw, err := os.ReadFile(s.cfg.CheckerFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	reason := ""
	var c checkerSettings
	switch {
	case err != nil:
		reason = "checker.json cannot be read"
	case json.Unmarshal(raw, &c) != nil:
		reason = "checker.json is not valid JSON"
	default:
		if msg := c.validate(); msg != "" {
			reason = "checker.json is not valid: " + msg
		}
	}
	s.checker.cfgMu.Lock()
	defer s.checker.cfgMu.Unlock()
	if reason != "" {
		s.checker.cfgErr = reason
		s.logWarn("checker disabled", "reason", reason)
		return
	}
	s.checker.cfg = c
}

// checkerQueueView counts the work of the queue: records waiting or being sent, records in
// error, and the applications created in the last hour.
type checkerQueueView struct {
	Queued      int `json:"queued"`
	Errors      int `json:"errors"`
	CreatedHour int `json:"created_hour"`
}

// checkerSettingsView is the body of GET and PUT /api/checker/settings.
// The token is never a field; HasToken only says it is set. Locales is the own list and
// DefaultLocales the built-in one. Error is set when checker.json could not be used and Paused
// after the checker refused the token. PostbackURL is the template as saved, with the token only
// as the {checker_token} placeholder; PostbackDefault is the template used while it is empty.
type checkerSettingsView struct {
	Error            string           `json:"error,omitempty"`
	BaseURL          string           `json:"base_url"`
	TenantID         string           `json:"tenant_id"`
	HasToken         bool             `json:"has_token"`
	Enabled          bool             `json:"enabled"`
	CreateApps       bool             `json:"create_apps"`
	AdminURL         string           `json:"admin_url"`
	AppURL           string           `json:"app_url"`
	Locales          []string         `json:"locales"`
	DefaultLocales   []checkerLocale  `json:"default_locales"`
	PostbackURL      string           `json:"postback_url"`
	PostbackDefault  string           `json:"postback_default"`
	PostbackMethod   string           `json:"postback_method"`
	HasPostbackToken bool             `json:"has_postback_token"`
	Paused           bool             `json:"paused"`
	Queue            checkerQueueView `json:"queue"`
}

// checkerView builds the public view of the settings.
func (s *Server) checkerView() checkerSettingsView {
	c := s.checkerSettings()
	locales := c.Locales
	if locales == nil {
		locales = []string{}
	}
	return checkerSettingsView{
		Error: s.checkerLoadError(), BaseURL: c.BaseURL, TenantID: c.TenantID, HasToken: c.Token != "",
		Enabled: c.Enabled, CreateApps: c.CreateApps, AdminURL: c.AdminURL, AppURL: c.AppURL,
		Locales: locales, DefaultLocales: checkerDefaultLocales(),
		PostbackURL: c.PostbackURL, PostbackDefault: checkerPostbackDefault(c.BaseURL),
		PostbackMethod: c.postbackMethod(), HasPostbackToken: c.PostbackToken != "",
		Paused: s.checker.paused.Load(), Queue: s.checkerQueueCounts(),
	}
}

// registerChecker mounts the checker routes. Every route carries the cookie flag in routeAccess,
// so none is open to an API key or to the assistant.
// s is the server and mux receives the routes.
func registerChecker(s *Server, mux *http.ServeMux) {
	s.handle(mux, "GET /api/checker/settings", http.HandlerFunc(s.handleCheckerSettingsGet))
	s.handle(mux, "PUT /api/checker/settings", http.HandlerFunc(s.handleCheckerSettingsPut))
	s.handle(mux, "POST /api/checker/test", http.HandlerFunc(s.handleCheckerTest))
	s.handle(mux, "GET /api/checker/keywords/preview", http.HandlerFunc(s.handleCheckerKeywordsPreview))
	s.handle(mux, "POST /api/checker/keywords/fill", http.HandlerFunc(s.handleCheckerKeywordsFill))
	s.handle(mux, "GET /api/checker/status", http.HandlerFunc(s.handleCheckerStatus))
	s.handle(mux, "GET /api/checker/consoles", http.HandlerFunc(s.handleCheckerConsoles))
	s.handle(mux, "GET /api/checker/apps/{group}", http.HandlerFunc(s.handleCheckerAppGet))
	s.handle(mux, "PUT /api/checker/apps/{group}", http.HandlerFunc(s.handleCheckerAppPut))
	s.handle(mux, "POST /api/checker/apps/{group}/create", http.HandlerFunc(s.handleCheckerAppCreate))
	s.handle(mux, "POST /api/checker/apps/{group}/refresh", http.HandlerFunc(s.handleCheckerAppRefresh))
}

// handleCheckerSettingsGet returns the settings without the token.
// When checker.json was damaged at start the body carries its reason in "error".
func (s *Server) handleCheckerSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.checkerView())
}

// handleCheckerSettingsPut merges the body into checker.json and writes it with mode 0600.
// The merge and the write happen under one lock, so two saves cannot interleave. A PUT also
// repairs a damaged file and lifts the pause after a refused token. Answers: 400 for bad JSON or
// a bad field, 503 when no file is configured, 500 "save error" when the file cannot be written.
// Success is 200 with the same body as GET. The token is never echoed or logged.
func (s *Server) handleCheckerSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body checkerSettingsPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkerSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.cfg.CheckerFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "checker settings unavailable")
		return
	}
	if !s.saveCheckerSettings(w, body) {
		return
	}
	s.checker.paused.Store(false)
	s.checkerKick()
	if err := s.syncCheckerPostbacks(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "postback sync error")
		return
	}
	writeJSON(w, http.StatusOK, s.checkerView())
}

// saveCheckerSettings merges body into the stored settings and writes the file.
// It returns false after writing the error answer to w.
func (s *Server) saveCheckerSettings(w http.ResponseWriter, body checkerSettingsPut) bool {
	s.checker.cfgMu.Lock()
	defer s.checker.cfgMu.Unlock()
	next, msg := mergeCheckerSettings(s.checker.cfg, body)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return false
	}
	raw, err := json.Marshal(next)
	if err == nil {
		err = writeFileAtomic(s.cfg.CheckerFile, append(raw, '\n'), 0o600)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return false
	}
	s.checker.cfg, s.checker.cfgErr = next, ""
	s.logInfo("checker settings saved")
	return true
}

// checkerTestView is the answer of the connection test. Status is the HTTP status of a refused call.
type checkerTestView struct {
	OK     bool         `json:"ok"`
	User   *checkerUser `json:"user,omitempty"`
	Error  string       `json:"error,omitempty"`
	Status int          `json:"status,omitempty"`
}

// handleCheckerTest checks the connection with GET /api/auth/me.
// The body is shaped like the PUT body; missing fields and a masked token fall back to the saved
// settings. Nothing is saved. When the merged settings have the integration off the answer is 409
// "integration disabled" and nothing leaves the server. A refused call is still 200 with ok false,
// the HTTP status and the short text of the checker; 400 means a field is wrong or the connection
// is incomplete. Success is {"ok": true, "user": {"name", "email"}}.
func (s *Server) handleCheckerTest(w http.ResponseWriter, r *http.Request) {
	var body checkerSettingsPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkerSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	set, msg := mergeCheckerSettings(s.checkerSettings(), body)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if !set.Enabled {
		writeErr(w, http.StatusConflict, msgCheckerDisabled)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkerTestTimeout)
	defer cancel()
	user, err := s.newCheckerClient(set).me(ctx, prioUser)
	if err != nil {
		view := checkerTestView{Error: err.Error()}
		var ce *checkerError
		if errors.As(err, &ce) {
			view.Status = ce.Status
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	writeJSON(w, http.StatusOK, checkerTestView{OK: true, User: &user})
}

// checkerStatusView is the body of GET /api/checker/status: the two switches and the status
// postback the campaign page offers.
type checkerStatusView struct {
	checkerIntegration
	Postback checkerPostbackView `json:"postback"`
}

// handleCheckerStatus tells the campaign page whether to show the checker block and the
// postback row. The answer is {"enabled", "create_apps", "postback": {"available", "method",
// "url_masked"}} of the saved settings; the postback token is replaced by a mask.
func (s *Server) handleCheckerStatus(w http.ResponseWriter, r *http.Request) {
	c := s.checkerSettings()
	writeJSON(w, http.StatusOK, checkerStatusView{
		checkerIntegration: checkerIntegration{Enabled: c.Enabled, CreateApps: c.CreateApps},
		Postback:           c.postbackView(),
	})
}

// checkerIntegration is the pair of switches the campaign page needs.
type checkerIntegration struct {
	Enabled    bool `json:"enabled"`
	CreateApps bool `json:"create_apps"`
}
