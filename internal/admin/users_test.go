package admin

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/security"
)

func usersHTTP(t *testing.T) (*Server, *http.Client, string, string) {
	t.Helper()
	s, srv, _ := usersServer(t)
	c, csrf := authedClient(t, srv.URL)
	return s, c, csrf, srv.URL
}

func userJSON(t *testing.T, c *http.Client, method, url, csrf, body string) (int, []byte) {
	t.Helper()
	return doReq(t, c, method, url, csrf, "", body)
}

func TestUsersCRUD(t *testing.T) {
	s, c, csrf, base := usersHTTP(t)
	usersPath := s.cfg.UsersFile

	code, raw := userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"bob","password":"secret1","type":"USER","enabled":true,"access":{"sections":["dashboard"],"grants":{}}}`)
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	var created security.User
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Hash != "" {
		t.Error("response must not include hash")
	}
	if created.Login != "bob" {
		t.Fatalf("login %q", created.Login)
	}
	disk, _ := os.ReadFile(usersPath)
	if !strings.Contains(string(disk), `"hash"`) {
		t.Error("file must store hash")
	}

	code, _ = userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"bob","password":"secret1","type":"USER","enabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("duplicate want 409, got %d", code)
	}
	code, _ = userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"admin","password":"secret1","type":"USER","enabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("reserved login want 409, got %d", code)
	}

	code, raw = userJSON(t, c, http.MethodGet, base+"/api/users", csrf, "")
	if code != http.StatusOK {
		t.Fatalf("list %d", code)
	}
	var list []security.User
	if err := json.Unmarshal(raw, &list); err != nil || len(list) < 2 {
		t.Fatalf("list %v", list)
	}
	if list[0].Source != security.SourceEnv || list[0].ID != "" || list[0].Type != security.UserAdmin || !list[0].Enabled || list[0].Login != "admin" {
		t.Fatalf("env row %+v", list[0])
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[0]["hash"]; ok {
		t.Error("env row JSON must not include hash")
	}
	var fileRow *security.User
	for i := range list {
		if list[i].Login == "bob" {
			fileRow = &list[i]
			break
		}
	}
	if fileRow == nil || fileRow.Source != security.SourceFile {
		t.Fatalf("file user source %+v", fileRow)
	}

	code, _ = userJSON(t, c, http.MethodPut, base+"/api/users/", csrf, `{"enabled":false}`)
	if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT env user want 404 or 405, got %d", code)
	}
	code, _ = userJSON(t, c, http.MethodDelete, base+"/api/users/", csrf, "")
	if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE env user want 404 or 405, got %d", code)
	}

	code, raw = userJSON(t, c, http.MethodPut, base+"/api/users/"+created.ID, csrf, `{"login":"bobby","type":"USER","enabled":true,"prefs":{"lang":"en","tz":"UTC"}}`)
	if code != http.StatusOK {
		t.Fatalf("update %d %s", code, raw)
	}

	code, _ = userJSON(t, c, http.MethodPut, base+"/api/users/"+created.ID, csrf, `{"login":"bobby","type":"USER","enabled":true,"prefs":{"tz":"Not/AZone"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad tz want 400, got %d", code)
	}

	code, _ = userJSON(t, c, http.MethodPut, base+"/api/users/"+created.ID+"/access", csrf, `{"sections":["nope"],"grants":{}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad section want 400, got %d", code)
	}

	code, raw = userJSON(t, c, http.MethodPut, base+"/api/users/"+created.ID+"/access", csrf, `{"sections":["dashboard","tools"],"grants":{}}`)
	if code != http.StatusOK {
		t.Fatalf("access %d %s", code, raw)
	}

	jar, _ := cookiejar.New(nil)
	uc := &http.Client{Jar: jar}
	_, ucsrf := login(t, uc, base, "bobby", "secret1")
	code, raw = userJSON(t, uc, http.MethodGet, base+"/api/me", ucsrf, "")
	if code != http.StatusOK {
		t.Fatalf("me %d", code)
	}
	var me struct {
		Pages []string `json:"pages"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Pages) != 2 || me.Pages[0] != "dashboard" {
		t.Fatalf("pages %v", me.Pages)
	}
}

func TestUsersPartialUpdateAdmin(t *testing.T) {
	_, c, csrf, base := usersHTTP(t)
	code, raw := userJSON(t, c, http.MethodPost, base+"/api/users", csrf,
		`{"login":"patchadm","password":"secret1","type":"ADMIN","enabled":true}`)
	if code != http.StatusCreated {
		t.Fatalf("create admin %d %s", code, raw)
	}
	var adm security.User
	if err := json.Unmarshal(raw, &adm); err != nil {
		t.Fatal(err)
	}
	code, raw = userJSON(t, c, http.MethodPut, base+"/api/users/"+adm.ID, csrf, `{"prefs":{"lang":"en"}}`)
	if code != http.StatusOK {
		t.Fatalf("patch prefs %d %s", code, raw)
	}
	if err := json.Unmarshal(raw, &adm); err != nil {
		t.Fatal(err)
	}
	if adm.Type != security.UserAdmin || !adm.Enabled || adm.Login != "patchadm" {
		t.Fatalf("after partial PUT: %+v", adm)
	}
	if adm.Prefs.Lang != "en" {
		t.Fatalf("prefs.lang %q", adm.Prefs.Lang)
	}
}

// TestUsersLangUK accepts Ukrainian and rejects a language outside the whitelist.
// t is the test handle. It returns nothing. A non-200 for uk or a non-400 for an unknown code fails the test.
func TestUsersLangUK(t *testing.T) {
	_, c, csrf, base := usersHTTP(t)
	code, raw := userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"ukrainian","password":"secret1","type":"USER","enabled":true}`)
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	var u security.User
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	code, raw = userJSON(t, c, http.MethodPut, base+"/api/users/"+u.ID, csrf, `{"prefs":{"lang":"uk"}}`)
	if code != http.StatusOK {
		t.Fatalf("uk lang %d %s", code, raw)
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	if u.Prefs.Lang != "uk" {
		t.Fatalf("prefs.lang %q", u.Prefs.Lang)
	}
	code, _ = userJSON(t, c, http.MethodPut, base+"/api/users/"+u.ID, csrf, `{"prefs":{"lang":"de"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad lang want 400, got %d", code)
	}
}

func TestUsersLastAdminGuards(t *testing.T) {
	_, c, csrf, base := usersHTTP(t)

	code, raw := userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"solo","password":"secret1","type":"ADMIN","enabled":true}`)
	if code != http.StatusCreated {
		t.Fatalf("create admin %d %s", code, raw)
	}
	var solo security.User
	_ = json.Unmarshal(raw, &solo)

	jar, _ := cookiejar.New(nil)
	ac := &http.Client{Jar: jar}
	_, acsrf := login(t, ac, base, "solo", "secret1")

	code, _ = userJSON(t, ac, http.MethodDelete, base+"/api/users/"+solo.ID, acsrf, "")
	if code != http.StatusConflict {
		t.Fatalf("delete self want 409, got %d", code)
	}
	code, _ = userJSON(t, ac, http.MethodPut, base+"/api/users/"+solo.ID, acsrf, `{"enabled":false}`)
	if code != http.StatusConflict {
		t.Fatalf("disable self want 409, got %d", code)
	}
	code, _ = userJSON(t, ac, http.MethodPut, base+"/api/users/"+solo.ID, acsrf, `{"type":"USER"}`)
	if code != http.StatusOK {
		t.Fatalf("demote last file admin want 200, got %d", code)
	}

	code, _ = userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"second","password":"secret1","type":"ADMIN","enabled":true}`)
	if code != http.StatusCreated {
		t.Fatalf("second admin %d", code)
	}
	code, _ = userJSON(t, c, http.MethodPut, base+"/api/users/"+solo.ID, csrf, `{"type":"USER"}`)
	if code != http.StatusOK {
		t.Fatalf("demote with backup admin want 200, got %d", code)
	}
}

func TestUsersDeleteFileAdmin(t *testing.T) {
	_, c, csrf, base := usersHTTP(t)
	code, raw := userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"fileadm","password":"secret1","type":"ADMIN","enabled":true}`)
	if code != http.StatusCreated {
		t.Fatalf("create admin %d %s", code, raw)
	}
	var u security.User
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	code, _ = userJSON(t, c, http.MethodDelete, base+"/api/users/"+u.ID, csrf, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete file admin want 204, got %d", code)
	}
}

func TestUsersPasswordRevokesSession(t *testing.T) {
	_, c, csrf, base := usersHTTP(t)
	code, raw := userJSON(t, c, http.MethodPost, base+"/api/users", csrf, `{"login":"pwu","password":"secret1","type":"USER","enabled":true}`)
	if code != http.StatusCreated {
		t.Fatal(code)
	}
	var u security.User
	_ = json.Unmarshal(raw, &u)

	jar, _ := cookiejar.New(nil)
	uc := &http.Client{Jar: jar}
	_, ucsrf := login(t, uc, base, "pwu", "secret1")

	code, _ = userJSON(t, c, http.MethodPut, base+"/api/users/"+u.ID+"/password", csrf, `{"password":"secret2"}`)
	if code != http.StatusNoContent {
		t.Fatalf("password change %d", code)
	}
	code, _ = userJSON(t, uc, http.MethodGet, base+"/api/me", ucsrf, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("old session want 401, got %d", code)
	}
}

func TestUsersForbiddenForNonAdmin(t *testing.T) {
	s, c, csrf, base := usersHTTP(t)
	_, err := s.createUser(userDraft{
		Login: "regular", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: []string{"dashboard"}, Grants: map[string]security.Grant{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	uc := &http.Client{Jar: jar}
	_, ucsrf := login(t, uc, base, "regular", "secret1")
	code, _ := userJSON(t, uc, http.MethodGet, base+"/api/users", ucsrf, "")
	if code != http.StatusForbidden {
		t.Fatalf("USER list want 403, got %d", code)
	}

	s.keyMu.Lock()
	rawKey, _, err := s.keys.Generate("k", []string{security.ScopeAdmin})
	s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/users", nil)
	req.Header.Set("Api-Key", rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("api key want 403, got %d", resp.StatusCode)
	}
	_ = c
	_ = csrf
}

func TestUsersDisabled(t *testing.T) {
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		UsersFile:    "",
		Sessions:     security.NewMemoryStore(),
		Limiter:      allowAll{},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c, csrf := authedClient(t, srv.URL)

	code, raw := userJSON(t, c, http.MethodGet, srv.URL+"/api/users", csrf, "")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	var list []security.User
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Source != security.SourceEnv || list[0].Login != "admin" {
		t.Fatalf("want single env row, got %v", list)
	}
	code, _ = userJSON(t, c, http.MethodPost, srv.URL+"/api/users", csrf, `{"login":"x","password":"secret1"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("post disabled want 503, got %d", code)
	}
}
