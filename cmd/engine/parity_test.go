package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
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

func TestOutboundFiresOncePerStatus(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Query().Get("st"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{{
			URL: srv.URL + "/pb?cid={subid}&st={status}", Method: "GET", Statuses: []string{"sale"},
		}},
	})
	d := &engineDeps{groups: g, outbound: make(chan outboundJob, 4)}
	d.fireOutbound("g1", "abc", 1.5, "lead", nil)
	d.fireOutbound("g1", "abc", 2, "sale", nil)
	if len(d.outbound) != 1 {
		t.Fatalf("queued %d, want the sale only", len(d.outbound))
	}
	job := <-d.outbound
	d.sendOutbound(srv.Client(), job)
	if len(hits) != 1 || hits[0] != "sale" || !strings.Contains(job.URL, "cid=abc") {
		t.Fatalf("hits=%v url=%s", hits, job.URL)
	}
}
