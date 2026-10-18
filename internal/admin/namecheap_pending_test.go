package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

// fakeClock is a manual clock for the purchase flow; tests move it instead of sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// useClock installs a fake clock on the server and returns it.
// It must be called before the first request.
func (e ncEnv) useClock() *fakeClock {
	clk := &fakeClock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	e.s.ncNow = clk.now
	return clk
}

// setInfoMode changes the getInfo answer of the live fake while the server may be answering.
func (f *fakeNC) setInfoMode(mode string) {
	f.mu.Lock()
	f.infoMode = mode
	f.mu.Unlock()
}

// pendingRow stores a pending purchase row for host, stamped with the server clock, without
// calling Namecheap, and returns it.
func (e ncEnv) pendingRow(t *testing.T, host string) config.Domain {
	t.Helper()
	return e.pendingRowSince(t, host, e.s.nowUTC().Format(time.RFC3339))
}

// pendingRowSince is pendingRow with an explicit registrar_pending_since value.
func (e ncEnv) pendingRowSince(t *testing.T, host, since string) config.Domain {
	t.Helper()
	row, code, msg := e.s.purchaseCatalog().insert(context.Background(), config.Domain{
		Host: host, Role: "links", Disabled: true, Registrar: namecheapRegistrar,
		RegistrarAccount: ncTestName, RegistrarStatus: registrarPending, RegistrarExpected: 11.16,
		RegistrarPendingSince: since,
	}, "")
	if code != http.StatusCreated {
		t.Fatalf("pending row %d %s", code, msg)
	}
	return row
}

// stuckPurchase leaves slow.com pending after a timed-out create, with a fake that kept nothing.
// It returns the environment and the fake clock, which stands at the time the row was written.
func stuckPurchase(t *testing.T) (ncEnv, *fakeClock) {
	t.Helper()
	e := newNCEnv(t, &http.Client{Timeout: 150 * time.Millisecond})
	clk := e.useClock()
	e.configure(t, false)
	e.live.createMode = "hang"
	e.live.createDelay = 400 * time.Millisecond
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("slow.com", 1, 100))
	if code != http.StatusBadGateway || !strings.Contains(raw, `"pending":true`) {
		t.Fatalf("register %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 1 || list[0].RegistrarPendingSince != clk.now().Format(time.RFC3339) {
		t.Fatalf("the pending row must carry the purchase time: %+v", list)
	}
	return e, clk
}

func registerSlow(t *testing.T, e ncEnv) (int, string) {
	t.Helper()
	return e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("slow.com", 1, 100))
}

func TestNamecheapReconcileReleasesUnboughtRow(t *testing.T) {
	e, clk := stuckPurchase(t)

	e.live.setUnavailable("slow.com", true)
	if code, raw := registerSlow(t, e); code != http.StatusConflict || !strings.Contains(raw, `"pending"`) || len(e.domains(t)) != 1 {
		t.Fatalf("taken name must stay pending: %d %s", code, raw)
	}
	e.live.setUnavailable("slow.com", false)

	clk.advance(namecheapPendingHold)
	for _, mode := range []string{"http500", "denied", "noname", "othername"} {
		e.live.setInfoMode(mode)
		if code, raw := registerSlow(t, e); code != http.StatusConflict || !strings.Contains(raw, `"pending"`) || strings.Contains(raw, "too early") || len(e.domains(t)) != 1 {
			t.Fatalf("info %s must not release: %d %s", mode, code, raw)
		}
	}
	e.live.setInfoMode("")

	code, raw := registerSlow(t, e)
	if code != http.StatusConflict || !strings.Contains(raw, "not registered") || !strings.Contains(raw, `"pending":false`) {
		t.Fatalf("release %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 0 {
		t.Fatalf("row must be removed: %+v", list)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create called %d times", n)
	}
}

func TestNamecheapReconcileHoldsRowForFifteenMinutes(t *testing.T) {
	e, clk := stuckPurchase(t)

	code, raw := registerSlow(t, e)
	if code != http.StatusConflict || !strings.Contains(raw, "too early") || !strings.Contains(raw, `"retry_after":900`) || len(e.domains(t)) != 1 {
		t.Fatalf("fresh row: %d %s", code, raw)
	}
	clk.advance(14*time.Minute + 30*time.Second)
	code, raw = registerSlow(t, e)
	if code != http.StatusConflict || !strings.Contains(raw, "too early") || !strings.Contains(raw, `"retry_after":30`) || len(e.domains(t)) != 1 {
		t.Fatalf("almost old: %d %s", code, raw)
	}
	clk.advance(30 * time.Second)
	if code, raw = registerSlow(t, e); code != http.StatusConflict || !strings.Contains(raw, "not registered") || len(e.domains(t)) != 0 {
		t.Fatalf("old row: %d %s", code, raw)
	}
}

func TestNamecheapReconcileConfirmsOwnershipAtOnce(t *testing.T) {
	e, _ := stuckPurchase(t)
	e.live.setOwned("slow.com")
	code, raw := registerSlow(t, e)
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, raw)
	}
	list := e.domains(t)
	if len(list) != 1 || list[0].RegistrarStatus != "" || list[0].RegistrarPendingSince != "" || list[0].Disabled {
		t.Fatalf("a confirmed row must lose its pending time: %+v", list)
	}
}

func TestNamecheapReconcileReleasesWhenAnotherAccountOwnsIt(t *testing.T) {
	e, clk := stuckPurchase(t)
	e.live.setOwned("slow.com")
	e.live.mu.Lock()
	e.live.notOwner["slow.com"] = true
	e.live.mu.Unlock()
	if code, raw := registerSlow(t, e); code != http.StatusConflict || !strings.Contains(raw, "too early") {
		t.Fatalf("before the hold: %d %s", code, raw)
	}
	clk.advance(namecheapPendingHold)
	code, raw := registerSlow(t, e)
	if code != http.StatusConflict || !strings.Contains(raw, "not registered") || len(e.domains(t)) != 0 {
		t.Fatalf("%d %s %+v", code, raw, e.domains(t))
	}
}

func TestNamecheapForceDeleteOfPendingRow(t *testing.T) {
	e := newNCEnv(t, nil)
	clk := e.useClock()
	e.configure(t, false)
	row := e.pendingRow(t, "gone.com")
	path := "/api/domains/" + row.ID + "?force=1"
	del := func() (int, string) { return e.do(t, http.MethodDelete, path, nil) }

	if code, raw := e.do(t, http.MethodDelete, "/api/domains/"+row.ID, nil); code != http.StatusConflict || !strings.Contains(raw, "pending") {
		t.Fatalf("plain delete must stay refused: %d %s", code, raw)
	}

	if code, raw := del(); code != http.StatusConflict || !strings.Contains(raw, "too early") || !strings.Contains(raw, `"retry_after":900`) || len(e.domains(t)) != 1 {
		t.Fatalf("a fresh row: %d %s", code, raw)
	}
	clk.advance(namecheapPendingHold)

	for _, mode := range []string{"http500", "denied"} {
		e.live.setInfoMode(mode)
		if code, raw := del(); code != http.StatusBadGateway || len(e.domains(t)) != 1 {
			t.Fatalf("info %s must keep the row: %d %s", mode, code, raw)
		}
	}
	for _, mode := range []string{"noname", "othername"} {
		e.live.setInfoMode(mode)
		if code, raw := del(); code != http.StatusConflict || !strings.Contains(raw, `"pending"`) || len(e.domains(t)) != 1 {
			t.Fatalf("ambiguous info %s must keep the row: %d %s", mode, code, raw)
		}
	}
	e.live.setInfoMode("")

	e.live.setUnavailable("gone.com", true)
	if code, raw := del(); code != http.StatusConflict || !strings.Contains(raw, `"pending"`) || len(e.domains(t)) != 1 {
		t.Fatalf("a name that is not available must keep the row: %d %s", code, raw)
	}
	e.live.setUnavailable("gone.com", false)

	if !e.s.ncBuy.acquire("gone.com") {
		t.Fatal("acquire")
	}
	if code, raw := del(); code != http.StatusConflict || !strings.Contains(raw, "in progress") || len(e.domains(t)) != 1 {
		t.Fatalf("active purchase: %d %s", code, raw)
	}
	e.s.ncBuy.release("gone.com")

	if code, raw := del(); code != http.StatusOK || !strings.Contains(raw, "deleted") {
		t.Fatalf("not ours: %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 0 {
		t.Fatalf("row must be gone: %+v", list)
	}
	if e.s.ncBuy.has("gone.com") {
		t.Fatal("the host lock must be released")
	}
}

func TestNamecheapForceDeleteDoesNotWaitForRowsWithoutTime(t *testing.T) {
	e := newNCEnv(t, nil)
	e.useClock()
	e.configure(t, false)
	for _, since := range []string{"", "garbage"} {
		row := e.pendingRowSince(t, "old"+strings.ToLower(since)+".com", since)
		if code, raw := e.do(t, http.MethodDelete, "/api/domains/"+row.ID+"?force=1", nil); code != http.StatusOK {
			t.Fatalf("since %q: %d %s", since, code, raw)
		}
	}
	if list := e.domains(t); len(list) != 0 {
		t.Fatalf("rows %+v", list)
	}
}

func TestNamecheapForceDeleteConfirmsOwnedDomain(t *testing.T) {
	e := newNCEnv(t, nil)
	e.useClock()
	e.configure(t, false)
	row := e.pendingRow(t, "mine.com")
	e.live.setOwned("mine.com")
	code, raw := e.do(t, http.MethodDelete, "/api/domains/"+row.ID+"?force=1", nil)
	if code != http.StatusConflict || !strings.Contains(raw, "registered at namecheap") || !strings.Contains(raw, `"confirmed":true`) {
		t.Fatalf("%d %s", code, raw)
	}
	list := e.domains(t)
	if len(list) != 1 || list[0].RegistrarStatus != "" || list[0].RegistrarPendingSince != "" || list[0].Disabled || list[0].Registrar != namecheapRegistrar {
		t.Fatalf("the purchase must be confirmed at once, not deleted: %+v", list)
	}
}

func TestNamecheapForceDeleteScope(t *testing.T) {
	e := newNCEnv(t, nil)
	e.useClock()
	e.configure(t, false)

	other, status, msg := e.s.domainCatalog(prepDomain).insert(context.Background(), config.Domain{Host: "plain.com", Role: "links"}, "")
	if status != http.StatusCreated {
		t.Fatalf("insert %d %s", status, msg)
	}
	if code, raw := e.do(t, http.MethodDelete, "/api/domains/"+other.ID+"?force=1", nil); code != http.StatusOK {
		t.Fatalf("a settled row goes the common way: %d %s", code, raw)
	}
	if n := e.live.count("namecheap.domains.getInfo"); n != 0 {
		t.Fatalf("getInfo called %d times for a settled row", n)
	}

	row := e.pendingRow(t, "quiet.com")
	for name, sess := range map[string]security.Session{
		"unknown user": {UID: "nobody"},
		"api key":      {API: true},
	} {
		r := httptest.NewRequest(http.MethodDelete, "/api/domains/"+row.ID+"?force=1", nil)
		r.SetPathValue("id", row.ID)
		r = r.WithContext(context.WithValue(r.Context(), sessionKey, sess))
		if e.s.forceDeleteDomain(httptest.NewRecorder(), r) {
			t.Fatalf("%s must not be answered", name)
		}
	}
	if n := e.live.count("namecheap.domains.getInfo"); n != 0 {
		t.Fatalf("getInfo called %d times without an administrator", n)
	}
	if code, _ := e.do(t, http.MethodDelete, "/api/domains/missing?force=1", nil); code != http.StatusNotFound {
		t.Fatalf("missing row: %d", code)
	}
}

func TestNamecheapForceDeleteNotConfigured(t *testing.T) {
	e := newNCEnv(t, nil)
	row := e.pendingRow(t, "nocfg.com")
	if code, raw := e.do(t, http.MethodDelete, "/api/domains/"+row.ID+"?force=1", nil); code != http.StatusServiceUnavailable || len(e.domains(t)) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestDomainInsertRefusedWhileHostIsBeingBought(t *testing.T) {
	e := newNCEnv(t, nil)
	post := func(host string) (int, string) {
		return e.do(t, http.MethodPost, "/api/domains", map[string]any{"host": host, "role": "links"})
	}

	if !e.s.ncBuy.acquire("busy.com") {
		t.Fatal("acquire")
	}
	if code, raw := post("Busy.com"); code != http.StatusConflict || !strings.Contains(raw, "registration in progress") {
		t.Fatalf("active purchase: %d %s", code, raw)
	}
	e.s.ncBuy.release("busy.com")

	e.pendingRow(t, "wait.com")
	if code, raw := post("WAIT.com"); code != http.StatusConflict || !strings.Contains(raw, "registration in progress") {
		t.Fatalf("pending row: %d %s", code, raw)
	}
	if code, raw := post("free.com"); code != http.StatusCreated {
		t.Fatalf("a free host: %d %s", code, raw)
	}
	if n := len(e.domains(t)); n != 2 {
		t.Fatalf("rows %d", n)
	}
}

func TestNamecheapBuyRefusesStoredHostBeforeCreate(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	if err := e.s.cfg.Domains.Save(context.Background(), []config.Domain{{ID: "1", Host: "Dup.com", Role: "links"}}); err != nil {
		t.Fatal(err)
	}
	acct := e.s.namecheapAccount()
	w := httptest.NewRecorder()
	p := ncPurchase{s: e.s, ctx: context.Background(), w: w, acct: acct, client: e.s.namecheapClient(acct), domain: "dup.com", maxPrice: 100}
	p.buy(ncRegisterReq{Domain: "dup.com", Years: 1, MaxPrice: 100}, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already exists") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if n := e.live.count("namecheap.domains.create"); n != 0 {
		t.Fatalf("create called %d times", n)
	}
	if list := e.domains(t); len(list) != 1 || list[0].ID != "1" {
		t.Fatalf("rows %+v", list)
	}

	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("DUP.com", 1, 100))
	if code != http.StatusConflict || !strings.Contains(raw, "already exists") || e.live.count("namecheap.domains.create") != 0 {
		t.Fatalf("handler: %d %s", code, raw)
	}
}

func TestDomainByHostPrefersPendingRow(t *testing.T) {
	e := newNCEnv(t, nil)
	rows := []config.Domain{
		{ID: "1", Host: "a.com", Role: "links"},
		{ID: "2", Host: "A.com", Role: "links", RegistrarStatus: registrarPending},
		{ID: "3", Host: "b.com", Role: "links"},
	}
	if err := e.s.cfg.Domains.Save(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	got, found, err := e.s.domainByHost(context.Background(), "a.com")
	if err != nil || !found || got.ID != "2" {
		t.Fatalf("%+v %v %v", got, found, err)
	}
	got, found, _ = e.s.domainByHost(context.Background(), "B.COM")
	if !found || got.ID != "3" {
		t.Fatalf("case-insensitive lookup: %+v %v", got, found)
	}
	if _, found, _ = e.s.domainByHost(context.Background(), "c.com"); found {
		t.Fatal("unknown host found")
	}
}

func TestNotInAccount(t *testing.T) {
	cases := []struct {
		name string
		info ncDomainInfo
		err  error
		want bool
	}{
		{"owned", ncDomainInfo{Owned: true}, nil, false},
		{"named other owner", ncDomainInfo{Foreign: true}, nil, true},
		{"empty or different name", ncDomainInfo{}, nil, false},
		{"not found", ncDomainInfo{}, &ncError{Code: "2019166", Definite: true}, true},
		{"not associated", ncDomainInfo{}, &ncError{Code: "2016166", Definite: true}, true},
		{"other api error", ncDomainInfo{}, &ncError{Code: "1011102", Definite: true}, false},
		{"not definite", ncDomainInfo{}, &ncError{Code: "2019166"}, false},
		{"transport", ncDomainInfo{}, context.DeadlineExceeded, false},
	}
	for _, c := range cases {
		if got := notInAccount(c.info, c.err); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestPendingWait(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) config.Domain {
		return config.Domain{RegistrarPendingSince: now.Add(-ago).Format(time.RFC3339)}
	}
	cases := []struct {
		name string
		row  config.Domain
		want time.Duration
	}{
		{"just written", at(0), namecheapPendingHold},
		{"ten minutes", at(10 * time.Minute), 5 * time.Minute},
		{"exactly the hold", at(namecheapPendingHold), 0},
		{"older", at(time.Hour), 0},
		{"future time keeps the full hold plus", config.Domain{RegistrarPendingSince: now.Add(time.Hour).Format(time.RFC3339)}, namecheapPendingHold + time.Hour},
		{"no time", config.Domain{}, 0},
		{"garbage", config.Domain{RegistrarPendingSince: "yesterday"}, 0},
	}
	for _, c := range cases {
		if got := pendingWait(c.row, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
