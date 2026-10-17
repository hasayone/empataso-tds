package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapUsersFixture(t *testing.T) {
	snap := Snapshot{
		Campaigns: []Campaign{{ID: 7, Alias: "promo", Name: "Spring"}},
		Groups:    []KGroup{{ID: 1, Name: "CampGrp", Type: "campaigns"}},
		Users: []KUser{{
			ID: 2, Login: "buyer", Type: "USER",
			AccessData: json.RawMessage(`{
				"resources":["campaigns","offers","archive","streams"],
				"campaigns_access_type":"to_groups_and_selected",
				"campaigns_selected_entities":[7,999],
				"campaigns_selected_groups":[1],
				"offers_access_type":"read_only",
				"streams_access_type":"full_access",
				"reports":["profit","cost","weird"]
			}`),
			Preferences: KUserPrefs{Language: "ru", Timezone: "Europe/Moscow"},
		}},
	}
	res := Map(snap, Selection{Campaigns: true, Offers: true, Users: true})
	if len(res.Users) != 1 || res.Users[0].Login != "buyer" {
		t.Fatalf("users %+v", res.Users)
	}
	u := res.Users[0]
	gotSec := strings.Join(u.Sections, ",")
	if !strings.Contains(gotSec, "flows") || !strings.Contains(gotSec, "offers") || !strings.Contains(gotSec, "lists") {
		t.Fatalf("sections %q", gotSec)
	}
	if g := u.Grants["campaigns"]; g.Mode != "selected" || len(g.IDs) != 1 || g.IDs[0] != "promo" {
		t.Fatalf("campaign grant %+v", g)
	}
	if g := u.Grants["offers"]; g.Mode != "read" {
		t.Fatalf("offers grant %+v", g)
	}
	for _, key := range []string{"landings", "sources", "networks", "domains"} {
		if u.Grants[key].Mode != "full" {
			t.Fatalf("grant %s = %+v want full", key, u.Grants[key])
		}
	}
	if len(u.Hidden) != 2 || u.Lang != "ru" {
		t.Fatalf("hidden/lang %+v %q", u.Hidden, u.Lang)
	}
	text := lossesText(res.Losses)
	for _, frag := range []string{
		"archive", "streams access", "campaigns 999", "метрика weird",
	} {
		if !strings.Contains(text, frag) {
			t.Fatalf("missing %q in %s", frag, text)
		}
	}
}

func TestMapUsersAdminAndDenied(t *testing.T) {
	snap := Snapshot{
		UsersDenied: true,
		Users:       []KUser{{ID: 1, Login: "x", Type: "USER"}},
	}
	res := Map(snap, Selection{Users: true})
	if len(res.Users) != 0 {
		t.Fatal(res.Users)
	}
	if len(res.Losses) != 1 || res.Losses[0].Ref != "users" {
		t.Fatalf("%+v", res.Losses)
	}
	snap = Snapshot{Users: []KUser{{ID: 3, Login: "root", Type: "ADMIN"}}}
	res = Map(snap, Selection{Users: true})
	if len(res.Users) != 1 || res.Users[0].Type != "ADMIN" || len(res.Users[0].Sections) != 0 {
		t.Fatalf("%+v", res.Users[0])
	}
}

func TestMapUsersKnownCampaign(t *testing.T) {
	snap := Snapshot{
		Campaigns: []Campaign{{ID: 5, Alias: "a", Name: "A"}},
		Users: []KUser{{
			ID: 1, Login: "u", Type: "USER",
			AccessData: json.RawMessage(`{
				"resources":["campaigns"],
				"campaigns_access_type":"to_groups_and_selected",
				"campaigns_selected_entities":[5]
			}`),
		}},
	}
	prev := Manifest{}
	prev.Add(KindGroups, "a")
	res := Map(snap, Selection{Users: true, Known: prev.Has})
	if g := res.Users[0].Grants["campaigns"]; len(g.IDs) != 1 || g.IDs[0] != "a" {
		t.Fatalf("%+v", g)
	}
}
