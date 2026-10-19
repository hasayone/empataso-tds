package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
	"github.com/redis/go-redis/v9"
)

// uniqGroup is a group with one catch-all text stream and the given method.
func uniqGroup(method string) *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop", UniqMethod: method, UniqSeconds: 3600,
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "OK"}}},
	})
}

// routedGroup picks "fresh" for a unique visitor and "repeat" for a repeat one.
func routedGroup(method string) *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop", UniqMethod: method, UniqSeconds: 3600,
		CostParam: "c", CurrencyParam: "cur",
		Streams: []config.Stream{
			{Name: "fresh", Status: true, Rules: config.Rules{Unique: config.FlagA},
				Out: config.Output{Redirect: "show_text", Out: "U"}},
			{Name: "repeat", Status: true, Rules: config.Rules{Unique: config.FlagB},
				Out: config.Output{Redirect: "show_text", Out: "R"}},
		},
	})
}

// withRedis attaches counters on a throwaway miniredis.
func withRedis(t *testing.T) func(*engineDeps) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return func(d *engineDeps) {
		d.counters = store.NewCounters(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	}
}

// jar is a cookie store for one visitor: it replays what the engine set.
type jar map[string]*http.Cookie

// visit requests path as 8.8.8.8 with the stored cookies and keeps new ones.
func (j jar) visit(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	req.Header.Set("User-Agent", uaWinChrome)
	for _, c := range j {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		j[c.Name] = c
	}
	return rec
}

// TestOfferStepRoutesByFirstStepButLogsOwnUniq pins both flags: the stream
// choice on /offer follows the first-step flag (a repeat after the landing),
// while the event carries the offer step's own uniqueness.
func TestOfferStepRoutesByFirstStepButLogsOwnUniq(t *testing.T) {
	for _, method := range []string{"ip", "cookie"} {
		t.Run(method, func(t *testing.T) {
			var opts []func(*engineDeps)
			if method == "ip" {
				opts = append(opts, withRedis(t))
			}
			h, ins, stop := curlEnv(t, routedGroup(method), "0", opts...)
			j := jar{}
			offerPath := "/g1/offer?cid=" + signedCID()
			steps := []struct{ path, stream string }{
				{"/g1", "fresh"},
				{offerPath, "repeat"},
				{offerPath, "repeat"},
			}
			for i, s := range steps {
				rec := j.visit(t, h, s.path)
				if got := rec.Header().Get("X-Kuztds-Stream"); got != s.stream {
					t.Errorf("step %d %s: stream %q, want %q", i, s.path, got, s.stream)
				}
			}
			stop()
			want := []uint8{1, 1, 0}
			if len(ins.events) != len(want) {
				t.Fatalf("events %d, want %d", len(ins.events), len(want))
			}
			for i, w := range want {
				if got := ins.events[i].Uniq; got != w {
					t.Errorf("event %d: Uniq=%d, want %d", i, got, w)
				}
			}
		})
	}
}

// TestOfferStepDirectIPUnique covers a visitor who reaches /offer first and a
// second visitor on another address: both are unique for routing and the log.
func TestOfferStepDirectIPUnique(t *testing.T) {
	h := newEng(t, routedGroup("ip"), engHarness{redis: true})
	hdr := map[string]string{"User-Agent": uaWinChrome}
	for _, s := range []struct{ path, ip, stream string }{
		{"/g1/offer", "8.8.8.8", "fresh"},
		{"/g1/offer", "8.8.8.8", "repeat"},
		{"/g1/offer", "9.9.9.9", "fresh"},
	} {
		rec := do(t, h, s.path, s.ip, hdr)
		if got := rec.Header().Get("X-Kuztds-Stream"); got != s.stream {
			t.Errorf("%s %s: stream %q, want %q", s.path, s.ip, got, s.stream)
		}
	}
}

// TestOfferStepUniqueCookieFlags checks both cookies keep their attributes and
// the repeat offer sets nothing.
func TestOfferStepUniqueCookieFlags(t *testing.T) {
	h := testEnv(t, uniqGroup("cookie"))
	j := jar{}
	landing := j.visit(t, h, "/g1")
	lc := landing.Result().Cookies()
	if len(lc) != 1 || lc[0].Name != "ztu_g1" {
		t.Fatalf("landing cookies %+v", lc)
	}
	offerPath := "/g1/offer?cid=" + signedCID()
	offer := j.visit(t, h, offerPath)
	oc := offer.Result().Cookies()
	if len(oc) != 1 || oc[0].Name != "ztuo_g1" {
		t.Fatalf("offer cookies %+v", oc)
	}
	for _, c := range append(lc, oc...) {
		if c.Path != "/" || c.SameSite != http.SameSiteLaxMode || !c.HttpOnly || c.MaxAge != 3600 {
			t.Errorf("cookie %s: path=%q samesite=%v httponly=%v maxage=%d", c.Name, c.Path, c.SameSite, c.HttpOnly, c.MaxAge)
		}
	}
	if again := j.visit(t, h, offerPath); len(again.Result().Cookies()) != 0 {
		t.Errorf("repeat offer must not set a cookie: %+v", again.Result().Cookies())
	}
}

// signedCID is a click id the offer step accepts as the landing's id.
func signedCID() string { return (*cidSigner)(nil).newCID() }

// TestOfferStepDoesNotRepeatClickCost sends the landing and the offer step
// with the same query, as offerStepURL does: the cents are stored once, and the
// offer step keeps the currency as the mark that the click came with a price.
func TestOfferStepDoesNotRepeatClickCost(t *testing.T) {
	h, ins, stop := curlEnv(t, routedGroup("ip"), "0", withRedis(t))
	j := jar{}
	j.visit(t, h, "/g1?c=1&cur=USD")
	j.visit(t, h, "/g1/offer?cid="+signedCID()+"&c=1&cur=USD")
	stop()
	if len(ins.events) != 2 {
		t.Fatalf("events %d, want 2", len(ins.events))
	}
	var sum int64
	for _, e := range ins.events {
		sum += e.CostCents
	}
	land, offer := ins.events[0], ins.events[1]
	if sum != 100 || land.CostCents != 100 || land.CostCur != "USD" || offer.CostCents != 0 || offer.CostCur != "USD" {
		t.Fatalf("costs %d/%q and %d/%q, sum %d", land.CostCents, land.CostCur, offer.CostCents, offer.CostCur, sum)
	}
}

// TestOfferStepWithoutPriceHasNoCurrency: a query without a valid price leaves
// no currency on either step, so no click is marked as priced by mistake.
func TestOfferStepWithoutPriceHasNoCurrency(t *testing.T) {
	for _, q := range []string{"", "?c=abc&cur=USD", "?c=1&cur=XXX", "?c=0&cur=USD"} {
		h, ins, stop := curlEnv(t, routedGroup("ip"), "0", withRedis(t))
		j := jar{}
		j.visit(t, h, "/g1"+q)
		sep := "?"
		if q != "" {
			sep = "&"
		}
		j.visit(t, h, "/g1/offer"+q+sep+"cid="+signedCID())
		stop()
		for i, e := range ins.events {
			if e.CostCents != 0 || e.CostCur != "" {
				t.Errorf("query %q event %d: cost %d/%q", q, i, e.CostCents, e.CostCur)
			}
		}
	}
}

// TestDirectOfferIsFirstStep: a visit to /offer without a click id this engine
// issued is a first step of its own. It shares the first step's uniqueness key,
// has step 0 and keeps the full price.
func TestDirectOfferIsFirstStep(t *testing.T) {
	h, ins, stop := curlEnv(t, routedGroup("ip"), "0", withRedis(t))
	do(t, h, "/g1/offer?c=2&cur=EUR", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	do(t, h, "/g1/offer?c=2&cur=EUR", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	do(t, h, "/g1", "9.9.9.9", map[string]string{"User-Agent": uaWinChrome})
	do(t, h, "/g1/offer?cid=a1b2c3d4e5f6", "9.9.9.9", map[string]string{"User-Agent": uaWinChrome})
	stop()
	if len(ins.events) != 4 {
		t.Fatalf("events %d, want 4", len(ins.events))
	}
	wantUniq := []uint8{1, 0, 1, 0}
	for i, e := range ins.events {
		if e.Step != 0 || e.Uniq != wantUniq[i] {
			t.Errorf("event %d: step=%d uniq=%d, want step 0 uniq %d", i, e.Step, e.Uniq, wantUniq[i])
		}
	}
	if e := ins.events[0]; e.CostCents != 200 || e.CostCur != "EUR" {
		t.Errorf("direct offer price %d/%q, want 200/EUR", e.CostCents, e.CostCur)
	}
}

// TestOfferStepWithoutStepWaveSharesFirstStepKey: while events has no step
// column the second step logs the first step's flag and sets no offer cookie, so
// a campaign with a landing does not count a visitor twice.
func TestOfferStepWithoutStepWaveSharesFirstStepKey(t *testing.T) {
	for _, method := range []string{"ip", "cookie"} {
		t.Run(method, func(t *testing.T) {
			opts := []func(*engineDeps){func(d *engineDeps) { d.noStepWave = true }}
			if method == "ip" {
				opts = append(opts, withRedis(t))
			}
			h, ins, stop := curlEnv(t, routedGroup(method), "0", opts...)
			j := jar{}
			offerPath := "/g1/offer?cid=" + signedCID()
			j.visit(t, h, "/g1")
			first := j.visit(t, h, offerPath)
			j.visit(t, h, offerPath)
			stop()
			if _, ok := j["ztuo_g1"]; ok {
				t.Error("the offer cookie must not be set without the step wave")
			}
			if got := first.Header().Get("X-Kuztds-Stream"); got != "repeat" {
				t.Errorf("offer stream %q, want repeat", got)
			}
			for i, w := range []uint8{1, 0, 0} {
				if got := ins.events[i].Uniq; got != w {
					t.Errorf("event %d: Uniq=%d, want %d", i, got, w)
				}
			}
		})
	}
}
