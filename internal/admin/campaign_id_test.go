package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// TestCampaignSlugOnCreateAndCopy checks that creating and copying a campaign
// yields a 9-character slug from campaignIDAlphabet, never flow_0000 or flow_0001,
// and that two draws differ. A slug the caller set is kept. Campaigns already
// stored, including alloffers, middleware, a number and a legacy flow_0000, keep their ids.
func TestCampaignSlugOnCreateAndCopy(t *testing.T) {
	stored := []config.Group{
		{ID: "alloffers", Name: "All"},
		{ID: "middleware", Name: "Mid"},
		{ID: "849", Name: "Num"},
		{ID: "flow_0000", Name: "Legacy", Aliases: []string{"old-alias"}},
	}
	groups := append(append([]config.Group{}, stored...),
		config.Group{ID: "flow_0001", Name: "Stub"},
		config.Group{ID: "", Name: "Blank"},
		config.Group{ID: "flow1", Name: "OldPanel"},
		config.Group{ID: "my-promo", Name: "Manual"},
		config.Group{ID: "123", Name: "Number"},
	)
	if _, err := fillNewCampaignIDs(stored, groups); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alloffers", "middleware", "849", "flow_0000"} {
		if _, ok := groupByID(groups, id); !ok {
			t.Fatalf("stored campaign %s was renamed", id)
		}
	}
	if _, ok := groupByID(groups, "my-promo"); !ok {
		t.Fatal("a slug the caller set was replaced")
	}
	if _, ok := groupByID(groups, "123"); !ok {
		t.Fatal("a numeric slug the caller set was replaced")
	}
	for _, name := range []string{"Stub", "Blank", "OldPanel"} {
		g, ok := groupByName(groups, name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		assertCampaignSlug(t, g.ID)
	}
	stub, _ := groupByName(groups, "Stub")
	blank, _ := groupByName(groups, "Blank")
	old, _ := groupByName(groups, "OldPanel")
	if stub.ID == blank.ID || stub.ID == old.ID || blank.ID == old.ID {
		t.Fatalf("new slugs collided: %s %s %s", stub.ID, blank.ID, old.ID)
	}
	if _, ok := groupByID(groups, "flow_0001"); ok {
		t.Fatal("flow_0001 was stored")
	}
	if _, ok := groupByID(groups, "flow1"); ok {
		t.Fatal("flow1 was stored")
	}

	first, err := nextCampaignID(groups)
	if err != nil {
		t.Fatal(err)
	}
	second, err := nextCampaignID(append(groups, config.Group{ID: first}))
	if err != nil {
		t.Fatal(err)
	}
	assertCampaignSlug(t, first)
	assertCampaignSlug(t, second)
	if first == second {
		t.Fatalf("two copies share %s", first)
	}
	if _, ok := groupByID(groups, first); ok || first == "old-alias" {
		t.Fatalf("copy reused %s", first)
	}

	srv := groupServerWith(t, stored)
	c := newClient(t)
	if code, csrf := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("login %d", code)
	} else {
		body := append(append([]config.Group{}, stored...),
			config.Group{ID: "flow_0001", Name: "Stub"},
			config.Group{ID: "", Name: "Blank"},
			config.Group{ID: "kept-slug", Name: "Manual"},
		)
		resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("save %d %s", resp.StatusCode, raw)
		}
		got := fetchGroups(t, c, srv.URL)
		for _, id := range []string{"alloffers", "middleware", "849", "flow_0000", "kept-slug"} {
			if _, ok := groupByID(got, id); !ok {
				t.Fatalf("after save, %s is missing: %v", id, groupIDs(got))
			}
		}
		savedStub, ok := groupByName(got, "Stub")
		savedBlank, okBlank := groupByName(got, "Blank")
		if !ok || !okBlank {
			t.Fatalf("new campaigns missing: %v", groupIDs(got))
		}
		assertCampaignSlug(t, savedStub.ID)
		assertCampaignSlug(t, savedBlank.ID)
		if savedStub.ID == savedBlank.ID {
			t.Fatalf("saved slugs collided on %s", savedStub.ID)
		}
	}
}

// groupByName returns the campaign with the given name.
// groups is the list to scan. The bool is false when no row has that name.
func groupByName(groups []config.Group, name string) (config.Group, bool) {
	for _, g := range groups {
		if g.Name == name {
			return g, true
		}
	}
	return config.Group{}, false
}

// assertCampaignSlug fails the test when id is not a drawn campaign slug.
// id is the slug under test. It does not return an error.
func assertCampaignSlug(t *testing.T, id string) {
	t.Helper()
	if len(id) != campaignIDLen || placeholderCampaignID(id) {
		t.Fatalf("slug %q is not a drawn campaign id", id)
	}
	for _, r := range id {
		if !strings.ContainsRune(campaignIDAlphabet, r) {
			t.Fatalf("slug %q contains %q outside the campaign alphabet", id, r)
		}
	}
}

// groupServerWith returns an admin server whose campaign store starts as seed.
// seed is copied into the store. The server is closed when the test ends.
func groupServerWith(t *testing.T, seed []config.Group) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &memGroups{list: append([]config.Group{}, seed...)},
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}
