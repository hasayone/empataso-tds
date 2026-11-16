package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// reconcileBody is a register request that only settles a pending row.
func reconcileBody(domain string) map[string]any {
	return map[string]any{"domain": domain, "reconcile": true}
}

// TestNamecheapReconcileNeverBuys checks that a request with reconcile set never reaches the
// purchase path: an unknown domain gives 404, a row that is not pending gives 409, and in both
// cases Namecheap is not called and nothing is written.
func TestNamecheapReconcileNeverBuys(t *testing.T) {
	e := newNCEnv(t, nil)
	e.configure(t, false)
	const path = "/api/registrars/namecheap/register"

	code, raw := e.do(t, http.MethodPost, path, reconcileBody("ghost.com"))
	if code != http.StatusNotFound {
		t.Fatalf("unknown domain: %d %s", code, raw)
	}
	if _, status, msg := e.s.domainCatalog(prepDomain).insert(context.Background(), config.Domain{Host: "manual.com", Role: "links"}, ""); status != http.StatusCreated {
		t.Fatalf("insert %d %s", status, msg)
	}
	code, raw = e.do(t, http.MethodPost, path, reconcileBody("manual.com"))
	if code != http.StatusConflict || !strings.Contains(raw, "not pending") {
		t.Fatalf("row that is not pending: %d %s", code, raw)
	}
	if n := e.live.count("namecheap.domains.create"); n != 0 {
		t.Fatalf("reconcile bought a domain: %d", n)
	}
	if n := e.live.total(); n != 0 {
		t.Fatalf("reconcile called Namecheap without a pending row: %d requests", n)
	}
	if list := e.domains(t); len(list) != 1 || list[0].Host != "manual.com" {
		t.Fatalf("rows changed: %+v", list)
	}
}

// TestNamecheapReconcileSettlesPending checks that a pending row is confirmed by a reconcile
// request without years or a price, and that the purchase is not repeated.
func TestNamecheapReconcileSettlesPending(t *testing.T) {
	e := newNCEnv(t, &http.Client{Timeout: 150 * time.Millisecond})
	e.configure(t, false)
	e.live.createMode = "hang"
	e.live.createDelay = 400 * time.Millisecond
	const path = "/api/registrars/namecheap/register"
	if code, raw := e.do(t, http.MethodPost, path, registerBody("slow.com", 1, 100)); code != http.StatusBadGateway || !strings.Contains(raw, `"pending":true`) {
		t.Fatalf("register %d %s", code, raw)
	}
	code, raw := e.do(t, http.MethodPost, path, reconcileBody("slow.com"))
	if code != http.StatusConflict {
		t.Fatalf("an unconfirmed purchase is not settled: %d %s", code, raw)
	}
	if list := e.domains(t); len(list) != 1 || list[0].RegistrarStatus != "pending" {
		t.Fatalf("the pending row must stay: %+v", list)
	}
	e.live.setOwned("slow.com")
	if code, raw = e.do(t, http.MethodPost, path, reconcileBody("slow.com")); code != http.StatusCreated {
		t.Fatalf("reconcile %d %s", code, raw)
	}
	if d := e.domains(t)[0]; d.Disabled || d.RegistrarStatus != "" {
		t.Fatalf("a confirmed purchase stays hidden: %+v", d)
	}
	if n := e.live.count("namecheap.domains.create"); n != 1 {
		t.Fatalf("create calls %d, want the single original one", n)
	}
}
