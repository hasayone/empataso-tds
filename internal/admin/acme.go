package admin

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/hasayone/empataso-tds/internal/config"
)

// Certificate schedule. One admin process runs it; cron and the engine never order certificates.
const (
	// certFirst is the wait before the first tick, certEvery the tick interval WatchCertificates uses by default.
	certFirst = time.Minute
	certEvery = 2 * time.Minute
	// certRenewBefore is how long before expiry a held certificate is ordered again.
	certRenewBefore = 30 * 24 * time.Hour
	// certOrdersPerTick caps the orders of one tick, so an imported list is never ordered in one go.
	certOrdersPerTick = 1
	// certLookupsPerTick caps the name resolutions one tick spends looking for a domain to order.
	certLookupsPerTick = 20
	// certOrderTimeout bounds one order: account, authorization, finalization and download.
	certOrderTimeout = 3 * time.Minute
	// certRetryFirst is the wait after the first failure; each next failure doubles it up to certRetryMax.
	certRetryFirst = time.Hour
	certRetryMax   = 24 * time.Hour
	// certRecheckWindow is how long after an order the domain check is repeated by the schedule,
	// at most once per certRecheckEvery and for certRechecksPerTick domains a tick, so the row turns
	// green once the host mirrored the certificate into nginx.
	certRecheckWindow   = time.Hour
	certRecheckEvery    = 2 * time.Minute
	certRechecksPerTick = 5
	// certErrorMax is the longest error text kept in state.json.
	certErrorMax = 300
)

// certNameRe matches a lowercase DNS name with at least two labels whose top label starts with a
// letter, so an IP address never matches. deploy/apply-edge.sh (edge_cert_name_ok) uses the same rule.
var certNameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// certHostState is what the schedule remembers about one domain between ticks.
// Failures counts the failed orders in a row, NextTry is the RFC 3339 time before which no new order
// is made, LastError is the text of the last failure, RecheckUntil ends the checks after an order.
type certHostState struct {
	Failures     int    `json:"failures,omitempty"`
	NextTry      string `json:"next_try,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	RecheckUntil string `json:"recheck_until,omitempty"`
}

// certState is state.json in Config.TLSDir. PauseUntil and PauseFailures stop every order after the
// ACME account itself could not be set up.
type certState struct {
	Hosts         map[string]certHostState `json:"hosts"`
	PauseUntil    string                   `json:"pause_until,omitempty"`
	PauseFailures int                      `json:"pause_failures,omitempty"`
}

// certReport counts what one tick did: orders made, orders that failed, certificates removed and
// domain checks repeated.
type certReport struct {
	ordered, failed, removed, rechecked int
}

// WatchCertificates orders and renews the certificates of domains served directly, without
// Cloudflare, until ctx ends. every is the tick interval; zero or less uses two minutes. The first
// tick runs a minute after the call. It returns at once when certificates are disabled (no
// Config.TLSDir, no Config.PublicIP or no domain store). The caller starts it once after New.
func (s *Server) WatchCertificates(ctx context.Context, every time.Duration) {
	if !s.certsEnabled() {
		return
	}
	if every <= 0 {
		every = certEvery
	}
	first := time.NewTimer(certFirst)
	defer first.Stop()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-ticker.C:
		}
		s.certOnce(ctx)
	}
}

// certsEnabled reports whether the certificate schedule may run.
func (s *Server) certsEnabled() bool {
	return s.cfg.TLSDir != "" && s.cfg.PublicIP != "" && s.cfg.Domains != nil
}

// certOnce runs one tick. Held certificates of domains that were deleted or moved to Cloudflare
// are removed. Then at most certOrdersPerTick orders are made, among domains that need a certificate
// (none held, or renewDue for the held one), whose retry time has come and whose
// name resolves only to Config.PublicIP. A new certificate is ordered only for an enabled domain the
// check found on this server with an untrusted certificate, or for a Namecheap purchase that is not
// OK yet; a renewal needs neither. A failure delays the next order of that domain (certRetryDelay);
// a failure to set up the account delays every order. Last, domains ordered within certRecheckWindow
// that are not OK are checked again. It returns the counts; a tick that finds another one running,
// or cannot read the domains, returns the zero report.
func (s *Server) certOnce(ctx context.Context) certReport {
	var rep certReport
	if !s.certsEnabled() || !s.certBusy.CompareAndSwap(false, true) {
		return rep
	}
	defer s.certBusy.Store(false)
	list, err := s.cfg.Domains.List(ctx)
	if err != nil {
		s.logWarn("certificates cannot read domains", "err", err)
		return rep
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Host < list[j].Host })
	now := s.nowUTC()
	st := s.loadCertState()
	live := filepath.Join(s.cfg.TLSDir, "live")
	held := heldCerts(live)
	rep.removed = s.pruneCerts(list, held, st)

	var client *acme.Client
	fresh := make(map[string]bool)
	lookups := 0
	paused := afterRFC3339(st.PauseUntil, now)
	for _, d := range list {
		if paused || rep.ordered >= certOrdersPerTick || lookups >= certLookupsPerTick {
			break
		}
		host, ok := certHost(d)
		if !ok || !certWanted(d, held[host], now) {
			continue
		}
		hs := st.Hosts[host]
		if afterRFC3339(hs.NextTry, now) {
			continue
		}
		lookups++
		if !s.pointsHere(ctx, host) {
			continue
		}
		if client == nil {
			client, err = s.acmeClient(ctx)
			if err != nil {
				st.PauseFailures++
				st.PauseUntil = now.Add(certRetryDelay(st.PauseFailures, err)).Format(time.RFC3339)
				s.logWarn("certificates: ACME account not ready", "err", err, "retry_at", st.PauseUntil)
				break
			}
			st.PauseFailures, st.PauseUntil = 0, ""
		}
		rep.ordered++
		cert, err := s.issueCert(ctx, client, host)
		if err != nil {
			rep.failed++
			hs.Failures++
			hs.NextTry = now.Add(certRetryDelay(hs.Failures, err)).Format(time.RFC3339)
			hs.LastError = clip(err.Error(), certErrorMax)
			hs.RecheckUntil = ""
			st.Hosts[host] = hs
			s.logWarn("certificate order failed", "host", host, "failures", hs.Failures, "retry_at", hs.NextTry, "err", hs.LastError)
			continue
		}
		held[host] = cert
		fresh[host] = true
		st.Hosts[host] = certHostState{RecheckUntil: now.Add(certRecheckWindow).Format(time.RFC3339)}
		s.logInfo("certificate issued", "host", host, "not_after", cert.NotAfter.UTC().Format(time.RFC3339))
	}
	if err := s.saveCertState(st); err != nil {
		s.logWarn("certificates: state not saved", "err", err)
	}
	rep.rechecked = s.recheckCertified(ctx, list, held, fresh, st, now)
	return rep
}

// certHost returns the name a certificate would be ordered for and whether d may have one.
// d is the stored row. A row with a Cloudflare zone, a port, an IP address, an admin.* name or a
// name certNameOK refuses gives false.
func certHost(d config.Domain) (string, bool) {
	if d.CfZoneID != "" {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d.Host)), ".")
	if !certNameOK(host) {
		return "", false
	}
	return host, true
}

// certNameOK reports whether host is a DNS name of at most 253 characters matching certNameRe
// that does not start with admin. (admin.* stays behind the Cloudflare-only vhost).
func certNameOK(host string) bool {
	return len(host) <= 253 && certNameRe.MatchString(host) && !strings.HasPrefix(host, "admin.")
}

// certWanted reports whether d needs an order now, ignoring the retry time and DNS.
// held is the certificate stored for the domain or nil and now the current time. A held certificate
// is renewed when renewDue says so. Without one, the domain must be enabled and
// either checked as this server with an untrusted certificate or a Namecheap purchase that is not OK.
func certWanted(d config.Domain, held *x509.Certificate, now time.Time) bool {
	if held != nil {
		return renewDue(held, now)
	}
	if d.Disabled {
		return false
	}
	return d.Reason == reasonUntrusted || (d.Registrar == namecheapRegistrar && !d.OK)
}

// renewDue reports whether c should be ordered again at now: its remaining time is below
// certRenewBefore, or below a third of its lifetime when that is shorter, so a short-lived
// certificate is not ordered again on every tick.
func renewDue(c *x509.Certificate, now time.Time) bool {
	before := certRenewBefore
	if third := c.NotAfter.Sub(c.NotBefore) / 3; third < before {
		before = third
	}
	return c.NotAfter.Sub(now) < before
}

// pointsHere reports whether every address host resolves to is Config.PublicIP.
// A resolver error, an empty answer or any other address (an AAAA elsewhere, a foreign proxy) is false.
func (s *Server) pointsHere(ctx context.Context, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := s.lookupIP(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	want := net.ParseIP(s.cfg.PublicIP)
	for _, a := range addrs {
		if !a.IP.Equal(want) {
			return false
		}
	}
	return true
}

// heldCerts reads every live/<domain>/fullchain.pem under live and returns the leaf certificates by
// domain. Directories with a bad name, an unreadable file or a leaf that does not name the domain
// are left out. A missing live directory gives an empty map.
func heldCerts(live string) map[string]*x509.Certificate {
	out := make(map[string]*x509.Certificate)
	entries, err := os.ReadDir(live)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !certNameOK(e.Name()) {
			continue
		}
		if c, err := readLeaf(filepath.Join(live, e.Name(), "fullchain.pem"), e.Name()); err == nil {
			out[e.Name()] = c
		}
	}
	return out
}

// readLeaf parses the first certificate of the PEM file path and checks that it names host.
// It returns the certificate, or an error when the file cannot be read, holds no certificate or
// the certificate is for another name.
func readLeaf(path, host string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no certificate")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := c.VerifyHostname(host); err != nil {
		return nil, err
	}
	return c, nil
}

// pruneCerts removes the held certificates of domains that are no longer stored or now have a
// Cloudflare zone, together with their state. list is domains.json, held the certificates by domain
// (entries are deleted) and st the state (entries are deleted). It returns how many were removed.
func (s *Server) pruneCerts(list []config.Domain, held map[string]*x509.Certificate, st *certState) int {
	keep := make(map[string]bool, len(list))
	for _, d := range list {
		if host, ok := certHost(d); ok {
			keep[host] = true
		}
	}
	removed := 0
	for host := range held {
		if keep[host] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.cfg.TLSDir, "live", host)); err != nil {
			s.logWarn("certificate not removed", "host", host, "err", err)
			continue
		}
		delete(held, host)
		delete(st.Hosts, host)
		removed++
		s.logInfo("certificate removed", "host", host)
	}
	return removed
}

// recheckCertified repeats the domain check of rows that hold a certificate ordered within
// certRecheckWindow, are not OK and were last checked more than certRecheckEvery ago, at most
// certRechecksPerTick of them. fresh holds the domains ordered in this tick: nginx cannot serve
// their certificate yet, so they wait for the next tick. It returns how many checks ran; a failed
// check is logged.
func (s *Server) recheckCertified(ctx context.Context, list []config.Domain, held map[string]*x509.Certificate, fresh map[string]bool, st *certState, now time.Time) int {
	n := 0
	for _, d := range list {
		if n >= certRechecksPerTick {
			break
		}
		host, ok := certHost(d)
		if !ok || d.OK || fresh[host] || held[host] == nil || !afterRFC3339(st.Hosts[host].RecheckUntil, now) {
			continue
		}
		if checked, ok := parseRFC3339(d.Checked); ok && now.Sub(checked) < certRecheckEvery {
			continue
		}
		n++
		if _, err := s.recheckDomain(ctx, d.ID); err != nil {
			s.logWarn("certificate: domain check failed", "host", host, "err", err)
		}
	}
	return n
}

// afterRFC3339 reports whether the stored time text lies after now. Empty or damaged text is false.
func afterRFC3339(text string, now time.Time) bool {
	t, ok := parseRFC3339(text)
	return ok && t.After(now)
}

// certRetryDelay returns the wait before the next order after failures failed ones in a row:
// certRetryFirst doubled per earlier failure, at most certRetryMax, and never shorter than the
// Retry-After of an ACME rate limit error err.
func certRetryDelay(failures int, err error) time.Duration {
	d := certRetryFirst
	for i := 1; i < failures && d < certRetryMax; i++ {
		d *= 2
	}
	if d > certRetryMax {
		d = certRetryMax
	}
	var ae *acme.Error
	if errors.As(err, &ae) {
		if ra, ok := acme.RateLimit(ae); ok && ra > d {
			d = ra
		}
	}
	return d
}

// clip returns text cut to at most n bytes.
func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[:n]
}

// loadCertState reads state.json of Config.TLSDir. A missing file gives an empty state; a damaged
// one is logged and gives an empty state too.
func (s *Server) loadCertState() *certState {
	st := &certState{}
	data, err := os.ReadFile(filepath.Join(s.cfg.TLSDir, "state.json"))
	if err == nil {
		if jerr := json.Unmarshal(data, st); jerr != nil {
			s.logWarn("certificates: state.json damaged, starting over", "err", jerr)
			st = &certState{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.logWarn("certificates: state.json unreadable", "err", err)
	}
	if st.Hosts == nil {
		st.Hosts = make(map[string]certHostState)
	}
	return st
}

// saveCertState writes st to state.json of Config.TLSDir with mode 0600, creating the directory.
// It returns the write error.
func (s *Server) saveCertState(st *certState) error {
	if err := os.MkdirAll(s.cfg.TLSDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.cfg.TLSDir, "state.json"), data, 0o600)
}

// acmeClient returns a client for Config.ACMEDirectory (Let's Encrypt when empty) with the account
// key account.key of Config.TLSDir, created once with mode 0600, and registers the account with the
// terms accepted and Config.ACMEEmail as the contact when set. An existing account is fine. It
// returns an error when the key cannot be read or written or the registration fails.
func (s *Server) acmeClient(ctx context.Context) (*acme.Client, error) {
	key, err := s.accountKey()
	if err != nil {
		return nil, err
	}
	dir := s.cfg.ACMEDirectory
	if dir == "" {
		dir = acme.LetsEncryptURL
	}
	c := &acme.Client{Key: key, DirectoryURL: dir, HTTPClient: s.cfg.ACMEHTTP, UserAgent: "emtds"}
	acct := &acme.Account{}
	if s.cfg.ACMEEmail != "" {
		acct.Contact = []string{"mailto:" + s.cfg.ACMEEmail}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := c.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("register: %w", err)
	}
	return c, nil
}

// accountKey reads account.key of Config.TLSDir, or creates a P-256 key there with mode 0600 when
// the file is absent. A concurrent creation keeps the file that was written first. It returns the
// key or the read, parse or write error.
func (s *Server) accountKey() (crypto.Signer, error) {
	if err := os.MkdirAll(s.cfg.TLSDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.cfg.TLSDir, "account.key")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		pemKey, err := newKeyPEM()
		if err != nil {
			return nil, err
		}
		if _, err := atomicfile.CreateIfAbsent(path, pemKey, 0o600); err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("account.key: no PEM block")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// newKeyPEM generates a P-256 private key and returns it as an EC PRIVATE KEY PEM block.
func newKeyPEM() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// issueCert orders a certificate for host with the HTTP-01 challenge and stores it in
// live/<host>/ (see writeLiveCert). The key authorization is served by handleACMEChallenge while
// the order runs. The issued leaf must name host, be valid now and carry the generated key. It
// returns the leaf, or the error of the order, the challenge, the finalization or the write.
func (s *Server) issueCert(ctx context.Context, client *acme.Client, host string) (*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, certOrderTimeout)
	defer cancel()
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(host))
	if err != nil {
		return nil, fmt.Errorf("order: %w", err)
	}
	for _, u := range order.AuthzURLs {
		if err := s.solveHTTP01(ctx, client, u); err != nil {
			return nil, err
		}
	}
	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return nil, fmt.Errorf("order: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: host},
		DNSNames: []string{host},
	}, key)
	if err != nil {
		return nil, err
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}
	leaf, err := checkIssued(chain, host, key, s.nowUTC())
	if err != nil {
		return nil, err
	}
	if err := s.writeLiveCert(host, key, chain); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return leaf, nil
}

// solveHTTP01 completes one authorization of an order. u is its URL. A valid authorization needs
// nothing; otherwise the http-01 challenge is published for handleACMEChallenge, accepted and
// waited for, and withdrawn afterwards. It returns an error when there is no http-01 challenge or
// the authorization does not become valid.
func (s *Server) solveHTTP01(ctx context.Context, client *acme.Client, u string) error {
	z, err := client.GetAuthorization(ctx, u)
	if err != nil {
		return fmt.Errorf("authorization: %w", err)
	}
	if z.Status == acme.StatusValid {
		return nil
	}
	var chal *acme.Challenge
	for _, c := range z.Challenges {
		if c.Type == "http-01" {
			chal = c
			break
		}
	}
	if chal == nil {
		return errors.New("authorization: no http-01 challenge")
	}
	answer, err := client.HTTP01ChallengeResponse(chal.Token)
	if err != nil {
		return err
	}
	s.putChallenge(chal.Token, answer)
	defer s.dropChallenge(chal.Token)
	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("challenge: %w", err)
	}
	if _, err := client.WaitAuthorization(ctx, z.URI); err != nil {
		return fmt.Errorf("challenge: %w", err)
	}
	return nil
}

// checkIssued parses the DER chain the CA returned and checks its leaf: it names host, is valid at
// now and holds the public key of key. It returns the leaf or the reason it is refused.
func checkIssued(chain [][]byte, host string, key *ecdsa.PrivateKey, now time.Time) (*x509.Certificate, error) {
	if len(chain) == 0 {
		return nil, errors.New("finalize: empty chain")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}
	if err := leaf.VerifyHostname(host); err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("finalize: certificate not valid now")
	}
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("finalize: certificate is for another key")
	}
	return leaf, nil
}

// writeLiveCert stores key and chain in live/<host>/ of Config.TLSDir: privkey.pem (mode 0600)
// first, then fullchain.pem (mode 0644), each replaced atomically. The host side keeps its previous
// pair while the two files do not match yet. It returns the first write error.
func (s *Server) writeLiveCert(host string, key *ecdsa.PrivateKey, chain [][]byte) error {
	dir := filepath.Join(s.cfg.TLSDir, "live", host)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(dir, "privkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return err
	}
	var full []byte
	for _, c := range chain {
		full = append(full, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c})...)
	}
	return atomicfile.Write(filepath.Join(dir, "fullchain.pem"), full, 0o644)
}

// putChallenge publishes the key authorization answer for token.
func (s *Server) putChallenge(token, answer string) {
	s.acmeMu.Lock()
	defer s.acmeMu.Unlock()
	if s.acmeTokens == nil {
		s.acmeTokens = make(map[string]string)
	}
	s.acmeTokens[token] = answer
}

// dropChallenge withdraws the answer for token.
func (s *Server) dropChallenge(token string) {
	s.acmeMu.Lock()
	defer s.acmeMu.Unlock()
	delete(s.acmeTokens, token)
}

// handleACMEChallenge answers GET /.well-known/acme-challenge/{token} without authentication: the
// key authorization of an order this process is running, as text/plain, or 404 for any other token.
// nginx sends the path here from port 80 and from every engine vhost on 443.
func (s *Server) handleACMEChallenge(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	s.acmeMu.Lock()
	answer, ok := s.acmeTokens[token]
	s.acmeMu.Unlock()
	if !ok || token == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, answer)
}
