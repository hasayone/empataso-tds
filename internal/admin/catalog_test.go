package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// memGroups is an in-memory campaign store. lists counts the List calls.
type memGroups struct {
	list  []config.Group
	lists atomic.Int32
}

func (m *memGroups) List(context.Context) ([]config.Group, error) {
	m.lists.Add(1)
	return append([]config.Group(nil), m.list...), nil
}

func (m *memGroups) Save(_ context.Context, g []config.Group) error {
	m.list = append([]config.Group(nil), g...)
	return nil
}

func catalogServer(t *testing.T) (*httptest.Server, *memGroups, string) {
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
	return srv, groups, dir
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
	srv, _, _ := catalogServer(t)
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

func TestOfferPayoutAndCountries(t *testing.T) {
	srv, _, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	offer := config.Offer{
		ID: "o-pay", Name: "Paid", URL: "https://offer.example/pay", Status: true,
		Countries: []string{"ru", "us"}, PayoutModel: "cpa", Payout: 12.5,
		PayoutCurrency: "USD", PayoutParam: true,
	}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, offer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d", resp.StatusCode)
	}
	var created config.Offer
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(created.Countries) != 2 || created.Countries[0] != "ru" || created.PayoutModel != "cpa" ||
		created.Payout != 12.5 || created.PayoutCurrency != "USD" || !created.PayoutParam {
		t.Fatalf("created = %+v", created)
	}
	resp = csrfDo(t, c, http.MethodGet, srv.URL+"/api/offers/o-pay", csrf, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %d", resp.StatusCode)
	}
	var loaded config.Offer
	if err := json.NewDecoder(resp.Body).Decode(&loaded); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loaded.PayoutModel != "cpa" || len(loaded.Countries) != 2 {
		t.Fatalf("loaded = %+v", loaded)
	}

	badModel := offer
	badModel.ID = "o-bad-model"
	badModel.PayoutModel = "revshare"
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, badModel)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad payout_model → %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	badCountry := offer
	badCountry.ID = "o-bad-cc"
	badCountry.Countries = []string{"rus"}
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, badCountry)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad country → %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "offers.json")
	if err := os.WriteFile(path, []byte(`[
  {"id":"legacy","name":"Old","url":"https://offer.example/old","status":true}
]`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewJSONStore[config.Offer](path)
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "legacy" || list[0].PayoutModel != "" || len(list[0].Countries) != 0 {
		t.Fatalf("legacy list = %+v", list)
	}
}

func TestCampaignPostbacksReplace(t *testing.T) {
	srv, groups, _ := catalogServer(t)
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
	srv, _, _ := catalogServer(t)
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

func TestCatalogGroupTrimAndOmit(t *testing.T) {
	srv, _, dir := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	long65 := strings.Repeat("а", 65)
	long64 := strings.Repeat("а", 64)
	landing := config.Landing{ID: "ld1", Name: "Landing", URL: "https://land.example/x", Status: true, Group: long65}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/landings", csrf, landing)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("landing create %d", resp.StatusCode)
	}
	var created config.Landing
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.Group != long64 || created.Name != landing.Name || created.URL != landing.URL || created.ID != landing.ID {
		t.Fatalf("landing = %+v", created)
	}

	netBody := config.AffiliateNetwork{ID: "net1", Name: "Net", Status: true, PostbackURL: "https://pb.example/"}
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/affiliate-networks", csrf, netBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("network create %d", resp.StatusCode)
	}
	resp.Body.Close()
	netBody.Group = "  abc  "
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/affiliate-networks/net1", csrf, netBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("network put %d", resp.StatusCode)
	}
	var net config.AffiliateNetwork
	if err := json.NewDecoder(resp.Body).Decode(&net); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if net.Group != "abc" {
		t.Fatalf("network group = %q", net.Group)
	}

	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/traffic-sources", csrf, config.TrafficSource{
		ID: "src1", Name: "Src", Group: "",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("source create %d", resp.StatusCode)
	}
	resp.Body.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"group"`) {
		t.Fatalf("sources file contains group: %s", raw)
	}

	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/landings", csrf, config.Landing{
		ID: "ld64", Name: "Full", URL: "https://land.example/y", Status: true, Group: long64,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("landing 64 %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.Group != long64 {
		t.Fatalf("landing 64 group len %d", len([]rune(created.Group)))
	}
}

// TestLandingEntryExtensionValidation checks that Entry with a non-allowed extension
// is rejected with 400, while .html, .htm and .php are accepted.
func TestLandingEntryExtensionValidation(t *testing.T) {
	srv, _, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	// notes.txt must be rejected.
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/landings", csrf, config.Landing{
		ID: "l-bad", Local: true, Entry: "notes.txt",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("notes.txt Entry → %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// .html must be accepted.
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/landings", csrf, config.Landing{
		ID: "l-html", Local: true, Entry: "index.html",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("index.html Entry → %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	// .php must be accepted.
	resp = csrfDo(t, c, http.MethodPost, srv.URL+"/api/landings", csrf, config.Landing{
		ID: "l-php", Local: true, Entry: "index.php",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("index.php Entry → %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTrafficSourceRejectsSubID16(t *testing.T) {
	srv, _, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/traffic-sources", csrf, config.TrafficSource{
		ID: "src", Parameters: map[string]string{"sub_id_31": "x"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("sub_id_31 → %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
