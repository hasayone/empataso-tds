package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapHasNoKtPrefix(t *testing.T) {
	res := MapAll(loadFixture(t))
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "\"kt-") || strings.Contains(text, "kt: ") {
		t.Fatal("map output still has an import prefix")
	}
}

func TestManifestSecondMergeKeepsLocal(t *testing.T) {
	res := MapAll(loadFixture(t))
	if _, ok := findGroup(res.Groups, "offer-es"); !ok {
		t.Fatal("offer-es group missing from map output")
	}
	if res.Manifest.Has(KindGroups, "offer-es") || res.Manifest.Empty() || !res.Manifest.Has(KindOffers, "101") || !res.Manifest.Has(KindLandings, "701") {
		t.Fatalf("%+v", res.Manifest)
	}
	for _, g := range res.Groups {
		res.Manifest.Add(KindGroups, g.ID)
	}
	imported := res.Manifest.Imported(KindGroups)
	merged, losses := Merge(nil, res.Groups, imported)
	if len(losses) != 0 {
		t.Fatal(losses)
	}
	changed := append([]Group(nil), res.Groups...)
	for i := range changed {
		if changed[i].ID == "offer-es" {
			changed[i].Name = "Offer ES 2"
		}
	}
	replaced, losses := Merge(merged, changed, imported)
	promo, ok := findGroup(replaced, "offer-es")
	if !ok || promo.Name != "Offer ES 2" || promo.Num != 1 || len(losses) != 0 {
		t.Fatalf("replaced %+v losses %v", promo, losses)
	}

	local := CatOffer{ID: "796", Name: "local", URL: "https://local.example", Status: true}
	incoming := append(append([]CatOffer{}, res.Offers...), CatOffer{ID: "796", Name: "from-import", URL: "https://new.example", Status: true})
	existing := []CatOffer{{ID: "101", Name: "old", URL: "https://old.example", Status: true}, local}
	got, offerLosses := MergeByIDReport(existing, incoming, func(o CatOffer) string { return o.ID }, res.Manifest.Imported(KindOffers))
	var kept CatOffer
	for _, o := range got {
		if o.ID == "796" {
			kept = o
		}
		if o.ID == "101" && o.Name != "Main" {
			t.Fatalf("101 not replaced: %+v", o)
		}
	}
	if kept.Name != "local" || kept.URL != "https://local.example" {
		t.Fatalf("local overwritten %+v", kept)
	}
	if len(offerLosses) != 1 || offerLosses[0].Kind != "conflict" || offerLosses[0].Ref != "796" || offerLosses[0].Detail != "id занят локальной записью" {
		t.Fatalf("%+v", offerLosses)
	}
}

func TestNumCollisionWithLocalGroup(t *testing.T) {
	existing := []Group{{ID: "local", Name: "Local", Num: 3}}
	incoming := []Group{{ID: "camp", Name: "Camp", Num: 3}, {ID: "other", Name: "Other", Num: 4}}
	imported := func(id string) bool { return id == "camp" || id == "other" }
	got, losses := Merge(existing, incoming, imported)
	if len(got) != 3 || got[0].Num != 3 || got[1].ID != "camp" || got[1].Num != 5 || got[2].Num != 4 {
		t.Fatalf("%+v", got)
	}
	if len(losses) != 1 || losses[0].Kind != "approximated" || losses[0].Ref != "camp" || losses[0].Detail != "номер 3 занят, выдан 5" {
		t.Fatalf("%+v", losses)
	}
}

func TestManifestAddMerge(t *testing.T) {
	var m Manifest
	if !m.Empty() {
		t.Fatal("zero manifest is not empty")
	}
	m.Add(KindOffers, "2", "1", "2", "")
	if strings.Join(m.Offers, ",") != "2,1" || !m.Has(KindOffers, "1") || m.Has(KindOffers, "9") {
		t.Fatalf("%+v", m.Offers)
	}
	check := m.Imported(KindOffers)
	m.Add(KindOffers, "9")
	if !check("1") || check("9") || !m.Imported(KindOffers)("9") {
		t.Fatal("closure saw a later add")
	}
	other := Manifest{Host: "tracker.example", Offers: []string{"1", "3"}}
	got := m.Merge(other)
	if got.Host != "tracker.example" || strings.Join(got.Offers, ",") != "2,1,9,3" {
		t.Fatalf("%+v", got)
	}
	back := other.Merge(Manifest{Offers: []string{"4"}})
	if back.Host != "tracker.example" || strings.Join(back.Offers, ",") != "1,3,4" {
		t.Fatalf("%+v", back)
	}
	if (Manifest{Host: "only"}).Empty() {
		t.Fatal("host-only manifest is empty")
	}
	var m2 Manifest
	m2.Add(KindUsers, "42", "42")
	if !m2.Has(KindUsers, "42") {
		t.Fatal("users manifest")
	}
	res := Result{Users: []CatUser{{ID: "7", Login: "u"}}}
	res = attachManifest(res)
	if !res.Manifest.Has(KindUsers, "7") {
		t.Fatalf("%+v", res.Manifest)
	}
}

func TestManifestMergeCampaigns(t *testing.T) {
	base := Manifest{Campaigns: map[string]string{"7": "old", "9": "nine"}}
	other := Manifest{Campaigns: map[string]string{"7": "new", "10": "ten"}}
	got := base.Merge(other)
	if got.Campaigns["7"] != "new" || got.Campaigns["9"] != "nine" || got.Campaigns["10"] != "ten" {
		t.Fatalf("%+v", got.Campaigns)
	}
	var m Manifest
	m.AddCampaignsIfAbsent(map[string]string{"1": "a", "2": "b"})
	m.AddCampaignsIfAbsent(map[string]string{"1": "z", "3": "c"})
	if m.Campaigns["1"] != "a" || m.Campaigns["3"] != "c" {
		t.Fatalf("%+v", m.Campaigns)
	}
	aliases := Manifest{Campaigns: map[string]string{"7": "x", "bad": "y", "0": "z"}}.LogCampaignAliases()
	if len(aliases) != 1 || aliases[7] != "x" {
		t.Fatalf("%+v", aliases)
	}
	if (Manifest{Campaigns: map[string]string{"1": "a"}}).Empty() {
		t.Fatal("campaign map is not empty manifest")
	}
	got = Manifest{Campaigns: map[string]string{"7": "promo", "9": "nine"}}.Merge(Manifest{Campaigns: map[string]string{"8": "promo"}})
	if len(got.Campaigns) != 2 || got.Campaigns["8"] != "promo" || got.Campaigns["9"] != "nine" || got.Campaigns["7"] != "" {
		t.Fatalf("gid reuse keeps other cids %+v", got.Campaigns)
	}
	got = Manifest{Campaigns: map[string]string{"7": "promo"}}.Merge(Manifest{Campaigns: map[string]string{"8": "promo"}})
	if len(got.Campaigns) != 1 || got.Campaigns["8"] != "promo" || got.Campaigns["7"] != "" {
		t.Fatalf("gid reuse %+v", got.Campaigns)
	}
	var backfill Manifest
	backfill.AddCampaignsIfAbsent(map[string]string{"7": "a"})
	backfill.AddCampaignsIfAbsent(map[string]string{"8": "a", "9": "b"})
	if backfill.Campaigns["8"] != "" || backfill.Campaigns["9"] != "b" {
		t.Fatalf("gid taken %+v", backfill.Campaigns)
	}
	dup := Manifest{Campaigns: map[string]string{"7": "promo", "8": "promo"}}
	if aliases := dup.LogCampaignAliases(); len(aliases) != 0 {
		t.Fatalf("ambiguous gid aliases %+v", aliases)
	}
}
