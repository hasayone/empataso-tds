package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
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
