package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

func recoveryFixture(t *testing.T, withUser bool, limit LoginLimiter) (*httptest.Server, string, *bytes.Buffer) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pw := filepath.Join(dir, "admin.hash")
	var logs bytes.Buffer
	cfg := Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		PasswordFile: pw,
		Sessions:     security.NewMemoryStore(),
		Limiter:      limit,
		SessionTTL:   time.Hour,
		Log:          slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	if withUser {
		userHash, err := security.HashPassword("old-user")
		if err != nil {
			t.Fatal(err)
		}
		set := security.NewUserSet()
		if err := set.Add(security.User{ID: "u1", Login: "Dasha", Type: security.UserRegular, Enabled: true, Hash: userHash}); err != nil {
			t.Fatal(err)
		}
		cfg.UsersFile = filepath.Join(dir, "users.json")
		if err := set.Save(cfg.UsersFile); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, pw, &logs
}

func postRecover(t *testing.T, base, login, code, password string) int {
	t.Helper()
	body := `{"login":` + jsonString(login) + `,"code":` + jsonString(code) + `,"password":` + jsonString(password) + `}`
	resp, err := http.Post(base+"/api/recover", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func hashStill(t *testing.T, path, password string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !security.VerifyPassword(password, strings.TrimSpace(string(raw))) {
		t.Fatal("stored hash does not match the expected password")
	}
}

func TestRecoveryCodeChangesHashOnce(t *testing.T) {
	srv, pw, logs := recoveryFixture(t, false, allowAll{})
	var out bytes.Buffer
	if err := IssueRecoveryCode(&out, "admin", pw, time.Now()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(pw), recoveryFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	code := strings.TrimSpace(out.String())
	if postRecover(t, srv.URL, "other", code, "n3w-pw-1") != http.StatusUnauthorized {
		t.Fatal("wrong login")
	}
	hashStill(t, pw, "p@ss")
	if postRecover(t, srv.URL, "admin", code, "n3w-pw-1") != http.StatusOK {
		t.Fatal("valid code")
	}
	hashStill(t, pw, "n3w-pw-1")
	if postRecover(t, srv.URL, "admin", code, "n3w-pw-2") != http.StatusUnauthorized {
		t.Fatal("second use")
	}
	hashStill(t, pw, "n3w-pw-1")
	text := logs.String()
	if strings.Contains(text, code) || strings.Contains(text, "n3w-pw-1") || strings.Contains(text, "n3w-pw-2") {
		t.Fatal("recovery secret reached the log")
	}
}

func TestRecoveryExpiredCodeDoesNotChangeHash(t *testing.T) {
	srv, pw, _ := recoveryFixture(t, false, allowAll{})
	codeHash, err := security.HashPassword("once-code")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(recoveryRecord{Login: "admin", CodeHash: codeHash, Expires: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(pw), recoveryFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if postRecover(t, srv.URL, "admin", "once-code", "n3w-pw-1") != http.StatusUnauthorized {
		t.Fatal("expired code")
	}
	hashStill(t, pw, "p@ss")
}

func TestRecoveryUserCodeLeavesRootHash(t *testing.T) {
	srv, pw, logs := recoveryFixture(t, true, allowAll{})
	var out bytes.Buffer
	if err := IssueRecoveryCode(&out, "Dasha", pw, time.Now()); err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(out.String())
	if postRecover(t, srv.URL, "Dasha", code, "user-pw-9") != http.StatusOK {
		t.Fatal("user code")
	}
	hashStill(t, pw, "p@ss")
	set, err := security.LoadUserSet(filepath.Join(filepath.Dir(pw), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	u, ok := set.ByLogin("Dasha")
	if !ok || !security.VerifyPassword("user-pw-9", u.Hash) {
		t.Fatal("user hash was not replaced")
	}
	if postRecover(t, srv.URL, "Dasha", code, "user-pw-8") != http.StatusUnauthorized {
		t.Fatal("second use")
	}
	if strings.Contains(logs.String(), code) || strings.Contains(logs.String(), "user-pw-9") {
		t.Fatal("recovery secret reached the log")
	}
}

func TestRecoveryLimiterMatchesLogin(t *testing.T) {
	srv, pw, _ := recoveryFixture(t, false, denyLimiter{})
	if postRecover(t, srv.URL, "admin", "nope", "n3w-pw-1") != http.StatusTooManyRequests {
		t.Fatal("limiter")
	}
	hashStill(t, pw, "p@ss")
}

func TestRecoverUnavailableOnAdminHost(t *testing.T) {
	srv := adminHostServer(t, []config.Domain{{ID: "1", Host: "park.com", Primary: true}})
	resp := withHost(t, http.MethodPost, srv.URL+"/api/recover", "admin.park.com", strings.NewReader(`{"login":"admin","code":"x","password":"longenough"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestLoginPageSeparatesUnavailable(t *testing.T) {
	raw, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		"e.status===503",
		"Sign-in is temporarily unavailable",
		"path!=='/api/recover'",
		"Вход временно недоступен",
		"Вхід тимчасово недоступний",
		"Восстановить пароль",
		"Відновити пароль",
		`<label for="rl">${tr('Login')}</label>`,
		`'Login':'Логин'`,
		`'Login':'Логін'`,
		`<label for="l">${tr('Username')}</label>`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if !strings.Contains(s, "loginMessage(e)") {
		t.Fatal("login catch still folds every error into one phrase")
	}
}
