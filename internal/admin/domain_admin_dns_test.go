package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

func TestPrepDomainDefaultRoleLinks(t *testing.T) {
	d := config.Domain{Host: "a.com"}
	prepDomain(&d)
	if d.Role != "links" {
		t.Fatalf("role=%q", d.Role)
	}
}

func TestValidDomainRejectsAdminHost(t *testing.T) {
	if msg := validDomain(config.Domain{Host: "admin.example.com", Role: "links"}); msg == "" {
		t.Fatal("expected rejection")
	}
}

func syncAdminDNS(s *Server) {
	s.adminDNSSync = true
}

func TestEnsureAdminDNSCreatesRecord(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{
		ID: "1", Host: "a.com", Primary: true, CfZoneID: "zone001", CfStatus: cfStatusActive,
	})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive, NameServers: cfTestNS}}
	syncAdminDNS(e.s)
	e.s.afterDomainWrite(config.Domain{ID: "1", Host: "a.com", Primary: true, CfZoneID: "zone001", CfStatus: cfStatusActive}, nil)
	got := e.row(t, "1")
	if got.AdminDNS != "ok" {
		t.Fatalf("admin_dns=%q", got.AdminDNS)
	}
	if recs := cfRecordsNamed(e.cf, "zone001", "admin.a.com"); len(recs) != 1 || !recs[0].Proxied {
		t.Fatalf("records %+v", recs)
	}
}

func TestEnsureAdminDNSSkipsLinksOnly(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links", CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive}}
	if err := e.s.ensureAdminDNS(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, "1"); got.AdminDNS != "not an admin domain" {
		t.Fatalf("admin_dns=%q", got.AdminDNS)
	}
}

func TestEnsureAdminDNSWithoutCloudflare(t *testing.T) {
	e := newCFEnv(t, func(c *Config) { c.CloudflareFile = "" })
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Primary: true})
	if err := e.s.ensureAdminDNS(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, "1"); got.AdminDNS != "cloudflare is not configured" {
		t.Fatalf("admin_dns=%q", got.AdminDNS)
	}
}

func TestEnsureAdminDNSZoneRootRequired(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "www.a.com", Primary: true, CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive, NameServers: cfTestNS}}
	if err := e.s.ensureAdminDNS(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, "1"); got.AdminDNS != errAdminDNSHost.Error() {
		t.Fatalf("admin_dns=%q", got.AdminDNS)
	}
}

func TestEnsureAdminDNSIdempotent(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Primary: true, CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive, NameServers: cfTestNS}}
	if err := e.s.ensureAdminDNS(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	before := e.cf.writes()
	if err := e.s.ensureAdminDNS(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if e.cf.writes() != before {
		t.Fatal("second ensure wrote again")
	}
}

func TestHandleDomainAdminDNS(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "admin", CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive}}
	code, raw := e.do(t, http.MethodPost, "/api/domains/1/admin-dns", nil)
	if code != http.StatusOK || !strings.Contains(raw, `"admin_host":"admin.a.com"`) || !strings.Contains(raw, `"admin_dns":"ok"`) {
		t.Fatalf("post admin-dns %d %s", code, raw)
	}
}

func TestAdminDNSRetryAfterBusy(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "admin", CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive}}
	host := config.NormalizeHTTPHost("a.com")
	if !e.s.adminDNSBusy.acquire(host) {
		t.Fatal("setup acquire")
	}
	e.s.scheduleAdminDNS("1")
	if id := e.s.adminDNSBusy.releaseAndRetry(host); id != "1" {
		t.Fatalf("retry id %q", id)
	}
	syncAdminDNS(e.s)
	e.s.scheduleAdminDNS("1")
	if got := e.row(t, "1"); got.AdminDNS != "ok" {
		t.Fatalf("admin_dns=%q", got.AdminDNS)
	}
}

func TestEnsureAdminDNSForeignAdminA(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "admin", CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive}}
	e.cf.records["zone001"] = []cfRecord{{ID: "r1", Type: "A", Name: "admin.a.com", Content: "198.51.100.9", Proxied: true}}
	if err := e.s.ensureAdminDNS(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if got := e.row(t, "1"); got.AdminDNS != errCFRecordForeign.Error() {
		t.Fatalf("admin_dns=%q", got.AdminDNS)
	}
}

func TestHandleDomainAdminDNSParallel(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "admin", CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive}}
	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			code, _ := e.do(t, http.MethodPost, "/api/domains/1/admin-dns", nil)
			results[n] = code
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, code := range results {
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("codes %v", results)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("codes %v", results)
	}
}

func TestPutPrimaryRunsAdminDNSSync(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links", CfZoneID: "zone001", CfStatus: cfStatusActive})
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "a.com", Status: cfStatusActive, NameServers: cfTestNS}}
	syncAdminDNS(e.s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		code, _ := e.do(t, http.MethodPut, "/api/domains/1", map[string]any{
			"host": "a.com", "role": "both", "primary": true,
		})
		if code != http.StatusOK {
			t.Errorf("put %d", code)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PUT blocked on domainMu")
	}
	if got := e.row(t, "1"); got.AdminDNS != "ok" {
		t.Fatalf("admin_dns=%q", got.AdminDNS)
	}
}

func TestWatchZoneActiveCreatesAdminDNS(t *testing.T) {
	e := newWatchEnv(t)
	ctx := context.Background()
	row := config.Domain{
		ID: "1", Host: "example.com", Primary: true, CfZoneID: "zone001", CfStatus: cfStatusPending,
		WatchSince: watchBase.Add(-time.Hour).Format(time.RFC3339),
	}
	e.seed(t, row)
	e.cf.zones = []*cfZone{{ID: "zone001", Name: "example.com", Status: cfStatusActive, NameServers: cfTestNS}}
	syncAdminDNS(e.s)
	e.s.refreshZone(ctx, row)
	got := e.row(t, "1")
	if got.AdminDNS != "ok" {
		t.Fatalf("admin_dns=%q cf=%v", got.AdminDNS, e.cf.records["zone001"])
	}
}

func TestRecheckDomainAdminHost(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	var now atomic.Int64
	s := recheckServer(t, []config.Domain{{ID: "1", Host: "example.com", Primary: true}}, adminProbeClient(t, &live), &now)
	got, err := s.recheckDomain(context.Background(), "1")
	if err != nil || !got.AdminOK || got.AdminReason != "" {
		t.Fatalf("admin ok %+v %v", got, err)
	}
	live.Store(false)
	got, _ = s.recheckDomain(context.Background(), "1")
	if got.AdminOK || got.AdminReason != "foreign" {
		t.Fatalf("admin foreign %+v", got)
	}
}

func adminProbeClient(t *testing.T, live *atomic.Bool) *http.Client {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !live.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Kuztds-Probe", r.URL.Query().Get("probe"))
		w.Header().Set("X-Kuztds-Host", r.Host)
		if strings.HasPrefix(r.Host, "admin.") {
			w.Header().Set("X-Kuztds-Service", "admin")
		} else {
			w.Header().Set("X-Kuztds-Service", "engine")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return probeClient(t, ts)
}
