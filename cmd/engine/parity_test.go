package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/store"
)

func loadBook[T any](t *testing.T, name string, list []T, idOf func(T) string) *config.Book[T] {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	book, err := config.OpenBook(path, idOf)
	if err != nil {
		t.Fatal(err)
	}
	return book
}

func TestEngineOfferLandingAndSubID(t *testing.T) {
	offers := loadBook(t, "offers.json", []config.Offer{
		{ID: "o1", URL: "https://offer.example/a?c={subid}&z={sub_id_6}", Status: true},
	}, func(o config.Offer) string { return o.ID })
	landings := loadBook(t, "landings.json", []config.Landing{
		{ID: "l1", URL: "https://land.example/", Status: true},
	}, func(o config.Landing) string { return o.ID })
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop",
		Streams: []config.Stream{{
			Name: "s", Status: true,
			Out: config.Output{
				Redirect: "http_redirect",
				Landings: []config.ShareRef{{ID: "l1", Share: 1}},
				Offers:   []config.ShareRef{{ID: "o1", Share: 1}},
			},
		}},
	})
	h := testEnv(t, g, func(d *engineDeps) {
		d.offers = offers
		d.landings = landings
	})
	rec := do(t, h, "/g1?sub_id_6=KEEP", "8.8.8.8", nil)
	if loc := rec.Header().Get("Location"); loc != "https://land.example/" {
		t.Fatalf("landing location = %q", loc)
	}
	rec = do(t, h, "/g1/offer?sub_id_6=KEEP", "8.8.8.8", nil)
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "z=KEEP") || strings.Contains(loc, "{sub_id_6}") || strings.Contains(loc, "{subid}") {
		t.Fatalf("offer location = %q", loc)
	}
}

func TestQueryParamMacroInShowText(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "a={bundle_id}&b={missing}"}})
	h := testEnv(t, gg)
	rec := do(t, h, "/g1?bundle_id=com.x", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	if got := rec.Body.String(); got != "a=com.x&b={missing}" {
		t.Fatalf("body %q, want a=com.x&b={missing}", got)
	}
}

func TestOutboundFiresOncePerStatus(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{{
			URL: "https://pb.example/pb?cid={subid}&st={status}", Method: "GET", Statuses: []string{"sale"},
		}},
	})
	d := &engineDeps{groups: g, outbound: make(chan outboundJob, 4)}
	d.fireOutbound("g1", "abc", "", 1.5, "lead", [config.MaxSubID]string{})
	d.fireOutbound("g1", "abc", "", 2, "sale", [config.MaxSubID]string{})
	if len(d.outbound) != 1 {
		t.Fatalf("queued %d, want the sale only", len(d.outbound))
	}
	job := <-d.outbound
	if !strings.Contains(job.URL, "cid=abc") || !strings.Contains(job.URL, "st=sale") {
		t.Fatalf("url=%s", job.URL)
	}
}

func TestQueueOutboundOnce(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{{
			URL: "https://pb.example/pb?cid={subid}&st={status}", Method: "GET", Statuses: []string{"sale"},
		}},
	})
	d := &engineDeps{groups: g, outbound: make(chan outboundJob, 4)}
	fresh := store.ConversionResult{GroupID: "g1", Fresh: true}
	d.queueOutbound(fresh, "abc", 1.5, "sale", [config.MaxSubID]string{})
	d.queueOutbound(fresh, "abc", 1.5, "sale", [config.MaxSubID]string{})
	if len(d.outbound) != 1 {
		t.Fatalf("queued %d, want one", len(d.outbound))
	}
}

// TestQueueOutboundSkipsRepeat checks that a pair the store already had sends
// nothing, however many times the postback arrives.
func TestQueueOutboundSkipsRepeat(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{{
			URL: "https://pb.example/pb?cid={subid}", Method: "GET", Statuses: []string{"sale"},
		}},
	})
	d := &engineDeps{groups: g, outbound: make(chan outboundJob, 4)}
	d.queueOutbound(store.ConversionResult{GroupID: "g1"}, "abc", 1, "sale", [config.MaxSubID]string{})
	if len(d.outbound) != 0 {
		t.Fatalf("queued %d for a repeated pair, want none", len(d.outbound))
	}
}

func TestQueueOutboundUnmarksWhenDropped(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{{
			URL: "https://pb.example/pb?cid={subid}", Method: "GET", Statuses: []string{"sale"},
		}},
	})
	d := &engineDeps{groups: g, outbound: make(chan outboundJob)}
	d.queueOutbound(store.ConversionResult{GroupID: "g1", Fresh: true}, "abc", 1, "sale", [config.MaxSubID]string{})
	if !d.takeOutbound(context.Background(), "abc", "sale") {
		t.Fatal("dropped job kept the lock")
	}
}

func TestOutboundSkipsPrivateHost(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	d := &engineDeps{}
	d.sendOutbound(srv.Client(), outboundJob{Method: "GET", URL: srv.URL + "/pb?cid=abc", CID: "abc", Status: "sale"})
	if hits != 0 {
		t.Fatalf("private host was requested %d times", hits)
	}
	if outboundURLAllowed("http://clickhouse:8123/") || outboundURLAllowed("http://engine:8080/") || outboundURLAllowed("http://169.254.169.254/") {
		t.Fatal("local names were allowed")
	}
	if !outboundURLAllowed("https://pb.example/hook?cid=abc") {
		t.Fatal("public host refused")
	}
}

func TestOfferStepReusesCID(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop",
		Streams: []config.Stream{{
			Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "ok"},
		}},
	})
	h, ins, stop := curlEnv(t, g, "0")
	rec := do(t, h, "/g1/offer?cid=0123456789", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	stop()
	if rec.Body.String() != "ok" {
		t.Fatalf("body %q", rec.Body.String())
	}
	if len(ins.events) != 1 || ins.events[0].CID != "0123456789" {
		t.Fatalf("events %+v", ins.events)
	}
}
