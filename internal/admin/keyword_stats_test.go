package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
	"github.com/egerkuzma/emtds/internal/store"
)

// kwStub is a statistics source that answers the keyword queries with canned bodies.
// err fails both queries; panicWith makes them panic. When entered is set every query sends
// on it as it starts, and when gate is set it waits until gate is closed. The last request
// is kept for the test to read.
type kwStub struct {
	fakeStats
	stats     store.KeywordStats
	trend     store.KeywordTrend
	err       error
	panicWith any
	entered   chan struct{}
	gate      chan struct{}

	mu       sync.Mutex
	from, to time.Time
	conds    []store.Cond
	top      int
	tz       string
	mode     store.KeywordMode
}

// seen records the arguments of one query.
func (k *kwStub) seen(from, to time.Time, conds []store.Cond, top int, tz string, mode store.KeywordMode) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.from, k.to, k.conds, k.top, k.tz = from, to, conds, top, tz
	k.mode = mode
}

// hold plays the slow query: it announces the start, waits for the gate and may panic.
func (k *kwStub) hold() {
	if k.entered != nil {
		k.entered <- struct{}{}
	}
	if k.gate != nil {
		<-k.gate
	}
	if k.panicWith != nil {
		panic(k.panicWith)
	}
}

func (k *kwStub) KeywordStats(_ context.Context, from, to time.Time, conds []store.Cond, mode store.KeywordMode) (store.KeywordStats, error) {
	k.seen(from, to, conds, 0, "", mode)
	k.hold()
	return k.stats, k.err
}

func (k *kwStub) KeywordTrend(_ context.Context, from, to time.Time, conds []store.Cond, params store.KeywordTrendParams) (store.KeywordTrend, error) {
	k.seen(from, to, conds, params.Top, params.TZ, params.Mode)
	k.hold()
	return k.trend, k.err
}

// kwServer starts an admin server over st and returns it with the administrator's client.
func kwServer(t *testing.T, st StatsProvider) (*Server, string, *http.Client) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: st,
		Limiter: allowAll{}, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("login → %d", code)
	}
	return s, srv.URL, c
}

// kwGet requests path with c and returns the status and the body. A transport error
// (a handler that panicked closes the connection) is a status of 0.
func kwGet(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// kwRange is the from and to query of a period of span ending now.
func kwRange(span time.Duration) string {
	to := time.Now().UTC().Truncate(time.Second)
	q := url.Values{"from": {to.Add(-span).Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
	return q.Encode()
}

var kwRoutes = []string{"/api/keywords/stats", "/api/keywords/trend"}

func (r *recStats) KeywordStats(_ context.Context, _, _ time.Time, conds []store.Cond, _ store.KeywordMode) (store.KeywordStats, error) {
	r.remember(conds)
	return store.KeywordStats{Rows: []store.KeywordRow{populated[store.KeywordRow]()}, ConvReady: true}, nil
}

func (r *recStats) KeywordTrend(_ context.Context, _, _ time.Time, conds []store.Cond, _ store.KeywordTrendParams) (store.KeywordTrend, error) {
	r.remember(conds)
	return store.KeywordTrend{Days: []string{"2026-01-02"}, Series: []store.KeywordSeries{populated[store.KeywordSeries]()}, ConvReady: true}, nil
}

// TestKeywordRoutesNeedTheKeywordsSection checks a role without the section is refused,
// and a hidden keyword field is 403 on both routes.
func TestKeywordRoutesNeedTheKeywordsSection(t *testing.T) {
	rig := newRolesRig(t, "")
	var rest []section
	for _, sec := range userSections {
		if sec != secKeywords {
			rest = append(rest, sec)
		}
	}
	rig.setRole(t, security.Access{Sections: sectionStrings(rest)})
	for _, path := range kwRoutes {
		if code, body := rig.get(t, rig.user, path); code != http.StatusForbidden {
			t.Errorf("%s without the section: %d %s", path, code, body)
		}
	}
	rig.setRole(t, security.Access{Sections: userSectionsAll()})
	for _, path := range kwRoutes {
		if code, body := rig.get(t, rig.user, path); code != http.StatusOK {
			t.Errorf("%s with the section: %d %s", path, code, body)
		}
	}
	rig.hide(t, "keyword")
	for _, path := range kwRoutes {
		if code, body := rig.get(t, rig.user, path); code != http.StatusForbidden {
			t.Errorf("%s with the keyword hidden: %d %s", path, code, body)
		}
		if code, body := rig.get(t, rig.admin, path); code != http.StatusOK {
			t.Errorf("%s: an administrator sees every field, got %d %s", path, code, body)
		}
	}
}

// TestKeywordOriginMode validates by, protects hidden origin attribution and forwards the mode.
func TestKeywordOriginMode(t *testing.T) {
	st := &kwStub{}
	_, base, c := kwServer(t, st)
	for _, path := range kwRoutes {
		if code, body := kwGet(t, c, base+path+"?by=other"); code != http.StatusBadRequest || !strings.Contains(body, "bad by") {
			t.Errorf("%s bad by: %d %s", path, code, body)
		}
		if code, body := kwGet(t, c, base+path+"?by=origin"); code != http.StatusOK {
			t.Fatalf("%s origin: %d %s", path, code, body)
		}
		st.mu.Lock()
		mode := st.mode
		st.mu.Unlock()
		if mode != store.KeywordModeOrigin {
			t.Errorf("%s forwarded mode %q", path, mode)
		}
	}

	rig := newRolesRig(t, "")
	rig.setRole(t, security.Access{Sections: userSectionsAll()})
	rig.hide(t, "origin_campaign_id")
	for _, path := range kwRoutes {
		if code, body := rig.get(t, rig.user, path+"?by=origin"); code != http.StatusForbidden {
			t.Errorf("%s hidden origin: %d %s", path, code, body)
		}
		if code, body := rig.get(t, rig.user, path); code != http.StatusOK {
			t.Errorf("%s default mode: %d %s", path, code, body)
		}
	}
}

// TestKeywordStatsHiddenFieldsLeaveTheBody checks the keys of hidden fields are cut from the
// table and from every series of the trend, and nothing else is.
func TestKeywordStatsHiddenFieldsLeaveTheBody(t *testing.T) {
	rig := newRolesRig(t, "")
	cases := []struct {
		hide string
		gone []string
	}{
		{"status", []string{"leads", "sales", "rejected"}},
		{"profit", []string{"profit", "depsum"}},
		{"uniq", []string{"unique"}},
	}
	for _, path := range kwRoutes {
		code, body := rig.get(t, rig.admin, path)
		if code != http.StatusOK {
			t.Fatalf("%s as administrator: %d %s", path, code, body)
		}
		full := jsonKeys(t, body)
		for _, k := range []string{"keyword", "hits", "conv", "conv_ready"} {
			if !full[k] {
				t.Fatalf("%s: the full body lacks %q: %s", path, k, body)
			}
		}
		for _, c := range cases {
			rig.hide(t, c.hide)
			code, body := rig.get(t, rig.user, path)
			if code != http.StatusOK {
				t.Fatalf("%s hide %s: %d %s", path, c.hide, code, body)
			}
			got := jsonKeys(t, body)
			gone := map[string]bool{}
			for _, k := range c.gone {
				gone[k] = true
			}
			for k := range full {
				if gone[k] && got[k] {
					t.Errorf("%s hide %s: key %q leaks: %s", path, c.hide, k, body)
				}
				if !gone[k] && !got[k] {
					t.Errorf("%s hide %s: key %q lost", path, c.hide, k)
				}
			}
		}
	}
	rig.hide(t, "country")
	for _, path := range kwRoutes {
		if code, body := rig.get(t, rig.user, path+"?f=country:eq:PL"); code != http.StatusBadRequest || !strings.Contains(body, "hidden field") {
			t.Errorf("%s filtered by a hidden field: %d %s", path, code, body)
		}
	}
}

// TestKeywordRoutesScopeConds checks the campaign scope of the session reaches the query.
func TestKeywordRoutesScopeConds(t *testing.T) {
	st := &recStats{}
	_, srv := scopeServer(t, st, "")
	sel := newClient(t)
	if code, _ := login(t, sel, srv.URL, "sel", "secret1"); code != http.StatusOK {
		t.Fatalf("sel login %d", code)
	}
	for _, path := range kwRoutes {
		st.mu.Lock()
		st.last = nil
		st.mu.Unlock()
		code, body := kwGet(t, sel, srv.URL+path+"?f=keyword:contains:win")
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		conds := st.snapshot()
		sc, ok := scopeCond(conds)
		if !ok || len(sc.Values) != 1 || sc.Values[0] != "c1" || len(conds) != 2 {
			t.Errorf("%s: conds %+v, want the keyword filter and the scope of c1", path, conds)
		}
		if k := jsonKeys(t, body); k["profit"] || k["depsum"] {
			t.Errorf("%s: a role with profit hidden got money: %s", path, body)
		}
	}
	reader := newClient(t)
	if code, _ := login(t, reader, srv.URL, "reader", "secret3"); code != http.StatusOK {
		t.Fatalf("reader login %d", code)
	}
	for _, path := range kwRoutes {
		code, body := kwGet(t, reader, srv.URL+path+"?by=origin")
		if code != http.StatusOK {
			t.Fatalf("%s reader origin: %d %s", path, code, body)
		}
		sc, ok := scopeCond(st.snapshot())
		if !ok || len(sc.Values) != len(scopeCampaigns()) {
			t.Errorf("%s reader origin scope: %+v", path, st.snapshot())
		}
	}
	none := newClient(t)
	login(t, none, srv.URL, "none", "secret2")
	if code, _ := kwGet(t, none, srv.URL+"/api/keywords/stats"); code != http.StatusOK {
		t.Fatalf("none: %d", code)
	}
	if sc, ok := scopeCond(st.snapshot()); !ok || len(sc.Values) != 1 || sc.Values[0] != store.ScopeNone {
		t.Errorf("a user with no campaigns: %+v", st.snapshot())
	}
}

// TestKeywordRoutesPeriod checks the period rules: an empty or reversed one and one over
// 92 days are 400 "bad range", 92 days pass, and the period reaches the source.
func TestKeywordRoutesPeriod(t *testing.T) {
	st := &kwStub{}
	_, base, c := kwServer(t, st)
	span := store.KeywordStatsMaxSpan
	for _, path := range kwRoutes {
		for _, tc := range []struct {
			name  string
			query string
			code  int
		}{
			{"92 days", kwRange(span), http.StatusOK},
			{"93 days", kwRange(span + 24*time.Hour), http.StatusBadRequest},
			{"92 days and a second", kwRange(span + time.Second), http.StatusBadRequest},
			{"from equals to", "from=2026-03-01T00:00:00Z&to=2026-03-01T00:00:00Z", http.StatusBadRequest},
			{"from after to", "from=2026-03-02T00:00:00Z&to=2026-03-01T00:00:00Z", http.StatusBadRequest},
			{"default day", "", http.StatusOK},
		} {
			code, body := kwGet(t, c, base+path+"?"+tc.query)
			if code != tc.code {
				t.Errorf("%s %s: %d %s", path, tc.name, code, body)
			}
			if tc.code == http.StatusBadRequest && !strings.Contains(body, "bad range") {
				t.Errorf("%s %s: %s", path, tc.name, body)
			}
		}
	}
	code, _ := kwGet(t, c, base+"/api/keywords/stats?from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z")
	st.mu.Lock()
	defer st.mu.Unlock()
	if code != http.StatusOK || !st.from.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) || !st.to.Equal(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("period reached the source as %v - %v (status %d)", st.from, st.to, code)
	}
}

// TestKeywordTrendParams checks top and tz: a bad value is 400 before any query, a good one
// reaches the source, and the defaults are the largest top and UTC.
func TestKeywordTrendParams(t *testing.T) {
	st := &kwStub{}
	_, base, c := kwServer(t, st)
	bad := []string{"top=0", "top=26", "top=-3", "top=x", "top=1.5", "tz=Mars/Base", "tz=Local", "tz=" + url.QueryEscape("x';DROP TABLE events;--"), "tz=..%2Fetc%2Fpasswd"}
	for _, q := range bad {
		st.seen(time.Time{}, time.Time{}, nil, -1, "", store.KeywordModeLast)
		if code, body := kwGet(t, c, base+"/api/keywords/trend?"+q); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, code, body)
		}
		st.mu.Lock()
		ran := st.top != -1
		st.mu.Unlock()
		if ran {
			t.Errorf("%s: the query ran", q)
		}
	}
	for _, tc := range []struct {
		query string
		top   int
		tz    string
	}{
		{"", 25, ""},
		{"top=1", 1, ""},
		{"top=25&tz=Europe/Moscow", 25, "Europe/Moscow"},
		{"top=7&tz=UTC", 7, "UTC"},
	} {
		if code, body := kwGet(t, c, base+"/api/keywords/trend?"+tc.query); code != http.StatusOK {
			t.Fatalf("%q: %d %s", tc.query, code, body)
		}
		st.mu.Lock()
		top, tz := st.top, st.tz
		st.mu.Unlock()
		if top != tc.top || tz != tc.tz {
			t.Errorf("%q reached the source as top=%d tz=%q, want top=%d tz=%q", tc.query, top, tz, tc.top, tc.tz)
		}
	}
}

// TestKeywordRoutesShareTwoSlots checks two running queries, of either route, fill the gate,
// a third request is 429 "busy", and the slots are free again once the answers are written.
func TestKeywordRoutesShareTwoSlots(t *testing.T) {
	st := &kwStub{entered: make(chan struct{}, 8), gate: make(chan struct{})}
	s, base, c := kwServer(t, st)
	done := make(chan int, 2)
	for _, path := range kwRoutes {
		go func() {
			code, _ := kwGet(t, c, base+path)
			done <- code
		}()
	}
	<-st.entered
	<-st.entered
	code, body := kwGet(t, c, base+"/api/keywords/stats")
	if code != http.StatusTooManyRequests {
		t.Fatalf("third query: %d %s", code, body)
	}
	var e map[string]string
	_ = json.Unmarshal([]byte(body), &e)
	if e["error"] != "busy" || e["code"] != "too_many_requests" {
		t.Errorf("busy body %s", body)
	}
	if code, _ := kwGet(t, c, base+"/api/keywords/trend"); code != http.StatusTooManyRequests {
		t.Errorf("a trend takes the same gate, got %d", code)
	}
	close(st.gate)
	for range kwRoutes {
		if code := <-done; code != http.StatusOK {
			t.Errorf("held query answered %d", code)
		}
	}
	if len(s.kwSlots) != 0 {
		t.Fatalf("%d slots still held after the answers", len(s.kwSlots))
	}
	if code, body := kwGet(t, c, base+"/api/keywords/stats"); code != http.StatusOK {
		t.Errorf("after release: %d %s", code, body)
	}
}

// TestKeywordSlotIsReleasedOnFailure checks the slot comes back after a failed query and
// after a handler that panics.
func TestKeywordSlotIsReleasedOnFailure(t *testing.T) {
	st := &kwStub{err: context.Canceled}
	s, base, c := kwServer(t, st)
	for _, path := range kwRoutes {
		if code, body := kwGet(t, c, base+path); code != http.StatusInternalServerError {
			t.Errorf("%s failing: %d %s", path, code, body)
		}
		if len(s.kwSlots) != 0 {
			t.Fatalf("%s: slot held after an error", path)
		}
	}
	st.err, st.panicWith = nil, "boom"
	for _, path := range kwRoutes {
		kwGet(t, c, base+path)
		if len(s.kwSlots) != 0 {
			t.Fatalf("%s: slot held after a panic", path)
		}
	}
	st.panicWith = nil
	for _, path := range kwRoutes {
		if code, body := kwGet(t, c, base+path); code != http.StatusOK {
			t.Errorf("%s after the panic: %d %s", path, code, body)
		}
	}
}

// TestKeywordRoutesStoreErrors checks a bad condition from the store is 400 and not 500.
func TestKeywordRoutesStoreErrors(t *testing.T) {
	_, base, c := kwServer(t, &kwStub{err: store.ErrBadCond})
	for _, path := range kwRoutes {
		if code, body := kwGet(t, c, base+path); code != http.StatusBadRequest || !strings.Contains(body, "bad filter") {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}

// TestKeywordRoutesWithoutSource checks a source that cannot answer gives an empty 200 body.
func TestKeywordRoutesWithoutSource(t *testing.T) {
	for _, st := range []StatsProvider{fakeStats{}, nil} {
		_, base, c := kwServer(t, st)
		code, body := kwGet(t, c, base+"/api/keywords/stats")
		if code != http.StatusOK || strings.TrimSpace(body) != `{"rows":[],"truncated":false,"conv_ready":false}` {
			t.Errorf("stats %T: %d %s", st, code, body)
		}
		code, body = kwGet(t, c, base+"/api/keywords/trend")
		if code != http.StatusOK || strings.TrimSpace(body) != `{"days":[],"series":[],"conv_ready":false}` {
			t.Errorf("trend %T: %d %s", st, code, body)
		}
	}
}

// TestKeywordRoutesBodyShape checks conv_ready reaches the body, empty lists are arrays, and a
// trend without conversions has no conv and profit arrays.
func TestKeywordRoutesBodyShape(t *testing.T) {
	st := &kwStub{
		stats: store.KeywordStats{ConvReady: false},
		trend: store.KeywordTrend{
			Days:   []string{"2026-03-01", "2026-03-02"},
			Series: []store.KeywordSeries{{Keyword: "a", Hits: []int64{1, 0}}},
		},
	}
	_, base, c := kwServer(t, st)
	_, body := kwGet(t, c, base+"/api/keywords/stats")
	if strings.TrimSpace(body) != `{"rows":[],"truncated":false,"conv_ready":false}` {
		t.Errorf("empty table: %s", body)
	}
	_, body = kwGet(t, c, base+"/api/keywords/trend")
	if strings.TrimSpace(body) != `{"days":["2026-03-01","2026-03-02"],"series":[{"keyword":"a","hits":[1,0]}],"conv_ready":false}` {
		t.Errorf("clicks-only trend: %s", body)
	}
	st.stats = store.KeywordStats{Rows: []store.KeywordRow{{Keyword: "a", Hits: 2}}, Truncated: true, ConvReady: true}
	_, body = kwGet(t, c, base+"/api/keywords/stats")
	var got store.KeywordStats
	if err := json.Unmarshal([]byte(body), &got); err != nil || !got.ConvReady || !got.Truncated || len(got.Rows) != 1 {
		t.Errorf("table body %s: %v", body, err)
	}
	st.trend = store.KeywordTrend{}
	_, body = kwGet(t, c, base+"/api/keywords/trend")
	if strings.TrimSpace(body) != `{"days":[],"series":[],"conv_ready":false}` {
		t.Errorf("nil lists must be arrays: %s", body)
	}
}
