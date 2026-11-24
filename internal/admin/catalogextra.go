package admin

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

// errPrivateAddr is the dial refusal for an address that is not a public IP.
// It is the same value as security.ErrPrivateAddr so errors.Is works across both packages.
var errPrivateAddr = security.ErrPrivateAddr

// Reason codes a domain check adds to dns, private, connect and foreign.
const (
	// reasonTLS means the server certificate of the host was refused for a cause
	// other than an untrusted chain on this server.
	reasonTLS = "tls"
	// reasonUntrusted means the name resolves to this server, or the engine answered
	// with the same Host, and the only failure is a server certificate the client
	// does not trust. It is not a direction failure.
	reasonUntrusted = "untrusted"
	// reasonElsewhere means the host resolves to addresses that are not Config.PublicIP.
	reasonElsewhere = "elsewhere"
	// reasonCFPending means the Cloudflare zone of the host is not active yet.
	reasonCFPending = "cf_pending"
)

var (
	defaultCheckOnce sync.Once
	defaultCheck     *http.Client
)

// handleDomainCheck rechecks one parked domain and stores OK, Reason, ReasonDetail and Checked.
// The id comes from the request path. The row is read once to apply the access rules, then
// recheckDomain runs the network check outside domainMu and writes the result under it.
// It writes the updated domain. A missing store is 503, a missing id is 404,
// a read or save failure is 500, and 409 when the host was edited during the probe.
func (s *Server) handleDomainCheck(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	id := r.PathValue("id")
	row, found, err := s.domainRowByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	v := s.viewerFrom(r.Context())
	dref := domainRef(row)
	if !v.canSee(resDomains, dref) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !v.canWrite(resDomains, dref) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	updated, err := s.recheckDomain(r.Context(), id)
	if err != nil {
		code, msg := domainErrStatus(err)
		writeErr(w, code, msg)
		return
	}
	s.writeDomainRow(w, r.Context(), http.StatusOK, updated)
}

// probeResult is the outcome of probing one host. IPs are the public addresses the host
// resolved to; they are empty when resolution failed or found only non-public addresses.
type probeResult struct {
	ok     bool
	reason string
	ips    []net.IP
}

// checkDomain reports whether host answers as this tracker over HTTPS.
// host is the parked host. A port stays in the request URL and the Host header.
// ok is true only when name resolution and the probe both succeed.
// reason is dns, private, connect, tls, untrusted or foreign when ok is false, and empty when ok is true.
// connect means the HTTPS call did not complete. tls means the certificate was refused
// for a cause other than an untrusted chain on this server. untrusted means this engine
// answered and the only failure is a server certificate the client does not trust.
// foreign means some server answered, but it was not this tracker.
// A resolver failure is dns. checkDomain does not return an error.
func (s *Server) checkDomain(ctx context.Context, host string) (ok bool, reason string) {
	r := s.probeHost(ctx, host)
	return r.ok, r.reason
}

// probeHost resolves host and probes it over HTTPS.
// host is the parked host, with a port when one is stored. It returns the check outcome with
// the resolved public addresses; a failed resolution skips the probe.
func (s *Server) probeHost(ctx context.Context, host string) probeResult {
	reason, ips := s.resolvePhase(ctx, host)
	if reason != "" {
		return probeResult{reason: reason, ips: ips}
	}
	ok, reason := s.probePhase(ctx, host, "engine")
	return probeResult{ok: ok, reason: reason, ips: ips}
}

// judgeDomain probes d.Host and names the cause when the probe fails.
// d is the stored row. A successful probe returns true with no reason. After a failure a row
// with a Cloudflare zone that is not active gets cf_pending with its name servers as the
// detail. A row without a zone whose resolved addresses lack Config.PublicIP gets elsewhere
// with the resolved addresses as the detail, including when the probe reason is untrusted.
// Any other failure keeps the probe reason. untrusted is that reason only after the engine
// answered with the same Host and the certificate chain is not trusted.
func (s *Server) judgeDomain(ctx context.Context, d config.Domain) (ok bool, reason, detail string) {
	r := s.probeHost(ctx, d.Host)
	switch {
	case r.ok:
		return true, "", ""
	case d.CfZoneID != "":
		if d.CfStatus != cfStatusActive {
			return false, reasonCFPending, strings.Join(d.CfNS, ",")
		}
	case s.cfg.PublicIP != "" && len(r.ips) > 0 && !containsIP(r.ips, s.cfg.PublicIP):
		return false, reasonElsewhere, joinIPs(r.ips)
	}
	return false, r.reason, ""
}

// containsIP reports whether ips holds the address text. text is an IPv4 or IPv6 address;
// an unparsable text is never contained.
func containsIP(ips []net.IP, text string) bool {
	want := net.ParseIP(text)
	for _, ip := range ips {
		if want != nil && ip.Equal(want) {
			return true
		}
	}
	return false
}

// joinIPs returns the distinct addresses as sorted text joined by commas.
func joinIPs(ips []net.IP) string {
	seen := make(map[string]struct{}, len(ips))
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		t := ip.String()
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// recheckDomain probes one stored domain and saves the verdict.
// ctx bounds the network check and id selects the row. The row is read under domainMu, the
// network runs outside it and the result is written under it again. The write sets OK, Reason,
// ReasonDetail and Checked. A passing probe on a row with a Cloudflare zone also marks the zone
// active. ConnectedAt is set once, at the first passing probe of a row that has no zone or
// whose zone is active, and is never overwritten; a row outside the watch gets ConnectSeen at
// once, because nobody waits for its notice.
// It returns the saved row. Errors are errDomainStore, errDomainNotFound, errDomainRead,
// errDomainSave, and errDomainHostChanged when the host was edited during the probe.
func (s *Server) recheckDomain(ctx context.Context, id string) (config.Domain, error) {
	if s.cfg.Domains == nil {
		return config.Domain{}, errDomainStore
	}
	s.domainMu.Lock()
	d, found, err := s.domainRowByID(ctx, id)
	s.domainMu.Unlock()
	if err != nil {
		return config.Domain{}, fmt.Errorf("%w: %v", errDomainRead, err)
	}
	if !found {
		return config.Domain{}, errDomainNotFound
	}
	ok, reason, detail := s.judgeDomain(ctx, d)
	var adminOK bool
	var adminReason string
	if config.IsAdminDomain(d) {
		adminOK, adminReason = s.checkAdminHost(ctx, d)
	}
	checked := s.nowUTC().Format(time.RFC3339)
	saved, err := s.editDomain(ctx, id, func(cur *config.Domain) error {
		if cur.Host != d.Host {
			return errDomainHostChanged
		}
		cur.OK, cur.Reason, cur.ReasonDetail, cur.Checked = ok, reason, detail, checked
		if config.IsAdminDomain(*cur) {
			cur.AdminOK, cur.AdminReason = adminOK, adminReason
		} else {
			cur.AdminOK, cur.AdminReason = false, ""
		}
		if ok && cur.CfZoneID != "" {
			cur.CfStatus = cfStatusActive
		}
		if ok && cur.ConnectedAt == "" {
			cur.ConnectedAt = checked
			cur.ConnectSeen = cur.WatchSince == ""
		}
		return nil
	})
	if err != nil {
		return config.Domain{}, err
	}
	s.maybeScheduleAdminDNS(saved)
	return saved, nil
}

// resolvePhase decides whether host may be probed.
// host may include a port. The port is removed only for DNS and for ParseIP.
// It returns dns or private when the probe must not run, and an empty string when it may.
// A resolver error or an empty answer is dns. The second value is the public addresses the host
// resolved to, or the host itself when it is an IP literal; it is nil after a dns answer.
// resolvePhase does not return a Go error.
func (s *Server) resolvePhase(ctx context.Context, host string) (string, []net.IP) {
	h := host
	if name, _, err := net.SplitHostPort(host); err == nil {
		h = name
	}
	if ip := net.ParseIP(h); ip != nil {
		if !publicIP(ip) {
			return "private", nil
		}
		return "", []net.IP{ip}
	}
	addrs, err := s.lookupIP(ctx, h)
	if err != nil || len(addrs) == 0 {
		return "dns", nil
	}
	var public []net.IP
	for _, a := range addrs {
		if publicIP(a.IP) {
			public = append(public, a.IP)
		}
	}
	if len(public) == 0 {
		return "private", nil
	}
	return "", public
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

// publicIP reports whether ip is a global unicast address that is not private.
// It delegates to security.PublicIP; see that function for the full range list.
func publicIP(ip net.IP) bool { return security.PublicIP(ip) }

// checkAdminHost probes https://admin.<host>/healthz and expects the admin service header.
func (s *Server) checkAdminHost(ctx context.Context, d config.Domain) (bool, string) {
	adminHost := config.AdminHostOf(d)
	if reason, _ := s.resolvePhase(ctx, adminHost); reason != "" {
		return false, reason
	}
	return s.probePhase(ctx, adminHost, "admin")
}

// probePhase requests https://host/healthz and checks the probe headers.
// host is the full parked host, including a port when one is stored.
// service is the expected X-Emtds-Service value: engine allows a missing header, admin requires it.
// It returns ok when the status is 200 and X-Emtds-Probe and X-Emtds-Host match.
// reason is untrusted when service is engine, the server certificate chain is not trusted,
// and a second request that does not check the certificate shows this engine answered
// with the same Host. reason is tls when the certificate was refused for another cause,
// or when that second request does not show this engine. reason is connect when client.Do
// fails for another cause (network, TLS handshake, timeout, or guardDial).
// Any HTTP response whose status is not 200, or whose probe headers do not match, is foreign.
// The body is discarded. Only HTTPS is requested. A matching unverified answer is not ok.
func (s *Server) probePhase(ctx context.Context, host, service string) (bool, string) {
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
		if service == "engine" && untrustedServerCert(err) && s.engineConfirms(ctx, client, host, probe) {
			return false, reasonUntrusted
		}
		if isCertError(err) {
			return false, reasonTLS
		}
		return false, "connect"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		return false, "foreign"
	}
	if healthzHeader(resp.Header, "Probe") != probe || !strings.EqualFold(healthzHeader(resp.Header, "Host"), host) {
		return false, "foreign"
	}
	if !healthzServiceOK(resp.Header, service) {
		return false, "foreign"
	}
	return true, ""
}

// healthzHeader returns the healthz header X-Emtds-{name} of h, or "" when it is absent.
func healthzHeader(h http.Header, name string) string {
	return h.Get("X-Emtds-" + name)
}

// healthzServiceOK reports whether the healthz service header matches the expectation.
// expect engine treats a missing header as this tracker for backward compatibility.
func healthzServiceOK(h http.Header, expect string) bool {
	got := strings.TrimSpace(healthzHeader(h, "Service"))
	switch expect {
	case "engine":
		if got == "" {
			return true
		}
		return strings.EqualFold(got, "engine")
	default:
		return strings.EqualFold(got, expect)
	}
}

// untrustedServerCert reports whether err is a server certificate the client does not trust.
// err is the error of an HTTPS request. A wrapped unknown authority is untrusted.
// When the handshake error carries the presented certificates, the chain is checked again
// without the host name: an unknown authority there is untrusted too, which covers a
// self-signed certificate whose name does not match. A chain that verifies, including a
// trusted certificate with the wrong name or an expired trusted certificate, is not untrusted.
// It returns false when err is nil or the certificates are not available.
func untrustedServerCert(err error) bool {
	if err == nil {
		return false
	}
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return true
	}
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) && len(verify.UnverifiedCertificates) > 0 {
		return chainUntrusted(verify.UnverifiedCertificates)
	}
	return false
}

// chainUntrusted reports whether certs do not chain to a trusted root.
// certs[0] is the leaf and the rest are intermediates presented with it.
// The host name is not checked. It returns false when certs is empty or the chain verifies.
// A verification error that is not an unknown authority, such as an expired certificate, returns false.
func chainUntrusted(certs []*x509.Certificate) bool {
	if len(certs) == 0 {
		return false
	}
	pool := x509.NewCertPool()
	for _, c := range certs[1:] {
		pool.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{Intermediates: pool})
	var unknown x509.UnknownAuthorityError
	return errors.As(err, &unknown)
}

// engineConfirms repeats the healthz probe without verifying the server certificate.
// ctx bounds the call. base is the probe client, host is the parked host, including a port
// when one is stored, and probe is the token the engine must echo.
// It returns true only when the status is 200 and the probe, host and service headers match
// this engine. It returns false when the client cannot be copied, the request fails, or the
// answer is not this engine. The match is not a passing check.
func (s *Server) engineConfirms(ctx context.Context, base *http.Client, host, probe string) bool {
	client := insecureClient(base)
	if client == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/healthz?probe="+probe, nil)
	if err != nil {
		return false
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if healthzHeader(resp.Header, "Probe") != probe || !strings.EqualFold(healthzHeader(resp.Header, "Host"), host) {
		return false
	}
	return healthzServiceOK(resp.Header, "engine")
}

// insecureClient returns a copy of base that does not verify the server certificate.
// base is the probe client. A nil base uses the shared check client. The dialer, the timeout
// and the redirect policy are kept. It returns nil when the transport is not an *http.Transport
// and cannot be copied. The copy is only for engineConfirms.
func insecureClient(base *http.Client) *http.Client {
	if base == nil {
		base = defaultCheckClient()
	}
	next := *base
	switch tr := base.Transport.(type) {
	case *http.Transport:
		cloned := tr.Clone()
		cfg := &tls.Config{InsecureSkipVerify: true}
		if cloned.TLSClientConfig != nil {
			cfg = cloned.TLSClientConfig.Clone()
			cfg.InsecureSkipVerify = true
		}
		cloned.TLSClientConfig = cfg
		next.Transport = cloned
	case nil:
		next.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	default:
		return nil
	}
	return &next
}

// isCertError reports whether err is a refused server certificate: an unknown authority, a
// name that does not match, an expired or otherwise invalid certificate.
// err is the error of an HTTPS request; wrapped errors are unwrapped.
func isCertError(err error) bool {
	var (
		verify  *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		name    x509.HostnameError
		invalid x509.CertificateInvalidError
	)
	return errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &name) || errors.As(err, &invalid)
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
// network is the dial network. address is host:port with an IP as the host.
// c is the raw connection; it is not used.
// It delegates to security.GuardDial and returns security.ErrPrivateAddr on failure.
func guardDial(network, address string, c syscall.RawConn) error {
	return security.GuardDial(network, address, c)
}

// botProfileView builds the answer of GET and PUT /api/bot-protection from the
// stored profile file content (nil when there is none).
// The profile keys come first as config.MarshalBotProfile writes them, with the
// effective ch_proxy: true when the file does not choose, so the checkbox has a
// state. proxy_default is true when the file has no ch_proxy value, and
// private_relay when the Private Relay list exists and is not empty.
// A missing or unreadable file gives config.DefaultBotProfile.
func (s *Server) botProfileView(stored []byte) ([]byte, error) {
	p, proxySet, err := config.ParseBotProfileExplicit(stored)
	if err != nil {
		p, proxySet = config.DefaultBotProfile(), false
	}
	b, err := config.MarshalBotProfile(p)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	relay := s.cfg.DataDir != "" && nonEmptyFile(filepath.Join(s.cfg.DataDir, detect.ListPrivateRelay+".dat"))
	for k, v := range map[string]bool{"proxy_default": !proxySet, "private_relay": relay} {
		raw, _ := json.Marshal(v)
		doc[k] = raw
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	return append(out, '\n'), err
}

// writeBotProfile answers with botProfileView of the stored file content.
// code is the HTTP status. An encoding failure answers 500.
func (s *Server) writeBotProfile(w http.ResponseWriter, code int, stored []byte) {
	b, err := s.botProfileView(stored)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode error")
		return
	}
	writeJSONBytes(w, code, b)
}

// handleBotGet returns the installation-wide bot profile, the default one when
// none is stored, with proxy_default and private_relay (see botProfileView).
func (s *Server) handleBotGet(w http.ResponseWriter, r *http.Request) {
	var stored []byte
	if s.cfg.BotPath != "" {
		stored, _ = os.ReadFile(s.cfg.BotPath)
	}
	s.writeBotProfile(w, http.StatusOK, stored)
}

// handleBotPut merges the body into the stored bot profile (config.MergeBotProfile)
// and answers like GET. Keys the body does not mention keep their stored value;
// null removes a key, and {"ch_proxy": null} returns the proxy check to its default.
// Unknown keys and the stream-only redirect, out and header are dropped.
// It answers 400 for a body that is not a JSON object, has a non-boolean switch
// or exceeds 1 MiB, 503 when no profile path is configured and 500 when the file
// cannot be written.
func (s *Server) handleBotPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BotPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	stored, _ := os.ReadFile(s.cfg.BotPath)
	merged, err := config.MergeBotProfile(stored, body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	merged = append(merged, '\n')
	if err := writeFileAtomic(s.cfg.BotPath, merged, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	s.writeBotProfile(w, http.StatusOK, merged)
}

// handleFacts writes the click and conversion breakdown of the catalog tables.
//
// Filters come from condsFrom. offer and countries together switch the answer to
// one offer split by those countries (writeOfferGeoFacts). For a viewer who may
// not see the conversion status the status rows are folded by foldConfirmed, so
// the totals match the report and no status value leaks. Missing stats answers
// an empty body, a query error 500.
func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	geo, offerID, countries, msg := parseOfferGeo(r.URL.Query())
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if geo && fieldHidden(s.viewerFrom(r.Context()), "country") {
		writeErr(w, http.StatusBadRequest, "bad filter: hidden field country")
		return
	}
	if s.cfg.Stats == nil {
		s.writeStats(w, r, http.StatusOK, store.Facts{Clicks: []store.ClickFact{}, Status: []store.StatusFact{}})
		return
	}
	conds, ok := s.condsFrom(w, r, "events")
	if !ok {
		return
	}
	if geo {
		s.writeOfferGeoFacts(w, r, from, to, offerID, countries, conds)
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
		writeStatsErr(w, err, "stats error")
		return
	}
	if facts.Clicks == nil {
		facts.Clicks = []store.ClickFact{}
	}
	if facts.Status == nil {
		facts.Status = []store.StatusFact{}
	}
	if fieldHidden(s.viewerFrom(r.Context()), "status") {
		facts.Status = foldConfirmed(facts.Status)
		s.writeStatsWith(w, r, http.StatusOK, facts, statsOpts{keep: map[string]bool{"status": true}})
		return
	}
	s.writeStats(w, r, http.StatusOK, facts)
}
