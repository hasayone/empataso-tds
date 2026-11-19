package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

const (
	aiCanary  = "sk-ant-CANARY-0123456789abcdefWXYZ"
	aiCanary2 = "CANARY2-9876543210zyxwvuTSRQ-key"
)

// aiTestLog is a logger that keeps everything written to it.
type aiTestLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *aiTestLog) add(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(&l.b, msg, fmt.Sprint(args...))
}

// Warn records a warning.
func (l *aiTestLog) Warn(msg string, args ...any) { l.add(msg, args...) }

// Info records an info line.
func (l *aiTestLog) Info(msg string, args ...any) { l.add(msg, args...) }

// text returns everything logged so far.
func (l *aiTestLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// aiTestEnv is a server with the assistant files in a temporary directory.
type aiTestEnv struct {
	s   *Server
	dir string
	log *aiTestLog
}

// newAITestEnv starts a server whose ai.json and assistant directory live in a temporary
// directory. tune may change the Config before the server starts and may be nil.
func newAITestEnv(t *testing.T, tune func(*Config)) *aiTestEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	lg := &aiTestLog{}
	cfg := Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(), Limiter: allowAll{},
		UsersFile: filepath.Join(dir, "users.json"),
		AIFile:    filepath.Join(dir, "ai.json"), AIDir: filepath.Join(dir, "assistant"),
		Log: lg,
	}
	if tune != nil {
		tune(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &aiTestEnv{s: s, dir: dir, log: lg}
}

// call runs a handler with a request body and a session (the zero session is the
// environment administrator) and returns the status and the body.
func (e *aiTestEnv) call(h http.HandlerFunc, method, body string, sess ...security.Session) (int, string) {
	req := httptest.NewRequest(method, "/api/ai/x", strings.NewReader(body))
	if len(sess) > 0 {
		req = req.WithContext(context.WithValue(req.Context(), sessionKey, sess[0]))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, rec.Body.String()
}

// put sends PUT /api/ai/settings.
func (e *aiTestEnv) put(body string) (int, string) {
	return e.call(e.s.handleAISettingsPut, http.MethodPut, body)
}

// view fetches GET /api/ai/settings and decodes it.
func (e *aiTestEnv) view(t *testing.T) aiSettingsView {
	t.Helper()
	code, body := e.call(e.s.handleAISettingsGet, http.MethodGet, "")
	if code != http.StatusOK {
		t.Fatalf("GET %d %s", code, body)
	}
	var v aiSettingsView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// mustPut sends a PUT and fails the test unless it answers 200.
func (e *aiTestEnv) mustPut(t *testing.T, body string) aiSettingsView {
	t.Helper()
	code, raw := e.put(body)
	if code != http.StatusOK {
		t.Fatalf("PUT %s -> %d %s", body, code, raw)
	}
	var v aiSettingsView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// aiBodyCode returns the "code" field of an error body.
func aiBodyCode(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("not an error body: %s", body)
	}
	return e.Code
}

// aiTestPtr returns a pointer to v.
func aiTestPtr[T any](v T) *T { return &v }

// aiTestVal returns *p, or -1 for nil, so a missing limit fails a comparison with a real value.
func aiTestVal[T int64 | float64](p *T) T {
	if p == nil {
		return -1
	}
	return *p
}

// aiTestOff returns the stored value of a switched-off daily limit.
func aiTestOff[T int64 | float64]() *T { return aiTestPtr(T(aiNoLimit)) }

// seedCatalog stores a fresh catalog of provider that lists ids, as a fetch would, so a PUT can name them.
func (e *aiTestEnv) seedCatalog(provider string, ids ...string) {
	e.seedModels(provider, nil, ids...)
}

// seedModels is seedCatalog with extra models that carry their own data.
func (e *aiTestEnv) seedModels(provider string, extra []aiModelInfo, ids ...string) {
	models := append([]aiModelInfo(nil), extra...)
	for _, id := range ids {
		models = append(models, aiModelInfo{ID: id})
	}
	e.s.aiCatalogPut(&aiCatalog{Provider: provider, FetchedAt: e.s.aiModelsNow().UTC(), Models: models})
}

// aiOnPosix skips a permission check on systems without Unix modes.
func aiOnPosix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
}

func TestAISettingsKeyMask(t *testing.T) {
	e := newAITestEnv(t, nil)
	v := e.mustPut(t, `{"provider":"anthropic","keys":{"anthropic":"`+aiCanary+`"}}`)
	wantMask := aiKeyMaskPrefix + aiCanary[len(aiCanary)-4:]
	if k := v.Keys["anthropic"]; !k.HasKey || k.Mask != wantMask {
		t.Fatalf("key view %+v, want mask %s", k, wantMask)
	}
	if k := v.Keys["openai"]; k.HasKey || k.Mask != "" {
		t.Fatalf("openai has no key: %+v", k)
	}
	if _, body := e.call(e.s.handleAISettingsGet, http.MethodGet, ""); strings.Contains(body, aiCanary) {
		t.Fatal("GET leaks the key")
	}
	stored := func() string {
		st, err := e.s.aiLoad()
		if err != nil {
			t.Fatal(err)
		}
		return st.Keys["anthropic"]
	}
	if stored() != aiCanary {
		t.Fatal("the key was not stored")
	}
	for _, body := range []string{
		`{"keys":{"anthropic":""}}`,
		`{"keys":{"anthropic":"   "}}`,
		`{"keys":{"anthropic":"` + wantMask + `"}}`,
		`{"keys":{"anthropic":"••••••••"}}`,
		`{"keys":{"anthropic":"****"}}`,
		`{"enabled":false}`,
	} {
		e.mustPut(t, body)
		if stored() != aiCanary {
			t.Fatalf("%s replaced the stored key", body)
		}
	}
	v = e.mustPut(t, `{"keys":{"openai":"`+aiCanary2+`"}}`)
	if !v.Keys["openai"].HasKey || !v.Keys["anthropic"].HasKey {
		t.Fatalf("both keys expected: %+v", v.Keys)
	}
	v = e.mustPut(t, `{"clear_keys":["anthropic"]}`)
	if v.Keys["anthropic"].HasKey || !v.Keys["openai"].HasKey {
		t.Fatalf("clear_keys removed the wrong key: %+v", v.Keys)
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, "ai.json"))
	if err != nil || strings.Contains(string(raw), aiCanary+`"`) || !strings.Contains(string(raw), aiCanary2) {
		t.Fatalf("file content after clear_keys: %s %v", raw, err)
	}
}

func TestAISettingsKeyNeverLeaks(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		key := r.Header.Get("x-api-key")
		if key == aiCanary2 {
			aiJSONReply(w, 400, `{"error":{"message":"rejected `+key+`"}}`)
			return
		}
		aiJSONReply(w, 200, `{"data":[{"id":"claude-sonnet-4"},{"id":"claude-haiku-4"}]}`)
	})
	e := newAITestEnv(t, func(c *Config) { c.AIBaseURLs = map[string]string{aiProviderAnthropic: stub.srv.URL} })
	var bodies []string
	e.seedCatalog(aiProviderAnthropic, "claude-sonnet-4", "claude-haiku-4")
	_, b := e.put(`{"enabled":true,"provider":"anthropic","keys":{"anthropic":"` + aiCanary + `"},"allowed_models":["claude-sonnet-4"],"model":"claude-sonnet-4"}`)
	bodies = append(bodies, b)
	_, b = e.call(e.s.handleAISettingsGet, http.MethodGet, "")
	bodies = append(bodies, b)
	code, b := e.call(e.s.handleAITest, http.MethodPost, `{"provider":"anthropic","key":""}`)
	if code != http.StatusOK || !strings.Contains(b, "claude-sonnet-4") {
		t.Fatalf("test with the stored key: %d %s", code, b)
	}
	bodies = append(bodies, b)
	code, b = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"anthropic","key":"`+aiCanary2+`"}`)
	if code != http.StatusBadGateway || aiBodyCode(t, b) != aiErrProvider {
		t.Fatalf("echoing provider: %d %s", code, b)
	}
	bodies = append(bodies, b)
	_, b = e.call(e.s.handleAIStatus, http.MethodGet, "", security.Session{})
	bodies = append(bodies, b)
	_, b = e.put(`{"keys":{"anthropic":"bad key with spaces"}}`)
	bodies = append(bodies, b)
	_, b = e.put(`{"keys":{"nope":"` + aiCanary2 + `"}}`)
	bodies = append(bodies, b)
	for i, body := range bodies {
		for _, secret := range []string{aiCanary, aiCanary2} {
			if strings.Contains(body, secret) {
				t.Errorf("answer %d leaks a key: %s", i, body)
			}
		}
	}
	if logged := e.log.text(); strings.Contains(logged, aiCanary) || strings.Contains(logged, aiCanary2) {
		t.Fatalf("the log holds a key: %s", logged)
	}
}

func TestAISettingsFileMode(t *testing.T) {
	aiOnPosix(t)
	e := newAITestEnv(t, nil)
	e.mustPut(t, `{"keys":{"anthropic":"`+aiCanary+`"}}`)
	info, err := os.Stat(filepath.Join(e.dir, "ai.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ai.json mode %v %v", info, err)
	}
	e.mustPut(t, `{"enabled":false}`)
	if info, _ = os.Stat(filepath.Join(e.dir, "ai.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after a second save %v", info.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(e.dir, ".ai.json.tmp*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestAISettingsModelMustBeAllowed(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.seedCatalog(aiProviderAnthropic, "a", "b", "c")
	code, body := e.put(`{"allowed_models":["a","b"],"model":"c"}`)
	if code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadModel {
		t.Fatalf("got %d %s", code, body)
	}
	if code, body = e.put(`{"model":"a"}`); code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadModel {
		t.Fatalf("a model with no allowed list: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "ai.json")); err == nil {
		t.Fatal("a refused PUT wrote the file")
	}
	v := e.mustPut(t, `{"allowed_models":["a","b","a"],"model":"b"}`)
	if v.Model != "b" || strings.Join(v.AllowedModels, ",") != "a,b" {
		t.Fatalf("view %+v", v)
	}
	if code, body = e.put(`{"allowed_models":["a"]}`); code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadModel {
		t.Fatalf("dropping the default model from the list: %d %s", code, body)
	}
	if v = e.view(t); v.Model != "b" || len(v.AllowedModels) != 2 {
		t.Fatalf("a refused PUT changed the settings: %+v", v)
	}
}

func TestAISettingsLimits(t *testing.T) {
	e := newAITestEnv(t, nil)
	v := e.view(t)
	if v.MaxOutputTokens != 2048 || v.MaxSteps != 30 || v.TurnInputTokens != 60000 || v.DailyTokens != nil ||
		v.UserDailyTokens != nil || aiTestVal(v.DailyUSD) != 5 || aiTestVal(v.UserDailyUSD) != 2 {
		t.Fatalf("defaults: tokens have no limit, USD keeps its default: %+v", v)
	}
	if !v.Beta || v.Enabled || v.Provider != aiProviderAnthropic {
		t.Fatalf("defaults: %+v", v)
	}
	v = e.mustPut(t, `{"daily_usd":null,"user_daily_usd":0,"max_steps":3}`)
	if v.DailyUSD != nil || aiTestVal(v.UserDailyUSD) != 0 || v.UserDailyUSD == nil || v.MaxSteps != 3 || v.UserDailyTokens != nil {
		t.Fatalf("null switches off, zero is a limit: %+v", v)
	}
	raw, _ := os.ReadFile(filepath.Join(e.dir, "ai.json"))
	if !strings.Contains(string(raw), `"daily_usd":-1`) || !strings.Contains(string(raw), `"user_daily_usd":0`) {
		t.Fatalf("no limit and zero must be stored: %s", raw)
	}
	if strings.Contains(string(raw), "user_daily_tokens") || strings.Contains(string(raw), `"daily_tokens"`) {
		t.Fatalf("an unset limit must stay unset: %s", raw)
	}
	v = e.mustPut(t, `{"enabled":false}`)
	if v.DailyUSD != nil || v.UserDailyUSD == nil || *v.UserDailyUSD != 0 {
		t.Fatalf("another PUT lost the stored limits: %+v", v)
	}
	v = e.mustPut(t, `{"daily_tokens":5000,"daily_usd":1.5}`)
	if aiTestVal(v.DailyTokens) != 5000 || aiTestVal(v.DailyUSD) != 1.5 || v.MaxSteps != 3 {
		t.Fatalf("new values: %+v", v)
	}
	v = e.mustPut(t, `{"daily_tokens":null}`)
	if v.DailyTokens != nil {
		t.Fatalf("null takes a set token limit off: %+v", v)
	}
	for _, body := range []string{
		`{"daily_tokens":-1}`, `{"user_daily_tokens":-5}`, `{"daily_usd":-0.1}`, `{"user_daily_usd":-1}`,
		`{"max_output_tokens":-1}`, `{"max_steps":9999}`, `{"turn_input_tokens":-3}`, `{"daily_tokens":1.5}`,
		`{"daily_tokens":"many"}`, `{"daily_usd":1e300}`,
	} {
		if code, _ := e.put(body); code != http.StatusBadRequest {
			t.Errorf("%s -> %d, want 400", body, code)
		}
	}
}

func TestAISettingsPrices(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.seedCatalog(aiProviderAnthropic, "claude-sonnet-4-20250514", "mystery-model", "anthropic/claude-3.5-haiku")
	v := e.mustPut(t, `{"allowed_models":["claude-sonnet-4-20250514","mystery-model","anthropic/claude-3.5-haiku"],"model":"claude-sonnet-4-20250514"}`)
	if p := v.Prices["claude-sonnet-4-20250514"]; p.InputPerM != 3 || p.OutputPerM != 15 {
		t.Fatalf("default price: %+v", v.Prices)
	}
	if p := v.DefaultPrices["anthropic/claude-3.5-haiku"]; p.InputPerM != 0.8 || p.OutputPerM != 4 {
		t.Fatalf("vendor prefix: %+v", v.DefaultPrices)
	}
	if len(v.PriceUnknown) != 1 || v.PriceUnknown[0] != "mystery-model" {
		t.Fatalf("price_unknown %v", v.PriceUnknown)
	}
	v = e.mustPut(t, `{"prices":{"mystery-model":{"input_per_m":1,"output_per_m":2},"claude-sonnet-4-20250514":{"input_per_m":4,"output_per_m":20}}}`)
	if len(v.PriceUnknown) != 0 {
		t.Fatalf("price_unknown %v", v.PriceUnknown)
	}
	if p := v.Prices["claude-sonnet-4-20250514"]; p.InputPerM != 4 || p.OutputPerM != 20 {
		t.Fatalf("the administrator price must win: %+v", p)
	}
	if p := v.DefaultPrices["claude-sonnet-4-20250514"]; p.InputPerM != 3 {
		t.Fatalf("the default stays visible: %+v", v.DefaultPrices)
	}
	if _, ok := v.DefaultPrices["mystery-model"]; ok {
		t.Fatal("an unknown model has no default price")
	}
	v = e.mustPut(t, `{"prices":{}}`)
	if len(v.PriceUnknown) != 1 {
		t.Fatalf("an empty map clears the overrides: %v", v.PriceUnknown)
	}
	for _, body := range []string{
		`{"prices":{"m":{"input_per_m":-1,"output_per_m":1}}}`,
		`{"prices":{"m":{"input_per_m":1,"output_per_m":-1}}}`,
		`{"prices":{"bad id":{"input_per_m":1,"output_per_m":1}}}`,
		`{"prices":{"m":{"input_per_m":1e9,"output_per_m":1}}}`,
	} {
		if code, _ := e.put(body); code != http.StatusBadRequest {
			t.Errorf("%s -> %d, want 400", body, code)
		}
	}
}

func TestAISettingsPriceLookup(t *testing.T) {
	cases := []struct {
		model   string
		in, out float64
		known   bool
	}{
		{"claude-opus-4-1-20250805", 15, 75, true},
		{"claude-sonnet-4", 3, 15, true},
		{"anthropic/claude-3.7-sonnet", 3, 15, true},
		{"anthropic/claude-3.7-sonnet:thinking", 0, 0, false},
		{"claude-sonnet-4-20250514", 3, 15, true},
		{"claude-sonnet-4-latest", 3, 15, true},
		{"claude-opus-4-5", 0, 0, false},
		{"o3-pro", 0, 0, false},
		{"o3-mini-high", 0, 0, false},
		{"o3-2025-04-16", 2, 8, true},
		{"gpt-4o-search-preview", 0, 0, false},
		{"gpt-4o-audio-preview", 0, 0, false},
		{"gpt-4o-mini-tts", 0, 0, false},
		{"gpt-4o-2024-11-20", 2.5, 10, true},
		{"gpt-4.1-mini-2025-04-14", 0.4, 1.6, true},
		{"gemini-2.5-pro-preview-05-06", 1.25, 10, true},
		{"gemini-2.5-flash-lite-preview-06-17", 0.1, 0.4, true},
		{"gemini-2.5-flash-image", 0, 0, false},
		{"gemini-2.0-flash-lite", 0, 0, false},
		{"claude-3-5-haiku-20241022", 0.8, 4, true},
		{"claude-haiku-4-5", 1, 5, true},
		{"gpt-4o", 2.5, 10, true},
		{"gpt-4o-2024-08-06", 2.5, 10, true},
		{"gpt-4o-mini-2024-07-18", 0.15, 0.6, true},
		{"openai/gpt-4.1-nano", 0.1, 0.4, true},
		{"gpt-4.1-mini", 0.4, 1.6, true},
		{"gpt-4.1", 2, 8, true},
		{"o3", 2, 8, true},
		{"o3-mini", 1.1, 4.4, true},
		{"o4-mini", 1.1, 4.4, true},
		{"models/gemini-2.5-pro", 1.25, 10, true},
		{"google/gemini-2.5-flash", 0.3, 2.5, true},
		{"gemini-2.5-flash-lite", 0.1, 0.4, true},
		{"gemini-2.0-flash-001", 0.1, 0.4, true},
		{"gpt-4", 0, 0, false},
		{"gpt-4ox", 0, 0, false},
		{"mystery", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tc := range cases {
		p, ok := aiPriceTable(tc.model)
		if ok != tc.known || p.InputPerM != tc.in || p.OutputPerM != tc.out {
			t.Errorf("%q -> %+v %v, want %v/%v %v", tc.model, p, ok, tc.in, tc.out, tc.known)
		}
	}
	over := map[string]aiPrice{"gpt-4o": {InputPerM: 9, OutputPerM: 9}}
	if p, ok := aiPriceOverride("gpt-4o", over); !ok || p.InputPerM != 9 {
		t.Fatalf("override: %+v", p)
	}
	if p, ok := aiPriceOverride("gpt-4o-mini", over); ok {
		t.Fatalf("an override must not capture a longer id: %+v", p)
	}
}

func TestAISettingsBadInput(t *testing.T) {
	e := newAITestEnv(t, nil)
	for _, body := range []string{
		`{`, `[]`, `{"provider":"bogus"}`, `{"provider":""}`, `{"keys":{"bogus":"abcdefghij1234"}}`,
		`{"keys":{"openai":"has space inside"}}`, `{"keys":{"openai":"` + strings.Repeat("k", 600) + `"}}`,
		`{"clear_keys":["bogus"]}`, `{"allowed_models":["bad model"]}`, `{"allowed_models":[""]}`,
	} {
		if code, _ := e.put(body); code != http.StatusBadRequest {
			t.Errorf("%s -> %d, want 400", body, code)
		}
	}
	none := newAITestEnv(t, func(c *Config) { c.AIFile = "" })
	if code, _ := none.put(`{"enabled":true}`); code != http.StatusServiceUnavailable {
		t.Fatalf("no file configured: %d", code)
	}
	if v := none.view(t); v.Enabled || v.Provider != aiProviderAnthropic {
		t.Fatalf("defaults without a file: %+v", v)
	}
}

func TestAISettingsDamagedFile(t *testing.T) {
	e := newAITestEnv(t, nil)
	if err := os.WriteFile(filepath.Join(e.dir, "ai.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := e.view(t)
	if v.Error == "" || v.Enabled || v.MaxSteps != 30 {
		t.Fatalf("damaged file view: %+v", v)
	}
	if _, err := e.s.aiLoad(); err == nil {
		t.Fatal("aiLoad must report the damage")
	}
	e.seedCatalog(aiProviderAnthropic, "m")
	v = e.mustPut(t, `{"enabled":true,"allowed_models":["m"],"keys":{"anthropic":"`+aiCanary+`"}}`)
	if v.Error != "" || !v.Enabled {
		t.Fatalf("a PUT repairs the file: %+v", v)
	}
	if _, err := e.s.aiLoad(); err != nil {
		t.Fatal(err)
	}
}

func TestAISettingsTestEndpoint(t *testing.T) {
	var status atomicStatus
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if s := status.get(); s != 200 {
			w.Header().Set("Retry-After", "0")
			aiJSONReply(w, s, `{"error":{"message":"nope"}}`)
			return
		}
		aiJSONReply(w, 200, `{"data":[{"id":"m2"},{"id":"m1"}]}`)
	})
	status.set(200)
	e := newAITestEnv(t, func(c *Config) {
		c.AIBaseURLs = map[string]string{aiProviderAnthropic: stub.srv.URL, aiProviderOpenAI: stub.srv.URL}
	})
	code, body := e.call(e.s.handleAITest, http.MethodPost, `{"provider":"anthropic"}`)
	if code != http.StatusBadRequest || aiBodyCode(t, body) != aiErrBadKey {
		t.Fatalf("no key anywhere: %d %s", code, body)
	}
	if stub.calls() != 0 {
		t.Fatal("no key must mean no provider call")
	}
	code, body = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"anthropic","key":"typed-key-0123456789"}`)
	var out struct {
		Models []aiModelInfo `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || code != 200 || strings.Join(aiIDs(out.Models), ",") != "m1,m2" {
		t.Fatalf("typed key: %d %s", code, body)
	}
	if stub.req(0).Header.Get("x-api-key") != "typed-key-0123456789" {
		t.Fatalf("the typed key was not used: %v", stub.req(0).Header)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "ai.json")); err == nil {
		t.Fatal("the test saved a file")
	}
	e.mustPut(t, `{"keys":{"anthropic":"`+aiCanary+`"}}`)
	for _, key := range []string{`""`, `"` + aiKeyMaskPrefix + `WXYZ"`} {
		if code, body = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"anthropic","key":`+key+`}`); code != 200 {
			t.Fatalf("stored key %s: %d %s", key, code, body)
		}
		if got := stub.req(stub.calls() - 1).Header.Get("x-api-key"); got != aiCanary {
			t.Fatalf("the stored key was not used for %s", key)
		}
	}
	if code, body = e.call(e.s.handleAITest, http.MethodPost, `{"key":"x y"}`); code != http.StatusBadRequest {
		t.Fatalf("bad key: %d %s", code, body)
	}
	if code, _ = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"bogus","key":"abcdefghij"}`); code != http.StatusBadRequest {
		t.Fatalf("bad provider: %d", code)
	}
	for _, tc := range []struct {
		upstream, status int
		code             string
	}{
		{401, 400, aiErrBadKey}, {403, 400, aiErrBadKey}, {429, 429, aiErrRateLimited}, {500, 502, aiErrProvider}, {404, 502, aiErrProvider},
	} {
		status.set(tc.upstream)
		if code, body = e.call(e.s.handleAITest, http.MethodPost, `{"provider":"openai","key":"typed-key-0123456789"}`); code != tc.status || aiBodyCode(t, body) != tc.code {
			t.Errorf("upstream %d -> %d %s, want %d %s", tc.upstream, code, body, tc.status, tc.code)
		}
	}
}

// atomicStatus is an HTTP status the stub reads and the test changes.
type atomicStatus struct {
	mu sync.Mutex
	v  int
}

func (a *atomicStatus) get() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.v
}

func (a *atomicStatus) set(v int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.v = v
}

func TestAISettingsStatus(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.seedCatalog(aiProviderOpenAI, "gpt-4o", "mystery")
	e.mustPut(t, `{"enabled":true,"provider":"openai","keys":{"openai":"`+aiCanary2+`"},"allowed_models":["gpt-4o","mystery"],"model":"mystery","user_daily_tokens":1000,"daily_usd":0}`)
	type statusBody struct {
		Enabled, Allowed, Beta bool
		Provider, Model        string
		Models                 []string
		PriceUnknown           bool `json:"price_unknown"`
		Usage                  *struct {
			Tokens          int64
			USD             float64
			LimitTokens     *int64   `json:"limit_tokens"`
			LimitUSD        *float64 `json:"limit_usd"`
			TeamTokens      int64    `json:"team_tokens"`
			TeamLimitTokens *int64   `json:"team_limit_tokens"`
			TeamUSD         float64  `json:"team_usd"`
			TeamLimitUSD    *float64 `json:"team_limit_usd"`
		}
	}
	get := func(sess security.Session) (statusBody, string) {
		code, raw := e.call(e.s.handleAIStatus, http.MethodGet, "", sess)
		if code != 200 {
			t.Fatalf("status %d %s", code, raw)
		}
		var b statusBody
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			t.Fatal(err)
		}
		return b, raw
	}
	b, _ := get(security.Session{})
	if !b.Enabled || !b.Allowed || !b.Beta || b.Provider != "openai" || b.Model != "mystery" || len(b.Models) != 2 || !b.PriceUnknown {
		t.Fatalf("admin status %+v", b)
	}
	if b.Usage == nil || aiTestVal(b.Usage.LimitTokens) != 1000 || b.Usage.TeamLimitTokens != nil || aiTestVal(b.Usage.TeamLimitUSD) != 0 || aiTestVal(b.Usage.LimitUSD) != 2 {
		t.Fatalf("limits %+v", b.Usage)
	}
	r, err := e.s.aiReserve("env-admin", "gpt-4o", aiNormalize(aiSettings{TurnInputTokens: 100, MaxOutputTokens: 100}))
	if err != nil {
		t.Fatal(err)
	}
	e.s.aiCommit(r, 300, 100)
	if b, _ = get(security.Session{}); b.Usage.Tokens != 400 || b.Usage.TeamTokens != 400 || math.Abs(b.Usage.USD-0.00175) > 1e-9 {
		t.Fatalf("usage %+v", b.Usage)
	}
	e.mustPut(t, `{"allowed_models":["gpt-4o","mystery"],"model":"gpt-4o"}`)
	if b, _ = get(security.Session{}); b.PriceUnknown {
		t.Fatal("gpt-4o has a price")
	}

	u, err := e.s.createUser(userDraft{Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b, raw := get(security.Session{UID: u.ID, User: "bob"})
	if !b.Enabled || b.Allowed || !b.Beta || b.Usage != nil || b.Provider != "" || len(b.Models) != 0 {
		t.Fatalf("a non-administrator sees %+v", b)
	}
	if strings.Contains(raw, "gpt-4o") {
		t.Fatalf("a non-administrator got the model list: %s", raw)
	}
	e.mustPut(t, `{"enabled":false}`)
	if b, _ = get(security.Session{}); b.Enabled || !b.Allowed {
		t.Fatalf("disabled status %+v", b)
	}
}

func TestAISettingsUsageToday(t *testing.T) {
	e := newAITestEnv(t, nil)
	u, err := e.s.createUser(userDraft{Login: "bob", Password: "secret1", Type: security.UserAdmin, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	st := aiNormalize(aiSettings{})
	for _, c := range []struct {
		uid     string
		in, out int
	}{{u.ID, 1000, 500}, {"env-admin", 10, 5}, {"gone-user", 1, 1}} {
		r, err := e.s.aiReserve(c.uid, "claude-sonnet-4", st)
		if err != nil {
			t.Fatal(err)
		}
		e.s.aiCommit(r, c.in, c.out)
	}
	v := e.view(t)
	if v.UsageToday.Day != time.Now().UTC().Format("2006-01-02") || v.UsageToday.Tokens != 1517 || len(v.UsageToday.Users) != 3 {
		t.Fatalf("usage %+v", v.UsageToday)
	}
	logins := map[string]string{}
	for _, row := range v.UsageToday.Users {
		logins[row.UID] = row.Login
	}
	if logins[u.ID] != "bob" || logins["env-admin"] != "admin" || logins["gone-user"] != "" {
		t.Fatalf("logins %v", logins)
	}
	if v.UsageToday.Users[0].UID != u.ID || v.UsageToday.USD <= 0 {
		t.Fatalf("order or usd: %+v", v.UsageToday)
	}
	_, raw := e.call(e.s.handleAISettingsGet, http.MethodGet, "")
	var generic struct {
		Usage struct {
			Users []map[string]any `json:"users"`
		} `json:"usage_today"`
	}
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatal(err)
	}
	for _, row := range generic.Usage.Users {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "login,tokens,uid,usd" {
			t.Fatalf("a usage row must hold only these fields: %v", keys)
		}
	}
}

func TestAISettingsProcessSecrets(t *testing.T) {
	// The environment must not matter: only the Config is read.
	t.Setenv("EMTDS_CLICKHOUSE_PASSWORD", "env-only-secret-never-read")
	t.Setenv("EMTDS_CID_KEY_FILE", filepath.Join(t.TempDir(), "env-cid.key"))
	cidFile := filepath.Join(t.TempDir(), "own-cid.key")
	if err := os.WriteFile(cidFile, []byte("cid-file-secret-canary-0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newAITestEnv(t, func(c *Config) {
		c.KeitaroKey = "kt-cfg-secret-key"
		c.CIDKeyFile = cidFile
		c.SecretValues = []string{"ch-pass-secret-1", "short", "", "kt-env-secret-key"}
	})
	user, err := e.s.createUser(userDraft{Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	userHash, _ := e.s.userByID(user.ID)
	e.mustPut(t, `{"keys":{"anthropic":"`+aiCanary+`","openai":"`+aiCanary2+`","gemini":"tiny"}}`)
	e.s.cfMu.Lock()
	e.s.cfAcct = cfAccount{Token: "cf-token-secret-1"}
	e.s.cfMu.Unlock()
	e.s.setNamecheapAccount(ncAccount{APIKey: "nc-api-key-secret"})
	e.s.ktMu.Lock()
	e.s.ktConns = []keitaroConn{{Key: "kt-conn-secret-key"}, {Key: ""}}
	e.s.ktMu.Unlock()
	got := e.s.processSecrets()
	set := map[string]bool{}
	for _, v := range got {
		if len(v) < aiSecretMinLen {
			t.Errorf("a short or empty value in the list: %q", v)
		}
		if set[v] {
			t.Errorf("duplicate %q", v)
		}
		set[v] = true
	}
	for _, want := range []string{
		aiCanary, aiCanary2, "cf-token-secret-1", "nc-api-key-secret", "kt-env-secret-key", "kt-cfg-secret-key",
		"kt-conn-secret-key", "ch-pass-secret-1", e.s.currentHash(),
		"cid-file-secret-canary-0123456789abcdef", userHash.Hash,
	} {
		if want == "" {
			t.Error("a secret under test is empty")
			continue
		}
		if !set[want] {
			t.Errorf("missing %q", want)
		}
	}
	if set["tiny"] || set["short"] || set["env-only-secret-never-read"] {
		t.Fatal("values shorter than 8 must be left out")
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool {
		return len(got[i]) > len(got[j]) || (len(got[i]) == len(got[j]) && got[i] < got[j])
	}) {
		t.Fatalf("the longest secrets must come first: %v", got)
	}
}

// aiUsageSettings returns normalized settings with the four daily limits set.
// A nil pointer keeps the default.
func aiUsageSettings(dailyTok, userTok *int64, dailyUSD, userUSD *float64) aiSettings {
	st := aiNormalize(aiSettings{})
	st.DailyTokens, st.UserDailyTokens, st.DailyUSD, st.UserDailyUSD = dailyTok, userTok, dailyUSD, userUSD
	return st
}

// aiWantQuota fails the test unless err is the 429 ai_quota error.
func aiWantQuota(t *testing.T, err error) {
	t.Helper()
	ae := aiErrOf(t, err)
	if ae.code != aiErrQuota || ae.status != http.StatusTooManyRequests {
		t.Fatalf("want ai_quota 429, got %s %d", ae.code, ae.status)
	}
}

func TestAIUsageReserveCommit(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiUsageSettings(aiTestOff[int64](), aiTestPtr(int64(100000)), aiTestOff[float64](), aiTestOff[float64]())
	r1, err := e.s.aiReserve("u1", "mystery", st)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Tokens != 62048 || r1.Priced || r1.USD != 0 {
		t.Fatalf("reservation %+v", r1)
	}
	_, err = e.s.aiReserve("u1", "mystery", st)
	aiWantQuota(t, err)
	if _, err = e.s.aiReserve("u2", "mystery", st); err != nil {
		t.Fatalf("another user is not limited: %v", err)
	}
	e.s.aiCommit(r1, 1000, 500)
	_, total, users := e.s.aiUsageSnapshot()
	if total.Tokens != 1500 || users["u1"].Tokens != 1500 || users["u2"].Tokens != 0 {
		t.Fatalf("snapshot %+v %+v", total, users)
	}
	if _, err = e.s.aiReserve("u1", "mystery", st); err != nil {
		t.Fatalf("the hold must be released by the commit: %v", err)
	}
	st.UserDailyTokens = aiTestPtr(int64(1600))
	_, err = e.s.aiReserve("u1", "mystery", st)
	aiWantQuota(t, err)
	e.s.aiCommit(aiReservation{UID: "u3", Tokens: 5}, 0, 0)
	if _, total, _ = e.s.aiUsageSnapshot(); total.Tokens != 1500 {
		t.Fatalf("a release must not count: %+v", total)
	}
}

func TestAIUsageTeamTokenLimit(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiUsageSettings(aiTestPtr(int64(100000)), aiTestOff[int64](), aiTestOff[float64](), aiTestOff[float64]())
	if _, err := e.s.aiReserve("a", "mystery", st); err != nil {
		t.Fatal(err)
	}
	_, err := e.s.aiReserve("b", "mystery", st)
	aiWantQuota(t, err)
	if ae := aiErrOf(t, err); !strings.Contains(ae.msg, "team") {
		t.Fatalf("the message must name the team limit: %s", ae.msg)
	}
}

func TestAIUsageUSDLimits(t *testing.T) {
	off := aiTestOff[int64]
	t.Run("team", func(t *testing.T) {
		e := newAITestEnv(t, nil)
		st := aiUsageSettings(off(), off(), aiTestPtr(0.3), aiTestOff[float64]())
		r, err := e.s.aiReserve("a", "claude-sonnet-4", st)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Priced || math.Abs(r.USD-0.21072) > 1e-9 {
			t.Fatalf("reservation %+v", r)
		}
		_, err = e.s.aiReserve("b", "claude-sonnet-4", st)
		aiWantQuota(t, err)
		e.s.aiCommit(r, 1000, 1000)
		_, total, _ := e.s.aiUsageSnapshot()
		if math.Abs(total.USD-0.018) > 1e-9 || total.Tokens != 2000 {
			t.Fatalf("fact %+v", total)
		}
		if _, err = e.s.aiReserve("b", "claude-sonnet-4", st); err != nil {
			t.Fatalf("room again after the commit: %v", err)
		}
	})
	t.Run("user", func(t *testing.T) {
		e := newAITestEnv(t, nil)
		st := aiUsageSettings(off(), off(), aiTestOff[float64](), aiTestPtr(0.3))
		if _, err := e.s.aiReserve("a", "claude-sonnet-4", st); err != nil {
			t.Fatal(err)
		}
		_, err := e.s.aiReserve("a", "claude-sonnet-4", st)
		aiWantQuota(t, err)
		if _, err = e.s.aiReserve("b", "claude-sonnet-4", st); err != nil {
			t.Fatalf("the USD limit of a user is per user: %v", err)
		}
	})
	t.Run("administrator price", func(t *testing.T) {
		e := newAITestEnv(t, nil)
		st := aiUsageSettings(off(), off(), aiTestOff[float64](), aiTestPtr(0.1))
		st.Prices = map[string]aiPrice{"claude-sonnet-4": {InputPerM: 1, OutputPerM: 1}}
		if _, err := e.s.aiReserve("a", "claude-sonnet-4", st); err != nil {
			t.Fatalf("cheaper price fits: %v", err)
		}
	})
}

func TestAIUsageNoLimitNeverRefuses(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiUsageSettings(aiTestOff[int64](), aiTestOff[int64](), aiTestOff[float64](), aiTestOff[float64]())
	for i := 0; i < 200; i++ {
		r, err := e.s.aiReserve("u", "claude-opus-4", st)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		e.s.aiCommit(r, 100_000, 10_000)
	}
	def := aiNormalize(aiSettings{})
	if lim := def.limits(); lim.DailyTokens != nil || lim.UserDailyTokens != nil || aiTestVal(lim.DailyUSD) != 5 || aiTestVal(lim.UserDailyUSD) != 2 {
		t.Fatalf("by default tokens have no limit and USD keeps its default: %+v", lim)
	}
	e2 := newAITestEnv(t, nil)
	for i := 0; i < 50; i++ {
		r, err := e2.s.aiReserve("u", "mystery", def)
		if err != nil {
			t.Fatalf("reserve %d: tokens must not refuse by default: %v", i, err)
		}
		e2.s.aiCommit(r, 100_000, 10_000)
	}
}

func TestAIUsageMoneyLimitRefuses(t *testing.T) {
	off := aiTestOff[int64]
	t.Run("zero dollars", func(t *testing.T) {
		e := newAITestEnv(t, nil)
		st := aiUsageSettings(off(), off(), aiTestOff[float64](), aiTestPtr(0.0))
		_, err := e.s.aiReserve("u", "claude-sonnet-4", st)
		aiWantQuota(t, err)
		if ae := aiErrOf(t, err); !strings.Contains(ae.msg, "USD") {
			t.Fatalf("the refusal must name the money limit: %s", ae.msg)
		}
	})
	t.Run("spent above the limit", func(t *testing.T) {
		e := newAITestEnv(t, nil)
		st := aiUsageSettings(off(), off(), aiTestPtr(0.5), aiTestOff[float64]())
		r, err := e.s.aiReserve("u", "claude-sonnet-4", st)
		if err != nil {
			t.Fatal(err)
		}
		e.s.aiCommit(r, 200_000, 32_000)
		if _, total, _ := e.s.aiUsageSnapshot(); total.USD <= 0.5 {
			t.Fatalf("the fact must pass the limit: %+v", total)
		}
		_, err = e.s.aiReserve("u", "claude-sonnet-4", st)
		aiWantQuota(t, err)
		if ae := aiErrOf(t, err); !strings.Contains(ae.msg, "team USD") {
			t.Fatalf("the refusal must name the team money limit: %s", ae.msg)
		}
	})
}

func TestAIUsageUnknownPriceDoesNotBlockUSD(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiUsageSettings(aiTestOff[int64](), aiTestOff[int64](), aiTestPtr(0.000001), aiTestPtr(0.000001))
	r, err := e.s.aiReserve("u", "mystery-model", st)
	if err != nil {
		t.Fatalf("an unpriced model must not hit the USD limit: %v", err)
	}
	e.s.aiCommit(r, 10, 10)
	_, total, users := e.s.aiUsageSnapshot()
	if total.USD != 0 || total.Tokens != 20 || users["u"].USD != 0 {
		t.Fatalf("snapshot %+v %+v", total, users)
	}
	if _, err = e.s.aiReserve("u", "claude-sonnet-4", st); err == nil {
		t.Fatal("a priced model must hit the tiny USD limit")
	}
}

func TestAIUsageDayRollover(t *testing.T) {
	e := newAITestEnv(t, nil)
	clock := time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC)
	e.s.ai.usage.now = func() time.Time { return clock }
	st := aiUsageSettings(aiTestPtr(int64(100000)), aiTestPtr(int64(100000)), aiTestOff[float64](), aiTestOff[float64]())
	r, err := e.s.aiReserve("u", "mystery", st)
	if err != nil {
		t.Fatal(err)
	}
	e.s.aiCommit(r, 40000, 1000)
	day, total, _ := e.s.aiUsageSnapshot()
	if day != "2026-11-15" || total.Tokens != 41000 {
		t.Fatalf("day one: %s %+v", day, total)
	}
	_, err = e.s.aiReserve("u", "mystery", st)
	aiWantQuota(t, err)
	clock = clock.Add(2 * time.Minute)
	day, total, users := e.s.aiUsageSnapshot()
	if day != "2026-11-16" || total.Tokens != 0 || len(users) != 0 {
		t.Fatalf("day two: %s %+v %v", day, total, users)
	}
	if _, err = e.s.aiReserve("u", "mystery", st); err != nil {
		t.Fatalf("the new day starts with room: %v", err)
	}
	clock = time.Date(2026, 11, 16, 1, 0, 0, 0, time.FixedZone("far", 14*3600))
	if day, _, _ = e.s.aiUsageSnapshot(); day != "2026-11-15" {
		t.Fatalf("days are UTC, got %s", day)
	}
}

func TestAIUsageSurvivesRestart(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiNormalize(aiSettings{})
	r, err := e.s.aiReserve("u1", "claude-sonnet-4", st)
	if err != nil {
		t.Fatal(err)
	}
	e.s.aiCommit(r, 2000, 1000)
	pending, err := e.s.aiReserve("u2", "claude-sonnet-4", st)
	if err != nil {
		t.Fatal(err)
	}
	_ = pending
	path := filepath.Join(e.dir, "assistant", "usage.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "u2") {
		t.Fatalf("a reservation must not reach the disk: %s", raw)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("usage.json mode %v %v", info, err)
		}
		if info, err := os.Stat(filepath.Join(e.dir, "assistant")); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("assistant dir mode %v %v", info, err)
		}
	}
	next := newAITestEnv(t, func(c *Config) { c.AIDir = filepath.Join(e.dir, "assistant") })
	day, total, users := next.s.aiUsageSnapshot()
	if day != time.Now().UTC().Format("2006-01-02") || total.Tokens != 3000 || users["u1"].Tokens != 3000 || len(users) != 1 {
		t.Fatalf("after a restart: %s %+v %v", day, total, users)
	}
	if math.Abs(total.USD-(0.006+0.015)) > 1e-9 {
		t.Fatalf("usd after a restart: %v", total.USD)
	}
	if _, err = next.s.aiReserve("u1", "claude-sonnet-4", st); err != nil {
		t.Fatalf("the restarted server counts on top of the file: %v", err)
	}
	next.s.aiCommit(aiReservation{UID: "u1"}, 100, 0)
	if _, total, _ = next.s.aiUsageSnapshot(); total.Tokens != 3100 {
		t.Fatalf("total %+v", total)
	}
}

func TestAIUsageOldDayDropped(t *testing.T) {
	e := newAITestEnv(t, nil)
	dir := filepath.Join(e.dir, "assistant")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"day":"2000-01-01","tokens":999,"usd":9,"users":{"u":{"tokens":999,"usd":9}}}`
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, total, users := e.s.aiUsageSnapshot(); total.Tokens != 0 || len(users) != 0 {
		t.Fatalf("an old day must be dropped: %+v %v", total, users)
	}
	r, err := e.s.aiReserve("u", "mystery", aiNormalize(aiSettings{}))
	if err != nil {
		t.Fatal(err)
	}
	e.s.aiCommit(r, 5, 5)
	raw, _ := os.ReadFile(filepath.Join(dir, "usage.json"))
	if strings.Contains(string(raw), "2000-01-01") || strings.Contains(string(raw), "999") {
		t.Fatalf("the old day stays on disk: %s", raw)
	}
	broken := newAITestEnv(t, nil)
	bdir := filepath.Join(broken.dir, "assistant")
	_ = os.MkdirAll(bdir, 0o700)
	_ = os.WriteFile(filepath.Join(bdir, "usage.json"), []byte("{oops"), 0o600)
	if _, total, _ := broken.s.aiUsageSnapshot(); total.Tokens != 0 {
		t.Fatalf("a damaged file starts empty: %+v", total)
	}
}

func TestAIUsageConcurrent(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiUsageSettings(aiTestOff[int64](), aiTestOff[int64](), aiTestOff[float64](), aiTestOff[float64]())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.s.aiReserve(fmt.Sprintf("u%d", i%3), "claude-sonnet-4", st)
			if err != nil {
				t.Error(err)
				return
			}
			e.s.aiCommit(r, 100, 50)
		}()
	}
	wg.Wait()
	_, total, users := e.s.aiUsageSnapshot()
	var sum int64
	for _, u := range users {
		sum += u.Tokens
	}
	if total.Tokens != 3000 || sum != 3000 {
		t.Fatalf("total %d, users %d", total.Tokens, sum)
	}
	e.s.ai.usage.mu.Lock()
	defer e.s.ai.usage.mu.Unlock()
	if e.s.ai.usage.held != (aiTally{}) || len(e.s.ai.usage.heldUsers) != 0 {
		t.Fatalf("holds left: %+v %v", e.s.ai.usage.held, e.s.ai.usage.heldUsers)
	}
}

func TestAISettingsGuard(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) { aiJSONReply(w, 200, `{"data":[]}`) })
	e := newAITestEnv(t, func(c *Config) { c.AIBaseURLs = map[string]string{aiProviderAnthropic: stub.srv.URL} })
	plain, err := e.s.createUser(userDraft{Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	boss, err := e.s.createUser(userDraft{Login: "boss", Password: "secret1", Type: security.UserAdmin, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	denied := map[string]security.Session{
		"admin key": {API: true, KeyType: security.KeyTypeAdmin, KeyID: "k1"},
		"user key":  {API: true, KeyType: security.KeyTypeUser, KeyID: "k2"},
		"user":      {UID: plain.ID, User: "bob"},
	}
	handlers := map[string]struct {
		h      http.HandlerFunc
		method string
		body   string
	}{
		"get":  {e.s.handleAISettingsGet, http.MethodGet, ""},
		"put":  {e.s.handleAISettingsPut, http.MethodPut, `{"enabled":false,"keys":{"anthropic":"` + aiCanary + `"}}`},
		"test": {e.s.handleAITest, http.MethodPost, `{"provider":"anthropic","key":"` + aiCanary + `"}`},
	}
	for who, sess := range denied {
		for name, h := range handlers {
			code, body := e.call(h.h, h.method, h.body, sess)
			if code != http.StatusForbidden || aiBodyCode(t, body) != "forbidden" {
				t.Errorf("%s on %s: %d %s", who, name, code, body)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "ai.json")); err == nil {
		t.Fatal("a refused PUT wrote ai.json")
	}
	if stub.calls() != 0 {
		t.Fatal("a refused test called the provider")
	}
	for _, sess := range []security.Session{{}, {UID: boss.ID, User: "boss"}} {
		for name, h := range handlers {
			if code, body := e.call(h.h, h.method, h.body, sess); code != http.StatusOK {
				t.Errorf("administrator on %s: %d %s", name, code, body)
			}
		}
	}
	if code, _ := e.call(e.s.handleAIStatus, http.MethodGet, "", denied["admin key"]); code != http.StatusOK {
		t.Fatalf("status keeps answering: %d", code)
	}
}

func TestAIUsageCountsOnlyReportedTokens(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiUsageSettings(aiTestOff[int64](), aiTestOff[int64](), aiTestOff[float64](), aiTestOff[float64]())
	r, err := e.s.aiReserve("u", "claude-sonnet-4", st)
	if err != nil {
		t.Fatal(err)
	}
	if r.In != 60000 || r.Out != 2048 {
		t.Fatalf("reservation %+v", r)
	}
	e.s.aiCommit(r, 0, 0)
	if _, total, users := e.s.aiUsageSnapshot(); total != (aiTally{}) || len(users) != 0 {
		t.Fatalf("a released reservation left usage behind: %+v %+v", total, users)
	}
	r, _ = e.s.aiReserve("u", "claude-sonnet-4", st)
	e.s.aiCommit(r, 1000, 500)
	_, total, users := e.s.aiUsageSnapshot()
	want := aiCost(r.Price, 1000, 500)
	if total.Tokens != 1500 || users["u"].Tokens != 1500 || math.Abs(total.USD-want) > 1e-12 || want == 0 {
		t.Fatalf("reported usage is charged as reported: %+v %+v, want $%v", total, users, want)
	}
}

func TestAIUsageDamagedFileRefusesCalls(t *testing.T) {
	e := newAITestEnv(t, nil)
	dir := filepath.Join(e.dir, "assistant")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "usage.json")
	garbage := "{oops-PRIVATE-DATA"
	if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	e.s.ai.usage.now = func() time.Time { return clock }
	st := aiNormalize(aiSettings{})
	_, err := e.s.aiReserve("u", "claude-sonnet-4", st)
	aiWantQuota(t, err)
	if ae := aiErrOf(t, err); !strings.Contains(ae.msg, "damaged") {
		t.Fatalf("the message must say why: %s", ae.msg)
	}
	bad, err := os.ReadFile(path + ".bad")
	if err != nil || string(bad) != garbage {
		t.Fatalf("copy of the damaged file: %q %v", bad, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path + ".bad"); info.Mode().Perm() != 0o600 {
			t.Fatalf("copy mode %v", info.Mode())
		}
	}
	logged := e.log.text()
	if !strings.Contains(logged, "damaged") || strings.Contains(logged, "PRIVATE") {
		t.Fatalf("log: %q", logged)
	}
	if _, err = e.s.aiReserve("u2", "claude-sonnet-4", st); err == nil {
		t.Fatal("the whole day is refused, for every user")
	}
	clock = clock.Add(24 * time.Hour)
	r, err := e.s.aiReserve("u", "claude-sonnet-4", st)
	if err != nil {
		t.Fatalf("calls resume on the next UTC day: %v", err)
	}
	e.s.aiCommit(r, 10, 10)
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "oops") {
		t.Fatalf("the damaged file was not replaced: %s", raw)
	}

	fixed := newAITestEnv(t, func(c *Config) { c.AIDir = dir })
	if _, err = fixed.s.aiReserve("u", "claude-sonnet-4", st); err != nil {
		t.Fatalf("a repaired file works after a restart: %v", err)
	}
}

func TestAISettingsTurnLimitsHaveCeilings(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.mustPut(t, `{"max_output_tokens":1000,"max_steps":4,"turn_input_tokens":50000}`)
	path := filepath.Join(e.dir, "ai.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ceilings := map[string]int{"max_output_tokens": aiMaxOutputCap, "max_steps": aiMaxStepsCap, "turn_input_tokens": aiTurnInputCap}
	for name, ceiling := range ceilings {
		for _, v := range []int{0, -1, ceiling + 1} {
			code, body := e.put(fmt.Sprintf(`{%q:%d}`, name, v))
			if code != http.StatusBadRequest || aiBodyCode(t, body) != "ai_bad_limit" || !strings.Contains(body, name) {
				t.Errorf("%s=%d -> %d %s", name, v, code, body)
			}
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("a refused PUT changed ai.json: %s", after)
	}
	for name, ceiling := range ceilings {
		for _, v := range []int{1, ceiling} {
			got := e.mustPut(t, fmt.Sprintf(`{%q:%d}`, name, v))
			if have := map[string]int{"max_output_tokens": got.MaxOutputTokens, "max_steps": got.MaxSteps, "turn_input_tokens": got.TurnInputTokens}[name]; have != v {
				t.Errorf("%s=%d saved as %d", name, v, have)
			}
		}
	}
	if aiMaxOutputCap != 32000 || aiMaxStepsCap != 60 || aiTurnInputCap != 200000 {
		t.Fatal("the documented ceilings changed")
	}
}

func TestAISettingsDailyLimitBounds(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.mustPut(t, `{"daily_tokens":5000,"user_daily_tokens":4000,"daily_usd":3,"user_daily_usd":2}`)
	path := filepath.Join(e.dir, "ai.json")
	before, _ := os.ReadFile(path)
	for _, body := range []string{
		`{"daily_tokens":-1}`, `{"user_daily_tokens":-1}`, `{"daily_usd":-0.01}`, `{"user_daily_usd":-1}`,
		`{"daily_tokens":1000000000001}`, `{"user_daily_tokens":9000000000000000000}`,
		`{"daily_usd":1000000.01}`, `{"user_daily_usd":1e300}`,
	} {
		code, resp := e.put(body)
		if code != http.StatusBadRequest || aiBodyCode(t, resp) != "ai_bad_limit" {
			t.Errorf("%s -> %d %s", body, code, resp)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("a refused PUT changed ai.json: %s", after)
	}
	cur, _ := e.s.aiLoad()
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := aiMergeSettings(cur, aiSettingsPut{DailyUSD: aiDailyOf(bad)})
		if ae := aiErrOf(t, err); ae.code != aiErrBadLimit || ae.status != http.StatusBadRequest {
			t.Errorf("%v: %+v", bad, ae)
		}
	}
	v := e.mustPut(t, `{"daily_tokens":1000000000000,"user_daily_tokens":1000000000000,"daily_usd":1000000,"user_daily_usd":1000000}`)
	if aiTestVal(v.DailyTokens) != aiTokensCap || aiTestVal(v.UserDailyUSD) != aiUSDCap {
		t.Fatalf("the largest values must be accepted: %+v", v)
	}

	v = e.mustPut(t, `{"daily_tokens":null,"user_daily_tokens":null,"daily_usd":null,"user_daily_usd":null}`)
	if v.DailyTokens != nil || v.UserDailyTokens != nil || v.DailyUSD != nil || v.UserDailyUSD != nil {
		t.Fatalf("null switches every daily limit off: %+v", v)
	}
	st, err := e.s.aiLoad()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r, err := e.s.aiReserve("u", "claude-sonnet-4", st)
		if err != nil {
			t.Fatalf("no daily limit may refuse a call: %v", err)
		}
		e.s.aiCommit(r, 900_000_000, 100_000_000)
	}
}

func TestAISettingsFileCannotPassCeilings(t *testing.T) {
	e := newAITestEnv(t, nil)
	file := `{"provider":"anthropic","max_steps":1000,"max_output_tokens":999999,"turn_input_tokens":99999999,` +
		`"daily_tokens":-5,"user_daily_tokens":0,"daily_usd":1e12,"user_daily_usd":-1}`
	if err := os.WriteFile(filepath.Join(e.dir, "ai.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	lim := func(st aiSettings) aiLimits { return st.limits() }
	st, err := e.s.aiLoad()
	if err != nil {
		t.Fatal(err)
	}
	for name, l := range map[string]aiLimits{"loaded": lim(st), "raw": lim(aiSettings{MaxSteps: 1000, MaxOutputTokens: 1 << 30, TurnInputTokens: 1 << 30})} {
		if l.MaxSteps != 60 || l.MaxOutput != 32000 || l.TurnInput != 200000 {
			t.Errorf("%s: per-turn limits not clamped: %+v", name, l)
		}
	}
	l := lim(st)
	if l.DailyTokens != nil || aiTestVal(l.UserDailyTokens) != 0 || aiTestVal(l.DailyUSD) != aiUSDCap || l.UserDailyUSD != nil {
		t.Fatalf("daily limits: negative is no limit, zero stays, huge is cut: %+v", l)
	}
	v := e.view(t)
	if v.MaxSteps != 60 || v.MaxOutputTokens != 32000 || v.TurnInputTokens != 200000 {
		t.Fatalf("the view shows the effective values: %+v", v)
	}
	if _, err = e.s.aiReserve("u", "mystery", st); err == nil {
		t.Fatal("a stored zero token limit must refuse the turn")
	}
	st.UserDailyTokens = nil
	r, err := e.s.aiReserve("u", "mystery", st)
	if err != nil || r.In != 200000 || r.Out != 32000 {
		t.Fatalf("a turn uses the clamped values: %+v %v", r, err)
	}
	if lim(aiSettings{}).MaxSteps != aiDefaultMaxSteps || lim(aiSettings{MaxSteps: -2}).MaxSteps != aiDefaultMaxSteps {
		t.Fatal("zero or negative takes the default")
	}
}

func TestAISettingsViewLimitBounds(t *testing.T) {
	e := newAITestEnv(t, nil)
	_, body := e.call(e.s.handleAISettingsGet, http.MethodGet, "")
	var got struct {
		Ceilings map[string]float64 `json:"limit_ceilings"`
		Max      map[string]float64 `json:"limit_max"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"max_output_tokens": 32000, "max_steps": 60, "turn_input_tokens": 200000}
	if len(got.Ceilings) != len(want) {
		t.Fatalf("limit_ceilings: %v", got.Ceilings)
	}
	for k, v := range want {
		if got.Ceilings[k] != v {
			t.Errorf("limit_ceilings.%s = %v, want %v", k, got.Ceilings[k], v)
		}
	}
	if len(got.Max) != 2 || got.Max["tokens"] != 1e12 || got.Max["usd"] != 1e6 {
		t.Fatalf("limit_max: %v", got.Max)
	}
	if v := e.mustPut(t, `{"max_steps":5}`); v.LimitCeilings.MaxSteps != 60 || v.LimitMax.Tokens != 1e12 {
		t.Fatalf("PUT answers with the bounds too: %+v %+v", v.LimitCeilings, v.LimitMax)
	}
}
