package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

// setupTestToken is the fictional first-run token used by these tests.
const setupTestToken = "example-setup-token"

// tokenSetupServer starts an admin server with the first-run form open and token as
// Config.SetupToken. It returns the HTTP test server and the directory of the password file.
func tokenSetupServer(t *testing.T, token string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordFile: filepath.Join(dir, "admin.hash"),
		Sessions:     security.NewMemoryStore(),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		SetupToken:   token,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, dir
}

// postSetup sends a first-run form with the given fields. A nil token leaves the field out.
// It returns the status and the body text.
func postSetup(t *testing.T, srv *httptest.Server, token *string) (int, string) {
	t.Helper()
	body := map[string]string{"login": "owner", "password": "secret1", "email": "owner@example.com"}
	if token != nil {
		body["token"] = *token
	}
	raw, _ := json.Marshal(body)
	res, err := srv.Client().Post(srv.URL+"/api/setup", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(text)
}

// getSetupView reads GET /api/setup.
func getSetupView(t *testing.T, srv *httptest.Server) setupView {
	t.Helper()
	res, err := srv.Client().Get(srv.URL + "/api/setup")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var view setupView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

// TestSetupTokenRequiredAnswers checks that a missing and a wrong token are 403 with the two
// texts the panel tells apart, and that nothing is written.
func TestSetupTokenRequiredAnswers(t *testing.T) {
	srv, dir := tokenSetupServer(t, setupTestToken)
	wrong := "example-other-token"
	empty := ""
	cases := []struct {
		name  string
		token *string
		want  string
	}{
		{"absent", nil, "setup token is required"},
		{"empty", &empty, "setup token is required"},
		{"wrong", &wrong, "setup token is wrong"},
	}
	for _, c := range cases {
		code, text := postSetup(t, srv, c.token)
		if code != http.StatusForbidden || !strings.Contains(text, c.want) {
			t.Errorf("%s: %d %s, want 403 %q", c.name, code, text, c.want)
		}
		if strings.Contains(text, setupTestToken) {
			t.Errorf("%s: the answer repeats the token", c.name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "admin.hash")); !os.IsNotExist(err) {
		t.Fatal("hash file was written without a valid token")
	}
}

// TestSetupTokenAccepted checks that the right token creates the administrator, and that the
// second call is 409 with or without the token.
func TestSetupTokenAccepted(t *testing.T) {
	srv, dir := tokenSetupServer(t, setupTestToken)
	tok := setupTestToken
	if code, text := postSetup(t, srv, &tok); code != http.StatusOK {
		t.Fatalf("right token: %d %s", code, text)
	}
	if _, err := os.Stat(filepath.Join(dir, "admin.hash")); err != nil {
		t.Fatalf("hash file: %v", err)
	}
	wrong := "example-other-token"
	for name, token := range map[string]*string{"right": &tok, "wrong": &wrong, "absent": nil} {
		if code, text := postSetup(t, srv, token); code != http.StatusConflict || strings.Contains(text, "setup token") {
			t.Errorf("%s token after setup: %d %s, want a plain 409", name, code, text)
		}
	}
}

// TestSetupWithoutTokenVariable checks that a server without Config.SetupToken ignores the
// token field and accepts the form as before.
func TestSetupWithoutTokenVariable(t *testing.T) {
	srv, _ := tokenSetupServer(t, "")
	if view := getSetupView(t, srv); !view.Needed || view.TokenRequired {
		t.Fatalf("view: %+v", view)
	}
	stray := "anything"
	if code, text := postSetup(t, srv, &stray); code != http.StatusOK {
		t.Fatalf("setup without a token variable: %d %s", code, text)
	}
}

// TestSetupViewTokenRequired checks token_required: true while the form is open and the token
// is set, false once the administrator exists, and never carrying the token itself.
func TestSetupViewTokenRequired(t *testing.T) {
	srv, _ := tokenSetupServer(t, setupTestToken)
	view := getSetupView(t, srv)
	if !view.Needed || !view.TokenRequired {
		t.Fatalf("open form: %+v", view)
	}
	res, err := srv.Client().Get(srv.URL + "/api/setup")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(raw), setupTestToken) {
		t.Fatalf("GET /api/setup leaks the token: %s", raw)
	}
	tok := setupTestToken
	if code, text := postSetup(t, srv, &tok); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, text)
	}
	if view := getSetupView(t, srv); view.Needed || view.TokenRequired {
		t.Fatalf("closed form: %+v", view)
	}
}

// TestSetupTokenOKComparesWholeValue checks that only the equal token passes, whatever the
// length of the given value, and that an empty configured token accepts anything.
func TestSetupTokenOKComparesWholeValue(t *testing.T) {
	s := &Server{cfg: Config{SetupToken: "s3cret-token"}}
	for given, want := range map[string]bool{
		"s3cret-token":       true,
		"":                   false,
		"s3cret":             false,
		"s3cret-token-extra": false,
		"S3CRET-TOKEN":       false,
	} {
		if got := s.setupTokenOK(given); got != want {
			t.Errorf("setupTokenOK(%q) = %v, want %v", given, got, want)
		}
	}
	if !(&Server{}).setupTokenOK("anything") {
		t.Error("setupTokenOK with no configured token must accept")
	}
}
