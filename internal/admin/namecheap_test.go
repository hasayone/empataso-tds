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
	ncTestKey  = "nc-secret-key-123"
	ncTestIP   = "203.0.113.7"
	ncTestName = "main-account"
)

// fakeNC is a local stand-in for the Namecheap XML API. Tests never reach a real host.
type fakeNC struct {
	mu        sync.Mutex
	srv       *httptest.Server
	forms     []map[string]string
	badMethod int
	urlKey    int

	unavailable map[string]bool
	premium     map[string]bool
	owned       map[string]bool
	price       string
	// pricingRows replaces the default Price rows of a getPricing answer when it is not empty.
	pricingRows string
	// charged is the ChargedAmount of a successful create; "none" leaves the attribute out.
	charged string
	// notOwner lists domains that getInfo answers with IsOwner false. infoStatus is the
	// getInfo Status; empty means Ok.
	notOwner   map[string]bool
	infoStatus string
	// infoMode changes the getInfo answer: "http500" is a transport-level failure, "denied"
	// is an API error that is not a not-found answer, and "noname" and "othername" are
	// IsOwner=false answers with an empty or a different domain name.
	infoMode string

	createMode  string // ok, refuse, hang, hang-registers
	createDelay time.Duration
	leakKey     bool
}

func newFakeNC(t *testing.T) *fakeNC {
	t.Helper()
	f := &fakeNC{unavailable: map[string]bool{}, premium: map[string]bool{}, owned: map[string]bool{}, notOwner: map[string]bool{}, price: "10.98", charged: "10.16", createMode: "ok"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNC) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		f.mu.Lock()
		f.badMethod++
		f.mu.Unlock()
	}
	if r.URL.RawQuery != "" {
		f.mu.Lock()
		f.urlKey++
		f.mu.Unlock()
	}
	_ = r.ParseForm()
	form := map[string]string{}
	for k := range r.PostForm {
		form[k] = r.PostForm.Get(k)
	}
	f.mu.Lock()
	f.forms = append(f.forms, form)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	cmd := form["Command"]
	if form["ClientIp"] == namecheapDetectIP {
		fmt.Fprint(w, ncFail("1011150", "Invalid request IP: "+ncTestIP))
		return
	}
	domain := strings.ToLower(form["DomainName"])
	switch cmd {
	case "namecheap.domains.check":
		var rows strings.Builder
		for _, d := range strings.Split(form["DomainList"], ",") {
			f.mu.Lock()
			avail, prem := !f.unavailable[d], f.premium[d]
			f.mu.Unlock()
			fmt.Fprintf(&rows, `<DomainCheckResult Domain="%s" Available="%t" ErrorNo="0" Description="" IsPremiumName="%t" PremiumRegistrationPrice="0.0000"/>`, d, avail, prem)
		}
		fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse Type="x">%s</CommandResponse></ApiResponse>`, rows.String())
	case "namecheap.users.getPricing":
		f.mu.Lock()
		rows, price := f.pricingRows, f.price
		f.mu.Unlock()
		if rows == "" {
			for _, years := range []int{1, 2, 3} {
				rows += fmt.Sprintf(`<Price Duration="%d" DurationType="YEAR" Price="%s" RegularPrice="%s" YourPrice="%s" CouponPrice="1.00" AdditionalCost="0.18" Currency="USD"/>`, years, price, price, price)
			}
		}
		fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse><UserGetPricingResult><ProductType Name="domains"><ProductCategory Name="register"><Product Name="%s">%s</Product></ProductCategory></ProductType></UserGetPricingResult></CommandResponse></ApiResponse>`,
			strings.ToLower(form["ProductName"]), rows)
	case "namecheap.domains.create":
		f.create(w, form, domain)
	case "namecheap.domains.getInfo":
		f.mu.Lock()
		owned, foreign, status, mode := f.owned[domain], f.notOwner[domain], f.infoStatus, f.infoMode
		f.mu.Unlock()
		switch mode {
		case "http500":
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case "denied":
			fmt.Fprint(w, ncFail("1011102", "API Key is invalid"))
			return
		case "noname", "othername":
			name := map[string]string{"noname": "", "othername": "other.com"}[mode]
			fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse><DomainGetInfoResult Status="Ok" ID="1" DomainName="%s" IsOwner="false"/></CommandResponse></ApiResponse>`, name)
			return
		}
		if !owned {
			fmt.Fprint(w, ncFail("2019166", "Domain not found"))
			return
		}
		if status == "" {
			status = "Ok"
		}
		fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse><DomainGetInfoResult Status="%s" ID="1" DomainName="%s" OwnerName="u" IsOwner="%t"/></CommandResponse></ApiResponse>`, status, domain, !foreign)
	case "namecheap.domains.dns.setCustom":
		fmt.Fprint(w, `<ApiResponse Status="OK"><CommandResponse><DomainDNSSetCustomResult Domain="x" Update="true"/></CommandResponse></ApiResponse>`)
	default:
		fmt.Fprint(w, ncFail("1", "unknown command"))
	}
}

func (f *fakeNC) create(w http.ResponseWriter, form map[string]string, domain string) {
	if f.createMode == "hang-registers" {
		f.mu.Lock()
		f.owned[domain] = true
		f.mu.Unlock()
	}
	if f.createDelay > 0 {
		time.Sleep(f.createDelay)
	}
	switch f.createMode {
	case "refuse":
		msg := "Insufficient funds"
		if f.leakKey {
			msg = "bad request ApiKey=" + form["ApiKey"]
		}
		fmt.Fprint(w, ncFail("2015610", msg))
	case "hang":
		fmt.Fprint(w, `<ApiResponse Status="OK"/>`)
	case "hang-registers":
		fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse><DomainCreateResult Domain="%s" Registered="true" ChargedAmount="10.16"/></CommandResponse></ApiResponse>`, domain)
	default:
		f.mu.Lock()
		f.owned[domain] = true
		f.mu.Unlock()
		attr := ` ChargedAmount="` + f.charged + `"`
		if f.charged == "none" {
			attr = ""
		}
		fmt.Fprintf(w, `<ApiResponse Status="OK"><CommandResponse><DomainCreateResult Domain="%s" Registered="true"%s OrderID="5"/></CommandResponse></ApiResponse>`, domain, attr)
	}
}

func ncFail(code, msg string) string {
	return fmt.Sprintf(`<ApiResponse Status="ERROR"><Errors><Error Number="%s">%s</Error></Errors></ApiResponse>`, code, msg)
}

func (f *fakeNC) count(command string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, form := range f.forms {
		if form["Command"] == command {
			n++
		}
	}
	return n
}

// total returns how many requests the fake has received.
func (f *fakeNC) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.forms)
}

func (f *fakeNC) last(command string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.forms) - 1; i >= 0; i-- {
		if f.forms[i]["Command"] == command {
			return f.forms[i]
		}
	}
	return nil
}

// setUnavailable marks domain as taken or free while the server may still be answering.
func (f *fakeNC) setUnavailable(domain string, taken bool) {
	f.mu.Lock()
	f.unavailable[domain] = taken
	f.mu.Unlock()
}

func (f *fakeNC) setOwned(domain string) {
	f.mu.Lock()
	f.owned[domain] = true
	f.mu.Unlock()
}

// ncEnv is an admin server wired to a fake Namecheap.
type ncEnv struct {
	s       *Server
	srv     *httptest.Server
	live    *fakeNC
	sandbox *fakeNC
	dir     string
}

// newNCEnv starts an admin server wired to two fake Namecheap endpoints.
func newNCEnv(t *testing.T, httpClient *http.Client) ncEnv {
	t.Helper()
	return newNCEnvSetup(t, httpClient, nil, noopLog{})
}

// newNCEnvSetup is newNCEnv with a hook that runs on the data directory before the server
// starts, and a logger. setup may be nil.
func newNCEnvSetup(t *testing.T, httpClient *http.Client, setup func(dir string), log slogLogger) ncEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	live, sandbox := newFakeNC(t), newFakeNC(t)
	if setup != nil {
		setup(dir)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: &memGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour,
		Domains:       NewJSONStore[config.Domain](filepath.Join(dir, "domains.json")),
		SeqDomains:    NewIDSeq(filepath.Join(dir, "domain_seq.json")),
		APIKeysFile:   filepath.Join(dir, "api-keys.json"),
		UsersFile:     filepath.Join(dir, "users.json"),
		NamecheapFile: filepath.Join(dir, "namecheap.json"),
		NamecheapURL:  live.srv.URL, NamecheapSandboxURL: sandbox.srv.URL,
		NamecheapHTTP: httpClient,
		Log:           log,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return ncEnv{s: s, srv: srv, live: live, sandbox: sandbox, dir: dir}
}

func (e ncEnv) do(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	c := newClient(t)
	code, csrf := login(t, c, e.srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	resp := csrfDo(t, c, method, e.srv.URL+path, csrf, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func ncContactsBody() map[string]string {
	return map[string]string{
		"first_name": "Ann", "last_name": "Lee", "address1": "1 Main St", "city": "Austin",
		"state": "TX", "postal_code": "73301", "country": "us", "phone": "+1.5125550100", "email": "ann@example.com",
	}
}

// configure saves a ready account through the API. sandbox selects the fake endpoint.
func (e ncEnv) configure(t *testing.T, sandbox bool) {
	t.Helper()
	code, raw := e.do(t, http.MethodPut, "/api/registrars/namecheap", map[string]any{
		"name": ncTestName, "api_user": "apiuser", "username": "apiuser", "client_ip": ncTestIP,
		"api_key": ncTestKey, "sandbox": sandbox, "contacts": ncContactsBody(),
	})
	if code != http.StatusOK {
		t.Fatalf("configure %d %s", code, raw)
	}
}

func (e ncEnv) domains(t *testing.T) []config.Domain {
	t.Helper()
	list, err := e.s.cfg.Domains.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func registerBody(domain string, years int, max float64) map[string]any {
	return map[string]any{"domain": domain, "years": years, "max_price": max, "group": "buyers"}
}

func TestNamecheapSettingsHideSecrets(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	code, raw := e.do(t, http.MethodGet, "/api/registrars/namecheap", nil)
	if code != http.StatusOK {
		t.Fatalf("get %d", code)
	}
	for _, leak := range []string{ncTestKey, "ann@example.com", "5125550100", "1 Main St", `"api_key"`, `"contacts"`} {
		if strings.Contains(raw, leak) {
			t.Fatalf("body leaks %q: %s", leak, raw)
		}
	}
	var v ncSettingsView
	if err := json.Unmarshal([]byte(raw), &v); err != nil || !v.Configured || !v.HasKey || !v.HasContacts || v.Name != ncTestName || v.ClientIP != ncTestIP {
		t.Fatalf("view %+v %v", v, err)
	}
}

func TestNamecheapSettingsKeepKeyAndContacts(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	for _, mask := range []string{"", "***", "••••"} {
		code, raw := e.do(t, http.MethodPut, "/api/registrars/namecheap", map[string]any{
			"name": ncTestName, "api_user": "apiuser", "client_ip": ncTestIP, "api_key": mask,
		})
		if code != http.StatusOK {
			t.Fatalf("mask %q: %d %s", mask, code, raw)
		}
		a := e.s.namecheapAccount()
		if a.APIKey != ncTestKey || a.Contacts == nil || a.Contacts.Email != "ann@example.com" || a.Username != "apiuser" {
			t.Fatalf("mask %q changed the account: %+v", mask, a)
		}
	}
	if a := e.s.namecheapAccount(); a.Contacts.Country != "US" {
		t.Fatalf("country not normalized: %q", a.Contacts.Country)
	}
}

func TestNamecheapSettingsFileMode(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	info, err := os.Stat(filepath.Join(e.dir, "namecheap.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	next, err := New(Config{AdminUser: "admin", PasswordHash: e.s.currentHash(), Sessions: security.NewMemoryStore(), Limiter: allowAll{}, NamecheapFile: filepath.Join(e.dir, "namecheap.json")})
	if err != nil {
		t.Fatal(err)
	}
	if a := next.namecheapAccount(); a.APIKey != ncTestKey || a.Name != ncTestName {
		t.Fatalf("restart lost the account: %+v", a)
	}
}

func TestNamecheapSettingsValidation(t *testing.T) {
	e := newNCEnv(t, nil)
	base := func() map[string]any {
		return map[string]any{"name": "n", "api_user": "u", "client_ip": ncTestIP, "api_key": "k", "contacts": ncContactsBody()}
	}
	cases := map[string]func(map[string]any){
		"no name":       func(m map[string]any) { m["name"] = " " },
		"bad user":      func(m map[string]any) { m["api_user"] = "a b" },
		"bad ip":        func(m map[string]any) { m["client_ip"] = "not-an-ip" },
		"no key":        func(m map[string]any) { delete(m, "api_key") },
		"key newline":   func(m map[string]any) { m["api_key"] = "a\nb" },
		"bad phone":     func(m map[string]any) { c := ncContactsBody(); c["phone"] = "5125550100"; m["contacts"] = c },
		"bad email":     func(m map[string]any) { c := ncContactsBody(); c["email"] = "nope"; m["contacts"] = c },
		"bad country":   func(m map[string]any) { c := ncContactsBody(); c["country"] = "USA"; m["contacts"] = c },
		"empty address": func(m map[string]any) { c := ncContactsBody(); c["address1"] = ""; m["contacts"] = c },
	}
	for name, mutate := range cases {
		body := base()
		mutate(body)
		if code, raw := e.do(t, http.MethodPut, "/api/registrars/namecheap", body); code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s", name, code, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "namecheap.json")); err == nil {
		t.Error("a refused PUT wrote the file")
	}
}

func TestNamecheapSettingsBusyDuringRegistration(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	if !e.s.ncBuy.acquire("held.com") {
		t.Fatal("acquire")
	}
	defer e.s.ncBuy.release("held.com")
	if code, _ := e.do(t, http.MethodPut, "/api/registrars/namecheap", map[string]any{"name": "x", "api_user": "u", "client_ip": ncTestIP}); code != http.StatusConflict {
		t.Fatalf("got %d", code)
	}
}

func TestNamecheapDetectIP(t *testing.T) {
	e := newNCEnv(t, nil)
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/test", map[string]any{"api_user": "apiuser", "api_key": ncTestKey, "detect": true})
	if code != http.StatusOK {
		t.Fatalf("test %d %s", code, raw)
	}
	var v ncTestView
	if err := json.Unmarshal([]byte(raw), &v); err != nil || v.OK || v.DetectedIP != ncTestIP {
		t.Fatalf("view %+v %v", v, err)
	}
	if strings.Contains(raw, ncTestKey) {
		t.Fatalf("leaks key: %s", raw)
	}
	if got := e.live.last("namecheap.domains.check")["ClientIp"]; got != namecheapDetectIP {
		t.Fatalf("detect used ClientIp %q", got)
	}
	code, raw = e.do(t, http.MethodPost, "/api/registrars/namecheap/test", map[string]any{"api_user": "apiuser", "api_key": ncTestKey, "client_ip": ncTestIP})
	if err := json.Unmarshal([]byte(raw), &v); code != http.StatusOK || err != nil || !v.OK {
		t.Fatalf("verify %d %s", code, raw)
	}
	if code, _ = e.do(t, http.MethodPost, "/api/registrars/namecheap/test", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("empty body: %d", code)
	}
}

func TestNamecheapRequestShape(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"Alpha.com"}}); code != http.StatusOK {
		t.Fatalf("check %d %s", code, raw)
	}
	e.live.mu.Lock()
	defer e.live.mu.Unlock()
	if e.live.badMethod != 0 || e.live.urlKey != 0 {
		t.Fatalf("requests must be POST forms with an empty query: bad=%d query=%d", e.live.badMethod, e.live.urlKey)
	}
	form := e.live.forms[0]
	if form["ApiKey"] != ncTestKey || form["ApiUser"] != "apiuser" || form["UserName"] != "apiuser" || form["ClientIp"] != ncTestIP {
		t.Fatalf("identity fields: %+v", form)
	}
}

func TestNamecheapCheckPricesAndCache(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.s.ncPrices.now = func() time.Time { return now }
	e.live.premium["rich.com"] = true
	e.live.unavailable["taken.com"] = true

	ask := func() []ncCheckRow {
		code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"free.com", "rich.com", "taken.com", "other.com", "FREE.com"}})
		if code != http.StatusOK {
			t.Fatalf("check %d %s", code, raw)
		}
		var out struct {
			Results []ncCheckRow `json:"results"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out.Results
	}
	rows := ask()
	if len(rows) != 4 {
		t.Fatalf("rows %+v", rows)
	}
	by := map[string]ncCheckRow{}
	for _, r := range rows {
		by[r.Domain] = r
	}
	if p := by["free.com"].Price; p == nil || *p != 11.16 || !by["free.com"].Available || by["free.com"].Currency != "USD" {
		t.Fatalf("free.com %+v", by["free.com"])
	}
	if by["rich.com"].Price != nil || !by["rich.com"].Premium {
		t.Fatalf("premium must not be priced: %+v", by["rich.com"])
	}
	if by["taken.com"].Available || by["taken.com"].Price != nil {
		t.Fatalf("taken.com %+v", by["taken.com"])
	}
	if n := e.live.count("namecheap.users.getPricing"); n != 1 {
		t.Fatalf("pricing calls %d, want one per TLD", n)
	}
	ask()
	if n := e.live.count("namecheap.users.getPricing"); n != 1 {
		t.Fatalf("cache missed: %d", n)
	}
	now = now.Add(2 * time.Hour)
	ask()
	if n := e.live.count("namecheap.users.getPricing"); n != 2 {
		t.Fatalf("cache did not expire: %d", n)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{}}); code != http.StatusBadRequest {
		t.Fatalf("empty list: %d", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"bad_name"}}); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", code)
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("d%d.com", i)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": many}); code != http.StatusBadRequest {
		t.Fatalf("51 names: %d", code)
	}
}

func TestNamecheapSandboxUsesSandboxEndpoint(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, true)
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"a.com"}}); code != http.StatusOK {
		t.Fatalf("check %d %s", code, raw)
	}
	if e.sandbox.count("namecheap.domains.check") != 1 || e.live.count("namecheap.domains.check") != 0 {
		t.Fatal("sandbox flag did not pick the sandbox endpoint")
	}
}

func TestNamecheapDefaultEndpoints(t *testing.T) {
	s := &Server{}
	if got := s.namecheapClient(ncAccount{}).baseURL; got != namecheapLiveURL {
		t.Fatalf("live default %q", got)
	}
	if got := s.namecheapClient(ncAccount{Sandbox: true}).baseURL; got != namecheapSandboxURL {
		t.Fatalf("sandbox default %q", got)
	}
}

func TestNamecheapRegisterSuccess(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("Fresh.com", 2, 22.32))
	if code != http.StatusCreated {
		t.Fatalf("register %d %s", code, raw)
	}
	if strings.Contains(raw, ncTestKey) {
		t.Fatalf("leaks key: %s", raw)
	}
	list := e.domains(t)
	if len(list) != 1 {
		t.Fatalf("domains %+v", list)
	}
	d := list[0]
	if d.Host != "fresh.com" || d.Registrar != "namecheap" || d.RegistrarAccount != ncTestName || d.RegistrarStatus != "" ||
		d.RegistrarCharged != 10.16 || d.RegistrarExpected != 22.32 || d.Disabled || d.Role != "links" || d.Group != "buyers" || d.ID == "" {
		t.Fatalf("row %+v", d)
	}
	if !strings.Contains(raw, `"charged":10.16`) || !strings.Contains(raw, `"expected":22.32`) || strings.Contains(raw, "over_max_price") {
		t.Fatalf("answer %s", raw)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create calls %d", n)
	}
	form := e.live.last("namecheap.domains.create")
	if form["DomainName"] != "fresh.com" || form["Years"] != "2" || form["RegistrantEmailAddress"] != "ann@example.com" ||
		form["AuxBillingCountry"] != "US" || form["AddFreeWhoisguard"] != "yes" {
		t.Fatalf("create form %+v", form)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("fresh.com", 1, 50)); code != http.StatusConflict {
		t.Fatalf("second registration of a parked name: %d", code)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("second request bought again: %d", n)
	}
}

func TestNamecheapRegisterAppliesNameservers(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	body := registerBody("withns.com", 1, 11.16)
	body["nameservers"] = []string{"Ann.NS.Cloudflare.com.", "bob.ns.cloudflare.com"}
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", body)
	if code != http.StatusCreated || strings.Contains(raw, "ns_error") {
		t.Fatalf("register %d %s", code, raw)
	}
	form := e.live.last("namecheap.domains.dns.setCustom")
	if form["SLD"] != "withns" || form["TLD"] != "com" || form["Nameservers"] != "ann.ns.cloudflare.com,bob.ns.cloudflare.com" {
		t.Fatalf("setCustom %+v", form)
	}
}

func TestNamecheapRegisterRefusals(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	e.live.premium["rich.com"] = true
	e.live.unavailable["taken.com"] = true
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"price above max", registerBody("cheap.com", 1, 5), http.StatusConflict},
		{"premium", registerBody("rich.com", 1, 9999), http.StatusUnprocessableEntity},
		{"taken", registerBody("taken.com", 1, 100), http.StatusConflict},
		{"years zero", registerBody("a.com", 0, 100), http.StatusBadRequest},
		{"years eleven", registerBody("a.com", 11, 100), http.StatusBadRequest},
		{"no max price", registerBody("a.com", 1, 0), http.StatusBadRequest},
		{"bad domain", registerBody("not a domain", 1, 100), http.StatusBadRequest},
		{"one nameserver", map[string]any{"domain": "a.com", "years": 1, "max_price": 100, "nameservers": []string{"ns1.example.com"}}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", c.body); code != c.want {
			t.Errorf("%s: got %d %s", c.name, code, raw)
		}
	}
	if n := e.live.count("namecheap.domains.create"); n != 0 {
		t.Fatalf("a refused request bought a domain: %d", n)
	}
	if list := e.domains(t); len(list) != 0 {
		t.Fatalf("a refused request left rows: %+v", list)
	}
	_, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("cheap.com", 1, 5))
	if !strings.Contains(raw, `"price":11.16`) {
		t.Fatalf("price changed answer must carry the price: %s", raw)
	}
}

func TestNamecheapRegisterNotConfigured(t *testing.T) {
	e := newNCEnv(t, nil)
	if code, _ := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("a.com", 1, 100)); code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"a.com"}}); code != http.StatusServiceUnavailable {
		t.Fatalf("check got %d", code)
	}
	if e.live.total() != 0 {
		t.Fatal("an unconfigured server called Namecheap")
	}
}

func TestNamecheapDefiniteRefusalRemovesPendingRow(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	e.live.createMode = "refuse"
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("broke.com", 1, 100))
	if code != http.StatusBadGateway || !strings.Contains(raw, "Insufficient funds") || strings.Contains(raw, `"pending"`) {
		t.Fatalf("refusal %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 0 {
		t.Fatalf("pending row stayed: %+v", list)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create calls %d", n)
	}
}

func TestNamecheapErrorHidesKeyAndURL(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	e.live.createMode = "refuse"
	e.live.leakKey = true
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("leak.com", 1, 100))
	if code != http.StatusBadGateway || strings.Contains(raw, ncTestKey) || !strings.Contains(raw, "***") {
		t.Fatalf("key not masked: %d %s", code, raw)
	}
	e.live.srv.Close()
	code, raw = e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"a.com"}})
	if code != http.StatusBadGateway || strings.Contains(raw, "http://") || strings.Contains(raw, "127.0.0.1") || strings.Contains(raw, ncTestKey) {
		t.Fatalf("transport error leaks: %d %s", code, raw)
	}
}

func TestNamecheapTimeoutNeverBuysTwice(t *testing.T) {
	e := newNCEnv(t, &http.Client{Timeout: 150 * time.Millisecond})
	e.configure(t, false)
	e.live.createMode = "hang"
	e.live.createDelay = 600 * time.Millisecond

	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("slow.com", 1, 100))
	if code != http.StatusBadGateway || !strings.Contains(raw, `"pending":true`) {
		t.Fatalf("timeout answer %d %s", code, raw)
	}
	list := e.domains(t)
	if len(list) != 1 || list[0].RegistrarStatus != "pending" || list[0].Host != "slow.com" || !list[0].Disabled || list[0].RegistrarExpected != 11.16 {
		t.Fatalf("row must stay pending and disabled: %+v", list)
	}
	e.live.setUnavailable("slow.com", true)
	code, raw = e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("slow.com", 1, 100))
	if code != http.StatusConflict || !strings.Contains(raw, "pending") {
		t.Fatalf("repeat while unknown: %d %s", code, raw)
	}
	e.live.setOwned("slow.com")
	code, raw = e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("slow.com", 1, 100))
	if code != http.StatusCreated {
		t.Fatalf("reconcile %d %s", code, raw)
	}
	if list = e.domains(t); len(list) != 1 || list[0].RegistrarStatus != "" || list[0].Registrar != "namecheap" || list[0].Disabled {
		t.Fatalf("row after reconcile: %+v", list)
	}
	if list[0].RegistrarCharged != 0 || list[0].RegistrarExpected != 11.16 {
		t.Fatalf("a reconciled purchase has no charged amount, only the expected one: %+v", list[0])
	}
	if !strings.Contains(raw, `"charged":0`) || !strings.Contains(raw, `"expected":11.16`) || !strings.Contains(raw, `"namecheap_status":"Ok"`) {
		t.Fatalf("reconcile answer %s", raw)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create called %d times", n)
	}
}

func TestNamecheapTimeoutAfterSuccessIsReconciled(t *testing.T) {
	e := newNCEnv(t, &http.Client{Timeout: 150 * time.Millisecond})
	e.configure(t, false)
	e.live.createMode = "hang-registers"
	e.live.createDelay = 500 * time.Millisecond
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("late.com", 1, 100))
	if code != http.StatusCreated {
		t.Fatalf("got %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 1 || list[0].RegistrarStatus != "" || list[0].Disabled || list[0].RegistrarCharged != 0 {
		t.Fatalf("rows %+v", list)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create called %d times", n)
	}
}

func TestNamecheapParallelRegistrationBuysOnce(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	e.live.createDelay = 300 * time.Millisecond
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("race.com", 1, 100))
		}(i)
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			created++
		} else if c != http.StatusConflict {
			t.Errorf("unexpected status %d in %v", c, codes)
		}
	}
	if created != 1 || e.live.count("namecheap.domains.create") != 1 {
		t.Fatalf("statuses %v, create calls %d", codes, e.live.count("namecheap.domains.create"))
	}
	if list := e.domains(t); len(list) != 1 {
		t.Fatalf("rows %+v", list)
	}
}

func TestNamecheapForbiddenForUserAndAPIKey(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	if _, err := e.s.createUser(userDraft{
		Login: "buyer", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: sectionStrings(userSections), Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}
	e.s.keyMu.Lock()
	rawKey, _, err := e.s.keys.Generate("t", []string{security.ScopeAdmin})
	e.s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	type call struct{ method, path string }
	all := []call{
		{"GET", "/api/registrars/namecheap"}, {"PUT", "/api/registrars/namecheap"},
		{"POST", "/api/registrars/namecheap/test"}, {"POST", "/api/registrars/namecheap/check"},
		{"POST", "/api/registrars/namecheap/register"}, {"POST", "/api/domains/1/ns"},
	}
	c := newClient(t)
	_, csrf := login(t, c, e.srv.URL, "buyer", "secret1")
	for _, k := range all {
		resp := csrfDo(t, c, k.method, e.srv.URL+k.path, csrf, map[string]any{})
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("USER %s %s: got %d", k.method, k.path, resp.StatusCode)
		}
	}
	forKey := []call{
		{"GET", "/api/registrars/namecheap"}, {"PUT", "/api/registrars/namecheap"},
		{"POST", "/api/registrars/namecheap/test"}, {"POST", "/api/registrars/namecheap/register"}, {"POST", "/api/domains/1/ns"},
	}
	for _, k := range forKey {
		req, _ := http.NewRequest(k.method, e.srv.URL+k.path, strings.NewReader(`{}`))
		req.Header.Set("Api-Key", rawKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Api-Key %s %s: got %d", k.method, k.path, resp.StatusCode)
		}
	}
	if e.live.total() != 0 {
		t.Fatal("a forbidden request reached Namecheap")
	}
}

func TestNamecheapNameserverChange(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("mine.com", 1, 100)); code != http.StatusCreated {
		t.Fatalf("register %d %s", code, raw)
	}
	mine := e.domains(t)[0]
	other, code, msg := e.s.domainCatalog(prepDomain).insert(context.Background(), config.Domain{Host: "manual.com", Role: "links"}, "")
	if code != http.StatusCreated {
		t.Fatalf("insert %d %s", code, msg)
	}
	foreign, code, msg := e.s.domainCatalog(prepRegistered).insert(context.Background(), config.Domain{Host: "foreign.com", Role: "links", Registrar: "namecheap", RegistrarAccount: "someone-else"}, "")
	if code != http.StatusCreated {
		t.Fatalf("insert %d %s", code, msg)
	}
	pending, _, _ := e.s.domainCatalog(prepRegistered).insert(context.Background(), config.Domain{Host: "wait.com", Role: "links", Registrar: "namecheap", RegistrarAccount: ncTestName, RegistrarStatus: "pending"}, "")
	ghost, _, _ := e.s.domainCatalog(prepRegistered).insert(context.Background(), config.Domain{Host: "ghost.com", Role: "links", Registrar: "namecheap", RegistrarAccount: ncTestName}, "")

	nsBody := map[string]any{"nameservers": []string{"a.ns.cloudflare.com", "b.ns.cloudflare.com"}, "confirm": true}
	cases := []struct {
		name string
		id   string
		body map[string]any
		want int
	}{
		{"ok", mine.ID, nsBody, http.StatusOK},
		{"no confirm", mine.ID, map[string]any{"nameservers": nsBody["nameservers"]}, http.StatusBadRequest},
		{"one server", mine.ID, map[string]any{"nameservers": []string{"a.ns.cloudflare.com"}, "confirm": true}, http.StatusBadRequest},
		{"bad server", mine.ID, map[string]any{"nameservers": []string{"a.ns.cloudflare.com", "bad host"}, "confirm": true}, http.StatusBadRequest},
		{"unknown id", "9999", nsBody, http.StatusNotFound},
		{"manual domain", other.ID, nsBody, http.StatusForbidden},
		{"other account", foreign.ID, nsBody, http.StatusForbidden},
		{"pending", pending.ID, nsBody, http.StatusConflict},
		{"unknown at Namecheap", ghost.ID, nsBody, http.StatusBadGateway},
	}
	for _, c := range cases {
		if code, raw := e.do(t, http.MethodPost, "/api/domains/"+c.id+"/ns", c.body); code != c.want {
			t.Errorf("%s: got %d %s", c.name, code, raw)
		}
	}
	if n := e.live.count("namecheap.domains.dns.setCustom"); n != 1 {
		t.Fatalf("setCustom calls %d, want only the accepted one", n)
	}
	form := e.live.last("namecheap.domains.dns.setCustom")
	if form["SLD"] != "mine" || form["TLD"] != "com" || form["Nameservers"] != "a.ns.cloudflare.com,b.ns.cloudflare.com" {
		t.Fatalf("setCustom %+v", form)
	}
}

func TestDomainRegistrarFieldsAreServerOwned(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	code, raw := e.do(t, http.MethodPost, "/api/domains", map[string]any{
		"host": "fake.com", "role": "links", "registrar": "namecheap", "registrar_account": ncTestName, "registrar_charged": 5, "registrar_expected": 5,
		"registrar_status": "pending", "registrar_pending_since": "2020-01-01T00:00:00Z",
	})
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	if strings.Contains(raw, "registrar") {
		t.Fatalf("client set registrar fields: %s", raw)
	}
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("real.com", 1, 100)); code != http.StatusCreated {
		t.Fatalf("register %d %s", code, raw)
	}
	var real config.Domain
	for _, d := range e.domains(t) {
		if d.Host == "real.com" {
			real = d
		}
	}
	code, raw = e.do(t, http.MethodPut, "/api/domains/"+real.ID, map[string]any{"host": "real.com", "role": "links", "group": "moved", "registrar": "none"})
	if code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	for _, d := range e.domains(t) {
		if d.Host == "real.com" && (d.Registrar != "namecheap" || d.RegistrarAccount != ncTestName || d.RegistrarCharged != 10.16 || d.RegistrarExpected != 11.16 || d.Group != "moved") {
			t.Fatalf("PUT lost the registrar fields: %+v", d)
		}
	}
	code, _ = e.do(t, http.MethodPut, "/api/domains/"+real.ID, map[string]any{"host": "renamed.com", "role": "links"})
	if code != http.StatusOK {
		t.Fatalf("rename %d", code)
	}
	for _, d := range e.domains(t) {
		if d.ID == real.ID && d.Registrar != "" {
			t.Fatalf("a renamed host kept the registrar: %+v", d)
		}
	}
}

func TestKeepLocalFieldsKeepsRegistrar(t *testing.T) {
	old := config.Domain{ID: "1", Host: "a.com", Registrar: "namecheap", RegistrarAccount: "acc", RegistrarStatus: "pending", RegistrarCharged: 9.5, RegistrarExpected: 11.16, RegistrarPendingSince: "2026-10-18T10:00:00Z"}
	got := keepLocalFields(old, config.Domain{ID: "1", Host: "a.com", Role: "links"})
	if got.Registrar != "namecheap" || got.RegistrarAccount != "acc" || got.RegistrarStatus != "pending" || got.RegistrarCharged != 9.5 || got.RegistrarExpected != 11.16 ||
		got.RegistrarPendingSince != "2026-10-18T10:00:00Z" {
		t.Fatalf("import overwrote the registrar fields: %+v", got)
	}
}

func TestNamecheapHelpers(t *testing.T) {
	if got := detectOutboundIP(&ncError{rawMessage: "Invalid request IP: 198.51.100.4."}); got != "198.51.100.4" {
		t.Errorf("ip %q", got)
	}
	if got := detectOutboundIP(&ncError{rawMessage: "Invalid request IP: 2001:db8::1"}); got != "2001:db8::1" {
		t.Errorf("ipv6 %q", got)
	}
	if got := detectOutboundIP(&ncError{rawMessage: "API Key is invalid"}); got != "" {
		t.Errorf("not an ip error: %q", got)
	}
	if got := detectOutboundIP(fmt.Errorf("plain")); got != "" {
		t.Errorf("plain error: %q", got)
	}
	for _, ok := range []string{"a.com", "my-site.co.uk", "xn--80ak6aa92e.com"} {
		if !validNCDomain(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"com", "-a.com", "a-.com", "a..com", "a b.com", "A.com", "http://a.com", "a.com/x", strings.Repeat("a", 64) + ".com"} {
		if validNCDomain(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if s, tld, err := splitNCDomain("My.Co.UK"); err != nil || s != "my" || tld != "co.uk" {
		t.Errorf("split %q %q %v", s, tld, err)
	}
	if _, err := normalizeNameservers([]string{"one.example.com"}, 0); err == nil {
		t.Error("a single server accepted")
	}
	if out, err := normalizeNameservers(nil, 0); err != nil || len(out) != 0 {
		t.Errorf("empty list: %v %v", out, err)
	}
	if _, err := normalizeNameservers(nil, 2); err == nil {
		t.Error("empty list accepted when two are required")
	}
	if got := roundCents(10.156); got != 10.16 {
		t.Errorf("round %v", got)
	}
}

// termPriceRows is a getPricing answer that exercises the choice of the price row:
// a coupon price that must be ignored, the larger of YourPrice and Price winning either way, a month row,
// an absolute row, a row with Price only and a row in another currency.
const termPriceRows = `<Price Duration="1" DurationType="YEAR" YourPrice="10.00" Price="12.00" CouponPrice="1.00" AdditionalCost="0.20" Currency="USD"/>` +
	`<Price Duration="2" DurationType="YEAR" YourPrice="9.00" Price="11.00" CouponPrice="1.00" AdditionalCost="0.20" Currency="USD"/>` +
	`<Price Duration="3" DurationType="MONTH" YourPrice="1.00"/>` +
	`<Price Duration="4" DurationType="YEAR" PricingType="ABSOLUTE" YourPrice="30.00" AdditionalCost="0.40"/>` +
	`<Price Duration="5" DurationType="YEAR" Price="5.00"/>` +
	`<Price Duration="6" DurationType="YEAR" YourPrice="3.00" Currency="EUR"/>` +
	`<Price Duration="8" DurationType="YEAR" YourPrice="2.00" Price="1.50"/>`

func TestNamecheapPurchaseUsesTermPrice(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	e.live.pricingRows = termPriceRows
	cases := []struct {
		domain   string
		years    int
		max      float64
		want     int
		contains string
	}{
		{"two.com", 2, 22.40, http.StatusCreated, `"expected":22.4`},
		{"dear.com", 1, 12.19, http.StatusConflict, `"price":12.2`},
		{"high.com", 8, 16, http.StatusCreated, `"expected":16`},
		{"month.com", 3, 1000, http.StatusConflict, "price unknown"},
		{"abs.com", 4, 30.40, http.StatusCreated, `"expected":30.4`},
		{"plain.com", 5, 25, http.StatusCreated, `"expected":25`},
		{"euro.com", 6, 1000, http.StatusConflict, "price unknown"},
		{"none.com", 7, 1000, http.StatusConflict, "price unknown"},
	}
	created := 0
	for _, c := range cases {
		code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody(c.domain, c.years, c.max))
		if code != c.want || !strings.Contains(raw, c.contains) {
			t.Errorf("%s: got %d %s", c.domain, code, raw)
		}
		if code == http.StatusCreated {
			created++
		}
		if n := e.live.count("namecheap.domains.create"); n != created {
			t.Fatalf("%s: %d create calls after %d purchases", c.domain, n, created)
		}
	}
	if list := e.domains(t); len(list) != created {
		t.Fatalf("rows %+v", list)
	}
}

func TestNamecheapPurchaseBypassesPriceCache(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"warm.com"}}); code != http.StatusOK {
		t.Fatalf("check %d %s", code, raw)
	}
	e.live.mu.Lock()
	e.live.price = "20.00"
	e.live.mu.Unlock()
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("warm.com", 1, 11.16))
	if code != http.StatusConflict || !strings.Contains(raw, `"price":20.18`) {
		t.Fatalf("a stale cached price was trusted: %d %s", code, raw)
	}
	if n := e.live.count("namecheap.users.getPricing"); n != 2 {
		t.Fatalf("pricing calls %d, want a fresh one for the purchase", n)
	}
	if e.live.count("namecheap.domains.create") != 0 {
		t.Fatal("bought at a changed price")
	}
}

func TestNamecheapChargedAboveMaxIsReported(t *testing.T) {
	log := &warnLog{}
	e := newNCEnvSetup(t, nil, nil, log)
	e.configure(t, false)
	e.live.charged = "50.00"
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("pricey.com", 1, 11.16))
	if code != http.StatusCreated || !strings.Contains(raw, `"over_max_price":true`) || !strings.Contains(raw, `"charged":50`) {
		t.Fatalf("got %d %s", code, raw)
	}
	if text := log.joined(); !strings.Contains(text, "max_price") || strings.Contains(text, ncTestKey) {
		t.Fatalf("warning %q", text)
	}
	if d := e.domains(t)[0]; d.RegistrarCharged != 50 {
		t.Fatalf("row %+v", d)
	}
}

func TestNamecheapUnreportedChargeIsNotZeroed(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	e.live.charged = "none"
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("quiet.com", 1, 11.16))
	if code != http.StatusCreated || !strings.Contains(raw, `"charged":0`) || !strings.Contains(raw, `"expected":11.16`) || strings.Contains(raw, "over_max_price") {
		t.Fatalf("got %d %s", code, raw)
	}
	if d := e.domains(t)[0]; d.RegistrarCharged != 0 || d.RegistrarExpected != 11.16 || d.Disabled {
		t.Fatalf("row %+v", d)
	}
}

func TestNamecheapPendingRowIsProtected(t *testing.T) {
	e := newNCEnv(t, &http.Client{Timeout: 150 * time.Millisecond})
	e.configure(t, false)
	e.live.createMode = "hang"
	e.live.createDelay = 400 * time.Millisecond
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("slow.com", 1, 100)); code != http.StatusBadGateway || !strings.Contains(raw, `"pending":true`) {
		t.Fatalf("register %d %s", code, raw)
	}
	row := e.domains(t)[0]
	path := "/api/domains/" + row.ID

	code, raw := e.do(t, http.MethodPut, path, map[string]any{"host": "slow.com", "role": "links", "group": "moved", "disabled": false})
	if code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	got := e.domains(t)[0]
	if got.RegistrarPendingSince != row.RegistrarPendingSince || row.RegistrarPendingSince == "" {
		t.Fatalf("a PUT lost the pending time: %q then %q", row.RegistrarPendingSince, got.RegistrarPendingSince)
	}
	if got.Group != "moved" || !got.Disabled || got.RegistrarStatus != "pending" || got.RegistrarExpected != 11.16 ||
		got.Registrar != "namecheap" || got.RegistrarAccount != ncTestName {
		t.Fatalf("a PUT changed a pending row: %+v", got)
	}
	if code, raw = e.do(t, http.MethodPut, path, map[string]any{"host": "renamed.com", "role": "links"}); code != http.StatusConflict || !strings.Contains(raw, "pending") {
		t.Fatalf("rename of a pending row: %d %s", code, raw)
	}
	if code, raw = e.do(t, http.MethodDelete, path, nil); code != http.StatusConflict || !strings.Contains(raw, "pending") {
		t.Fatalf("delete of a pending row: %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 1 || list[0].Host != "slow.com" {
		t.Fatalf("rows %+v", list)
	}

	e.live.setOwned("slow.com")
	if code, raw = e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("slow.com", 1, 100)); code != http.StatusCreated {
		t.Fatalf("reconcile %d %s", code, raw)
	}
	if d := e.domains(t)[0]; d.Disabled || d.RegistrarStatus != "" {
		t.Fatalf("a confirmed purchase stays hidden: %+v", d)
	}

	if !e.s.ncBuy.acquire("slow.com") {
		t.Fatal("acquire")
	}
	if code, raw = e.do(t, http.MethodDelete, path, nil); code != http.StatusConflict {
		t.Fatalf("delete during a purchase: %d %s", code, raw)
	}
	e.s.ncBuy.release("slow.com")

	other, status, msg := e.s.domainCatalog(prepDomain).insert(context.Background(), config.Domain{Host: "manual.com", Role: "links"}, "")
	if status != http.StatusCreated {
		t.Fatalf("insert %d %s", status, msg)
	}
	if !e.s.ncBuy.acquire("held.com") {
		t.Fatal("acquire")
	}
	if code, raw = e.do(t, http.MethodPut, "/api/domains/"+other.ID, map[string]any{"host": "held.com", "role": "links"}); code != http.StatusConflict {
		t.Fatalf("rename onto a host being bought: %d %s", code, raw)
	}
	e.s.ncBuy.release("held.com")
	if code, raw = e.do(t, http.MethodPut, "/api/domains/"+other.ID, map[string]any{"host": "held.com", "role": "links"}); code != http.StatusOK {
		t.Fatalf("rename after the purchase: %d %s", code, raw)
	}
	if code, raw = e.do(t, http.MethodDelete, path, nil); code != http.StatusOK {
		t.Fatalf("delete of a confirmed row: %d %s", code, raw)
	}
}

func TestNamecheapInfoDecidesOwnerByIsOwnerAndName(t *testing.T) {
	f := newFakeNC(t)
	c := newNCClient(f.srv.URL, ncCreds{APIUser: "u", APIKey: ncTestKey, Username: "u", ClientIP: ncTestIP}, nil)
	ask := func() ncDomainInfo {
		t.Helper()
		got, err := c.info(context.Background(), "mine.com")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	f.setOwned("mine.com")
	if got := ask(); !got.Owned || got.Status != "Ok" {
		t.Fatalf("owner: %+v", got)
	}
	f.mu.Lock()
	f.infoStatus = "Locked"
	f.mu.Unlock()
	if got := ask(); !got.Owned || got.Status != "Locked" {
		t.Fatalf("a locked domain of the account is still owned: %+v", got)
	}
	f.mu.Lock()
	f.notOwner["mine.com"] = true
	f.mu.Unlock()
	if got := ask(); got.Owned || !got.Foreign || got.Status != "Locked" {
		t.Fatalf("IsOwner false must not count and must be foreign: %+v", got)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<ApiResponse Status="OK"><CommandResponse><DomainGetInfoResult Status="Ok" DomainName="other.com" IsOwner="true"/></CommandResponse></ApiResponse>`)
	}))
	defer other.Close()
	mismatch := newNCClient(other.URL, ncCreds{APIUser: "u", APIKey: ncTestKey}, nil)
	if got, err := mismatch.info(context.Background(), "mine.com"); err != nil || got.Owned || got.Foreign {
		t.Fatalf("another domain name: %+v %v", got, err)
	}
	unnamed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<ApiResponse Status="OK"><CommandResponse><DomainGetInfoResult Status="Ok" IsOwner="false"/></CommandResponse></ApiResponse>`)
	}))
	defer unnamed.Close()
	empty := newNCClient(unnamed.URL, ncCreds{APIUser: "u", APIKey: ncTestKey}, nil)
	if got, err := empty.info(context.Background(), "mine.com"); err != nil || got.Owned || got.Foreign {
		t.Fatalf("an empty domain name is ambiguous: %+v %v", got, err)
	}
	cased := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<ApiResponse Status="OK"><CommandResponse><DomainGetInfoResult Status="Ok" DomainName="MINE.com" IsOwner="FALSE"/></CommandResponse></ApiResponse>`)
	}))
	defer cased.Close()
	upper := newNCClient(cased.URL, ncCreds{APIUser: "u", APIKey: ncTestKey}, nil)
	if got, err := upper.info(context.Background(), "mine.com"); err != nil || got.Owned || !got.Foreign {
		t.Fatalf("name and flag ignore case: %+v %v", got, err)
	}
}

func TestNamecheapSettleOutlivesRequestDeadline(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	e.live.createMode = "hang-registers"
	e.live.createDelay = 700 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	acct := e.s.namecheapAccount()
	rec := httptest.NewRecorder()
	p := ncPurchase{s: e.s, ctx: ctx, w: rec, acct: acct, client: e.s.namecheapClient(acct), domain: "late.com", maxPrice: 100}
	p.buy(ncRegisterReq{Domain: "late.com", Years: 1, MaxPrice: 100, Group: "g"}, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("the ownership check ran out of time with the request: %d %s", rec.Code, rec.Body.String())
	}
	if list := e.domains(t); len(list) != 1 || list[0].RegistrarStatus != "" || list[0].Disabled {
		t.Fatalf("rows %+v", list)
	}
}

func TestNamecheapBuyLockHoldsSettingsWrite(t *testing.T) {
	var b ncBuyState
	if !b.acquire("a.com") {
		t.Fatal("acquire")
	}
	ran := false
	if b.whenIdle(func() { ran = true }) || ran {
		t.Fatal("settings were written during a purchase")
	}
	b.release("a.com")

	inWrite := make(chan struct{})
	wrote := false
	done := make(chan bool)
	go func() {
		done <- b.whenIdle(func() {
			close(inWrite)
			time.Sleep(100 * time.Millisecond)
			wrote = true
		})
	}()
	<-inWrite
	if !b.acquire("b.com") {
		t.Fatal("acquire after the write")
	}
	if !wrote {
		t.Fatal("a purchase started while the settings were being written")
	}
	if !<-done {
		t.Fatal("whenIdle refused an idle state")
	}
}

func TestNamecheapDamagedFileDisablesRegistrar(t *testing.T) {
	cases := map[string]string{
		"not json":     "{broken",
		"bad fields":   `{"name":"n","api_user":"u","username":"u","client_ip":"not-an-ip","api_key":"leaky-secret-key"}`,
		"no key":       `{"name":"n","api_user":"u","username":"u","client_ip":"203.0.113.7","api_key":""}`,
		"bad contacts": `{"name":"n","api_user":"u","username":"u","client_ip":"203.0.113.7","api_key":"leaky-secret-key","contacts":{"first_name":"A"}}`,
	}
	for name, body := range cases {
		log := &warnLog{}
		e := newNCEnvSetup(t, nil, func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "namecheap.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}, log)
		code, raw := e.do(t, http.MethodGet, "/api/registrars/namecheap", nil)
		var v ncSettingsView
		if err := json.Unmarshal([]byte(raw), &v); code != http.StatusOK || err != nil || v.Error == "" || v.Configured || v.HasKey {
			t.Fatalf("%s: get %d %s", name, code, raw)
		}
		if strings.Contains(raw, "leaky-secret-key") || strings.Contains(log.joined(), "leaky-secret-key") || log.joined() == "" {
			t.Fatalf("%s: secret or missing log: %s | %s", name, raw, log.joined())
		}
		if code, _ = e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("a.com", 1, 100)); code != http.StatusServiceUnavailable {
			t.Fatalf("%s: register %d", name, code)
		}
		if code, _ = e.do(t, http.MethodPost, "/api/registrars/namecheap/check", map[string]any{"domains": []string{"a.com"}}); code != http.StatusServiceUnavailable {
			t.Fatalf("%s: check %d", name, code)
		}
		if e.live.total() != 0 {
			t.Fatalf("%s: a disabled registrar called Namecheap", name)
		}
		e.configure(t, false)
		_, raw = e.do(t, http.MethodGet, "/api/registrars/namecheap", nil)
		if strings.Contains(raw, `"error"`) || !strings.Contains(raw, `"configured":true`) {
			t.Fatalf("%s: a PUT did not repair the file: %s", name, raw)
		}
		if code, raw = e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("fix.com", 1, 100)); code != http.StatusCreated {
			t.Fatalf("%s: register after repair %d %s", name, code, raw)
		}
	}
}
