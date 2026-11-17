package admin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

// watchBase is the clock of the watch tests.
var watchBase = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// watchEnv is a cfEnv whose domain check is driven by the test: probeOK decides whether the
// probe is answered as this tracker, lookups counts the name resolutions, offset moves the clock.
type watchEnv struct {
	cfEnv
	probeOK *atomic.Bool
	lookups *atomic.Int32
	offset  *atomic.Int64
}

// newWatchEnv starts a cfEnv with Namecheap and Cloudflare configured, a fake probe target
// and a clock that starts at watchBase.
func newWatchEnv(t *testing.T) watchEnv {
	t.Helper()
	var live atomic.Bool
	var lookups atomic.Int32
	var offset atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if live.Load() {
			w.Header().Set("X-Emtds-Probe", r.URL.Query().Get("probe"))
			w.Header().Set("X-Emtds-Host", r.Host)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	e := newCFEnv(t, func(c *Config) {
		c.CheckClient = probeClient(t, ts)
		c.LookupIP = func(context.Context, string) ([]net.IPAddr, error) {
			lookups.Add(1)
			return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
		}
	})
	e.s.ncNow = func() time.Time { return watchBase.Add(time.Duration(offset.Load())) }
	e.configure(t, false)
	e.configureCF(t)
	return watchEnv{cfEnv: e, probeOK: &live, lookups: &lookups, offset: &offset}
}

// advance moves the clock of the watch by d.
func (e watchEnv) advance(d time.Duration) { e.offset.Add(int64(d)) }

// markFailing stores OK false with the reason for the domain, as a failed check would.
func (e watchEnv) markFailing(t *testing.T, id, reason string) {
	t.Helper()
	if _, err := e.s.updateDomainRow(context.Background(), id, func(d *config.Domain) { d.OK, d.Reason = false, reason }); err != nil {
		t.Fatal(err)
	}
}

func TestWatchConnectsThenMarksConnectedOnce(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	row := ncOwnedRow("example.com")
	row.WatchSince = watchBase.Add(-time.Hour).Format(time.RFC3339)
	e.seed(t, row)
	e.live.setOwned("example.com")

	rep := e.s.watchOnce(ctx)
	got := e.row(t, "1")
	if rep.checked != 1 || rep.changed != 1 || got.CfStatus != cfStatusPending || got.Reason != reasonCFPending || got.ConnectedAt != "" {
		t.Fatalf("first tick %+v %+v", rep, got)
	}

	e.advance(5 * time.Minute)
	rep = e.s.watchOnce(ctx)
	got = e.row(t, "1")
	if rep.checked != 1 || rep.changed != 0 || got.CfStatus != cfStatusPending || got.ConnectedAt != "" {
		t.Fatalf("pending tick %+v %+v", rep, got)
	}
	if e.cf.count("PUT /zones/"+got.CfZoneID+"/activation_check") < 2 {
		t.Fatalf("the pending zone was not asked to check again: %v", e.cf.calls)
	}

	e.cf.setStatus("example.com", cfStatusActive)
	e.probeOK.Store(true)
	e.advance(5 * time.Minute)
	rep = e.s.watchOnce(ctx)
	got = e.row(t, "1")
	first := watchBase.Add(10 * time.Minute).Format(time.RFC3339)
	if rep.changed != 1 || got.CfStatus != cfStatusActive || !got.OK || got.Reason != "" || got.ReasonDetail != "" || got.ConnectedAt != first || got.ConnectSeen {
		t.Fatalf("active tick %+v %+v", rep, got)
	}

	e.advance(5 * time.Minute)
	if rep = e.s.watchOnce(ctx); rep.checked != 0 {
		t.Fatalf("an OK domain was checked: %+v", rep)
	}

	e.markFailing(t, "1", "connect")
	e.probeOK.Store(false)
	e.advance(5 * time.Minute)
	rep = e.s.watchOnce(ctx)
	got = e.row(t, "1")
	if rep.checked != 1 || got.OK || got.Reason != "foreign" || got.ConnectedAt != first {
		t.Fatalf("flap tick %+v %+v", rep, got)
	}

	e.probeOK.Store(true)
	e.advance(5 * time.Minute)
	e.s.watchOnce(ctx)
	if got = e.row(t, "1"); !got.OK || got.ConnectedAt != first {
		t.Fatalf("connected_at was overwritten: %+v", got)
	}
}

func TestWatchRetriesConnectPendingAfterFailure(t *testing.T) {
	e := newWatchEnv(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links", CfStatus: cfStatusConnectPending})
	e.cf.fail["POST /zones"] = "temporarily unavailable"
	rep := e.s.watchOnce(context.Background())
	if rep.failed != 1 || rep.firstError == "" {
		t.Fatalf("failed tick %+v", rep)
	}
	if got := e.row(t, "1"); got.CfStatus != cfStatusConnectPending {
		t.Fatalf("row %+v", got)
	}
	delete(e.cf.fail, "POST /zones")
	e.advance(5 * time.Minute)
	if rep = e.s.watchOnce(context.Background()); rep.failed != 0 || e.row(t, "1").CfStatus != cfStatusPending {
		t.Fatalf("retry %+v %+v", rep, e.row(t, "1"))
	}
}

func TestWatchSkipsDisabledOkAndImportedDomains(t *testing.T) {
	e := newWatchEnv(t)
	recent := watchBase.Add(-24 * time.Hour).Format(time.RFC3339)
	old := watchBase.Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	e.seed(t,
		config.Domain{ID: "1", Host: "disabled.example", Role: "links", Disabled: true, CfStatus: cfStatusPending, CfZoneID: "z1"},
		config.Domain{ID: "2", Host: "imported.example", Role: "both"},
		config.Domain{ID: "3", Host: "ok.example", Role: "links", OK: true, CfStatus: cfStatusActive, CfZoneID: "z3"},
		config.Domain{ID: "4", Host: "stale.example", Role: "links", Registrar: namecheapRegistrar, WatchSince: old},
		config.Domain{ID: "5", Host: "fresh.example", Role: "links", Registrar: namecheapRegistrar, WatchSince: recent},
	)
	rep := e.s.watchOnce(context.Background())
	if rep.checked != 1 || e.lookups.Load() != 1 {
		t.Fatalf("tick %+v lookups %d", rep, e.lookups.Load())
	}
	if got := e.row(t, "5"); got.Checked == "" || got.Reason == "" {
		t.Fatalf("the fresh purchase was not checked: %+v", got)
	}
	for _, id := range []string{"1", "2", "3", "4"} {
		if got := e.row(t, id); got.Checked != "" {
			t.Fatalf("domain %s was checked: %+v", id, got)
		}
	}
}

func TestPickWatchedSlowsDownOldDomains(t *testing.T) {
	now := watchBase
	stamp := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	list := []config.Domain{
		{ID: "young", Host: "a", CfStatus: cfStatusPending, WatchSince: stamp(time.Hour), Checked: stamp(time.Minute)},
		{ID: "old-recent", Host: "b", CfStatus: cfStatusPending, WatchSince: stamp(72 * time.Hour), Checked: stamp(30 * time.Minute)},
		{ID: "old-due", Host: "c", CfStatus: cfStatusPending, WatchSince: stamp(72 * time.Hour), Checked: stamp(2 * time.Hour)},
		{ID: "nostart-recent", Host: "d", CfStatus: cfStatusPending, Checked: stamp(10 * time.Minute)},
		{ID: "never", Host: "e", CfStatus: cfStatusPending, WatchSince: stamp(72 * time.Hour)},
	}
	var ids []string
	for _, d := range pickWatched(list, now) {
		ids = append(ids, d.ID)
	}
	want := []string{"never", "old-due", "young"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("picked %v, want %v", ids, want)
	}
}

func TestPickWatchedCapsTheTick(t *testing.T) {
	list := make([]config.Domain, 80)
	for i := range list {
		list[i] = config.Domain{ID: fmt.Sprint(i), Host: fmt.Sprintf("d%d.example", i), CfStatus: cfStatusPending, WatchSince: watchBase.Format(time.RFC3339)}
	}
	if got := pickWatched(list, watchBase); len(got) != domainWatchCap {
		t.Fatalf("picked %d", len(got))
	}
}

func TestWatchTickDoesNotOverlap(t *testing.T) {
	e := newWatchEnv(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links", CfStatus: cfStatusPending, CfZoneID: "z"})
	e.s.watchBusy.Store(true)
	if rep := e.s.watchOnce(context.Background()); rep.checked != 0 || e.lookups.Load() != 0 {
		t.Fatalf("an overlapping tick worked: %+v", rep)
	}
	e.s.watchBusy.Store(false)
	if rep := e.s.watchOnce(context.Background()); rep.checked != 1 {
		t.Fatalf("tick %+v", rep)
	}
}

func TestWatchDomainsRunsOnItsOwn(t *testing.T) {
	e := newWatchEnv(t)
	e.s.watchFirst = 10 * time.Millisecond
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links", CfStatus: cfStatusConnectPending})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.s.WatchDomains(ctx, time.Hour)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for e.row(t, "1").CfStatus != cfStatusPending && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchDomains did not stop")
	}
	if got := e.row(t, "1"); got.CfStatus != cfStatusPending || got.CfZoneID == "" {
		t.Fatalf("the watch did not connect the domain: %+v", got)
	}
}

func TestWatchDomainsWithoutStore(t *testing.T) {
	s := &Server{}
	done := make(chan struct{})
	go func() {
		s.WatchDomains(context.Background(), time.Minute)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchDomains without a store did not return")
	}
}
