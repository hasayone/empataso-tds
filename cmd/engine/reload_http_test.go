package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// countBook counts Reload calls.
type countBook struct{ n int }

func (b *countBook) Reload() error { b.n++; return nil }

// replaceGroupsFile replaces path the way the admin does: a temporary file renamed over it.
// The new file gets the size and modification time of the old one's, the worst case for a
// fingerprint made of those two alone.
func replaceGroupsFile(t *testing.T, path, body string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if pad := int(st.Size()) - len(body); pad > 0 {
		body += strings.Repeat(" ", pad)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.Stat(path); now.Size() != st.Size() || !now.ModTime().Equal(st.ModTime()) {
		t.Fatal("the replacement must keep size and mtime for this test")
	}
}

func postReload(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/reload", nil))
	return rec
}

// TestReloadHandlerSwapsAtOnce renames a campaign in the file with a replacement of the same size
// and mtime, then asks the engine to reload: the new id opens, the old id is gone, with no timer tick.
func TestReloadHandlerSwapsAtOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	writeGroups(t, path, `[{"id":"aaaa","status":true,"redirect":"http_redirect","out":"https://kt.example/x"}]`)
	groups, err := config.LoadGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newWatcherFor(t, dir, path, groups)
	book := &countBook{}
	h := reloadHandler(w, book)

	rec := postReload(h)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"groups":false`) {
		t.Fatalf("untouched file: %d %s", rec.Code, rec.Body)
	}

	replaceGroupsFile(t, path, `[{"id":"bbbb","status":true,"redirect":"show_text","out":"NEW-ROUTE-"}]`)
	rec = postReload(h)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"groups":true`) {
		t.Fatalf("replaced file: %d %s", rec.Code, rec.Body)
	}
	if _, ok := groups.Get("aaaa"); ok {
		t.Error("the replaced id still opens a campaign")
	}
	if g, ok := groups.Get("bbbb"); !ok || g.Out != "NEW-ROUTE-" {
		t.Errorf("the new id after reload: %+v ok=%v", g, ok)
	}
	if book.n != 2 {
		t.Errorf("catalogs reloaded %d times, want 2", book.n)
	}
}

// TestReloadHandlerKeepsConfigOnBadFile checks that a broken file is 500 without the path, and the
// serving set stays.
func TestReloadHandlerKeepsConfigOnBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	writeGroups(t, path, `[{"id":"promo","status":true}]`)
	groups, _ := config.LoadGroups(path)
	w := newWatcherFor(t, dir, path, groups)

	writeGroups(t, path, `{ not json`)
	rec := postReload(reloadHandler(w))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), dir) {
		t.Fatalf("bad file: %d %s", rec.Code, rec.Body)
	}
	if _, ok := groups.Get("promo"); !ok {
		t.Error("the serving set was dropped")
	}
}

// TestReloadHandlerMethodAndDemo checks that only POST is accepted and that a missing watcher answers 200.
func TestReloadHandlerMethodAndDemo(t *testing.T) {
	h := reloadHandler(nil)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/internal/reload", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d, want 405", m, rec.Code)
		}
	}
	if rec := postReload(h); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"groups":false`) {
		t.Fatalf("demo engine: %d %s", rec.Code, rec.Body)
	}
}
