package admin

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/hasayone/empataso-tds/internal/security"
)

// section is a panel area a user may be granted. Names are stored in users.json access.sections.
type section string

const (
	secDashboard   section = "dashboard"
	secFlows       section = "flows"
	secOffers      section = "offers"
	secLandings    section = "landings"
	secNetworks    section = "networks"
	secSources     section = "sources"
	secDomains     section = "domains"
	secLogs        section = "logs"
	secConversions section = "conversions"
	secReport      section = "report"
	secKeywords    section = "keywords"
	secLists       section = "lists"
	secTools       section = "tools"
	secGroups      section = "groups"
	// secAssistant is the AI assistant. While aiAdminOnly is true it is in neither
	// userSections nor adminSections: a role cannot grant it and the panel does not list it.
	secAssistant section = "assistant"
	// Admin-only panel areas.
	secUsers         section = "users"
	secAPIKeys       section = "api_keys"
	secIntegrations  section = "integrations"
	secAutomation    section = "automation"
	secPages         section = "pages"
	secBotProtection section = "bot_protection"
	secSettings      section = "settings"
	// secAny marks routes open to every authenticated session.
	secAny section = ""
)

var (
	userSections = []section{
		secDashboard, secFlows, secOffers, secLandings, secNetworks, secSources,
		secDomains, secLogs, secConversions, secReport, secKeywords, secLists,
		secTools, secGroups,
	}
	adminSections = []section{
		secUsers, secAPIKeys, secIntegrations, secAutomation, secPages,
		secBotProtection, secSettings,
	}
	allSections = append(append([]section{}, userSections...), adminSections...)

	grantResources = []string{resCampaigns, resOffers, resLandings, resSources, resNetworks, resDomains}
	grantModes     = []security.Mode{security.ModeFull, security.ModeRead, security.ModeSelected, security.ModeCreated}
)

var (
	errAccessSection  = errors.New("unknown access section")
	errAccessResource = errors.New("unknown access resource")
	errAccessMode     = errors.New("unknown access mode")
	errAccessLimit    = errors.New("access list too long")
	errAccessHidden   = errors.New("unknown hidden field")
)

// isAccessErr reports whether err comes from normalizeAccess: an unknown
// section, resource, mode or hidden field, or a list that is too long.
func isAccessErr(err error) bool {
	return errors.Is(err, errAccessSection) || errors.Is(err, errAccessResource) ||
		errors.Is(err, errAccessMode) || errors.Is(err, errAccessLimit) || errors.Is(err, errAccessHidden)
}

// routeRule binds one mux pattern to a section and optional administrator-only flag.
//
// cookie marks a route that an Api-Key session may not call. action names the
// dangerous operation (one of the security.Action constants) an API key must
// hold; cookie sessions are not limited by it. heavy marks a route that reads
// large ranges of ClickHouse data.
type routeRule struct {
	sec    section
	admin  bool
	cookie bool
	action string
	heavy  bool
}

// cookieRoutes lists the routes closed to Api-Key sessions: sessions, accounts,
// keys, roles, integration secrets, domain connection and update write actions.
var cookieRoutes = append([]string{
	"POST /api/logout", "GET /api/me", "GET /api/me/views", "PUT /api/me/views", "POST /api/password",
	"GET /api/api-keys", "POST /api/api-keys", "POST /api/api-keys/{id}/rotate",
	"PUT /api/api-keys/{id}", "DELETE /api/api-keys/{id}",
	"GET /api/users", "POST /api/users", "GET /api/users/{id}", "PUT /api/users/{id}",
	"DELETE /api/users/{id}", "PUT /api/users/{id}/password", "PUT /api/users/{id}/access",
	"GET /api/roles", "POST /api/roles", "POST /api/roles/reload", "GET /api/roles/{id}",
	"PUT /api/roles/{id}", "DELETE /api/roles/{id}",
	"GET /api/keitaro/settings", "PUT /api/keitaro/settings", "POST /api/keitaro/check",
	"GET /api/cloudflare", "PUT /api/cloudflare", "POST /api/cloudflare/test",
	"GET /api/checker/settings", "PUT /api/checker/settings", "POST /api/checker/test",
	"GET /api/checker/keywords/preview", "POST /api/checker/keywords/fill",
	"GET /api/checker/status", "GET /api/checker/consoles",
	"GET /api/checker/apps/{group}", "PUT /api/checker/apps/{group}",
	"POST /api/checker/apps/{group}/create", "POST /api/checker/apps/{group}/refresh",
	"GET /api/docs/settings", "PUT /api/docs/settings",
	"GET /api/registrars/namecheap", "PUT /api/registrars/namecheap",
	"POST /api/registrars/namecheap/test", "POST /api/registrars/namecheap/check",
	"POST /api/registrars/namecheap/register",
	"POST /api/domains/{id}/ns", "POST /api/domains/{id}/cloudflare",
	"POST /api/domains/{id}/admin-dns", "POST /api/domains/connected/ack",
	"POST /api/update/apply", "POST /api/update/rollback", "POST /api/update/check",
	"GET /api/system", "GET /api/system/signals",
}, aiRoutePatterns...)

// actionRoutes maps each dangerous action to the routes it unlocks for an API key.
var actionRoutes = map[string][]string{
	security.ActionKeitaroImport: {"POST /api/keitaro/import"},
	security.ActionKeitaroPurge:  {"POST /api/keitaro/purge"},
	security.ActionLogsDelete:    {"DELETE /api/logs"},
	security.ActionGroupsReplace: {"PUT /api/groups"},
	security.ActionCron:          {"PUT /api/cron", "POST /api/cron/run", geoDownloadPattern},
	security.ActionBoards: {
		"POST /api/boards", "PUT /api/boards/order", "PATCH /api/boards/{id}",
		"PUT /api/boards/{id}/members", "POST /api/boards/{id}/assign",
		"DELETE /api/boards/{id}", "POST /api/boards/move", "POST /api/boards/import",
	},
	security.ActionLandingFiles: {
		"DELETE /api/landings/{id}",
		"POST /api/landings/{id}/archive", "PUT /api/landings/{id}/files/{path...}",
		"DELETE /api/landings/{id}/files/{path...}",
	},
	security.ActionListsRead: {
		"GET /api/lists", "GET /api/lists/{name}",
	},
}

// heavyRoutes lists the routes that scan large ranges of ClickHouse data.
// A heavy route takes one of the API key's keyHeavyMax slots; cookie sessions are never counted.
var heavyRoutes = []string{
	"GET /api/logs/export", "GET /api/postbacks/export", "GET /api/stats/facts", "GET /api/postback-log",
	"GET /api/stats/summary", "GET /api/stats/timeseries", "GET /api/stats/breakdown", "GET /api/stats/performance",
	"GET /api/report", "GET /api/logs", "GET /api/log-values", "GET /api/logs/filters", "GET /api/postbacks",
	"GET /api/keywords/stats", "GET /api/keywords/trend",
}

// flagRoutes applies set to the rule of every pattern in patterns.
// It panics on a pattern that has no rule: the flag tables are fixed at build
// time and a typo must not start the server.
func flagRoutes(m map[string]routeRule, patterns []string, set func(*routeRule)) {
	for _, p := range patterns {
		rr, ok := m[p]
		if !ok {
			// Intentional startup panic: a flag on a route without a rule is a typo in a build-time table.
			panic("admin: route flag for unknown route: " + p)
		}
		set(&rr)
		m[p] = rr
	}
}

// routeAccess maps mux pattern strings to access rules for every protected route.
var routeAccess = buildRouteAccess()

// buildRouteAccess returns the full mux-pattern to routeRule map used by handle.
func buildRouteAccess() map[string]routeRule {
	m := make(map[string]routeRule)
	open := func() routeRule { return routeRule{sec: secAny} }
	rule := func(sec section) routeRule {
		return routeRule{sec: sec, admin: isAdminSection(sec)}
	}
	addCRUD := func(base string, sec section) {
		rr := rule(sec)
		for _, p := range []string{
			"GET " + base,
			"POST " + base,
			"GET " + base + "/{id}",
			"PUT " + base + "/{id}",
			"DELETE " + base + "/{id}",
		} {
			m[p] = rr
		}
	}
	addCoreRoutes(m, open, rule)
	addCatalogRoutes(m, rule, addCRUD)
	addAdminRoutes(m, rule)
	addAssistantRoutes(m, open, rule)
	flagRoutes(m, cookieRoutes, func(rr *routeRule) { rr.cookie = true })
	flagRoutes(m, heavyRoutes, func(rr *routeRule) { rr.heavy = true })
	for action, patterns := range actionRoutes {
		flagRoutes(m, patterns, func(rr *routeRule) { rr.action = action })
	}
	return m
}

// addCoreRoutes registers session-open routes and dashboard, flows, logs, conversions, report, keywords.
func addCoreRoutes(m map[string]routeRule, open func() routeRule, rule func(section) routeRule) {
	m["POST /api/logout"] = open()
	m["GET /api/me"] = open()
	m["GET /api/me/views"] = open()
	m["PUT /api/me/views"] = open()
	m["POST /api/password"] = open()
	m["GET /api/notices"] = open()
	m["GET /api/log-fields"] = open()
	m["GET /api/ui"] = open()
	m["GET /api/openapi.json"] = open()
	m["GET /api/postman.json"] = open()
	m["GET /api/groups"] = open()
	m["GET /api/boards"] = open()
	m["POST /api/boards"] = routeRule{sec: secAny, admin: true}
	m["PUT /api/boards/order"] = routeRule{sec: secAny, admin: true}
	m["PATCH /api/boards/{id}"] = routeRule{sec: secAny, admin: true}
	m["DELETE /api/boards/{id}"] = routeRule{sec: secAny, admin: true}
	m["PUT /api/boards/{id}/members"] = routeRule{sec: secAny, admin: true}
	m["POST /api/boards/{id}/assign"] = routeRule{sec: secAny, admin: true}
	m["POST /api/boards/import"] = open()
	m["POST /api/boards/move"] = open()

	m["GET /api/stats/summary"] = rule(secDashboard)
	m["GET /api/stats/timeseries"] = rule(secDashboard)
	m["GET /api/stats/breakdown"] = rule(secDashboard)
	m["GET /api/stats/facts"] = rule(secDashboard)

	m["GET /api/stats/performance"] = rule(secFlows)
	m["PUT /api/groups"] = rule(secFlows)
	m["PUT /api/groups/{id}"] = rule(secFlows)
	m["DELETE /api/groups/{id}"] = rule(secFlows)
	m["GET /api/campaigns/{id}/postbacks"] = rule(secFlows)
	m["PUT /api/campaigns/{id}/postbacks"] = rule(secFlows)
	m["GET /api/s2s"] = rule(secFlows)
	m["PUT /api/s2s"] = routeRule{sec: secFlows, admin: true}
	m["GET /api/spend"] = rule(secFlows)
	m["POST /api/spend"] = rule(secFlows)
	m["DELETE /api/spend/{id}"] = rule(secFlows)

	m["GET /api/logs"] = rule(secLogs)
	m["GET /api/log-values"] = rule(secLogs)
	m["GET /api/logs/filters"] = rule(secLogs)
	m["GET /api/logs/export"] = rule(secLogs)
	m["DELETE /api/logs"] = routeRule{sec: secLogs, admin: true}

	m["GET /api/postbacks"] = rule(secConversions)
	m["GET /api/postbacks/export"] = rule(secConversions)
	m["GET /api/postback-log"] = rule(secConversions)

	m["GET /api/report"] = rule(secReport)
	m["GET /api/keys"] = rule(secKeywords)
	m["GET /api/keys/download"] = rule(secKeywords)
	m["GET /api/keywords/stats"] = rule(secKeywords)
	m["GET /api/keywords/trend"] = rule(secKeywords)
}

// addCatalogRoutes registers catalog CRUD, landing files, domain check, lists, and tools.
func addCatalogRoutes(m map[string]routeRule, rule func(section) routeRule, addCRUD func(string, section)) {
	addCRUD("/api/offers", secOffers)
	addCRUD("/api/landings", secLandings)
	addCRUD("/api/traffic-sources", secSources)
	addCRUD("/api/affiliate-networks", secNetworks)
	addCRUD("/api/domains", secDomains)

	land := rule(secLandings)
	m["GET /api/landings/{id}/files"] = land
	m["POST /api/landings/{id}/archive"] = land
	m["GET /api/landings/{id}/files/{path...}"] = land
	m["PUT /api/landings/{id}/files/{path...}"] = land
	m["DELETE /api/landings/{id}/files/{path...}"] = land

	m["POST /api/domains/{id}/check"] = rule(secDomains)
	m["POST /api/domains/{id}/ns"] = routeRule{sec: secDomains, admin: true}
	m["POST /api/domains/{id}/cloudflare"] = routeRule{sec: secDomains, admin: true}
	m["POST /api/domains/{id}/admin-dns"] = routeRule{sec: secDomains, admin: true}
	m["GET /api/domains/connected"] = rule(secDomains)
	m["POST /api/domains/connected/ack"] = rule(secDomains)
	m["GET /api/cloudflare"] = rule(secIntegrations)
	m["PUT /api/cloudflare"] = rule(secIntegrations)
	m["GET /api/docs/settings"] = rule(secIntegrations)
	m["PUT /api/docs/settings"] = rule(secIntegrations)
	m["POST /api/cloudflare/test"] = rule(secIntegrations)
	m["GET /api/checker/settings"] = rule(secIntegrations)
	m["PUT /api/checker/settings"] = rule(secIntegrations)
	m["POST /api/checker/test"] = rule(secIntegrations)
	m["GET /api/checker/keywords/preview"] = rule(secIntegrations)
	m["POST /api/checker/keywords/fill"] = rule(secIntegrations)
	m["GET /api/checker/status"] = rule(secFlows)
	m["GET /api/checker/consoles"] = rule(secFlows)
	m["GET /api/checker/apps/{group}"] = rule(secFlows)
	m["PUT /api/checker/apps/{group}"] = rule(secFlows)
	m["POST /api/checker/apps/{group}/create"] = rule(secFlows)
	m["POST /api/checker/apps/{group}/refresh"] = rule(secFlows)
	m["GET /api/registrars/namecheap"] = rule(secIntegrations)
	m["PUT /api/registrars/namecheap"] = rule(secIntegrations)
	m["POST /api/registrars/namecheap/test"] = rule(secIntegrations)
	m["POST /api/registrars/namecheap/check"] = routeRule{sec: secDomains, admin: true}
	m["POST /api/registrars/namecheap/register"] = routeRule{sec: secDomains, admin: true}

	m["GET /api/lists"] = rule(secLists)
	m["GET /api/lists/{name}"] = rule(secLists)
	m["PUT /api/lists/{name}"] = routeRule{sec: secLists, admin: true}

	m["GET /api/lookup"] = rule(secTools)
	m["GET /api/tools/lookups"] = rule(secTools)
	m["DELETE /api/tools/lookups"] = routeRule{sec: secTools, admin: true, cookie: true}
	m["GET /api/tools/geo"] = rule(secTools)
	m[geoDownloadPattern] = routeRule{sec: secSettings, admin: true}
	m["POST /api/simulate"] = rule(secTools)
}

// addAdminRoutes registers settings, bot protection, pages, cron, Keitaro, API keys, users, and roles.
func addAdminRoutes(m map[string]routeRule, rule func(section) routeRule) {
	m["PUT /api/ui"] = rule(secSettings)
	m["GET /api/bot-protection"] = rule(secBotProtection)
	m["PUT /api/bot-protection"] = rule(secBotProtection)

	m["GET /api/pages"] = rule(secPages)
	m["GET /api/pages/{name}"] = rule(secPages)
	m["PUT /api/pages/{name}"] = rule(secPages)
	m["DELETE /api/pages/{name}"] = rule(secPages)

	m["GET /api/cron"] = rule(secAutomation)
	m["PUT /api/cron"] = rule(secAutomation)
	m["POST /api/cron/run"] = rule(secAutomation)

	m["GET /api/keitaro/status"] = rule(secIntegrations)
	m["GET /api/keitaro/settings"] = rule(secIntegrations)
	m["PUT /api/keitaro/settings"] = rule(secIntegrations)
	m["POST /api/keitaro/check"] = rule(secIntegrations)
	m["POST /api/keitaro/import"] = rule(secIntegrations)
	m["GET /api/keitaro/job"] = rule(secIntegrations)
	m["POST /api/keitaro/purge"] = rule(secIntegrations)

	// Update routes: explicitly admin-only regardless of isAdminSection evaluation.
	updateAdmin := routeRule{sec: secSettings, admin: true}
	m["GET /api/version"] = updateAdmin
	m["GET /api/system"] = updateAdmin
	m["GET /api/system/signals"] = updateAdmin
	m["GET /api/update"] = updateAdmin
	m["GET /api/update/log"] = updateAdmin
	m["POST /api/update/apply"] = updateAdmin
	m["POST /api/update/rollback"] = updateAdmin
	m["POST /api/update/check"] = updateAdmin

	keys := rule(secAPIKeys)
	m["GET /api/api-keys"] = keys
	m["POST /api/api-keys"] = keys
	m["POST /api/api-keys/{id}/rotate"] = keys
	m["PUT /api/api-keys/{id}"] = keys
	m["DELETE /api/api-keys/{id}"] = keys

	users := rule(secUsers)
	m["GET /api/users"] = users
	m["POST /api/users"] = users
	m["GET /api/users/{id}"] = users
	m["PUT /api/users/{id}"] = users
	m["DELETE /api/users/{id}"] = users
	m["PUT /api/users/{id}/password"] = users
	m["PUT /api/users/{id}/access"] = users

	m["GET /api/roles"] = users
	m["POST /api/roles"] = users
	m["POST /api/roles/reload"] = users
	m["GET /api/roles/{id}"] = users
	m["PUT /api/roles/{id}"] = users
	m["DELETE /api/roles/{id}"] = users
}

// isAdminSection reports whether sec is visible only to administrators.
func isAdminSection(sec section) bool {
	if sec == secAny {
		return false
	}
	for _, a := range adminSections {
		if a == sec {
			return true
		}
	}
	return false
}

// keyUserPrefix starts the id of the synthetic user behind a USER API key.
const keyUserPrefix = "apikey:"

// isUserKey reports whether sess was opened by an API key of type USER.
func isUserKey(sess security.Session) bool {
	return sess.API && sess.KeyType == security.KeyTypeUser
}

// sessionUID returns the owner id recorded on rows sess creates.
// It is the users.json id for a file user, "apikey:<id>" for a USER key, and
// empty for the environment administrator and ADMIN keys.
func sessionUID(sess security.Session) string {
	if isUserKey(sess) {
		return keyUserPrefix + sess.KeyID
	}
	return sess.UID
}

// keyUser returns the synthetic USER account of a USER key session.
// Its role and access come from the session; accessOf resolves them like a file user.
func keyUser(sess security.Session) security.User {
	u := security.User{ID: sessionUID(sess), Type: security.UserRegular, Enabled: true, RoleID: sess.KeyRole}
	if sess.KeyAccess != nil {
		u.Access = *sess.KeyAccess
	}
	return u
}

// sessionUser returns the account behind sess: the users.json record, or the
// synthetic user of a USER key. Root and ADMIN key sessions return false.
func (s *Server) sessionUser(sess security.Session) (security.User, bool) {
	if sess.API {
		if isUserKey(sess) {
			return keyUser(sess), true
		}
		return security.User{}, false
	}
	if sess.UID == "" {
		return security.User{}, false
	}
	return s.userByID(sess.UID)
}

// isAdmin reports whether sess has full administrator privileges.
// An API key has them when its type is ADMIN or empty; any other type is limited.
func (s *Server) isAdmin(sess security.Session) bool {
	if sess.API {
		return sess.KeyType == "" || sess.KeyType == security.KeyTypeAdmin
	}
	if sess.UID == "" {
		return true
	}
	u, ok := s.userByID(sess.UID)
	return ok && u.Type == security.UserAdmin
}

// aiAdminOnly keeps the AI assistant to administrators during the beta. Opening it
// to users takes this switch to false, secAssistant added to userSections, and the
// same name added to USER_ACCESS_SECTIONS in the panel.
const aiAdminOnly = true

// aiAllowed reports whether sess may use the AI assistant.
// A request authenticated by an API key never may: every /api/ai/* route is for cookie
// sessions only. An administrator account always may; any other session only when
// aiAdminOnly is off and its access holds secAssistant.
// It returns false for a session without an account.
func (s *Server) aiAllowed(sess security.Session) bool {
	if sess.API {
		return false
	}
	if s.isAdmin(sess) {
		return true
	}
	return !aiAdminOnly && s.allowed(sess, routeRule{sec: secAssistant})
}

// allowed reports whether sess may use a route described by rule.
func (s *Server) allowed(sess security.Session, rule routeRule) bool {
	if s.isAdmin(sess) {
		return true
	}
	if rule.admin {
		return false
	}
	if rule.sec == secAny {
		return true
	}
	u, ok := s.sessionUser(sess)
	if !ok {
		return false
	}
	want := string(rule.sec)
	for _, sec := range s.accessOf(u).Sections {
		if sec == want {
			return true
		}
	}
	return false
}

// guard rejects sessions that fail allowed before calling next.
func (s *Server) guard(rule routeRule, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if !s.allowed(sess, rule) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAction rejects an API session whose key lacks rule.action.
// A rule without an action, and every cookie session, pass to next. The answer
// is 403 "action required: <name>". There is no error return.
func requireAction(rule routeRule, next http.Handler) http.Handler {
	if rule.action == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if sess.API && !slices.Contains(sess.KeyActions, rule.action) {
			writeErr(w, http.StatusForbidden, "action required: "+rule.action)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handle registers pattern behind auth, guard and the rule flags using routeAccess.
// A cookie rule wraps h in cookieOnly and an action rule requires the key action,
// both after guard. Between auth and the scope checks an API-key request passes
// the per-key limiter (keyLimit) and, for a write, the audit log (auditWrites,
// which records pattern as the route). It is called only from Handler() at server startup. Panic
// when pattern is missing from routeAccess is intentional: an uncovered route
// is a programmer error and the server must not start with an unprotected
// route (fail-closed).
func (s *Server) handle(mux *http.ServeMux, pattern string, h http.Handler) {
	rule, ok := routeAccess[pattern]
	if !ok {
		// Intentional startup panic: every mux route must have an access rule.
		panic("admin: route without access rule: " + pattern)
	}
	s.routes = append(s.routes, pattern)
	if rule.cookie {
		h = cookieOnly(h)
	}
	inner := s.boardScope(s.guard(rule, requireAction(rule, h)))
	if s.inner == nil {
		s.inner = make(map[string]http.Handler)
	}
	s.inner[pattern] = inner
	mux.Handle(pattern, s.auth(s.keyLimit(rule, s.auditWrites(pattern, inner))))
}

// publicRoutes lists the routes mounted without the session check, by mux pattern.
// A true value marks a sign-in route: it belongs to the browser session flow and an
// API key has no use for it. None of them reads the Api-Key header.
var publicRoutes = map[string]bool{
	"GET /api/health": false, "GET /healthz": false, "GET /api/brand": false,
	"GET /docs": false, "GET /api/docs/openapi.json": false, "GET /api/docs/postman.json": false, "GET /api/docs.md": false, "GET /llms.txt": false,
	"GET /api/setup": true, "POST /api/setup": true, "POST /api/login": true, "POST /api/recover": true,
}

// publicRule returns the rule the API docs give the public route pattern: open to
// every caller, and cookie-only for a sign-in route. It is never used to guard a request.
func publicRule(pattern string) routeRule {
	return routeRule{sec: secAny, cookie: publicRoutes[pattern]}
}

// handlePublic registers pattern on mux without the session check and records it in
// s.public for the API docs. It is called only from Handler() at server startup and
// panics when pattern is missing from publicRoutes: an unlisted public route is a
// programmer error.
func (s *Server) handlePublic(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	if _, ok := publicRoutes[pattern]; !ok {
		// Intentional startup panic: every public route must be listed for the docs.
		panic("admin: public route not listed: " + pattern)
	}
	s.public = append(s.public, pattern)
	mux.HandleFunc(pattern, h)
}

// userPages returns panel section ids visible to u in userSections order.
// acc is the access in force for u, from accessOf. An administrator sees every section.
func userPages(u security.User, acc security.Access) []string {
	if u.Type == security.UserAdmin {
		return sectionStrings(allSections)
	}
	allowed := make(map[string]struct{}, len(acc.Sections))
	for _, sec := range acc.Sections {
		allowed[sec] = struct{}{}
	}
	out := make([]string, 0, len(userSections))
	for _, sec := range userSections {
		if _, ok := allowed[string(sec)]; ok {
			out = append(out, string(sec))
		}
	}
	return out
}

// rootPages returns every panel section for the environment administrator.
func rootPages() []string {
	return sectionStrings(allSections)
}

// sectionStrings converts section ids to plain strings in the same order.
func sectionStrings(list []section) []string {
	out := make([]string, len(list))
	for i, sec := range list {
		out[i] = string(sec)
	}
	return out
}

// normalizeAccess trims and validates access for storage on a USER account.
// The modes full, read and created keep no id or label lists: they list no records.
func normalizeAccess(a security.Access) (security.Access, error) {
	out := security.Access{}
	if a.Sections == nil {
		out.Sections = []string{}
	} else {
		seen := make(map[string]struct{})
		for _, raw := range a.Sections {
			sec := strings.TrimSpace(raw)
			if sec == "" {
				continue
			}
			if isAdminSection(section(sec)) {
				return security.Access{}, fmt.Errorf("%w: %s", errAccessSection, sec)
			}
			if !isUserSection(section(sec)) {
				return security.Access{}, fmt.Errorf("%w: %s", errAccessSection, sec)
			}
			if _, ok := seen[sec]; ok {
				continue
			}
			seen[sec] = struct{}{}
			out.Sections = append(out.Sections, sec)
		}
	}
	out.Grants = make(map[string]security.Grant)
	if a.Grants != nil {
		for key, g := range a.Grants {
			res := strings.TrimSpace(key)
			if !grantResourceOK(res) {
				return security.Access{}, fmt.Errorf("%w: %s", errAccessResource, res)
			}
			mode := security.Mode(strings.TrimSpace(string(g.Mode)))
			if mode == "" {
				continue
			}
			if !grantModeOK(mode) {
				return security.Access{}, fmt.Errorf("%w: %s", errAccessMode, mode)
			}
			ng := security.Grant{Mode: mode}
			if mode == security.ModeFull || mode == security.ModeRead || mode == security.ModeCreated {
				ng.IDs = nil
				ng.Labels = nil
			} else {
				var err error
				if ng.IDs, err = normalizeStringList(g.IDs, 500); err != nil {
					return security.Access{}, err
				}
				if ng.Labels, err = normalizeStringList(g.Labels, 500); err != nil {
					return security.Access{}, err
				}
			}
			out.Grants[res] = ng
		}
	}
	if a.Hidden != nil {
		seen := make(map[string]struct{})
		for _, raw := range a.Hidden {
			m := strings.TrimSpace(raw)
			if m == "" {
				continue
			}
			if !hiddenIDOK(m) {
				return security.Access{}, fmt.Errorf("%w: %s", errAccessHidden, m)
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			out.Hidden = append(out.Hidden, m)
		}
	}
	return out, nil
}

// isUserSection reports whether sec is grantable to a USER account.
func isUserSection(sec section) bool {
	for _, u := range userSections {
		if u == sec {
			return true
		}
	}
	return false
}

// grantResourceOK reports whether name is an allowed grants map key.
func grantResourceOK(name string) bool {
	for _, r := range grantResources {
		if r == name {
			return true
		}
	}
	return false
}

// grantModeOK reports whether mode is an allowed grant mode value.
func grantModeOK(mode security.Mode) bool {
	for _, m := range grantModes {
		if m == mode {
			return true
		}
	}
	return false
}

// normalizeStringList trims, deduplicates in, and enforces at most limit entries.
func normalizeStringList(in []string, limit int) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
		if len(out) > limit {
			return nil, errAccessLimit
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
