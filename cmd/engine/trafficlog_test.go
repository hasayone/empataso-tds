package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/store"
	"github.com/redis/go-redis/v9"
)

// trafficRig is an engine with a traffic log on a memory Redis.
type trafficRig struct {
	h   http.Handler
	log *store.TrafficLog
	d   *engineDeps
}

// newTrafficRig builds a testEnv whose engine records click paths into a
// miniredis. Writers stop with the test.
func newTrafficRig(t *testing.T, gg *config.Groups, limits store.TrafficLogLimits) *trafficRig {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	st := store.NewTrafficLog(rdb, limits)
	ctx, cancel := context.WithCancel(context.Background())
	rig := &trafficRig{log: st}
	rig.h = testEnv(t, gg, func(d *engineDeps) {
		rig.d = d
		d.traffic = newTrafficLogger(st, router.Deps{IP: d.lists}, discardLog())
		d.traffic.run(ctx, d.goBG)
	})
	t.Cleanup(func() {
		cancel()
		rig.d.bg.Wait()
	})
	return rig
}

// waitTraces waits until the campaign log holds want entries and returns them.
func waitTraces(t *testing.T, st *store.TrafficLog, group string, want int) []router.Trace {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := st.Recent(context.Background(), group, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) >= want || time.Now().After(deadline) {
			var out []router.Trace
			for _, b := range raw {
				var tr router.Trace
				if err := json.Unmarshal(b, &tr); err != nil {
					t.Fatalf("stored entry is not a trace: %v", err)
				}
				out = append(out, tr)
			}
			if len(out) < want {
				t.Fatalf("log of %s holds %d entries, want %d", group, len(out), want)
			}
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func trafficGroups(off bool) *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "g1", Name: "Group one", Status: true, Redirect: "show_text", Out: "DEFAULT", TrafficLogOff: off,
		UniqSeconds: 86400,
		Streams: []config.Stream{
			{Name: "ru_only", Status: true,
				Rules: config.Rules{
					Country: config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}},
					Lang:    config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}},
				},
				Out: config.Output{Redirect: "show_text", Out: "RU"}},
			{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
		},
	})
}

func TestClickPathRecordsStreamsReasonsAndVisitor(t *testing.T) {
	rig := newTrafficRig(t, trafficGroups(false), store.DefaultTrafficLogLimits())
	req := httptest.NewRequest(http.MethodGet, "/g1?sub_id_1=abc&secret=SECRETVALUE&token=T0K", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	req.Header.Set("CF-IPCountry", "DE")
	req.Header.Set("Accept-Language", "de-DE")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36")
	req.Header.Set("Cookie", "session=COOKIESECRET")
	req.Header.Set("Authorization", "Bearer AUTHSECRET")
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, req)
	if rec.Body.String() != "REST" {
		t.Fatalf("served %q, want REST code %d hdr %v", rec.Body.String(), rec.Code, rec.Header())
	}

	got := waitTraces(t, rig.log, "g1", 1)[0]
	if got.Campaign != "g1" || got.Name != "Group one" || got.IP != "8.8.8.8" || got.Stream != "rest" || got.Action != "show_text" {
		t.Fatalf("trace = %+v", got)
	}
	if got.CID == "" || got.DurUS < 0 || got.V != router.TraceVersion {
		t.Errorf("cid %q dur %d v %d", got.CID, got.DurUS, got.V)
	}
	if len(got.Hops) != 1 || got.Hops[0].Selected != "rest" || len(got.Hops[0].Streams) != 2 {
		t.Fatalf("hops = %+v", got.Hops)
	}
	first := got.Hops[0].Streams[0]
	if first.Name != "ru_only" || first.Match || strings.Join(first.Why, ",") != "lang,country" {
		t.Errorf("first stream check = %+v, want every failed rule (lang, country)", first)
	}
	if !got.Hops[0].Streams[1].Match {
		t.Errorf("second stream check = %+v", got.Hops[0].Streams[1])
	}
	if got.Visitor.Country != "de" || got.Visitor.OS != "Android" || got.Visitor.Browser != "Chrome" || !got.Visitor.Unique {
		t.Errorf("visitor = %+v", got.Visitor)
	}
	if got.Visitor.Subs["sub_id_1"] != "abc" {
		t.Errorf("subs = %v", got.Visitor.Subs)
	}
	var xff string
	for _, h := range got.Headers {
		if h.Name == "x-forwarded-for" {
			xff = h.Value
		}
	}
	if xff != "8.8.8.8" || !got.Trusted {
		t.Errorf("x-forwarded-for = %q trusted %v", xff, got.Trusted)
	}

	raw, _ := json.Marshal(got)
	for _, secret := range []string{"SECRETVALUE", "COOKIESECRET", "AUTHSECRET", "T0K"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the stored path holds %q", secret)
		}
	}
	if !strings.Contains(got.URL, "secret=[hidden]") || !strings.Contains(got.URL, "sub_id_1=abc") {
		t.Errorf("url = %q", got.URL)
	}

	byClick, err := rig.log.Click(context.Background(), got.CID)
	if err != nil || len(byClick) != 1 {
		t.Fatalf("path by click id: %d entries, err %v", len(byClick), err)
	}
}

func TestClickPathSwitchedOffRecordsNothing(t *testing.T) {
	rig := newTrafficRig(t, trafficGroups(true), store.DefaultTrafficLogLimits())
	do(t, rig.h, "/g1", "8.8.8.8", nil)
	time.Sleep(100 * time.Millisecond)
	raw, err := rig.log.Recent(context.Background(), "g1", 10)
	if err != nil || len(raw) != 0 {
		t.Fatalf("a campaign with the log off recorded %d entries, err %v", len(raw), err)
	}
	if n := rig.d.traffic.stats().Written; n != 0 {
		t.Errorf("written = %d", n)
	}
}

func TestClickPathKeepsOnlyTheLatest(t *testing.T) {
	lim := store.DefaultTrafficLogLimits()
	lim.PerCampaign = 3
	rig := newTrafficRig(t, trafficGroups(false), lim)
	for i := 0; i < 6; i++ {
		do(t, rig.h, "/g1", "8.8.8.8", nil)
	}
	deadline := time.Now().Add(3 * time.Second)
	for rig.d.traffic.stats().Written < 6 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	raw, _ := rig.log.Recent(context.Background(), "g1", 100)
	if len(raw) != 3 {
		t.Fatalf("the log keeps %d entries, want 3", len(raw))
	}
}

func TestTrafficLoggerDropsWhenFullAndNeverBlocks(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	st := store.NewTrafficLog(redis.NewClient(&redis.Options{Addr: mr.Addr()}), store.DefaultTrafficLogLimits())
	l := newTrafficLogger(st, router.Deps{}, discardLog())
	// No writers run, so the queue only fills.
	done := make(chan struct{})
	var wg sync.WaitGroup
	go func() {
		for i := 0; i < trafficQueue+50; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); l.offer(trafficJob{groups: []string{"g1"}}) }()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("offer blocked on a full queue")
	}
	if got := l.stats().Dropped; got != 50 {
		t.Errorf("dropped = %d, want 50", got)
	}
}

func TestTrafficLoggerIsOffWithoutAStore(t *testing.T) {
	if l := newTrafficLogger(nil, router.Deps{}, discardLog()); l != nil {
		t.Fatal("a logger without a store must be nil")
	}
	if l := newTrafficLogger(store.NewTrafficLog(nil, store.TrafficLogLimits{}), router.Deps{}, discardLog()); l != nil {
		t.Fatal("a log with zero entries must be off")
	}
	var l *trafficLogger
	if l.on(&config.Group{}) || l.offer(trafficJob{}) {
		t.Error("a nil logger accepts nothing")
	}
}

func TestClickPathBrokenGroupLinkIsNoted(t *testing.T) {
	// A link to a campaign that does not exist ends in the trash answer.
	gg := config.NewGroups(&config.Group{
		ID: "a", Name: "A", Status: true, Redirect: "group", Out: "missing", UniqSeconds: 60,
	})
	rig := newTrafficRig(t, gg, store.DefaultTrafficLogLimits())
	do(t, rig.h, "/a", "8.8.8.8", nil)
	got := waitTraces(t, rig.log, "a", 1)[0]
	if len(got.Notes) == 0 || got.Notes[0] != "group_link" || got.Hops[0].Error != "group_link" {
		t.Errorf("notes %v hops %+v", got.Notes, got.Hops)
	}
}

func TestClickPathFollowsAGroupLink(t *testing.T) {
	gg := config.NewGroups(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "group", Out: "b", UniqSeconds: 60},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "show_text", Out: "B-OUT", UniqSeconds: 60},
	)
	rig := newTrafficRig(t, gg, store.DefaultTrafficLogLimits())
	do(t, rig.h, "/a", "8.8.8.8", nil)
	for _, id := range []string{"a", "b"} {
		got := waitTraces(t, rig.log, id, 1)[0]
		if got.Campaign != "b" || len(got.Hops) != 2 || got.Hops[0].Link != "b" || got.Hops[1].Group != "b" {
			t.Errorf("log of %s: campaign %q hops %+v", id, got.Campaign, got.Hops)
		}
	}
}
