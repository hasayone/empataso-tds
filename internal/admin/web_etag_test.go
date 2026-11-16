package admin

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// spaGet runs GET / through gzipMiddleware and securityHeaders like Handler.
// gz selects Accept-Encoding: gzip; inm is the If-None-Match value or empty.
func spaGet(t *testing.T, gz bool, inm string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if gz {
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	}
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	rec := httptest.NewRecorder()
	gzipMiddleware(securityHeaders(http.HandlerFunc(serveUI))).ServeHTTP(rec, req)
	return rec
}

// TestSPAETagIdentity checks the strong tag, no-cache and the 304 answer.
func TestSPAETagIdentity(t *testing.T) {
	first := spaGet(t, false, "")
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || etag != `"`+contentTag(indexHTML)+`"` {
		t.Fatalf("first %d etag %q", first.Code, etag)
	}
	if first.Body.Len() != len(indexHTML) || first.Header().Get("Content-Encoding") != "" {
		t.Fatalf("identity body %d bytes, encoding %q", first.Body.Len(), first.Header().Get("Content-Encoding"))
	}
	if first.Header().Get("Cache-Control") != "no-cache" || first.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("cache-control %q vary %q", first.Header().Get("Cache-Control"), first.Header().Values("Vary"))
	}
	again := spaGet(t, false, etag)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != etag {
		t.Fatalf("revalidate %d body %d etag %q", again.Code, again.Body.Len(), again.Header().Get("ETag"))
	}
	if again.Header().Get("Content-Encoding") != "" || again.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("304 headers %v", again.Header())
	}
	if stale := spaGet(t, false, `"0000"`); stale.Code != http.StatusOK || stale.Body.Len() != len(indexHTML) {
		t.Fatalf("stale tag %d body %d", stale.Code, stale.Body.Len())
	}
}

// TestSPAETagGzip checks the weak per-encoding tag, a single Vary and that
// a tag from one encoding does not revalidate the other.
func TestSPAETagGzip(t *testing.T) {
	first := spaGet(t, true, "")
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || etag != `W/"`+contentTag(indexHTML)+`-gzip"` {
		t.Fatalf("first %d etag %q", first.Code, etag)
	}
	if first.Header().Get("Content-Encoding") != "gzip" || len(first.Header().Values("Vary")) != 1 {
		t.Fatalf("encoding %q vary %q", first.Header().Get("Content-Encoding"), first.Header().Values("Vary"))
	}
	zr, err := gzip.NewReader(first.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil || string(body) != string(indexHTML) {
		t.Fatalf("gzip body %d bytes err %v", len(body), err)
	}
	again := spaGet(t, true, etag)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("Content-Encoding") != "" {
		t.Fatalf("revalidate %d body %d encoding %q", again.Code, again.Body.Len(), again.Header().Get("Content-Encoding"))
	}
	if cross := spaGet(t, true, `"`+contentTag(indexHTML)+`"`); cross.Code != http.StatusOK {
		t.Fatalf("identity tag revalidated the gzip body: %d", cross.Code)
	}
	if cross := spaGet(t, false, etag); cross.Code != http.StatusOK {
		t.Fatalf("gzip tag revalidated the identity body: %d", cross.Code)
	}
}

// TestETagMatches checks weak comparison, lists and the wildcard.
func TestETagMatches(t *testing.T) {
	cases := []struct {
		header, etag string
		want         bool
	}{
		{"", `"a"`, false},
		{`"a"`, `"a"`, true},
		{`W/"a"`, `"a"`, true},
		{`"a"`, `W/"a"`, true},
		{`"b", W/"a-gzip"`, `W/"a-gzip"`, true},
		{`"b"`, `"a"`, false},
		{"*", `"a"`, true},
		{` , `, `"a"`, false},
	}
	for _, c := range cases {
		if got := etagMatches(c.header, c.etag); got != c.want {
			t.Errorf("etagMatches(%q, %q) = %v", c.header, c.etag, got)
		}
	}
	if !strings.HasPrefix(spaGet(t, false, "*").Header().Get("ETag"), `"`) {
		t.Fatal("wildcard answer lacks the tag")
	}
}

// assetGet runs GET path through gzipMiddleware and securityHeaders like Handler.
// gz selects Accept-Encoding: gzip; inm is the If-None-Match value or empty.
func assetGet(t *testing.T, path string, gz bool, inm string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if gz {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	rec := httptest.NewRecorder()
	gzipMiddleware(securityHeaders(serveAssets())).ServeHTTP(rec, req)
	return rec
}

// TestAssetETag checks the strong content tag, the 304 answer and the cache header of a static file.
func TestAssetETag(t *testing.T) {
	const asset = "/assets/flags/ua.svg"
	raw, err := vendorFS.ReadFile("web/vendor/flags/ua.svg")
	if err != nil {
		t.Fatal(err)
	}
	want := `"` + contentTag(raw) + `"`
	first := assetGet(t, asset, false, "")
	if first.Code != http.StatusOK || first.Header().Get("ETag") != want || first.Body.Len() != len(raw) {
		t.Fatalf("first %d etag %q body %d", first.Code, first.Header().Get("ETag"), first.Body.Len())
	}
	if cc := first.Header().Get("Cache-Control"); cc != "private, max-age=86400" {
		t.Fatalf("cache-control %q", cc)
	}
	again := assetGet(t, asset, false, want)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != want {
		t.Fatalf("revalidate %d body %d etag %q", again.Code, again.Body.Len(), again.Header().Get("ETag"))
	}
	if stale := assetGet(t, asset, false, `"0000"`); stale.Code != http.StatusOK || stale.Body.Len() != len(raw) {
		t.Fatalf("stale tag %d body %d", stale.Code, stale.Body.Len())
	}
}

// TestAssetETagGzip checks the weak per-encoding tag of a static file and that tags do not cross encodings.
func TestAssetETagGzip(t *testing.T) {
	const asset = "/assets/flags/ua.svg"
	raw, err := vendorFS.ReadFile("web/vendor/flags/ua.svg")
	if err != nil {
		t.Fatal(err)
	}
	tag := contentTag(raw)
	weak := `W/"` + tag + `-gzip"`
	first := assetGet(t, asset, true, "")
	if first.Code != http.StatusOK || first.Header().Get("ETag") != weak || first.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("first %d etag %q encoding %q", first.Code, first.Header().Get("ETag"), first.Header().Get("Content-Encoding"))
	}
	if n := len(first.Header().Values("Vary")); n != 1 {
		t.Fatalf("vary %q", first.Header().Values("Vary"))
	}
	again := assetGet(t, asset, true, weak)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("Content-Encoding") != "" {
		t.Fatalf("revalidate %d body %d encoding %q", again.Code, again.Body.Len(), again.Header().Get("Content-Encoding"))
	}
	if cross := assetGet(t, asset, true, `"`+tag+`"`); cross.Code != http.StatusOK {
		t.Fatalf("identity tag revalidated the gzip body: %d", cross.Code)
	}
	if cross := assetGet(t, asset, false, weak); cross.Code != http.StatusOK {
		t.Fatalf("gzip tag revalidated the identity body: %d", cross.Code)
	}
}

// TestAssetTagsCoverEveryVendorFile requires a tag for each embedded file and no answer for a missing one.
func TestAssetTagsCoverEveryVendorFile(t *testing.T) {
	tags, err := vendorTags()
	if err != nil || len(tags) == 0 {
		t.Fatalf("tags %d err %v", len(tags), err)
	}
	if _, ok := tags["flags/ua.svg"]; !ok {
		t.Fatal("flags/ua.svg has no tag")
	}
	if rec := assetGet(t, "/assets/flags/zz.svg", false, "*"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing file answered %d", rec.Code)
	}
}
