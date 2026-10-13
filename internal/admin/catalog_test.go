package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

type memGroups struct{ list []config.Group }

func (m *memGroups) List(context.Context) ([]config.Group, error) {
	return append([]config.Group(nil), m.list...), nil
}

func (m *memGroups) Save(_ context.Context, g []config.Group) error {
	m.list = append([]config.Group(nil), g...)
	return nil
}

func catalogServer(t *testing.T) (*httptest.Server, *memGroups) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	groups := &memGroups{list: []config.Group{{ID: "promo", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "s", Status: true, Out: config.Output{Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o1", Share: 1}}}},
	}}}}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: groups, Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		Offers:   NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings: NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:  NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks: NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, groups
}

func csrfDo(t *testing.T, c *http.Client, method, url, csrf string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestOfferCRUDAndSimulate(t *testing.T) {
	srv, _ := catalogServer(t)
	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != 200 {
		t.Fatalf("login %d", code)
	}
	offer := config.Offer{ID: "o1", Name: "A", URL: "https://offer.example/a", Status: true}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, offer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, offer)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second create %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	offer.Name = "A2"
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/offers/o1", csrf, offer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = csrfDo(t, c, http.MethodGet, srv.URL+"/api/offers", csrf, nil)
	var list []config.Offer
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list) != 1 || list[0].Name != "A2" {
		t.Fatalf("list = %+v", list)
	}

	var sim struct {
		Path []simStep `json:"path"`
	}
	body := map[string]any{
		"group": "promo",
		"groups": []config.Group{{ID: "promo", Status: true, Redirect: "stop", Streams: []config.Stream{
			{Name: "s", Status: true, Out: config.Output{Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o1", Share: 1}}}},
		}}},
		"offers":  []config.Offer{offer},
		"visitor": map[string]string{"ip": "8.8.8.8"},
	}
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/simulate", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("simulate %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&sim); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(sim.Path) == 0 || sim.Path[0].Out != "https://offer.example/a" {
		t.Fatalf("simulate out = %+v", sim.Path)
	}
}

func TestCampaignPostbacksReplace(t *testing.T) {
	srv, groups := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	body := []config.OutboundPostback{{URL: "https://pb.example/?cid={subid}&st={status}", Method: "GET", Statuses: []string{"sale"}}}
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/campaigns/promo/postbacks", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put postbacks %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/campaigns/promo/postbacks", csrf, body)
	resp.Body.Close()
	if len(groups.list) != 1 || len(groups.list[0].Postbacks) != 1 {
		t.Fatalf("postbacks = %+v", groups.list)
	}
}

func TestAffiliateNetworkCreate(t *testing.T) {
	srv, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	body := config.AffiliateNetwork{
		ID: "s2s", Name: "Generic", Status: true,
		PostbackURL: "https://tracker.example/?cid={subid}&status={status}&profit={payout}",
		OfferParam:  "offer_id",
	}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/affiliate-networks", csrf, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create network %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/affiliate-networks", csrf, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate network %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTrafficSourceRejectsSubID16(t *testing.T) {
	srv, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/traffic-sources", csrf, config.TrafficSource{
		ID: "src", Parameters: map[string]string{"sub_id_16": "x"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("sub_id_16 → %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
