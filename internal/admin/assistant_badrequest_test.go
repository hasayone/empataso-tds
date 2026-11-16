package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wantBadRequest fails the test unless code and body are the 400 ai_bad_request answer with
// exactly the params field and reason (and provider for reason wrong_provider), and an error
// text that starts with the code and holds text.
func wantBadRequest(t *testing.T, label string, code int, body, field, reason, text string) {
	t.Helper()
	var v struct {
		Error  string         `json:"error"`
		Code   string         `json:"code"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%s: not an error body: %s", label, body)
	}
	params := 2
	if reason == aiReasonWrongProvider {
		params = 3
	}
	if code != http.StatusBadRequest || v.Code != aiErrBadRequest || v.Params["field"] != field || v.Params["reason"] != reason ||
		len(v.Params) != params || v.Error != aiErrBadRequest+": "+text {
		t.Fatalf("%s: %d %s, want ai_bad_request %s/%s %q", label, code, body, field, reason, text)
	}
}

// aiManyIDs returns a JSON array of n model ids.
func aiManyIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%q", fmt.Sprintf("m%d", i))
	}
	return "[" + strings.Join(ids, ",") + "]"
}

func TestAISettingsPutBadRequest(t *testing.T) {
	e := newAITestEnv(t, nil)
	manyPrices := make([]string, aiMaxModels+1)
	for i := range manyPrices {
		manyPrices[i] = fmt.Sprintf(`"m%d":{"input_per_m":1,"output_per_m":1}`, i)
	}
	for _, c := range []struct {
		name, body, field, reason, text string
	}{
		{"not json", `{`, "body", "invalid", "bad json"},
		{"provider", `{"provider":"nope"}`, "provider", "unknown_provider", "provider must be anthropic, openai, openrouter or gemini"},
		{"keys provider", `{"keys":{"nope":"k"}}`, "keys", "unknown_provider", "unknown provider in keys"},
		{"key", `{"keys":{"anthropic":"bad key with spaces"}}`, "keys", "invalid", "key is not valid"},
		{"clear_keys provider", `{"clear_keys":["nope"]}`, "clear_keys", "unknown_provider", "unknown provider in clear_keys"},
		{"model id", `{"allowed_models":["bad id"]}`, "allowed_models", "invalid", "allowed_models holds a bad model id"},
		{"too many models", `{"allowed_models":` + aiManyIDs(aiMaxModels+1) + `}`, "allowed_models", "too_many", "too many allowed_models"},
		{"too many prices", `{"prices":{` + strings.Join(manyPrices, ",") + `}}`, "prices", "too_many", "too many prices"},
		{"price id", `{"prices":{"bad id":{"input_per_m":1,"output_per_m":1}}}`, "prices", "bad_price", "prices hold a bad model id or price"},
		{"price value", `{"prices":{"m":{"input_per_m":-1,"output_per_m":1}}}`, "prices", "bad_price", "prices hold a bad model id or price"},
	} {
		code, body := e.put(c.body)
		wantBadRequest(t, c.name, code, body, c.field, c.reason, c.text)
	}
}

func TestAISettingsEnabledNeedsModels(t *testing.T) {
	const text = "allowed_models is required while the assistant is enabled"
	e := newAITestEnv(t, nil)
	e.seedCatalog(aiProviderAnthropic, "a", "b")
	for _, body := range []string{`{"enabled":true}`, `{"enabled":true,"allowed_models":[]}`} {
		code, raw := e.put(body)
		wantBadRequest(t, body, code, raw, "allowed_models", "required", text)
	}
	if v := e.view(t); v.Enabled {
		t.Fatalf("a refused save changed the settings: %+v", v)
	}
	if v := e.mustPut(t, `{"enabled":false,"daily_usd":3}`); v.Enabled || len(v.AllowedModels) != 0 {
		t.Fatalf("a disabled assistant may keep an empty list: %+v", v)
	}
	e.mustPut(t, `{"enabled":true,"allowed_models":["a","b"],"model":"a","keys":{"anthropic":"`+aiCanary+`"}}`)
	code, raw := e.put(`{"allowed_models":[],"model":""}`)
	wantBadRequest(t, "emptying the list of an enabled assistant", code, raw, "allowed_models", "required", text)
	if v := e.mustPut(t, `{"enabled":false,"allowed_models":[],"model":""}`); v.Enabled || len(v.AllowedModels) != 0 {
		t.Fatalf("switching off and emptying in one save: %+v", v)
	}
}

func TestAISettingsEnabledNeedsKey(t *testing.T) {
	const text = "a key of the provider is required while the assistant is enabled"
	for name, tune := range map[string]func(*Config){
		"normal":    nil,
		"test mode": func(c *Config) { c.AITestBaseURL = "http://127.0.0.1:9999/v1" },
	} {
		e := newAITestEnv(t, tune)
		e.seedCatalog(aiProviderAnthropic, "a")
		const enable = `{"enabled":true,"allowed_models":["a"],"model":"a"`
		code, raw := e.put(enable + `}`)
		wantBadRequest(t, name+": no key", code, raw, "keys", "required", text)
		code, raw = e.put(enable + `,"keys":{"openai":"` + aiCanary2 + `"}}`)
		wantBadRequest(t, name+": the key of another provider", code, raw, "keys", "required", text)
		if v := e.view(t); v.Enabled || v.Keys["openai"].HasKey {
			t.Fatalf("%s: a refused save changed the settings: %+v", name, v)
		}
		if v := e.mustPut(t, `{"enabled":false,"keys":{"openai":"`+aiCanary2+`"}}`); v.Enabled {
			t.Fatalf("%s: a disabled assistant needs no key of its provider: %+v", name, v)
		}
		if v := e.mustPut(t, enable+`,"keys":{"anthropic":"`+aiCanary+`"}}`); !v.Enabled || !v.Keys["anthropic"].HasKey {
			t.Fatalf("%s: %+v", name, v)
		}
		code, raw = e.put(`{"clear_keys":["anthropic"]}`)
		wantBadRequest(t, name+": clearing the key of an enabled assistant", code, raw, "keys", "required", text)
		code, raw = e.put(`{"provider":"gemini"}`)
		wantBadRequest(t, name+": switching to a provider without a key", code, raw, "keys", "required", text)
		if v := e.mustPut(t, `{"enabled":false,"clear_keys":["anthropic"]}`); v.Enabled || v.Keys["anthropic"].HasKey {
			t.Fatalf("%s: switching off and clearing in one save: %+v", name, v)
		}
	}
}

func TestAIKeyOfAnotherProvider(t *testing.T) {
	for key, want := range map[string]string{
		"sk-ant-api03-x": aiProviderAnthropic, "sk-or-v1-x": aiProviderOpenRouter, "AIzaSyX": aiProviderGemini,
		"sk-proj-x": aiProviderOpenAI, "sk-x": aiProviderOpenAI, "plain-key": "",
	} {
		if got := aiKeyProvider(key); got != want {
			t.Errorf("aiKeyProvider(%q) = %q, want %q", key, got, want)
		}
	}
	e := newAITestEnv(t, nil)
	for prov, key := range map[string]string{
		aiProviderOpenAI: "sk-ant-api03-abcdefghij", aiProviderAnthropic: "sk-or-v1-abcdefghij",
		aiProviderOpenRouter: "AIzaSyabcdefghij", aiProviderGemini: "sk-proj-abcdefghij",
	} {
		owner := aiKeyProvider(key)
		code, raw := e.put(`{"keys":{"` + prov + `":"` + key + `"}}`)
		wantBadRequest(t, prov, code, raw, "keys", "wrong_provider", "the key belongs to provider "+owner)
		if !strings.Contains(raw, `"provider":"`+owner+`"`) || strings.Contains(raw, key) {
			t.Fatalf("%s: the answer names the owner and never the key: %s", prov, raw)
		}
		code, raw = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"`+prov+`","key":"`+key+`"}`)
		wantBadRequest(t, prov+" test", code, raw, "key", "wrong_provider", "the key belongs to provider "+owner)
	}
	if v := e.view(t); len(v.Keys) != 0 && (v.Keys["openai"].HasKey || v.Keys["anthropic"].HasKey) {
		t.Fatalf("a refused key was stored: %+v", v.Keys)
	}
	v := e.mustPut(t, `{"keys":{"anthropic":"  sk-ant-api03-abcdefghij\n"}}`)
	if !v.Keys["anthropic"].HasKey {
		t.Fatalf("a key of the right provider with spaces around is stored: %+v", v.Keys)
	}
	st, _ := e.s.aiLoad()
	if st.Keys["anthropic"] != "sk-ant-api03-abcdefghij" {
		t.Fatalf("the stored key is trimmed, got length %d", len(st.Keys["anthropic"]))
	}
}

func TestAIOtherHandlersBadRequest(t *testing.T) {
	e := newAITestEnv(t, nil)
	code, body := e.call(e.s.handleAITest, http.MethodPost, `{`)
	wantBadRequest(t, "test not json", code, body, "body", "invalid", "bad json")
	code, body = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"nope"}`)
	wantBadRequest(t, "test provider", code, body, "provider", "unknown_provider", "provider must be anthropic, openai, openrouter or gemini")
	code, body = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"openai","key":"two words"}`)
	wantBadRequest(t, "test key", code, body, "key", "invalid", "key is not valid")

	req := httptest.NewRequest(http.MethodGet, "/api/ai/models?provider=nope", nil)
	rec := httptest.NewRecorder()
	e.s.handleAIModels(rec, req)
	wantBadRequest(t, "models provider", rec.Code, rec.Body.String(), "provider", "unknown_provider", "provider must be anthropic, openai, openrouter or gemini")

	code, body = e.call(e.s.handleAIMessageRevert, http.MethodPost, `{`)
	wantBadRequest(t, "revert not json", code, body, "body", "invalid", "bad json")
	code, body = e.call(e.s.handleAIMessageRevert, http.MethodPost, `{}`)
	wantBadRequest(t, "revert chat_id", code, body, "chat_id", "required", "chat_id required")
}

func TestAIBadRequestTexts(t *testing.T) {
	for _, lang := range []string{"ru", "uk"} {
		if aiLangTexts[lang][aiErrBadRequest] == "" {
			t.Errorf("%s has no text for %s", lang, aiErrBadRequest)
		}
	}
	e := newAITestEnv(t, nil)
	rec := httptest.NewRecorder()
	e.s.aiWriteErrIn(rec, aiBadRequest("text", aiReasonTooLong, "text is too long"), "uk")
	var v struct {
		Error  string         `json:"error"`
		Code   string         `json:"code"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest || v.Code != aiErrBadRequest || v.Error != aiLangTexts["uk"][aiErrBadRequest] ||
		v.Params["field"] != "text" || v.Params["reason"] != "too_long" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	e.s.aiWriteErrIn(rec, aiBadRequest("text", aiReasonRequired, "text is required"), "en")
	if !strings.Contains(rec.Body.String(), `"error":"ai_bad_request: text is required"`) {
		t.Fatalf("English keeps the text of the error: %s", rec.Body.String())
	}
}
