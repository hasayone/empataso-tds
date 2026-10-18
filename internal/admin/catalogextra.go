package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
)

// errPrivateAddr is the dial refusal for an address that is not a public IP.
var errPrivateAddr = errors.New("private address")

var (
	defaultCheckOnce sync.Once
	defaultCheck     *http.Client
)

// handleDomainCheck rechecks one parked domain and stores OK, Reason and Checked.
// The id comes from the request path. The network check runs before domainMu,
// then List, modify and Save run under that lock.
// It writes the updated domain. A missing store is 503, a missing id is 404,
// a read or save failure is 500, and 409 when the host was edited during the probe.
func (s *Server) handleDomainCheck(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	id := r.PathValue("id")
	list, err := s.cfg.Domains.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	var domainRow config.Domain
	host, found := domainHost(list, id)
	if !found {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	for i := range list {
		if list[i].ID == id {
			domainRow = list[i]
			break
		}
	}
	v := s.viewerFrom(r.Context())
	dref := ref{id: domainRow.ID, label: domainRow.Group, owner: domainRow.CreatedBy}
	if !v.canSee(resDomains, dref) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !v.canWrite(resDomains, dref) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	ok, reason := s.checkDomain(r.Context(), host)
	checked := time.Now().UTC().Format(time.RFC3339)

	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	list, err = s.cfg.Domains.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	idx := -1
	for i := range list {
		if list[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if list[idx].Host != host {
		writeErr(w, http.StatusConflict, "host changed")
		return
	}
	list[idx].OK = ok
	list[idx].Reason = reason
	list[idx].Checked = checked
	if err := s.cfg.Domains.Save(r.Context(), list); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, list[idx])
}

// domainHost returns the host stored for id.
// list is the current domain file. found is false when id is absent.
func domainHost(list []config.Domain, id string) (host string, found bool) {
	for i := range list {
		if list[i].ID == id {
			return list[i].Host, true
		}
	}
	return "", false
}

// checkDomain reports whether host answers as this tracker over HTTPS.
// host is the parked host. A port stays in the request URL and the Host header.
// ok is true only when name resolution and the probe both succeed.
// reason is dns, private, connect or foreign when ok is false, and empty when ok is true.
// connect means the HTTPS call did not complete. foreign means some server answered, but it was not this tracker.
// A resolver failure is dns. checkDomain does not return an error.
func (s *Server) checkDomain(ctx context.Context, host string) (ok bool, reason string) {
	if reason = s.resolvePhase(ctx, host); reason != "" {
		return false, reason
	}
	return s.probePhase(ctx, host)
}

// resolvePhase decides whether host may be probed.
// host may include a port. The port is removed only for DNS and for ParseIP.
// It returns dns or private when the probe must not run, and an empty string when it may.
// A resolver error or an empty answer is dns. resolvePhase does not return a Go error.
func (s *Server) resolvePhase(ctx context.Context, host string) string {
	h := host
	if name, _, err := net.SplitHostPort(host); err == nil {
		h = name
	}
	if ip := net.ParseIP(h); ip != nil {
		if !publicIP(ip) {
			return "private"
		}
		return ""
	}
	addrs, err := s.lookupIP(ctx, h)
	if err != nil || len(addrs) == 0 {
		return "dns"
	}
	for _, a := range addrs {
		if publicIP(a.IP) {
			return ""
		}
	}
	return "private"
}

// lookupIP resolves host for a domain check.
// host is the name without a port. A nil Config.LookupIP uses net.DefaultResolver.
// It returns the addresses or the resolver error.
func (s *Server) lookupIP(ctx context.Context, host string) ([]net.IPAddr, error) {
	if s.cfg.LookupIP != nil {
		return s.cfg.LookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// reservedNets lists ranges IP.IsPrivate does not cover.
// 100.64.0.0/10 is CGNAT. 198.18.0.0/15 is benchmarking.
var reservedNets = []net.IPNet{
	{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)},
	{IP: net.IPv4(198, 18, 0, 0).To4(), Mask: net.CIDRMask(15, 32)},
}

// publicIP reports whether ip is a global unicast address that is not private.
// A nil, loopback, link-local, unspecified, multicast or private address is false.
// CGNAT 100.64.0.0/10 and benchmarking 198.18.0.0/15 are also false.
// Documentation ranges such as 203.0.113.0/24 stay true.
func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// probePhase requests https://host/healthz and checks the probe headers.
// host is the full parked host, including a port when one is stored.
// It returns ok when the status is 200 and X-Kuztds-Probe and X-Kuztds-Host match.
// reason is connect when client.Do fails (network, TLS, timeout, or guardDial).
// Any HTTP response whose status is not 200, or whose probe headers do not match, is foreign.
// The body is discarded. Only HTTPS is requested.
func (s *Server) probePhase(ctx context.Context, host string) (bool, string) {
	probe, err := newProbe()
	if err != nil {
		return false, "connect"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/healthz?probe="+probe, nil)
	if err != nil {
		return false, "connect"
	}
	req.Host = host
	client := s.cfg.CheckClient
	if client == nil {
		client = defaultCheckClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, "connect"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		return false, "foreign"
	}
	if resp.Header.Get("X-Kuztds-Probe") != probe || !strings.EqualFold(resp.Header.Get("X-Kuztds-Host"), host) {
		return false, "foreign"
	}
	return true, ""
}

// newProbe returns sixteen hex characters drawn from crypto/rand.
// It returns an error when the random source fails.
func newProbe() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// defaultCheckClient returns the shared client used when Config.CheckClient is nil.
// The client times out at five seconds, does not follow redirects, and refuses a non-public address at connect time.
func defaultCheckClient() *http.Client {
	defaultCheckOnce.Do(func() {
		dialer := &net.Dialer{
			Timeout: 5 * time.Second,
			Control: guardDial,
		}
		defaultCheck = &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				DialContext:         dialer.DialContext,
				TLSHandshakeTimeout: 5 * time.Second,
				DisableKeepAlives:   true,
			},
		}
	})
	return defaultCheck
}

// guardDial rejects a connection to a non-public address.
// network is the dial network. address is host:port, with an IP as the host.
// c is the raw connection and may be nil; it is not used.
// It returns errPrivateAddr when the address is missing, not an IP, or not public.
func guardDial(network, address string, c syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !publicIP(net.ParseIP(host)) {
		return errPrivateAddr
	}
	return nil
}

func (s *Server) handleBotGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, loadBotProfile(s.cfg.BotPath))
}

func (s *Server) handleBotPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BotPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	var profile config.Bots
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&profile); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	profile.Redirect, profile.Out, profile.Header = "", "", ""
	b, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if err := writeFileAtomic(s.cfg.BotPath, append(b, '\n'), 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func loadBotProfile(path string) config.Bots {
	if path == "" {
		return config.DefaultBotProfile()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return config.DefaultBotProfile()
	}
	var profile config.Bots
	if json.Unmarshal(b, &profile) != nil {
		return config.DefaultBotProfile()
	}
	return profile
}

func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	if s.cfg.Stats == nil {
		s.writeStats(w, r, http.StatusOK, store.Facts{Clicks: []store.ClickFact{}, Status: []store.StatusFact{}})
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	var facts store.Facts
	var err error
	if len(conds) > 0 {
		facts, err = s.cfg.Stats.ClickFactsFiltered(r.Context(), from, to, conds)
	} else {
		facts, err = s.cfg.Stats.ClickFacts(r.Context(), from, to)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stats error")
		return
	}
	if facts.Clicks == nil {
		facts.Clicks = []store.ClickFact{}
	}
	if facts.Status == nil {
		facts.Status = []store.StatusFact{}
	}
	s.writeStats(w, r, http.StatusOK, facts)
}
