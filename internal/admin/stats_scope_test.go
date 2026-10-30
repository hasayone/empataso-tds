package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

type recStats struct {
	fakeStats
	mu   sync.Mutex
	last []store.Cond
}

func (r *recStats) remember(conds []store.Cond) {
	r.mu.Lock()
	r.last = append([]store.Cond(nil), conds...)
	r.mu.Unlock()
}

func (r *recStats) snapshot() []store.Cond {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]store.Cond(nil), r.last...)
}

func (r *recStats) SummaryFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) (store.StatsSummary, error) {
	r.remember(conds)
	return r.fakeStats.SummaryFiltered(ctx, from, to, conds)
}

func (r *recStats) TimeSeriesFiltered(ctx context.Context, from, to time.Time, step int, conds []store.Cond) ([]store.TSPoint, error) {
	r.remember(conds)
	return r.fakeStats.TimeSeriesFiltered(ctx, from, to, step, conds)
}

func (r *recStats) PerformanceFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) ([]store.PerfRow, error) {
	r.remember(conds)
	return r.fakeStats.PerformanceFiltered(ctx, from, to, conds)
}

func (r *recStats) BreakdownFiltered(ctx context.Context, from, to time.Time, dim string, limit int, conds []store.Cond) ([]store.KV, error) {
	r.remember(conds)
	return r.fakeStats.BreakdownFiltered(ctx, from, to, dim, limit, conds)
}

func (r *recStats) SliceFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) ([]store.SliceRow, error) {
	r.remember(conds)
	return r.fakeStats.SliceFiltered(ctx, from, to, conds)
}

func (r *recStats) ClickFactsFiltered(ctx context.Context, from, to time.Time, conds []store.Cond) (store.Facts, error) {
	r.remember(conds)
	return r.fakeStats.ClickFactsFiltered(ctx, from, to, conds)
}

func (r *recStats) Logs(ctx context.Context, f store.LogFilter) ([]store.LogRow, int64, error) {
	r.remember(f.Conds)
	return r.fakeStats.Logs(ctx, f)
}

func (r *recStats) QueryPostbacks(ctx context.Context, q store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error) {
	r.remember(q.Conds)
	return r.fakeStats.QueryPostbacks(ctx, q)
}

func (r *recStats) Values(_ context.Context, q store.ValuesQuery) ([]store.KV, error) {
	r.remember(q.Conds)
	return []store.KV{{Key: "x", Count: 1}}, nil
}

type scopeKeys struct{}

func (scopeKeys) Read(group, _ string, _ bool) (string, error) {
	if group == "c1" {
		return "kw\n", nil
	}
	return "", nil
}

func scopeCampaigns() []config.Group {
	return []config.Group{
		{ID: "c1", Name: "one", Folder: "promo", Status: true, Redirect: "stop"},
		{ID: "c2", Name: "two", Status: true, Redirect: "stop"},
		{ID: "c3", Name: "three", Status: true, Redirect: "stop"},
	}
}

func scopeServer(t *testing.T, st *recStats, spendPath string) (*Server, *httptest.Server) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	groups := &memGroups{list: scopeCampaigns()}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		UsersFile:  filepath.Join(dir, "users.json"),
		Sessions:   security.NewMemoryStore(),
		Stats:      st,
		Groups:     groups,
		Keys:       scopeKeys{},
		SpendPath:  spendPath,
		Limiter:    allowAll{},
		SessionTTL: time.Hour, CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustUser(t, s, "sel", "secret1", security.Access{
		Sections: userSectionsAll(),
		Hidden:   []string{"profit"},
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeSelected, IDs: []string{"c1"}, Labels: []string{"promo"}},
		},
	})
	mustUser(t, s, "none", "secret2", security.Access{
		Sections: userSectionsAll(),
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeSelected},
		},
	})
	mustUser(t, s, "reader", "secret3", security.Access{
		Sections: userSectionsAll(),
		Hidden:   []string{"cost"},
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeRead},
		},
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func scopeCond(conds []store.Cond) (store.Cond, bool) {
	for _, c := range conds {
		if c.Scope && c.Field == "campaign_id" && c.Op == "in" {
			return c, true
		}
	}
	return store.Cond{}, false
}

func TestStatsScopeConds(t *testing.T) {
	st := &recStats{}
	_, srv := scopeServer(t, st, "")

	sel := newClient(t)
	if code, _ := login(t, sel, srv.URL, "sel", "secret1"); code != http.StatusOK {
		t.Fatalf("sel login %d", code)
	}
	resp, err := sel.Get(srv.URL + "/api/stats/summary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary %d", resp.StatusCode)
	}
	sc, ok := scopeCond(st.snapshot())
	if !ok || len(sc.Values) != 1 || sc.Values[0] != "c1" {
		t.Fatalf("scope cond %+v", st.snapshot())
	}

	resp, err = sel.Get(srv.URL + "/api/stats/summary?f=country:eq:ru")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	last := st.snapshot()
	if len(last) != 2 {
		t.Fatalf("want 2 conds got %+v", last)
	}
	if _, ok := scopeCond(last); !ok {
		t.Fatalf("missing scope in %+v", last)
	}

	root := newClient(t)
	login(t, root, srv.URL, "admin", "p@ss")
	st.mu.Lock()
	st.last = nil
	st.mu.Unlock()
	resp, err = root.Get(srv.URL + "/api/stats/summary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	st.mu.Lock()
	rootLast := append([]store.Cond(nil), st.last...)
	st.mu.Unlock()
	sc, ok = scopeCond(rootLast)
	if !ok || len(sc.Values) != 3 {
		t.Fatalf("admin in main gets scope for section campaigns: %+v", rootLast)
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		found := false
		for _, v := range sc.Values {
			if v == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("admin scope missing %s: %+v", id, sc.Values)
		}
	}

	none := newClient(t)
	login(t, none, srv.URL, "none", "secret2")
	resp, err = none.Get(srv.URL + "/api/stats/summary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sc, ok = scopeCond(st.snapshot())
	if !ok || len(sc.Values) != 1 || sc.Values[0] != store.ScopeNone {
		t.Fatalf("none scope %+v", st.snapshot())
	}
}

func TestStatsStripCostKeys(t *testing.T) {
	keys := statsStripKeys(viewer{hidden: map[string]bool{"cost": true}}, nil)
	for _, k := range []string{"cost_cur", "cost", "cost_cents"} {
		if !keys[k] {
			t.Fatalf("hidden cost missing %s in %#v", k, keys)
		}
	}
	raw := []any{
		map[string]any{
			"hits": float64(2),
			"cost": map[string]any{"USD": float64(150)},
		},
	}
	got := stripKeys(raw, keys).([]any)
	row := got[0].(map[string]any)
	if _, ok := row["cost"]; ok {
		t.Fatalf("nested cost kept: %#v", row)
	}
	if row["hits"] != float64(2) {
		t.Fatalf("hits %#v", row)
	}
}

func TestHiddenFilterProfit(t *testing.T) {
	st := &recStats{}
	_, srv := scopeServer(t, st, "")
	sel := newClient(t)
	login(t, sel, srv.URL, "sel", "secret1")
	resp, err := sel.Get(srv.URL + "/api/stats/summary?f=profit:eq:1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "hidden field profit") {
		t.Fatalf("sel profit filter: %d %s", resp.StatusCode, body)
	}

	reader := newClient(t)
	login(t, reader, srv.URL, "reader", "secret3")
	resp, err = reader.Get(srv.URL + "/api/postbacks?f=profit:eq:1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reader postbacks profit filter: %d", resp.StatusCode)
	}
}

func TestHiddenMoneyInJSON(t *testing.T) {
	st := &recStats{}
	_, srv := scopeServer(t, st, "")
	sel := newClient(t)
	login(t, sel, srv.URL, "sel", "secret1")

	resp, _ := sel.Get(srv.URL + "/api/stats/performance")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), `"profit"`) {
		t.Fatalf("performance still has profit: %s", body)
	}
	if !strings.Contains(string(body), `"hits"`) {
		t.Fatalf("performance missing hits: %s", body)
	}

	resp, _ = sel.Get(srv.URL + "/api/stats/timeseries")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), `"revenue"`) {
		t.Fatalf("timeseries still has revenue: %s", body)
	}

	resp, _ = sel.Get(srv.URL + "/api/postbacks")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), `"profit"`) || strings.Contains(string(body), `"total"`) {
		t.Fatalf("postbacks money keys: %s", body)
	}
	if !strings.Contains(string(body), `"count"`) {
		t.Fatalf("postbacks missing count: %s", body)
	}

	resp, _ = sel.Get(srv.URL + "/api/logs")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"total"`) {
		t.Fatalf("logs total missing: %s", body)
	}

	root := newClient(t)
	login(t, root, srv.URL, "admin", "p@ss")
	resp, _ = root.Get(srv.URL + "/api/stats/performance")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"profit"`) {
		t.Fatalf("root performance missing profit: %s", body)
	}
}

func TestSpendScopeAndCostHidden(t *testing.T) {
	dir := t.TempDir()
	spendPath := filepath.Join(dir, "spend.json")
	entries := []config.SpendEntry{
		{ID: "1", GroupID: "c1", Cents: 100, Currency: "USD"},
		{ID: "2", GroupID: "c2", Cents: 200, Currency: "USD"},
	}
	b, _ := json.MarshalIndent(entries, "", "  ")
	if err := os.WriteFile(spendPath, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &recStats{}
	_, srv := scopeServer(t, st, spendPath)

	reader := newClient(t)
	login(t, reader, srv.URL, "reader", "secret3")
	resp, _ := reader.Get(srv.URL + "/api/spend")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader spend %d", resp.StatusCode)
	}

	sel := newClient(t)
	login(t, sel, srv.URL, "sel", "secret1")
	resp, _ = sel.Get(srv.URL + "/api/spend")
	var list []config.SpendEntry
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(list) != 1 || list[0].GroupID != "c1" {
		t.Fatalf("sel spend %+v status %d", list, resp.StatusCode)
	}

	root := newClient(t)
	login(t, root, srv.URL, "admin", "p@ss")
	resp, _ = root.Get(srv.URL + "/api/spend")
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 2 {
		t.Fatalf("root spend len %d", len(list))
	}
}

func TestLogValuesAndKeysScope(t *testing.T) {
	st := &recStats{}
	_, srv := scopeServer(t, st, "")
	sel := newClient(t)
	login(t, sel, srv.URL, "sel", "secret1")

	resp, _ := sel.Get(srv.URL + "/api/log-values?table=events&field=country")
	resp.Body.Close()
	if _, ok := scopeCond(st.snapshot()); !ok {
		t.Fatalf("log-values conds %+v", st.snapshot())
	}

	resp, _ = sel.Get(srv.URL + "/api/logs/filters")
	resp.Body.Close()
	if _, ok := scopeCond(st.snapshot()); !ok {
		t.Fatalf("log filters conds %+v", st.snapshot())
	}

	resp, _ = sel.Get(srv.URL + "/api/keys?group=c2&date=2026-01-01")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("keys c2 %d", resp.StatusCode)
	}
	resp, _ = sel.Get(srv.URL + "/api/keys?group=c1&date=2026-01-01")
	var out struct{ Content string }
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || out.Content != "kw\n" {
		t.Fatalf("keys c1 %d content %q", resp.StatusCode, out.Content)
	}
}
