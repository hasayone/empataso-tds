package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
)

// aiOpenRouterModels is an OpenRouter /models answer: a model with tools and a price that differs
// from the built-in table, one without tools, a router priced at -1, a model with no
// supported_parameters and one without a price.
const aiOpenRouterModels = `{"data":[
 {"id":"vendor/no-tools","name":"No Tools","context_length":8192,"pricing":{"prompt":"0.0000005","completion":"0.000001"},"supported_parameters":["temperature"]},
 {"id":"anthropic/claude-sonnet-4","name":"Anthropic: Claude Sonnet 4","context_length":200000,"pricing":{"prompt":"0.000004","completion":"0.00002"},"supported_parameters":["tools","temperature"]},
 {"id":"openrouter/auto","name":"Auto Router","context_length":2000000,"pricing":{"prompt":"-1","completion":"-1"},"supported_parameters":["tools"]},
 {"id":"vendor/cheap","name":"Cheap","context_length":32000,"pricing":{"prompt":"0.0000002","completion":"0.0000008"},"supported_parameters":["tools"]},
 {"id":"vendor/vague","name":"Vague","context_length":null,"pricing":{"prompt":"0.000001","completion":"0.000002"}},
 {"id":"vendor/cheap","name":"Duplicate"},
 {"id":"","name":"No id"}
]}`

// aiModelsEnv is a server whose openrouter provider talks to a stub. The stub answers
// aiOpenRouterModels, or the status in fail when it is not 0. The clock is fixed and moved by
// tick.
type aiModelsEnv struct {
	*aiTestEnv
	stub  *aiStub
	fail  atomic.Int32
	clock atomic.Int64
}

func newAIModelsEnv(t *testing.T) *aiModelsEnv {
	t.Helper()
	m := &aiModelsEnv{}
	m.clock.Store(time.Now().UnixNano())
	m.stub = newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if code := int(m.fail.Load()); code != 0 {
			aiJSONReply(w, code, `{"error":{"message":"nope"}}`)
			return
		}
		aiJSONReply(w, 200, aiOpenRouterModels)
	})
	m.aiTestEnv = newAITestEnv(t, func(c *Config) {
		c.AIBaseURLs = map[string]string{aiProviderOpenRouter: m.stub.srv.URL}
	})
	m.s.ai.models.now = func() time.Time { return time.Unix(0, m.clock.Load()) }
	m.mustPut(t, `{"provider":"openrouter","keys":{"openrouter":"`+aiCanary2+`"}}`)
	return m
}

// tick moves the clock of the catalog by d.
func (m *aiModelsEnv) tick(d time.Duration) { m.clock.Add(int64(d)) }

// models calls GET /api/ai/models with the query and returns the status and the body.
func (m *aiModelsEnv) models(query string, sess ...security.Session) (int, string) {
	req, _ := http.NewRequest(http.MethodGet, "/api/ai/models?"+query, nil)
	return m.callReq(m.s.handleAIModels, req, sess...)
}

// callReq runs h on req with an optional session.
func (m *aiModelsEnv) callReq(h http.HandlerFunc, req *http.Request, sess ...security.Session) (int, string) {
	if len(sess) > 0 {
		req = req.WithContext(context.WithValue(req.Context(), sessionKey, sess[0]))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, rec.Body.String()
}

// aiCatalogBody decodes the answer of GET /api/ai/models.
func aiCatalogBody(t *testing.T, body string) aiCatalogView {
	t.Helper()
	var v aiCatalogView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return v
}

// aiModelByID returns the model with id from models, failing the test when absent.
func aiModelByID(t *testing.T, models []aiModelInfo, id string) aiModelInfo {
	t.Helper()
	for _, m := range models {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("model %s is not listed: %v", id, aiIDs(models))
	return aiModelInfo{}
}

func TestAIModelsOpenRouterParse(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) { aiJSONReply(w, 200, aiOpenRouterModels) })
	p := newOpenAIProvider(aiProviderOpenRouter, stub.srv.URL, aiTestKey, stub.srv.Client())
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(aiIDs(models), ","); got != "anthropic/claude-sonnet-4,openrouter/auto,vendor/cheap,vendor/no-tools,vendor/vague" {
		t.Fatalf("sorted, without duplicates and empty ids: %s", got)
	}
	sonnet := aiModelByID(t, models, "anthropic/claude-sonnet-4")
	if sonnet.Name != "Anthropic: Claude Sonnet 4" || sonnet.Context != 200000 || sonnet.Tools == nil || !*sonnet.Tools ||
		sonnet.InputPerM == nil || *sonnet.InputPerM != 4 || sonnet.OutputPerM == nil || *sonnet.OutputPerM != 20 {
		t.Fatalf("sonnet %+v", sonnet)
	}
	if cheap := aiModelByID(t, models, "vendor/cheap"); cheap.Name != "Cheap" || *cheap.InputPerM != 0.2 || *cheap.OutputPerM != 0.8 {
		t.Fatalf("a per token price is shown per million without float noise: %+v", cheap)
	}
	if nt := aiModelByID(t, models, "vendor/no-tools"); nt.Tools == nil || *nt.Tools {
		t.Fatalf("no tools in supported_parameters means tools:false: %+v", nt)
	}
	if auto := aiModelByID(t, models, "openrouter/auto"); auto.InputPerM != nil || auto.OutputPerM != nil || auto.Tools == nil || !*auto.Tools {
		t.Fatalf("a negative price is no price: %+v", auto)
	}
	if vague := aiModelByID(t, models, "vendor/vague"); vague.Tools != nil || vague.Context != 0 {
		t.Fatalf("without supported_parameters the tools are unknown: %+v", vague)
	}
	if req := stub.req(0); req.Path != "/models" || req.Header.Get("Authorization") != "Bearer "+aiTestKey {
		t.Fatalf("request %s %v", req.Path, req.Header)
	}
}

func TestAIModelsOtherProvidersGiveIDsOnly(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, 200, `{"data":[{"id":"models/m-b","display_name":"Model B","context_length":9999,"pricing":{"prompt":"1","completion":"1"},"supported_parameters":["tools"]},{"id":"models/m-a"}]}`)
	})
	cases := map[string]aiProvider{
		aiProviderAnthropic: newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()),
		aiProviderOpenAI:    newOpenAIProvider(aiProviderOpenAI, stub.srv.URL, aiTestKey, stub.srv.Client()),
		aiProviderGemini:    newOpenAIProvider(aiProviderGemini, stub.srv.URL, aiTestKey, stub.srv.Client()),
	}
	for id, p := range cases {
		models, err := p.Models(context.Background())
		if err != nil || len(models) != 2 {
			t.Fatalf("%s: %v %v", id, models, err)
		}
		for _, m := range models {
			if m.Tools != nil || m.InputPerM != nil || m.OutputPerM != nil || m.Context != 0 {
				t.Errorf("%s gives id and name only: %+v", id, m)
			}
		}
		wantID := "models/m-a"
		if id == aiProviderGemini {
			wantID = "m-a"
		}
		if models[0].ID != wantID {
			t.Errorf("%s first id %s, want %s", id, models[0].ID, wantID)
		}
		if id == aiProviderAnthropic && models[1].Name != "Model B" {
			t.Errorf("anthropic display_name is the name: %+v", models[1])
		}
	}
}

func TestAIModelsCacheAndRefresh(t *testing.T) {
	m := newAIModelsEnv(t)
	code, body := m.models("provider=openrouter")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	v := aiCatalogBody(t, body)
	if v.Provider != aiProviderOpenRouter || v.TTLSeconds != 3600 || v.FetchedAt == "" || len(v.Models) != 5 {
		t.Fatalf("answer %+v", v)
	}
	if m.stub.calls() != 1 {
		t.Fatalf("first call asks the provider: %d", m.stub.calls())
	}
	m.models("provider=openrouter")
	m.models("")
	if m.stub.calls() != 1 {
		t.Fatalf("a fresh catalog is served without a request: %d", m.stub.calls())
	}
	m.models("provider=openrouter&refresh=1")
	if m.stub.calls() != 2 {
		t.Fatalf("refresh=1 asks again: %d", m.stub.calls())
	}
	m.tick(59 * time.Minute)
	m.models("provider=openrouter")
	if m.stub.calls() != 2 {
		t.Fatalf("inside the TTL: %d", m.stub.calls())
	}
	m.tick(2 * time.Minute)
	m.models("provider=openrouter")
	if m.stub.calls() != 3 {
		t.Fatalf("an expired catalog is fetched again: %d", m.stub.calls())
	}

	m.fail.Store(http.StatusUnauthorized)
	code, body = m.models("refresh=1")
	if code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadKey {
		t.Fatalf("provider refusal: %d %s", code, body)
	}
	m.fail.Store(0)
	if cat := m.s.aiCatalogGet(aiProviderOpenRouter); cat == nil || len(cat.Models) != 5 {
		t.Fatal("a failed refresh must keep the last catalog")
	}
	if strings.Contains(body, aiCanary) {
		t.Fatalf("key in the answer: %s", body)
	}
}

func TestAIModelsCacheFile(t *testing.T) {
	m := newAIModelsEnv(t)
	if code, body := m.models(""); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	path := filepath.Join(m.dir, "assistant", "models-openrouter.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk aiCatalog
	if err := json.Unmarshal(raw, &disk); err != nil || disk.Provider != aiProviderOpenRouter || len(disk.Models) != 5 {
		t.Fatalf("file %v %s", err, raw)
	}
	if strings.Contains(string(raw), aiCanary) {
		t.Fatal("the key must not reach the catalog file")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v", info, err)
		}
	}
	again := newAITestEnv(t, func(c *Config) { c.AIDir = filepath.Join(m.dir, "assistant") })
	if cat := again.s.aiCatalogGet(aiProviderOpenRouter); cat == nil || len(cat.Models) != 5 {
		t.Fatalf("a restarted server reads the file: %+v", cat)
	}
	if again.s.aiCatalogGet(aiProviderOpenAI) != nil {
		t.Fatal("a provider without a file has no catalog")
	}
	if err := os.WriteFile(path, []byte(`{"provider":"openai","models":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	third := newAITestEnv(t, func(c *Config) { c.AIDir = filepath.Join(m.dir, "assistant") })
	if third.s.aiCatalogGet(aiProviderOpenRouter) != nil {
		t.Fatal("a file of another provider is not used")
	}
}

func TestAIModelsHandlerErrors(t *testing.T) {
	m := newAIModelsEnv(t)
	plain, err := m.s.createUser(userDraft{Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	code, body := m.models("provider=bogus")
	if code != http.StatusBadRequest {
		t.Fatalf("unknown provider: %d %s", code, body)
	}
	code, body = m.models("provider=openai")
	if code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadKey {
		t.Fatalf("no key: %d %s", code, body)
	}
	for who, sess := range map[string]security.Session{
		"api key": {API: true, KeyType: security.KeyTypeAdmin, KeyID: "k"},
		"user":    {UID: plain.ID, User: "bob"},
	} {
		if code, _ = m.models("provider=openrouter", sess); code != http.StatusForbidden {
			t.Errorf("%s: %d", who, code)
		}
	}
	if m.stub.calls() != 0 {
		t.Fatal("a refused request must not reach the provider")
	}
}

func TestAIModelsFilledFromTable(t *testing.T) {
	m := newAIModelsEnv(t)
	_, body := m.models("")
	v := aiCatalogBody(t, body)
	if auto := aiModelByID(t, v.Models, "openrouter/auto"); auto.InputPerM != nil {
		t.Fatalf("no price anywhere: %+v", auto)
	}
	m.mustPut(t, `{"provider":"anthropic"}`)
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, 200, `{"data":[{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4"},{"id":"unlisted-model"}]}`)
	})
	m.s.cfg.AIBaseURLs[aiProviderAnthropic] = stub.srv.URL
	m.mustPut(t, `{"keys":{"anthropic":"`+aiCanary2+`"}}`)
	_, body = m.models("provider=anthropic")
	v = aiCatalogBody(t, body)
	got := aiModelByID(t, v.Models, "claude-sonnet-4-20250514")
	if got.Name != "Claude Sonnet 4" || got.InputPerM == nil || *got.InputPerM != 3 || *got.OutputPerM != 15 || got.Tools != nil {
		t.Fatalf("the built-in price is shown: %+v", got)
	}
	if un := aiModelByID(t, v.Models, "unlisted-model"); un.InputPerM != nil {
		t.Fatalf("%+v", un)
	}
	if cat := m.s.aiCatalogGet(aiProviderAnthropic); cat.Models[0].InputPerM != nil {
		t.Fatal("the table price is for the answer only, not stored in the catalog")
	}
}

func TestAIModelsTestEndpointRefreshesCache(t *testing.T) {
	m := newAIModelsEnv(t)
	if m.s.aiCatalogGet(aiProviderOpenRouter) != nil {
		t.Fatal("no catalog before the first call")
	}
	code, body := m.call(m.s.handleAITest, http.MethodPost, `{"provider":"openrouter","key":"`+aiCanary2+`"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		Models []aiModelInfo `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Models) != 5 {
		t.Fatalf("%v %s", err, body)
	}
	if sonnet := aiModelByID(t, got.Models, "anthropic/claude-sonnet-4"); *sonnet.InputPerM != 4 || sonnet.Context != 200000 {
		t.Fatalf("objects, not ids: %+v", sonnet)
	}
	if req := m.stub.req(0); req.Header.Get("Authorization") != "Bearer "+aiCanary2 {
		t.Fatalf("the typed key is used: %v", req.Header)
	}
	if cat := m.s.aiCatalogGet(aiProviderOpenRouter); cat == nil || len(cat.Models) != 5 {
		t.Fatal("the test refreshes the cache")
	}
	m.models("")
	if m.stub.calls() != 1 {
		t.Fatalf("the catalog the test made is fresh: %d calls", m.stub.calls())
	}
	code, _ = m.call(m.s.handleAITest, http.MethodPost, `{"provider":"openrouter"}`)
	if code != http.StatusOK || m.stub.calls() != 2 {
		t.Fatalf("a test always asks the provider: %d %d", code, m.stub.calls())
	}
}

func TestAIModelsPutChecksCatalog(t *testing.T) {
	m := newAIModelsEnv(t)
	params := func(body string) map[string]any {
		var e struct {
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal([]byte(body), &e)
		return e.Params
	}
	badModel := func(put, wantModel string) {
		t.Helper()
		code, body := m.put(put)
		if code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadModel || params(body)["model"] != wantModel {
			t.Fatalf("%s -> %d %s", put, code, body)
		}
	}
	badModel(`{"allowed_models":["vendor/cheap"],"model":"vendor/cheap"}`, "vendor/cheap")
	if code, body := m.put(`{"allowed_models":["vendor/cheap"]}`); !strings.Contains(body, "check the API key") {
		t.Fatalf("without a catalog the text asks to check the key: %d %s", code, body)
	}
	if code, _ := m.put(`{"max_steps":3,"daily_usd":4}`); code != http.StatusOK {
		t.Fatalf("limits need no catalog: %d", code)
	}
	if m.stub.calls() != 0 {
		t.Fatal("a PUT never asks the provider")
	}

	m.models("")
	badModel(`{"allowed_models":["vendor/cheap","vendor/unknown"]}`, "vendor/unknown")
	badModel(`{"allowed_models":["vendor/no-tools"],"model":"vendor/no-tools"}`, "vendor/no-tools")
	badModel(`{"allowed_models":["vendor/cheap"],"model":"vendor/cheap","provider":"openai"}`, "vendor/cheap")
	if _, err := os.Stat(filepath.Join(m.dir, "ai.json")); err != nil {
		t.Fatal(err)
	}
	if v := m.view(t); len(v.AllowedModels) != 0 {
		t.Fatalf("refused PUTs changed the settings: %+v", v)
	}

	v := m.mustPut(t, `{"allowed_models":["anthropic/claude-sonnet-4","vendor/cheap","openrouter/auto","vendor/vague"],"model":"vendor/cheap"}`)
	if v.Model != "vendor/cheap" || len(v.AllowedModels) != 4 {
		t.Fatalf("%+v", v)
	}
	if p := v.Prices["vendor/cheap"]; p.InputPerM != 0.2 || p.OutputPerM != 0.8 {
		t.Fatalf("the catalog price is effective: %+v", v.Prices)
	}
	if p := v.DefaultPrices["vendor/cheap"]; p.InputPerM != 0.2 {
		t.Fatalf("default_prices holds the catalog price: %+v", v.DefaultPrices)
	}
	if p := v.Prices["anthropic/claude-sonnet-4"]; p.InputPerM != 4 || p.OutputPerM != 20 {
		t.Fatalf("catalog beats the built-in table (3/15): %+v", p)
	}
	if got := strings.Join(v.PriceUnknown, ","); got != "openrouter/auto" {
		t.Fatalf("price_unknown with the catalog taken into account: %s", got)
	}

	v = m.mustPut(t, `{"prices":{"vendor/cheap":{"input_per_m":9,"output_per_m":9},"anthropic/claude-sonnet-4":{"input_per_m":1,"output_per_m":2}}}`)
	if p := v.Prices["vendor/cheap"]; p.InputPerM != 9 {
		t.Fatalf("the administrator price wins: %+v", v.Prices)
	}
	if p := v.DefaultPrices["vendor/cheap"]; p.InputPerM != 0.2 {
		t.Fatalf("the default stays the catalog price: %+v", v.DefaultPrices)
	}
	if p := v.Prices["anthropic/claude-sonnet-4"]; p.InputPerM != 1 {
		t.Fatalf("%+v", v.Prices)
	}

	if err := os.Remove(filepath.Join(m.dir, "assistant", "models-openrouter.json")); err != nil {
		t.Fatal(err)
	}
	if code, _ := m.put(`{"max_steps":4}`); code != http.StatusOK {
		t.Fatalf("a PUT that names no model needs no catalog: %d", code)
	}
}

func TestAIModelsReserveUsesCatalogPrice(t *testing.T) {
	m := newAIModelsEnv(t)
	m.models("")
	st := aiUsageSettings(aiTestPtr(int64(0)), aiTestPtr(int64(0)), aiTestPtr(0.0), aiTestPtr(0.0))
	st.Provider = aiProviderOpenRouter
	r, err := m.s.aiReserve("u", "vendor/cheap", st)
	if err != nil || !r.Priced || r.Price.InputPerM != 0.2 {
		t.Fatalf("the catalog price is used for the accounting: %+v %v", r, err)
	}
	m.s.aiCommit(r, 1_000_000, 0)
	if _, total, _ := m.s.aiUsageSnapshot(); total.USD < 0.19 || total.USD > 0.21 {
		t.Fatalf("usd %v", total.USD)
	}
	st.Provider = aiProviderOpenAI
	if r, err = m.s.aiReserve("u", "vendor/cheap", st); err != nil || r.Priced {
		t.Fatalf("the catalog of another provider does not price the model: %+v %v", r, err)
	}
	st.Provider = aiProviderOpenRouter
	st.Prices = map[string]aiPrice{"vendor/cheap": {InputPerM: 7, OutputPerM: 7}}
	if r, err = m.s.aiReserve("u", "vendor/cheap", st); err != nil || r.Price.InputPerM != 7 {
		t.Fatalf("the administrator price wins: %+v", r.Price)
	}
}

func TestAIModelsUsageMe(t *testing.T) {
	e := newAITestEnv(t, nil)
	boss, err := e.s.createUser(userDraft{Login: "boss", Password: "secret1", Type: security.UserAdmin, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	st := aiNormalize(aiSettings{})
	for uid, in := range map[string]int{"env-admin": 300, boss.ID: 50} {
		r, err := e.s.aiReserve(uid, "mystery", st)
		if err != nil {
			t.Fatal(err)
		}
		e.s.aiCommit(r, in, 10)
	}
	me := func(sess ...security.Session) aiUsageUserView {
		t.Helper()
		_, body := e.call(e.s.handleAISettingsGet, http.MethodGet, "", sess...)
		var v struct {
			Usage struct {
				Tokens int64           `json:"tokens"`
				Me     aiUsageUserView `json:"me"`
				Users  []any           `json:"users"`
			} `json:"usage_today"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		if v.Usage.Tokens != 370 || len(v.Usage.Users) != 2 {
			t.Fatalf("the team view is unchanged: %s", body)
		}
		return v.Usage.Me
	}
	if got := me(); got.UID != "env-admin" || got.Login != "admin" || got.Tokens != 310 {
		t.Fatalf("environment administrator: %+v", got)
	}
	if got := me(security.Session{UID: boss.ID, User: "boss"}); got.UID != boss.ID || got.Login != "boss" || got.Tokens != 60 {
		t.Fatalf("another administrator sees their own usage: %+v", got)
	}
	var raw struct {
		Usage struct {
			Me map[string]any `json:"me"`
		} `json:"usage_today"`
	}
	_, body := e.call(e.s.handleAISettingsGet, http.MethodGet, "")
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Usage.Me) != 4 {
		t.Fatalf("me has uid, login, tokens and usd: %s", body)
	}
}

func TestAIModelsPerMillion(t *testing.T) {
	for in, want := range map[string]float64{`"0.000003"`: 3, `0.000015`: 15, `"0"`: 0, `"0.0000002"`: 0.2, `"0.00000125"`: 1.25} {
		if got := aiPerMillion(json.RawMessage(in)); got == nil || *got != want {
			t.Errorf("%s -> %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{``, `null`, `"-1"`, `"abc"`, `"NaN"`, `"Inf"`} {
		if got := aiPerMillion(json.RawMessage(in)); got != nil {
			t.Errorf("%q must be no price, got %v", in, *got)
		}
	}
}
