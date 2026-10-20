package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

const (
	cfTestToken   = "cf-secret-token-123"
	cfTestAccount = "0123456789abcdef0123456789abcdef"
	cfTestIP      = "203.0.113.50"
)

// cfTestNS are the name servers the fake gives every zone it creates.
var cfTestNS = []string{"amy.ns.cloudflare.com", "bob.ns.cloudflare.com"}

// fakeCF is a local stand-in for the Cloudflare API. Tests never reach a real host.
type fakeCF struct {
	mu       sync.Mutex
	srv      *httptest.Server
	calls    []string
	auth     []string
	zones    []*cfZone
	records  map[string][]cfRecord
	settings map[string]map[string]string
	// accountToken makes /user/tokens/verify refuse the token the way an account token is refused.
	accountToken bool
	// fail maps "METHOD /path" to the message of an error answer.
	fail   map[string]string
	nextID int
}

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{records: map[string][]cfRecord{}, settings: map[string]map[string]string{}, fail: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCF) reply(w http.ResponseWriter, status int, ok bool, result any, total int, errs ...cfAPIError) {
	if errs == nil {
		errs = []cfAPIError{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": ok, "errors": errs, "result": result, "result_info": map[string]int{"total_count": total},
	})
}

func (f *fakeCF) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if msg, ok := f.fail[call]; ok {
		f.reply(w, http.StatusBadRequest, false, nil, 0, cfAPIError{Code: 1004, Message: msg})
		return
	}
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case call == "GET /user/tokens/verify":
		if f.accountToken {
			f.reply(w, http.StatusUnauthorized, false, nil, 0, cfAPIError{Code: 1000, Message: "Invalid API Token"})
			return
		}
		f.reply(w, http.StatusOK, true, map[string]string{"status": "active"}, 0)
	case len(parts) == 4 && parts[0] == "accounts" && parts[2] == "tokens":
		f.reply(w, http.StatusOK, true, map[string]string{"status": "active"}, 0)
	case call == "GET /zones":
		name := r.URL.Query().Get("name")
		out := []*cfZone{}
		for _, z := range f.zones {
			if name == "" || z.Name == name {
				out = append(out, z)
			}
		}
		f.reply(w, http.StatusOK, true, out, len(out))
	case call == "POST /zones":
		f.nextID++
		z := &cfZone{ID: fmt.Sprintf("zone%03d", f.nextID), Name: fmt.Sprint(body["name"]), Status: "pending", NameServers: cfTestNS}
		f.zones = append(f.zones, z)
		f.reply(w, http.StatusOK, true, z, 0)
	case len(parts) == 2 && parts[0] == "zones" && r.Method == http.MethodGet:
		if z := f.zoneByID(parts[1]); z != nil {
			f.reply(w, http.StatusOK, true, z, 0)
			return
		}
		f.reply(w, http.StatusNotFound, false, nil, 0, cfAPIError{Code: 1001, Message: "Zone not found"})
	case len(parts) == 3 && parts[2] == "dns_records" && r.Method == http.MethodGet:
		name := r.URL.Query().Get("name")
		out := []cfRecord{}
		for _, rec := range f.records[parts[1]] {
			if name == "" || rec.Name == name {
				out = append(out, rec)
			}
		}
		f.reply(w, http.StatusOK, true, out, len(out))
	case len(parts) == 3 && parts[2] == "dns_records" && r.Method == http.MethodPost:
		f.nextID++
		rec := cfRecord{ID: fmt.Sprintf("rec%03d", f.nextID), Type: fmt.Sprint(body["type"]), Name: fmt.Sprint(body["name"]), Content: fmt.Sprint(body["content"]), Proxied: body["proxied"] == true}
		f.records[parts[1]] = append(f.records[parts[1]], rec)
		f.reply(w, http.StatusOK, true, rec, 0)
	case len(parts) == 4 && parts[2] == "dns_records" && r.Method == http.MethodPatch:
		list := f.records[parts[1]]
		for i := range list {
			if list[i].ID == parts[3] {
				list[i].Content, list[i].Proxied = fmt.Sprint(body["content"]), body["proxied"] == true
				f.reply(w, http.StatusOK, true, list[i], 0)
				return
			}
		}
		f.reply(w, http.StatusNotFound, false, nil, 0, cfAPIError{Code: 81044, Message: "Record not found"})
	case len(parts) == 4 && parts[2] == "settings" && r.Method == http.MethodPatch:
		if f.settings[parts[1]] == nil {
			f.settings[parts[1]] = map[string]string{}
		}
		f.settings[parts[1]][parts[3]] = fmt.Sprint(body["value"])
		f.reply(w, http.StatusOK, true, map[string]any{"id": parts[3], "value": body["value"]}, 0)
	case len(parts) == 3 && parts[2] == "activation_check" && r.Method == http.MethodPut:
		f.reply(w, http.StatusOK, true, map[string]string{"id": parts[1]}, 0)
	default:
		f.reply(w, http.StatusNotFound, false, nil, 0, cfAPIError{Code: 7000, Message: "No route for that URI"})
	}
}

// zoneByID returns the zone with the id. The caller holds f.mu.
func (f *fakeCF) zoneByID(id string) *cfZone {
	for _, z := range f.zones {
		if z.ID == id {
			return z
		}
	}
	return nil
}

// count returns how many requests matched call, "METHOD /path".
func (f *fakeCF) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

// writes returns how many calls changed something: every call that is not a GET and not an
// activation check.
func (f *fakeCF) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") && !strings.HasSuffix(c, "/activation_check") {
			n++
		}
	}
	return n
}

// setStatus sets the status of the zone named name.
func (f *fakeCF) setStatus(name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, z := range f.zones {
		if z.Name == name {
			z.Status = status
		}
	}
}

// recordsOf returns a copy of the records of the zone named name.
func (f *fakeCF) recordsOf(name string) []cfRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, z := range f.zones {
		if z.Name == name {
			return append([]cfRecord(nil), f.records[z.ID]...)
		}
	}
	return nil
}

// cfEnv is an admin server wired to a fake Cloudflare and a fake Namecheap.
type cfEnv struct {
	ncEnv
	cf *fakeCF
}

// newCFEnv starts an admin server with both fakes and Config.PublicIP set to cfTestIP.
// tune may change the Config before the server starts and may be nil.
func newCFEnv(t *testing.T, tune func(*Config)) cfEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	live, sandbox, cf := newFakeNC(t), newFakeNC(t), newFakeCF(t)
	cfg := Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour,
		Domains:       NewJSONStore[config.Domain](filepath.Join(dir, "domains.json")),
		SeqDomains:    NewIDSeq(filepath.Join(dir, "domain_seq.json")),
		APIKeysFile:   filepath.Join(dir, "api-keys.json"),
		UsersFile:     filepath.Join(dir, "users.json"),
		NamecheapFile: filepath.Join(dir, "namecheap.json"),
		NamecheapURL:  live.srv.URL, NamecheapSandboxURL: sandbox.srv.URL,
		CloudflareFile: filepath.Join(dir, "cloudflare.json"), CloudflareURL: cf.srv.URL,
		PublicIP: cfTestIP,
		Log:      noopLog{},
	}
	if tune != nil {
		tune(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return cfEnv{ncEnv: ncEnv{s: s, srv: srv, live: live, sandbox: sandbox, dir: dir}, cf: cf}
}

// configureCF saves a ready Cloudflare account through the API.
func (e cfEnv) configureCF(t *testing.T) {
	t.Helper()
	code, raw := e.do(t, http.MethodPut, "/api/cloudflare", map[string]string{"token": cfTestToken, "account_id": cfTestAccount})
	if code != http.StatusOK {
		t.Fatalf("configure cloudflare %d %s", code, raw)
	}
}

// seed stores the domains as the current catalog.
func (e cfEnv) seed(t *testing.T, list ...config.Domain) {
	t.Helper()
	if err := e.s.cfg.Domains.Save(context.Background(), list); err != nil {
		t.Fatal(err)
	}
}

// row returns the stored domain with the id.
func (e cfEnv) row(t *testing.T, id string) config.Domain {
	t.Helper()
	d, ok := domainByID(e.domains(t), id)
	if !ok {
		t.Fatalf("domain %s is gone", id)
	}
	return d
}

func TestCloudflareSettingsHideToken(t *testing.T) {
	e := newCFEnv(t, nil)
	code, raw := e.do(t, http.MethodGet, "/api/cloudflare", nil)
	if code != http.StatusOK || !strings.Contains(raw, `"configured":false`) || !strings.Contains(raw, `"public_ip":"`+cfTestIP+`"`) {
		t.Fatalf("empty get %d %s", code, raw)
	}
	e.configureCF(t)
	code, raw = e.do(t, http.MethodGet, "/api/cloudflare", nil)
	if code != http.StatusOK || strings.Contains(raw, cfTestToken) || strings.Contains(raw, `"token"`) {
		t.Fatalf("get leaks the token: %d %s", code, raw)
	}
	var v cfSettingsView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	if v.AccountID != cfTestAccount || !v.HasToken || !v.Configured || v.PublicIP != cfTestIP {
		t.Fatalf("view %+v", v)
	}
}

func TestCloudflareSettingsKeepTokenAndValidate(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	other := "ABCDEF0123456789ABCDEF0123456789"
	for _, token := range []string{"", "  ", "****", "••••"} {
		code, raw := e.do(t, http.MethodPut, "/api/cloudflare", map[string]string{"token": token, "account_id": other})
		if code != http.StatusOK {
			t.Fatalf("put %q %d %s", token, code, raw)
		}
		if a := e.s.cloudflareAccount(); a.Token != cfTestToken || a.AccountID != strings.ToLower(other) {
			t.Fatalf("token %q changed the account: %+v", token, a)
		}
	}
	if code, _ := e.do(t, http.MethodPut, "/api/cloudflare", map[string]string{"account_id": "not-hex"}); code != http.StatusBadRequest {
		t.Fatalf("bad account id: %d", code)
	}
	if code, _ := e.do(t, http.MethodPut, "/api/cloudflare", map[string]string{"token": "bad token!"}); code != http.StatusBadRequest {
		t.Fatalf("bad token: %d", code)
	}
	if a := e.s.cloudflareAccount(); a.Token != cfTestToken {
		t.Fatalf("a refused PUT changed the account: %+v", a)
	}
}

func TestCloudflareSettingsFileAndRestart(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	path := filepath.Join(e.dir, "cloudflare.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	next, err := New(Config{AdminUser: "admin", PasswordHash: e.s.currentHash(), Sessions: security.NewMemoryStore(), Limiter: allowAll{}, CloudflareFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if a := next.cloudflareAccount(); a.Token != cfTestToken || a.AccountID != cfTestAccount {
		t.Fatalf("restart lost the account: %+v", a)
	}
	if err := os.WriteFile(path, []byte(`{"account_id":"zz"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := New(Config{AdminUser: "admin", PasswordHash: e.s.currentHash(), Sessions: security.NewMemoryStore(), Limiter: allowAll{}, CloudflareFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if broken.cloudflareAccount().ready() || broken.cloudflareLoadError() == "" {
		t.Fatal("a damaged file was used")
	}
}

func TestCloudflareSettingsWithoutFile(t *testing.T) {
	e := newCFEnv(t, func(c *Config) { c.CloudflareFile = "" })
	code, _ := e.do(t, http.MethodPut, "/api/cloudflare", map[string]string{"token": cfTestToken, "account_id": cfTestAccount})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("put without file: %d", code)
	}
}

func TestPublicIPIsValidated(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	base := Config{AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(), Limiter: allowAll{}}
	base.PublicIP = "not-an-ip"
	if _, err := New(base); err == nil {
		t.Fatal("a bad public ip was accepted")
	}
	for in, want := range map[string]string{"203.0.113.9": "203.0.113.9", " 2001:db8::1 ": "2001:db8::1", "": ""} {
		base.PublicIP = in
		s, err := New(base)
		if err != nil || s.cfg.PublicIP != want {
			t.Fatalf("%q: %v %q", in, err, s.cfg.PublicIP)
		}
	}
}

func TestCloudflareTestEndpoint(t *testing.T) {
	e := newCFEnv(t, nil)
	e.cf.zones = []*cfZone{{ID: "a", Name: "a.com"}, {ID: "b", Name: "b.com"}}
	if code, _ := e.do(t, http.MethodPost, "/api/cloudflare/test", map[string]string{}); code != http.StatusBadRequest {
		t.Fatalf("test without credentials: %d", code)
	}
	body := map[string]string{"token": cfTestToken, "account_id": cfTestAccount}
	code, raw := e.do(t, http.MethodPost, "/api/cloudflare/test", body)
	if code != http.StatusOK || !strings.Contains(raw, `"ok":true`) || !strings.Contains(raw, `"zones":2`) {
		t.Fatalf("test %d %s", code, raw)
	}
	if e.s.cloudflareAccount().ready() {
		t.Fatal("the test saved the account")
	}
	e.cf.accountToken = true
	code, raw = e.do(t, http.MethodPost, "/api/cloudflare/test", body)
	if code != http.StatusOK || !strings.Contains(raw, `"ok":true`) || e.cf.count("GET /accounts/"+cfTestAccount+"/tokens/verify") != 1 {
		t.Fatalf("account token %d %s", code, raw)
	}
	e.cf.fail["GET /zones"] = "token " + cfTestToken + " may not list zones"
	code, raw = e.do(t, http.MethodPost, "/api/cloudflare/test", body)
	if code != http.StatusOK || !strings.Contains(raw, `"ok":false`) || strings.Contains(raw, cfTestToken) || !strings.Contains(raw, "may not list zones") {
		t.Fatalf("refused test %d %s", code, raw)
	}
}

func TestCloudflareClientSendsBearerOnly(t *testing.T) {
	f := newFakeCF(t)
	c := newCFClient(f.srv.URL, cfAccount{Token: cfTestToken, AccountID: cfTestAccount}, nil)
	if err := c.verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.auth) != 1 || f.auth[0] != "Bearer "+cfTestToken {
		t.Fatalf("auth %v", f.auth)
	}
	f.fail["GET /zones"] = "denied for " + cfTestToken
	_, err := c.listZonesCount(context.Background())
	if err == nil || strings.Contains(err.Error(), cfTestToken) || !strings.Contains(err.Error(), "denied for") {
		t.Fatalf("error %v", err)
	}
}

func TestCloudflareClientUpsertApex(t *testing.T) {
	ctx := context.Background()
	f := newFakeCF(t)
	c := newCFClient(f.srv.URL, cfAccount{Token: cfTestToken, AccountID: cfTestAccount}, nil)
	z, err := c.ensureZone(ctx, "a.com")
	if err != nil || z.Status != "pending" {
		t.Fatalf("zone %+v %v", z, err)
	}
	again, err := c.ensureZone(ctx, "a.com")
	if err != nil || again.ID != z.ID || f.count("POST /zones") != 1 {
		t.Fatalf("ensure created a second zone: %+v %v", again, err)
	}
	if err := c.upsertApexA(ctx, z.ID, "a.com", cfTestIP); err != nil {
		t.Fatal(err)
	}
	if recs := f.recordsOf("a.com"); len(recs) != 1 || recs[0].Type != "A" || recs[0].Content != cfTestIP || !recs[0].Proxied {
		t.Fatalf("records %+v", recs)
	}
	before := f.writes()
	if err := c.upsertApexA(ctx, z.ID, "a.com", cfTestIP); err != nil || f.writes() != before {
		t.Fatalf("same record was written again: %v", err)
	}
	if err := c.upsertApexA(ctx, z.ID, "a.com", "198.51.100.9"); err != nil {
		t.Fatal(err)
	}
	if recs := f.recordsOf("a.com"); len(recs) != 1 || recs[0].Content != "198.51.100.9" || !recs[0].Proxied {
		t.Fatalf("records after change %+v", recs)
	}
	if err := c.upsertApexA(ctx, z.ID, "a.com", "2001:db8::7"); err != nil {
		t.Fatal(err)
	}
	if recs := f.recordsOf("a.com"); len(recs) != 2 || recs[1].Type != "AAAA" {
		t.Fatalf("ipv6 records %+v", recs)
	}
}

func TestCloudflareClientRefusesApexCNAMEAndMany(t *testing.T) {
	ctx := context.Background()
	f := newFakeCF(t)
	c := newCFClient(f.srv.URL, cfAccount{Token: cfTestToken, AccountID: cfTestAccount}, nil)
	z, _ := c.ensureZone(ctx, "c.com")
	f.records[z.ID] = []cfRecord{{ID: "r1", Type: "CNAME", Name: "c.com", Content: "x.example"}}
	if err := c.upsertApexA(ctx, z.ID, "c.com", cfTestIP); err != errCFApexCNAME {
		t.Fatalf("cname: %v", err)
	}
	f.records[z.ID] = []cfRecord{{ID: "r1", Type: "A", Name: "c.com", Content: "198.51.100.1"}, {ID: "r2", Type: "A", Name: "c.com", Content: "198.51.100.2"}}
	if err := c.upsertApexA(ctx, z.ID, "c.com", cfTestIP); err != errCFApexMany {
		t.Fatalf("many: %v", err)
	}
	if f.writes() != 1 {
		t.Fatalf("a refused apex was changed: %v", f.calls)
	}
}

func ncOwnedRow(host string) config.Domain {
	return config.Domain{ID: "1", Host: host, Role: "links", Registrar: namecheapRegistrar, RegistrarAccount: ncTestName, CfStatus: cfStatusConnectPending}
}

func TestConnectCloudflareNamecheapDomain(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	e.configureCF(t)
	e.live.setOwned("mine.com")
	e.seed(t, ncOwnedRow("mine.com"))
	code, raw := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true})
	if code != http.StatusOK {
		t.Fatalf("connect %d %s", code, raw)
	}
	if strings.Contains(raw, cfTestToken) || strings.Contains(raw, ncTestKey) {
		t.Fatalf("answer leaks a secret: %s", raw)
	}
	var out struct {
		config.Domain
		NS       []string `json:"ns"`
		NSSet    bool     `json:"ns_set"`
		NSManual bool     `json:"ns_manual"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.CfZoneID == "" || out.CfStatus != cfStatusPending || out.OK || out.Reason != reasonCFPending ||
		out.ReasonDetail != strings.Join(cfTestNS, ",") || out.WatchSince == "" || len(out.CfNS) != 2 {
		t.Fatalf("row %+v", out.Domain)
	}
	if !out.NSSet || out.NSManual || strings.Join(out.NS, ",") != strings.Join(cfTestNS, ",") {
		t.Fatalf("ns outcome %+v", out)
	}
	if recs := e.cf.recordsOf("mine.com"); len(recs) != 1 || recs[0].Content != cfTestIP || !recs[0].Proxied {
		t.Fatalf("records %+v", recs)
	}
	settings := e.cf.settings[out.CfZoneID]
	if settings["ssl"] != "full" || settings["always_use_https"] != "on" {
		t.Fatalf("settings %+v", settings)
	}
	if form := e.live.last("namecheap.domains.dns.setCustom"); form["SLD"] != "mine" || form["Nameservers"] != strings.Join(cfTestNS, ",") {
		t.Fatalf("setCustom %+v", form)
	}
	if e.cf.count("PUT /zones/"+out.CfZoneID+"/activation_check") != 1 {
		t.Fatalf("activation checks: %v", e.cf.calls)
	}
	customs := e.live.count("namecheap.domains.dns.setCustom")
	if code, raw := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true}); code != http.StatusOK {
		t.Fatalf("second connect %d %s", code, raw)
	}
	recordWrites := 0
	for _, c := range e.cf.calls {
		if !strings.HasPrefix(c, "GET ") && strings.Contains(c, "/dns_records") {
			recordWrites++
		}
	}
	if e.cf.count("POST /zones") != 1 || recordWrites != 1 || e.live.count("namecheap.domains.dns.setCustom") != customs {
		t.Fatalf("a second connect changed things: %v", e.cf.calls)
	}
}

func TestConnectCloudflareForeignRegistrarIsManual(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	e.configureCF(t)
	row := ncOwnedRow("other.com")
	row.Registrar, row.RegistrarAccount = "", ""
	e.seed(t, row)
	code, raw := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true})
	if code != http.StatusOK || !strings.Contains(raw, `"ns_manual":true`) || !strings.Contains(raw, `"ns_set":false`) {
		t.Fatalf("connect %d %s", code, raw)
	}
	if n := e.live.count("namecheap.domains.dns.setCustom") + e.live.count("namecheap.domains.getInfo"); n != 0 {
		t.Fatalf("namecheap was called %d times for another registrar", n)
	}
	// A Namecheap domain the account does not own gets no name server change either.
	row.ID, row.Host, row.Registrar, row.RegistrarAccount = "2", "notmine.com", namecheapRegistrar, ncTestName
	e.seed(t, e.row(t, "1"), row)
	e.live.notOwner["notmine.com"] = true
	e.live.setOwned("notmine.com")
	code, raw = e.do(t, http.MethodPost, "/api/domains/2/cloudflare", map[string]any{"confirm": true})
	if code != http.StatusOK || !strings.Contains(raw, `"ns_manual":true`) || e.live.count("namecheap.domains.dns.setCustom") != 0 {
		t.Fatalf("not owned %d %s", code, raw)
	}
}

func TestConnectCloudflareActiveZoneKeepsCheckState(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.cf.zones = []*cfZone{{ID: "zone900", Name: "live.com", Status: cfStatusActive, NameServers: cfTestNS}}
	row := config.Domain{ID: "1", Host: "live.com", Role: "links", CfStatus: cfStatusConnectPending}
	e.seed(t, row)
	if code, raw := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true}); code != http.StatusOK {
		t.Fatalf("connect %d %s", code, raw)
	}
	got := e.row(t, "1")
	if got.CfStatus != cfStatusActive || got.CfZoneID != "zone900" || got.Reason != "" {
		t.Fatalf("row %+v", got)
	}
	if e.cf.count("PUT /zones/zone900/activation_check") != 0 {
		t.Fatal("an active zone was asked to check its name servers")
	}
}

func TestConnectCloudflareRecreatesVanishedZone(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	row := config.Domain{ID: "1", Host: "gone.com", Role: "links", CfZoneID: "deadzone", CfStatus: cfStatusPending}
	e.seed(t, row)
	if code, raw := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true}); code != http.StatusOK {
		t.Fatalf("connect %d %s", code, raw)
	}
	if got := e.row(t, "1"); got.CfZoneID == "deadzone" || got.CfZoneID == "" {
		t.Fatalf("zone id %q", got.CfZoneID)
	}
}

func TestConnectCloudflareRefusals(t *testing.T) {
	e := newCFEnv(t, nil)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links"}, config.Domain{ID: "2", Host: "b.com", Role: "links", Registrar: namecheapRegistrar, RegistrarStatus: registrarPending, Disabled: true})
	post := func(id string, body any) int {
		code, _ := e.do(t, http.MethodPost, "/api/domains/"+id+"/cloudflare", body)
		return code
	}
	if code := post("1", map[string]any{"confirm": false}); code != http.StatusBadRequest {
		t.Fatalf("without confirm: %d", code)
	}
	if code := post("1", map[string]any{"confirm": true}); code != http.StatusConflict {
		t.Fatalf("cloudflare not configured: %d", code)
	}
	e.configureCF(t)
	if code := post("404", map[string]any{"confirm": true}); code != http.StatusNotFound {
		t.Fatalf("unknown domain: %d", code)
	}
	if code := post("2", map[string]any{"confirm": true}); code != http.StatusConflict {
		t.Fatalf("pending purchase: %d", code)
	}
	if n := len(e.cf.calls); n != 0 {
		t.Fatalf("a refused connection reached Cloudflare: %v", e.cf.calls)
	}
	noIP := newCFEnv(t, func(c *Config) { c.PublicIP = "" })
	noIP.configureCF(t)
	noIP.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links"})
	if code, raw := noIP.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true}); code != http.StatusConflict || !strings.Contains(raw, "public ip") {
		t.Fatalf("no public ip: %d %s", code, raw)
	}
	if rule := routeAccess["POST /api/domains/{id}/cloudflare"]; !rule.admin || rule.sec != secDomains {
		t.Fatalf("rule %+v", rule)
	}
}

func TestConnectCloudflareFailureKeepsConnectPending(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links", CfStatus: cfStatusConnectPending})
	e.cf.fail["POST /zones"] = "account " + cfTestToken + " cannot add zones"
	code, raw := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true})
	if code != http.StatusBadGateway || strings.Contains(raw, cfTestToken) || !strings.Contains(raw, "cannot add zones") {
		t.Fatalf("failed connect %d %s", code, raw)
	}
	if got := e.row(t, "1"); got.CfStatus != cfStatusConnectPending || got.CfZoneID != "" {
		t.Fatalf("row %+v", got)
	}
}

func TestConnectCloudflareOneAtATime(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configureCF(t)
	e.seed(t, config.Domain{ID: "1", Host: "a.com", Role: "links"})
	if !e.s.cfBusy.acquire("1") {
		t.Fatal("lock not free")
	}
	if code, _ := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true}); code != http.StatusConflict {
		t.Fatalf("parallel connect: %d", code)
	}
	e.s.cfBusy.release("1")
	if code, _ := e.do(t, http.MethodPost, "/api/domains/1/cloudflare", map[string]any{"confirm": true}); code != http.StatusOK {
		t.Fatalf("connect after release: %d", code)
	}
}

func TestConnectedNoticesAndAck(t *testing.T) {
	e := newCFEnv(t, nil)
	e.seed(t,
		config.Domain{ID: "1", Host: "a.com", Role: "links", ConnectedAt: "2026-10-20T10:00:00Z"},
		config.Domain{ID: "2", Host: "b.com", Role: "links", ConnectedAt: "2026-10-20T11:00:00Z", ConnectSeen: true},
		config.Domain{ID: "3", Host: "c.com", Role: "links"},
		config.Domain{ID: "4", Host: "d.com", Role: "links", ConnectedAt: "2026-10-20T12:00:00Z"},
	)
	code, raw := e.do(t, http.MethodGet, "/api/domains/connected", nil)
	if code != http.StatusOK {
		t.Fatalf("get %d %s", code, raw)
	}
	var list []map[string]string
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) != 2 {
		t.Fatalf("list %s %v", raw, err)
	}
	if list[0]["id"] != "1" || list[0]["host"] != "a.com" || list[0]["connected_at"] != "2026-10-20T10:00:00Z" {
		t.Fatalf("notice %+v", list[0])
	}
	for i := 0; i < 2; i++ {
		code, raw = e.do(t, http.MethodPost, "/api/domains/connected/ack", map[string]any{"ids": []string{"1", "3", "nope"}})
		if code != http.StatusOK || strings.TrimSpace(raw) != `{"acked":1}` {
			t.Fatalf("ack %d %d %s", i, code, raw)
		}
	}
	code, raw = e.do(t, http.MethodGet, "/api/domains/connected", nil)
	if code != http.StatusOK || !strings.Contains(raw, `"id":"4"`) || strings.Contains(raw, `"id":"1"`) {
		t.Fatalf("after ack %d %s", code, raw)
	}
	e.seed(t)
	if code, raw = e.do(t, http.MethodGet, "/api/domains/connected", nil); code != http.StatusOK || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("empty list %d %s", code, raw)
	}
	if rule := routeAccess["GET /api/domains/connected"]; rule.admin || rule.sec != secDomains {
		t.Fatalf("rule %+v", rule)
	}
}
