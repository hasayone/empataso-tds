package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

const aiTestKey = "sk-test-KEY-0123456789abcdefghij"

// aiStubReq is one request a provider stub received.
type aiStubReq struct {
	Method string
	Path   string
	Query  string
	URL    string
	Header http.Header
	Body   []byte
}

// aiStub is a provider stub: it records the requests and answers through handle.
type aiStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []aiStubReq
}

// newAIStub starts a stub. handle gets the 1-based number of the request.
func newAIStub(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, n int)) *aiStub {
	t.Helper()
	st := &aiStub{}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		st.mu.Lock()
		st.reqs = append(st.reqs, aiStubReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, URL: r.URL.String(), Header: r.Header.Clone(), Body: body})
		n := len(st.reqs)
		st.mu.Unlock()
		handle(w, r, n)
	}))
	t.Cleanup(st.srv.Close)
	return st
}

// calls returns how many requests the stub received.
func (s *aiStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// req returns the i-th request (0-based).
func (s *aiStub) req(i int) aiStubReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[i]
}

// aiJSONReply writes status and a JSON body.
func aiJSONReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// aiBodyMap decodes a recorded request body.
func aiBodyMap(t *testing.T, r aiStubReq) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return m
}

// aiErrOf returns the *aiError inside err, failing the test when there is none.
func aiErrOf(t *testing.T, err error) *aiError {
	t.Helper()
	var ae *aiError
	if !errors.As(err, &ae) {
		t.Fatalf("want *aiError, got %T %v", err, err)
	}
	return ae
}

// aiIDs returns the ids of models in order.
func aiIDs(models []aiModelInfo) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// aiTestRequest is a request with a system text, a tool round and one tool.
func aiTestRequest() aiRequest {
	return aiRequest{
		Model: "m1", System: "be brief", MaxTokens: 777,
		Messages: []aiTurnMsg{
			{Role: aiRoleUser, Text: "hello"},
			{Role: aiRoleAssistant, Text: "checking", ToolCalls: []aiToolCall{
				{ID: "c1", Name: "list_offers", Args: json.RawMessage(`{"limit":2}`)},
				{ID: "c2", Name: "list_landings"},
			}},
			{Role: aiRoleTool, ToolCallID: "c1", ToolName: "list_offers", Text: `{"items":[]}`},
			{Role: aiRoleTool, ToolCallID: "c2", ToolName: "list_landings", Text: `{"items":[1]}`},
		},
		Tools: []aiToolSpec{{Name: "list_offers", Description: "offers", Schema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}}}}},
	}
}

func TestAIProviderAnthropic(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/v1/models" {
			aiJSONReply(w, 200, `{"data":[{"id":"b"},{"id":"a"},{"id":"a"}]}`)
			return
		}
		aiJSONReply(w, 200, `{"content":[{"type":"text","text":"Hi "},{"type":"text","text":"there"},{"type":"tool_use","id":"tu_1","name":"list_offers","input":{"limit":5}}],
			"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":10}}`)
	})
	p := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client())
	reply, err := p.Complete(context.Background(), aiTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "Hi there" || reply.StopReason != "tool_use" || reply.InputTokens != 110 || reply.OutputTokens != 20 {
		t.Fatalf("reply %+v", reply)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ID != "tu_1" || reply.ToolCalls[0].Name != "list_offers" || string(reply.ToolCalls[0].Args) != `{"limit":5}` {
		t.Fatalf("tool calls %+v", reply.ToolCalls)
	}
	r := stub.req(0)
	if r.Method != http.MethodPost || r.Path != "/v1/messages" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
	if r.Header.Get("x-api-key") != aiTestKey || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
		t.Fatalf("headers %v", r.Header)
	}
	body := aiBodyMap(t, r)
	if body["model"] != "m1" || body["max_tokens"] != float64(777) || body["system"] != "be brief" {
		t.Fatalf("body %v", body)
	}
	tools := body["tools"].([]any)
	if _, ok := tools[0].(map[string]any)["input_schema"]; !ok || tools[0].(map[string]any)["name"] != "list_offers" {
		t.Fatalf("tools %v", tools)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d: %s", len(msgs), r.Body)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 3 || asst[1].(map[string]any)["type"] != "tool_use" || asst[2].(map[string]any)["input"] == nil {
		t.Fatalf("assistant blocks %v", asst)
	}
	last := msgs[2].(map[string]any)
	results := last["content"].([]any)
	if last["role"] != "user" || len(results) != 2 || results[0].(map[string]any)["type"] != "tool_result" || results[1].(map[string]any)["tool_use_id"] != "c2" {
		t.Fatalf("tool results %v", last)
	}

	models, err := p.Models(context.Background())
	if err != nil || strings.Join(aiIDs(models), ",") != "a,b" {
		t.Fatalf("models %v %v", models, err)
	}
	if stub.req(1).Path != "/v1/models" || stub.req(1).Method != http.MethodGet {
		t.Fatalf("models request %+v", stub.req(1))
	}
}

func TestAIProviderOpenAICompat(t *testing.T) {
	cases := []struct {
		provider, maxField, otherField string
		toolName                       bool
		modelID, wantModel             string
	}{
		{aiProviderOpenAI, "max_completion_tokens", "max_tokens", false, "gpt-4o", "gpt-4o"},
		{aiProviderOpenRouter, "max_tokens", "max_completion_tokens", false, "anthropic/claude-sonnet-4", "anthropic/claude-sonnet-4"},
		{aiProviderGemini, "max_tokens", "max_completion_tokens", true, "models/gemini-2.5-pro", "gemini-2.5-pro"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if r.URL.Path == "/models" {
					aiJSONReply(w, 200, `{"data":[{"id":"`+tc.modelID+`"}]}`)
					return
				}
				aiJSONReply(w, 200, `{"choices":[{"message":{"content":null,"tool_calls":[
					{"id":"","type":"function","function":{"name":"list_offers","arguments":"{\"limit\":3}"}}]},"finish_reason":"tool_calls"}],
					"usage":{"prompt_tokens":50,"completion_tokens":7}}`)
			})
			p := newOpenAIProvider(tc.provider, stub.srv.URL, aiTestKey, stub.srv.Client())
			reply, err := p.Complete(context.Background(), aiTestRequest())
			if err != nil {
				t.Fatal(err)
			}
			if reply.Text != "" || reply.StopReason != "tool_calls" || reply.InputTokens != 50 || reply.OutputTokens != 7 {
				t.Fatalf("reply %+v", reply)
			}
			if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ID != "call_0" || reply.ToolCalls[0].Name != "list_offers" || string(reply.ToolCalls[0].Args) != `{"limit":3}` {
				t.Fatalf("tool calls %+v", reply.ToolCalls)
			}
			r := stub.req(0)
			if r.Path != "/chat/completions" || r.Query != "" {
				t.Fatalf("request %s?%s", r.Path, r.Query)
			}
			if r.Header.Get("Authorization") != "Bearer "+aiTestKey {
				t.Fatalf("authorization %q", r.Header.Get("Authorization"))
			}
			if strings.Contains(r.URL, aiTestKey) || strings.Contains(r.Query, "key") {
				t.Fatalf("the key is in the URL: %s", r.URL)
			}
			body := aiBodyMap(t, r)
			if body[tc.maxField] != float64(777) || body[tc.otherField] != nil {
				t.Fatalf("max field %s: %v", tc.maxField, body)
			}
			msgs := body["messages"].([]any)
			if len(msgs) != 5 || msgs[0].(map[string]any)["role"] != "system" {
				t.Fatalf("messages %s", r.Body)
			}
			calls := msgs[2].(map[string]any)["tool_calls"].([]any)
			fn := calls[0].(map[string]any)["function"].(map[string]any)
			if calls[0].(map[string]any)["type"] != "function" || fn["arguments"] != `{"limit":2}` {
				t.Fatalf("assistant tool calls %v", calls)
			}
			if second := calls[1].(map[string]any)["function"].(map[string]any); second["arguments"] != `{}` {
				t.Fatalf("empty arguments %v", second)
			}
			tool := msgs[3].(map[string]any)
			if tool["role"] != "tool" || tool["tool_call_id"] != "c1" {
				t.Fatalf("tool message %v", tool)
			}
			if _, has := tool["name"]; has != tc.toolName {
				t.Fatalf("tool message name present=%v want %v", has, tc.toolName)
			}
			tools := body["tools"].([]any)
			if tools[0].(map[string]any)["type"] != "function" || tools[0].(map[string]any)["function"].(map[string]any)["parameters"] == nil {
				t.Fatalf("tools %v", tools)
			}
			models, err := p.Models(context.Background())
			if err != nil || len(models) != 1 || models[0].ID != tc.wantModel {
				t.Fatalf("models %v %v", models, err)
			}
			if got := stub.req(1).Header.Get("Authorization"); got != "Bearer "+aiTestKey {
				t.Fatalf("models authorization %q", got)
			}
		})
	}
}

func TestAIProviderStatusErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantStatus int
		wantText   string
		wantCalls  int
	}{
		{"unauthorized", 401, `{"error":{"message":"bad key"}}`, aiErrBadKey, 400, "(HTTP 401)", 1},
		{"forbidden", 403, `{}`, aiErrBadKey, 400, "(HTTP 403)", 1},
		{"openai key code", 401, `{"error":{"message":"Incorrect API key provided: ` + aiTestKey[:8] + `****","type":"invalid_request_error","code":"invalid_api_key"}}`,
			aiErrBadKey, 400, "(HTTP 401, invalid_api_key)", 1},
		{"anthropic key type", 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, aiErrBadKey, 400, "(HTTP 401, authentication_error)", 1},
		{"gemini key status", 403, `[{"error":{"code":403,"message":"API key not valid","status":"PERMISSION_DENIED"}}]`, aiErrBadKey, 400, "(HTTP 403, PERMISSION_DENIED)", 1},
		{"bad request with echo", 400, `{"error":{"message":"invalid param ` + aiTestKey + ` here"}}`, aiErrProvider, 502, "invalid param *** here", 1},
		{"error as text", 400, `{"error":"plain reason"}`, aiErrProvider, 502, "plain reason", 1},
		{"list of errors", 400, `[{"error":{"message":"gemini style"}}]`, aiErrProvider, 502, "gemini style", 1},
		{"not found html", 404, `<html>private page</html>`, aiErrProvider, 502, "the provider answered 404", 1},
		{"server error twice", 500, `{"error":{"message":"boom"}}`, aiErrProvider, 502, "boom", 2},
		{"rate limited twice", 429, `{}`, aiErrRateLimited, 429, "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
				w.Header().Set("Retry-After", "0")
				aiJSONReply(w, tc.status, tc.body)
			})
			for name, call := range map[string]func() error{
				"anthropic": func() error {
					_, err := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()).Complete(context.Background(), aiTestRequest())
					return err
				},
				"openai": func() error {
					_, err := newOpenAIProvider(aiProviderOpenAI, stub.srv.URL, aiTestKey, stub.srv.Client()).Complete(context.Background(), aiTestRequest())
					return err
				},
			} {
				before := stub.calls()
				ae := aiErrOf(t, call())
				if ae.code != tc.wantCode || ae.status != tc.wantStatus {
					t.Fatalf("%s: code %s status %d", name, ae.code, ae.status)
				}
				if strings.Contains(ae.msg, aiTestKey) || strings.Contains(ae.msg, aiTestKey[:8]) || strings.Contains(ae.msg, "private page") {
					t.Fatalf("%s: error text leaks: %s", name, ae.msg)
				}
				if tc.wantText != "" && !strings.Contains(ae.msg, tc.wantText) {
					t.Fatalf("%s: text %q lacks %q", name, ae.msg, tc.wantText)
				}
				if got := stub.calls() - before; got != tc.wantCalls {
					t.Fatalf("%s: %d calls, want %d", name, got, tc.wantCalls)
				}
			}
		})
	}
}

func TestAIProviderRetry(t *testing.T) {
	t.Run("429 then ok", func(t *testing.T) {
		stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
			if n == 1 {
				w.Header().Set("Retry-After", "0")
				aiJSONReply(w, 429, `{}`)
				return
			}
			aiJSONReply(w, 200, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
		})
		reply, err := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()).Complete(context.Background(), aiTestRequest())
		if err != nil || reply.Text != "ok" || stub.calls() != 2 {
			t.Fatalf("reply %+v err %v calls %d", reply, err, stub.calls())
		}
	})
	t.Run("503 then ok without Retry-After", func(t *testing.T) {
		stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
			if n == 1 {
				aiJSONReply(w, 503, `{}`)
				return
			}
			aiJSONReply(w, 200, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		})
		p := newOpenAIProvider(aiProviderOpenAI, stub.srv.URL, aiTestKey, stub.srv.Client())
		p.c.retryWait = 0
		reply, err := p.Complete(context.Background(), aiTestRequest())
		if err != nil || reply.Text != "ok" || stub.calls() != 2 {
			t.Fatalf("reply %+v err %v calls %d", reply, err, stub.calls())
		}
	})
	t.Run("long Retry-After is not waited for", func(t *testing.T) {
		stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("Retry-After", "60")
			aiJSONReply(w, 429, `{}`)
		})
		start := time.Now()
		_, err := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()).Complete(context.Background(), aiTestRequest())
		if ae := aiErrOf(t, err); ae.code != aiErrRateLimited {
			t.Fatalf("code %s", ae.code)
		}
		if stub.calls() != 1 || time.Since(start) > 5*time.Second {
			t.Fatalf("calls %d after %v", stub.calls(), time.Since(start))
		}
	})
	t.Run("canceled while waiting", func(t *testing.T) {
		stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("Retry-After", "5")
			aiJSONReply(w, 500, `{}`)
		})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()).Complete(ctx, aiTestRequest())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want the context error, got %v", err)
		}
	})
}

func TestAIProviderBodyTooLarge(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"`))
		chunk := []byte(strings.Repeat("x", 1<<20))
		for i := 0; i < 5; i++ {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`"}]}`))
	})
	_, err := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()).Models(context.Background())
	ae := aiErrOf(t, err)
	if ae.code != aiErrProvider || !strings.Contains(ae.msg, "too large") {
		t.Fatalf("got %s %s", ae.code, ae.msg)
	}
}

func TestAIProviderBadAnswer(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, 200, `not json`)
	})
	if _, err := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()).Complete(context.Background(), aiTestRequest()); aiErrOf(t, err).code != aiErrProvider {
		t.Fatalf("anthropic: %v", err)
	}
	empty := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) { aiJSONReply(w, 200, `{"choices":[]}`) })
	if _, err := newOpenAIProvider(aiProviderOpenAI, empty.srv.URL, aiTestKey, empty.srv.Client()).Complete(context.Background(), aiTestRequest()); aiErrOf(t, err).code != aiErrProvider {
		t.Fatalf("openai: %v", err)
	}
}

func TestAIProviderTimeout(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	p := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client())
	p.c.timeout = 50 * time.Millisecond
	_, err := p.Complete(context.Background(), aiTestRequest())
	if ae := aiErrOf(t, err); ae.code != aiErrTimeout || ae.status != http.StatusGatewayTimeout {
		t.Fatalf("got %s %d", ae.code, ae.status)
	}
}

func TestAIProviderCanceled(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) { aiJSONReply(w, 200, `{}`) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newAnthropicProvider(stub.srv.URL, aiTestKey, stub.srv.Client()).Complete(ctx, aiTestRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestAIProviderUnreachableNoKeyInError(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {})
	url := stub.srv.URL
	stub.srv.Close()
	_, err := newAnthropicProvider(url, aiTestKey, http.DefaultClient).Models(context.Background())
	ae := aiErrOf(t, err)
	if ae.code != aiErrProvider || strings.Contains(ae.msg, aiTestKey) || strings.Contains(ae.msg, url) {
		t.Fatalf("got %s %s", ae.code, ae.msg)
	}
}

func TestAIProviderRedirectRejected(t *testing.T) {
	var hits atomic.Int32
	target := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		hits.Add(1)
		aiJSONReply(w, 200, `{"data":[]}`)
	})
	redir := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		http.Redirect(w, r, target.srv.URL+"/v1/models", http.StatusFound)
	})
	hc := newAIHTTPClient(&http.Transport{})
	_, err := newAnthropicProvider(redir.srv.URL, aiTestKey, hc).Models(context.Background())
	ae := aiErrOf(t, err)
	if ae.code != aiErrProvider || !strings.Contains(ae.msg, "redirected") {
		t.Fatalf("got %s %s", ae.code, ae.msg)
	}
	if hits.Load() != 0 {
		t.Fatalf("the redirect target was called %d times", hits.Load())
	}
}

func TestAIProviderHostAllowList(t *testing.T) {
	for _, h := range []string{"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com", "openrouter.ai", "API.OpenAI.com"} {
		if !aiHostAllowed(h) {
			t.Errorf("%s must be allowed", h)
		}
	}
	for _, h := range []string{"", "127.0.0.1", "localhost", "evil.com", "api.openai.com.evil.com", "evilopenrouter.ai", "169.254.169.254"} {
		if aiHostAllowed(h) {
			t.Errorf("%s must be refused", h)
		}
	}
	tr, ok := aiProdHTTP.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("the production transport must not use a proxy: %#v", aiProdHTTP.Transport)
	}
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) { aiJSONReply(w, 200, `{"data":[]}`) })
	if _, err := aiProdHTTP.Get(stub.srv.URL); !errors.Is(err, errAIHostDenied) {
		t.Fatalf("direct call to a local host: %v", err)
	}
	_, err := newAnthropicProvider(stub.srv.URL, aiTestKey, aiProdHTTP).Models(context.Background())
	ae := aiErrOf(t, err)
	if ae.code != aiErrProvider || !strings.Contains(ae.msg, "not allowed") {
		t.Fatalf("got %s %s", ae.code, ae.msg)
	}
	if stub.calls() != 0 {
		t.Fatalf("a host outside the list was dialed")
	}
}

func TestAIProviderForServer(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, 200, `{"data":[{"id":"only"}]}`)
	})
	bases := map[string]string{}
	for _, id := range aiProviderIDs {
		bases[id] = stub.srv.URL + "/" + id
	}
	e := newAITestEnv(t, func(c *Config) { c.AIBaseURLs = bases })
	for i, id := range aiProviderIDs {
		prov, err := e.s.aiProviderFor(aiSettings{Provider: id, Keys: map[string]string{id: aiTestKey}})
		if err != nil {
			t.Fatal(err)
		}
		models, err := prov.Models(context.Background())
		if err != nil || len(models) != 1 || models[0].ID != "only" {
			t.Fatalf("%s: %v %v", id, models, err)
		}
		if got := stub.req(i).Path; !strings.HasPrefix(got, "/"+id+"/") {
			t.Fatalf("%s called %s", id, got)
		}
	}
	if _, err := e.s.aiProviderFor(aiSettings{Provider: aiProviderOpenAI}); aiErrOf(t, err).code != aiErrBadKey {
		t.Fatalf("a missing key must be ai_bad_key: %v", err)
	}
	plain := newAITestEnv(t, nil)
	for id, want := range map[string]string{
		aiProviderOpenAI:     "https://api.openai.com/v1",
		aiProviderOpenRouter: "https://openrouter.ai/api/v1",
		aiProviderGemini:     "https://generativelanguage.googleapis.com/v1beta/openai",
		aiProviderAnthropic:  "https://api.anthropic.com",
	} {
		if got := plain.s.aiBaseURL(id); got != want {
			t.Errorf("%s base %s, want %s", id, got, want)
		}
	}
	if plain.s.aiHTTPClient(aiProviderOpenAI) != aiProdHTTP {
		t.Fatal("without test injection the guarded client must be used")
	}
}

func TestAIProviderUsageMissing(t *testing.T) {
	anth := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			aiJSONReply(w, 200, `{"content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn"}`)
			return
		}
		aiJSONReply(w, 200, `{"content":[],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0}}`)
	})
	p := newAnthropicProvider(anth.srv.URL, aiTestKey, anth.srv.Client())
	reply, err := p.Complete(context.Background(), aiTestRequest())
	if err != nil || !reply.UsageMissing || reply.InputTokens != 0 {
		t.Fatalf("anthropic without usage: %+v %v", reply, err)
	}
	if reply, err = p.Complete(context.Background(), aiTestRequest()); err != nil || reply.UsageMissing {
		t.Fatalf("an empty answer is not flagged: %+v %v", reply, err)
	}
	oa := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, 200, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"t","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	})
	reply, err = newOpenAIProvider(aiProviderOpenRouter, oa.srv.URL, aiTestKey, oa.srv.Client()).Complete(context.Background(), aiTestRequest())
	if err != nil || !reply.UsageMissing || len(reply.ToolCalls) != 1 {
		t.Fatalf("openai without usage: %+v %v", reply, err)
	}
	with := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, 200, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	})
	reply, _ = newOpenAIProvider(aiProviderOpenAI, with.srv.URL, aiTestKey, with.srv.Client()).Complete(context.Background(), aiTestRequest())
	if reply.UsageMissing || reply.InputTokens != 3 {
		t.Fatalf("reported usage: %+v", reply)
	}
}

func TestAIProviderDialPort(t *testing.T) {
	for addr, want := range map[string]bool{
		"api.openai.com:443": true, "api.anthropic.com:443": true, "openrouter.ai:443": true,
		"generativelanguage.googleapis.com:443": true, "API.openai.com:443": true,
		"api.openai.com:80": false, "api.openai.com:8443": false, "api.openai.com:22": false,
		"api.openai.com": false, "api.openai.com:": false, "evil.com:443": false, "127.0.0.1:443": false,
	} {
		if got := aiDialAllowed(addr); got != want {
			t.Errorf("%s: allowed=%v, want %v", addr, got, want)
		}
	}
	tr := aiProdHTTP.Transport.(*http.Transport)
	for _, addr := range []string{"api.openai.com:8080", "api.openai.com:80", "evil.com:443"} {
		if _, err := tr.DialContext(context.Background(), "tcp", addr); !errors.Is(err, errAIHostDenied) {
			t.Errorf("%s must be refused by the production dialer, got %v", addr, err)
		}
	}
}

func TestAIProviderTestEndpoint(t *testing.T) {
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, 200, `{"data":[{"id":"local-model"}]}`)
	})
	other := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) { aiJSONReply(w, 200, `{"data":[]}`) })
	e := newAITestEnv(t, func(c *Config) { c.AITestBaseURL = stub.srv.URL + "/v1/" })

	prov, err := e.s.aiNewProvider(aiProviderOpenAI, aiTestKey)
	if err != nil {
		t.Fatal(err)
	}
	models, err := prov.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "local-model" {
		t.Fatalf("the openai provider must reach the test endpoint: %v %v", models, err)
	}
	if got := stub.req(0); got.Path != "/v1/models" || got.Header.Get("Authorization") != "Bearer "+aiTestKey {
		t.Fatalf("request %s %v", got.Path, got.Header)
	}
	if e.s.aiHTTPClient(aiProviderOpenAI) == aiProdHTTP {
		t.Fatal("the test endpoint needs its own guarded client")
	}

	hc := e.s.aiHTTPClient(aiProviderOpenAI)
	if _, err = newOpenAIProvider(aiProviderOpenAI, other.srv.URL, aiTestKey, hc).Models(context.Background()); !strings.Contains(err.Error(), "not allowed") || other.calls() != 0 {
		t.Fatalf("only the test host:port is let through: %v, calls %d", err, other.calls())
	}

	for _, id := range []string{aiProviderAnthropic, aiProviderOpenRouter, aiProviderGemini} {
		if got := e.s.aiBaseURL(id); got != aiProviderBases[id] {
			t.Errorf("%s must ignore the test address: %s", id, got)
		}
		if e.s.aiHTTPClient(id) != aiProdHTTP {
			t.Errorf("%s must use the guarded client", id)
		}
	}
	if _, err = e.s.aiNewProvider(aiProviderAnthropic, aiTestKey); err != nil {
		t.Fatal(err)
	}

	plain := newAITestEnv(t, nil)
	hc = plain.s.aiHTTPClient(aiProviderOpenAI)
	_, err = newOpenAIProvider(aiProviderOpenAI, stub.srv.URL, aiTestKey, hc).Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not allowed") || stub.calls() != 1 {
		t.Fatalf("without the test address a host outside the list is refused: %v, calls %d", err, stub.calls())
	}
}

func TestAIProviderTestEndpointFlag(t *testing.T) {
	flag := func(e *aiTestEnv, h http.HandlerFunc) bool {
		code, body := e.call(h, http.MethodGet, "")
		var v map[string]any
		if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil {
			t.Fatalf("%d %s", code, body)
		}
		on, ok := v["test_endpoint"].(bool)
		if !ok {
			t.Fatalf("test_endpoint is missing: %s", body)
		}
		return on
	}
	on := newAITestEnv(t, func(c *Config) { c.AITestBaseURL = "http://127.0.0.1:9999/v1" })
	off := newAITestEnv(t, nil)
	if !flag(on, on.s.handleAISettingsGet) || !flag(on, on.s.handleAIStatus) {
		t.Fatal("test_endpoint must be true when the address is set")
	}
	if flag(off, off.s.handleAISettingsGet) || flag(off, off.s.handleAIStatus) {
		t.Fatal("test_endpoint must be false without the address")
	}
	if logged := on.log.text(); !strings.Contains(logged, "127.0.0.1:9999") || !strings.Contains(logged, "AI test endpoint") {
		t.Fatalf("the start must warn: %q", logged)
	}
	if strings.Contains(off.log.text(), "AI test endpoint") {
		t.Fatal("no warning without the address")
	}
}

func TestAIProviderTestEndpointBadAddress(t *testing.T) {
	for _, base := range []string{"ftp://127.0.0.1/v1", "127.0.0.1:9999", "http://", "://x"} {
		dir := t.TempDir()
		hash, _ := security.HashPassword("p@ss")
		_, err := New(Config{AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(), Limiter: allowAll{},
			AIFile: filepath.Join(dir, "ai.json"), AIDir: filepath.Join(dir, "assistant"), AITestBaseURL: base})
		if err == nil {
			t.Errorf("%q must stop the start", base)
		}
	}
	for base, want := range map[string]string{
		"http://127.0.0.1:9999/v1": "127.0.0.1:9999", "https://localhost/v1": "localhost:443", "http://[::1]/x": "[::1]:80",
	} {
		if got, err := aiTestHostPort(base); err != nil || got != want {
			t.Errorf("%s: %q %v", base, got, err)
		}
	}
}
