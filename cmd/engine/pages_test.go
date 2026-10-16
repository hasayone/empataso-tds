package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

func TestPagesDir(t *testing.T) {
	if got := pagesDir("/var/lib/kuztds/pages", "/var/lib/kuztds/groups.json"); got != "/var/lib/kuztds/pages" {
		t.Fatalf("explicit: %q", got)
	}
	want := filepath.Join(filepath.Dir("/var/lib/kuztds/groups.json"), "pages")
	if got := pagesDir("", "/var/lib/kuztds/groups.json"); got != want {
		t.Fatalf("default: %q", got)
	}
	if pagesDir("", "") != "" {
		t.Fatal("empty groups file should not invent a directory")
	}
}

func TestPageCacheValidAndInvalid(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "pages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.html"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.html"), []byte("OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.b_c-1.html"), []byte("NEST"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	c := newPageCache(dir, slog.New(slog.NewTextHandler(&buf, nil)))

	body, ok := c.Get("ok.html")
	if !ok || body != "OK" {
		t.Fatalf("ok.html: %q %v", body, ok)
	}
	body, ok = c.Get("a.b_c-1.html")
	if !ok || body != "NEST" {
		t.Fatalf("dotted name: %q %v", body, ok)
	}
	for _, name := range []string{
		"", "../secret.html", "..html", "foo..html", "a/b.html", "p.txt",
		"p.HTML", "p.html ", "@page:ok.html", "ok.html/x",
	} {
		if body, ok = c.Get(name); ok || body != "" {
			t.Errorf("invalid %q returned %q", name, body)
		}
	}
	if strings.Contains(buf.String(), "page file missing") {
		t.Fatalf("invalid names were warned: %s", buf.String())
	}
	if body, ok = c.Get("../secret.html"); ok || strings.Contains(body, "SECRET") {
		t.Fatalf("path escaped: %q", body)
	}
}

func TestPageCacheMtimeChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.html")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newPageCache(dir, discardLog())
	if body, ok := c.Get("p.html"); !ok || body != "one" {
		t.Fatalf("first: %q %v", body, ok)
	}
	if err := os.WriteFile(path, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if body, ok := c.Get("p.html"); !ok || body != "one" {
		t.Fatalf("inside 5s window: %q %v", body, ok)
	}
	c.mu.Lock()
	ent := c.items["p.html"]
	ent.checked = time.Now().Add(-pageStatInterval - time.Second)
	c.items["p.html"] = ent
	c.mu.Unlock()
	if body, ok := c.Get("p.html"); !ok || body != "two" {
		t.Fatalf("after mtime change: %q %v", body, ok)
	}

	if err := os.WriteFile(path, []byte("six"), 0o644); err != nil {
		t.Fatal(err)
	}
	later = time.Now().Add(6 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if body, ok := c.Get("p.html"); !ok || body != "two" {
		t.Fatalf("same-size edit still cached: %q %v", body, ok)
	}
	c.mu.Lock()
	ent = c.items["p.html"]
	ent.checked = time.Now().Add(-pageStatInterval - time.Second)
	c.items["p.html"] = ent
	c.mu.Unlock()
	if body, ok := c.Get("p.html"); !ok || body != "six" {
		t.Fatalf("same-size mtime change: %q %v", body, ok)
	}
}

func TestPageCacheMissingFileFalseOnceWarned(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	c := newPageCache(dir, slog.New(slog.NewTextHandler(&buf, nil)))
	if body, ok := c.Get("gone.html"); ok || body != "" {
		t.Fatalf("missing: %q %v", body, ok)
	}
	if _, ok := c.Get("gone.html"); ok {
		t.Fatal("cached miss returned true")
	}
	c.mu.Lock()
	ent := c.items["gone.html"]
	ent.checked = time.Now().Add(-pageStatInterval - time.Second)
	c.items["gone.html"] = ent
	c.mu.Unlock()
	if body, ok := c.Get("gone.html"); ok || body != "" {
		t.Fatalf("still missing after restat: %q %v", body, ok)
	}
	if n := strings.Count(buf.String(), "page file missing"); n != 1 {
		t.Fatalf("warn count %d\n%s", n, buf.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.html"), []byte("back"), 0o644); err != nil {
		t.Fatal(err)
	}
	if body, ok := c.Get("gone.html"); ok || body != "" {
		t.Fatalf("appeared inside the stat window: %q %v", body, ok)
	}
	c.mu.Lock()
	ent = c.items["gone.html"]
	ent.checked = time.Now().Add(-pageStatInterval - time.Second)
	c.items["gone.html"] = ent
	c.mu.Unlock()
	if body, ok := c.Get("gone.html"); !ok || body != "back" {
		t.Fatalf("appeared after the stat window: %q %v", body, ok)
	}
	if n := strings.Count(buf.String(), "page file missing"); n != 1 {
		t.Fatalf("warn count after appear %d\n%s", n, buf.String())
	}
	if _, ok := c.Get(""); ok {
		t.Fatal("empty name")
	}
	if n := strings.Count(buf.String(), "page file missing"); n != 1 {
		t.Fatalf("invalid name warned, count %d", n)
	}
}

func TestPageFileExpandsSubID(t *testing.T) {
	var dir string
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_html_404", Out: " @page:p1.html "}})
	h := testEnv(t, gg, func(d *engineDeps) {
		dir = d.dataDir
		d.pages = newPageCache(filepath.Join(d.dataDir, "pages"), d.log)
	})
	pages := filepath.Join(dir, "pages")
	if err := os.MkdirAll(pages, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pages, "p1.html"), []byte("<h1>{subid}</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, "/g1", "8.8.8.8", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "{subid}") || strings.Contains(body, "@page:") {
		t.Fatalf("body %q", body)
	}
	if !regexp.MustCompile(`<h1>[0-9a-f]{10}</h1>`).MatchString(body) {
		t.Fatalf("expanded subid missing: %q", body)
	}
	if rec.Header().Get("X-Kuztds-Stream") != "s" {
		t.Fatalf("stream %q", rec.Header().Get("X-Kuztds-Stream"))
	}
}

func TestPageFileMissKeepsRedirect(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_html_404", Out: "@page:missing.html"}})
	h := testEnv(t, gg, func(d *engineDeps) {
		d.pages = newPageCache(filepath.Join(d.dataDir, "pages"), d.log)
	})
	rec := do(t, h, "/g1", "8.8.8.8", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if rec.Body.String() != "" {
		t.Fatalf("body %q", rec.Body.String())
	}
	if rec.Header().Get("X-Kuztds-Stream") != "s" {
		t.Fatalf("stream %q", rec.Header().Get("X-Kuztds-Stream"))
	}
}

func TestPageBodyDoesNotTakeOfferQuery(t *testing.T) {
	offers := loadBook(t, "offers.json", []config.Offer{{
		ID: "o1", URL: "@page:p1.html", Status: true, NetworkID: "n1",
	}}, func(o config.Offer) string { return o.ID })
	networks := loadBook(t, "networks.json", []config.AffiliateNetwork{{
		ID: "n1", Status: true, OfferQuery: "injected=1",
	}}, func(n config.AffiliateNetwork) string { return n.ID })
	var dir string
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop",
		Streams: []config.Stream{{
			Name: "s", Status: true,
			Out: config.Output{
				Redirect: "show_text",
				Out:      "@page:p1.html",
				Offers:   []config.ShareRef{{ID: "o1", Share: 1}},
			},
		}},
	})
	h := testEnv(t, g, func(d *engineDeps) {
		dir = d.dataDir
		d.offers = offers
		d.networks = networks
		d.pages = newPageCache(filepath.Join(d.dataDir, "pages"), d.log)
	})
	pages := filepath.Join(dir, "pages")
	if err := os.MkdirAll(pages, 0o755); err != nil {
		t.Fatal(err)
	}
	const page = "see https://x.test/a"
	if err := os.WriteFile(filepath.Join(pages, "p1.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, "/g1", "8.8.8.8", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Body.String(); got != page {
		t.Fatalf("body %q", got)
	}
}
