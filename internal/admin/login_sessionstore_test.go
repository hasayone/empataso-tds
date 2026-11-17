package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

// loginBody posts the credentials to base and returns the response, closed by the test.
func loginBody(t *testing.T, client *http.Client, base, user, pass string) *http.Response {
	t.Helper()
	resp, err := client.Post(base+"/api/login", "application/json",
		strings.NewReader(`{"Login":"`+user+`","Password":"`+pass+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestLoginWithDeadSessionStoreSaysSo: a right password that the session store
// cannot save is 503 with its own text and a journal line, and sets no cookie;
// a wrong password stays 401.
func TestLoginWithDeadSessionStoreSaysSo(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingLogger{}
	srv := loginServer(t, Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: failingSessions{security.NewMemoryStore()}, Limiter: allowAll{},
		SessionTTL: time.Hour, Log: rec,
	})

	resp := loginBody(t, http.DefaultClient, srv.URL, "admin", "p@ss")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var body struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != errSessionStoreText {
		t.Errorf("error text = %q, want %q", body.Error, errSessionStoreText)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("no cookie may be set when the session was not saved: %v", resp.Cookies())
	}
	logged := false
	for _, line := range rec.lines() {
		if strings.Contains(line, "session was not issued") {
			logged = true
		}
	}
	if !logged {
		t.Errorf("the failed write must be in the journal, got %q", rec.lines())
	}

	if code := loginBody(t, http.DefaultClient, srv.URL, "admin", "wrong").StatusCode; code != http.StatusUnauthorized {
		t.Errorf("a wrong password = %d, want 401", code)
	}
}

// TestMeCarriesUniqCeiling: GET /api/me tells the panel the ceiling of the
// uniqueness window, a day when the config has none.
func TestMeCarriesUniqCeiling(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		set  time.Duration
		want int
	}{
		{"default", 0, int(store.DefaultUniqMax / time.Second)},
		{"configured", 6 * time.Hour, 21600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Config{
				AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
				Limiter: allowAll{}, SessionTTL: time.Hour, UniqMax: tc.set,
			})
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(s.Handler())
			defer srv.Close()
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar}
			if code := loginBody(t, client, srv.URL, "admin", "p@ss").StatusCode; code != http.StatusOK {
				t.Fatalf("login = %d", code)
			}
			resp, err := client.Get(srv.URL + "/api/me")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var me struct {
				UniqMaxSeconds int `json:"uniq_max_seconds"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
				t.Fatal(err)
			}
			if me.UniqMaxSeconds != tc.want {
				t.Errorf("uniq_max_seconds = %d, want %d", me.UniqMaxSeconds, tc.want)
			}
		})
	}
}

// TestLoginWithBrokenLimiterIs503BeforeThePassword: a limiter that cannot reach
// Redis answers 503 with the session store text for a right and a wrong password
// alike, sets no cookie and is written to the journal; a limiter that says "too
// many" stays 429.
func TestLoginWithBrokenLimiterIs503BeforeThePassword(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingLogger{}
	srv := loginServer(t, Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: brokenLimiter{}, SessionTTL: time.Hour, Log: rec,
	})
	for _, pass := range []string{"p@ss", "wrong"} {
		resp := loginBody(t, http.DefaultClient, srv.URL, "admin", pass)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("password %q: status = %d, want 503", pass, resp.StatusCode)
		}
		var body struct{ Error string }
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error != errSessionStoreText {
			t.Errorf("password %q: error = %q, %v", pass, body.Error, err)
		}
		if len(resp.Cookies()) != 0 {
			t.Errorf("password %q: a cookie was set", pass)
		}
	}
	logged := false
	for _, line := range rec.lines() {
		if strings.Contains(line, "login limiter unavailable") {
			logged = true
		}
	}
	if !logged {
		t.Errorf("the limiter failure must be in the journal, got %q", rec.lines())
	}

	deny := loginServer(t, Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: denyAll{}, SessionTTL: time.Hour,
	})
	if code := loginBody(t, http.DefaultClient, deny.URL, "admin", "p@ss").StatusCode; code != http.StatusTooManyRequests {
		t.Errorf("a refusing limiter = %d, want 429", code)
	}
}

// denyAll is a limiter that refuses every attempt.
type denyAll struct{}

func (denyAll) Allow(context.Context, string) (bool, error) { return false, nil }
