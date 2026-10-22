package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/egerkuzma/kuztds/internal/security"
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

// routeRule binds one mux pattern to a section and optional administrator-only flag.
type routeRule struct {
	sec   section
	admin bool
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
	m["GET /api/groups"] = open()

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
	m["GET /api/postback-log"] = routeRule{sec: secConversions, admin: true}

	m["GET /api/report"] = rule(secReport)
	m["GET /api/keys"] = rule(secKeywords)
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
	m["POST /api/cloudflare/test"] = rule(secIntegrations)
	m["GET /api/registrars/namecheap"] = rule(secIntegrations)
	m["PUT /api/registrars/namecheap"] = rule(secIntegrations)
	m["POST /api/registrars/namecheap/test"] = rule(secIntegrations)
	m["POST /api/registrars/namecheap/check"] = routeRule{sec: secDomains, admin: true}
	m["POST /api/registrars/namecheap/register"] = routeRule{sec: secDomains, admin: true}

	m["GET /api/lists"] = rule(secLists)
	m["GET /api/lists/{name}"] = rule(secLists)
	m["PUT /api/lists/{name}"] = routeRule{sec: secLists, admin: true}

	m["GET /api/lookup"] = rule(secTools)
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

// sessionUser returns the users.json record for sess, or false for root and API sessions.
func (s *Server) sessionUser(sess security.Session) (security.User, bool) {
	if sess.API || sess.UID == "" {
		return security.User{}, false
	}
	return s.userByID(sess.UID)
}

// isAdmin reports whether sess has full administrator privileges.
func (s *Server) isAdmin(sess security.Session) bool {
	if sess.API || sess.UID == "" {
		return true
	}
	u, ok := s.userByID(sess.UID)
	return ok && u.Type == security.UserAdmin
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

// handle registers pattern behind auth and guard using routeAccess.
// It is called only from Handler() at server startup. Panic when pattern is
// missing from routeAccess is intentional: an uncovered route is a programmer
// error and the server must not start with an unprotected route (fail-closed).
func (s *Server) handle(mux *http.ServeMux, pattern string, h http.Handler) {
	rule, ok := routeAccess[pattern]
	if !ok {
		// Intentional startup panic: every mux route must have an access rule.
		panic("admin: route without access rule: " + pattern)
	}
	s.routes = append(s.routes, pattern)
	mux.Handle(pattern, s.auth(s.guard(rule, h)))
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
