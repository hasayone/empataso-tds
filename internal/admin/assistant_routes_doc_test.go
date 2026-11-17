package admin

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
)

// assistantCatalogBases are the catalogs whose records the assistant may propose to change.
var assistantCatalogBases = []string{
	"/api/offers", "/api/landings", "/api/affiliate-networks", "/api/traffic-sources",
}

// assistantForbiddenPaths are path fragments no assistant route may contain: landing
// files and archives, pages, the assistant itself, S2S, campaign postbacks, accounts,
// keys and every integration.
var assistantForbiddenPaths = []string{
	"/files", "/archive", "zip", "/api/pages", "/api/ai/", "/api/s2s", "/api/campaigns/",
	"/api/users", "/api/roles", "/api/api-keys", "/api/cloudflare", "/api/checker", "/api/registrars",
	"/api/keitaro", "/api/cron", "/api/bot-protection", "/api/lists", "/api/ui",
	"/api/spend", "/api/logs/export", "/api/postbacks/export",
}

// assistantPatterns returns the patterns of routeDocs marked for the assistant, sorted.
func assistantPatterns() []string {
	var out []string
	for pattern, doc := range routeDocs {
		if doc.Assistant != "" {
			out = append(out, pattern)
		}
	}
	sort.Strings(out)
	return out
}

// isAssistantCatalogPath reports whether path is a catalog collection or one record of it.
func isAssistantCatalogPath(path string) bool {
	for _, base := range assistantCatalogBases {
		if path == base || path == base+"/{id}" {
			return true
		}
	}
	return false
}

// TestAssistantToolAllowlist checks every route marked for the assistant: it is registered,
// visible to MCP and belongs to a user section. A route the assistant calls must not be
// cookie-only, admin-only or action-gated. A propose route may require an API-key action:
// the assistant never calls it, and a cookie session passes requireAction.
func TestAssistantToolAllowlist(t *testing.T) {
	s := accessTestServer(t)
	patterns := assistantPatterns()
	if len(patterns) == 0 {
		t.Fatal("no route is marked for the assistant")
	}
	registered := map[string]bool{}
	for _, p := range s.routes {
		registered[p] = true
	}
	for _, pattern := range patterns {
		doc := routeDocs[pattern]
		verb, path, _ := strings.Cut(pattern, " ")
		rule, ok := routeAccess[pattern]
		if !ok || !registered[pattern] {
			t.Errorf("%s: not a registered route", pattern)
			continue
		}
		if doc.MCP == "" {
			t.Errorf("%s: assistant route is not visible to MCP", pattern)
		}
		if rule.cookie || rule.admin || (rule.action != "" && doc.Assistant != asstPropose) {
			t.Errorf("%s: rule %+v is cookie-only, admin-only or action-gated for a call the assistant makes", pattern, rule)
		}
		if isAdminSection(rule.sec) || rule.sec != secAny && !isUserSection(rule.sec) {
			t.Errorf("%s: section %q is not a user section", pattern, rule.sec)
		}
		for _, bad := range assistantForbiddenPaths {
			if strings.Contains(strings.ToLower(path), bad) {
				t.Errorf("%s: path contains %q", pattern, bad)
			}
		}
		switch doc.Assistant {
		case asstRead:
			if doc.MCP != mcpRead {
				t.Errorf("%s: read route has MCP %q", pattern, doc.MCP)
			}
			if verb != http.MethodGet && pattern != "POST /api/simulate" {
				t.Errorf("%s: read route with method %s", pattern, verb)
			}
		case asstPropose:
			if doc.MCP != mcpWrite {
				t.Errorf("%s: propose route has MCP %q", pattern, doc.MCP)
			}
			if !slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodDelete}, verb) {
				t.Errorf("%s: propose route with method %s", pattern, verb)
			}
			catalog := isAssistantCatalogPath(path)
			campaign := path == "/api/groups/{id}" && verb != http.MethodPost
			if !catalog && !campaign {
				t.Errorf("%s: propose route outside catalogs and campaigns", pattern)
			}
			if path == "/api/groups" || strings.HasPrefix(path, "/api/domains") {
				t.Errorf("%s: propose route on a forbidden resource", pattern)
			}
		default:
			t.Errorf("%s: unknown assistant mode %q", pattern, doc.Assistant)
		}
		if strings.HasPrefix(path, "/api/domains") && verb != http.MethodGet {
			t.Errorf("%s: domains are read-only for the assistant", pattern)
		}
		if strings.HasPrefix(path, "/api/domains") && path != "/api/domains" && path != "/api/domains/{id}" {
			t.Errorf("%s: domain sub-route", pattern)
		}
	}
}

func TestAssistantToolAllowlistCoversTheBrief(t *testing.T) {
	want := map[string]string{
		"GET /api/offers": asstRead, "GET /api/offers/{id}": asstRead,
		"GET /api/landings": asstRead, "GET /api/landings/{id}": asstRead,
		"GET /api/affiliate-networks": asstRead, "GET /api/affiliate-networks/{id}": asstRead,
		"GET /api/traffic-sources": asstRead, "GET /api/traffic-sources/{id}": asstRead,
		"GET /api/domains": asstRead, "GET /api/domains/{id}": asstRead,
		"GET /api/groups": asstRead, "GET /api/boards": asstRead,
		"GET /api/stats/summary": asstRead, "GET /api/stats/timeseries": asstRead,
		"GET /api/stats/breakdown": asstRead, "GET /api/stats/performance": asstRead,
		"GET /api/logs": asstRead, "GET /api/postbacks": asstRead, "GET /api/log-values": asstRead,
		"GET /api/report": asstRead, "GET /api/log-fields": asstRead,
		"POST /api/simulate": asstRead, "GET /api/lookup": asstRead,
		"PUT /api/groups/{id}": asstPropose, "DELETE /api/groups/{id}": asstPropose,
	}
	for _, base := range assistantCatalogBases {
		want["POST "+base] = asstPropose
		want["PUT "+base+"/{id}"] = asstPropose
		want["DELETE "+base+"/{id}"] = asstPropose
	}
	for pattern, mode := range want {
		if got := routeDocs[pattern].Assistant; got != mode {
			t.Errorf("%s: assistant = %q, want %q", pattern, got, mode)
		}
	}
	for _, pattern := range assistantPatterns() {
		if strings.Contains(pattern, "keywords") {
			if routeDocs[pattern].Assistant != asstRead || routeDocs[pattern].MCP == "" {
				t.Errorf("%s: keyword route may only be an MCP-visible read", pattern)
			}
			continue
		}
		if _, ok := want[pattern]; !ok {
			t.Errorf("%s: marked for the assistant but not in the brief", pattern)
		}
	}
}

func TestAssistantFieldStaysOutOfOpenAPI(t *testing.T) {
	s := accessTestServer(t)
	doc, ops := openAPIOperations(t, s, security.Session{})
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(raw))
	for _, leak := range []string{"assistant", "asst", `"propose"`} {
		if strings.Contains(text, leak) {
			t.Errorf("OpenAPI document holds %q", leak)
		}
	}
	for _, pattern := range assistantPatterns() {
		op, ok := ops[convertedKey(pattern)]
		if !ok {
			t.Errorf("%s: missing from OpenAPI", pattern)
			continue
		}
		for key := range op {
			if strings.Contains(strings.ToLower(key), "assist") {
				t.Errorf("%s: operation key %q", pattern, key)
			}
		}
		if op["x-mcp"] != routeDocs[pattern].MCP {
			t.Errorf("%s: x-mcp = %v, want %q", pattern, op["x-mcp"], routeDocs[pattern].MCP)
		}
	}
}
