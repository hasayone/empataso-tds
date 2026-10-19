package admin

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// keitaroRoutes returns every pattern of routeAccess that belongs to the Keitaro
// import, sorted.
func keitaroRoutes() []string {
	var out []string
	for pattern := range routeAccess {
		if strings.Contains(pattern, " /api/keitaro/") {
			out = append(out, pattern)
		}
	}
	sort.Strings(out)
	return out
}

// TestKeitaroRoutesAreAdminOnly pins the rule that keeps an import out of the reach of
// a role: it writes campaigns, lists, landings and clicks without the macro, source and
// price checks a role is held to. Every Keitaro route must sit in an administrator
// section, so a route added later without that rule fails here.
func TestKeitaroRoutesAreAdminOnly(t *testing.T) {
	routes := keitaroRoutes()
	for _, want := range []string{
		"GET /api/keitaro/status", "GET /api/keitaro/settings", "PUT /api/keitaro/settings",
		"POST /api/keitaro/check", "POST /api/keitaro/import", "GET /api/keitaro/job", "POST /api/keitaro/purge",
	} {
		found := false
		for _, r := range routes {
			found = found || r == want
		}
		if !found {
			t.Errorf("route %q is not in routeAccess", want)
		}
	}
	for _, pattern := range routes {
		rule := routeAccess[pattern]
		if !rule.admin || !isAdminSection(rule.sec) {
			t.Errorf("%s: a role can reach it (section %q, admin-only %v)", pattern, rule.sec, rule.admin)
		}
	}
	for _, sec := range userSections {
		if sec == secIntegrations {
			t.Error("integrations is a section a role can be given")
		}
	}
}

func TestKeitaroRoutesRefuseRoleWithHiddenFields(t *testing.T) {
	rig := newRolesRig(t, "")
	for _, hidden := range [][]string{{"ip"}, {"cost"}, {"keyword"}, nil} {
		rig.setRole(t, editorAccess(hidden...))
		for _, pattern := range keitaroRoutes() {
			method, path, _ := strings.Cut(pattern, " ")
			var body any
			if method != http.MethodGet {
				body = map[string]any{}
			}
			if code, raw := rig.api(t, rig.user, rig.ucsrf, method, path, body); code != http.StatusForbidden {
				t.Errorf("hidden %v: %s answers %d %s to a role, want 403", hidden, pattern, code, raw)
			}
		}
	}
	for _, sections := range [][]string{{"integrations"}, {"offers", "integrations"}} {
		code, raw := rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{
			"name": "keitaro-" + strings.Join(sections, "-"), "access": map[string]any{"sections": sections},
		})
		if code != http.StatusBadRequest {
			t.Errorf("a role with sections %v: %d %s", sections, code, raw)
		}
	}
}
