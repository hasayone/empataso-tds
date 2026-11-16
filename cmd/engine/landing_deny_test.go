package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLandingDenied(t *testing.T) {
	cases := []struct {
		rel  string
		deny bool
	}{
		{"log.json", true},
		{"LOG.JSON", true},
		{"sub/log.json", true},
		{"error-log.json", true},
		{"php_errors.json", true},
		{"logs/today.json", true},
		{"app.log", true},
		{"error_log", true},
		{"groups.json", true},
		{"s2s.json", true},
		{"composer.json", true},
		{"composer.lock", true},
		{"package-lock.json", true},
		{"src.phps", true},
		{"config.inc", true},
		{"page.phtml", true},
		{".env", true},
		{".git/config", true},
		{".htaccess", true},
		{".user.ini", true},
		{"php.ini", true},
		{"dump.sql", true},
		{"index.php.bak", true},
		{".index.php.swp", true},
		{"index.swp", true},
		{"index.php~", true},
		{"backup.zip", true},
		{"site.tar.gz", true},
		{"vendor/autoload.php", true},
		{"Vendor/lib.js", true},
		{"node_modules/x/index.js", true},
		{"style.css", false},
		{"files/main(2).js", false},
		{"img/logo.png", false},
		{"icon.svg", false},
		{"fonts/r.woff2", false},
		{"fonts/r.ttf", false},
		{"data.json", false},
		{"blog.json", false},
		{"i18n/ru.json", false},
		{"manifest.json", false},
		{"index.php", false},
		{"index.html", false},
		{"catalog.js", false},
	}
	for _, c := range cases {
		if got := landingDenied(c.rel); got != c.deny {
			t.Errorf("landingDenied(%q) = %v, want %v", c.rel, got, c.deny)
		}
	}
}

// writeLandingFiles creates files under dir/7, each holding its own name and SECRET.
func writeLandingFiles(t *testing.T, dir string, names []string) {
	t.Helper()
	for _, n := range names {
		full := filepath.Join(dir, "7", filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("SECRET "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLandingAssetDenyAndAllow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "groups.json"), []byte("SECRET root"), 0o644); err != nil {
		t.Fatal(err)
	}
	denied := []string{
		"log.json", "error-log.json", "app.log", "groups.json", "s2s.json", "composer.json",
		"composer.lock", "src.phps", "config.inc", "dump.sql", "index.php.bak", "index.php~",
		"backup.zip", ".env", ".htaccess", ".user.ini", ".git/config", "vendor/lib.js",
	}
	allowed := map[string]string{
		"style.css":     "text/css",
		"app.js":        "javascript",
		"img/logo.png":  "image/png",
		"icon.svg":      "image/svg+xml",
		"fonts/r.woff2": "font/woff2",
		"i18n/ru.json":  "application/json",
		"blog.json":     "application/json",
	}
	names := append([]string{}, denied...)
	for n := range allowed {
		names = append(names, n)
	}
	writeLandingFiles(t, dir, names)
	var phpHits atomic.Int32
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		phpHits.Add(1)
		_, _ = w.Write([]byte("SECRET php"))
	}))
	defer php.Close()
	srv := newLandingServer(dir, php.URL, nil, discardLog())
	mux := http.NewServeMux()
	mux.HandleFunc("/l/", srv.serveAsset)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example"+target, nil))
		return rec
	}
	missing := get("/l/7/absent.json")
	for _, n := range append(denied, "error_log", "vendor/autoload.php", "VENDOR/x") {
		rec := get("/l/7/" + n)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("%s: status %d body %q", n, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != missing.Body.String() {
			t.Errorf("%s: body %q differs from a missing file %q", n, rec.Body.String(), missing.Body.String())
		}
	}
	if n := phpHits.Load(); n != 0 {
		t.Fatalf("denied names reached PHP %d times", n)
	}
	for n, ct := range allowed {
		rec := get("/l/7/" + n)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), n) {
			t.Errorf("%s: status %d body %q", n, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, ct) {
			t.Errorf("%s: content-type %q, want %q", n, got, ct)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "7", "index.php"), []byte("<?php echo 'ok'; ?>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := get("/l/7/index.php"); rec.Code != http.StatusOK || phpHits.Load() != 1 {
		t.Fatalf("index.php: status %d php hits %d", rec.Code, phpHits.Load())
	}
}

func TestLandingAssetExtAllowList(t *testing.T) {
	dir := t.TempDir()
	var phpHits atomic.Int32
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		phpHits.Add(1)
		_, _ = w.Write([]byte("php"))
	}))
	defer php.Close()
	srv := newLandingServer(dir, php.URL, nil, discardLog())
	mux := http.NewServeMux()
	mux.HandleFunc("/l/", srv.serveAsset)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example"+target, nil))
		return rec
	}
	// Files that must be 404 without hitting PHP, regardless of content.
	blocked := []string{
		"/l/1/leads.txt",
		"/l/1/orders.csv",
		"/l/1/users.xml",
	}
	missing := get("/l/1/absent.bin")
	for _, p := range blocked {
		rec := get(p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
		if rec.Body.String() != missing.Body.String() {
			t.Errorf("%s: body %q differs from a missing file", p, rec.Body.String())
		}
	}

	// leads.json and orders.csv without files on disk — denied by name or allow-list.
	for _, p := range []string{"/l/1/leads.json", "/l/1/orders.csv"} {
		if rec := get(p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
	if phpHits.Load() != 0 {
		t.Fatalf("blocked extensions reached PHP %d times", phpHits.Load())
	}

	// README (no extension, no index.php on disk) → 404.
	if rec := get("/l/40/README"); rec.Code != http.StatusNotFound {
		t.Errorf("/l/40/README: status %d, want 404", rec.Code)
	}

	// nope.php without the file on disk → 404, no PHP hit.
	if rec := get("/l/40/nope.php"); rec.Code != http.StatusNotFound {
		t.Errorf("/l/40/nope.php: status %d, want 404", rec.Code)
	}
	if phpHits.Load() != 0 {
		t.Fatalf("missing .php reached PHP %d times", phpHits.Load())
	}

	// Allowed static extensions (files do not exist — still 404 from missing file, not from allow-list).
	// Just verify the response body matches a plain missing-file body (no "forbidden" message).
	for _, p := range []string{"/l/1/index.html", "/l/1/app.js", "/l/1/logo.png", "/l/1/data.json"} {
		rec := get(p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

// TestLandingAssetExtAllowListHit checks that existing files with allowed extensions are served.
func TestLandingAssetExtAllowListHit(t *testing.T) {
	dir := t.TempDir()
	land := filepath.Join(dir, "1")
	if err := os.Mkdir(land, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "app.js", "logo.png", "data.json"} {
		if err := os.WriteFile(filepath.Join(land, name), []byte("content-"+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newLandingServer(dir, "", nil, discardLog())
	mux := http.NewServeMux()
	mux.HandleFunc("/l/", srv.serveAsset)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example"+target, nil))
		return rec
	}
	for _, name := range []string{"index.html", "app.js", "logo.png", "data.json"} {
		rec := get("/l/1/" + name)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", name, rec.Code)
		}
	}
}

// TestLandingAssetNoExtRedirect checks that a path without extension redirects to index.php
// when that file exists, and returns 404 otherwise.
func TestLandingAssetNoExtRedirect(t *testing.T) {
	dir := t.TempDir()
	land40 := filepath.Join(dir, "40")
	if err := os.MkdirAll(filepath.Join(land40, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(land40, "files", "index.php"), []byte("<?php ?>"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newLandingServer(dir, "", nil, discardLog())
	mux := http.NewServeMux()
	mux.HandleFunc("/l/", srv.serveAsset)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example"+target, nil))
		return rec
	}

	// /l/40/files with files/index.php on disk → 301 to /l/40/files/index.php.
	rec := get("/l/40/files")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/l/40/files: status %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/l/40/files/index.php" {
		t.Fatalf("/l/40/files: Location %q, want /l/40/files/index.php", loc)
	}

	// Query is preserved in the redirect.
	rec = get("/l/40/files?x=1&y=2")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/l/40/files?x=1: status %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/l/40/files/index.php?x=1&y=2" {
		t.Fatalf("/l/40/files?x=1: Location %q", loc)
	}

	// /l/40/README with no index.php → 404.
	if rec := get("/l/40/README"); rec.Code != http.StatusNotFound {
		t.Fatalf("/l/40/README: status %d, want 404", rec.Code)
	}
}

// TestLandingPHPLocationRewrite checks that proxyPHP rewrites Location headers
// from the PHP sidecar into engine-relative paths, leaving external URLs alone.
func TestLandingPHPLocationRewrite(t *testing.T) {
	cases := []struct {
		name     string
		phpURL   string
		id       string
		location string
		want     string
	}{
		{
			name:     "absolute php host no port",
			phpURL:   "http://php:80",
			id:       "40",
			location: "http://php/40/x/",
			want:     "/l/40/x/",
		},
		{
			name:     "absolute php host with port",
			phpURL:   "http://php:80",
			id:       "40",
			location: "http://php:80/40/x/",
			want:     "/l/40/x/",
		},
		{
			name:     "relative path",
			phpURL:   "http://php:80",
			id:       "40",
			location: "/40/x/",
			want:     "/l/40/x/",
		},
		{
			name:     "external absolute stays",
			phpURL:   "http://php:80",
			id:       "40",
			location: "https://example.com/40/x/",
			want:     "https://example.com/40/x/",
		},
		{
			name:     "relative with query",
			phpURL:   "http://php:80",
			id:       "40",
			location: "/40/x/?foo=bar",
			want:     "/l/40/x/?foo=bar",
		},
	}
	for _, c := range cases {
		if got := rewritePHPLocation(c.location, c.phpURL, c.id); got != c.want {
			t.Errorf("[%s] rewritePHPLocation(%q, %q, %q) = %q, want %q",
				c.name, c.location, c.phpURL, c.id, got, c.want)
		}
	}
}

func TestLandingAssetTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "groups.json"), []byte("SECRET root"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeLandingFiles(t, dir, []string{"sub/style.css"})
	srv := newLandingServer(dir, "", nil, discardLog())
	mux := http.NewServeMux()
	mux.HandleFunc("/l/", srv.serveAsset)
	targets := []string{
		"/l/7/../groups.json",
		"/l/7/%2e%2e/groups.json",
		"/l/7/%2E%2E/groups.json",
		"/l/7/%252e%252e/groups.json",
		"/l/7/..%2fgroups.json",
		"/l/7/..%5cgroups.json",
		"/l/7/sub%5c..%5c..%5cgroups.json",
		"/l/7/sub/../../groups.json",
		"/l/..%2f7/sub/style.css",
		"/l/7/%00groups.json",
		"/l/7//groups.json",
	}
	for _, target := range targets {
		req := httptest.NewRequest(http.MethodGet, "http://example"+target, nil)
		for name, h := range map[string]http.Handler{"mux": mux, "direct": http.HandlerFunc(srv.serveAsset)} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "SECRET") {
				t.Errorf("%s %s: status %d body %q", name, target, rec.Code, rec.Body.String())
			}
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example/l/7/sub/style.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("legal asset status %d", rec.Code)
	}
}
