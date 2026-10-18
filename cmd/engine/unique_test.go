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
			steps := []struct{ path, stream string }{
				{"/g1", "fresh"},
				{"/g1/offer", "repeat"},
				{"/g1/offer", "repeat"},
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
	offer := j.visit(t, h, "/g1/offer")
	oc := offer.Result().Cookies()
	if len(oc) != 1 || oc[0].Name != "ztuo_g1" {
		t.Fatalf("offer cookies %+v", oc)
	}
	for _, c := range append(lc, oc...) {
		if c.Path != "/" || c.SameSite != http.SameSiteLaxMode || !c.HttpOnly || c.MaxAge != 3600 {
			t.Errorf("cookie %s: path=%q samesite=%v httponly=%v maxage=%d", c.Name, c.Path, c.SameSite, c.HttpOnly, c.MaxAge)
		}
	}
	if again := j.visit(t, h, "/g1/offer"); len(again.Result().Cookies()) != 0 {
		t.Errorf("repeat offer must not set a cookie: %+v", again.Result().Cookies())
	}
}

// TestOfferStepDoesNotRepeatClickCost sends the landing and the offer step
// with the same query, as offerStepURL does: the price is stored once.
func TestOfferStepDoesNotRepeatClickCost(t *testing.T) {
	h, ins, stop := curlEnv(t, routedGroup("ip"), "0", withRedis(t))
	j := jar{}
	j.visit(t, h, "/g1?c=1&cur=USD")
	j.visit(t, h, "/g1/offer?cid=0123456789&c=1&cur=USD")
	stop()
	if len(ins.events) != 2 {
		t.Fatalf("events %d, want 2", len(ins.events))
	}
	var sum int64
	for _, e := range ins.events {
		sum += e.CostCents
	}
	if sum != 100 || ins.events[0].CostCents != 100 || ins.events[1].CostCents != 0 || ins.events[1].CostCur != "" {
		t.Fatalf("costs %d/%q and %d/%q, sum %d", ins.events[0].CostCents, ins.events[0].CostCur,
			ins.events[1].CostCents, ins.events[1].CostCur, sum)
	}
}
