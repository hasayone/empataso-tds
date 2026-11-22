package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
)

// mcpOperationID is the operationId alphabet and length cmd/mcp accepts without rewriting.
var mcpOperationID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// mcpForbidden lists routes that must never be exposed to agents: exports, archive
// upload, Keitaro import and purge, cron, log deletion, the whole-array group save,
// landing file removal, update endpoints and every /api/ai/* route.
var mcpForbidden = []string{
	"GET /api/logs/export", "GET /api/postbacks/export", "POST /api/landings/{id}/archive",
	"POST /api/keitaro/import", "POST /api/keitaro/purge", "PUT /api/cron", "POST /api/cron/run",
	"GET /api/cron", "DELETE /api/logs", "PUT /api/groups",
	"DELETE /api/landings/{id}/files/{path...}",
	"GET /api/version", "GET /api/system", "GET /api/system/signals", "GET /api/update", "GET /api/update/log",
	"POST /api/update/apply", "POST /api/update/rollback", "POST /api/update/check",
	"GET /api/ai/settings", "PUT /api/ai/settings", "POST /api/ai/test", "GET /api/ai/models", "GET /api/ai/status",
	"GET /api/ai/chats", "POST /api/ai/chats", "GET /api/ai/chats/{id}", "DELETE /api/ai/chats/{id}",
	"POST /api/ai/chats/{id}/messages", "GET /api/ai/chats/{id}/stream", "POST /api/ai/chats/{id}/cancel", "GET /api/ai/changes", "POST /api/ai/changes/{id}/apply",
	"POST /api/ai/changes/{id}/reject", "POST /api/ai/changes/{id}/revert",
	"POST /api/ai/messages/{message_id}/revert", "POST /api/ai/chats/{id}/revert",
}

// landingFilePut is the one action-gated route an agent may call through MCP.
const landingFilePut = "PUT /api/landings/{id}/files/{path...}"

// openAPIOperations decodes the document built for sess through JSON, as a client sees it.
// It returns the full document and its operations keyed by "METHOD /path".
func openAPIOperations(t *testing.T, s *Server, sess security.Session) (map[string]any, map[string]map[string]any) {
	t.Helper()
	raw, err := json.Marshal(s.openAPIDocument(sess))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v", doc["openapi"])
	}
	out := map[string]map[string]any{}
	paths, _ := doc["paths"].(map[string]any)
	for path, item := range paths {
		for method, op := range item.(map[string]any) {
			out[strings.ToUpper(method)+" "+path] = op.(map[string]any)
		}
	}
	return doc, out
}

// wantKeys returns the converted keys of every registered route, guarded or public,
// for which keep is true.
func wantKeys(s *Server, keep func(routeRule) bool) []string {
	var out []string
	for _, p := range append(slices.Clone(s.routes), s.public...) {
		if keep(ruleOf(p)) {
			out = append(out, convertedKey(p))
		}
	}
	sort.Strings(out)
	return out
}

// gotKeys returns the sorted keys of ops.
func gotKeys(ops map[string]map[string]any) []string {
	out := make([]string, 0, len(ops))
	for k := range ops {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameKeys fails the test when got and want differ, naming the missing and extra keys.
func sameKeys(t *testing.T, who string, got, want []string) {
	t.Helper()
	have := map[string]bool{}
	for _, k := range got {
		have[k] = true
	}
	for _, k := range want {
		if !have[k] {
			t.Errorf("%s: missing %s", who, k)
		}
		delete(have, k)
	}
	for k := range have {
		t.Errorf("%s: unexpected %s", who, k)
	}
}

func TestOpenAPICookieAdminSeesEveryRoute(t *testing.T) {
	s := accessTestServer(t)
	doc, ops := openAPIOperations(t, s, security.Session{})
	sameKeys(t, "cookie admin", gotKeys(ops), wantKeys(s, func(routeRule) bool { return true }))
	if len(ops) != len(s.routes)+len(s.public) {
		t.Errorf("operations %d, routes %d, public %d", len(ops), len(s.routes), len(s.public))
	}
	info := doc["info"].(map[string]any)
	if info["title"] != "emtds admin API" || info["version"] == "" {
		t.Errorf("info = %v", info)
	}
	scheme := doc["components"].(map[string]any)["securitySchemes"].(map[string]any)["ApiKey"].(map[string]any)
	if scheme["type"] != "apiKey" || scheme["in"] != "header" || scheme["name"] != "Api-Key" {
		t.Errorf("security scheme = %v", scheme)
	}
	if sec, _ := doc["security"].([]any); len(sec) != 1 {
		t.Errorf("security = %v", doc["security"])
	}
}

func TestOpenAPIAdminKeyHidesCookieAndActionRoutes(t *testing.T) {
	s := accessTestServer(t)
	for _, keyType := range []string{"", security.KeyTypeAdmin} {
		_, ops := openAPIOperations(t, s, security.Session{API: true, KeyType: keyType})
		sameKeys(t, "admin key "+keyType, gotKeys(ops), wantKeys(s, func(r routeRule) bool {
			return !r.cookie && r.action == ""
		}))
	}
}

func TestOpenAPIKeyActionUnlocksItsRoutes(t *testing.T) {
	s := accessTestServer(t)
	sess := security.Session{API: true, KeyType: security.KeyTypeAdmin, KeyActions: []string{security.ActionKeitaroPurge}}
	_, ops := openAPIOperations(t, s, sess)
	if _, ok := ops["POST /api/keitaro/purge"]; !ok {
		t.Error("purge route missing for a key with keitaro_purge")
	}
	for _, hidden := range []string{"POST /api/keitaro/import", "DELETE /api/logs", "PUT /api/cron", "GET /api/me"} {
		if _, ok := ops[hidden]; ok {
			t.Errorf("%s visible without its action", hidden)
		}
	}
	op := ops["POST /api/keitaro/purge"]
	if op["x-emtds-action"] != security.ActionKeitaroPurge {
		t.Errorf("purge action extension = %v", op["x-emtds-action"])
	}
	all := security.Session{API: true, KeyType: security.KeyTypeAdmin, KeyActions: security.AllActions()}
	_, full := openAPIOperations(t, s, all)
	sameKeys(t, "all actions", gotKeys(full), wantKeys(s, func(r routeRule) bool { return !r.cookie }))
}

func TestOpenAPIUserKeySeesOnlyItsSections(t *testing.T) {
	s := accessTestServer(t)
	sess := security.Session{
		API: true, KeyID: "k1", KeyType: security.KeyTypeUser,
		KeyAccess: &security.Access{Sections: []string{string(secOffers), string(secLogs)}},
	}
	_, ops := openAPIOperations(t, s, sess)
	sameKeys(t, "user key", gotKeys(ops), wantKeys(s, func(r routeRule) bool {
		return !r.cookie && !r.admin && r.action == "" &&
			(r.sec == secAny || r.sec == secOffers || r.sec == secLogs)
	}))
	for _, key := range []string{"GET /api/offers", "GET /api/logs"} {
		if _, ok := ops[key]; !ok {
			t.Errorf("%s missing", key)
		}
	}
	for _, key := range []string{"GET /api/domains", "GET /api/landings", "PUT /api/ui", "GET /api/users"} {
		if _, ok := ops[key]; ok {
			t.Errorf("%s visible outside the granted sections", key)
		}
	}
}

func TestOpenAPIOperationsFollowTheMCPContract(t *testing.T) {
	s := accessTestServer(t)
	_, ops := openAPIOperations(t, s, security.Session{})
	ids := map[string]string{}
	mcpCount := 0
	for key, op := range ops {
		id, _ := op["operationId"].(string)
		if !mcpOperationID.MatchString(id) {
			t.Errorf("%s: operationId %q is not a valid tool name", key, id)
		}
		if prev, dup := ids[id]; dup {
			t.Errorf("%s and %s share operationId %q", key, prev, id)
		}
		ids[id] = key
		if op["summary"] == "" || op["summary"] == nil {
			t.Errorf("%s: empty summary", key)
		}
		if _, ok := op["responses"].(map[string]any)["default"]; !ok {
			t.Errorf("%s: no default response", key)
		}
		checkPlaceholders(t, key, op)
		if mode, ok := op["x-mcp"].(string); ok {
			mcpCount++
			if mode != "read" && mode != "write" {
				t.Errorf("%s: x-mcp = %q", key, mode)
			}
			checkMCPOperation(t, key, op)
		}
	}
	if mcpCount == 0 {
		t.Error("no operation is exposed to MCP")
	}
}

// checkPlaceholders fails when the path of key has a {name} without a path parameter,
// or a path parameter outside the path template.
func checkPlaceholders(t *testing.T, key string, op map[string]any) {
	t.Helper()
	_, path, _ := strings.Cut(key, " ")
	have := map[string]bool{}
	params, _ := op["parameters"].([]any)
	for _, p := range params {
		pm := p.(map[string]any)
		if pm["in"] == "path" {
			have[pm["name"].(string)] = true
			if pm["required"] != true {
				t.Errorf("%s: path parameter %v is not required", key, pm["name"])
			}
		}
	}
	for _, m := range pathParamPattern.FindAllStringSubmatch(path, -1) {
		if !have[m[1]] {
			t.Errorf("%s: placeholder {%s} has no path parameter", key, m[1])
		}
		delete(have, m[1])
	}
	for name := range have {
		t.Errorf("%s: path parameter %s is not in the path", key, name)
	}
}

// checkMCPOperation applies the rules cmd/mcp enforces on an exposed operation:
// path and query parameters only, unique names, and a JSON body.
func checkMCPOperation(t *testing.T, key string, op map[string]any) {
	t.Helper()
	seen := map[string]bool{}
	params, _ := op["parameters"].([]any)
	for _, p := range params {
		pm := p.(map[string]any)
		name := pm["name"].(string)
		if pm["in"] != "path" && pm["in"] != "query" {
			t.Errorf("%s: parameter %s in %v", key, name, pm["in"])
		}
		if seen[name] {
			t.Errorf("%s: parameter %s declared twice", key, name)
		}
		seen[name] = true
		if _, ok := pm["schema"].(map[string]any); !ok {
			t.Errorf("%s: parameter %s has no schema", key, name)
		}
	}
	if body, ok := op["requestBody"].(map[string]any); ok {
		checkMCPBody(t, key, body["content"].(map[string]any))
	}
}

// checkMCPBody fails when content offers neither application/json nor text/plain
// with a string schema, the only bodies cmd/mcp accepts.
func checkMCPBody(t *testing.T, key string, content map[string]any) {
	t.Helper()
	if _, ok := content["application/json"]; ok {
		return
	}
	text, ok := content["text/plain"].(map[string]any)
	if !ok {
		t.Errorf("%s: MCP body has neither application/json nor text/plain content", key)
		return
	}
	if schema, _ := text["schema"].(map[string]any); schema["type"] != "string" {
		t.Errorf("%s: text/plain body schema = %v, want type string", key, text["schema"])
	}
}

func TestOpenAPIPathTailKeepsSlashes(t *testing.T) {
	s := accessTestServer(t)
	_, ops := openAPIOperations(t, s, security.Session{})
	op := ops["GET /api/landings/{id}/files/{path}"]
	if op == nil {
		t.Fatal("landing file route missing")
	}
	tails := 0
	for _, p := range op["parameters"].([]any) {
		pm := p.(map[string]any)
		schema := pm["schema"].(map[string]any)
		if pm["name"] == "path" {
			if schema["x-emtds-path"] != true {
				t.Errorf("path schema = %v", schema)
			}
			tails++
		} else if _, marked := schema["x-emtds-path"]; marked {
			t.Errorf("%v wrongly marked as a path tail", pm["name"])
		}
	}
	if tails != 1 {
		t.Errorf("path tail parameters = %d", tails)
	}
}

func TestOpenAPIExtensionsMirrorTheAccessRule(t *testing.T) {
	s := accessTestServer(t)
	_, ops := openAPIOperations(t, s, security.Session{})
	for _, pattern := range append(slices.Clone(s.routes), s.public...) {
		rule := ruleOf(pattern)
		op := ops[convertedKey(pattern)]
		if _, public := publicRoutes[pattern]; public != (op[publicExtension] == true) {
			t.Errorf("%s: public %v, want %v", pattern, op[publicExtension], public)
		}
		if got, _ := op["x-emtds-section"].(string); got != string(rule.sec) {
			t.Errorf("%s: section %q, want %q", pattern, got, rule.sec)
		}
		if got, _ := op["x-emtds-action"].(string); got != rule.action {
			t.Errorf("%s: action %q, want %q", pattern, got, rule.action)
		}
		if got, _ := op["x-emtds-admin"].(bool); got != rule.admin {
			t.Errorf("%s: admin %v, want %v", pattern, got, rule.admin)
		}
		if got, _ := op["x-emtds-heavy"].(bool); got != rule.heavy {
			t.Errorf("%s: heavy %v, want %v", pattern, got, rule.heavy)
		}
		if got, _ := op["x-emtds-cookie-only"].(bool); got != rule.cookie {
			t.Errorf("%s: cookie-only %v, want %v", pattern, got, rule.cookie)
		}
	}
}

func TestRouteDocsAreRegisteredRoutes(t *testing.T) {
	s := accessTestServer(t)
	registered := map[string]bool{}
	for _, p := range append(slices.Clone(s.routes), s.public...) {
		registered[p] = true
	}
	ids := map[string]string{}
	for pattern, doc := range routeDocs {
		if !registered[pattern] {
			t.Errorf("routeDocs lists %s, which is not a registered route", pattern)
		}
		if !mcpOperationID.MatchString(doc.ID) {
			t.Errorf("%s: operationId %q is not a valid tool name", pattern, doc.ID)
		}
		if prev, dup := ids[doc.ID]; dup {
			t.Errorf("%s and %s share operationId %q", pattern, prev, doc.ID)
		}
		ids[doc.ID] = pattern
		if doc.Summary == "" {
			t.Errorf("%s: empty summary", pattern)
		}
		if doc.MCP != "" && doc.MCP != mcpRead && doc.MCP != mcpWrite {
			t.Errorf("%s: mcp %q", pattern, doc.MCP)
		}
		if doc.Body != nil && doc.RawBody {
			t.Errorf("%s: both a JSON and a raw body", pattern)
		}
	}
}

func TestRouteDocsKeepDangerousRoutesOutOfMCP(t *testing.T) {
	for _, pattern := range mcpForbidden {
		if _, ok := routeAccess[pattern]; !ok {
			t.Errorf("forbidden list names unknown route %s", pattern)
		}
		if doc, ok := routeDocs[pattern]; ok && doc.MCP != "" {
			t.Errorf("%s must not be exposed to MCP", pattern)
		}
	}
	for pattern, doc := range routeDocs {
		rule := routeAccess[pattern]
		if doc.MCP == "" {
			continue
		}
		if rule.cookie {
			t.Errorf("%s: a cookie-only route cannot be an MCP tool", pattern)
		}
		if rule.action != "" && pattern != landingFilePut && pattern != "DELETE /api/landings/{id}" {
			t.Errorf("%s: only %s and DELETE /api/landings/{id} may be an MCP tool behind a key action", pattern, landingFilePut)
		}
	}
}

func TestLandingFilePutNeedsItsAction(t *testing.T) {
	s := accessTestServer(t)
	without := security.Session{API: true, KeyType: security.KeyTypeAdmin}
	_, ops := openAPIOperations(t, s, without)
	if _, ok := ops[convertedKey(landingFilePut)]; ok {
		t.Fatal("a key without landing_files sees the landing file write")
	}
	with := security.Session{API: true, KeyType: security.KeyTypeAdmin, KeyActions: []string{security.ActionLandingFiles}}
	_, ops = openAPIOperations(t, s, with)
	op, ok := ops[convertedKey(landingFilePut)]
	if !ok {
		t.Fatal("a key with landing_files misses the landing file write")
	}
	if op["x-mcp"] != "write" || op["x-emtds-action"] != security.ActionLandingFiles {
		t.Errorf("operation = %v", op)
	}
	content := op["requestBody"].(map[string]any)["content"].(map[string]any)
	if _, isJSON := content["application/json"]; isJSON {
		t.Error("landing file body must be text/plain")
	}
	checkMCPBody(t, landingFilePut, content)
	for _, hidden := range []string{"POST /api/landings/{id}/archive", "DELETE /api/landings/{id}/files/{path}"} {
		if o, ok := ops[hidden]; ok && o["x-mcp"] != nil {
			t.Errorf("%s must not be an MCP tool", hidden)
		}
	}
}

// mcpReadPosts lists the non-GET routes that read only: they store nothing and change no record.
var mcpReadPosts = map[string]bool{"POST /api/simulate": true}

// TestRouteDocsMarkEveryStateChangeAsWrite requires every documented non-GET route to be an
// MCP write unless mcpReadPosts names it, and no GET route to be one.
func TestRouteDocsMarkEveryStateChangeAsWrite(t *testing.T) {
	for pattern, doc := range routeDocs {
		if doc.MCP == "" {
			continue
		}
		if strings.HasPrefix(pattern, "GET ") {
			if doc.MCP != mcpRead {
				t.Errorf("%s: mcp %q, a GET route must be %q", pattern, doc.MCP, mcpRead)
			}
			continue
		}
		want := mcpWrite
		if mcpReadPosts[pattern] {
			want = mcpRead
		}
		if doc.MCP != want {
			t.Errorf("%s: mcp %q, want %q", pattern, doc.MCP, want)
		}
	}
	for pattern := range mcpReadPosts {
		if _, ok := routeDocs[pattern]; !ok {
			t.Errorf("mcpReadPosts names %s, which routeDocs does not list", pattern)
		}
	}
	if doc := routeDocs["POST /api/domains/{id}/check"]; doc.MCP != mcpWrite {
		t.Errorf("check_domain stores the probe result, mcp %q, want %q", doc.MCP, mcpWrite)
	}
}

func TestRouteDocsCoverTheAgentToolSet(t *testing.T) {
	want := []string{
		"GET /api/groups", "PUT /api/groups/{id}", "DELETE /api/groups/{id}",
		"GET /api/campaigns/{id}/postbacks", "PUT /api/campaigns/{id}/postbacks", "GET /api/s2s",
		"GET /api/offers", "GET /api/offers/{id}", "POST /api/offers", "PUT /api/offers/{id}", "DELETE /api/offers/{id}",
		"GET /api/landings", "GET /api/landings/{id}", "POST /api/landings", "PUT /api/landings/{id}", "DELETE /api/landings/{id}",
		"GET /api/affiliate-networks", "GET /api/affiliate-networks/{id}", "POST /api/affiliate-networks",
		"PUT /api/affiliate-networks/{id}", "DELETE /api/affiliate-networks/{id}",
		"GET /api/traffic-sources", "GET /api/traffic-sources/{id}", "POST /api/traffic-sources",
		"PUT /api/traffic-sources/{id}", "DELETE /api/traffic-sources/{id}",
		"GET /api/domains", "GET /api/domains/{id}", "POST /api/domains", "PUT /api/domains/{id}", "DELETE /api/domains/{id}",
		"GET /api/landings/{id}/files", "GET /api/landings/{id}/files/{path...}", landingFilePut,
		"GET /api/stats/summary", "GET /api/stats/timeseries", "GET /api/stats/breakdown",
		"GET /api/stats/performance", "GET /api/stats/facts", "GET /api/logs", "GET /api/postbacks",
		"GET /api/report", "GET /api/log-fields", "GET /api/log-values", "GET /api/lookup",
		"POST /api/simulate", "POST /api/domains/{id}/check", "GET /api/keitaro/job", "GET /api/boards",
	}
	for _, pattern := range want {
		doc, ok := routeDocs[pattern]
		if !ok || doc.MCP == "" {
			t.Errorf("%s is not exposed to MCP", pattern)
			continue
		}
		wantMode := mcpRead
		if strings.HasPrefix(pattern, "PUT ") || strings.HasPrefix(pattern, "DELETE ") || (strings.HasPrefix(pattern, "POST ") && !mcpReadPosts[pattern]) {
			wantMode = mcpWrite
		}
		if doc.MCP != wantMode {
			t.Errorf("%s: mcp %q, want %q", pattern, doc.MCP, wantMode)
		}
	}
}

func TestOpenAPIServedToAPIKeyOnly(t *testing.T) {
	s := accessTestServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	s.keyMu.Lock()
	rawKey, _, err := s.keys.Generate("t", []string{security.ScopeAdmin})
	s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/openapi.json", nil)
	req.Header.Set("Api-Key", rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.OpenAPI != "3.1.0" {
		t.Fatalf("document: %v %q", err, doc.OpenAPI)
	}
	if _, ok := doc.Paths["/api/me"]; ok {
		t.Error("API key sees a cookie-only route")
	}
	if _, ok := doc.Paths["/api/groups"]["get"]; !ok {
		t.Error("API key misses GET /api/groups")
	}

	anon, err := http.Get(srv.URL + "/api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous status %d, want 401", anon.StatusCode)
	}
}

func TestGeneratedOperationID(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{"put", "/api/groups/{id}", "put_groups_id"},
		{"get", "/api/landings/{id}/files/{path...}", "get_landings_id_files_path"},
		{"get", "/api/log-fields", "get_log_fields"},
		{"get", "/api/openapi.json", "get_openapi_json"},
		{"post", "/api/domains/{id}/check", "post_domains_id_check"},
		{"delete", "/api/landings/{id}/files/{path...}", "delete_landings_id_files_path"},
	}
	for _, c := range cases {
		if got := generatedOperationID(c.method, c.path); got != c.want {
			t.Errorf("generatedOperationID(%q, %q) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}
