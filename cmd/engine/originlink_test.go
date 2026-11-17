package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/render"
)

func testDomains(t *testing.T, rows ...config.Domain) *config.Book[config.Domain] {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "domains.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := config.OpenBook(p, func(d config.Domain) string { return d.ID })
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func originLinkDeps(t *testing.T, host string, groups ...*config.Group) (*engineDeps, *cidSigner) {
	t.Helper()
	key := strings.Repeat("k", cidKeyBytes)
	signer := newCIDSigner(key, "", discardLog())
	d := &engineDeps{
		groups:  linked(groups...),
		domains: testDomains(t, config.Domain{ID: "1", Host: host}),
		cids:    signer,
	}
	return d, signer
}

func TestWithOriginAppendsOnOwnHost(t *testing.T) {
	d, _ := originLinkDeps(t, "emaso.online",
		&config.Group{ID: "b", Status: true},
	)
	got := d.withOrigin("https://EMASO.ONLINE:443/b", "tok")
	if got != "https://EMASO.ONLINE:443/b?origin=tok" {
		t.Fatalf("location %q", got)
	}
	if d.stats.originLink.added.Load() != 1 || d.stats.originLink.unknownHost.Load() != 0 {
		t.Fatalf("added=%d unknown=%d", d.stats.originLink.added.Load(), d.stats.originLink.unknownHost.Load())
	}
	disabled := testDomains(t, config.Domain{ID: "1", Host: "emaso.online", Disabled: true})
	d.domains = disabled
	again := d.withOrigin("https://emaso.online/b?x=1#z", "tok")
	if again != "https://emaso.online/b?x=1&origin=tok#z" {
		t.Fatalf("disabled host %q", again)
	}
}

func TestWithOriginLeavesForeignAndInactive(t *testing.T) {
	d, _ := originLinkDeps(t, "emaso.online",
		&config.Group{ID: "b", Status: true},
		&config.Group{ID: "off", Status: false},
	)
	foreign := "https://other.example/b?x=1"
	if got := d.withOrigin(foreign, "tok"); got != foreign || d.stats.originLink.unknownHost.Load() != 1 {
		t.Fatalf("foreign %q unknown=%d", got, d.stats.originLink.unknownHost.Load())
	}
	plain := "https://emaso.online/"
	if got := d.withOrigin(plain, "tok"); got != plain {
		t.Fatalf("no campaign %q", got)
	}
	off := "https://emaso.online/off"
	if got := d.withOrigin(off, "tok"); got != off {
		t.Fatalf("disabled campaign %q", got)
	}
	for _, loc := range []string{"/b", "//emaso.online/b", "https://emaso.online/missing"} {
		if got := d.withOrigin(loc, "tok"); got != loc {
			t.Fatalf("%q became %q", loc, got)
		}
	}
	if d.stats.originLink.added.Load() != 0 {
		t.Fatalf("added %d", d.stats.originLink.added.Load())
	}
}

func TestWithOriginKeepsLiveToken(t *testing.T) {
	d, signer := originLinkDeps(t, "emaso.online", &config.Group{ID: "b", Status: true})
	live := signer.signPath("a", "rootclick01", time.Now())
	loc := "https://emaso.online/b?origin=" + live + "&x=1"
	if got := d.withOrigin(loc, "other"); got != loc {
		t.Fatalf("live token changed %q", got)
	}
	bad := "https://emaso.online/b?x=1&origin=a.forged"
	got := d.withOrigin(bad, "tok")
	if got != "https://emaso.online/b?x=1&origin=tok" {
		t.Fatalf("invalid origin %q", got)
	}
	if d.stats.originLink.added.Load() != 1 {
		t.Fatalf("added %d", d.stats.originLink.added.Load())
	}
}

func TestExtraParamsSkipsOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/g1?a=1&origin=tok&b=2&q=key", nil)
	got := extraParams(req)
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("params %v", got)
	}
}

func TestLandingEntryOriginHeaderAndRedirect(t *testing.T) {
	var gotOrigin string
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin = r.Header.Get("X-Emtds-Origin")
		w.Header().Set("Location", "https://emaso.online/b?x=1#z")
		w.WriteHeader(http.StatusFound)
	}))
	defer php.Close()
	srv := newLandingServer(t.TempDir(), php.URL, func(id string) (config.Landing, bool) {
		if id == "7" {
			return config.Landing{ID: "7", Status: true, Local: true, Entry: "index.php"}, true
		}
		return config.Landing{}, false
	}, discardLog())
	d, _ := originLinkDeps(t, "emaso.online", &config.Group{ID: "b", Status: true})
	srv.originOn = d.withOrigin
	req := httptest.NewRequest(http.MethodGet, "/g1", nil)
	rec := httptest.NewRecorder()
	srv.serveEntry(rec, req, "7", render.MacroDeps{CID: "0123456789", Origin: "path-token"})
	if gotOrigin != "path-token" {
		t.Fatalf("X-Emtds-Origin %q", gotOrigin)
	}
	if loc := rec.Header().Get("Location"); loc != "https://emaso.online/b?x=1&origin=path-token#z" {
		t.Fatalf("location %q", loc)
	}
}
