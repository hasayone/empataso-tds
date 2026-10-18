package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/kuztds/bridge/ktimport"
)

// shareGroup builds a campaign with one stream whose offer list holds the given shares.
func shareGroup(id string, shares ...int) config.Group {
	refs := make([]config.ShareRef, len(shares))
	for i, n := range shares {
		refs[i] = config.ShareRef{ID: "o" + string(rune('1'+i)), Share: n}
	}
	return config.Group{ID: id, Name: id, Streams: []config.Stream{{Name: "s", Out: config.Output{Offers: refs}}}}
}

// sharesServer starts an admin server over a group store seeded with seed.
func sharesServer(t *testing.T, seed []config.Group) (*httptest.Server, *memGroups) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	store := &memGroups{list: seed}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Stats: fakeStats{}, Groups: store, Limiter: allowAll{},
		SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, store
}

func TestShareSumAndExceed(t *testing.T) {
	cases := []struct {
		name   string
		shares []int
		want   bool
	}{
		{"exactly 100", []int{60, 40}, false},
		{"below 100", []int{30, 20}, false},
		{"above 100", []int{60, 41}, true},
		{"negative is not counted", []int{101, -5}, true},
		{"empty", nil, false},
	}
	for _, c := range cases {
		if got := sharesExceed(shareGroup("g", c.shares...)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	land := config.Group{ID: "l", Streams: []config.Stream{{Out: config.Output{
		Landings: []config.ShareRef{{ID: "a", Share: 70}, {ID: "b", Share: 70}},
	}}}}
	if !sharesExceed(land) {
		t.Error("landing list above 100 must exceed")
	}
	split := shareGroup("s", 80)
	split.Streams[0].Out.Landings = []config.ShareRef{{ID: "a", Share: 80}}
	if sharesExceed(split) {
		t.Error("offer and landing lists are summed separately")
	}
}

func TestGroupPutSharesOnlyForChanged(t *testing.T) {
	legacy := shareGroup("legacy", 70, 70)
	srv, _ := sharesServer(t, []config.Group{legacy, shareGroup("other", 50)})
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	put := func(g config.Group) (int, string) {
		t.Helper()
		resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/"+g.ID, csrf, g)
		defer resp.Body.Close()
		var er struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&er)
		return resp.StatusCode, er.Error
	}

	other := shareGroup("other", 50)
	other.Name = "renamed"
	if code, msg := put(other); code != http.StatusOK {
		t.Fatalf("other beside a legacy campaign: %d %q", code, msg)
	}
	if code, msg := put(shareGroup("other", 60, 40)); code != http.StatusOK {
		t.Fatalf("exactly 100: %d %q", code, msg)
	}
	if code, msg := put(shareGroup("other", 60, 41)); code != http.StatusBadRequest || msg != "shares exceed 100" {
		t.Fatalf("changed above 100: %d %q", code, msg)
	}
	if code, msg := put(shareGroup("fresh", 90, 20)); code != http.StatusBadRequest || msg != "shares exceed 100" {
		t.Fatalf("new above 100: %d %q", code, msg)
	}
	legacy.Name = "touched"
	if code, msg := put(legacy); code != http.StatusBadRequest || msg != "shares exceed 100" {
		t.Fatalf("changed legacy above 100: %d %q", code, msg)
	}
	fixed := shareGroup("legacy", 70, 30)
	if code, msg := put(fixed); code != http.StatusOK {
		t.Fatalf("legacy fixed to 100: %d %q", code, msg)
	}
}

func TestGroupsBulkSaveKeepsUntouchedLegacy(t *testing.T) {
	srv, store := sharesServer(t, []config.Group{shareGroup("legacy", 70, 70), shareGroup("b", 10)})
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	b := shareGroup("b", 10)
	b.Name = "b2"
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{shareGroup("legacy", 70, 70), b})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bulk save %d", resp.StatusCode)
	}
	if got, _ := groupByID(store.list, "b"); got.Name != "b2" {
		t.Fatalf("b not saved: %+v", got)
	}
}

func TestGroupsBulkSaveRejectsChangedOverHundred(t *testing.T) {
	seed := []config.Group{shareGroup("legacy", 70, 70), shareGroup("b", 10)}
	srv, store := sharesServer(t, seed)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{
		shareGroup("legacy", 70, 70), shareGroup("b", 60, 41),
	})
	defer resp.Body.Close()
	var er struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	if resp.StatusCode != http.StatusBadRequest || er.Error != "shares exceed 100" {
		t.Fatalf("bulk %d %q", resp.StatusCode, er.Error)
	}
	if len(store.list) != 2 || shareSum(store.list[1].Streams[0].Out.Offers) != 10 {
		t.Fatalf("store changed: %+v", store.list)
	}
}

func TestGroupsDuplicateIDBeforeShares(t *testing.T) {
	srv, _ := sharesServer(t, nil)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, []config.Group{
		shareGroup("a", 90, 90), shareGroup("a", 1),
	})
	defer resp.Body.Close()
	var er struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	if resp.StatusCode != http.StatusBadRequest || er.Error != "duplicate group id: a" {
		t.Fatalf("duplicate %d %q", resp.StatusCode, er.Error)
	}
}

func TestGroupPutSharesSelectedUserBesideForeignLegacy(t *testing.T) {
	srv, groups, s, _ := ownerServer(t)
	groups.list = []config.Group{shareGroup("legacy", 70, 70), shareGroup("b", 10)}
	mustUser(t, s, "sel", "secret1", security.Access{
		Sections: userSectionsAll(),
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeSelected, IDs: []string{"b"}},
		},
	})
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "sel", "secret1")
	b := shareGroup("b", 10)
	b.Name = "b2"
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/b", csrf, b)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("selected user save %d", resp.StatusCode)
	}
	if got, _ := groupByID(groups.list, "b"); got.Name != "b2" {
		t.Fatalf("b not saved: %+v", got)
	}
	if got, _ := groupByID(groups.list, "legacy"); shareSum(got.Streams[0].Out.Offers) != 140 {
		t.Fatalf("legacy changed: %+v", got)
	}
	over := shareGroup("b", 60, 41)
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/b", csrf, over)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("selected user over 100: %d", resp.StatusCode)
	}
}

func TestImportGroupsReportsSharesAboveHundred(t *testing.T) {
	groups := &memGroups{}
	s, _ := newKeitaroServers(t, "", "", groups, nil, nil)
	n, losses, err := s.importGroups(context.Background(), []ktimport.Group{{
		ID: "kt-over", Name: "kt: Over", Streams: []ktimport.Stream{{
			Name: "kt-1", Status: true,
			Out: ktimport.Output{Offers: []ktimport.ShareRef{{ID: "a", Share: 80}, {ID: "b", Share: 50}}},
		}},
	}, {
		ID: "kt-ok", Name: "kt: Ok", Streams: []ktimport.Stream{{
			Name: "kt-1", Status: true,
			Out: ktimport.Output{Offers: []ktimport.ShareRef{{ID: "a", Share: 60}, {ID: "b", Share: 40}}},
		}},
	}}, ktimport.Manifest{}, "")
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, ok := savedGroup(groups.list, "kt-over"); !ok {
		t.Fatal("group above 100 must still be saved")
	}
	if !lossHas(losses, ktimport.Loss{Kind: "approximated", Ref: "kt-over", Detail: "shares exceed 100"}) {
		t.Fatalf("losses %+v", losses)
	}
	if lossHas(losses, ktimport.Loss{Kind: "approximated", Ref: "kt-ok", Detail: "shares exceed 100"}) {
		t.Fatalf("group at 100 must not be reported: %+v", losses)
	}
}
