package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnzipStripsTopAndPicksEntry(t *testing.T) {
	files := NewFileLandings(t.TempDir())
	if err := files.Write("701", "old.txt", []byte("old")); err != nil {
		t.Fatal(err)
	}
	n, err := files.Unzip("701", zipBytes(t, map[string]string{
		"site/index.php":  "<?php echo 1;",
		"site/a.html":     "hi",
		"site/__MACOSX/x": "skip",
		"__MACOSX/y":      "skip",
	}))
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := files.Read("701", "old.txt"); !os.IsNotExist(err) {
		t.Fatal("old file kept")
	}
	got, err := files.Read("701", "index.php")
	if err != nil || string(got) != "<?php echo 1;" {
		t.Fatalf("index %q %v", got, err)
	}
	if _, err := files.Read("701", "site/index.php"); err == nil {
		t.Fatal("top dir kept")
	}
	if files.Entry("701") != "index.php" {
		t.Fatal(files.Entry("701"))
	}
	if _, err := files.Unzip("702", zipBytes(t, map[string]string{
		"wrap/page.html": "p",
		"wrap/note.txt":  "n",
	})); err != nil {
		t.Fatal(err)
	}
	if files.Entry("702") != "page.html" {
		t.Fatal(files.Entry("702"))
	}
	if _, err := files.Unzip("703", zipBytes(t, map[string]string{
		"index.html": "h",
		"index.php":  "p",
	})); err != nil {
		t.Fatal(err)
	}
	if files.Entry("703") != "index.php" {
		t.Fatal(files.Entry("703"))
	}
}

func TestUnzipRejectsSlip(t *testing.T) {
	dir := t.TempDir()
	files := NewFileLandings(filepath.Join(dir, "landings"))
	if _, err := files.Unzip("701", zipBytes(t, map[string]string{"../x": "no"})); err == nil {
		t.Fatal("slip accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); !os.IsNotExist(err) {
		t.Fatal("file escaped")
	}
	if _, err := os.Stat(filepath.Join(dir, "landings", "701")); !os.IsNotExist(err) {
		t.Fatal("dir created for a rejected archive")
	}
	if _, err := files.Unzip("701", zipBytes(t, map[string]string{"ok/../../x": "no"})); err == nil {
		t.Fatal("nested slip accepted")
	}
	if _, err := files.Unzip(".", zipBytes(t, map[string]string{"index.html": "x"})); err == nil {
		t.Fatal("dot id accepted")
	}
	if _, err := files.Unzip("..", zipBytes(t, map[string]string{"index.html": "x"})); err == nil {
		t.Fatal("dotdot id accepted")
	}
}

func TestLandingArchiveUpdatesEntryAndDeleteRemovesDir(t *testing.T) {
	dir := t.TempDir()
	landings := NewJSONStore[config.Landing](filepath.Join(dir, "landings.json"))
	if err := landings.Save(context.Background(), []config.Landing{{
		ID: "701", Name: "Local", Local: true, Status: true,
	}}); err != nil {
		t.Fatal(err)
	}
	files := NewFileLandings(filepath.Join(dir, "files"))
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		Landings: landings, LandingFiles: files,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	raw, code := landingDo(t, c, http.MethodPost, srv.URL+"/api/landings/701/archive", csrf, "application/zip", zipBytes(t, map[string]string{
		"pack/index.php": "<?php",
		"pack/a.css":     "a{}",
	}))
	if code != http.StatusOK {
		t.Fatalf("archive %d %s", code, raw)
	}
	var got struct {
		Files int    `json:"files"`
		Entry string `json:"entry"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Files != 2 || got.Entry != "index.php" {
		t.Fatalf("response %s %v", raw, err)
	}
	list, err := landings.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Entry != "index.php" {
		t.Fatalf("catalog %+v %v", list, err)
	}
	raw, code = landingDo(t, c, http.MethodPut, srv.URL+"/api/landings/701/files/note.txt", csrf, "text/plain", []byte("hello"))
	if code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	raw, code = landingDo(t, c, http.MethodGet, srv.URL+"/api/landings/701/files/note.txt", csrf, "", nil)
	if code != http.StatusOK || string(raw) != "hello" {
		t.Fatalf("get %d %s", code, raw)
	}
	_, code = landingDo(t, c, http.MethodPut, srv.URL+"/api/landings/701/files/.env", csrf, "text/plain", []byte("x"))
	if code != http.StatusBadRequest {
		t.Fatalf("hidden %d", code)
	}
	raw, code = landingMultipart(t, c, srv.URL+"/api/landings/701/archive", csrf, zipBytes(t, map[string]string{
		"only.html": "h",
	}))
	if code != http.StatusOK {
		t.Fatalf("multipart %d %s", code, raw)
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Entry != "only.html" {
		t.Fatalf("multipart body %s", raw)
	}
	list, err = landings.List(context.Background())
	if err != nil || list[0].Entry != "only.html" {
		t.Fatalf("entry %+v %v", list, err)
	}
	raw, code = landingDo(t, c, http.MethodDelete, srv.URL+"/api/landings/701", csrf, "", nil)
	if code != http.StatusOK {
		t.Fatalf("delete %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "files", "701")); !os.IsNotExist(err) {
		t.Fatal("directory remains")
	}
	list, err = landings.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("catalog after delete %+v %v", list, err)
	}
}

// TestLandingFilesListRereadsSize checks that GET /api/landings/{id}/files shows the size
// and change time from disk after an archive, an editor save and a write from outside
// the admin, and that the answer is never cached.
func TestLandingFilesListRereadsSize(t *testing.T) {
	dir := t.TempDir()
	landings := NewJSONStore[config.Landing](filepath.Join(dir, "landings.json"))
	if err := landings.Save(context.Background(), []config.Landing{{ID: "702", Name: "Local", Local: true, Status: true}}); err != nil {
		t.Fatal(err)
	}
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour,
		Landings: landings, LandingFiles: NewFileLandings(filepath.Join(dir, "files")),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	listURL := srv.URL + "/api/landings/702/files"
	upload := func(step string, files map[string]string) {
		t.Helper()
		if raw, code := landingDo(t, c, http.MethodPost, srv.URL+"/api/landings/702/archive", csrf, "application/zip", zipBytes(t, files)); code != http.StatusOK {
			t.Fatalf("%s: %d %s", step, code, raw)
		}
	}
	upload("archive", map[string]string{"index.php": "<?php", "log.json": "[1,2,3,4,5]"})
	if f := listedFile(t, c, listURL, "log.json"); f.Size != 11 || f.Modified.IsZero() || f.Modified.Location() != time.UTC {
		t.Fatalf("after archive %+v", f)
	}
	if raw, code := landingDo(t, c, http.MethodPut, srv.URL+"/api/landings/702/files/log.json", csrf, "text/plain", []byte("[]")); code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	if f := listedFile(t, c, listURL, "log.json"); f.Size != 2 {
		t.Fatalf("after editor save %+v", f)
	}
	outside := filepath.Join(dir, "files", "702", "log.json")
	later := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := os.WriteFile(outside, []byte("[1,2,3,4,5,6,7,8,9]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outside, later, later); err != nil {
		t.Fatal(err)
	}
	if f := listedFile(t, c, listURL, "log.json"); f.Size != 19 || !f.Modified.Equal(later) {
		t.Fatalf("after outside write %+v, want time %v", f, later)
	}
	upload("second archive", map[string]string{"index.php": "<?php", "log.json": "{}"})
	if f := listedFile(t, c, listURL, "log.json"); f.Size != 2 {
		t.Fatalf("after second archive %+v", f)
	}
}

// listedFile reads one file row from a landing file list and checks the list is not cacheable.
// c is a signed-in client, url the list route, path the file to return.
// It fails the test on a request error, a cache header other than private no-store,
// a validator header, or a missing path.
func listedFile(t *testing.T, c *http.Client, url, path string) FileInfo {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); cc != "private, no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if resp.Header.Get("ETag") != "" || resp.Header.Get("Last-Modified") != "" {
		t.Fatal("the file list carries a cache validator")
	}
	var list []FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, f := range list {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s missing in %+v", path, list)
	return FileInfo{}
}

func landingDo(t *testing.T, c *http.Client, method, url, csrf, contentType string, body []byte) ([]byte, int) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw, resp.StatusCode
}

func landingMultipart(t *testing.T, c *http.Client, url, csrf string, payload []byte) ([]byte, int) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "land.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return landingDo(t, c, http.MethodPost, url, csrf, mw.FormDataContentType(), buf.Bytes())
}

// corruptZip returns a stored zip of two files in order whose second entry has a wrong
// checksum: the first reads fine, the second fails with zip.ErrChecksum.
func corruptZip(t *testing.T, first, second string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{{"index.html", first}, {"page.html", second}} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f[0], Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, f[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	at := bytes.Index(raw, []byte("SECOND-BODY"))
	if at < 0 {
		t.Fatal("second body not found in the archive")
	}
	raw[at] ^= 0xFF
	return raw
}

func TestUnzipCorruptEntryLeavesDirectoryUntouched(t *testing.T) {
	dir := t.TempDir()
	files := NewFileLandings(dir)
	if err := files.Write("705", "old.txt", []byte("old")); err != nil {
		t.Fatal(err)
	}
	_, err := files.Unzip("705", corruptZip(t, "<p>[CID]</p>", "SECOND-BODY"))
	if !errors.Is(err, errLandingArchive) {
		t.Fatalf("corrupt entry: %v", err)
	}
	got, err := files.Read("705", "old.txt")
	if err != nil || string(got) != "old" {
		t.Fatalf("old file after failure: %q, %v", got, err)
	}
	if _, err := files.Read("705", "index.html"); err == nil {
		t.Error("a file of the failed archive was left in the landing")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "705" {
		t.Errorf("staging directory left behind: %v", entries)
	}
	if _, err := files.Unzip("705", zipBytes(t, map[string]string{"new.html": "n"})); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read("705", "old.txt"); err == nil {
		t.Error("old file survived a successful replacement")
	}
	if entries, _ = os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("staging or backup left after success: %v", entries)
	}
}

func TestUnzipKeepsTheModesPHPNeeds(t *testing.T) {
	dir := t.TempDir()
	files := NewFileLandings(dir)
	if _, err := files.Unzip("706", zipBytes(t, map[string]string{"index.php": "<?php", "sub/a.css": "a"})); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"706", "706/sub"} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o700 != 0o700 || perm&0o005 != 0o005 {
			t.Errorf("directory %s is %v: the web server user must be able to enter it", rel, perm)
		}
	}
	for _, rel := range []string{"706/index.php", "706/sub/a.css"} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("file %s is %v, want 0644", rel, perm)
		}
	}
}

func TestUnzipRefusesAHiddenID(t *testing.T) {
	files := NewFileLandings(t.TempDir())
	if _, err := files.Unzip(".unzip-1", zipBytes(t, map[string]string{"index.html": "x"})); err == nil {
		t.Error("an id with a leading dot was accepted")
	}
	if err := files.Write(".hidden", "a.txt", []byte("x")); err == nil {
		t.Error("a file was written into a hidden landing")
	}
	if _, err := files.Unzip("kt-ip.1_a", zipBytes(t, map[string]string{"index.html": "x"})); err != nil {
		t.Errorf("an id with dot, dash and underscore inside: %v", err)
	}
}

func TestRecoverUnzip(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, age time.Duration) {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "f.txt"), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(full, when, when); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	mk(".unzip-710-123.old", time.Minute)
	mk(".unzip-710-123", time.Minute)
	mk(".unzip-a-b-9.old", 3*time.Hour)
	mk(".unzip-711-5", 2*time.Hour)
	mk(".unzip-712-6", time.Minute)
	mk("713", time.Minute)
	mk(".unzip-713-7.old", 2*time.Hour)
	mk(".unzip-nodash", 2*time.Hour)
	mk("714", 5*time.Hour)

	restored, removed, err := NewFileLandings(dir).RecoverUnzip(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if restored != 2 || removed != 2 {
		t.Errorf("restored %d removed %d", restored, removed)
	}
	if !exists("710/f.txt") || exists(".unzip-710-123.old") {
		t.Error("the backup of a landing with no directory was not put back")
	}
	if !exists(".unzip-710-123") {
		t.Error("a fresh staging directory was deleted")
	}
	if !exists("a-b/f.txt") {
		t.Error("an id with a dash was not recovered")
	}
	if exists(".unzip-711-5") {
		t.Error("a stale staging directory was kept")
	}
	if !exists(".unzip-712-6") {
		t.Error("a fresh staging directory was deleted")
	}
	if !exists("713/f.txt") || exists(".unzip-713-7.old") {
		t.Error("a stale backup beside an existing landing was not deleted")
	}
	if !exists(".unzip-nodash") || !exists("714/f.txt") {
		t.Error("an unrelated directory was touched")
	}
	if _, _, err := NewFileLandings(filepath.Join(dir, "absent")).RecoverUnzip(time.Hour); err != nil {
		t.Errorf("missing landings directory: %v", err)
	}
}
