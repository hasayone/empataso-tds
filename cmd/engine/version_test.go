package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

// versionTestCommit is a valid 40-character lowercase hex commit SHA.
const versionTestCommit = "deadbeef1234567890abcdef1234567890abcd00"

func TestVersionHandlerGet(t *testing.T) {
	restore := config.SetBuildForTest(config.BuildInfo{
		Version:    "v1.0.0",
		Commit:     versionTestCommit,
		CommitDate: "2025-01-01T00:00:00Z",
		Channel:    "main",
	})
	defer restore()

	req := httptest.NewRequest(http.MethodGet, "/internal/version", nil)
	rec := httptest.NewRecorder()
	versionHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}

	var b config.BuildInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, rec.Body.String())
	}
	if b.Commit != versionTestCommit {
		t.Errorf("Commit = %q, want %q", b.Commit, versionTestCommit)
	}
	if b.Version != "v1.0.0" {
		t.Errorf("Version = %q", b.Version)
	}
	if b.Channel != "main" {
		t.Errorf("Channel = %q", b.Channel)
	}
}

func TestVersionHandlerMethodNotAllowed(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/internal/version", nil)
		rec := httptest.NewRecorder()
		versionHandler()(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
	}
}

func TestVersionNotInHealthz(t *testing.T) {
	restore := config.SetBuildForTest(config.BuildInfo{
		Version:    "v1.0.0",
		Commit:     versionTestCommit,
		CommitDate: "2025-01-01T00:00:00Z",
		Channel:    "main",
	})
	defer restore()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthzHandler(nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	// Body must not contain the commit.
	if strings.Contains(rec.Body.String(), versionTestCommit) {
		t.Errorf("healthz body contains commit %q", versionTestCommit)
	}
	// No header must contain the commit.
	for k, vs := range rec.Header() {
		for _, v := range vs {
			if strings.Contains(v, versionTestCommit) {
				t.Errorf("healthz header %s contains commit %q", k, versionTestCommit)
			}
		}
	}
}
