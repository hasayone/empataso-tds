package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/security"
)

func TestKeitaroImportUsers(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	dir := t.TempDir()
	adminSrv := newKeitaroAdmin(t, fake.URL, key, &memGroups{}, nil, &keitaroFiles{dir: dir})
	body := map[string]any{
		"users": true, "campaigns": true, "offers": true, "networks": true,
	}
	postKeitaroImport(t, adminSrv, body)
	job := waitKeitaroJob(t, adminSrv)
	if job.Stage != "done" || job.Error != "" {
		t.Fatalf("job %+v", job)
	}
	if job.Counts["users"] != 1 {
		t.Fatalf("counts %+v", job.Counts)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, `"enabled": true`) {
		t.Fatal("imported user must be disabled")
	}
	if !strings.Contains(text, `"keitaro_id": 10`) || !strings.Contains(text, `"access_source": "keitaro"`) {
		t.Fatalf("file %s", text)
	}
	if !strings.Contains(text, `"promo"`) {
		t.Fatal("campaign grant must use alias")
	}
	for _, l := range job.Losses {
		if l.Ref == "user 1" && strings.Contains(l.Detail, "зарезервирован") {
			return
		}
	}
	t.Fatal("expected reserved admin login loss", job.Losses)
}

func TestKeitaroImportUsersManualAccessUntouched(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	dir := t.TempDir()
	s, adminSrv := newKeitaroServers(t, fake.URL, key, &memGroups{}, nil, &keitaroFiles{dir: dir})
	body := map[string]any{"users": true, "campaigns": true, "offers": true}
	postKeitaroImport(t, adminSrv, body)
	waitKeitaroJob(t, adminSrv)
	c, csrf := authedClient(t, adminSrv.URL)
	var list []security.User
	code, raw := userJSON(t, c, http.MethodGet, adminSrv.URL+"/api/users", csrf, "")
	if code != http.StatusOK || json.Unmarshal(raw, &list) != nil {
		t.Fatalf("list %d", code)
	}
	var buyerID string
	for _, u := range list {
		if u.Login == "buyer" {
			buyerID = u.ID
		}
	}
	if buyerID == "" {
		t.Fatal("buyer missing")
	}
	code, _ = userJSON(t, c, http.MethodPut, adminSrv.URL+"/api/users/"+buyerID+"/access", csrf,
		`{"sections":["dashboard"],"grants":{"offers":{"mode":"full"}}}`)
	if code != http.StatusOK {
		t.Fatalf("access %d", code)
	}
	postKeitaroImport(t, adminSrv, body)
	waitKeitaroJob(t, adminSrv)
	u, ok := s.users.Get(buyerID)
	if !ok || len(u.Access.Sections) != 1 || u.Access.Sections[0] != "dashboard" {
		t.Fatalf("access changed %+v", u.Access)
	}
}

func TestKeitaroImportUsersReimportUpdatesAccess(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	dir := t.TempDir()
	s, adminSrv := newKeitaroServers(t, fake.URL, key, &memGroups{}, nil, &keitaroFiles{dir: dir})
	body := map[string]any{"users": true, "campaigns": true, "offers": true}
	postKeitaroImport(t, adminSrv, body)
	waitKeitaroJob(t, adminSrv)
	var buyerID string
	for _, u := range s.users.List() {
		if u.Login == "buyer" {
			buyerID = u.ID
		}
	}
	if err := s.setUserEnabled(buyerID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.setUserPassword(buyerID, "secret1"); err != nil {
		t.Fatal(err)
	}
	postKeitaroImport(t, adminSrv, body)
	waitKeitaroJob(t, adminSrv)
	u, ok := s.users.Get(buyerID)
	if !ok || !u.Enabled || u.Hash == "" {
		t.Fatal("enabled/hash touched")
	}
	if u.Access.Grants["offers"].Mode != security.ModeRead {
		t.Fatalf("access not refreshed %+v", u.Access.Grants)
	}
}

func TestKeitaroPurgeUsersDisabledRemoved(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	dir := t.TempDir()
	s, adminSrv := newKeitaroServers(t, fake.URL, key, &memGroups{}, nil, &keitaroFiles{dir: dir})
	body := map[string]any{"users": true, "campaigns": true, "offers": true}
	postKeitaroImport(t, adminSrv, body)
	waitKeitaroJob(t, adminSrv)
	_, code := keitaroPOST(t, adminSrv, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge %d", code)
	}
	for _, u := range s.users.List() {
		if u.Login == "buyer" {
			t.Fatal("disabled imported user should be removed")
		}
	}
}

func TestKeitaroPurgeUsersActiveKept(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	dir := t.TempDir()
	s, adminSrv := newKeitaroServers(t, fake.URL, key, &memGroups{}, nil, &keitaroFiles{dir: dir})
	body := map[string]any{"users": true, "campaigns": true, "offers": true}
	postKeitaroImport(t, adminSrv, body)
	waitKeitaroJob(t, adminSrv)
	var buyerID string
	for _, u := range s.users.List() {
		if u.Login == "buyer" {
			buyerID = u.ID
		}
	}
	if err := s.setUserEnabled(buyerID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.setUserPassword(buyerID, "secret1"); err != nil {
		t.Fatal(err)
	}
	_, code := keitaroPOST(t, adminSrv, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge %d", code)
	}
	u, ok := s.users.Get(buyerID)
	if !ok || u.KeitaroID != 0 || !u.Enabled {
		t.Fatalf("active user %+v", u)
	}
}

func TestKeitaroImportUsers403(t *testing.T) {
	key := "fixture-key"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin_api/v1/users" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	adminSrv := newKeitaroAdmin(t, srv.URL, key, nil, nil, &keitaroFiles{dir: t.TempDir()})
	postKeitaroImport(t, adminSrv, map[string]any{"users": true})
	job := waitKeitaroJob(t, adminSrv)
	if job.Stage != "done" || job.Error != "" {
		t.Fatalf("job %+v", job)
	}
	found := false
	for _, l := range job.Losses {
		if l.Ref == "users" {
			found = true
		}
	}
	if !found {
		t.Fatalf("losses %+v", job.Losses)
	}
}

func TestKeitaroImportOwnerCreatedBy(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	dir := t.TempDir()
	s, adminSrv := newKeitaroServers(t, fake.URL, key, &memGroups{}, nil, &keitaroFiles{dir: dir})
	panelAdmin, err := s.createUser(userDraft{
		Login: "paneladm", Password: "secret1", Type: security.UserAdmin, Enabled: true,
		Access: security.Access{Sections: userSectionsAll(), Grants: map[string]security.Grant{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t)
	_, csrf := login(t, c, adminSrv.URL, "paneladm", "secret1")
	body := map[string]any{"offers": true, "networks": true}
	postKeitaroImportWithClient(t, c, adminSrv.URL, csrf, body)
	waitKeitaroJob(t, adminSrv)
	offers, err := s.cfg.Offers.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offers {
		if o.ID == "4" && o.CreatedBy != panelAdmin.ID {
			t.Fatalf("offer owner %q want %q", o.CreatedBy, panelAdmin.ID)
		}
	}
	_, csrf = login(t, c, adminSrv.URL, "admin", "p@ss")
	postKeitaroImportWithClient(t, c, adminSrv.URL, csrf, body)
	waitKeitaroJob(t, adminSrv)
	offers, err = s.cfg.Offers.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offers {
		if o.ID == "4" && o.CreatedBy != panelAdmin.ID {
			t.Fatalf("created_by cleared %+v", o)
		}
	}
}

func postKeitaroImportWithClient(t *testing.T, c *http.Client, base, csrf string, body any) []byte {
	t.Helper()
	code, raw := doReq(t, c, http.MethodPost, base+"/api/keitaro/import", csrf, "", mustJSON(body))
	if code != http.StatusAccepted {
		t.Fatalf("import %d %s", code, raw)
	}
	return raw
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
