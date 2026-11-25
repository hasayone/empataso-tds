package admin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
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

	"golang.org/x/crypto/acme"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// certTestIP is the public address of the server in the certificate tests.
const certTestIP = "203.0.113.10"

// certBase is the clock of the certificate tests.
var certBase = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// fakeACME is an RFC 8555 certificate authority for tests. It accepts every JWS without checking
// the signature, decides an http-01 challenge with validate and signs each CSR with its own CA for
// validity. failAccount makes new-account answer malformed. Counters record the calls.
type fakeACME struct {
	srv      *httptest.Server
	caKey    *ecdsa.PrivateKey
	caCert   *x509.Certificate
	now      func() time.Time
	validity time.Duration
	validate func(token string) error

	failAccount atomic.Bool
	accounts    atomic.Int32
	orders      atomic.Int32
	finalized   atomic.Int32

	mu     sync.Mutex
	seq    int
	known  bool
	authz  map[string]*fakeAuthz
	order  map[string]*fakeOrder
	chains map[string][]byte
}

// fakeAuthz is one authorization of fakeACME: its host, challenge token, status and failure text.
type fakeAuthz struct {
	host, token, status, problem string
}

// fakeOrder is one order of fakeACME: its host, authorization id, status and certificate id.
type fakeOrder struct {
	host, authzID, status, certID string
}

// newFakeACME starts the authority with a fresh CA. now is the clock certificates are dated by.
func newFakeACME(t *testing.T, now func() time.Time) *fakeACME {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake ACME CA"},
		NotBefore: now().Add(-time.Hour), NotAfter: now().Add(10 * 365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeACME{caKey: key, caCert: ca, now: now, validity: 90 * 24 * time.Hour,
		authz: map[string]*fakeAuthz{}, order: map[string]*fakeOrder{}, chains: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /directory", f.directory)
	mux.HandleFunc("HEAD /nonce", f.nonce)
	mux.HandleFunc("GET /nonce", f.nonce)
	mux.HandleFunc("POST /account", f.newAccount)
	mux.HandleFunc("POST /order", f.newOrder)
	mux.HandleFunc("POST /order/{id}", f.getOrder)
	mux.HandleFunc("POST /authz/{id}", f.getAuthz)
	mux.HandleFunc("POST /chal/{id}", f.accept)
	mux.HandleFunc("POST /finalize/{id}", f.finalize)
	mux.HandleFunc("POST /cert/{id}", f.cert)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", fmt.Sprintf("n%d", time.Now().UnixNano()))
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// url returns the absolute address of path on the authority.
func (f *fakeACME) url(path string) string { return f.srv.URL + path }

// payload returns the decoded payload of the JWS request body r; POST-as-GET gives nil.
func payload(r *http.Request) []byte {
	var jws struct {
		Payload string `json:"payload"`
	}
	_ = json.NewDecoder(r.Body).Decode(&jws)
	b, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
	return b
}

// reply writes v as JSON with code.
func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeACME) directory(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{
		"newNonce": f.url("/nonce"), "newAccount": f.url("/account"), "newOrder": f.url("/order"),
		"revokeCert": f.url("/revoke"), "keyChange": f.url("/key-change"),
		"meta": map[string]any{"termsOfService": f.url("/terms")},
	})
}

func (f *fakeACME) nonce(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func (f *fakeACME) newAccount(w http.ResponseWriter, r *http.Request) {
	f.accounts.Add(1)
	if f.failAccount.Load() {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:malformed","detail":"account refused"}`))
		return
	}
	f.mu.Lock()
	code := http.StatusCreated
	if f.known {
		code = http.StatusOK
	}
	f.known = true
	f.mu.Unlock()
	w.Header().Set("Location", f.url("/acct/1"))
	reply(w, code, map[string]any{"status": "valid"})
}

func (f *fakeACME) newOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Identifiers []struct{ Type, Value string } `json:"identifiers"`
	}
	if err := json.Unmarshal(payload(r), &req); err != nil || len(req.Identifiers) != 1 {
		http.Error(w, "bad order", http.StatusBadRequest)
		return
	}
	f.orders.Add(1)
	f.mu.Lock()
	f.seq++
	id := fmt.Sprint(f.seq)
	host := req.Identifiers[0].Value
	f.authz[id] = &fakeAuthz{host: host, token: "tok" + id + "x" + strings.ReplaceAll(host, ".", "-"), status: "pending"}
	f.order[id] = &fakeOrder{host: host, authzID: id, status: "pending"}
	view := f.orderView(id)
	f.mu.Unlock()
	w.Header().Set("Location", f.url("/order/"+id))
	reply(w, http.StatusCreated, view)
}

// orderView is the wire form of order id; the caller holds mu.
func (f *fakeACME) orderView(id string) map[string]any {
	o := f.order[id]
	status := o.status
	if status == "pending" {
		switch f.authz[o.authzID].status {
		case "valid":
			status = "ready"
		case "invalid":
			status = "invalid"
		}
	}
	v := map[string]any{
		"status":         status,
		"identifiers":    []map[string]string{{"type": "dns", "value": o.host}},
		"authorizations": []string{f.url("/authz/" + o.authzID)},
		"finalize":       f.url("/finalize/" + id),
	}
	if o.certID != "" {
		v["certificate"] = f.url("/cert/" + o.certID)
	}
	return v
}

func (f *fakeACME) getOrder(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.order[r.PathValue("id")] == nil {
		http.NotFound(w, r)
		return
	}
	reply(w, http.StatusOK, f.orderView(r.PathValue("id")))
}

// authzView is the wire form of authorization id; the caller holds mu.
func (f *fakeACME) authzView(id string) map[string]any {
	a := f.authz[id]
	chal := map[string]any{"type": "http-01", "url": f.url("/chal/" + id), "token": a.token, "status": a.status}
	if a.problem != "" {
		chal["error"] = map[string]string{"type": "urn:ietf:params:acme:error:unauthorized", "detail": a.problem}
	}
	return map[string]any{
		"status":     a.status,
		"identifier": map[string]string{"type": "dns", "value": a.host},
		"challenges": []any{chal},
	}
}

func (f *fakeACME) getAuthz(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authz[r.PathValue("id")] == nil {
		http.NotFound(w, r)
		return
	}
	reply(w, http.StatusOK, f.authzView(r.PathValue("id")))
}

// accept decides the challenge at once with validate, so no client ever waits.
func (f *fakeACME) accept(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f.mu.Lock()
	a := f.authz[id]
	f.mu.Unlock()
	if a == nil {
		http.NotFound(w, r)
		return
	}
	err := f.validate(a.token)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		a.status, a.problem = "invalid", err.Error()
	} else {
		a.status = "valid"
	}
	view := f.authzView(id)["challenges"].([]any)[0]
	reply(w, http.StatusOK, view)
}

func (f *fakeACME) finalize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		CSR string `json:"csr"`
	}
	_ = json.Unmarshal(payload(r), &req)
	der, err := base64.RawURLEncoding.DecodeString(req.CSR)
	if err != nil {
		http.Error(w, "csr", http.StatusBadRequest)
		return
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		http.Error(w, "csr", http.StatusBadRequest)
		return
	}
	f.finalized.Add(1)
	now := f.now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: csr.Subject, DNSNames: csr.DNSNames,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(f.validity),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leaf, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, csr.PublicKey, f.caKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.order[id]
	if o == nil || f.authz[o.authzID].status != "valid" {
		http.Error(w, "not ready", http.StatusForbidden)
		return
	}
	o.status, o.certID = "valid", id
	f.chains[id] = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.caCert.Raw})...)
	w.Header().Set("Location", f.url("/order/"+id))
	reply(w, http.StatusOK, f.orderView(id))
}

func (f *fakeACME) cert(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	chain := f.chains[r.PathValue("id")]
	f.mu.Unlock()
	if chain == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	_, _ = w.Write(chain)
}

// certEnv is an admin server wired to fakeACME: dns maps a host to the addresses it resolves to,
// probeOK decides whether the domain check sees this tracker, offset moves the clock.
type certEnv struct {
	s       *Server
	ca      *fakeACME
	tlsDir  string
	dns     map[string][]string
	dnsMu   *sync.Mutex
	probeOK *atomic.Bool
	offset  *atomic.Int64
}

// newCertEnv starts the server with the certificate schedule on, a fake domain probe and a fake
// authority that validates challenges through the server's own handler.
func newCertEnv(t *testing.T) certEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var offset atomic.Int64
	var probeOK atomic.Bool
	now := func() time.Time { return certBase.Add(time.Duration(offset.Load())) }
	probe := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probeOK.Load() {
			w.Header().Set("X-Emtds-Probe", r.URL.Query().Get("probe"))
			w.Header().Set("X-Emtds-Host", r.Host)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(probe.Close)
	e := certEnv{tlsDir: filepath.Join(dir, "tls"), dns: map[string][]string{}, dnsMu: &sync.Mutex{}, probeOK: &probeOK, offset: &offset}
	e.ca = newFakeACME(t, now)
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour,
		Domains:       NewJSONStore[config.Domain](filepath.Join(dir, "domains.json")),
		PublicIP:      certTestIP,
		TLSDir:        e.tlsDir,
		ACMEDirectory: e.ca.url("/directory"),
		ACMEHTTP:      e.ca.srv.Client(),
		CheckClient:   probeClient(t, probe),
		LookupIP: func(_ context.Context, host string) ([]net.IPAddr, error) {
			e.dnsMu.Lock()
			defer e.dnsMu.Unlock()
			var out []net.IPAddr
			for _, a := range e.dns[host] {
				out = append(out, net.IPAddr{IP: net.ParseIP(a)})
			}
			if len(out) == 0 {
				return nil, errors.New("no such host")
			}
			return out, nil
		},
		Log: noopLog{},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.ncNow = now
	e.s = s
	h := s.Handler()
	e.ca.validate = func(token string) error {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://direct.example/.well-known/acme-challenge/"+token, nil))
		want, err := e.keyAuth(token)
		if err != nil {
			return err
		}
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
			return fmt.Errorf("challenge answer %d %q", rec.Code, rec.Body.String())
		}
		return nil
	}
	return e
}

// keyAuth computes the expected HTTP-01 answer for token from account.key on disk.
func (e certEnv) keyAuth(token string) (string, error) {
	data, err := os.ReadFile(filepath.Join(e.tlsDir, "account.key"))
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("account.key")
	}
	k, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	th, err := acme.JWKThumbprint(k.Public())
	if err != nil {
		return "", err
	}
	return token + "." + th, nil
}

// point makes host resolve to addrs.
func (e certEnv) point(host string, addrs ...string) {
	e.dnsMu.Lock()
	defer e.dnsMu.Unlock()
	e.dns[host] = addrs
}

// seed replaces domains.json with list.
func (e certEnv) seed(t *testing.T, list ...config.Domain) {
	t.Helper()
	if err := e.s.cfg.Domains.Save(context.Background(), list); err != nil {
		t.Fatal(err)
	}
}

// row returns the stored domain with id.
func (e certEnv) row(t *testing.T, id string) config.Domain {
	t.Helper()
	list, err := e.s.cfg.Domains.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d, ok := domainByID(list, id)
	if !ok {
		t.Fatalf("domain %s is gone", id)
	}
	return d
}

// leaf reads the stored leaf certificate of host, or nil when there is none.
func (e certEnv) leaf(t *testing.T, host string) *x509.Certificate {
	t.Helper()
	c, err := readLeaf(filepath.Join(e.tlsDir, "live", host, "fullchain.pem"), host)
	if err != nil {
		return nil
	}
	return c
}

// state reads state.json.
func (e certEnv) state(t *testing.T) certState {
	t.Helper()
	var st certState
	data, err := os.ReadFile(filepath.Join(e.tlsDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("state.json holds a key")
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// untrustedRow is a domain the check found on this server with an untrusted certificate.
func untrustedRow(id, host string) config.Domain {
	return config.Domain{ID: id, Host: host, Role: "links", Reason: reasonUntrusted, Checked: certBase.Add(-time.Hour).Format(time.RFC3339)}
}

// TestCertOrderStoresServesAndTurnsGreen uses example.com: the probe test server's certificate names
// it, so the check after the order sees a trusted certificate, as it would once nginx serves the new one.
func TestCertOrderStoresServesAndTurnsGreen(t *testing.T) {
	const host = "example.com"
	e := newCertEnv(t)
	ctx := context.Background()
	e.seed(t, untrustedRow("1", host))
	e.point(host, certTestIP)

	rep := e.s.certOnce(ctx)
	if rep.ordered != 1 || rep.failed != 0 || rep.rechecked != 0 || e.ca.orders.Load() != 1 || e.ca.finalized.Load() != 1 {
		t.Fatalf("first tick %+v orders %d", rep, e.ca.orders.Load())
	}
	dir := filepath.Join(e.tlsDir, "live", host)
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem"))
	if err != nil {
		t.Fatalf("stored pair: %v", err)
	}
	if len(pair.Certificate) != 2 {
		t.Fatalf("chain length %d, want leaf and CA", len(pair.Certificate))
	}
	leaf := e.leaf(t, host)
	if leaf == nil || leaf.NotAfter.Sub(certBase) < 89*24*time.Hour {
		t.Fatalf("leaf %+v", leaf)
	}
	for name, want := range map[string]os.FileMode{"privkey.pem": 0o600, "fullchain.pem": 0o644} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode %v %v, want %v", name, info, err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(e.tlsDir, "account.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("account.key %v %v", info, err)
	}
	if len(e.s.acmeTokens) != 0 {
		t.Fatalf("challenge answers left after the order: %v", e.s.acmeTokens)
	}
	if st := e.state(t); st.Hosts[host].Failures != 0 || st.Hosts[host].RecheckUntil == "" {
		t.Fatalf("state %+v", st)
	}

	if rep := e.s.certOnce(ctx); rep.ordered != 0 || e.ca.orders.Load() != 1 {
		t.Fatalf("a held certificate was ordered again: %+v", rep)
	}

	e.probeOK.Store(true)
	e.offset.Add(int64(3 * time.Minute))
	if rep := e.s.certOnce(ctx); rep.rechecked != 1 {
		t.Fatalf("no recheck after the order: %+v", rep)
	}
	if got := e.row(t, "1"); !got.OK || got.Reason != "" {
		t.Fatalf("row after recheck %+v", got)
	}
	e.offset.Add(int64(3 * time.Minute))
	if rep := e.s.certOnce(ctx); rep.rechecked != 0 {
		t.Fatalf("an OK row was checked again: %+v", rep)
	}
}

func TestCertRenewsBeforeExpiry(t *testing.T) {
	e := newCertEnv(t)
	ctx := context.Background()
	e.seed(t, untrustedRow("1", "direct.example"))
	e.point("direct.example", certTestIP)
	if rep := e.s.certOnce(ctx); rep.ordered != 1 {
		t.Fatalf("first order %+v", rep)
	}
	first := e.leaf(t, "direct.example")

	e.offset.Add(int64(59 * 24 * time.Hour))
	if rep := e.s.certOnce(ctx); rep.ordered != 0 {
		t.Fatalf("renewed with 31 days left: %+v", rep)
	}
	e.offset.Add(int64(2 * 24 * time.Hour))
	if rep := e.s.certOnce(ctx); rep.ordered != 1 || e.ca.orders.Load() != 2 {
		t.Fatalf("no renewal with 29 days left: %+v", rep)
	}
	second := e.leaf(t, "direct.example")
	if second == nil || !second.NotAfter.After(first.NotAfter) {
		t.Fatalf("renewed leaf %v, first %v", second, first)
	}
	if rep := e.s.certOnce(ctx); rep.ordered != 0 {
		t.Fatalf("renewed twice: %+v", rep)
	}
}

func TestCertRenewalNotLoopingOnShortLifetime(t *testing.T) {
	e := newCertEnv(t)
	ctx := context.Background()
	e.ca.validity = 6 * 24 * time.Hour
	e.seed(t, untrustedRow("1", "direct.example"))
	e.point("direct.example", certTestIP)
	e.s.certOnce(ctx)
	e.offset.Add(int64(time.Hour))
	if rep := e.s.certOnce(ctx); rep.ordered != 0 {
		t.Fatalf("a six-day certificate was ordered again after an hour: %+v", rep)
	}
	e.offset.Add(int64(4*24*time.Hour + time.Hour))
	if rep := e.s.certOnce(ctx); rep.ordered != 1 {
		t.Fatalf("a six-day certificate was not renewed in its last third: %+v", rep)
	}
}

func TestCertFailureBacksOff(t *testing.T) {
	e := newCertEnv(t)
	ctx := context.Background()
	e.seed(t, untrustedRow("1", "direct.example"))
	e.point("direct.example", certTestIP)
	e.ca.validate = func(string) error { return errors.New("connection refused on port 80") }

	if rep := e.s.certOnce(ctx); rep.ordered != 1 || rep.failed != 1 {
		t.Fatalf("first tick %+v", rep)
	}
	if e.leaf(t, "direct.example") != nil || e.ca.finalized.Load() != 0 {
		t.Fatal("a failed order stored a certificate")
	}
	hs := e.state(t).Hosts["direct.example"]
	if hs.Failures != 1 || hs.NextTry != certBase.Add(time.Hour).Format(time.RFC3339) || !strings.Contains(hs.LastError, "port 80") {
		t.Fatalf("state after the first failure %+v", hs)
	}
	for i := 0; i < 3; i++ {
		e.offset.Add(int64(10 * time.Minute))
		e.s.certOnce(ctx)
	}
	if e.ca.orders.Load() != 1 {
		t.Fatalf("the CA was asked again before the retry time: %d orders", e.ca.orders.Load())
	}
	e.offset.Add(int64(31 * time.Minute))
	if rep := e.s.certOnce(ctx); rep.failed != 1 || e.ca.orders.Load() != 2 {
		t.Fatalf("no retry after an hour: %+v", rep)
	}
	hs = e.state(t).Hosts["direct.example"]
	if hs.Failures != 2 || hs.NextTry != certBase.Add(61*time.Minute+2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("second failure %+v", hs)
	}
	if len(e.s.acmeTokens) != 0 {
		t.Fatalf("challenge answers left after a failure: %v", e.s.acmeTokens)
	}

	e.ca.validate = func(string) error { return nil }
	e.offset.Add(int64(2 * time.Hour))
	if rep := e.s.certOnce(ctx); rep.ordered != 1 || rep.failed != 0 || e.leaf(t, "direct.example") == nil {
		t.Fatalf("no certificate after the CA recovered: %+v", rep)
	}
	if hs := e.state(t).Hosts["direct.example"]; hs.Failures != 0 || hs.NextTry != "" || hs.LastError != "" {
		t.Fatalf("state not reset after success %+v", hs)
	}
}

func TestCertAccountFailurePausesEveryOrder(t *testing.T) {
	e := newCertEnv(t)
	ctx := context.Background()
	e.seed(t, untrustedRow("1", "direct.example"))
	e.point("direct.example", certTestIP)
	e.ca.failAccount.Store(true)
	if rep := e.s.certOnce(ctx); rep.ordered != 0 || e.ca.accounts.Load() != 1 || e.ca.orders.Load() != 0 {
		t.Fatalf("first tick %+v accounts %d", rep, e.ca.accounts.Load())
	}
	st := e.state(t)
	if st.PauseFailures != 1 || st.PauseUntil != certBase.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("pause %+v", st)
	}
	e.offset.Add(int64(30 * time.Minute))
	e.s.certOnce(ctx)
	if e.ca.accounts.Load() != 1 {
		t.Fatalf("the account was asked again during the pause: %d", e.ca.accounts.Load())
	}
	e.ca.failAccount.Store(false)
	e.offset.Add(int64(31 * time.Minute))
	if rep := e.s.certOnce(ctx); rep.ordered != 1 || e.leaf(t, "direct.example") == nil {
		t.Fatalf("no order after the pause: %+v", rep)
	}
	if st := e.state(t); st.PauseFailures != 0 || st.PauseUntil != "" {
		t.Fatalf("pause not cleared %+v", st)
	}
}

func TestCertPicksOnlyDirectDomainsOnePerTick(t *testing.T) {
	e := newCertEnv(t)
	ctx := context.Background()
	cf := untrustedRow("1", "cloud.example")
	cf.CfZoneID, cf.CfStatus = "zone1", cfStatusActive
	elsewhere := untrustedRow("2", "elsewhere.example")
	imported := config.Domain{ID: "3", Host: "imported.example", Role: "links"}
	admin := untrustedRow("4", "admin.direct.example")
	port := untrustedRow("5", "port.example:8443")
	ip := untrustedRow("6", certTestIP)
	v6 := untrustedRow("7", "dual.example")
	disabled := untrustedRow("8", "off.example")
	disabled.Disabled = true
	a := untrustedRow("9", "a.example")
	b := untrustedRow("10", "b.example")
	nc := config.Domain{ID: "11", Host: "bought.example", Role: "links", Registrar: namecheapRegistrar, Reason: "dns"}
	e.seed(t, cf, elsewhere, imported, admin, port, ip, v6, disabled, a, b, nc)
	for _, h := range []string{"cloud.example", "imported.example", "admin.direct.example", "port.example", "off.example", "a.example", "b.example", "bought.example"} {
		e.point(h, certTestIP)
	}
	e.point("elsewhere.example", "198.51.100.7")
	e.point("dual.example", certTestIP, "2001:db8::1")

	got := map[string]bool{}
	for i := 0; i < 6; i++ {
		before := e.ca.orders.Load()
		e.s.certOnce(ctx)
		if n := e.ca.orders.Load() - before; n > 1 {
			t.Fatalf("tick %d made %d orders", i, n)
		}
		e.offset.Add(int64(certEvery))
	}
	entries, _ := os.ReadDir(filepath.Join(e.tlsDir, "live"))
	for _, en := range entries {
		got[en.Name()] = true
	}
	want := map[string]bool{"a.example": true, "b.example": true, "bought.example": true}
	if len(got) != len(want) {
		t.Fatalf("certificates for %v, want %v", got, want)
	}
	for h := range want {
		if !got[h] {
			t.Fatalf("certificates for %v, want %v", got, want)
		}
	}
	if e.ca.orders.Load() != 3 {
		t.Fatalf("orders %d, want 3", e.ca.orders.Load())
	}
}

func TestCertRemovedWhenDomainMovesToCloudflare(t *testing.T) {
	e := newCertEnv(t)
	ctx := context.Background()
	e.seed(t, untrustedRow("1", "direct.example"), untrustedRow("2", "gone.example"))
	e.point("direct.example", certTestIP)
	e.point("gone.example", certTestIP)
	e.s.certOnce(ctx)
	e.offset.Add(int64(certEvery))
	e.s.certOnce(ctx)
	if e.leaf(t, "direct.example") == nil || e.leaf(t, "gone.example") == nil {
		t.Fatal("setup: both certificates expected")
	}
	moved := untrustedRow("1", "direct.example")
	moved.CfZoneID, moved.CfStatus = "zone1", cfStatusActive
	e.seed(t, moved)
	if rep := e.s.certOnce(ctx); rep.removed != 2 || rep.ordered != 0 {
		t.Fatalf("prune %+v", rep)
	}
	for _, h := range []string{"direct.example", "gone.example"} {
		if _, err := os.Stat(filepath.Join(e.tlsDir, "live", h)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still has a certificate: %v", h, err)
		}
	}
}

func TestCertsOffWithoutPublicIPOrDir(t *testing.T) {
	for _, tune := range []func(*Server){
		func(s *Server) { s.cfg.PublicIP = "" },
		func(s *Server) { s.cfg.TLSDir = "" },
	} {
		e := newCertEnv(t)
		e.seed(t, untrustedRow("1", "direct.example"))
		e.point("direct.example", certTestIP)
		tune(e.s)
		if rep := e.s.certOnce(context.Background()); rep != (certReport{}) || e.ca.accounts.Load() != 0 {
			t.Fatalf("schedule ran while off: %+v", rep)
		}
	}
}

func TestACMEChallengeRouteIsPublic(t *testing.T) {
	e := newCertEnv(t)
	srv := httptest.NewServer(e.s.Handler())
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/.well-known/acme-challenge/unknown")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown token: %d, want 404", res.StatusCode)
	}
	e.s.putChallenge("tok1", "tok1.thumb")
	res, err = http.Get(srv.URL + "/.well-known/acme-challenge/tok1")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 64)
	n, _ := res.Body.Read(body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body[:n]) != "tok1.thumb" {
		t.Fatalf("published token: %d %q", res.StatusCode, body[:n])
	}
	e.s.dropChallenge("tok1")
	res, err = http.Get(srv.URL + "/.well-known/acme-challenge/tok1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("withdrawn token: %d", res.StatusCode)
	}
}

func TestCertNameOK(t *testing.T) {
	for host, want := range map[string]bool{
		"example.com": true, "a.b.example.com": true, "xn--80ak6aa92e.com": true, "my-site.io": true,
		"admin.example.com": false, "203.0.113.10": false, "example.com:8443": false, "localhost": false,
		"*.example.com": false, "Example.com": false, "-bad.com": false, "": false,
	} {
		if got := certNameOK(host); got != want {
			t.Fatalf("certNameOK(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestCertRetryDelay(t *testing.T) {
	for failures, want := range map[int]time.Duration{1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour, 5: 16 * time.Hour, 6: 24 * time.Hour, 20: 24 * time.Hour} {
		if got := certRetryDelay(failures, errors.New("x")); got != want {
			t.Fatalf("certRetryDelay(%d) = %v, want %v", failures, got, want)
		}
	}
	limited := &acme.Error{StatusCode: http.StatusTooManyRequests, ProblemType: "urn:ietf:params:acme:error:rateLimited",
		Header: http.Header{"Retry-After": []string{"172800"}}}
	if got := certRetryDelay(1, fmt.Errorf("order: %w", limited)); got != 48*time.Hour {
		t.Fatalf("rate limit delay %v, want 48h", got)
	}
}
