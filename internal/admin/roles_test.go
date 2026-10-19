package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/kuztds/bridge/ktimport"
)

// api sends a JSON request with c and returns the status and the body.
// csrf is the token of the session behind c.
func (r *rolesRig) api(t *testing.T, c *http.Client, csrf, method, path string, body any) (int, []byte) {
	t.Helper()
	code, raw, err := doJSON(c, method, r.srv.URL+path, csrf, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
}

// adminAPI is api as the administrator.
func (r *rolesRig) adminAPI(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	return r.api(t, r.admin, r.csrf, method, path, body)
}

// me returns the decoded GET /api/me of the user of the rig.
func (r *rolesRig) me(t *testing.T) meResponse {
	t.Helper()
	code, body := r.get(t, r.user, "/api/me")
	if code != http.StatusOK {
		t.Fatalf("me %d %s", code, body)
	}
	var out meResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRolesCRUD(t *testing.T) {
	rig := newRolesRig(t, "")
	code, raw := rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{
		"name": "Buyers",
		"access": map[string]any{
			"sections": []string{"dashboard", "report"},
			"grants":   map[string]any{"campaigns": map[string]any{"mode": "created", "ids": []string{"x"}}},
			"hidden":   []string{"country"},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	var created roleView
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" || created.Users != 0 {
		t.Fatalf("created %+v err %v", created, err)
	}
	if g := created.Access.Grants["campaigns"]; g.Mode != security.ModeCreated || len(g.IDs) != 0 {
		t.Fatalf("created grant keeps ids: %+v", g)
	}

	code, raw = rig.adminAPI(t, http.MethodGet, "/api/roles", nil)
	var list []roleView
	if code != http.StatusOK || json.Unmarshal(raw, &list) != nil || len(list) != 2 {
		t.Fatalf("list %d %s", code, raw)
	}
	if list[0].Name != "Buyers" || list[1].Name != "ops" || list[1].Users != 1 || list[0].Users != 0 {
		t.Fatalf("list order or usage: %+v", list)
	}

	code, _ = rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{"name": "buyers"})
	if code != http.StatusConflict {
		t.Fatalf("same name in other case: %d", code)
	}
	for name, body := range map[string]map[string]any{
		"admin section": {"name": "bad1", "access": map[string]any{"sections": []string{"users"}}},
		"selected mode": {"name": "bad2", "access": map[string]any{"grants": map[string]any{"offers": map[string]any{"mode": "selected", "ids": []string{"o"}}}}},
		"hidden field":  {"name": "bad3", "access": map[string]any{"hidden": []string{"nope"}}},
		"unknown grant": {"name": "bad4", "access": map[string]any{"grants": map[string]any{"nope": map[string]any{"mode": "full"}}}},
		"empty name":    {"name": " "},
	} {
		if code, raw := rig.adminAPI(t, http.MethodPost, "/api/roles", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}

	code, raw = rig.adminAPI(t, http.MethodPut, "/api/roles/"+created.ID, map[string]any{
		"name":   "Media buyers",
		"access": map[string]any{"sections": []string{"flows"}},
	})
	var updated roleView
	if code != http.StatusOK || json.Unmarshal(raw, &updated) != nil || updated.Name != "Media buyers" ||
		!reflect.DeepEqual(updated.Access.Sections, []string{"flows"}) {
		t.Fatalf("update %d %s", code, raw)
	}
	code, raw = rig.adminAPI(t, http.MethodPut, "/api/roles/"+created.ID, map[string]any{"name": "ops"})
	if code != http.StatusConflict {
		t.Fatalf("rename onto taken name: %d %s", code, raw)
	}
	code, raw = rig.adminAPI(t, http.MethodPut, "/api/roles/"+created.ID, map[string]any{})
	if code != http.StatusOK || !strings.Contains(string(raw), `"Media buyers"`) {
		t.Fatalf("empty update keeps values: %d %s", code, raw)
	}
	if code, _ = rig.adminAPI(t, http.MethodPut, "/api/roles/nope", map[string]any{"name": "x"}); code != http.StatusNotFound {
		t.Fatalf("update missing: %d", code)
	}

	if code, _ = rig.adminAPI(t, http.MethodDelete, "/api/roles/"+rig.roleID, nil); code != http.StatusConflict {
		t.Fatalf("delete role in use: %d", code)
	}
	if code, _ = rig.adminAPI(t, http.MethodDelete, "/api/roles/"+created.ID, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ = rig.adminAPI(t, http.MethodGet, "/api/roles/"+created.ID, nil); code != http.StatusNotFound {
		t.Fatalf("get deleted: %d", code)
	}
	if code, _ = rig.adminAPI(t, http.MethodDelete, "/api/roles/"+created.ID, nil); code != http.StatusNotFound {
		t.Fatalf("delete deleted: %d", code)
	}
}

func TestRolesAreAdministratorOnly(t *testing.T) {
	rig := newRolesRig(t, "")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/roles"}, {http.MethodPost, "/api/roles"}, {http.MethodGet, "/api/roles/" + rig.roleID},
		{http.MethodPut, "/api/roles/" + rig.roleID}, {http.MethodDelete, "/api/roles/" + rig.roleID},
	} {
		if code, _ := rig.api(t, rig.user, rig.ucsrf, c.method, c.path, map[string]any{"name": "x"}); code != http.StatusForbidden {
			t.Errorf("%s %s as user: %d", c.method, c.path, code)
		}
	}
	if code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/users/zz", map[string]any{"role_id": rig.roleID}); code != http.StatusForbidden {
		t.Errorf("user sets a role: %d", code)
	}
}

func TestRoleDrivesAccessAtOnce(t *testing.T) {
	rig := newRolesRig(t, "")
	me := rig.me(t)
	if !reflect.DeepEqual(me.Pages, sectionStrings(userSections)) || me.Role == nil || me.Role.Name != "ops" || len(me.Hidden) != 0 {
		t.Fatalf("me %+v", me)
	}
	if code, _ := rig.get(t, rig.user, "/api/report"); code != http.StatusOK {
		t.Fatalf("report before: %d", code)
	}

	code, raw := rig.adminAPI(t, http.MethodPut, "/api/roles/"+rig.roleID, map[string]any{
		"access": map[string]any{"sections": []string{"dashboard"}, "hidden": []string{"cost"}},
	})
	if code != http.StatusOK {
		t.Fatalf("update role %d %s", code, raw)
	}
	me = rig.me(t)
	if !reflect.DeepEqual(me.Pages, []string{"dashboard"}) {
		t.Fatalf("pages after role change: %v", me.Pages)
	}
	if !reflect.DeepEqual(me.Hidden, []string{"cost", "cost_cents", "cost_cur"}) {
		t.Fatalf("hidden: %v", me.Hidden)
	}
	if me.Access == nil || !reflect.DeepEqual(me.Access.Hidden, []string{"cost"}) {
		t.Fatalf("access in me: %+v", me.Access)
	}
	if code, _ := rig.get(t, rig.user, "/api/report"); code != http.StatusForbidden {
		t.Fatalf("report after role change: %d", code)
	}
	if code, _ := rig.get(t, rig.user, "/api/stats/summary"); code != http.StatusOK {
		t.Fatalf("summary after role change: %d", code)
	}
}

func TestRoleGrantModesFilterCatalog(t *testing.T) {
	rig := newRolesRig(t, "")
	groups := func() int {
		code, body := rig.get(t, rig.user, "/api/groups")
		var list []map[string]any
		if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil {
			t.Fatalf("groups %d %s", code, body)
		}
		return len(list)
	}
	put := func(mode string) {
		code, raw := rig.adminAPI(t, http.MethodPut, "/api/roles/"+rig.roleID, map[string]any{
			"access": map[string]any{
				"sections": []string{"flows"},
				"grants":   map[string]any{"campaigns": map[string]any{"mode": mode}},
			},
		})
		if code != http.StatusOK {
			t.Fatalf("role %s: %d %s", mode, code, raw)
		}
	}
	put("read")
	if n := groups(); n != len(scopeCampaigns()) {
		t.Fatalf("read sees %d campaigns", n)
	}
	put("created")
	if n := groups(); n != 0 {
		t.Fatalf("created sees %d foreign campaigns", n)
	}
	put("full")
	if n := groups(); n != len(scopeCampaigns()) {
		t.Fatalf("full sees %d campaigns", n)
	}
}

func TestMissingRoleClosesAccess(t *testing.T) {
	rig := newRolesRig(t, "")
	if err := rig.s.roles.Remove(rig.roleID); err != nil {
		t.Fatal(err)
	}
	me := rig.me(t)
	if len(me.Pages) != 0 || me.Role != nil {
		t.Fatalf("me %+v", me)
	}
	for _, p := range []string{"/api/report", "/api/stats/summary", "/api/logs", "/api/offers"} {
		if code, _ := rig.get(t, rig.user, p); code != http.StatusForbidden {
			t.Errorf("GET %s with a missing role: %d", p, code)
		}
	}
	code, body := rig.get(t, rig.user, "/api/groups")
	var list []map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || len(list) != 0 {
		t.Fatalf("groups with a missing role: %d %s", code, body)
	}
	if code, _ := rig.get(t, rig.admin, "/api/report"); code != http.StatusOK {
		t.Fatalf("administrator report: %d", code)
	}
}

func TestAccessOf(t *testing.T) {
	rig := newRolesRig(t, "")
	own := security.Access{Sections: []string{"logs"}, Grants: map[string]security.Grant{}, Hidden: []string{"ip"}}
	if got := rig.s.accessOf(security.User{Type: security.UserRegular, Access: own}); !reflect.DeepEqual(got, own) {
		t.Errorf("user without role: %+v", got)
	}
	if got := rig.s.accessOf(security.User{Type: security.UserAdmin, RoleID: rig.roleID, Access: own}); !reflect.DeepEqual(got, own) {
		t.Errorf("administrator ignores the role: %+v", got)
	}
	got := rig.s.accessOf(security.User{Type: security.UserRegular, RoleID: rig.roleID, Access: own})
	if !reflect.DeepEqual(got.Sections, userSectionsAll()) {
		t.Errorf("role sections: %v", got.Sections)
	}
	closed := rig.s.accessOf(security.User{Type: security.UserRegular, RoleID: "ghost", Access: own})
	if len(closed.Sections) != 0 || len(closed.Hidden) != 0 || len(closed.Grants) != len(grantResources) {
		t.Errorf("closed access: %+v", closed)
	}
	v := viewer{uid: "u", grants: closed.Grants}
	for _, res := range grantResources {
		if v.mode(res) != modeNone || v.canSee(res, ref{id: "x", owner: "u"}) || v.canWrite(res, ref{id: "x"}) || v.canCreate(res) {
			t.Errorf("closed access leaks %s", res)
		}
	}
}

func TestNormalizeRoleAccess(t *testing.T) {
	if _, err := normalizeRoleAccess(security.Access{Sections: []string{"users"}}); !errorsIsAccess(err, errAccessSection) {
		t.Errorf("admin section: %v", err)
	}
	if _, err := normalizeRoleAccess(security.Access{
		Grants: map[string]security.Grant{"offers": {Mode: security.ModeSelected, IDs: []string{"a"}}},
	}); !errorsIsAccess(err, errAccessMode) {
		t.Errorf("selected: %v", err)
	}
	if _, err := normalizeRoleAccess(security.Access{Hidden: []string{"nope"}}); !errorsIsAccess(err, errAccessHidden) {
		t.Errorf("hidden: %v", err)
	}
	out, err := normalizeRoleAccess(security.Access{
		Sections: []string{"logs", "logs"},
		Grants: map[string]security.Grant{
			"campaigns": {Mode: security.ModeCreated, IDs: []string{"a"}, Labels: []string{"b"}},
			"offers":    {Mode: security.ModeFull},
		},
		Hidden: []string{"ip", "ip", "cost"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if g := out.Grants["campaigns"]; g.Mode != security.ModeCreated || g.IDs != nil || g.Labels != nil {
		t.Errorf("created grant: %+v", g)
	}
	if !reflect.DeepEqual(out.Sections, []string{"logs"}) || !reflect.DeepEqual(out.Hidden, []string{"ip", "cost"}) {
		t.Errorf("normalized: %+v", out)
	}
}

func TestRoleAssignRules(t *testing.T) {
	rig := newRolesRig(t, "")
	create := func(body map[string]any) int {
		code, _ := rig.adminAPI(t, http.MethodPost, "/api/users", body)
		return code
	}
	if code := create(map[string]any{"login": "a1", "password": "secret1", "type": "ADMIN", "role_id": rig.roleID}); code != http.StatusBadRequest {
		t.Errorf("administrator with a role: %d", code)
	}
	if code := create(map[string]any{"login": "a2", "password": "secret1", "role_id": "ghost"}); code != http.StatusBadRequest {
		t.Errorf("unknown role on create: %d", code)
	}
	if code := create(map[string]any{"login": "a3", "password": "secret1", "role_id": rig.roleID, "enabled": true}); code != http.StatusCreated {
		t.Errorf("user with a role: %d", code)
	}
	a3, ok := rig.s.users.ByLogin("a3")
	if !ok || a3.RoleID != rig.roleID {
		t.Fatalf("a3 %+v", a3)
	}

	code, raw := rig.adminAPI(t, http.MethodGet, "/api/users", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"role_id": "`+rig.roleID+`"`) && !strings.Contains(string(raw), `"role_id":"`+rig.roleID+`"`) {
		t.Errorf("list has no role_id: %d %s", code, raw)
	}
	if code, _ := rig.adminAPI(t, http.MethodPut, "/api/users/"+a3.ID, map[string]any{"role_id": "ghost"}); code != http.StatusBadRequest {
		t.Errorf("unknown role on update: %d", code)
	}
	code, raw = rig.adminAPI(t, http.MethodPut, "/api/users/"+a3.ID+"/access", map[string]any{"sections": []string{"logs"}})
	if code != http.StatusConflict || !strings.Contains(string(raw), "user has a role") {
		t.Errorf("own access of a user with a role: %d %s", code, raw)
	}
	if code, _ := rig.adminAPI(t, http.MethodPut, "/api/users/"+a3.ID, map[string]any{"enabled": false}); code != http.StatusOK {
		t.Errorf("other changes keep the role: %d", code)
	}
	if u, _ := rig.s.users.Get(a3.ID); u.RoleID != rig.roleID {
		t.Errorf("role lost: %+v", u)
	}

	code, raw = rig.adminAPI(t, http.MethodPut, "/api/users/"+a3.ID, map[string]any{"type": "ADMIN"})
	if code != http.StatusOK || strings.Contains(string(raw), "role_id") {
		t.Errorf("promotion drops the role: %d %s", code, raw)
	}
	if code, _ := rig.adminAPI(t, http.MethodPut, "/api/users/"+a3.ID, map[string]any{"role_id": rig.roleID}); code != http.StatusBadRequest {
		t.Errorf("role on an administrator: %d", code)
	}
	if code, _ := rig.adminAPI(t, http.MethodDelete, "/api/roles/"+rig.roleID, nil); code != http.StatusConflict {
		t.Errorf("ops still uses the role: %d", code)
	}
}

// brokenRolesFiles lists contents of roles.json the server must not use.
var brokenRolesFiles = map[string]string{
	"bad json":      `{broken`,
	"torn":          ``,
	"admin section": `[{"id":"r1","name":"x","access":{"sections":["users"],"grants":{}}}]`,
	"unknown field": `[{"id":"r1","name":"x","access":{"sections":[],"grants":{},"hidden":["nope"]}}]`,
	"repeated name": `[{"id":"r1","name":"x","access":{}},{"id":"r2","name":"X","access":{}}]`,
	"no id":         `[{"id":"","name":"x","access":{}}]`,
}

func TestBrokenRolesFileLocksRoleUsersNotAdmins(t *testing.T) {
	for name, content := range brokenRolesFiles {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "roles.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			s := newRolesServer(t, dir)
			u, err := s.createUser(userDraft{Login: "ghost", Password: "secret1", Type: security.UserRegular, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.updateUser(u.ID, func(rec *security.User) error {
				rec.RoleID = "r1"
				rec.Access = security.Access{Sections: userSectionsAll(), Grants: map[string]security.Grant{}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			srv := newRolesHTTP(t, s)
			admin := newClient(t)
			_, csrf := login(t, admin, srv.URL, "admin", "p@ss")
			ghost := newClient(t)
			login(t, ghost, srv.URL, "ghost", "secret1")

			if code, _, _ := doJSON(admin, http.MethodGet, srv.URL+"/api/roles", csrf, nil); code != http.StatusServiceUnavailable {
				t.Errorf("list: %d", code)
			}
			if code, _, _ := doJSON(admin, http.MethodPost, srv.URL+"/api/roles", csrf, map[string]any{"name": "n"}); code != http.StatusServiceUnavailable {
				t.Errorf("create: %d", code)
			}
			if code, _, _ := doJSON(admin, http.MethodDelete, srv.URL+"/api/roles/r1", csrf, nil); code != http.StatusServiceUnavailable {
				t.Errorf("delete: %d", code)
			}
			if got, _ := os.ReadFile(path); string(got) != content {
				t.Errorf("damaged file was overwritten: %q", got)
			}
			if resp, err := ghost.Get(srv.URL + "/api/report"); err != nil || resp.StatusCode != http.StatusForbidden {
				t.Errorf("role user report: %v %v", resp, err)
			}
			if resp, err := admin.Get(srv.URL + "/api/report"); err != nil || resp.StatusCode != http.StatusOK {
				t.Errorf("administrator report: %v %v", resp, err)
			}
		})
	}
}

func TestRolesPersistAcrossRestart(t *testing.T) {
	rig := newRolesRig(t, "")
	code, raw := rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{
		"name": "Persisted", "access": map[string]any{"sections": []string{"logs"}, "hidden": []string{"ip"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	path := filepath.Join(rig.dir, "roles.json")
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("roles.json mode: %v %v", info, err)
		}
	}
	again := newRolesServer(t, rig.dir)
	var names []string
	for _, r := range again.roles.List() {
		names = append(names, r.Name)
	}
	if !reflect.DeepEqual(names, []string{"ops", "Persisted"}) && !reflect.DeepEqual(names, []string{"Persisted", "ops"}) {
		t.Fatalf("roles after restart: %v", names)
	}
	if again.roles.Failure() != "" {
		t.Fatalf("roles failure %q", again.roles.Failure())
	}
	data, _ := os.ReadFile(filepath.Join(rig.dir, "users.json"))
	if !strings.Contains(string(data), `"role_id"`) {
		t.Errorf("users.json lost role_id: %s", data)
	}
}

func TestOldUsersWithoutRolesBehaveAsBefore(t *testing.T) {
	dir := t.TempDir()
	hash, err := security.HashPassword("secret1")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal([]map[string]any{{
		"id": "u1", "login": "old", "type": "USER", "enabled": true, "hash": hash,
		"prefs": map[string]any{}, "created_at": "2026-01-01T00:00:00Z",
		"access": map[string]any{"sections": []string{"report"}, "grants": map[string]any{}, "hidden": []string{"profit"}},
	}})
	if err := os.WriteFile(filepath.Join(dir, "users.json"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	s := newRolesServer(t, dir)
	srv := newRolesHTTP(t, s)
	c := newClient(t)
	login(t, c, srv.URL, "old", "secret1")
	resp, err := c.Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var me meResponse
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(me.Pages, []string{"report"}) || !reflect.DeepEqual(me.Hidden, []string{"profit"}) || me.Role != nil {
		t.Fatalf("me %+v", me)
	}
	if got := s.accessOf(mustGetUser(t, s, "u1")); !reflect.DeepEqual(got.Sections, []string{"report"}) {
		t.Fatalf("access %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "roles.json")); !os.IsNotExist(err) {
		t.Fatalf("roles.json must not be created by reading: %v", err)
	}
	if _, err := s.createUser(userDraft{Login: "plain", Password: "secret1", Type: security.UserRegular}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "users.json")); strings.Contains(string(data), "role_id") {
		t.Errorf("users without a role must not get role_id: %s", data)
	}
}

func TestRolesDisabledWithoutFile(t *testing.T) {
	s := newRolesServer(t, t.TempDir())
	s.cfg.RolesFile, s.roles = "", nil
	srv := newRolesHTTP(t, s)
	admin := newClient(t)
	_, csrf := login(t, admin, srv.URL, "admin", "p@ss")
	if code, body, _ := doJSON(admin, http.MethodGet, srv.URL+"/api/roles", csrf, nil); code != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("list: %d %s", code, body)
	}
	if code, _, _ := doJSON(admin, http.MethodPost, srv.URL+"/api/roles", csrf, map[string]any{"name": "n"}); code != http.StatusServiceUnavailable {
		t.Errorf("create: %d", code)
	}
	if _, err := s.createUser(userDraft{Login: "r", Password: "secret1", Type: security.UserRegular, RoleID: "x"}); err == nil {
		t.Error("a role cannot be set without roles storage")
	}
}

func TestKeitaroImportAndPurgeSkipUsersWithRole(t *testing.T) {
	rig := newRolesRig(t, "")
	s := rig.s
	if err := s.users.Add(security.User{
		Login: "kt", Type: security.UserRegular, KeitaroID: 10, AccessSource: "keitaro", RoleID: rig.roleID,
		Access: security.Access{Sections: []string{"logs"}},
	}); err != nil {
		t.Fatal(err)
	}
	n, _, err := s.importUsers([]ktimport.CatUser{{ID: "10", Login: "kt", Type: "USER", Sections: []string{"tools"}, Lang: "ru"}})
	if err != nil || n != 1 {
		t.Fatalf("import %d %v", n, err)
	}
	u, _ := s.users.ByLogin("kt")
	if u.RoleID != rig.roleID || !reflect.DeepEqual(u.Access.Sections, []string{"logs"}) || u.Prefs.Lang != "" {
		t.Fatalf("import touched a user with a role: %+v", u)
	}
	removed, err := s.purgeUsers(func(id string) bool { return id == "10" })
	if err != nil || removed != 0 {
		t.Fatalf("purge %d %v", removed, err)
	}
	if u, ok := s.users.ByLogin("kt"); !ok || u.KeitaroID != 0 || u.RoleID != rig.roleID {
		t.Fatalf("purge result %+v %v", u, ok)
	}
}

// newRolesHTTP serves s over a test HTTP server closed with the test.
func newRolesHTTP(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// mustGetUser returns the stored user with id.
func mustGetUser(t *testing.T, s *Server, id string) security.User {
	t.Helper()
	u, ok := s.users.Get(id)
	if !ok {
		t.Fatalf("user %s missing", id)
	}
	return u
}
