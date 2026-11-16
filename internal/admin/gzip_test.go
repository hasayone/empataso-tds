package admin

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGzipJSON(t *testing.T) {
	want := map[string]string{"id": "a", "name": "alpha"}
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, want)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("content-encoding %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("vary %q", rec.Header().Get("Vary"))
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("content-length %q", rec.Header().Get("Content-Length"))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != want["id"] || got["name"] != want["name"] {
		t.Fatalf("body %#v", got)
	}
}

func TestGzipPassthroughWithoutAcceptEncoding(t *testing.T) {
	const want = `{"ok":true}`
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(want))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("content-encoding %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.String() != want {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestGzipNoBodyStatuses(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusNoContent} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			h.ServeHTTP(rec, req)
			if rec.Code != code {
				t.Fatalf("status %d", rec.Code)
			}
			if rec.Header().Get("Content-Encoding") != "" {
				t.Fatalf("content-encoding %q", rec.Header().Get("Content-Encoding"))
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("body %q", rec.Body.String())
			}
		})
	}
}

func TestGzipFlushDeliversChunks(t *testing.T) {
	const want = "hello world"
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not a flusher")
		}
		if _, err := w.Write([]byte("hello ")); err != nil {
			t.Fatal(err)
		}
		f.Flush()
		if _, err := w.Write([]byte("world")); err != nil {
			t.Fatal(err)
		}
		f.Flush()
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("body %q", got)
	}
}

func TestGzipSkipsEventStreams(t *testing.T) {
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": open\n\n"))
		w.(http.Flusher).Flush()
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept", "text/event-stream")
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("content-encoding %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.String() != ": open\n\n" {
		t.Fatalf("body %q", rec.Body.String())
	}
	if !rec.Flushed {
		t.Fatal("the chunk was not flushed through")
	}
}

func TestGzipEligible(t *testing.T) {
	for _, c := range []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"gzip", map[string]string{"Accept-Encoding": "gzip"}, true},
		{"no gzip", nil, false},
		{"upgrade", map[string]string{"Accept-Encoding": "gzip", "Upgrade": "websocket"}, false},
		{"event stream", map[string]string{"Accept-Encoding": "gzip", "Accept": "text/event-stream"}, false},
		{"event stream among others", map[string]string{"Accept-Encoding": "gzip", "Accept": "application/json, Text/Event-Stream"}, false},
		{"json", map[string]string{"Accept-Encoding": "gzip", "Accept": "application/json"}, true},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if got := gzipEligible(req); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
