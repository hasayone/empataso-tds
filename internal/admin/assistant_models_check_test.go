package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
)

// aiBodyParams returns the "params" object of an error body, nil when there is none.
func aiBodyParams(t *testing.T, body string) map[string]any {
	t.Helper()
	var e struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("not an error body: %s", body)
	}
	return e.Params
}

// wantBadModel fails the test unless code and body are the 400 ai_bad_model answer for model
// with reason and with no other param.
func wantBadModel(t *testing.T, label string, code int, body, model, reason string) {
	t.Helper()
	p := aiBodyParams(t, body)
	if code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadModel || p["model"] != model || p["reason"] != reason || len(p) != 2 {
		t.Fatalf("%s: %d %s, want ai_bad_model %s %s", label, code, body, model, reason)
	}
}

// dropCatalog forgets the catalog of provider in memory and on disk.
func (m *aiModelsEnv) dropCatalog(provider string) {
	m.s.ai.models.mu.Lock()
	delete(m.s.ai.models.cat, provider)
	m.s.ai.models.mu.Unlock()
	_ = os.Remove(m.s.aiCatalogPath(provider))
}

// statusModels calls GET /api/ai/status and returns the model_info objects by id. It fails the
// test when model_info does not follow models or an object lacks one of the seven fields.
func (m *aiModelsEnv) statusModels(t *testing.T) map[string]map[string]any {
	t.Helper()
	code, raw := m.call(m.s.handleAIStatus, http.MethodGet, "", security.Session{})
	var v struct {
		Models    []string         `json:"models"`
		ModelInfo []map[string]any `json:"model_info"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(raw), &v) != nil || len(v.Models) != len(v.ModelInfo) {
		t.Fatalf("%d %s", code, raw)
	}
	out := map[string]map[string]any{}
	for i, info := range v.ModelInfo {
		if info["id"] != v.Models[i] || len(info) != 7 {
			t.Fatalf("model_info follows models and has the 7 fields: %v", info)
		}
		out[v.Models[i]] = info
	}
	return out
}

func TestAINewModels(t *testing.T) {
	cur := aiSettings{Provider: "openai", AllowedModels: []string{"a", "b"}, Model: "a"}
	cases := []struct {
		name string
		next aiSettings
		want []string
	}{
		{"nothing changed", aiSettings{Provider: "openai", AllowedModels: []string{"a", "b"}, Model: "a"}, nil},
		{"fewer models", aiSettings{Provider: "openai", AllowedModels: []string{"b"}, Model: "b"}, nil},
		{"added in the list", aiSettings{Provider: "openai", AllowedModels: []string{"a", "b", "c"}, Model: "a"}, []string{"c"}},
		{"new default is new once", aiSettings{Provider: "openai", AllowedModels: []string{"c"}, Model: "c"}, []string{"c"}},
		{"other provider checks everything", aiSettings{Provider: "gemini", AllowedModels: []string{"a", "b"}, Model: "a"}, []string{"a", "b"}},
		{"no models", aiSettings{Provider: "gemini"}, nil},
	}
	for _, c := range cases {
		if got := aiNewModels(cur, c.next); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAIPutModelsCheckOnlyNewOnes(t *testing.T) {
	m := newAIModelsEnv(t)
	m.models("")
	m.mustPut(t, `{"enabled":true,"allowed_models":["vendor/cheap","vendor/vague"],"model":"vendor/cheap"}`)
	m.dropCatalog(aiProviderOpenRouter)

	for _, body := range []string{
		`{"enabled":false}`,
		`{"max_steps":3,"daily_usd":4,"user_daily_tokens":5000}`,
		`{"allowed_models":["vendor/cheap","vendor/vague"],"model":"vendor/cheap"}`,
		`{"allowed_models":["vendor/vague"],"model":"vendor/vague"}`,
		`{"provider":"openrouter","keys":{"openrouter":"` + aiCanary2 + `"}}`,
	} {
		if code, raw := m.put(body); code != http.StatusOK {
			t.Fatalf("a save that adds no model needs no catalog: %s -> %d %s", body, code, raw)
		}
	}
	code, body := m.put(`{"allowed_models":["vendor/vague","vendor/new"]}`)
	wantBadModel(t, "a new model without a catalog", code, body, "vendor/new", aiModelNoCatalog)
	code, body = m.put(`{"provider":"openai"}`)
	wantBadModel(t, "a changed provider checks the stored models again", code, body, "vendor/vague", aiModelNoCatalog)
	if v := m.view(t); v.Provider != aiProviderOpenRouter || v.Enabled || len(v.AllowedModels) != 1 {
		t.Fatalf("refused saves changed nothing: %+v", v)
	}
	if m.stub.calls() != 1 {
		t.Fatalf("a PUT never asks the provider: %d calls", m.stub.calls())
	}
}

func TestAIPutBadModelReasons(t *testing.T) {
	m := newAIModelsEnv(t)
	code, body := m.put(`{"allowed_models":["vendor/cheap"],"model":"vendor/cheap"}`)
	wantBadModel(t, "no catalog", code, body, "vendor/cheap", aiModelNoCatalog)

	m.models("")
	code, body = m.put(`{"allowed_models":["vendor/cheap","vendor/gone"]}`)
	wantBadModel(t, "not in the catalog", code, body, "vendor/gone", aiModelUnknown)
	code, body = m.put(`{"allowed_models":["vendor/no-tools"]}`)
	wantBadModel(t, "no tools", code, body, "vendor/no-tools", aiModelNoTools)
	code, body = m.put(`{"allowed_models":["vendor/cheap"],"model":"vendor/vague"}`)
	wantBadModel(t, "default outside the list", code, body, "vendor/vague", aiModelNotAllowed)
}

func TestAIPickModelBadReason(t *testing.T) {
	_, err := aiPickModel(aiSettings{AllowedModels: []string{"a"}}, "b")
	if ae := aiErrOf(t, err); ae.code != aiErrBadModel || ae.Params["model"] != "b" || ae.Params["reason"] != aiModelNotAllowed {
		t.Fatalf("%+v", ae)
	}
}

func TestAIStatusModelInfo(t *testing.T) {
	m := newAIModelsEnv(t)
	m.models("")
	m.mustPut(t, `{"enabled":true,"allowed_models":["anthropic/claude-sonnet-4","vendor/cheap","openrouter/auto","vendor/vague"],"model":"vendor/cheap"}`)

	got := m.statusModels(t)
	sonnet := got["anthropic/claude-sonnet-4"]
	if sonnet["name"] != "Anthropic: Claude Sonnet 4" || sonnet["context"] != float64(200000) || sonnet["input_per_m"] != float64(4) ||
		sonnet["output_per_m"] != float64(20) || sonnet["tools"] != true || sonnet["price_unknown"] != false {
		t.Fatalf("the catalog price beats the table: %v", sonnet)
	}
	auto := got["openrouter/auto"]
	if auto["name"] != "Auto Router" || auto["input_per_m"] != nil || auto["output_per_m"] != nil || auto["price_unknown"] != true {
		t.Fatalf("no price anywhere: %v", auto)
	}
	if vague := got["vendor/vague"]; vague["tools"] != nil || vague["context"] != float64(0) || vague["input_per_m"] != float64(1) {
		t.Fatalf("tools unknown stay null: %v", vague)
	}

	m.mustPut(t, `{"prices":{"vendor/cheap":{"input_per_m":9,"output_per_m":8},"openrouter/auto":{"input_per_m":1,"output_per_m":2}}}`)
	got = m.statusModels(t)
	if cheap := got["vendor/cheap"]; cheap["input_per_m"] != float64(9) || cheap["output_per_m"] != float64(8) || cheap["price_unknown"] != false {
		t.Fatalf("the administrator price wins: %v", cheap)
	}
	if auto = got["openrouter/auto"]; auto["price_unknown"] != false || auto["input_per_m"] != float64(1) || auto["name"] != "Auto Router" {
		t.Fatalf("an administrator price removes price_unknown: %v", auto)
	}

	calls := m.stub.calls()
	m.dropCatalog(aiProviderOpenRouter)
	got = m.statusModels(t)
	sonnet = got["anthropic/claude-sonnet-4"]
	if sonnet["name"] != "" || sonnet["context"] != float64(0) || sonnet["tools"] != nil || sonnet["input_per_m"] != float64(3) || sonnet["price_unknown"] != false {
		t.Fatalf("without a catalog only the table price is left: %v", sonnet)
	}
	if vague := got["vendor/vague"]; vague["input_per_m"] != nil || vague["output_per_m"] != nil || vague["price_unknown"] != true {
		t.Fatalf("no data: %v", vague)
	}
	if m.stub.calls() != calls {
		t.Fatal("the status makes no network call")
	}
}

func TestAIStatusModelInfoEmpty(t *testing.T) {
	e := newAITestEnv(t, nil)
	for _, sess := range []security.Session{{}, {API: true}} {
		_, raw := e.call(e.s.handleAIStatus, http.MethodGet, "", sess)
		var v struct {
			ModelInfo *[]map[string]any `json:"model_info"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil || v.ModelInfo == nil || len(*v.ModelInfo) != 0 {
			t.Fatalf("model_info is an empty list: %s", raw)
		}
	}
}
