package ktimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func catalogFake(t *testing.T, paths *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*paths = append(*paths, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.Path {
		case "/admin_api/v1/offers/849":
			_, _ = w.Write([]byte(` {"id":849,"name":"Late","state":"active","action_type":"http","action_payload":"https://o.example/a","affiliate_network_id":60,"group_id":3}`))
		case "/admin_api/v1/offers/850", "/admin_api/v1/traffic_sources/5":
			http.NotFound(w, r)
		case "/admin_api/v1/affiliate_networks/60":
			_, _ = w.Write([]byte(`{"id":60,"name":"Net60","state":"active"}`))
		case "/admin_api/v1/affiliate_networks/61":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/admin_api/v1/domains/51":
			_, _ = w.Write([]byte(`{"id":51,"name":"late.example","state":"active","group_id":4}`))
		case "/admin_api/v1/domains/52":
			http.NotFound(w, r)
		case "/admin_api/v1/groups":
			if r.URL.Query().Get("type") == "domains" {
				_, _ = w.Write([]byte(`[{"id":4,"name":"Parked"}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":3,"name":"Folder"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchCatalogByIDFoundAndGone(t *testing.T) {
	var paths []string
	srv := catalogFake(t, &paths)
	c := &Client{Base: srv.URL, Key: "k", HTTP: srv.Client()}
	snap, gone, err := c.FetchCatalogByID(context.Background(), CatalogIDs{Offers: []int{849, 850, 0}, Networks: []int{60}, Sources: []int{5}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Offers) != 1 || int(snap.Offers[0].ID) != 849 || len(snap.AffiliateNetworks) != 1 {
		t.Fatalf("snap %+v", snap)
	}
	if len(gone.Offers) != 1 || gone.Offers[0] != 850 || len(gone.Sources) != 1 || gone.Count() != 2 {
		t.Fatalf("gone %+v", gone)
	}
	for _, p := range paths {
		if p[:4] != "GET " {
			t.Fatalf("non-GET %s", p)
		}
	}
	res := Map(snap, Selection{Offers: true, Networks: true})
	if len(res.Offers) != 1 || res.Offers[0].ID != "849" || res.Offers[0].Group != "Folder" || res.Offers[0].NetworkID != "60" {
		t.Fatalf("mapped %+v", res.Offers)
	}
}

func TestFetchCatalogByIDDomains(t *testing.T) {
	var paths []string
	srv := catalogFake(t, &paths)
	c := &Client{Base: srv.URL, Key: "k", HTTP: srv.Client()}
	snap, gone, err := c.FetchCatalogByID(context.Background(), CatalogIDs{Domains: []int{51, 52}})
	if err != nil {
		t.Fatal(err)
	}
	if len(gone.Domains) != 1 || gone.Domains[0] != 52 || gone.Count() != 1 {
		t.Fatalf("gone %+v", gone)
	}
	for _, p := range paths {
		if p[:4] != "GET " || p == "GET /admin_api/v1/domains" || p == "GET /admin_api/v1/groups?type=offers" {
			t.Fatalf("unexpected request %s", p)
		}
	}
	res := Map(snap, Selection{Domains: true})
	if len(res.Domains) != 1 || res.Domains[0].ID != "51" || res.Domains[0].Host != "late.example" || res.Domains[0].Group != "Parked" || res.Domains[0].Role != "links" {
		t.Fatalf("mapped %+v", res.Domains)
	}
}

func TestFetchCatalogByIDStopsOnError(t *testing.T) {
	var paths []string
	srv := catalogFake(t, &paths)
	c := &Client{Base: srv.URL, Key: "k", HTTP: srv.Client()}
	snap, _, err := c.FetchCatalogByID(context.Background(), CatalogIDs{Networks: []int{60, 61, 62}})
	if err == nil {
		t.Fatal("want error")
	}
	if len(snap.AffiliateNetworks) != 1 {
		t.Fatalf("partial %+v", snap.AffiliateNetworks)
	}
	for _, p := range paths {
		if p == "GET /admin_api/v1/affiliate_networks/62" {
			t.Fatal("read after error")
		}
	}
}
