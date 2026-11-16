package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
)

func TestReadKeysPreview(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		lines     int
		bytes     int64
		want      string
		truncated bool
	}{
		{"fits", "a\nb\n", 5, 100, "a\nb\n", false},
		{"exact lines", "a\nb\n", 2, 100, "a\nb\n", false},
		{"no final feed", "a\nb", 5, 100, "a\nb", false},
		{"line cap", "a\nb\nc\n", 2, 100, "a\nb\n", true},
		{"exact bytes", "ab\ncd\n", 5, 6, "ab\ncd\n", false},
		{"byte cap ends on a line", "ab\ncd\nef\n", 5, 7, "ab\ncd\n", true},
		{"byte cap inside the first line", "abcdef\n", 5, 3, "abc", true},
		{"empty", "", 5, 10, "", false},
	}
	for _, c := range cases {
		got, truncated, err := readKeysPreview(strings.NewReader(c.in), c.lines, c.bytes)
		if err != nil || string(got) != c.want || truncated != c.truncated {
			t.Errorf("%s: %q truncated %v err %v, want %q truncated %v", c.name, got, truncated, err, c.want, c.truncated)
		}
	}
}

func TestCountLines(t *testing.T) {
	for in, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2} {
		if got := countLines([]byte(in)); got != want {
			t.Errorf("countLines(%q) = %d, want %d", in, got, want)
		}
	}
}

// keysTestServer returns an admin server reading keyword lists from dir and a logged-in admin client.
func keysTestServer(t *testing.T, dir string) (*httptest.Server, *http.Client) {
	t.Helper()
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(),
		Stats:    fakeStats{}, Groups: &fakeGroups{}, Keys: NewFileKeys(dir),
		Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c, _ := authedClient(t, srv.URL)
	return srv, c
}

// writeKeysList stores n numbered keyword lines as the list of group g1 for 2026-06-07.
// It returns the file contents.
func writeKeysList(t *testing.T, dir string, n int, se bool) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("keyword " + strconv.Itoa(i) + "\n")
	}
	if err := os.MkdirAll(filepath.Join(dir, "g1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "g1", keysBaseName("2026-06-07", se)+".dat"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestKeysEndpointTruncatesLongList(t *testing.T) {
	dir := t.TempDir()
	full := writeKeysList(t, dir, keysPreviewLines+100, false)
	srv, c := keysTestServer(t, dir)

	resp, err := c.Get(srv.URL + "/api/keys?group=g1&date=2026-06-07")
	if err != nil {
		t.Fatal(err)
	}
	var out keysPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !out.Truncated || out.Lines != keysPreviewLines || out.MaxLines != keysPreviewLines || out.Size != int64(len(full)) {
		t.Fatalf("preview truncated %v lines %d max %d size %d", out.Truncated, out.Lines, out.MaxLines, out.Size)
	}
	if !strings.HasPrefix(full, out.Content) || !strings.HasSuffix(out.Content, "keyword "+strconv.Itoa(keysPreviewLines-1)+"\n") {
		t.Errorf("preview must be the first whole lines, ends with %q", out.Content[len(out.Content)-20:])
	}
}

func TestKeysEndpointShortListIsWhole(t *testing.T) {
	dir := t.TempDir()
	full := writeKeysList(t, dir, 3, true)
	srv, c := keysTestServer(t, dir)

	resp, err := c.Get(srv.URL + "/api/keys?group=g1&date=2026-06-07&se=1")
	if err != nil {
		t.Fatal(err)
	}
	var out keysPreview
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Truncated || out.Content != full || out.Lines != 3 {
		t.Errorf("short list %+v", out)
	}
}

func TestKeysDownloadStreamsWholeFile(t *testing.T) {
	dir := t.TempDir()
	full := writeKeysList(t, dir, keysPreviewLines+100, false)
	srv, c := keysTestServer(t, dir)

	resp, err := c.Get(srv.URL + "/api/keys/download?group=g1&date=2026-06-07")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != full || !resp.Uncompressed {
		t.Fatalf("gzip download %d, %d bytes of %d, uncompressed %v", resp.StatusCode, len(body), len(full), resp.Uncompressed)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/keys/download?group=g1&date=2026-06-07", nil)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != full {
		t.Fatalf("identity download %d, %d bytes of %d", resp.StatusCode, len(body), len(full))
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="keywords-g1-2026-06-07.txt"` {
		t.Errorf("Content-Disposition %q", cd)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(full)) {
		t.Errorf("Content-Length %q", cl)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type %q", ct)
	}
}

func TestKeysDownloadErrors(t *testing.T) {
	dir := t.TempDir()
	writeKeysList(t, dir, 1, false)
	srv, c := keysTestServer(t, dir)
	for path, want := range map[string]int{
		"/api/keys/download?group=g1&date=2026-06-08":      http.StatusNotFound,
		"/api/keys/download?group=g1&date=2026-06-07&se=1": http.StatusNotFound,
		"/api/keys/download?group=..&date=2026-06-07":      http.StatusBadRequest,
		"/api/keys/download?group=g1&date=..%2Fx":          http.StatusBadRequest,
		"/api/keys/download?date=2026-06-07":               http.StatusBadRequest,
	} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	resp, err := http.Get(srv.URL + "/api/keys/download?group=g1&date=2026-06-07")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous download: %d, want 401", resp.StatusCode)
	}
}
