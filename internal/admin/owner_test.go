package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

func ownerServer(t *testing.T) (*httptest.Server, *memGroups, *Server, string) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	groups := &memGroups{}
	usersPath := filepath.Join(dir, "users.json")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		UsersFile: usersPath,
		Sessions:  security.NewMemoryStore(), Stats: fakeStats{},
		Groups: groups, Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:     NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Domains:      NewJSONStore[config.Domain](filepath.Join(dir, "domains.json")),
		Sources:      NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		LandingFiles: NewFileLandings(filepath.Join(dir, "landing-files")),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, groups, s, dir
}

func userSectionsAll() []string {
	return sectionStrings(userSections)
}

func mustUser(t *testing.T, s *Server, login, pass string, acc security.Access) security.User {
	t.Helper()
	u, err := s.createUser(userDraft{
		Login: login, Password: pass, Type: security.UserRegular, Enabled: true, Access: acc,
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOwnerCatalogAndCampaigns(t *testing.T) {
	srv, groups, s, dir := ownerServer(t)
	root := newClient(t)
	_, rootCSRF := login(t, root, srv.URL, "admin", "p@ss")

	mustUser(t, s, "sel", "secret1", security.Access{
		Sections: userSectionsAll(),
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeSelected, IDs: []string{"c1"}},
			resOffers:    {Mode: security.ModeRead},
			resLandings:  {Mode: security.ModeSelected, Labels: []string{"promo"}},
		},
	})
	own := mustUser(t, s, "own", "secret2", security.Access{
		Sections: userSectionsAll(),
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeCreated},
			resOffers:    {Mode: security.ModeCreated},
			resDomains:   {Mode: security.ModeCreated},
		},
	})

	c1 := config.Group{ID: "c1", Name: "one", Status: true, Redirect: "stop", Streams: []config.Stream{}}
	c2 := config.Group{ID: "c2", Name: "two", Status: true, Redirect: "stop", Streams: []config.Stream{}}
	if resp := csrfDo(t, root, http.MethodPut, srv.URL+"/api/groups", rootCSRF, []config.Group{c1, c2}); resp.StatusCode != http.StatusOK {
		t.Fatalf("root groups: %d", resp.StatusCode)
	}
	resp := csrfDo(t, root, http.MethodPost, srv.URL+"/api/offers", rootCSRF, config.Offer{
		ID: "o1", Name: "O1", URL: "https://a/", Status: true, Group: "promo",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("o1: %d", resp.StatusCode)
	}
	resp = csrfDo(t, root, http.MethodPost, srv.URL+"/api/offers", rootCSRF, config.Offer{
		ID: "o2", Name: "O2", URL: "https://b/", Status: true, Group: "other",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("o2: %d", resp.StatusCode)
	}
	resp = csrfDo(t, root, http.MethodPost, srv.URL+"/api/landings", rootCSRF, config.Landing{
		ID: "lpromo", Name: "P", URL: "https://p/", Status: true, Group: "promo", Local: true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("lpromo: %d", resp.StatusCode)
	}
	resp = csrfDo(t, root, http.MethodPost, srv.URL+"/api/landings", rootCSRF, config.Landing{
		ID: "lother", Name: "O", URL: "https://o/", Status: true, Group: "x", Local: true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("lother: %d", resp.StatusCode)
	}

	selC := newClient(t)
	_, selCSRF := login(t, selC, srv.URL, "sel", "secret1")

	resp = csrfDo(t, selC, http.MethodGet, srv.URL+"/api/groups", selCSRF, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	var gotGroups []config.Group
	decodeJSON(t, resp, &gotGroups)
	if len(gotGroups) != 1 || gotGroups[0].ID != "c1" {
		t.Fatalf("sel groups: %+v", gotGroups)
	}

	resp = csrfDo(t, selC, http.MethodGet, srv.URL+"/api/offers", selCSRF, nil)
	var gotOffers []config.Offer
	decodeJSON(t, resp, &gotOffers)
	if len(gotOffers) != 2 {
		t.Fatalf("sel offers read: %d", len(gotOffers))
	}
	resp = csrfDo(t, selC, http.MethodPut, srv.URL+"/api/offers/o1", selCSRF, gotOffers[0])
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sel put offer: %d", resp.StatusCode)
	}
	resp = csrfDo(t, selC, http.MethodPost, srv.URL+"/api/offers", selCSRF, config.Offer{Name: "n", URL: "https://n/", Status: true})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sel post offer: %d", resp.StatusCode)
	}

	resp = csrfDo(t, selC, http.MethodGet, srv.URL+"/api/landings", selCSRF, nil)
	var gotLand []config.Landing
	decodeJSON(t, resp, &gotLand)
	if len(gotLand) != 1 || gotLand[0].ID != "lpromo" {
		t.Fatalf("sel landings: %+v", gotLand)
	}
	resp = csrfDo(t, selC, http.MethodGet, srv.URL+"/api/landings/lother", selCSRF, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("sel landing other: %d", resp.StatusCode)
	}

	c1mod := c1
	c1mod.Name = "one-edited"
	resp = csrfDo(t, selC, http.MethodPut, srv.URL+"/api/groups", selCSRF, []config.Group{c1mod})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sel edit c1: %d", resp.StatusCode)
	}
	if len(groups.list) != 2 || groups.list[0].Name != "one-edited" {
		t.Fatalf("file after c1 edit: %+v", groups.list)
	}

	c2mod := c2
	c2mod.Name = "hack"
	resp = csrfDo(t, selC, http.MethodPut, srv.URL+"/api/groups", selCSRF, []config.Group{c1mod, c2mod})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sel edit c2: %d", resp.StatusCode)
	}
	resp = csrfDo(t, selC, http.MethodPut, srv.URL+"/api/groups", selCSRF, groups.list)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sel noop save: %d", resp.StatusCode)
	}
	c3new := config.Group{ID: "c3", Name: "three", Status: true, Redirect: "stop", Streams: []config.Stream{}}
	resp = csrfDo(t, selC, http.MethodPut, srv.URL+"/api/groups", selCSRF, append(groups.list, c3new))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sel create c3: %d", resp.StatusCode)
	}

	ownC := newClient(t)
	_, ownCSRF := login(t, ownC, srv.URL, "own", "secret2")
	resp = csrfDo(t, ownC, http.MethodPut, srv.URL+"/api/groups", ownCSRF, append(groups.list, c3new))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own create c3: %d", resp.StatusCode)
	}
	var c3stored config.Group
	for _, g := range groups.list {
		if g.ID == "c3" {
			c3stored = g
		}
	}
	if c3stored.CreatedBy != own.ID {
		t.Fatalf("c3 owner %q want %q", c3stored.CreatedBy, own.ID)
	}
	resp = csrfDo(t, ownC, http.MethodGet, srv.URL+"/api/groups", ownCSRF, nil)
	decodeJSON(t, resp, &gotGroups)
	if len(gotGroups) != 1 || gotGroups[0].ID != "c3" {
		t.Fatalf("own groups: %+v", gotGroups)
	}

	resp = csrfDo(t, ownC, http.MethodPost, srv.URL+"/api/offers", ownCSRF, config.Offer{
		ID: "o_own", Name: "mine", URL: "https://mine/", Status: true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("own post offer: %d", resp.StatusCode)
	}
	var ownOffer config.Offer
	decodeJSON(t, resp, &ownOffer)
	if ownOffer.CreatedBy != own.ID {
		t.Fatalf("offer owner")
	}
	resp = csrfDo(t, ownC, http.MethodGet, srv.URL+"/api/offers", ownCSRF, nil)
	decodeJSON(t, resp, &gotOffers)
	if len(gotOffers) != 1 {
		t.Fatalf("own offers list: %d", len(gotOffers))
	}
	bodyNo := ownOffer
	bodyNo.CreatedBy = ""
	resp = csrfDo(t, root, http.MethodPut, srv.URL+"/api/offers/"+ownOffer.ID, rootCSRF, bodyNo)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root keep owner: %d", resp.StatusCode)
	}
	resp = csrfDo(t, root, http.MethodGet, srv.URL+"/api/offers/"+ownOffer.ID, rootCSRF, nil)
	decodeJSON(t, resp, &ownOffer)
	if ownOffer.CreatedBy != own.ID {
		t.Fatalf("owner preserved")
	}
	streamBad := config.Stream{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o2", Share: 1}},
	}}
	c3bad := c3stored
	c3bad.Streams = []config.Stream{streamBad}
	resp = csrfDo(t, ownC, http.MethodPut, srv.URL+"/api/groups", ownCSRF, []config.Group{c3bad})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("own bad offer link: %d", resp.StatusCode)
	}
	streamOk := config.Stream{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: ownOffer.ID, Share: 1}},
	}}
	c3ok := c3stored
	c3ok.Streams = []config.Stream{streamOk}
	resp = csrfDo(t, ownC, http.MethodPut, srv.URL+"/api/groups", ownCSRF, []config.Group{c3ok})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own ok offer link: %d", resp.StatusCode)
	}
	c3admin := c3ok
	c3admin.Streams = []config.Stream{{Name: "s", Status: true, Out: config.Output{
		Redirect: "http_redirect", Offers: []config.ShareRef{{ID: "o2", Share: 1}},
	}}}
	resp = csrfDo(t, root, http.MethodPut, srv.URL+"/api/groups", rootCSRF, []config.Group{c1, c2, c3admin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root o2 link: %d", resp.StatusCode)
	}
	c3rename := c3admin
	c3rename.Name = "renamed"
	resp = csrfDo(t, ownC, http.MethodPut, srv.URL+"/api/groups", ownCSRF, []config.Group{c3rename})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grandfather link: %d", resp.StatusCode)
	}

	bodyNo.CreatedBy = "x"
	resp = csrfDo(t, root, http.MethodPut, srv.URL+"/api/offers/"+ownOffer.ID, rootCSRF, bodyNo)
	decodeJSON(t, resp, &ownOffer)
	if ownOffer.CreatedBy != "x" {
		t.Fatalf("root set owner")
	}

	resp = csrfDo(t, ownC, http.MethodPost, srv.URL+"/api/domains", ownCSRF, config.Domain{
		ID: "d1", Host: "links.example.com", Primary: true,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("own primary post: %d", resp.StatusCode)
	}
	resp = csrfDo(t, ownC, http.MethodPost, srv.URL+"/api/domains", ownCSRF, config.Domain{
		ID: "d2", Host: "x.example.com", Role: "admin",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("own admin role post: %d", resp.StatusCode)
	}
	resp = csrfDo(t, ownC, http.MethodPost, srv.URL+"/api/domains", ownCSRF, config.Domain{
		ID: "d3", Host: "y.example.com",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("own domain post: %d", resp.StatusCode)
	}
	var dom config.Domain
	decodeJSON(t, resp, &dom)
	if dom.Role != "links" || dom.CreatedBy != own.ID {
		t.Fatalf("domain defaults: %+v", dom)
	}
	resp = csrfDo(t, root, http.MethodPut, srv.URL+"/api/domains/d3", rootCSRF, config.Domain{
		ID: "d3", Host: "y.example.com", Role: "links", Primary: true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root primary: %d", resp.StatusCode)
	}
	resp = csrfDo(t, ownC, http.MethodPut, srv.URL+"/api/domains/d3", ownCSRF, config.Domain{
		ID: "d3", Host: "y.example.com", Primary: false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own domain put: %d", resp.StatusCode)
	}
	resp = csrfDo(t, root, http.MethodGet, srv.URL+"/api/domains/d3", rootCSRF, nil)
	decodeJSON(t, resp, &dom)
	if !dom.Primary {
		t.Fatal("primary kept")
	}

	resp = csrfDo(t, selC, http.MethodGet, srv.URL+"/api/landings/lother/files", selCSRF, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("sel files other: %d", resp.StatusCode)
	}
	resp = csrfDo(t, selC, http.MethodGet, srv.URL+"/api/landings/lpromo/files", selCSRF, nil)
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		t.Fatalf("sel promo files: %d", resp.StatusCode)
	}
	_ = dir
	resp = csrfDo(t, selC, http.MethodPut, srv.URL+"/api/landings/lpromo/files/index.html", selCSRF, "hi")
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("sel put landing file forbidden")
	}

	mustUser(t, s, "lread", "secret3", security.Access{
		Sections: userSectionsAll(),
		Grants: map[string]security.Grant{
			resLandings: {Mode: security.ModeRead},
		},
	})
	lc := newClient(t)
	_, lCSRF := login(t, lc, srv.URL, "lread", "secret3")
	resp = csrfDo(t, lc, http.MethodPut, srv.URL+"/api/landings/lpromo/files/index.html", lCSRF, "nope")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read landing put: %d", resp.StatusCode)
	}

	mustUser(t, s, "esel", "secret4", security.Access{
		Sections: userSectionsAll(),
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeSelected},
			resOffers:    {Mode: security.ModeSelected},
		},
	})
	ec := newClient(t)
	_, eCSRF := login(t, ec, srv.URL, "esel", "secret4")
	resp = csrfDo(t, ec, http.MethodGet, srv.URL+"/api/groups", eCSRF, nil)
	decodeJSON(t, resp, &gotGroups)
	if len(gotGroups) != 0 {
		t.Fatalf("empty selected groups: %d", len(gotGroups))
	}
	resp = csrfDo(t, ec, http.MethodGet, srv.URL+"/api/offers/o1", eCSRF, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("empty selected offer: %d", resp.StatusCode)
	}

	gpath := filepath.Join(dir, "groups-export.json")
	fg := NewFileGroups(gpath)
	if err := fg.Save(context.Background(), []config.Group{{ID: "gx", CreatedBy: "u9", Status: true, Redirect: "stop"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadGroups(gpath); err != nil {
		t.Fatal(err)
	}
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
