package admin

import (
	"net/http"
	"strings"
	"testing"
)

// TestServeAssets — встроенная статика /assets/ доступна без сессии, с кешем; листинг и отсутствующие файлы — 404.
func TestServeAssets(t *testing.T) {
	srv, _, _ := fullServer(t, nil)

	resp, err := http.Get(srv.URL + "/assets/ace/ace.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/ace/ace.js → 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, expected javascript", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q, expected max-age", cc)
	}

	resp404, err := http.Get(srv.URL + "/assets/nope.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /assets/nope.js → 404, got %d", resp404.StatusCode)
	}

	respDir, err := http.Get(srv.URL + "/assets/ace/")
	if err != nil {
		t.Fatal(err)
	}
	defer respDir.Body.Close()
	if respDir.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /assets/ace/ → 404, got %d", respDir.StatusCode)
	}
}

// TestServeAssetsFlags — встроенные SVG-флаги /assets/flags/ доступны без сессии, с кешем; отсутствующий код — 404.
func TestServeAssetsFlags(t *testing.T) {
	srv, _, _ := fullServer(t, nil)

	resp, err := http.Get(srv.URL + "/assets/flags/ua.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/flags/ua.svg → 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "svg") {
		t.Errorf("Content-Type = %q, expected svg", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q, expected max-age", cc)
	}

	resp404, err := http.Get(srv.URL + "/assets/flags/zz.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /assets/flags/zz.svg → 404, got %d", resp404.StatusCode)
	}
}

// TestIndexHTMLUsesEmbeddedFlags — SPA ссылается на /assets/flags/, без встроенного FLAG_SPEC.
func TestIndexHTMLUsesEmbeddedFlags(t *testing.T) {
	html := spaSource()
	if !strings.Contains(html, "/assets/flags/") {
		t.Error("indexHTML must reference /assets/flags/")
	}
	if strings.Contains(html, "FLAG_SPEC") {
		t.Error("indexHTML must not contain FLAG_SPEC")
	}
}
