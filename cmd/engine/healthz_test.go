package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzProbeHeaders(t *testing.T) {
	h := healthzHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "http://park.example/healthz", nil)
	req.Host = "park.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Emtds-Host") != "park.example" {
		t.Fatalf("host = %q", rec.Header().Get("X-Emtds-Host"))
	}
	if rec.Header().Get("X-Emtds-Service") != "engine" {
		t.Fatalf("service = %q", rec.Header().Get("X-Emtds-Service"))
	}
	if _, ok := rec.Header()["X-Emtds-Probe"]; ok {
		t.Fatal("probe header set without a probe")
	}

	req = httptest.NewRequest(http.MethodGet, "http://park.example/healthz?probe=abc123", nil)
	req.Host = "park.example"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "ok" {
		t.Fatalf("probe body = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Emtds-Host") != "park.example" {
		t.Fatalf("probe host = %q", rec.Header().Get("X-Emtds-Host"))
	}
	if rec.Header().Get("X-Emtds-Probe") != "abc123" {
		t.Fatalf("probe = %q", rec.Header().Get("X-Emtds-Probe"))
	}

	req = httptest.NewRequest(http.MethodGet, "http://park.example/healthz?probe=zz!!", nil)
	req.Host = "park.example"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "ok" {
		t.Fatalf("bad probe body = %q", rec.Body.String())
	}
	if _, ok := rec.Header()["X-Emtds-Probe"]; ok {
		t.Fatal("invalid probe was echoed")
	}
}
