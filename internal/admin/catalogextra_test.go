package admin

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

func lookupPublic(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
}

func checkSrv(lookup func(context.Context, string) ([]net.IPAddr, error), client *http.Client) *Server {
	return &Server{cfg: Config{LookupIP: lookup, CheckClient: client}}
}

func TestCheckDomainDNS(t *testing.T) {
	s := checkSrv(func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}, nil)
	ok, reason := s.checkDomain(context.Background(), "missing.example")
	if ok || reason != "dns" {
		t.Fatalf("ok=%v reason=%s", ok, reason)
	}
}

func TestCheckDomainPrivate(t *testing.T) {
	s := checkSrv(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}, nil)
	ok, reason := s.checkDomain(context.Background(), "local.example")
	if ok || reason != "private" {
		t.Fatalf("ok=%v reason=%s", ok, reason)
	}
}

func probeClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	tr, ok := ts.Client().Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatal("tls transport")
	}
	tlsCfg := tr.TLSClientConfig.Clone()
	tlsCfg.NextProtos = []string{"http/1.1"}
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, ts.Listener.Addr().String())
			},
			TLSClientConfig: tlsCfg,
			TLSNextProto:    make(map[string]func(string, *tls.Conn) http.RoundTripper),
		},
	}
}

func TestCheckDomainHTTPS(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Kuztds-Probe", r.URL.Query().Get("probe"))
			w.Header().Set("X-Kuztds-Host", r.Host)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "nope")
		}))
		t.Cleanup(ts.Close)
		s := checkSrv(lookupPublic, probeClient(t, ts))
		ok, reason := s.checkDomain(context.Background(), "example.com")
		if !ok || reason != "" {
			t.Fatalf("ok=%v reason=%s", ok, reason)
		}
	})
	t.Run("foreign", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(ts.Close)
		s := checkSrv(lookupPublic, probeClient(t, ts))
		ok, reason := s.checkDomain(context.Background(), "example.com")
		if ok || reason != "foreign" {
			t.Fatalf("ok=%v reason=%s", ok, reason)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com/away", http.StatusFound)
		}))
		t.Cleanup(ts.Close)
		s := checkSrv(lookupPublic, probeClient(t, ts))
		ok, reason := s.checkDomain(context.Background(), "example.com")
		if ok || reason != "foreign" {
			t.Fatalf("ok=%v reason=%s", ok, reason)
		}
	})
	t.Run("notfound", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(ts.Close)
		s := checkSrv(lookupPublic, probeClient(t, ts))
		ok, reason := s.checkDomain(context.Background(), "example.com")
		if ok || reason != "foreign" {
			t.Fatalf("ok=%v reason=%s", ok, reason)
		}
	})
	t.Run("closed", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		client := probeClient(t, ts)
		ts.Close()
		s := checkSrv(lookupPublic, client)
		ok, reason := s.checkDomain(context.Background(), "example.com")
		if ok || reason != "connect" {
			t.Fatalf("ok=%v reason=%s", ok, reason)
		}
	})
}

func TestGuardDial(t *testing.T) {
	if err := guardDial("tcp", "127.0.0.1:443", nil); err == nil {
		t.Fatal("loopback accepted")
	}
	if err := guardDial("tcp", "10.0.0.1:443", nil); err == nil {
		t.Fatal("private accepted")
	}
	if err := guardDial("tcp", "100.64.1.1:443", nil); err == nil {
		t.Fatal("cgnat accepted")
	}
	if err := guardDial("tcp", "198.18.0.5:443", nil); err == nil {
		t.Fatal("benchmarking accepted")
	}
	if err := guardDial("tcp", "203.0.113.10:443", nil); err != nil {
		t.Fatalf("public rejected: %v", err)
	}
}

func domainHTTPServer(t *testing.T, groups []config.Group, domains []config.Domain, lookup func(context.Context, string) ([]net.IPAddr, error)) (*httptest.Server, string) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := NewJSONStore[config.Domain](filepath.Join(dir, "domains.json"))
	if err := store.Save(context.Background(), domains); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{list: groups}, Limiter: allowAll{},
		SessionTTL: time.Hour, CookieSecure: false,
		Domains:  store,
		LookupIP: lookup,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, dir
}

func readDomainFile(t *testing.T, dir string) []config.Domain {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "domains.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []config.Domain
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestDomainCheckPersistsDNS(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}
	srv, dir := domainHTTPServer(t, nil, []config.Domain{
		{ID: "1", Host: "missing.example", Role: "both"},
	}, lookup)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/domains/1/check", csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("check %d", resp.StatusCode)
	}
	resp.Body.Close()
	list := readDomainFile(t, dir)
	if len(list) != 1 || list[0].OK || list[0].Reason != "dns" || list[0].Checked == "" {
		t.Fatalf("stored = %+v", list)
	}
}

func TestDomainListCampaignCounts(t *testing.T) {
	srv, _ := domainHTTPServer(t, []config.Group{
		{ID: "g1", DomainID: "1"},
		{ID: "g2", DomainID: "1"},
	}, []config.Domain{
		{ID: "1", Host: "a.example", Role: "both"},
		{ID: "2", Host: "b.example", Role: "links"},
	}, nil)
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	resp, err := c.Get(srv.URL + "/api/domains")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list %d %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		t.Fatalf("body = %s", body)
	}
	var rows []domainRow
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, row := range rows {
		got[row.ID] = row.Campaigns
	}
	if got["1"] != 2 || got["2"] != 0 || len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDomainCheckParallel(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}
	srv, dir := domainHTTPServer(t, nil, []config.Domain{
		{ID: "1", Host: "a.example", Role: "both"},
		{ID: "2", Host: "b.example", Role: "links"},
	}, lookup)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	var wg sync.WaitGroup
	errc := make(chan error, 2)
	for _, id := range []string{"1", "2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/domains/"+id+"/check", nil)
			if err != nil {
				errc <- err
				return
			}
			req.Header.Set("X-CSRF-Token", csrf)
			resp, err := c.Do(req)
			if err != nil {
				errc <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errc <- fmt.Errorf("%s status %d", id, resp.StatusCode)
			}
		}(id)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	list := readDomainFile(t, dir)
	if len(list) != 2 || list[0].Checked == "" || list[1].Checked == "" {
		t.Fatalf("checked = %+v", list)
	}
}

func TestDomainPutKeepsCheck(t *testing.T) {
	srv, dir := domainHTTPServer(t, nil, []config.Domain{
		{ID: "1", Host: "a.example", Role: "both", OK: true, Checked: "2026-01-01T00:00:00Z"},
	}, nil)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/domains/1", csrf, map[string]any{
		"host": "a.example", "role": "both", "group": "g", "ok": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put same host %d", resp.StatusCode)
	}
	resp.Body.Close()
	list := readDomainFile(t, dir)
	if len(list) != 1 || !list[0].OK || list[0].Checked != "2026-01-01T00:00:00Z" || list[0].Group != "g" {
		t.Fatalf("stored = %+v", list)
	}
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/domains/1", csrf, map[string]any{
		"host": "b.example", "role": "both", "group": "g",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put new host %d", resp.StatusCode)
	}
	resp.Body.Close()
	list = readDomainFile(t, dir)
	if len(list) != 1 || list[0].OK || list[0].Reason != "" || list[0].Checked != "" {
		t.Fatalf("after host change = %+v", list)
	}
}

func TestDomainPostIgnoresCheckFields(t *testing.T) {
	srv, dir := domainHTTPServer(t, nil, nil, nil)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/domains", csrf, map[string]any{
		"id": "7", "host": "c.example", "role": "links", "ok": true, "checked": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post %d", resp.StatusCode)
	}
	resp.Body.Close()
	list := readDomainFile(t, dir)
	if len(list) != 1 || list[0].OK || list[0].Checked != "" {
		t.Fatalf("stored = %+v", list)
	}
}

func TestDomainDisabledDropsPrimary(t *testing.T) {
	list := []config.Domain{
		{ID: "1", Primary: true, Disabled: true},
		{ID: "2"},
	}
	out := tidyDomains(list, "1")
	if len(out) != 2 || out[0].Primary {
		t.Fatalf("disabled primary = %+v", out)
	}
	list = []config.Domain{
		{ID: "1", Primary: true},
		{ID: "2", Primary: true},
	}
	out = tidyDomains(list, "1")
	if !out[0].Primary || out[1].Primary {
		t.Fatalf("single primary = %+v", out)
	}
}

func TestDomainCheckHostChanged(t *testing.T) {
	var dir string
	lookup := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		store := NewJSONStore[config.Domain](filepath.Join(dir, "domains.json"))
		if err := store.Save(context.Background(), []config.Domain{
			{ID: "1", Host: "other.example", Role: "both"},
		}); err != nil {
			return nil, err
		}
		return nil, errors.New("no such host")
	}
	srv, d := domainHTTPServer(t, nil, []config.Domain{
		{ID: "1", Host: "a.example", Role: "both"},
	}, lookup)
	dir = d
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/domains/1/check", csrf, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("check %d", resp.StatusCode)
	}
	resp.Body.Close()
	list := readDomainFile(t, dir)
	if len(list) != 1 || list[0].Host != "other.example" || list[0].Checked != "" {
		t.Fatalf("stored = %+v", list)
	}
}

func TestDomainParallelPuts(t *testing.T) {
	domains := make([]config.Domain, 8)
	for i := range domains {
		id := fmt.Sprintf("%d", i+1)
		domains[i] = config.Domain{ID: id, Host: id + ".example", Role: "both"}
	}
	srv, dir := domainHTTPServer(t, nil, domains, nil)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		id := fmt.Sprintf("%d", i)
		go func(id string) {
			defer wg.Done()
			var buf bytes.Buffer
			if err := json.NewEncoder(&buf).Encode(map[string]any{
				"host":  id + ".example",
				"role":  "both",
				"group": "g" + id,
			}); err != nil {
				errc <- err
				return
			}
			req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/domains/"+id, &buf)
			if err != nil {
				errc <- err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-CSRF-Token", csrf)
			resp, err := c.Do(req)
			if err != nil {
				errc <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errc <- fmt.Errorf("%s status %d", id, resp.StatusCode)
			}
		}(id)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	list := readDomainFile(t, dir)
	if len(list) != 8 {
		t.Fatalf("len %d", len(list))
	}
	for _, d := range list {
		want := "g" + d.ID
		if d.Group != want {
			t.Fatalf("id %s group %q want %q", d.ID, d.Group, want)
		}
	}
}

// untrustedClient is a probe client that dials ts but trusts none of its certificates.
func untrustedClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	c := probeClient(t, ts)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport")
	}
	tr.TLSClientConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	return c
}

// closedClient is a probe client whose target is gone, so every probe fails with connect.
func closedClient(t *testing.T) *http.Client {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := probeClient(t, ts)
	ts.Close()
	return c
}

// probeTarget is a TLS server that answers as this tracker only while live is set.
func probeTarget(t *testing.T, live *atomic.Bool) *http.Client {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if live.Load() {
			w.Header().Set("X-Kuztds-Probe", r.URL.Query().Get("probe"))
			w.Header().Set("X-Kuztds-Host", r.Host)
			w.Header().Set("X-Kuztds-Service", "engine")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return probeClient(t, ts)
}

func TestCheckDomainTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	s := checkSrv(lookupPublic, untrustedClient(t, ts))
	ok, reason := s.checkDomain(context.Background(), "example.com")
	if ok || reason != "tls" {
		t.Fatalf("ok=%v reason=%s", ok, reason)
	}
}

func TestJudgeDomainElsewhere(t *testing.T) {
	ctx := context.Background()
	d := config.Domain{ID: "1", Host: "example.com"}
	s := &Server{cfg: Config{PublicIP: "203.0.113.50", LookupIP: lookupPublic, CheckClient: closedClient(t)}}
	ok, reason, detail := s.judgeDomain(ctx, d)
	if ok || reason != "elsewhere" || detail != "203.0.113.10" {
		t.Fatalf("ok=%v reason=%s detail=%q", ok, reason, detail)
	}
	two := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}, {IP: net.ParseIP("198.51.100.4")}, {IP: net.ParseIP("203.0.113.10")}}, nil
	}
	s.cfg.LookupIP = two
	if _, reason, detail = s.judgeDomain(ctx, d); reason != "elsewhere" || detail != "198.51.100.4,203.0.113.10" {
		t.Fatalf("reason=%s detail=%q", reason, detail)
	}
	s.cfg.PublicIP = "198.51.100.4"
	if _, reason, detail = s.judgeDomain(ctx, d); reason != "connect" || detail != "" {
		t.Fatalf("an address that is ours: reason=%s detail=%q", reason, detail)
	}
	s.cfg.PublicIP = ""
	s.cfg.LookupIP = lookupPublic
	if _, reason, detail = s.judgeDomain(ctx, d); reason != "connect" || detail != "" {
		t.Fatalf("without a public ip: reason=%s detail=%q", reason, detail)
	}
	s.cfg.PublicIP = "203.0.113.50"
	s.cfg.LookupIP = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("no such host") }
	if _, reason, detail = s.judgeDomain(ctx, d); reason != "dns" || detail != "" {
		t.Fatalf("no answer: reason=%s detail=%q", reason, detail)
	}
}

func TestJudgeDomainCloudflarePending(t *testing.T) {
	ctx := context.Background()
	s := &Server{cfg: Config{PublicIP: "203.0.113.50", LookupIP: lookupPublic, CheckClient: closedClient(t)}}
	d := config.Domain{ID: "1", Host: "example.com", CfZoneID: "z", CfStatus: "pending", CfNS: []string{"a.ns.example", "b.ns.example"}}
	ok, reason, detail := s.judgeDomain(ctx, d)
	if ok || reason != "cf_pending" || detail != "a.ns.example,b.ns.example" {
		t.Fatalf("ok=%v reason=%s detail=%q", ok, reason, detail)
	}
	s.cfg.LookupIP = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("no such host") }
	if _, reason, _ = s.judgeDomain(ctx, d); reason != "cf_pending" {
		t.Fatalf("a pending zone that does not resolve yet: reason=%s", reason)
	}
	s.cfg.LookupIP = lookupPublic
	d.CfStatus = "active"
	if _, reason, detail = s.judgeDomain(ctx, d); reason != "connect" || detail != "" {
		t.Fatalf("an active zone keeps the probe reason: reason=%s detail=%q", reason, detail)
	}
}

func TestJudgeDomainSuccessClearsReasons(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	s := &Server{cfg: Config{PublicIP: "203.0.113.50", LookupIP: lookupPublic, CheckClient: probeTarget(t, &live)}}
	ok, reason, detail := s.judgeDomain(context.Background(), config.Domain{ID: "1", Host: "example.com", Reason: "elsewhere", ReasonDetail: "x"})
	if !ok || reason != "" || detail != "" {
		t.Fatalf("ok=%v reason=%s detail=%q", ok, reason, detail)
	}
}

// recheckServer builds a server over a domain file for recheckDomain tests.
// The clock reads now; PublicIP is empty.
func recheckServer(t *testing.T, domains []config.Domain, client *http.Client, now *atomic.Int64) *Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	store := NewJSONStore[config.Domain](filepath.Join(t.TempDir(), "domains.json"))
	if err := store.Save(context.Background(), domains); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(), Limiter: allowAll{},
		Domains: store, LookupIP: lookupPublic, CheckClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.ncNow = func() time.Time { return time.Unix(now.Load(), 0) }
	return s
}

func TestRecheckDomainSetsConnectedAtOnce(t *testing.T) {
	var live atomic.Bool
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	s := recheckServer(t, []config.Domain{{ID: "1", Host: "example.com", Role: "links", WatchSince: "2026-10-20T11:00:00Z"}}, probeTarget(t, &live), &now)
	ctx := context.Background()

	got, err := s.recheckDomain(ctx, "1")
	if err != nil || got.OK || got.Reason != "foreign" || got.ConnectedAt != "" || got.Checked != "2026-10-20T12:00:00Z" {
		t.Fatalf("failed check %+v %v", got, err)
	}
	live.Store(true)
	now.Add(60)
	got, err = s.recheckDomain(ctx, "1")
	if err != nil || !got.OK || got.Reason != "" || got.ConnectedAt != "2026-10-20T12:01:00Z" || got.ConnectSeen {
		t.Fatalf("first success %+v %v", got, err)
	}
	live.Store(false)
	now.Add(60)
	got, _ = s.recheckDomain(ctx, "1")
	if got.OK || got.ConnectedAt != "2026-10-20T12:01:00Z" {
		t.Fatalf("flap %+v", got)
	}
	live.Store(true)
	now.Add(60)
	got, _ = s.recheckDomain(ctx, "1")
	if !got.OK || got.ConnectedAt != "2026-10-20T12:01:00Z" || got.Checked != "2026-10-20T12:03:00Z" {
		t.Fatalf("second success %+v", got)
	}
}

func TestRecheckDomainWithoutWatchIsSeenAtOnce(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	s := recheckServer(t, []config.Domain{{ID: "1", Host: "example.com", Role: "both"}}, probeTarget(t, &live), &now)
	got, err := s.recheckDomain(context.Background(), "1")
	if err != nil || got.ConnectedAt == "" || !got.ConnectSeen {
		t.Fatalf("imported domain %+v %v", got, err)
	}
}

func TestRecheckDomainCloudflareZone(t *testing.T) {
	var live atomic.Bool
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	pending := config.Domain{ID: "1", Host: "example.com", Role: "links", CfZoneID: "z", CfStatus: "pending", CfNS: []string{"a.ns.example", "b.ns.example"}, WatchSince: "2026-10-20T11:00:00Z"}
	s := recheckServer(t, []config.Domain{pending}, probeTarget(t, &live), &now)
	got, err := s.recheckDomain(context.Background(), "1")
	if err != nil || got.OK || got.Reason != "cf_pending" || got.ReasonDetail != "a.ns.example,b.ns.example" || got.ConnectedAt != "" || got.CfStatus != "pending" {
		t.Fatalf("pending zone %+v %v", got, err)
	}
	live.Store(true)
	got, err = s.recheckDomain(context.Background(), "1")
	if err != nil || !got.OK || got.CfStatus != "active" || got.ConnectedAt == "" || got.Reason != "" || got.ReasonDetail != "" {
		t.Fatalf("answering zone %+v %v", got, err)
	}
}

func TestRecheckDomainErrors(t *testing.T) {
	var now atomic.Int64
	s := recheckServer(t, nil, closedClient(t), &now)
	if _, err := s.recheckDomain(context.Background(), "9"); !errors.Is(err, errDomainNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := (&Server{}).recheckDomain(context.Background(), "1"); !errors.Is(err, errDomainStore) {
		t.Fatalf("no store: %v", err)
	}
}

func TestDomainCheckAnswerCarriesReasonDetail(t *testing.T) {
	e := newCFEnv(t, func(c *Config) {
		c.LookupIP = lookupPublic
		c.CheckClient = closedClient(t)
	})
	e.seed(t, config.Domain{ID: "1", Host: "example.com", Role: "links"})
	code, raw := e.do(t, http.MethodPost, "/api/domains/1/check", nil)
	if code != http.StatusOK || !strings.Contains(raw, `"reason":"elsewhere"`) || !strings.Contains(raw, `"reason_detail":"203.0.113.10"`) {
		t.Fatalf("check %d %s", code, raw)
	}
	if got := e.row(t, "1"); got.Reason != "elsewhere" || got.ReasonDetail != "203.0.113.10" || got.Checked == "" {
		t.Fatalf("stored %+v", got)
	}
}
