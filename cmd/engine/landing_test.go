package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/logbuf"
	"github.com/egerkuzma/kuztds/internal/render"
)

func TestLocalLandingHTMLAndAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "groups.json"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	land := filepath.Join(dir, "7")
	if err := os.Mkdir(land, 0o755); err != nil {
		t.Fatal(err)
	}
	page := "<html><head><title>t</title></head><body><a href=\"{offer}\">go</a><link rel=\"stylesheet\" href=\"style.css\"></body></html>"
	if err := os.WriteFile(filepath.Join(land, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(land, "style.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	book := loadBook(t, "landings.json", []config.Landing{
		{ID: "7", Status: true, Local: true},
	}, func(l config.Landing) string { return l.ID })
	g := group(config.Stream{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect",
		Landings: []config.ShareRef{{ID: "7", Share: 1}},
	}})
	ins := &captureIns{}
	buf := logbuf.New(ins, 100, 1, time.Millisecond, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go buf.Run(ctx)
	h := testEnv(t, g, func(d *engineDeps) {
		d.landings = book
		d.logs = buf
		d.landServe = newLandingServer(dir, "", func(id string) (config.Landing, bool) {
			return book.Get(id)
		}, discardLog())
	})
	rec := do(t, h, "/g1?utm=fb", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if err := buf.Close(cctx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type %s", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<base href=\"/l/7/\">") {
		t.Fatalf("base missing: %s", body)
	}
	if !strings.Contains(body, "/g1/offer?cid=") || strings.Contains(body, "{offer}") {
		t.Fatalf("offer macro: %s", body)
	}
	if !strings.Contains(body, "utm=fb") {
		t.Fatalf("query dropped: %s", body)
	}
	if len(ins.events) != 1 || ins.events[0].LandingID != "7" || ins.events[0].Redirect != "landing" {
		t.Fatalf("event %+v", ins.events)
	}
	if !strings.Contains(body, ins.events[0].CID) {
		t.Fatalf("body cid %s not in %s", ins.events[0].CID, body)
	}
	css := do(t, h, "/l/7/style.css", "8.8.8.8", nil)
	if css.Code != http.StatusOK || !strings.Contains(css.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css %d %s body %s", css.Code, css.Header().Get("Content-Type"), css.Body.String())
	}
	if css.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("cache %s", css.Header().Get("Cache-Control"))
	}
	req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
	req.URL.Path = "/l/7/../groups.json"
	got := httptest.NewRecorder()
	srv := newLandingServer(dir, "", nil, discardLog())
	srv.serveAsset(got, req)
	if got.Code != http.StatusNotFound || strings.Contains(got.Body.String(), "SECRET") {
		t.Fatalf("escape %d %s", got.Code, got.Body.String())
	}
}

func TestLandingPHPProxy(t *testing.T) {
	var gotPath, gotQuery, gotCID, gotIP string
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotCID = r.Header.Get("X-Kuztds-Cid")
		gotIP = r.Header.Get("X-Forwarded-For")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<head></head><a href=\"{offer}\">go</a>"))
	}))
	defer php.Close()
	srv := newLandingServer(t.TempDir(), php.URL, func(id string) (config.Landing, bool) {
		if id == "7" {
			return config.Landing{ID: "7", Status: true, Local: true, Entry: "index.php"}, true
		}
		return config.Landing{}, false
	}, discardLog())
	req := httptest.NewRequest(http.MethodGet, "/g1?x=1", nil)
	req.Header.Set("User-Agent", "ua-test")
	rec := httptest.NewRecorder()
	srv.serveEntry(rec, req, "7", render.MacroDeps{
		CID: "0123456789", IP: "203.0.113.8", Offer: "/g1/offer?cid=0123456789",
	})
	if gotPath != "/7/index.php" || gotQuery != "x=1" || gotCID != "0123456789" || gotIP != "203.0.113.8" {
		t.Fatalf("php saw path %s query %s cid %s ip %s", gotPath, gotQuery, gotCID, gotIP)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<base href=\"/l/7/\">") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/g1/offer?cid=0123456789") {
		t.Fatalf("offer not expanded: %s", rec.Body.String())
	}
}

func TestLandingPHPEntryRedirect(t *testing.T) {
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.com/x")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("<head></head>{offer}"))
	}))
	defer php.Close()
	srv := newLandingServer(t.TempDir(), php.URL, func(id string) (config.Landing, bool) {
		if id == "7" {
			return config.Landing{ID: "7", Status: true, Local: true, Entry: "index.php"}, true
		}
		return config.Landing{}, false
	}, discardLog())
	req := httptest.NewRequest(http.MethodGet, "/g1?x=1", nil)
	rec := httptest.NewRecorder()
	srv.serveEntry(rec, req, "7", render.MacroDeps{
		CID: "0123456789", Offer: "/g1/offer?cid=0123456789",
	})
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/x" {
		t.Fatalf("location %s", loc)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<base") || !strings.Contains(body, "{offer}") {
		t.Fatalf("body %s", body)
	}
}

func TestLandingPHPAssetRedirect(t *testing.T) {
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/7/index.php" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", "https://example.com/x")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("gone"))
	}))
	defer php.Close()
	srv := newLandingServer(t.TempDir(), php.URL, nil, discardLog())
	req := httptest.NewRequest(http.MethodGet, "http://example/l/7/index.php", nil)
	rec := httptest.NewRecorder()
	srv.serveAsset(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/x" {
		t.Fatalf("location %s", loc)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html" {
		t.Fatalf("content-type %s", ct)
	}
}

func TestLandingPHPEntryHTML(t *testing.T) {
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<head></head><a href=\"{offer}\">go</a>"))
	}))
	defer php.Close()
	srv := newLandingServer(t.TempDir(), php.URL, func(id string) (config.Landing, bool) {
		if id == "7" {
			return config.Landing{ID: "7", Status: true, Local: true, Entry: "index.php"}, true
		}
		return config.Landing{}, false
	}, discardLog())
	req := httptest.NewRequest(http.MethodGet, "/g1", nil)
	rec := httptest.NewRecorder()
	srv.serveEntry(rec, req, "7", render.MacroDeps{
		CID: "0123456789", Offer: "/g1/offer?cid=0123456789",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<base href=\"/l/7/\">") {
		t.Fatalf("base missing: %s", body)
	}
	if !strings.Contains(body, "/g1/offer?cid=0123456789") || strings.Contains(body, "{offer}") {
		t.Fatalf("offer macro: %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type %s", ct)
	}
}

func TestLandingPHPDisabled(t *testing.T) {
	book := loadBook(t, "landings.json", []config.Landing{
		{ID: "7", Status: true, Local: true, Entry: "index.php"},
	}, func(l config.Landing) string { return l.ID })
	g := group(config.Stream{Name: "s", Status: true, Out: config.Output{
		Landings: []config.ShareRef{{ID: "7", Share: 1}},
	}})
	h := testEnv(t, g, func(d *engineDeps) {
		d.landings = book
		d.landServe = newLandingServer(t.TempDir(), "", func(id string) (config.Landing, bool) {
			return book.Get(id)
		}, discardLog())
	})
	rec := do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}
