package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestLoadConfigFormat(t *testing.T) {
	env := map[string]string{"EMTDS_URL": "http://x", "EMTDS_API_KEY": "k"}
	get := func(k string) string { return env[k] }
	for value, want := range map[string]bool{"": false, "json": false, "toon": true, " TOON ": true} {
		env["EMTDS_MCP_FORMAT"] = value
		cfg, err := loadConfig(get)
		if err != nil || cfg.TOON != want {
			t.Errorf("EMTDS_MCP_FORMAT=%q: toon=%v err=%v", value, cfg.TOON, err)
		}
	}
	env["EMTDS_MCP_FORMAT"] = "yaml"
	if _, err := loadConfig(get); err == nil || !strings.Contains(err.Error(), "EMTDS_MCP_FORMAT") {
		t.Errorf("unknown format accepted: %v", err)
	}
}

func TestToonFormatAsksForTOONOnToolCallsOnly(t *testing.T) {
	var mu sync.Mutex
	accepts := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accepts[r.URL.Path] = r.Header.Get("Accept")
		mu.Unlock()
		if r.URL.Path == specPath {
			_, _ = w.Write([]byte(testSpec))
			return
		}
		w.Header().Set("Content-Type", "text/toon; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("error: \"no such group: g1\"\ncode: not_found"))
	}))
	defer ts.Close()
	client, err := newAPIClient(config{URL: ts.URL, APIKey: testKey, TOON: true})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.loadTools()
	if err != nil {
		t.Fatal(err)
	}
	var missing *tool
	for _, tl := range tools {
		if tl.path == "/api/missing" {
			missing = tl
		}
	}
	if missing == nil {
		t.Fatal("tool for /api/missing not found")
	}
	res := client.call(missing, map[string]any{})
	if !res.IsError || !strings.Contains(res.Text, "code: not_found\nerror: no such group: g1\n") {
		t.Errorf("result = %q", res.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	if accepts[specPath] != jsonContent || accepts["/api/missing"] != toonContent {
		t.Errorf("accept headers = %v", accepts)
	}
}

func TestToonField(t *testing.T) {
	body := []byte("error: \"a \\\"b\\\": c\"\ncode: bad_request\nparams:\n  code: nested")
	if got := toonField(body, "error"); got != `a "b": c` {
		t.Errorf("error = %q", got)
	}
	if got := toonField(body, "code"); got != "bad_request" {
		t.Errorf("code = %q", got)
	}
	if got := toonField(body, "missing"); got != "" {
		t.Errorf("missing = %q", got)
	}
}
