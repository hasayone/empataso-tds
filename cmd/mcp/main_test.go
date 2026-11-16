package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

const testKey = "k-secret-0123456789"

// testSpec is the OpenAPI document served by the fake admin.
const testSpec = `{
  "openapi": "3.1.0",
  "info": {"title": "emtds", "version": "1"},
  "paths": {
    "/api/groups": {
      "get": {
        "operationId": "listGroups", "summary": "List groups", "description": "All groups.",
        "x-mcp": "read",
        "parameters": [
          {"name": "f", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}},
          {"name": "limit", "in": "query", "description": "Page size", "schema": {"type": "integer"}}
        ]
      }
    },
    "/api/groups/{id}": {
      "put": {
        "operationId": "updateGroup", "summary": "Update a group", "x-mcp": "write", "x-emtds-heavy": true,
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object"}}}}
      }
    },
    "/api/echo/{id}": {
      "post": {
        "operationId": "echo", "x-mcp": "read",
        "parameters": [
          {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "body", "in": "query", "schema": {"type": "string"}}
        ],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object"}}}}
      }
    },
    "/api/landing/{path}": {
      "get": {
        "operationId": "readLanding", "x-mcp": "read",
        "parameters": [{"name": "path", "in": "path", "required": true, "schema": {"type": "string", "x-emtds-path": true}}]
      }
    },
    "/api/files/{id}/{path}": {
      "put": {
        "operationId": "writeFile", "summary": "Write a text file", "x-mcp": "write",
        "parameters": [
          {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}},
          {"name": "path", "in": "path", "required": true, "schema": {"type": "string", "x-emtds-path": true}}
        ],
        "requestBody": {"required": true, "content": {"text/plain": {"schema": {"type": "string", "description": "file text"}}}}
      }
    },
    "/api/missing": {"get": {"operationId": "missing", "x-mcp": "read"}},
    "/api/big": {"get": {"operationId": "big.report", "x-mcp": "read"}},
    "/api/leak": {"get": {"operationId": "leak", "x-mcp": "read"}},
    "/api/internal": {"get": {"operationId": "internal"}}
  }
}`

// seen is the last API request the fake admin received.
type seen struct {
	method, uri, key, contentType string
	body                          []byte
}

// fakeAdmin is an httptest admin serving testSpec and a few endpoints.
type fakeAdmin struct {
	*httptest.Server
	mu   sync.Mutex
	last seen
}

// lastRequest returns the last recorded API request.
func (f *fakeAdmin) lastRequest() seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// newFakeAdmin starts the fake admin; it requires testKey in the Api-Key header.
func newFakeAdmin(t *testing.T) *fakeAdmin {
	t.Helper()
	f := &fakeAdmin{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != testKey {
			http.Error(w, `{"code":"unauthorized","error":"bad key"}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.last = seen{r.Method, r.RequestURI, r.Header.Get("Api-Key"), r.Header.Get("Content-Type"), body}
		f.mu.Unlock()
		switch r.URL.Path {
		case specPath:
			io.WriteString(w, testSpec)
		case "/api/missing":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"not_found","error":"no such group"}`)
		case "/api/big":
			io.WriteString(w, strings.Repeat("a", 150000))
		case "/api/leak":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"echo `+r.Header.Get("Api-Key")+`"}`)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// newTestServer returns an MCP server wired to a fresh fake admin.
func newTestServer(t *testing.T, writes bool) (*server, *fakeAdmin) {
	t.Helper()
	admin := newFakeAdmin(t)
	client, err := newAPIClient(config{URL: admin.URL, APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.loadTools()
	if err != nil {
		t.Fatal(err)
	}
	return newServer(tools, writes, client, log.New(io.Discard, "", 0)), admin
}

// exchange feeds lines to the server and returns the decoded responses and
// the raw output.
func exchange(t *testing.T, s *server, lines ...string) ([]map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	if err := s.serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("output line is not JSON: %q", l)
		}
		msgs = append(msgs, m)
	}
	return msgs, out.String()
}

// callLine builds a tools/call request line.
func callLine(id int, name string, args map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	return string(b)
}

// toolText returns the text and isError flag of a tools/call response.
func toolText(t *testing.T, msg map[string]any) (string, bool) {
	t.Helper()
	res, ok := msg["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", msg)
	}
	content := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("want one content item, got %d", len(content))
	}
	return content[0].(map[string]any)["text"].(string), res["isError"].(bool)
}

// toolNames returns the names in a tools/list response.
func toolNames(msg map[string]any) []string {
	var names []string
	for _, x := range msg["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestRoundTrip(t *testing.T) {
	s, _ := newTestServer(t, false)
	msgs, _ := exchange(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		callLine(4, "listGroups", map[string]any{"limit": 5}),
	)
	if len(msgs) != 4 {
		t.Fatalf("want 4 responses (notification gets none), got %d", len(msgs))
	}
	hello := msgs[0]["result"].(map[string]any)
	if hello["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", hello["protocolVersion"])
	}
	if info := hello["serverInfo"].(map[string]any); info["name"] != "emtds" {
		t.Errorf("serverInfo = %v", info)
	}
	if _, ok := hello["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("capabilities.tools missing")
	}
	if r, ok := msgs[1]["result"].(map[string]any); !ok || len(r) != 0 {
		t.Errorf("ping result = %v", msgs[1]["result"])
	}
	names := toolNames(msgs[2])
	for _, want := range []string{"listGroups", "echo", "readLanding", "missing", "big_report"} {
		if !contains(names, want) {
			t.Errorf("tool %s missing from %v", want, names)
		}
	}
	if contains(names, "internal") {
		t.Error("operation without x-mcp became a tool")
	}
	text, isErr := toolText(t, msgs[3])
	if isErr || !strings.HasPrefix(text, dataBanner+"\nHTTP 200 OK\n") || !strings.Contains(text, `{"ok":true}`) {
		t.Errorf("unexpected result (isError=%v): %q", isErr, text)
	}
}

func TestToolDescriptionAndSchema(t *testing.T) {
	s, _ := newTestServer(t, true)
	upd := s.byName["updateGroup"]
	if !strings.Contains(upd.Description, "Update a group") || !strings.Contains(upd.Description, "WARNING") || !strings.Contains(upd.Description, "Heavy") {
		t.Errorf("description = %q", upd.Description)
	}
	req := upd.InputSchema["required"].([]string)
	if len(req) != 2 || req[0] != "id" || req[1] != "body" {
		t.Errorf("required = %v", req)
	}
	list := s.byName["listGroups"]
	props := list.InputSchema["properties"].(map[string]any)
	if props["limit"].(map[string]any)["description"] != "Page size" {
		t.Errorf("limit schema = %v", props["limit"])
	}
	if _, has := list.InputSchema["required"]; has {
		t.Error("listGroups has no required arguments")
	}
	tail := s.byName["readLanding"].InputSchema["properties"].(map[string]any)["path"].(map[string]any)
	if _, has := tail["x-emtds-path"]; has {
		t.Error("internal x-emtds-path marker leaked into the input schema")
	}
}

func TestWriteToolsGated(t *testing.T) {
	update := callLine(2, "updateGroup", map[string]any{"id": "g1", "body": map[string]any{"name": "x"}})
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	s, admin := newTestServer(t, false)
	msgs, _ := exchange(t, s, list, update)
	if contains(toolNames(msgs[0]), "updateGroup") {
		t.Error("write tool listed while writes are disabled")
	}
	text, isErr := toolText(t, msgs[1])
	if !isErr || !strings.Contains(text, "EMTDS_MCP_WRITE=1") {
		t.Errorf("disabled write tool: isError=%v text=%q", isErr, text)
	}
	if admin.lastRequest().method == http.MethodPut {
		t.Error("write request reached the admin while writes are disabled")
	}

	s, admin = newTestServer(t, true)
	msgs, _ = exchange(t, s, list, update)
	if !contains(toolNames(msgs[0]), "updateGroup") {
		t.Error("write tool missing while writes are enabled")
	}
	if _, isErr := toolText(t, msgs[1]); isErr {
		t.Error("write call failed")
	}
	got := admin.lastRequest()
	if got.method != http.MethodPut || got.uri != "/api/groups/g1" || got.contentType != "application/json" || string(got.body) != `{"name":"x"}` {
		t.Errorf("admin saw %+v", got)
	}
}

func TestUnknownToolIsErrorResult(t *testing.T) {
	s, _ := newTestServer(t, false)
	msgs, _ := exchange(t, s, callLine(1, "nope", nil))
	if text, isErr := toolText(t, msgs[0]); !isErr || !strings.Contains(text, "Unknown tool") {
		t.Errorf("isError=%v text=%q", isErr, text)
	}
}

func TestBodyParamCollisionKeepsBoth(t *testing.T) {
	s, admin := newTestServer(t, false)
	props := s.byName["echo"].InputSchema["properties"].(map[string]any)
	if _, ok := props["body"]; !ok {
		t.Error("query param body lost")
	}
	if _, ok := props["request_body"]; !ok {
		t.Error("body property not renamed to request_body")
	}
	req := s.byName["echo"].InputSchema["required"].([]string)
	if !contains(req, "request_body") || contains(req, "body") {
		t.Errorf("required = %v", req)
	}
	exchange(t, s, callLine(1, "echo", map[string]any{"id": "7", "body": "q", "request_body": map[string]any{"a": 1}}))
	got := admin.lastRequest()
	if got.uri != "/api/echo/7?body=q" || string(got.body) != `{"a":1}` {
		t.Errorf("admin saw %+v", got)
	}
}

func TestTextBodySchema(t *testing.T) {
	s, _ := newTestServer(t, true)
	schema := s.byName["writeFile"].InputSchema
	body, ok := schema["properties"].(map[string]any)["body"].(map[string]any)
	if !ok || body["type"] != "string" || body["description"] != "file text" {
		t.Errorf("body schema = %v", schema["properties"])
	}
	req := schema["required"].([]string)
	if len(req) != 3 || req[2] != "body" {
		t.Errorf("required = %v", req)
	}
}

func TestTextBodySentRaw(t *testing.T) {
	s, admin := newTestServer(t, true)
	text := "<?php echo \"hi\"; ?>\n{\"not\":\"json-encoded\"}\n"
	msgs, _ := exchange(t, s, callLine(1, "writeFile", map[string]any{"id": "7", "path": "css/main.css", "body": text}))
	if out, isErr := toolText(t, msgs[0]); isErr {
		t.Fatalf("write failed: %s", out)
	}
	got := admin.lastRequest()
	if got.method != http.MethodPut || got.uri != "/api/files/7/css/main.css" {
		t.Errorf("admin saw %s %s", got.method, got.uri)
	}
	if got.contentType != "text/plain; charset=utf-8" {
		t.Errorf("content type = %q", got.contentType)
	}
	if string(got.body) != text {
		t.Errorf("body = %q, want the raw text", got.body)
	}
}

func TestTextBodyEmptyFileAndBadArguments(t *testing.T) {
	s, admin := newTestServer(t, true)
	msgs, _ := exchange(t, s, callLine(1, "writeFile", map[string]any{"id": "7", "path": "a.txt", "body": ""}))
	if out, isErr := toolText(t, msgs[0]); isErr {
		t.Fatalf("empty file refused: %s", out)
	}
	if got := admin.lastRequest(); got.method != http.MethodPut || len(got.body) != 0 || got.contentType != "text/plain; charset=utf-8" {
		t.Errorf("admin saw %+v", got)
	}
	before := admin.lastRequest()
	for _, args := range []map[string]any{
		{"id": "7", "path": "a.txt", "body": map[string]any{"a": 1}},
		{"id": "7", "path": "a.txt", "body": 5},
		{"id": "7", "path": "a.txt"},
	} {
		msgs, _ := exchange(t, s, callLine(1, "writeFile", args))
		if out, isErr := toolText(t, msgs[0]); !isErr || !strings.Contains(out, "Invalid arguments") {
			t.Errorf("%v accepted: %q", args, out)
		}
	}
	if admin.lastRequest().uri != before.uri {
		t.Error("invalid call reached the admin")
	}
}

func TestTextBodyCollisionRenamesProperty(t *testing.T) {
	doc := `{"openapi":"3.1.0","paths":{"/a":{"put":{"operationId":"a","x-mcp":"write",` +
		`"parameters":[{"name":"body","in":"query","schema":{"type":"string"}}],` +
		`"requestBody":{"content":{"text/plain":{"schema":{"type":"string"}}}}}}}}`
	tools, err := parseTools([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	props := tools[0].InputSchema["properties"].(map[string]any)
	if _, ok := props["request_body"]; !ok || tools[0].bodyProp != "request_body" {
		t.Errorf("properties = %v, bodyProp = %q", props, tools[0].bodyProp)
	}
	if tools[0].contentType() != "text/plain; charset=utf-8" {
		t.Errorf("content type = %q", tools[0].contentType())
	}
}

func TestPathEscaping(t *testing.T) {
	s, admin := newTestServer(t, true)
	cases := []struct {
		tool string
		args map[string]any
		uri  string
	}{
		{"readLanding", map[string]any{"path": "dir one/c#d.html"}, "/api/landing/dir%20one/c%23d.html"},
		{"updateGroup", map[string]any{"id": "a/b c", "body": map[string]any{}}, "/api/groups/a%2Fb%20c"},
	}
	for _, c := range cases {
		msgs, _ := exchange(t, s, callLine(1, c.tool, c.args))
		if text, isErr := toolText(t, msgs[0]); isErr {
			t.Fatalf("%s: %s", c.tool, text)
		}
		if got := admin.lastRequest().uri; got != c.uri {
			t.Errorf("%s: uri = %s, want %s", c.tool, got, c.uri)
		}
	}
}

func TestPathTailRejectsTraversal(t *testing.T) {
	s, _ := newTestServer(t, false)
	for _, p := range []string{"../etc/passwd", "a/./b", "a//b", "/abs", ""} {
		msgs, _ := exchange(t, s, callLine(1, "readLanding", map[string]any{"path": p}))
		if text, isErr := toolText(t, msgs[0]); !isErr || !strings.Contains(text, "Invalid arguments") {
			t.Errorf("path %q accepted: %q", p, text)
		}
	}
}

func TestRepeatedQueryArray(t *testing.T) {
	s, admin := newTestServer(t, false)
	exchange(t, s, callLine(1, "listGroups", map[string]any{"f": []any{"a=1", "b=2"}, "limit": 5}))
	if got := admin.lastRequest().uri; got != "/api/groups?f=a%3D1&f=b%3D2&limit=5" {
		t.Errorf("uri = %s", got)
	}
}

func TestInvalidArguments(t *testing.T) {
	s, admin := newTestServer(t, true)
	before := admin.lastRequest()
	for _, args := range []map[string]any{
		{"limt": 5},
		{"id": "g1"},
	} {
		name := "listGroups"
		if _, ok := args["id"]; ok {
			name = "updateGroup"
		}
		msgs, _ := exchange(t, s, callLine(1, name, args))
		if text, isErr := toolText(t, msgs[0]); !isErr || !strings.Contains(text, "Invalid arguments") {
			t.Errorf("%s %v: %q", name, args, text)
		}
	}
	if admin.lastRequest().uri != before.uri {
		t.Error("invalid call reached the admin")
	}
}

func TestHTTPErrorIsError(t *testing.T) {
	s, _ := newTestServer(t, false)
	msgs, _ := exchange(t, s, callLine(1, "missing", nil))
	text, isErr := toolText(t, msgs[0])
	if !isErr {
		t.Fatal("404 must be isError")
	}
	for _, want := range []string{"HTTP 404 Not Found", "code: not_found", "error: no such group"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestTruncation(t *testing.T) {
	s, _ := newTestServer(t, false)
	msgs, _ := exchange(t, s, callLine(1, "big_report", nil))
	text, isErr := toolText(t, msgs[0])
	if isErr {
		t.Fatal("200 must not be isError")
	}
	if !strings.HasSuffix(text, "[truncated 47600 bytes]") {
		t.Errorf("suffix = %q", text[len(text)-40:])
	}
	if len(text) > maxBodyBytes+300 {
		t.Errorf("result has %d bytes", len(text))
	}
}

func TestReadCappedKeepsUTF8(t *testing.T) {
	src := strings.Repeat("€", 40000)
	head, cut := readCapped(strings.NewReader(src))
	if !utf8.Valid(head) {
		t.Error("cut inside a rune")
	}
	if int64(len(head))+cut != int64(len(src)) {
		t.Errorf("kept %d + cut %d != %d", len(head), cut, len(src))
	}
}

func TestAPIKeyNeverInOutput(t *testing.T) {
	s, _ := newTestServer(t, true)
	msgs, raw := exchange(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		callLine(2, "leak", nil),
		callLine(3, "missing", nil),
	)
	if strings.Contains(raw, testKey) {
		t.Errorf("API key found in output: %s", raw)
	}
	text, isErr := toolText(t, msgs[1])
	if !isErr || !strings.Contains(text, "[redacted]") {
		t.Errorf("leaking body not redacted: %q", text)
	}
}

func TestRPCErrors(t *testing.T) {
	s, _ := newTestServer(t, false)
	msgs, _ := exchange(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":"x"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{}}`,
		`{"jsonrpc":"2.0","method":"unknown/notification"}`,
		`not json`,
	)
	if len(msgs) != 4 {
		t.Fatalf("want 4 responses, got %d", len(msgs))
	}
	wantCodes := []float64{-32601, -32602, -32602, -32700}
	for i, want := range wantCodes {
		if got := msgs[i]["error"].(map[string]any)["code"]; got != want {
			t.Errorf("response %d code = %v, want %v", i, got, want)
		}
	}
	if id, present := msgs[3]["id"]; !present || id != nil {
		t.Errorf("parse error id = %v, want null", id)
	}
}

func TestLoadToolsUnauthorized(t *testing.T) {
	admin := newFakeAdmin(t)
	client, err := newAPIClient(config{URL: admin.URL, APIKey: "bad-key"})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.loadTools()
	if err == nil || tools != nil {
		t.Fatalf("want error, got %d tools", len(tools))
	}
	if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "bad-key") {
		t.Errorf("error = %v", err)
	}
}

func TestLoadToolsFailures(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"forbidden": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"server":    func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"bad json":  func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>") },
		"not spec":  func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"paths":{}}`) },
		"no tools":  func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"openapi":"3.1.0","paths":{}}`) },
	}
	for name, h := range cases {
		ts := httptest.NewServer(h)
		client, err := newAPIClient(config{URL: ts.URL, APIKey: testKey})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.loadTools(); err == nil {
			t.Errorf("%s: want error", name)
		}
		ts.Close()
	}
	client, _ := newAPIClient(config{URL: "http://127.0.0.1:1", APIKey: testKey})
	if _, err := client.loadTools(); err == nil {
		t.Error("unreachable admin: want error")
	}
}

func TestParseToolsRejects(t *testing.T) {
	op := func(extra string) string {
		return `{"openapi":"3.1.0","paths":{"/a/{id}":{"get":{"operationId":"a","x-mcp":"read"` + extra + `}}}}`
	}
	cases := map[string]string{
		"bad x-mcp":           `{"openapi":"3.1.0","paths":{"/a":{"get":{"operationId":"a","x-mcp":"admin"}}}}`,
		"missing operationId": `{"openapi":"3.1.0","paths":{"/a":{"get":{"x-mcp":"read"}}}}`,
		"placeholder":         op(``),
		"header param":        op(`,"parameters":[{"name":"id","in":"path"},{"name":"h","in":"header"}]`),
		"duplicate param":     op(`,"parameters":[{"name":"id","in":"path"},{"name":"id","in":"query"}]`),
		"no json body":        op(`,"parameters":[{"name":"id","in":"path"}],"requestBody":{"content":{"text/plain":{}}}`),
		"text not string":     op(`,"parameters":[{"name":"id","in":"path"}],"requestBody":{"content":{"text/plain":{"schema":{"type":"object"}}}}`),
		"xml body":            op(`,"parameters":[{"name":"id","in":"path"}],"requestBody":{"content":{"application/xml":{"schema":{"type":"string"}}}}`),
		"duplicate name":      `{"openapi":"3.1.0","paths":{"/a":{"get":{"operationId":"x.y","x-mcp":"read"}},"/b":{"get":{"operationId":"x_y","x-mcp":"read"}}}}`,
	}
	for name, doc := range cases {
		if _, err := parseTools([]byte(doc)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("a.b/c d-é_9"); got != "a_b_c_d-__9" {
		t.Errorf("got %q", got)
	}
	if got := sanitizeName(strings.Repeat("x", 100)); len(got) != maxToolName {
		t.Errorf("length = %d", len(got))
	}
}

func TestLoadConfig(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if _, err := loadConfig(get); err == nil || !strings.Contains(err.Error(), "EMTDS_URL") {
		t.Errorf("missing URL: %v", err)
	}
	env["EMTDS_URL"] = "http://x"
	if _, err := loadConfig(get); err == nil || !strings.Contains(err.Error(), "EMTDS_API_KEY") {
		t.Errorf("missing key: %v", err)
	}
	env["EMTDS_API_KEY"] = "k"
	cfg, err := loadConfig(get)
	if err != nil || cfg.Write {
		t.Errorf("cfg=%+v err=%v", cfg, err)
	}
	env["EMTDS_MCP_WRITE"] = "1"
	if cfg, _ = loadConfig(get); !cfg.Write {
		t.Error("EMTDS_MCP_WRITE=1 not honoured")
	}
	env["EMTDS_MCP_WRITE"] = "true"
	if cfg, _ = loadConfig(get); cfg.Write {
		t.Error("only 1 enables writes")
	}
}

func TestNewAPIClientRejects(t *testing.T) {
	for _, u := range []string{"ftp://x", "nohost", "http://"} {
		if _, err := newAPIClient(config{URL: u, APIKey: "k"}); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	bad := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(bad, []byte("not pem"), 0o600)
	if _, err := newAPIClient(config{URL: "https://x", APIKey: "k", CAFile: bad}); err == nil {
		t.Error("CA file without certificate accepted")
	}
	if _, err := newAPIClient(config{URL: "https://x", APIKey: "k", CAFile: bad + ".missing"}); err == nil {
		t.Error("missing CA file accepted")
	}
}

func TestCAFileForSelfSignedAdmin(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, testSpec)
	}))
	defer ts.Close()

	without, err := newAPIClient(config{URL: ts.URL, APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := without.loadTools(); err == nil {
		t.Fatal("self-signed certificate accepted without EMTDS_CA_FILE")
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	with, err := newAPIClient(config{URL: ts.URL, APIKey: testKey, CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := with.loadTools(); err != nil {
		t.Fatalf("with CA file: %v", err)
	}
}

func TestCrossHostRedirectNotFollowed(t *testing.T) {
	var hits int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer other.Close()
	redirector := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusFound))
	defer redirector.Close()

	client, err := newAPIClient(config{URL: redirector.URL, APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	res := client.call(&tool{method: http.MethodGet, path: "/x"}, nil)
	if !strings.Contains(res.Text, "HTTP 302") {
		t.Errorf("text = %q", res.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Error("redirect to another host was followed")
	}
}

func TestRunEndToEnd(t *testing.T) {
	admin := newFakeAdmin(t)
	env := map[string]string{"EMTDS_URL": admin.URL, "EMTDS_API_KEY": testKey}
	get := func(k string) string { return env[k] }

	var out, errOut bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	if code := run(get, in, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"result":{}`) {
		t.Errorf("stdout = %q", out.String())
	}

	env["EMTDS_API_KEY"] = "wrong"
	out.Reset()
	errOut.Reset()
	if code := run(get, strings.NewReader(""), &out, &errOut); code != 2 || out.Len() != 0 {
		t.Errorf("wrong key: exit %d stdout %q", code, out.String())
	}
	if !strings.Contains(errOut.String(), "401") || strings.Contains(errOut.String(), "wrong") {
		t.Errorf("stderr = %q", errOut.String())
	}

	env["EMTDS_URL"] = ""
	errOut.Reset()
	if code := run(get, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "EMTDS_URL") {
		t.Errorf("missing URL: exit %d stderr %q", code, errOut.String())
	}
}
