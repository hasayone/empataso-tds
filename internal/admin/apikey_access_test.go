package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

// expectedActionRoutes is an independent copy of the action table.
var expectedActionRoutes = func() map[string]string {
	m := make(map[string]string)
	for _, e := range [][2]string{
		{"POST /api/keitaro/import", security.ActionKeitaroImport},
		{"POST /api/keitaro/purge", security.ActionKeitaroPurge},
		{"DELETE /api/logs", security.ActionLogsDelete},
		{"PUT /api/groups", security.ActionGroupsReplace},
		{"PUT /api/cron", security.ActionCron},
		{"POST /api/cron/run", security.ActionCron},
		{geoDownloadPattern, security.ActionCron},
		{"POST /api/boards", security.ActionBoards},
		{"PUT /api/boards/order", security.ActionBoards},
		{"PATCH /api/boards/{id}", security.ActionBoards},
		{"PUT /api/boards/{id}/members", security.ActionBoards},
		{"POST /api/boards/{id}/assign", security.ActionBoards},
		{"DELETE /api/boards/{id}", security.ActionBoards},
		{"POST /api/boards/move", security.ActionBoards},
		{"POST /api/boards/import", security.ActionBoards},
		{"DELETE /api/landings/{id}", security.ActionLandingFiles},
		{"POST /api/landings/{id}/archive", security.ActionLandingFiles},
		{"PUT /api/landings/{id}/files/{path...}", security.ActionLandingFiles},
		{"DELETE /api/landings/{id}/files/{path...}", security.ActionLandingFiles},
		{"GET /api/lists", security.ActionListsRead},
		{"GET /api/lists/{name}", security.ActionListsRead},
	} {
		m[e[0]] = e[1]
	}
	return m
}()

// expectedHeavyRoutes is an independent copy of the heavy route set.
var expectedHeavyRoutes = map[string]bool{
	"GET /api/logs/export":       true,
	"GET /api/postbacks/export":  true,
	"GET /api/stats/facts":       true,
	"GET /api/postback-log":      true,
	"GET /api/stats/summary":     true,
	"GET /api/stats/timeseries":  true,
	"GET /api/stats/breakdown":   true,
	"GET /api/stats/performance": true,
	"GET /api/report":            true,
	"GET /api/logs":              true,
	"GET /api/log-values":        true,
	"GET /api/logs/filters":      true,
	"GET /api/postbacks":         true,
	"GET /api/keywords/stats":    true,
	"GET /api/keywords/trend":    true,
}

func TestRouteFlagsMatchTables(t *testing.T) {
	for p, rule := range routeAccess {
		if rule.cookie != cookieOnlyRoutes[p] {
			t.Errorf("%s: cookie flag %v, oracle %v", p, rule.cookie, cookieOnlyRoutes[p])
		}
		if rule.action != expectedActionRoutes[p] {
			t.Errorf("%s: action %q, oracle %q", p, rule.action, expectedActionRoutes[p])
		}
		if rule.heavy != expectedHeavyRoutes[p] {
			t.Errorf("%s: heavy flag %v, oracle %v", p, rule.heavy, expectedHeavyRoutes[p])
		}
	}
	for p := range cookieOnlyRoutes {
		if _, ok := routeAccess[p]; !ok {
			t.Errorf("cookie oracle route %q has no rule", p)
		}
	}
	for p := range expectedActionRoutes {
		if _, ok := routeAccess[p]; !ok {
			t.Errorf("action oracle route %q has no rule", p)
		}
	}
	for p := range expectedHeavyRoutes {
		if _, ok := routeAccess[p]; !ok {
			t.Errorf("heavy oracle route %q has no rule", p)
		}
	}
	for _, action := range security.AllActions() {
		found := false
		for _, a := range expectedActionRoutes {
			found = found || a == action
		}
		if !found {
			t.Errorf("action %q guards no route", action)
		}
	}
}

// limitStats records the query asked of PostbackLog and answers rows.
type limitStats struct {
	richStats
	limit int
	query store.PostbackLogQuery
	rows  []store.PostbackLogRow
}

func (l *limitStats) PostbackLog(_ context.Context, q store.PostbackLogQuery) ([]store.PostbackLogRow, error) {
	l.limit, l.query = q.Limit, q
	if l.rows == nil {
		return []store.PostbackLogRow{}, nil
	}
	return l.rows, nil
}

// keyRig is an admin server with users, roles, offers and an API key file.
type keyRig struct {
	s     *Server
	srv   *httptest.Server
	admin *http.Client
	csrf  string
}

func newKeyRig(t *testing.T, stats StatsProvider) *keyRig {
	t.Helper()
	dir := t.TempDir()
	s := newRolesServerStats(t, dir, stats)
	s.cfg.APIKeysFile = filepath.Join(dir, "api-keys.json")
	s.loadAPIKeys()
	s.cfg.CronFile = filepath.Join(dir, "cron.json")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	rig := &keyRig{s: s, srv: srv, admin: newClient(t)}
	code, csrf := login(t, rig.admin, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("admin login %d", code)
	}
	rig.csrf = csrf
	return rig
}

// newKey stores a key with ka and returns its id and raw secret.
func (k *keyRig) newKey(t *testing.T, ka security.KeyAccess) (id, raw string) {
	t.Helper()
	k.s.keyMu.Lock()
	defer k.s.keyMu.Unlock()
	raw, key, err := k.s.keys.GenerateWith("k", []string{security.ScopeAdmin}, ka)
	if err != nil {
		t.Fatal(err)
	}
	return key.ID, raw
}

// call sends a request authenticated by the Api-Key raw.
func (k *keyRig) call(t *testing.T, raw, method, path string, body any) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, k.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// asAdmin sends a request as the cookie administrator.
func (k *keyRig) asAdmin(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	code, raw, err := doJSON(k.admin, method, k.srv.URL+path, k.csrf, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
}

func TestAPIKeyActionGate(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, plain := rig.newKey(t, security.KeyAccess{})
	code, body := rig.call(t, plain, http.MethodDelete, "/api/logs?group=g1", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "action required: logs_delete") {
		t.Fatalf("key without actions: %d %s", code, body)
	}
	if code, _ := rig.call(t, plain, http.MethodGet, "/api/offers", nil); code != http.StatusOK {
		t.Fatalf("plain read: %d", code)
	}

	_, granted := rig.newKey(t, security.KeyAccess{Type: security.KeyTypeAdmin, Actions: []string{security.ActionLogsDelete}})
	if code, body := rig.call(t, granted, http.MethodDelete, "/api/logs?group=g1", nil); code != http.StatusOK {
		t.Fatalf("key with logs_delete: %d %s", code, body)
	}
	if code, _ := rig.call(t, granted, http.MethodPut, "/api/cron", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("logs_delete must not unlock cron: %d", code)
	}

	_, all := rig.newKey(t, security.KeyAccess{Actions: security.AllActions()})
	for _, p := range []string{"/api/keitaro/settings", "/api/api-keys", "/api/me", "/api/cloudflare"} {
		code, body := rig.call(t, all, http.MethodGet, p, nil)
		if code != http.StatusForbidden || !strings.Contains(body, "cookie session required") {
			t.Fatalf("%s with all actions: %d %s", p, code, body)
		}
	}

	if code, body := rig.asAdmin(t, http.MethodDelete, "/api/logs?group=g1", nil); code != http.StatusOK {
		t.Fatalf("cookie admin is not limited by actions: %d %s", code, body)
	}
}

func TestUserKeyGrantsLikeUser(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, readKey := rig.newKey(t, security.KeyAccess{
		Type: security.KeyTypeUser,
		Access: &security.Access{
			Sections: []string{"offers"},
			Grants:   map[string]security.Grant{"offers": {Mode: security.ModeRead}},
		},
	})
	if code, body := rig.call(t, readKey, http.MethodGet, "/api/offers", nil); code != http.StatusOK {
		t.Fatalf("read offers: %d %s", code, body)
	}
	offer := config.Offer{Name: "n", URL: "https://n.example/", Status: true}
	if code, _ := rig.call(t, readKey, http.MethodPost, "/api/offers", offer); code != http.StatusForbidden {
		t.Fatalf("read grant creates offer: %d", code)
	}
	if code, _ := rig.call(t, readKey, http.MethodGet, "/api/landings", nil); code != http.StatusForbidden {
		t.Fatalf("section not granted: %d", code)
	}
	if code, _ := rig.call(t, readKey, http.MethodGet, "/api/users", nil); code != http.StatusForbidden {
		t.Fatalf("admin route for USER key: %d", code)
	}
}

func TestUserKeyCreatedModeRecordsKey(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	if code, body := rig.asAdmin(t, http.MethodPost, "/api/offers", config.Offer{ID: "o_admin", Name: "A", URL: "https://a.example/", Status: true}); code != http.StatusCreated {
		t.Fatalf("admin offer: %d %s", code, body)
	}
	id, raw := rig.newKey(t, security.KeyAccess{
		Type: security.KeyTypeUser,
		Access: &security.Access{
			Sections: []string{"offers"},
			Grants:   map[string]security.Grant{"offers": {Mode: security.ModeCreated}},
		},
	})
	code, body := rig.call(t, raw, http.MethodPost, "/api/offers", config.Offer{ID: "o_key", Name: "K", URL: "https://k.example/", Status: true})
	if code != http.StatusCreated {
		t.Fatalf("key offer: %d %s", code, body)
	}
	var created config.Offer
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.CreatedBy != "apikey:"+id {
		t.Fatalf("created_by %q err %v", created.CreatedBy, err)
	}
	code, body = rig.call(t, raw, http.MethodGet, "/api/offers", nil)
	var list []config.Offer
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || len(list) != 1 || list[0].ID != "o_key" {
		t.Fatalf("created mode list: %d %s", code, body)
	}
}

func TestUserKeyRoleLimitsSections(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	role, err := rig.s.roles.Add(security.Role{Name: "offers only", Access: security.Access{Sections: []string{"offers"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, raw := rig.newKey(t, security.KeyAccess{Type: security.KeyTypeUser, RoleID: role.ID})
	if code, body := rig.call(t, raw, http.MethodGet, "/api/offers", nil); code != http.StatusOK {
		t.Fatalf("role section: %d %s", code, body)
	}
	for _, p := range []string{"/api/landings", "/api/logs", "/api/stats/summary"} {
		if code, _ := rig.call(t, raw, http.MethodGet, p, nil); code != http.StatusForbidden {
			t.Fatalf("%s outside the role: %d", p, code)
		}
	}
	if err := rig.s.roles.Remove(role.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := rig.call(t, raw, http.MethodGet, "/api/offers", nil); code != http.StatusUnauthorized {
		t.Fatalf("deleted role: %d, want 401", code)
	}
}

func TestKeySessionKinds(t *testing.T) {
	s := accessTestServer(t)
	adminSess := security.Session{API: true, KeyType: security.KeyTypeAdmin}
	legacy := security.Session{API: true}
	userSess := security.Session{API: true, KeyType: security.KeyTypeUser, KeyID: "abc"}
	odd := security.Session{API: true, KeyType: "WEIRD"}
	if !s.isAdmin(adminSess) || !s.isAdmin(legacy) || s.isAdmin(userSess) || s.isAdmin(odd) {
		t.Fatal("isAdmin by key type")
	}
	if !s.canManageBoards(adminSess) || s.canManageBoards(userSess) {
		t.Fatal("canManageBoards by key type")
	}
	if _, ok := s.sessionUser(adminSess); ok {
		t.Fatal("ADMIN key has no user")
	}
	u, ok := s.sessionUser(userSess)
	if !ok || u.ID != "apikey:abc" || u.Type != security.UserRegular {
		t.Fatalf("USER key user: %+v %v", u, ok)
	}
	if got := sessionUID(userSess); got != "apikey:abc" {
		t.Fatalf("uid %q", got)
	}
	if got := sessionUID(adminSess); got != "" {
		t.Fatalf("admin key uid %q", got)
	}
}

func TestAPIKeyCreateAndUpdateAccess(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	role, err := rig.s.roles.Add(security.Role{Name: "r1", Access: security.Access{Sections: []string{"offers"}}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(body map[string]any) (int, []byte) {
		return rig.asAdmin(t, http.MethodPost, "/api/api-keys", body)
	}
	for name, body := range map[string]map[string]any{
		"unknown role":    {"name": "a", "scopes": []string{"admin"}, "type": "USER", "role_id": "nope"},
		"typo type":       {"name": "b", "scopes": []string{"admin"}, "type": "user"},
		"typo action":     {"name": "c", "scopes": []string{"admin"}, "actions": []string{"logs-delete"}},
		"user cron":       {"name": "d", "scopes": []string{"admin"}, "type": "USER", "actions": []string{"cron"}},
		"user boards":     {"name": "e", "scopes": []string{"admin"}, "type": "USER", "actions": []string{"boards"}},
		"role and access": {"name": "f", "scopes": []string{"admin"}, "type": "USER", "role_id": role.ID, "access": map[string]any{"sections": []string{"offers"}}},
		"admin section":   {"name": "g", "scopes": []string{"admin"}, "type": "USER", "access": map[string]any{"sections": []string{"users"}}},
	} {
		if code, raw := create(body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
	if n := len(rig.s.keys.List()); n != 0 {
		t.Fatalf("refused requests stored %d keys", n)
	}

	code, raw := create(map[string]any{
		"name": "ok", "scopes": []string{"admin"}, "type": "USER", "role_id": role.ID,
		"actions": []string{"logs_delete", "logs_delete"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}
	var made apiKeyCreated
	if err := json.Unmarshal(raw, &made); err != nil {
		t.Fatal(err)
	}
	if made.Item.Type != security.KeyTypeUser || made.Item.RoleID != role.ID || !slices.Equal(made.Item.Actions, []string{"logs_delete"}) {
		t.Fatalf("created item: %+v", made.Item)
	}

	path := "/api/api-keys/" + made.Item.ID
	if code, raw := rig.asAdmin(t, http.MethodPut, path, map[string]any{"name": "renamed", "scopes": []string{"admin", "engine"}}); code != http.StatusOK {
		t.Fatalf("legacy update: %d %s", code, raw)
	}
	got, _ := rig.s.keys.Get(made.Item.ID)
	if got.Name != "renamed" || got.Type != security.KeyTypeUser || got.RoleID != role.ID || !slices.Equal(got.Actions, []string{"logs_delete"}) {
		t.Fatalf("legacy update changed access: %+v", got)
	}

	if code, raw := rig.asAdmin(t, http.MethodPut, path, map[string]any{"actions": []string{"cron"}}); code != http.StatusBadRequest {
		t.Fatalf("cron on USER key: %d %s", code, raw)
	}
	got, _ = rig.s.keys.Get(made.Item.ID)
	if got.Name != "renamed" || !slices.Equal(got.Actions, []string{"logs_delete"}) {
		t.Fatalf("refused update changed the key: %+v", got)
	}

	if code, raw := rig.asAdmin(t, http.MethodPut, path, map[string]any{"type": "ADMIN", "actions": []string{"cron"}}); code != http.StatusOK {
		t.Fatalf("to ADMIN: %d %s", code, raw)
	}
	got, _ = rig.s.keys.Get(made.Item.ID)
	if got.Type != security.KeyTypeAdmin || got.RoleID != "" || !slices.Equal(got.Actions, []string{"cron"}) {
		t.Fatalf("after type change: %+v", got)
	}

	if code, raw := rig.asAdmin(t, http.MethodPut, path, map[string]any{"actions": []string{}}); code != http.StatusOK {
		t.Fatalf("clear actions: %d %s", code, raw)
	}
	got, _ = rig.s.keys.Get(made.Item.ID)
	if len(got.Actions) != 0 {
		t.Fatalf("actions not cleared: %+v", got)
	}

	if code, _ := rig.asAdmin(t, http.MethodPut, "/api/api-keys/unknown", map[string]any{"name": "x"}); code != http.StatusNotFound {
		t.Fatalf("unknown key: %d", code)
	}
}

func TestAPIKeySwitchRoleToAccess(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	role, err := rig.s.roles.Add(security.Role{Name: "r1", Access: security.Access{Sections: []string{"offers"}}})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := rig.newKey(t, security.KeyAccess{Type: security.KeyTypeUser, RoleID: role.ID})
	path := "/api/api-keys/" + id
	code, raw := rig.asAdmin(t, http.MethodPut, path, map[string]any{"access": map[string]any{"sections": []string{"landings"}}})
	if code != http.StatusOK {
		t.Fatalf("role to access: %d %s", code, raw)
	}
	got, _ := rig.s.keys.Get(id)
	if got.RoleID != "" || got.Access == nil || !slices.Equal(got.Access.Sections, []string{"landings"}) {
		t.Fatalf("after switch: %+v", got)
	}
	code, raw = rig.asAdmin(t, http.MethodPut, path, map[string]any{"role_id": role.ID})
	if code != http.StatusOK {
		t.Fatalf("access to role: %d %s", code, raw)
	}
	got, _ = rig.s.keys.Get(id)
	if got.RoleID != role.ID || got.Access != nil {
		t.Fatalf("after switch back: %+v", got)
	}
}

func TestCronGetThroughKeyHidesSecrets(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	cfg := cron.Default()
	cfg.Telegram.Token = "123456:SECRET-TOKEN"
	cfg.Telegram.ChatID = "42"
	if err := cron.Save(rig.s.cfg.CronFile, cfg); err != nil {
		t.Fatal(err)
	}
	_, raw := rig.newKey(t, security.KeyAccess{})
	code, body := rig.call(t, raw, http.MethodGet, "/api/cron", nil)
	if code != http.StatusOK {
		t.Fatalf("cron get: %d %s", code, body)
	}
	if strings.Contains(body, "SECRET-TOKEN") || !strings.Contains(body, cron.SecretMask) {
		t.Fatalf("cron body leaks or lacks mask: %s", body)
	}
}

func TestPostbackLogLimitClamp(t *testing.T) {
	stats := &limitStats{}
	rig := newKeyRig(t, stats)
	for query, want := range map[string]int{"": 200, "?limit=5": 5, "?limit=99999": postbackLogMaxLimit, "?limit=1000": 1000} {
		if code, raw := rig.asAdmin(t, http.MethodGet, "/api/postback-log"+query, nil); code != http.StatusOK {
			t.Fatalf("postback-log%s: %d %s", query, code, raw)
		}
		if stats.limit != want {
			t.Fatalf("postback-log%s asked limit %d, want %d", query, stats.limit, want)
		}
	}
}

// breakdownErrStats returns the given error from every Breakdown call; used to test
// handleLogFilters error handling.
type breakdownErrStats struct {
	richStats
	err error
}

// Breakdown returns the configured error for every dimension.
func (b *breakdownErrStats) Breakdown(_ context.Context, _, _ time.Time, _ string, _ int) ([]store.KV, error) {
	return nil, b.err
}

// BreakdownFiltered returns the configured error for every dimension.
func (b *breakdownErrStats) BreakdownFiltered(_ context.Context, _, _ time.Time, _ string, _ int, _ []store.Cond) ([]store.KV, error) {
	return nil, b.err
}

// TestCookieOnlyRoutesBlockAPIKeys verifies that cookie-only routes return 403
// "cookie session required" to an ADMIN API key with all actions.
func TestCookieOnlyRoutesBlockAPIKeys(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, all := rig.newKey(t, security.KeyAccess{Type: security.KeyTypeAdmin, Actions: security.AllActions()})
	for _, p := range []string{
		"POST /api/registrars/namecheap/check",
		"POST /api/keitaro/check",
	} {
		method, path, _ := strings.Cut(p, " ")
		code, body := rig.call(t, all, method, path, map[string]any{})
		if code != http.StatusForbidden || !strings.Contains(body, "cookie session required") {
			t.Errorf("%s with all-actions key: %d %s", p, code, body)
		}
	}
	// Cookie admin is not affected.
	for _, p := range []string{
		"POST /api/registrars/namecheap/check",
		"POST /api/keitaro/check",
	} {
		method, path, _ := strings.Cut(p, " ")
		code, body := rig.asAdmin(t, method, path, map[string]any{})
		// 400 or 503 is fine — the route was reached.
		if code == http.StatusForbidden && strings.Contains(string(body), "cookie session required") {
			t.Errorf("cookie admin blocked from %s: %d %s", p, code, string(body))
		}
	}
}

// TestCronRunTelegramTestBlocksAPIKey checks that POST /api/cron/run with job
// telegram_test returns 403 "cookie session required" for an API key, even one
// with the cron action, while a cookie session may call it.
func TestCronRunTelegramTestBlocksAPIKey(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, cronKey := rig.newKey(t, security.KeyAccess{Type: security.KeyTypeAdmin, Actions: []string{security.ActionCron}})
	body := map[string]any{"job": cron.JobTelegramTest}
	code, resp := rig.call(t, cronKey, http.MethodPost, "/api/cron/run", body)
	if code != http.StatusForbidden || !strings.Contains(resp, "cookie session required") {
		t.Errorf("cron key on telegram_test: %d %s", code, resp)
	}
	// Cookie admin reaches the handler (not 403 cookie_required).
	code2, resp2 := rig.asAdmin(t, http.MethodPost, "/api/cron/run", body)
	if code2 == http.StatusForbidden && strings.Contains(string(resp2), "cookie session required") {
		t.Errorf("cookie admin blocked from telegram_test: %d %s", code2, string(resp2))
	}
}

// TestBoardActionGate checks that POST /api/boards and other new board write routes
// require the boards action from an API key; with the action they are accepted;
// cookie admin is never blocked.
func TestBoardActionGate(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, plain := rig.newKey(t, security.KeyAccess{})
	boardRoutes := []string{
		"POST /api/boards",
		"PUT /api/boards/order",
		"PATCH /api/boards/main",
		"PUT /api/boards/main/members",
		"POST /api/boards/main/assign",
	}
	for _, p := range boardRoutes {
		method, path, _ := strings.Cut(p, " ")
		code, body := rig.call(t, plain, method, path, map[string]any{})
		if code != http.StatusForbidden || !strings.Contains(body, "action required: boards") {
			t.Errorf("plain key on %s: %d %s", p, code, body)
		}
	}
	_, boardKey := rig.newKey(t, security.KeyAccess{Type: security.KeyTypeAdmin, Actions: []string{security.ActionBoards}})
	for _, p := range boardRoutes {
		method, path, _ := strings.Cut(p, " ")
		code, _ := rig.call(t, boardKey, method, path, map[string]any{"name": "x"})
		if code == http.StatusForbidden {
			t.Errorf("boards key blocked from %s: %d", p, code)
		}
	}
	for _, p := range boardRoutes {
		method, path, _ := strings.Cut(p, " ")
		code, body := rig.asAdmin(t, method, path, map[string]any{"name": "x"})
		if code == http.StatusForbidden && strings.Contains(string(body), "cookie session required") {
			t.Errorf("cookie admin blocked from %s: %d %s", p, code, string(body))
		}
	}
}

// TestLandingDeleteActionGate verifies that DELETE /api/landings/{id} requires
// the landing_files action from an API key.
func TestLandingDeleteActionGate(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	// Create a landing first.
	code, raw := rig.asAdmin(t, http.MethodPost, "/api/landings",
		config.Landing{ID: "l_del", Name: "Del me", URL: "https://del.example/"})
	if code != http.StatusCreated {
		t.Fatalf("create landing: %d %s", code, raw)
	}
	_, plain := rig.newKey(t, security.KeyAccess{})
	code, body := rig.call(t, plain, http.MethodDelete, "/api/landings/l_del", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "action required: landing_files") {
		t.Fatalf("plain key on DELETE landing: %d %s", code, body)
	}
	_, lf := rig.newKey(t, security.KeyAccess{Type: security.KeyTypeAdmin, Actions: []string{security.ActionLandingFiles}})
	code, body = rig.call(t, lf, http.MethodDelete, "/api/landings/l_del", nil)
	if code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("landing_files key on DELETE landing: %d %s", code, body)
	}
}

// TestListsReadActionGate verifies that GET /api/lists and GET /api/lists/{name}
// require the lists_read action from an API key.
func TestListsReadActionGate(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, plain := rig.newKey(t, security.KeyAccess{})
	for _, path := range []string{"/api/lists", "/api/lists/ip_test.dat"} {
		code, body := rig.call(t, plain, http.MethodGet, path, nil)
		if code != http.StatusForbidden || !strings.Contains(body, "action required: lists_read") {
			t.Errorf("plain key on %s: %d %s", path, code, body)
		}
	}
	rig.s.cfg.Lists = &fakeLists{data: map[string]string{"ip_test.dat": "1.2.3.4\n"}}
	_, lrKey := rig.newKey(t, security.KeyAccess{Actions: []string{security.ActionListsRead}})
	for _, path := range []string{"/api/lists", "/api/lists/ip_test.dat"} {
		code, body := rig.call(t, lrKey, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Errorf("lists_read key on %s: %d %s", path, code, body)
		}
	}
	// Cookie admin is not affected.
	for _, path := range []string{"/api/lists", "/api/lists/ip_test.dat"} {
		code, body := rig.asAdmin(t, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Errorf("cookie admin on %s: %d %s", path, code, string(body))
		}
	}
}

// emptyGroupsServer builds a minimal Server with an empty memGroups store,
// no pre-seeded campaigns, and returns its URL and a cookie admin client.
func emptyGroupsServer(t *testing.T) (srvURL, csrf string, do func(method, path string, body any) (int, []byte)) {
	t.Helper()
	dir := t.TempDir()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        richStats{},
		Groups:       &memGroups{},
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:     NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:      NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:     NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		Keys:         fakeKeys{},
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		UsersFile:    filepath.Join(dir, "users.json"),
		APIKeysFile:  filepath.Join(dir, "api-keys.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	admin := newClient(t)
	code, csrf2 := login(t, admin, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	do = func(method, path string, body any) (int, []byte) {
		c, raw, e := doJSON(admin, method, srv.URL+path, csrf2, body)
		if e != nil {
			t.Fatal(e)
		}
		return c, raw
	}
	return srv.URL, csrf2, do
}

// TestGroupsSaveEmptyBodyConflict verifies that PUT /api/groups with an empty array
// returns 409 when stored campaigns exist, and that DELETE-then-PUT succeeds.
// Uses a dedicated server with empty memGroups so pre-seeded scope campaigns do not interfere.
func TestGroupsSaveEmptyBodyConflict(t *testing.T) {
	_, _, do := emptyGroupsServer(t)

	// Seed one campaign via PUT /api/groups/{id}.
	grp := config.Group{ID: "gc1", Num: 1}
	if code, raw := do(http.MethodPut, "/api/groups/gc1", grp); code != http.StatusOK {
		t.Fatalf("create group: %d %s", code, raw)
	}
	// Empty PUT while stored is non-empty → 409.
	if code, body := do(http.MethodPut, "/api/groups", []config.Group{}); code != http.StatusConflict ||
		!strings.Contains(string(body), "empty list would delete campaigns") {
		t.Fatalf("empty PUT: %d %s", code, string(body))
	}
	// SPA flow: DELETE first, then PUT [].
	if code, raw := do(http.MethodDelete, "/api/groups/gc1", nil); code != http.StatusOK {
		t.Fatalf("delete group: %d %s", code, raw)
	}
	// Store is now empty; PUT [] must succeed.
	if code, body := do(http.MethodPut, "/api/groups", []config.Group{}); code != http.StatusOK {
		t.Fatalf("PUT after DELETE: %d %s", code, string(body))
	}
}

// TestLogFiltersErrFieldNotReady verifies that handleLogFilters returns 200 with
// an empty list for a dimension when ErrFieldNotReady is returned, and 500 for
// any other error.
func TestLogFiltersErrFieldNotReady(t *testing.T) {
	stats := &breakdownErrStats{err: store.ErrFieldNotReady}
	rig := newKeyRig(t, stats)
	code, raw := rig.asAdmin(t, http.MethodGet, "/api/logs/filters", nil)
	if code != http.StatusOK {
		t.Fatalf("ErrFieldNotReady: %d %s", code, raw)
	}
	var out map[string][]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for _, dim := range []string{"group", "stream", "country", "device", "os", "browser", "brand"} {
		if v, ok := out[dim]; !ok || v == nil {
			t.Errorf("dim %q absent or nil on ErrFieldNotReady", dim)
		}
	}

	stats2 := &breakdownErrStats{err: errors.New("simulated stats failure")}
	rig2 := newKeyRig(t, stats2)
	code2, raw2 := rig2.asAdmin(t, http.MethodGet, "/api/logs/filters", nil)
	if code2 != http.StatusInternalServerError {
		t.Fatalf("non-ready error: %d %s", code2, raw2)
	}
	if !strings.Contains(string(raw2), "stats error") {
		t.Fatalf("non-ready error body: %s", string(raw2))
	}
}
