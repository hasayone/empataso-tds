package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
	"github.com/emtds/bridge/ktimport"
)

func TestFilePagesRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pages")
	fp := NewFilePages(dir)
	names, err := fp.Names()
	if err != nil || len(names) != 0 {
		t.Fatalf("names before write: %v err %v", names, err)
	}
	if _, err := fp.Read("missing.html"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read: %v", err)
	}
	if err := fp.Delete("missing.html"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../x.html", "..html", "a..b.html", "noext", "a/b.html"} {
		if err := fp.Write(bad, "z"); err == nil {
			t.Fatalf("Write(%q) accepted", bad)
		}
		if _, err := fp.Read(bad); err == nil {
			t.Fatalf("Read(%q) accepted", bad)
		}
		if err := fp.Delete(bad); err == nil {
			t.Fatalf("Delete(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("directory created for a rejected name")
	}
	const body = "<p>hello</p>"
	if err := fp.Write("b.html", body); err != nil {
		t.Fatal(err)
	}
	if err := fp.Write("a.html", "A"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode %o", info.Mode().Perm())
	}
	got, err := fp.Read("b.html")
	if err != nil || got != body {
		t.Fatalf("read %q err %v", got, err)
	}
	names, err = fp.Names()
	if err != nil || len(names) != 2 || names[0].Name != "a.html" || names[0].Size != 1 || names[1].Name != "b.html" || names[1].Size != int64(len(body)) {
		t.Fatalf("names %+v err %v", names, err)
	}
	if err := fp.Delete("b.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := fp.Read("b.html"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted read: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.html")); err != nil {
		t.Fatal(err)
	}
}

func TestPageHandlers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pages")
	pages := NewFilePages(dir)
	srv := pagesServer(t, pages)
	c, csrf := authedClient(t, srv.URL)

	code, raw := pageDo(t, c, http.MethodGet, srv.URL+"/api/pages", csrf, nil)
	if code != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("empty index %d %s", code, raw)
	}
	code, raw = pageDo(t, c, http.MethodGet, srv.URL+"/api/pages/missing.html", csrf, nil)
	if code != http.StatusNotFound {
		t.Fatalf("missing %d %s", code, raw)
	}
	for _, name := range []string{"page.txt", "a..b.html"} {
		code, raw = pageDo(t, c, http.MethodGet, srv.URL+"/api/pages/"+name, csrf, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("get %s %d %s", name, code, raw)
		}
		code, raw = pageDo(t, c, http.MethodPut, srv.URL+"/api/pages/"+name, csrf, map[string]string{"content": "x"})
		if code != http.StatusBadRequest {
			t.Fatalf("put %s %d %s", name, code, raw)
		}
		code, raw = pageDo(t, c, http.MethodDelete, srv.URL+"/api/pages/"+name, csrf, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("delete %s %d %s", name, code, raw)
		}
	}
	code, raw = pageDo(t, c, http.MethodPut, srv.URL+"/api/pages/hello.html", csrf, map[string]string{"content": "<p>hi</p>"})
	if code != http.StatusOK || !strings.Contains(string(raw), `"saved"`) {
		t.Fatalf("put %d %s", code, raw)
	}
	code, raw = pageDo(t, c, http.MethodGet, srv.URL+"/api/pages/hello.html", csrf, nil)
	var page struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &page) != nil || page.Name != "hello.html" || page.Content != "<p>hi</p>" {
		t.Fatalf("get %d %s", code, raw)
	}
	code, raw = pageDo(t, c, http.MethodGet, srv.URL+"/api/pages", csrf, nil)
	var infos []PageInfo
	if code != http.StatusOK || json.Unmarshal(raw, &infos) != nil || len(infos) != 1 || infos[0].Name != "hello.html" || infos[0].Size != 9 {
		t.Fatalf("index %d %s", code, raw)
	}
	code, raw = pageDo(t, c, http.MethodDelete, srv.URL+"/api/pages/hello.html", csrf, nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"deleted"`) {
		t.Fatalf("delete %d %s", code, raw)
	}
	code, _ = pageDo(t, c, http.MethodGet, srv.URL+"/api/pages/hello.html", csrf, nil)
	if code != http.StatusNotFound {
		t.Fatalf("after delete %d", code)
	}

	bare := pagesServer(t, nil)
	c, csrf = authedClient(t, bare.URL)
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/api/pages"},
		{http.MethodGet, "/api/pages/a.html"},
		{http.MethodPut, "/api/pages/a.html"},
		{http.MethodDelete, "/api/pages/a.html"},
	} {
		var body any
		if call.method == http.MethodPut {
			body = map[string]string{"content": "x"}
		}
		code, raw = pageDo(t, c, call.method, bare.URL+call.path, csrf, body)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("nil store %s %s %d %s", call.method, call.path, code, raw)
		}
	}
}

func TestKeitaroImportPagesAndPurge(t *testing.T) {
	key := "fixture-key"
	payload := strings.Repeat("x", 600)
	fake := keitaroPageFake(t, payload)
	root := filepath.Join(t.TempDir(), "pages")
	pages := NewFilePages(root)
	if err := pages.Write("keep.html", "<p>keep</p>"); err != nil {
		t.Fatal(err)
	}
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local", Status: true}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, nil, nil)
	s.cfg.Pages = pages
	raw := postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || job.Error != "" || job.Counts["pages"] != 1 || strings.Contains(job.Error, key) {
		t.Fatalf("job %+v", job)
	}
	want := ktimport.PageName(payload)
	body, err := pages.Read(want)
	if err != nil || body != payload || want != ktimport.PageName(payload) {
		t.Fatalf("page %s len %d err %v", want, len(body), err)
	}
	if _, err := os.Stat(filepath.Join(root, want)); err != nil {
		t.Fatal(err)
	}
	saved, ok := savedGroup(groups.list, "html")
	if !ok || len(saved.Streams) != 1 || saved.Streams[0].Out.Redirect != "show_page_html" || saved.Streams[0].Out.Out != "@page:"+want {
		t.Fatalf("stream %+v", saved)
	}
	raw, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK || strings.Contains(string(raw), key) {
		t.Fatalf("purge %d %s", code, raw)
	}
	var purged struct {
		Removed struct {
			Pages int `json:"pages"`
		} `json:"removed"`
	}
	if err := json.Unmarshal(raw, &purged); err != nil {
		t.Fatal(err)
	}
	if purged.Removed.Pages != 1 {
		t.Fatalf("removed %+v", purged.Removed)
	}
	if _, err := pages.Read(want); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("imported page remains: %v", err)
	}
	kept, err := pages.Read("keep.html")
	if err != nil || kept != "<p>keep</p>" {
		t.Fatalf("keep %q err %v", kept, err)
	}
}

func TestKeitaroPagesStoreMissing(t *testing.T) {
	payload := strings.Repeat("y", 600)
	fake := keitaroPageFake(t, payload)
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local"}}}
	_, admin := newKeitaroServers(t, fake.URL, "fixture-key", groups, nil, nil)
	postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || job.Error != "" || job.Counts["pages"] != 0 {
		t.Fatalf("job %+v", job)
	}
	if !jobLoss(job, "skipped", "pages", "pages store unavailable") {
		t.Fatalf("losses %+v", job.Losses)
	}
	want := "@page:" + ktimport.PageName(payload)
	saved, ok := savedGroup(groups.list, "html")
	if !ok || len(saved.Streams) != 1 || saved.Streams[0].Out.Out != want {
		t.Fatalf("stream %+v", saved)
	}
}

func TestKeitaroPageWriteError(t *testing.T) {
	fake := keitaroPageFake(t, strings.Repeat("z", 600))
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local"}}}
	s, admin := newKeitaroServers(t, fake.URL, "fixture-key", groups, nil, nil)
	s.cfg.Pages = failPages{}
	postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "failed" || job.Error != "save error" || strings.Contains(job.Error, "fixture-key") {
		t.Fatalf("job %+v", job)
	}
	if len(groups.list) != 1 || groups.list[0].ID != "local" {
		t.Fatalf("groups %+v", groups.list)
	}
}

func TestGroupsSaveAccepts9MB(t *testing.T) {
	srv, fg, _ := fullServer(t, nil)
	c, csrf := authedClient(t, srv.URL)
	var b strings.Builder
	b.Grow((9 << 20) + 64)
	b.WriteString(`[{"id":"wide","name":"`)
	b.WriteString(strings.Repeat("n", 9<<20))
	b.WriteString(`","streams":[]}]`)
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/groups", strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		t.Fatalf("9MB groups → 200, got %d %s", resp.StatusCode, raw)
	}
	if len(fg.saved) != 1 || fg.saved[0].ID != "wide" || len(fg.saved[0].Name) != 9<<20 {
		id := ""
		n := 0
		if len(fg.saved) == 1 {
			id = fg.saved[0].ID
			n = len(fg.saved[0].Name)
		}
		t.Fatalf("saved groups=%d id=%q nameLen=%d", len(fg.saved), id, n)
	}
}

func pagesServer(t *testing.T, pages PagesStore) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &fakeGroups{}, Pages: pages, Limiter: allowAll{},
		SessionTTL: time.Hour, CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func pageDo(t *testing.T, c *http.Client, method, url, csrf string, body any) (int, []byte) {
	t.Helper()
	resp := csrfDo(t, c, method, url, csrf, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func keitaroPageFake(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	bodies := map[string]string{
		"/admin_api/v1/campaigns": `[{"id":1,"alias":"html","name":"HTML","state":"active"}]`,
		"/admin_api/v1/campaigns/1/streams": fmt.Sprintf(
			`[{"id":1,"type":"regular","name":"page","state":"active","action_type":"show_html","action_payload":%q}]`,
			payload,
		),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := bodies[r.URL.Path]
		if body == "" {
			body = "[]"
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type failPages struct{}

func (failPages) Names() ([]PageInfo, error)  { return []PageInfo{}, nil }
func (failPages) Read(string) (string, error) { return "", os.ErrNotExist }
func (failPages) Write(string, string) error  { return os.ErrPermission }
func (failPages) Delete(string) error         { return nil }
