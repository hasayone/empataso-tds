package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

// Canary values placed in every source of processSecrets and in the content the tools read.
const (
	aiCanaryCF     = "CFCANARYtoken0123456789abcdefXYZ"
	aiCanaryNC     = "NCCANARYkey0123456789"
	aiCanaryKT     = "KTCANARYfile0123456789"
	aiCanaryKTEnv  = "KTENVCANARY0123456789"
	aiCanaryCH     = "CHCANARYpass0123456789"
	aiCanaryAIPlan = "AICANARYopenaikey0123456789"
	aiCanaryAIAnth = "sk-ant-AICANARYanthropic0123456789"
	aiLongToken    = "aB3dE9fG2hJ5kL8mN1pQ4rS7tU0vW6xY"
	aiLongToken2   = "Zq8Wx3Lm5Nb7Vc1Xs9Df2Gh4Jk6Pr0Tu"
)

// aiContentCanaries are the values planted in offers, networks, landings, campaigns and journals.
var aiContentCanaries = []string{
	"CANARYQ", "CANARYSIG", "CANARYNET", "CANARYLAND", "CANARYPB", "CANARYCURL", "CANARYBCURL",
	"CANARYG", "CANARYFB", aiLongToken, aiLongToken2, "203.0.113.77", "198.51.100.4",
}

// aiLeakStats is a stats source whose journals carry canaries. It records the limit of the last
// click journal request and can return many rows.
type aiLeakStats struct {
	fakeStats
	lastLimit atomic.Int64
	bigRows   int
}

func (a *aiLeakStats) Logs(_ context.Context, f store.LogFilter) ([]store.LogRow, int64, error) {
	a.lastLimit.Store(int64(f.Limit))
	n := max(a.bigRows, 1)
	rows := make([]store.LogRow, n)
	for i := range rows {
		rows[i] = store.LogRow{
			IP: "203.0.113.77", Country: "pl", Keyword: "kw",
			Referer:  "https://ref.test/p?gclid=CANARYG&sub=1",
			Redirect: "https://off.test/o?token=CANARYQ&cid=1",
		}
	}
	return rows, int64(n), nil
}

func (a *aiLeakStats) QueryPostbacks(context.Context, store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error) {
	return []store.PostbackRow{{
		Profit: 1.5, IP: "198.51.100.4", Referer: "https://ref.test/c?fbclid=CANARYFB", CID: "c1",
	}}, store.PostbackTotals{Profit: 1.5, Count: 1}, nil
}

func (a *aiLeakStats) Values(context.Context, store.ValuesQuery) ([]store.KV, error) {
	return []store.KV{{Key: "https://ref.test/v?gclid=CANARYG", Count: 2}}, nil
}

func (a *aiLeakStats) BreakdownFiltered(context.Context, time.Time, time.Time, string, int, []store.Cond) ([]store.KV, error) {
	return []store.KV{{Key: "https://ref.test/b?fbclid=CANARYFB", Count: 2}}, nil
}

// aiToolGroups is an in-memory campaign store.
type aiToolGroups struct {
	mu   sync.Mutex
	list []config.Group
}

func (g *aiToolGroups) List(context.Context) ([]config.Group, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]config.Group(nil), g.list...), nil
}

func (g *aiToolGroups) Save(_ context.Context, list []config.Group) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.list = append([]config.Group(nil), list...)
	return nil
}

// aiToolEnv is an admin server whose files, environment and content hold canaries.
type aiToolEnv struct {
	s        *Server
	srv      *httptest.Server
	stats    *aiLeakStats
	groups   *aiToolGroups
	root     security.Session
	literals map[string]string
}

// writeAIFile writes content to dir/name with mode 0600.
func writeAIFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// mustJSONText marshals v for a fixture file.
func mustJSONText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newAIToolEnv builds the server: secrets in ai.json, cloudflare.json, namecheap.json,
// keitaro.json and Config.SecretValues, and content with canaries in every catalog.
func newAIToolEnv(t *testing.T) *aiToolEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := &aiToolEnv{stats: &aiLeakStats{}, groups: &aiToolGroups{}}
	cfg := Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Stats: env.stats, Groups: env.groups, Limiter: allowAll{}, SessionTTL: time.Hour,
		Lists: &fakeLists{data: map[string]string{"ip_test.dat": "1.2.3.4\n"}}, Keys: fakeKeys{},
		Offers:    NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:  NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:   NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:  NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		Domains:   NewJSONStore[config.Domain](filepath.Join(dir, "domains.json")),
		Boards:    NewJSONStore[config.Board](filepath.Join(dir, "boards.json")),
		UsersFile: filepath.Join(dir, "users.json"), APIKeysFile: filepath.Join(dir, "api-keys.json"),
		KeitaroKey: aiCanaryKTEnv, SecretValues: []string{aiCanaryCH},
	}
	cfg.CloudflareFile = writeAIFile(t, dir, "cloudflare.json",
		mustJSONText(t, map[string]string{"token": aiCanaryCF, "account_id": "0123456789abcdef0123456789abcdef"}))
	cfg.NamecheapFile = writeAIFile(t, dir, "namecheap.json", mustJSONText(t, map[string]any{
		"name": "nc", "api_user": "ncuser", "username": "ncuser", "client_ip": "192.0.2.1", "api_key": aiCanaryNC,
	}))
	cfg.KeitaroFile = writeAIFile(t, dir, "keitaro.json",
		mustJSONText(t, map[string]string{"base_url": "https://kt.example.test", "api_key": aiCanaryKT}))
	cfg.AIFile = writeAIFile(t, dir, "ai.json", mustJSONText(t, map[string]any{
		"enabled": true, "provider": "openai",
		"keys": map[string]string{"openai": aiCanaryAIPlan, "anthropic": aiCanaryAIAnth},
	}))
	cfg.AIDir = filepath.Join(dir, "assistant")
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	env.s = s
	env.srv = httptest.NewServer(s.Handler())
	t.Cleanup(env.srv.Close)
	env.root = security.Session{User: "admin", PwFP: s.current().fp}
	env.literals = map[string]string{
		"cloudflare": aiCanaryCF, "namecheap": aiCanaryNC, "keitaro file": aiCanaryKT,
		"keitaro env": aiCanaryKTEnv, "clickhouse": aiCanaryCH, "ai openai": aiCanaryAIPlan,
		"ai anthropic": aiCanaryAIAnth, "password hash": s.current().hash,
	}
	env.seed(t)
	return env
}

// seed fills the catalogs and the campaign store. One offer per literal secret carries it in its
// name, as a user could paste it.
func (e *aiToolEnv) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	offers := []config.Offer{
		{ID: "o1", Name: "Alpha", Status: true, Payout: 7777, PayoutCurrency: "USD",
			URL: "https://x.test/p/" + aiLongToken + "?token=CANARYQ&sig=CANARYSIG&cid={subid}"},
		{ID: "o2", Name: "Beta", Status: true, URL: "https://x.test/b"},
	}
	i := 0
	for label, v := range e.literals {
		i++
		offers = append(offers, config.Offer{ID: "leak" + string(rune('a'+i)), Name: "note " + label + " " + v, Status: true, URL: "https://x.test/l"})
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.s.cfg.Offers.Save(ctx, offers))
	must(e.s.cfg.Networks.Save(ctx, []config.AffiliateNetwork{{
		ID: "n1", Name: "Net", Status: true,
		PostbackURL: "https://t.test/postback/" + aiLongToken2 + "/?cid={subid}&token=CANARYNET",
	}}))
	must(e.s.cfg.Landings.Save(ctx, []config.Landing{{
		ID: "l1", Name: "Land", Status: true, URL: "https://l.test/a?token=CANARYLAND",
	}}))
	must(e.s.cfg.Sources.Save(ctx, []config.TrafficSource{{ID: "s1", Name: "Src"}}))
	must(e.s.cfg.Domains.Save(ctx, []config.Domain{{ID: "d1", Host: "tds.example.test", Role: "both"}}))
	must(e.groups.Save(ctx, []config.Group{{
		ID: "g1", Name: "Demo", Status: true, Redirect: "stop", CostToken: "{price}", SaveKeys: true,
		Postbacks:   []config.OutboundPostback{{URL: "https://hook.test/?token=CANARYPB", Method: "GET"}},
		PostbackIDs: []string{"s2s1"},
		Streams:     []config.Stream{{Name: "main", Status: true, Curl: "CANARYCURL", BotCurl: "CANARYBCURL"}},
	}}))
}

// allCanaries lists every value that must never appear in a tool result, spec or error.
func (e *aiToolEnv) allCanaries(t *testing.T) []string {
	t.Helper()
	out := append([]string{}, aiContentCanaries...)
	for _, v := range e.literals {
		out = append(out, v)
	}
	out = append(out, e.s.processSecrets()...)
	if u, ok := e.s.userByID(e.root.UID); ok {
		out = append(out, u.Hash)
	}
	return out
}

// assertClean fails when text holds any canary.
func (e *aiToolEnv) assertClean(t *testing.T, label, text string) {
	t.Helper()
	for _, c := range e.allCanaries(t) {
		if c != "" && strings.Contains(text, c) {
			t.Errorf("%s leaks %q: %.300s", label, c, text)
		}
	}
}

// aiToolTestArgs gives valid arguments for every read tool. A read tool of the registry without
// an entry fails the leak test, so a new tool cannot skip it.
var aiToolTestArgs = map[string]string{
	"list_offers": `{}`, "get_offer": `{"id":"o1"}`,
	"list_landings": `{}`, "get_landing": `{"id":"l1"}`,
	"list_affiliate_networks": `{}`, "get_affiliate_network": `{"id":"n1"}`,
	"list_traffic_sources": `{}`, "get_traffic_source": `{"id":"s1"}`,
	"list_domains": `{}`, "get_domain": `{"id":"d1"}`,
	"list_campaigns": `{}`, "list_boards": `{}`,
	"stats_summary": `{}`, "stats_timeseries": `{}`, "stats_breakdown": `{"dim":"referer"}`,
	"stats_performance": `{}`, "stats_facts": `{}`,
	"list_logs": `{}`, "list_postbacks": `{}`,
	"list_log_values": `{"table":"events","field":"referer"}`,
	"get_report":      `{}`, "list_log_fields": `{}`,
	"keyword_stats": `{}`, "keyword_trend": `{}`,
	"simulate_visitor": `{"body":{"groups":[{"id":"g1","name":"Demo","status":true,"redirect":"stop","streams":[]}],` +
		`"group":"g1","visitor":{"ip":"203.0.113.77","referer":"https://ref.test/p?gclid=CANARYG"}}}`,
	"lookup_ip": `{"ip":"203.0.113.77"}`,
}

// aiUnitErrorTools lists the read tools that answer an error in the unit test, with the reason.
// The answer of such a tool must carry no canary either: an error instead of data is fine.
var aiUnitErrorTools = map[string]string{}

func TestAssistantToolsNoSecretLeak(t *testing.T) {
	env := newAIToolEnv(t)
	secrets := env.s.processSecrets()
	for label, v := range env.literals {
		if !slices.Contains(secrets, v) {
			t.Errorf("processSecrets misses the %s secret", label)
		}
	}
	ran := 0
	for _, tool := range env.s.aiTools(env.root) {
		if tool.Mode != asstRead {
			continue
		}
		args, ok := aiToolTestArgs[tool.Name]
		if !ok {
			t.Errorf("read tool %s has no entry in aiToolTestArgs", tool.Name)
			continue
		}
		ran++
		res, err := env.s.aiRunRead(context.Background(), env.root, "", tool, json.RawMessage(args))
		text := string(res)
		if err != nil {
			text = err.Error()
		}
		env.assertClean(t, tool.Name, text)
		reason, expectErr := aiUnitErrorTools[tool.Name]
		switch {
		case expectErr && err == nil:
			t.Errorf("%s answers now; remove it from aiUnitErrorTools (%s)", tool.Name, reason)
		case !expectErr && err != nil:
			t.Errorf("%s failed: %v", tool.Name, err)
		}
	}
	if ran < 20 {
		t.Errorf("only %d read tools ran", ran)
	}
	for name := range aiUnitErrorTools {
		if tool, ok := aiToolNamed(name); !ok || tool.Mode != asstRead {
			t.Errorf("aiUnitErrorTools names %s, which is not a read tool", name)
		}
	}
}

func TestAssistantToolsContentIsRedactedNotEmpty(t *testing.T) {
	env := newAIToolEnv(t)
	call := func(name, args string) string {
		tool, ok := aiToolNamed(name)
		if !ok {
			t.Fatalf("no tool %s", name)
		}
		res, err := env.s.aiRunRead(context.Background(), env.root, "", tool, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return string(res)
	}
	offers := call("list_offers", `{}`)
	for _, want := range []string{"Alpha", "https://x.test/p/***?token=***&sig=***&cid={subid}", "note cloudflare ***"} {
		if !strings.Contains(offers, want) {
			t.Errorf("list_offers lacks %q: %s", want, offers)
		}
	}
	if got := call("get_affiliate_network", `{"id":"n1"}`); !strings.Contains(got, "https://t.test/postback/***/?cid={subid}&token=***") {
		t.Errorf("network link: %s", got)
	}
	logs := call("list_logs", `{}`)
	for _, want := range []string{`"ip":"203.0.113.x"`, "https://ref.test/p", "https://off.test/o"} {
		if !strings.Contains(logs, want) {
			t.Errorf("list_logs lacks %q: %s", want, logs)
		}
	}
	if strings.Contains(logs, "gclid") || strings.Contains(logs, "cid=1") {
		t.Errorf("list_logs keeps a link query: %s", logs)
	}
	if pb := call("list_postbacks", `{}`); !strings.Contains(pb, "198.51.100.x") || strings.Contains(pb, "fbclid") {
		t.Errorf("list_postbacks: %s", pb)
	}
}

func TestAssistantToolsSpecsHoldNoSecrets(t *testing.T) {
	env := newAIToolEnv(t)
	keyFormat := regexp.MustCompile(`sk-[A-Za-z0-9]|AIza|ghp_|Bearer `)
	for _, tool := range aiRegistry() {
		raw, err := json.Marshal(tool.Spec)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		env.assertClean(t, tool.Name+" spec", string(raw))
		if keyFormat.Match(raw) {
			t.Errorf("%s spec holds a key format: %s", tool.Name, raw)
		}
	}
}

func TestAssistantToolNamesMatchMCP(t *testing.T) {
	if got := aiToolName("a.b/c d-é_9"); got != "a_b_c_d-__9" {
		t.Errorf("aiToolName = %q", got)
	}
	if got := aiToolName(strings.Repeat("x", 100)); len(got) != 64 {
		t.Errorf("long name has %d characters", len(got))
	}
	env := newAIToolEnv(t)
	_, ops := openAPIOperations(t, env.s, env.root)
	bad := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	seen := map[string]bool{}
	for _, tool := range aiRegistry() {
		if seen[tool.Name] {
			t.Errorf("duplicate tool name %s", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Name == aiToolCreateCampaign {
			continue
		}
		op, ok := ops[convertedKey(tool.Pattern)]
		if !ok {
			t.Errorf("%s: %s is not in OpenAPI", tool.Name, tool.Pattern)
			continue
		}
		id, _ := op["operationId"].(string)
		want := bad.ReplaceAllString(id, "_")
		if len(want) > 64 {
			want = want[:64]
		}
		if tool.Name != want {
			t.Errorf("%s: MCP name of %s is %q", tool.Name, tool.Pattern, want)
		}
		if op["x-mcp"] == nil {
			t.Errorf("%s: route is not visible to MCP", tool.Name)
		}
	}
	if want := len(assistantPatterns()) + 1; len(aiRegistry()) != want {
		t.Errorf("registry has %d tools, want %d (every marked route plus create_campaign)", len(aiRegistry()), want)
	}
}

func TestAssistantToolsRegistryShape(t *testing.T) {
	type want struct{ resource, op, mode string }
	cases := map[string]want{
		"list_offers":              {"offers", "", asstRead},
		"get_offer":                {"offers", "", asstRead},
		"get_affiliate_network":    {"affiliate_networks", "", asstRead},
		"list_traffic_sources":     {"traffic_sources", "", asstRead},
		"list_campaigns":           {"campaigns", "", asstRead},
		"list_logs":                {"logs", "", asstRead},
		"stats_summary":            {"stats", "", asstRead},
		"create_offer":             {"offers", aiOpCreate, asstPropose},
		"update_offer":             {"offers", aiOpUpdate, asstPropose},
		"delete_offer":             {"offers", aiOpDelete, asstPropose},
		"update_affiliate_network": {"affiliate_networks", aiOpUpdate, asstPropose},
		"delete_traffic_source":    {"traffic_sources", aiOpDelete, asstPropose},
		"create_landing":           {"landings", aiOpCreate, asstPropose},
		"delete_landing":           {"landings", aiOpDelete, asstPropose},
		"update_campaign":          {"campaigns", aiOpUpdate, asstPropose},
		"create_campaign":          {"campaigns", aiOpCreate, asstPropose},
		"delete_campaign":          {"campaigns", aiOpDelete, asstPropose},
	}
	for name, w := range cases {
		tool, ok := aiToolNamed(name)
		if !ok {
			t.Errorf("no tool %s", name)
			continue
		}
		if tool.Resource != w.resource || tool.Op != w.op || tool.Mode != w.mode {
			t.Errorf("%s = resource %q op %q mode %q, want %+v", name, tool.Resource, tool.Op, tool.Mode, w)
		}
		if tool.Spec.Name != name || tool.Spec.Description == "" {
			t.Errorf("%s: spec %+v", name, tool.Spec)
		}
	}
	props := func(name string) (map[string]any, []string) {
		tool, _ := aiToolNamed(name)
		p, _ := tool.Spec.Schema["properties"].(map[string]any)
		req, _ := tool.Spec.Schema["required"].([]string)
		return p, req
	}
	if p, req := props("create_offer"); p["patch"] == nil || p["id"] != nil || !slices.Equal(req, []string{"patch"}) {
		t.Errorf("create_offer schema: %v %v", p, req)
	}
	if p, req := props("update_offer"); p["patch"] == nil || p["id"] == nil || !slices.Equal(req, []string{"id", "patch"}) {
		t.Errorf("update_offer schema: %v %v", p, req)
	}
	if p, req := props("delete_offer"); p["patch"] != nil || p["id"] == nil || !slices.Equal(req, []string{"id"}) {
		t.Errorf("delete_offer schema: %v %v", p, req)
	}
	if p, req := props("create_campaign"); p["id"] != nil || !slices.Equal(req, []string{"patch"}) {
		t.Errorf("create_campaign schema: %v %v", p, req)
	}
	logs, _ := props("list_logs")
	limit, _ := logs["limit"].(map[string]any)
	if limit["maximum"] != aiReadLimitMax {
		t.Errorf("list_logs limit = %v", limit)
	}
	if p, req := props("simulate_visitor"); p["body"] == nil || !slices.Contains(req, "body") {
		t.Errorf("simulate_visitor schema: %v %v", p, req)
	}
	for _, tool := range aiRegistry() {
		if tool.Mode == asstPropose && tool.Pattern == "" {
			t.Errorf("%s: no pattern", tool.Name)
		}
		if tool.Mode == asstRead && tool.Op != "" {
			t.Errorf("%s: read tool with op %q", tool.Name, tool.Op)
		}
	}
}

func TestAssistantToolsFilterBySession(t *testing.T) {
	env := newAIToolEnv(t)
	names := func(sess security.Session) []string {
		var out []string
		for _, tool := range env.s.aiTools(sess) {
			out = append(out, tool.Name)
		}
		return out
	}
	root := names(env.root)
	for _, want := range []string{"list_offers", "list_logs", "update_offer", "delete_campaign", "create_campaign"} {
		if !slices.Contains(root, want) {
			t.Errorf("the administrator lacks %s", want)
		}
	}
	lim := env.limitedSession(t, "lim", security.Access{
		Sections: []string{"offers"},
		Grants:   map[string]security.Grant{"offers": {Mode: security.ModeSelected, IDs: []string{"o1"}}},
	})
	got := names(lim)
	if !slices.Contains(got, "list_offers") || !slices.Contains(got, "update_offer") {
		t.Errorf("limited user lacks the offers tools: %v", got)
	}
	for _, denied := range []string{"list_landings", "list_logs", "stats_summary", "update_campaign"} {
		if slices.Contains(got, denied) {
			t.Errorf("limited user is offered %s", denied)
		}
	}
	if len(names(security.Session{API: true, KeyType: security.KeyTypeUser})) >= len(root) {
		t.Error("a USER key sees every tool")
	}
}

// limitedSession creates a USER account with access and returns its session.
func (e *aiToolEnv) limitedSession(t *testing.T, login string, acc security.Access) security.Session {
	t.Helper()
	created, err := e.s.createUser(userDraft{Login: login, Password: "secret1", Type: security.UserRegular, Enabled: true, Access: acc})
	if err != nil {
		t.Fatal(err)
	}
	u, ok := e.s.userByID(created.ID)
	if !ok {
		t.Fatal("user missing")
	}
	return security.Session{User: u.Login, UID: u.ID, PwFP: u.Fingerprint()}
}

func TestAssistantToolsLimitedUserSeesWhatTheUISees(t *testing.T) {
	env := newAIToolEnv(t)
	lim := env.limitedSession(t, "lim", security.Access{
		Sections: []string{"offers"},
		Grants:   map[string]security.Grant{"offers": {Mode: security.ModeSelected, IDs: []string{"o1"}}},
		Hidden:   []string{"profit"},
	})
	c := newClient(t)
	if code, _ := login(t, c, env.srv.URL, "lim", "secret1"); code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	resp, err := c.Get(env.srv.URL + "/api/offers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ui []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
		t.Fatal(err)
	}
	tool, _ := aiToolNamed("list_offers")
	res, err := env.s.aiRunRead(context.Background(), lim, "", tool, nil)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(res, &page); err != nil {
		t.Fatalf("%v: %s", err, res)
	}
	viaTool := page.Items
	if page.Total != len(ui) {
		t.Errorf("total %d, the UI shows %d", page.Total, len(ui))
	}
	summary := func(rows []map[string]any) map[string]any {
		out := map[string]any{}
		for _, r := range rows {
			out[r["id"].(string)] = r["payout"]
		}
		return out
	}
	if len(ui) != 1 || ui[0]["id"] != "o1" {
		t.Fatalf("the UI shows %v", ui)
	}
	if got, want := aiTestJSON(summary(viaTool)), aiTestJSON(summary(ui)); got != want {
		t.Errorf("tool shows %s, the UI shows %s", got, want)
	}
	if strings.Contains(string(res), "7777") || strings.Contains(string(res), "Beta") || strings.Contains(string(res), "leak") {
		t.Errorf("the tool shows what the user may not see: %s", res)
	}
	get, _ := aiToolNamed("get_offer")
	_, err = env.s.aiRunRead(context.Background(), lim, "", get, json.RawMessage(`{"id":"o2"}`))
	var te *aiToolError
	if !asToolError(err, &te) || te.Code != "not_found" {
		t.Errorf("a foreign offer answers %v", err)
	}
	if one, err := env.s.aiRunRead(context.Background(), lim, "", get, json.RawMessage(`{"id":"o1"}`)); err != nil || strings.Contains(string(one), "7777") {
		t.Errorf("own offer: %s %v", one, err)
	}
}

func TestAssistantToolsCampaignsLoseOutboundFields(t *testing.T) {
	env := newAIToolEnv(t)
	tool, _ := aiToolNamed("list_campaigns")
	res, err := env.s.aiRunRead(context.Background(), env.root, "", tool, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(res)
	for _, gone := range []string{"postbacks", "postback_ids", "curl", "b_curl", "hook.test", "CANARYPB", "CANARYCURL", "CANARYBCURL", "s2s1"} {
		if strings.Contains(text, gone) {
			t.Errorf("campaign result keeps %q: %s", gone, text)
		}
	}
	for _, keep := range []string{`"id":"g1"`, `"name":"Demo"`, `"cost_token":"{price}"`, `"save_keys":true`, `"streams"`} {
		if !strings.Contains(text, keep) {
			t.Errorf("campaign result lost %q: %s", keep, text)
		}
	}
}
