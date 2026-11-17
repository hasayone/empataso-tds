package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// registerCF is a register body that asks for Cloudflare.
func registerCF(domain string) map[string]any {
	body := registerBody(domain, 1, 20)
	body["cloudflare"] = true
	return body
}

func TestPurchaseWithCloudflareConnectsTheDomain(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	e.configureCF(t)
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerCF("shop.com"))
	if code != http.StatusCreated || strings.Contains(raw, "cloudflare_error") || strings.Contains(raw, cfTestToken) {
		t.Fatalf("register %d %s", code, raw)
	}
	if !strings.Contains(raw, `"ns_set":true`) || !strings.Contains(raw, `"ns_manual":false`) {
		t.Fatalf("answer has no connection result: %s", raw)
	}
	list := e.domains(t)
	if len(list) != 1 {
		t.Fatalf("domains %+v", list)
	}
	d := list[0]
	if d.CfStatus != cfStatusPending || d.CfZoneID == "" || d.Registrar != namecheapRegistrar || d.RegistrarStatus != "" || d.Disabled || d.WatchSince == "" {
		t.Fatalf("row %+v", d)
	}
	if form := e.live.last("namecheap.domains.dns.setCustom"); form["Nameservers"] != strings.Join(cfTestNS, ",") {
		t.Fatalf("setCustom %+v", form)
	}
	if n := e.live.count("namecheap.domains.dns.setHosts"); n != 0 {
		t.Fatalf("setHosts called %d times for a Cloudflare purchase", n)
	}
}

func TestPurchaseCloudflareFailureKeepsConnectPending(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	e.configureCF(t)
	e.cf.fail["POST /zones"] = "token " + cfTestToken + " is not allowed to add zones"
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerCF("shop.com"))
	if code != http.StatusCreated || !strings.Contains(raw, `"cloudflare_error"`) || strings.Contains(raw, cfTestToken) {
		t.Fatalf("register %d %s", code, raw)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create calls %d", n)
	}
	list := e.domains(t)
	if len(list) != 1 || list[0].CfStatus != cfStatusConnectPending || list[0].RegistrarStatus != "" || list[0].Disabled || list[0].CfZoneID != "" {
		t.Fatalf("rows %+v", list)
	}
}

func TestPurchaseCloudflareIsRefusedBeforeBuying(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerCF("shop.com")); code != http.StatusConflict {
		t.Fatalf("cloudflare not configured: %d %s", code, raw)
	}
	e.configureCF(t)
	both := registerCF("shop.com")
	both["nameservers"] = []string{"a.ns.example.com", "b.ns.example.com"}
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", both); code != http.StatusBadRequest {
		t.Fatalf("cloudflare with nameservers: %d %s", code, raw)
	}
	noIP := newCFEnv(t, func(c *Config) { c.PublicIP = "" })
	noIP.configure(t, false)
	noIP.configureCF(t)
	if code, raw := noIP.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerCF("shop.com")); code != http.StatusConflict || !strings.Contains(raw, "public ip") {
		t.Fatalf("no public ip: %d %s", code, raw)
	}
	if n := e.live.total() + noIP.live.total(); n != 0 {
		t.Fatalf("a refused purchase reached Namecheap %d times", n)
	}
	if len(e.domains(t))+len(noIP.domains(t)) != 0 {
		t.Fatal("a refused purchase left a row")
	}
}

func TestPurchaseSetsApexRecordOnce(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("plain.com", 1, 20))
	if code != http.StatusCreated || strings.Contains(raw, "hosts_error") {
		t.Fatalf("register %d %s", code, raw)
	}
	if n := e.live.count("namecheap.domains.dns.setHosts"); n != 1 {
		t.Fatalf("setHosts calls %d", n)
	}
	form := e.live.last("namecheap.domains.dns.setHosts")
	if form["SLD"] != "plain" || form["TLD"] != "com" || form["HostName1"] != "@" || form["RecordType1"] != "A" || form["Address1"] != cfTestIP || form["TTL1"] != "1800" {
		t.Fatalf("setHosts %+v", form)
	}
	if e.live.count("namecheap.domains.dns.setCustom") != 0 {
		t.Fatal("setCustom was called without name servers")
	}
	// A repeated request for the parked name never touches the DNS again.
	if code, _ := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("plain.com", 1, 20)); code != http.StatusConflict {
		t.Fatalf("second register %d", code)
	}
	if n := e.live.count("namecheap.domains.dns.setHosts"); n != 1 {
		t.Fatalf("setHosts calls after the second request %d", n)
	}
}

func TestPurchaseWithNameserversSetsNoHosts(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	body := registerBody("custom.com", 1, 20)
	body["nameservers"] = []string{"a.ns.example.com", "b.ns.example.com"}
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", body); code != http.StatusCreated {
		t.Fatalf("register %d %s", code, raw)
	}
	if n := e.live.count("namecheap.domains.dns.setHosts"); n != 0 {
		t.Fatalf("setHosts calls %d", n)
	}
	if e.live.count("namecheap.domains.dns.setCustom") != 1 {
		t.Fatal("name servers were not applied")
	}
}

func TestPurchaseWithoutPublicIPSetsNoHosts(t *testing.T) {
	e := newCFEnv(t, func(c *Config) { c.PublicIP = "" })
	e.configure(t, false)
	if code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerBody("plain.com", 1, 20)); code != http.StatusCreated {
		t.Fatalf("register %d %s", code, raw)
	}
	if n := e.live.count("namecheap.domains.dns.setHosts"); n != 0 {
		t.Fatalf("setHosts calls %d", n)
	}
}

func TestReconciledPurchaseKeepsCloudflareIntent(t *testing.T) {
	e := newCFEnv(t, nil)
	e.configure(t, false)
	e.configureCF(t)
	e.live.createMode = "hang"
	code, raw := e.do(t, http.MethodPost, "/api/registrars/namecheap/register", registerCF("late.com"))
	if code != http.StatusBadGateway || !strings.Contains(raw, `"pending":true`) {
		t.Fatalf("register %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 1 || list[0].CfStatus != cfStatusConnectPending || list[0].RegistrarStatus != registrarPending {
		t.Fatalf("pending row %+v", list)
	}
	e.live.setOwned("late.com")
	code, raw = e.do(t, http.MethodPost, "/api/registrars/namecheap/register", map[string]any{"domain": "late.com", "reconcile": true})
	if code != http.StatusCreated || strings.Contains(raw, "cloudflare_error") {
		t.Fatalf("reconcile %d %s", code, raw)
	}
	list := e.domains(t)
	if len(list) != 1 || list[0].CfStatus != cfStatusPending || list[0].CfZoneID == "" || list[0].RegistrarStatus != "" {
		t.Fatalf("row %+v", list)
	}
	if n := e.live.count("namecheap.domains.dns.setHosts"); n != 0 {
		t.Fatalf("setHosts called %d times for a reconciled row", n)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create calls %d", n)
	}
}
func TestMarkedPendingRowKeepsCloudflareStatus(t *testing.T) {
	d := config.Domain{Host: "a.com", CfStatus: cfStatusConnectPending, CfZoneID: "z", ConnectedAt: "x", WatchSince: "y", CfNS: []string{"n"}}
	prepRegistered(&d)
	if d.CfStatus != cfStatusConnectPending || d.CfZoneID != "" || d.ConnectedAt != "" || d.WatchSince != "" || d.CfNS != nil {
		t.Fatalf("prepRegistered %+v", d)
	}
	d = config.Domain{Host: "a.com", CfStatus: cfStatusConnectPending, CfZoneID: "z", ConnectedAt: "x", WatchSince: "y", CfNS: []string{"n"}}
	prepDomain(&d)
	if d.CfStatus != "" || d.CfZoneID != "" || d.ConnectedAt != "" || d.WatchSince != "" || d.CfNS != nil || d.ReasonDetail != "" {
		t.Fatalf("prepDomain %+v", d)
	}
}
