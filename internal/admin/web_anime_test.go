package admin

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

// TestAnimeVendorUnderTwoPercent проверяет долю gzip вендора anime.min.js к gzip встроенной страницы,
// отдачу файла тем же путём, что у ace и datepicker, и что страница не подключает библиотеку тегом или с CDN.
// t — тест. Возврата нет. Падает, если доля gzip строго больше 2%, отдача не совпала с уже работающим
// путём статики, в HTML есть script src или CDN anime/motion, pure-блок содержит anime, из drawChart
// пропали axisScale, склейка color+'40' или разрыв pen=false, в uiViewKeys есть motion, или кадр anime пишет текст .n или .v.
func TestAnimeVendorUnderTwoPercent(t *testing.T) {
	raw, err := fs.ReadFile(vendorFS, "web/vendor/anime/anime.min.js")
	if err != nil {
		t.Fatal(err)
	}
	jsGz, err := gzipLen(raw)
	if err != nil {
		t.Fatal(err)
	}
	pageGz, err := gzipLen(indexHTML)
	if err != nil {
		t.Fatal(err)
	}
	if pageGz == 0 {
		t.Fatal("embedded index.html gzip is empty")
	}
	if share := float64(jsGz) / float64(pageGz); share > 0.02 {
		t.Fatalf("anime.min.js gzip is %.2f%% of index.html gzip, want at most 2%%", share*100)
	}

	html := spaSource()
	if strings.Count(html, "/assets/anime/anime.min.js") != 1 {
		t.Fatalf("anime asset path appears %d times, want 1", strings.Count(html, "/assets/anime/anime.min.js"))
	}
	must(t, html, "loadScript('/assets/anime/anime.min.js')", "anime loads through loadScript from vendor assets")
	mustNot(t, html, "<script src=", "no script tag in the page")
	for _, bad := range []string{
		"cdn.jsdelivr.net/npm/anime",
		"cdn.jsdelivr.net/npm/motion",
		"unpkg.com/animejs",
		"unpkg.com/motion",
	} {
		mustNot(t, html, bad, "library is not loaded from a CDN")
	}
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	mustNot(t, pure, "anime", "the pure block does not carry the animation runtime")
	if uiViewKeys["motion"] {
		t.Fatal("uiViewKeys must not carry motion")
	}
	mustNot(t, html, "ui.motion", "the page has no motion view key")

	draw := spaFunctionLine(html, "drawChart")
	must(t, draw, "axisScale(", "drawChart still scales axes with axisScale")
	must(t, draw, "color+'40'", "drawChart still builds the fill from the series colour")
	must(t, draw, "pen=false", "a null point still breaks the line")
	mustNot(t, draw, "anime(", "drawChart does not start a tween")
	mustNot(t, draw, "themeChanged(", "drawChart does not repaint the theme")
	mustNot(t, draw, "drawWires(", "drawChart does not redraw the flow wires")
	for _, name := range []string{"motionIn", "chartPlay", "barsPlay", "sparkPlay", "trendReveal"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s is missing", name)
		}
		if strings.Contains(fn, "textContent") {
			t.Errorf("%s writes text from an animation frame", name)
		}
	}
	must(t, spaFunctionLine(html, "loadBreakdown"), `<span class="n">${fmt(x.count)}</span>`, "the breakdown count is written with the row")
	must(t, spaFunctionLine(html, "dashboard"), `<div class="v">`, "the KPI value is in the card markup")

	srv, _, _ := fullServer(t, nil)
	for _, c := range []struct{ path, ctype, needle string }{
		{"/assets/anime/anime.min.js", "javascript", "anime.js v3.2.2"},
		{"/assets/anime/LICENSE", "", "MIT License"},
		{"/assets/anime/VERSION", "", "3.2.2"},
	} {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s → 200, got %d", c.path, resp.StatusCode)
		}
		if c.ctype != "" && !strings.Contains(resp.Header.Get("Content-Type"), c.ctype) {
			t.Errorf("%s: Content-Type = %q, expected %s", c.path, resp.Header.Get("Content-Type"), c.ctype)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=86400" {
			t.Errorf("%s: Cache-Control = %q, expected private, max-age=86400", c.path, cc)
		}
		if !strings.Contains(string(body), c.needle) {
			t.Errorf("%s: body does not contain %q", c.path, c.needle)
		}
	}
}

// gzipLen returns the gzip size of b.
// b is the raw file. It returns the compressed length. It returns an error when the writer fails.
func gzipLen(b []byte) (int, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return buf.Len(), nil
}
