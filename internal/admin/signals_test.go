package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// getSignals calls GET /api/system/signals and decodes the answer.
func getSignals(t *testing.T, c *http.Client, base string) signalsView {
	t.Helper()
	code, body := callJSON(t, c, http.MethodGet, base+"/api/system/signals", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/system/signals: %d %s", code, body)
	}
	var out signalsView
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if out.Down == nil {
		t.Fatalf("down is null, want a list: %s", body)
	}
	return out
}

// TestSignalsServicesDown checks that down lists the engine, ClickHouse and Redis that do not answer, in
// that order, that answering or not configured services stay out, and that the list is empty when all answer.
func TestSignalsServicesDown(t *testing.T) {
	_, srv, c := newSystemServer(t, func(cfg *Config) { cfg.Stats = &fakePing{} })
	if got := getSignals(t, c, srv.URL); len(got.Down) != 0 || got.Update {
		t.Errorf("all services answer: got %+v, want no marks", got)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, srv, c = newSystemServer(t, func(cfg *Config) {
		cfg.Stats = &fakePing{err: errors.New("refused")}
		cfg.Sessions = &fakePingSessions{SessionStore: cfg.Sessions, err: errors.New("refused")}
		cfg.EngineURL = deadURL
	})
	if got := getSignals(t, c, srv.URL).Down; !reflect.DeepEqual(got, []string{signalEngine, signalClickHouse, signalRedis}) {
		t.Errorf("down = %v, want engine, clickhouse, redis", got)
	}

	_, srv, c = newSystemServer(t, func(cfg *Config) {
		cfg.Sessions = &fakePingSessions{SessionStore: cfg.Sessions, err: errors.New("refused")}
	})
	if got := getSignals(t, c, srv.URL).Down; !reflect.DeepEqual(got, []string{signalRedis}) {
		t.Errorf("down = %v, want only redis", got)
	}
}

// TestSignalsUpdate checks that update follows a fresh check of a newer commit for the running build only:
// a development build, a check of another build, a stale check and no update leave it false.
func TestSignalsUpdate(t *testing.T) {
	dir := t.TempDir()
	_, srv, c := newSystemServer(t, func(cfg *Config) { cfg.UpdateDir = dir })
	writeCheckFile(t, dir, freshCheck())

	cases := []struct {
		name   string
		commit string
		want   bool
	}{
		{"newer commit for this build", validCommit, true},
		{"development build", "", false},
		{"check made for another build", anotherCommit, false},
	}
	for _, tc := range cases {
		restore := config.SetBuildForTest(config.BuildInfo{Version: "v1.0.0", Commit: tc.commit, Channel: "main"})
		got := getSignals(t, c, srv.URL).Update
		restore()
		if got != tc.want {
			t.Errorf("%s: update = %v, want %v", tc.name, got, tc.want)
		}
	}

	restore := config.SetBuildForTest(config.BuildInfo{Version: "v1.0.0", Commit: validCommit, Channel: "main"})
	defer restore()
	stale := freshCheck()
	stale.At = time.Now().Add(-2 * updateCheckFreshAge)
	writeCheckFile(t, dir, stale)
	if getSignals(t, c, srv.URL).Update {
		t.Error("a stale check must not show the update mark")
	}
	none := freshCheck()
	none.Ahead = 0
	writeCheckFile(t, dir, none)
	if getSignals(t, c, srv.URL).Update {
		t.Error("no newer commit must not show the update mark")
	}
}

// TestSignalsAdminSessionOnly checks that a regular user gets 403 from GET /api/system/signals.
func TestSignalsAdminSessionOnly(t *testing.T) {
	s, srv, _ := newSystemServer(t, nil)
	if _, err := s.createUser(userDraft{
		Login: "user1", Password: "pw1234", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: sectionStrings(userSections), Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	login(t, c, srv.URL, "user1", "pw1234")
	if code, body := callJSON(t, c, http.MethodGet, srv.URL+"/api/system/signals", "", ""); code != http.StatusForbidden {
		t.Errorf("regular user: %d %s, want 403", code, body)
	}
}
