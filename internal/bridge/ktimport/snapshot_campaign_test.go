package ktimport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// twoCampaignServer отдаёт две кампании с потоками, офферы, лендинги, сети, источники и домены.
//
// Параметры: t — тест; seen — куда записать пути запросов.
// Результат: запущенный сервер; закрывает его вызывающий.
// Ошибки: нет.
func twoCampaignServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = io.WriteString(w, `[{"id":7,"alias":"test","name":"T","state":"active","traffic_source_id":3,"domain_id":5},
				{"id":8,"alias":"other","name":"O","state":"active","traffic_source_id":4}]`)
		case "/admin_api/v1/campaigns/7/streams":
			_, _ = io.WriteString(w, `[{"id":43041,"type":"forced","name":"Bot Protection","state":"active","action_type":"status404",
				"filters":[{"name":"bot","mode":"accept","payload":null}]},
				{"id":44844,"type":"regular","name":"Flow","state":"active","schema":"landings","action_type":"http",
				"filters":[{"name":"bot","mode":"reject","payload":null}],"offers":[{"offer_id":11,"share":100,"state":"active"}],
				"landings":[{"landing_id":21,"share":100,"state":"active"}]}]`)
		case "/admin_api/v1/campaigns/8/streams":
			t.Errorf("streams of another campaign were read")
			_, _ = io.WriteString(w, `[]`)
		case "/admin_api/v1/offers":
			_, _ = io.WriteString(w, `[{"id":11,"name":"A","affiliate_network_id":31,"action_type":"http","action_payload":"https://a.example/","state":"active"},
				{"id":12,"name":"B","affiliate_network_id":32,"action_type":"http","action_payload":"https://b.example/","state":"active"}]`)
		case "/admin_api/v1/landing_pages":
			_, _ = io.WriteString(w, `[{"id":21,"name":"L","landing_type":"external","action_type":"http","action_payload":"https://l.example/","state":"active"},
				{"id":22,"name":"M","landing_type":"external","action_type":"http","action_payload":"https://m.example/","state":"active"}]`)
		case "/admin_api/v1/affiliate_networks":
			_, _ = io.WriteString(w, `[{"id":31,"name":"N1","state":"active"},{"id":32,"name":"N2","state":"active"}]`)
		case "/admin_api/v1/traffic_sources":
			_, _ = io.WriteString(w, `[{"id":3,"name":"S3","state":"active"},{"id":4,"name":"S4","state":"active"}]`)
		case "/admin_api/v1/domains":
			_, _ = io.WriteString(w, `[{"id":5,"name":"d5.example","state":"active"},{"id":6,"name":"d6.example","state":"active"}]`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
}

func TestFetchCampaignSnapshotOnlyThatCampaign(t *testing.T) {
	var seen []string
	srv := twoCampaignServer(t, &seen)
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	snap, err := c.FetchCampaignSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range seen {
		if !strings.HasPrefix(p, "GET ") || strings.HasSuffix(p, "/users") {
			t.Errorf("unexpected request %s", p)
		}
	}
	if len(snap.Campaigns) != 2 || len(snap.Streams) != 1 || len(snap.Streams["7"]) != 2 {
		t.Fatalf("campaigns %d streams %v", len(snap.Campaigns), snap.Streams)
	}
	if len(snap.Offers) != 1 || int(snap.Offers[0].ID) != 11 || len(snap.Landings) != 1 || int(snap.Landings[0].ID) != 21 {
		t.Fatalf("offers %+v landings %+v", snap.Offers, snap.Landings)
	}
	if len(snap.AffiliateNetworks) != 1 || int(snap.AffiliateNetworks[0].ID) != 31 {
		t.Fatalf("networks %+v", snap.AffiliateNetworks)
	}
	if len(snap.TrafficSources) != 1 || int(snap.TrafficSources[0].ID) != 3 || len(snap.Domains) != 1 || int(snap.Domains[0].ID) != 5 {
		t.Fatalf("sources %+v domains %+v", snap.TrafficSources, snap.Domains)
	}
}

func TestFetchCampaignSnapshotUnknownID(t *testing.T) {
	var seen []string
	srv := twoCampaignServer(t, &seen)
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	_, err := c.FetchCampaignSnapshot(context.Background(), 99)
	if !errors.Is(err, ErrCampaignNotFound) || !strings.Contains(err.Error(), "99") {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.FetchCampaignSnapshot(context.Background(), 0); err == nil || errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("zero id err = %v", err)
	}
}

func TestFetchCampaignsSnapshotOnlyChosen(t *testing.T) {
	var seen []string
	srv := twoCampaignServer(t, &seen)
	defer srv.Close()
	c := &Client{Base: srv.URL, Key: "k-test", HTTP: srv.Client()}
	snap, err := c.FetchCampaignsSnapshot(context.Background(), []int{7, 99})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range seen {
		if !strings.HasPrefix(p, "GET ") || strings.HasSuffix(p, "/users") {
			t.Errorf("unexpected request %s", p)
		}
	}
	if len(snap.Campaigns) != 2 || len(snap.Streams) != 1 || len(snap.Streams["7"]) != 2 || len(snap.Offers) != 2 {
		t.Fatalf("campaigns %d streams %v offers %d", len(snap.Campaigns), snap.Streams, len(snap.Offers))
	}
	one := snap.ForCampaign(7)
	if len(one.Offers) != 1 || int(one.Offers[0].ID) != 11 || len(one.Domains) != 1 {
		t.Fatalf("one = %+v", one)
	}
	if _, err := c.FetchCampaignsSnapshot(context.Background(), nil); err == nil {
		t.Fatal("empty ids accepted")
	}
	if _, err := c.FetchCampaignsSnapshot(context.Background(), []int{7, 0}); err == nil {
		t.Fatal("zero id accepted")
	}
}

func TestMapOneCampaignKeepsFullImportIDs(t *testing.T) {
	snap := Snapshot{
		Campaigns: []Campaign{
			{ID: 1, Alias: "same", Name: "A", State: "active"},
			{ID: 2, Alias: "same", Name: "B", State: "active"},
			{ID: 3, Alias: "z", Name: "C", State: "active"},
		},
		Streams: map[string][]KStream{
			"1": {{ID: 10, Type: "regular", Name: "s1", State: "active", ActionType: "http", ActionPayload: "https://a.example/"}},
			"2": {{ID: 20, Type: "forced", Name: "Bot Protection", State: "active", ActionType: "status404",
				Filters: []KFilter{{Name: "bot", Mode: "accept"}}}},
			"3": {{ID: 30, Type: "regular", Name: "s3", State: "active", ActionType: "http", ActionPayload: "https://c.example/"}},
		},
	}
	full := MapAll(snap)
	one := Map(snap.ForCampaign(2), Selection{Campaigns: true, Offers: true, Landings: true, Networks: true, Sources: true, Domains: true, Campaign: 2})
	if len(one.Groups) != 1 || one.Groups[0].Num != 2 {
		t.Fatalf("groups = %+v", one.Groups)
	}
	var fullID string
	for _, g := range full.Groups {
		if g.Num == 2 {
			fullID = g.ID
		}
	}
	if fullID == "" || one.Groups[0].ID != fullID {
		t.Fatalf("one id %q, full id %q", one.Groups[0].ID, fullID)
	}
	if s := one.Groups[0].Streams; len(s) != 1 || s[0].Rules.Bot != 1 {
		t.Fatalf("streams = %+v", s)
	}
}
