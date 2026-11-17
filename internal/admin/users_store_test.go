package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

func usersServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		UsersFile:    usersPath,
		APIKeysFile:  filepath.Join(dir, "api-keys.json"),
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &fakeGroups{},
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, hash
}

func clientWithCookie(t *testing.T, base, token string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: cookieName, Value: token, Path: "/"}})
	return &http.Client{Jar: jar}
}

func TestLegacyRootSessionWithoutUID(t *testing.T) {
	s, srv, hash := usersServer(t)
	token := "legacy-root"
	fp := security.PasswordFingerprint(hash)
	ctx := context.Background()
	if err := s.cfg.Sessions.Create(ctx, token, security.Session{
		User: "admin", CSRF: "x", PwFP: fp, Created: time.Now(),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	c := clientWithCookie(t, srv.URL, token)
	resp, err := c.Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me status %d", resp.StatusCode)
	}
	var me meResponse
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if me.Type != security.UserAdmin || me.UID != "" || me.User != "admin" {
		t.Fatalf("me: %+v", me)
	}
}

func TestUserLoginAndDisable(t *testing.T) {
	s, srv, _ := usersServer(t)
	u, err := s.createUser(userDraft{
		Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "bob", "secret1"); code != http.StatusOK {
		t.Fatalf("bob login %d", code)
	}
	resp, _ := c.Get(srv.URL + "/api/me")
	var me meResponse
	_ = json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if me.User != "bob" || me.UID != u.ID || me.Type != security.UserRegular {
		t.Fatalf("me: %+v", me)
	}
	if err := s.setUserEnabled(u.ID, false); err != nil {
		t.Fatal(err)
	}
	r2, _ := c.Get(srv.URL + "/api/me")
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled user me want 401, got %d", r2.StatusCode)
	}
}

func TestUserPasswordChangeLeavesRoot(t *testing.T) {
	s, srv, _ := usersServer(t)
	pwFile := filepath.Join(t.TempDir(), "admin.hash")
	s.cfg.PasswordFile = pwFile

	u, err := s.createUser(userDraft{
		Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := newClient(t)
	login(t, root, srv.URL, "admin", "p@ss")
	bob := newClient(t)
	_, csrf := login(t, bob, srv.URL, "bob", "secret1")
	oldBob := clientWithCookie(t, srv.URL, mustCookie(t, bob, srv.URL))

	if code := changePassword(t, bob, srv.URL, csrf, "secret1", "secret2"); code != http.StatusOK {
		t.Fatalf("password change %d", code)
	}
	if _, err := os.Stat(pwFile); err == nil {
		t.Error("root password file must not be created for user password change")
	}
	rRoot, _ := root.Get(srv.URL + "/api/me")
	rRoot.Body.Close()
	if rRoot.StatusCode != http.StatusOK {
		t.Fatalf("root session must stay alive, got %d", rRoot.StatusCode)
	}
	rOld, _ := oldBob.Get(srv.URL + "/api/me")
	rOld.Body.Close()
	if rOld.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old bob cookie want 401, got %d", rOld.StatusCode)
	}
	if code, _ := login(t, newClient(t), srv.URL, "bob", "secret2"); code != http.StatusOK {
		t.Fatalf("bob new password login %d", code)
	}
	_ = u
}

func mustCookie(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	u, _ := url.Parse(base)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == cookieName {
			return ck.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestCredentialForDummyHash(t *testing.T) {
	s, _, _ := usersServer(t)
	u, _ := s.createUser(userDraft{
		Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	})
	_ = s.setUserEnabled(u.ID, false)

	h, uid, ok := s.credentialFor("nobody")
	if ok || uid != "" || h != security.DummyHash {
		t.Fatalf("unknown login: hash=%q uid=%q ok=%v", h, uid, ok)
	}
	h2, _, ok2 := s.credentialFor("bob")
	if ok2 || h2 != security.DummyHash {
		t.Fatalf("disabled user: hash=%q ok=%v", h2, ok2)
	}
	_, srv, _ := usersServer(t)
	if code, _ := login(t, newClient(t), srv.URL, "nobody", "x"); code != http.StatusUnauthorized {
		t.Fatalf("unknown login HTTP %d", code)
	}
}

func TestUsersFileLoadErrors(t *testing.T) {
	hash, _ := security.HashPassword("p@ss")
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, UsersFile: bad,
		Sessions: security.NewMemoryStore(), Limiter: allowAll{},
	}); err == nil {
		t.Fatal("broken users file must fail New")
	}
	missing := filepath.Join(dir, "missing.json")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, UsersFile: missing,
		Sessions: security.NewMemoryStore(), Limiter: allowAll{},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	if code, _ := login(t, newClient(t), srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("root login with missing users file %d", code)
	}
}

func TestSetUserPasswordEmptyDisablesLogin(t *testing.T) {
	s, srv, _ := usersServer(t)
	u, err := s.createUser(userDraft{
		Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.setUserPassword(u.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, ok := s.users.ByLogin("bob")
	if !ok || got.Hash != "" {
		t.Fatalf("empty password must clear hash, got %q", got.Hash)
	}
	if code, _ := login(t, newClient(t), srv.URL, "bob", ""); code != http.StatusUnauthorized {
		t.Fatalf("empty password login want 401, got %d", code)
	}
	if code, _ := login(t, newClient(t), srv.URL, "bob", "secret1"); code != http.StatusUnauthorized {
		t.Fatalf("login after hash cleared want 401, got %d", code)
	}
}

func TestCreateUserReservedLogin(t *testing.T) {
	s, _, _ := usersServer(t)
	_, err := s.createUser(userDraft{
		Login: "admin", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	})
	if !errors.Is(err, errUserReserved) {
		t.Fatalf("admin login: %v", err)
	}
	_, err = s.createUser(userDraft{
		Login: "ADMIN", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	})
	if !errors.Is(err, errUserReserved) {
		t.Fatalf("ADMIN login: %v", err)
	}
	_, err = s.createUser(userDraft{
		Login: "apikey:x", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	})
	if err == nil {
		t.Fatal("apikey login must fail")
	}
}
