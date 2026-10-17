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

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

type fakeStats struct{}

func (f fakeStats) Summary(ctx context.Context, from, to time.Time) (store.StatsSummary, error) {
	return store.StatsSummary{Total: 100, Unique: 70, Bots: 12}, nil
}
func (f fakeStats) SummaryFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) (store.StatsSummary, error) {
	return f.Summary(ctx, from, to)
}
func (f fakeStats) TimeSeries(ctx context.Context, from, to time.Time, step int) ([]store.TSPoint, error) {
	return []store.TSPoint{{Total: 100, Unique: 70, Bots: 12, Revenue: 9.5}}, nil
}
func (f fakeStats) TimeSeriesFiltered(ctx context.Context, from, to time.Time, step int, _ []store.Cond) ([]store.TSPoint, error) {
	return f.TimeSeries(ctx, from, to, step)
}
func (fakeStats) Breakdown(context.Context, time.Time, time.Time, string, int) ([]store.KV, error) {
	return []store.KV{{Key: "ru", Count: 50}}, nil
}

// BreakdownFiltered returns the same rows as Breakdown.
// conds are ignored. It does not fail.
func (f fakeStats) BreakdownFiltered(ctx context.Context, from, to time.Time, dim string, limit int, _ []store.Cond) ([]store.KV, error) {
	return f.Breakdown(ctx, from, to, dim, limit)
}
func (f fakeStats) Performance(ctx context.Context, from, to time.Time) ([]store.PerfRow, error) {
	return []store.PerfRow{{GroupID: "g1", Group: "demo", Stream: "s1", Hits: 80, Unique: 60, Bots: 5, Conv: 3, Profit: 4.5}}, nil
}
func (f fakeStats) PerformanceFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) ([]store.PerfRow, error) {
	return f.Performance(ctx, from, to)
}
func (fakeStats) Logs(context.Context, store.LogFilter) ([]store.LogRow, int64, error) {
	return []store.LogRow{{IP: "1.2.3.4", Country: "ru"}}, 1, nil
}
func (fakeStats) Postbacks(context.Context, time.Time, time.Time, string, int) ([]store.PostbackRow, float64, error) {
	return []store.PostbackRow{{Profit: 1.5}}, 1.5, nil
}
func (fakeStats) QueryPostbacks(context.Context, store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error) {
	return []store.PostbackRow{{Profit: 1.5}}, store.PostbackTotals{Profit: 1.5, Count: 1}, nil
}
func (fakeStats) PostbackLog(context.Context, time.Time, time.Time, int) ([]store.PostbackLogRow, error) {
	return []store.PostbackLogRow{}, nil
}
func (fakeStats) Slice(context.Context, time.Time, time.Time) ([]store.SliceRow, error) {
	return []store.SliceRow{}, nil
}
func (fakeStats) DeleteGroupLogs(context.Context, string) error { return nil }
func (f fakeStats) ClickFacts(ctx context.Context, from, to time.Time) (store.Facts, error) {
	return store.Facts{Ready: true, Clicks: []store.ClickFact{}, Status: []store.StatusFact{}}, nil
}
func (f fakeStats) ClickFactsFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) (store.Facts, error) {
	return f.ClickFacts(ctx, from, to)
}
func (fakeStats) ClickBuckets(context.Context, string, time.Time, time.Time, bool) ([]store.BucketCount, error) {
	return nil, nil
}
func (fakeStats) Fields() store.FieldsInfo {
	return store.FieldsInfo{Fields: store.StaticFields(), Ready: map[string]map[string]bool{}}
}
func (fakeStats) Values(_ context.Context, q store.ValuesQuery) ([]store.KV, error) {
	if q.Table != "events" && q.Table != "conversions" {
		return nil, store.ErrBadCond
	}
	for _, f := range store.StaticFields() {
		if f.Name != q.Field {
			continue
		}
		on := f.Events
		if q.Table == "conversions" {
			on = f.Conv
		}
		if !on {
			return nil, store.ErrUnknownField
		}
		if !f.Dim {
			return nil, store.ErrBadCond
		}
		return []store.KV{{Key: "PL", Count: 3}}, nil
	}
	return nil, store.ErrUnknownField
}
func (fakeStats) SliceFiltered(context.Context, time.Time, time.Time, []store.Cond) ([]store.SliceRow, error) {
	return []store.SliceRow{}, nil
}

type fakeGroups struct{ saved []config.Group }

func (f *fakeGroups) List(context.Context) ([]config.Group, error) {
	return []config.Group{{ID: "g1", Name: "demo"}}, nil
}
func (f *fakeGroups) Save(_ context.Context, g []config.Group) error { f.saved = g; return nil }

type allowAll struct{}

func (allowAll) Allow(context.Context, string) (bool, error) { return true, nil }

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
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
	return srv
}

func newClient(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// login performs a login and returns the csrf token.
func login(t *testing.T, c *http.Client, base, user, pass string) (int, string) {
	t.Helper()
	body := `{"login":"` + user + `","password":"` + pass + `"}`
	resp, err := c.Post(base+"/api/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ CSRF string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.CSRF
}

func TestLoginFailAndSucceed(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t)

	if code, _ := login(t, c, srv.URL, "admin", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: expected 401, got %d", code)
	}
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK || csrf == "" {
		t.Fatalf("valid login: expected 200 and csrf, got %d csrf=%q", code, csrf)
	}
}

func TestProtectedRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t) // without login

	resp, err := c.Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session expected 401, got %d", resp.StatusCode)
	}
}

func TestMeAndSummaryAfterLogin(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t)
	login(t, c, srv.URL, "admin", "p@ss")

	resp, err := c.Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/me: expected 200, got %d", resp.StatusCode)
	}
	var me struct{ User string }
	_ = json.NewDecoder(resp.Body).Decode(&me)
	if me.User != "admin" {
		t.Errorf("expected user=admin, got %q", me.User)
	}

	r2, err := c.Get(srv.URL + "/api/stats/summary")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var sum store.StatsSummary
	_ = json.NewDecoder(r2.Body).Decode(&sum)
	if sum.Total != 100 || sum.Unique != 70 {
		t.Errorf("invalid summary: %+v", sum)
	}
}

func TestCSRFRequiredOnMutation(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	// logout without CSRF → 403
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/logout", nil)
	resp, _ := c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without CSRF: expected 403, got %d", resp.StatusCode)
	}

	// logout with CSRF → 200
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/logout", nil)
	req2.Header.Set("X-CSRF-Token", csrf)
	resp2, _ := c.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("logout with CSRF: expected 200, got %d", resp2.StatusCode)
	}

	// after logout the session is invalid
	r3, _ := c.Get(srv.URL + "/api/me")
	r3.Body.Close()
	if r3.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout /api/me must be 401, got %d", r3.StatusCode)
	}
}

func TestLinkBasePrefersPublicURL(t *testing.T) {
	s := &Server{cfg: Config{EngineURL: "http://engine:8080/", PublicURL: "http://127.0.0.1:8080/"}}
	if got := s.linkBase(); got != "http://127.0.0.1:8080" {
		t.Fatalf("public url: %s", got)
	}
	s.cfg.PublicURL = "  "
	if got := s.linkBase(); got != "http://engine:8080" {
		t.Fatalf("fallback: %s", got)
	}
}
