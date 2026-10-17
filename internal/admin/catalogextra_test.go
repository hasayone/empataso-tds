package admin

import (
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
