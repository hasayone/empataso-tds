package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

func TestImportCatalogNotesSeq(t *testing.T) {
	dir := t.TempDir()
	s, _ := newKeitaroServers(t, "http://tracker.example", "fixture-key", nil, nil, &keitaroFiles{dir: dir})
	s.cfg.SeqOffers = NewIDSeq(filepath.Join(dir, "offer_seq.json"))
	res := ktimport.Result{
		Offers: []ktimport.CatOffer{{ID: "796", Name: "A", URL: "https://a.example/x", Status: true}},
	}
	res.Manifest.Add(ktimport.KindOffers, "796")
	st := &importState{
		counts: map[string]int{},
		losses: []ktimport.Loss{},
		prev:   ktimport.Manifest{},
		res:    res,
	}
	if err := s.importCatalogs(context.Background(), keitaroImportReq{Offers: true}, st); err != nil {
		t.Fatal(err)
	}
	if st.counts["offers"] != 1 {
		t.Fatalf("counts %+v", st.counts)
	}
	s.cfg.SeqOffers.mu.Lock()
	n, err := s.cfg.SeqOffers.take(1)
	s.cfg.SeqOffers.mu.Unlock()
	if err != nil || n != 797 {
		t.Fatalf("seq %d %v", n, err)
	}
}

func TestMergeCatalogKeepsManualGroup(t *testing.T) {
	ctx := context.Background()
	netStore := NewJSONStore[config.AffiliateNetwork](filepath.Join(t.TempDir(), "networks.json"))
	local := config.AffiliateNetwork{
		ID: "3", Name: "Local", Status: true, PostbackURL: "https://pb.example/", Group: "manual",
	}
	if err := netStore.Save(ctx, []config.AffiliateNetwork{local}); err != nil {
		t.Fatal(err)
	}
	netID := func(v config.AffiliateNetwork) string { return v.ID }
	prev := ktimport.Manifest{}
	prev.Add(ktimport.KindNetworks, "3")
	incoming := []ktimport.CatNetwork{{ID: "3", Name: "Remote", Status: true, PostbackURL: "https://pb.example/"}}
	if _, _, err := mergeCatalog(ctx, incoming, catalogMerge[config.AffiliateNetwork]{
		store: netStore, idOf: netID, valid: validNetwork, imported: prev.Imported(ktimport.KindNetworks),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := netStore.List(ctx)
	if err != nil || len(got) != 1 || got[0].Name != "Remote" || got[0].Group != "manual" {
		t.Fatalf("network merge = %+v %v", got, err)
	}

	offerStore := NewJSONStore[config.Offer](filepath.Join(t.TempDir(), "offers.json"))
	offerLocal := config.Offer{ID: "9", Name: "Mine", URL: "https://mine.example/a", Status: true, Group: "manual"}
	if err := offerStore.Save(ctx, []config.Offer{offerLocal}); err != nil {
		t.Fatal(err)
	}
	offerID := func(v config.Offer) string { return v.ID }
	prev = ktimport.Manifest{}
	prev.Add(ktimport.KindOffers, "9")
	withGroup := []ktimport.CatOffer{{
		ID: "9", Name: "Remote", URL: "https://offer.example/a", Status: true, Group: "tracker",
	}}
	if _, _, err := mergeCatalog(ctx, withGroup, catalogMerge[config.Offer]{
		store: offerStore, idOf: offerID, valid: validOffer, imported: prev.Imported(ktimport.KindOffers),
	}); err != nil {
		t.Fatal(err)
	}
	offers, err := offerStore.List(ctx)
	if err != nil || len(offers) != 1 || offers[0].Group != "tracker" || offers[0].Name != "Remote" {
		t.Fatalf("offer group replace = %+v %v", offers, err)
	}
}

func TestLocalOfferConflictThenReimport(t *testing.T) {
	ctx := context.Background()
	store := NewJSONStore[config.Offer](filepath.Join(t.TempDir(), "offers.json"))
	local := config.Offer{ID: "796", Name: "Mine", URL: "https://mine.example/a", Status: true}
	if err := store.Save(ctx, []config.Offer{local}); err != nil {
		t.Fatal(err)
	}
	incoming := []ktimport.CatOffer{{
		ID: "796", Name: "Remote", URL: "https://offer.example/a", Status: true,
	}}
	idOf := func(v config.Offer) string { return v.ID }
	_, losses, err := mergeCatalog(ctx, incoming, catalogMerge[config.Offer]{
		store: store, idOf: idOf, valid: validOffer, imported: (ktimport.Manifest{}).Imported(ktimport.KindOffers),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !lossHas(losses, ktimport.Loss{Kind: "conflict", Ref: "796", Detail: "id занят локальной записью"}) {
		t.Fatalf("losses %+v", losses)
	}
	got, err := store.List(ctx)
	if err != nil || len(got) != 1 || got[0].Name != "Mine" || got[0].URL != local.URL {
		t.Fatalf("kept %+v %v", got, err)
	}
	prev := ktimport.Manifest{}
	prev.Add(ktimport.KindOffers, "796")
	_, losses, err = mergeCatalog(ctx, incoming, catalogMerge[config.Offer]{
		store: store, idOf: idOf, valid: validOffer, imported: prev.Imported(ktimport.KindOffers),
	})
	if err != nil {
		t.Fatal(err)
	}
	if lossHas(losses, ktimport.Loss{Kind: "conflict", Ref: "796", Detail: "id занят локальной записью"}) {
		t.Fatalf("reimport conflict %+v", losses)
	}
	got, err = store.List(ctx)
	if err != nil || len(got) != 1 || got[0].Name != "Remote" || got[0].URL != "https://offer.example/a" {
		t.Fatalf("replaced %+v %v", got, err)
	}
}

func TestPurgeEmptyManifest(t *testing.T) {
	dir := t.TempDir()
	lists := NewFileLists(dir)
	if err := lists.Write("ip_test.dat", "9.9.9.9\n"); err != nil {
		t.Fatal(err)
	}
	pages := NewFilePages(filepath.Join(dir, "pages"))
	if err := pages.Write("keep.html", "x"); err != nil {
		t.Fatal(err)
	}
	landings := NewFileLandings(filepath.Join(dir, "landings"))
	if err := landings.Write("701", "index.php", []byte("<?php")); err != nil {
		t.Fatal(err)
	}
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local", Status: true}}}
	sink := &fakeSink{eventsN: 4, convN: 5}
	s, admin := newKeitaroServers(t, "http://tracker.example", "fixture-key", groups, sink, &keitaroFiles{dir: dir})
	s.cfg.Lists = lists
	s.cfg.Pages = pages
	s.cfg.LandingFiles = landings
	if err := s.cfg.Offers.Save(context.Background(), []config.Offer{{
		ID: "796", Name: "Mine", URL: "https://mine.example/a", Status: true,
	}}); err != nil {
		t.Fatal(err)
	}
	raw, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge %d %s", code, raw)
	}
	var body struct {
		Removed keitaroRemoved `json:"removed"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Removed != (keitaroRemoved{}) || len(sink.purged) != 0 || len(groups.list) != 1 {
		t.Fatalf("removed %+v sink %+v groups %+v", body.Removed, sink.purged, groups.list)
	}
	if kept, err := lists.Read("ip_test.dat"); err != nil || kept != "9.9.9.9\n" {
		t.Fatalf("list %q %v", kept, err)
	}
	if kept, err := pages.Read("keep.html"); err != nil || kept != "x" {
		t.Fatalf("page %q %v", kept, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "landings", "701", "index.php")); err != nil {
		t.Fatal(err)
	}
	offers := readOffers(t, filepath.Join(dir, "offers.json"))
	if len(offers) != 1 || offers[0].ID != "796" {
		t.Fatalf("offers %+v", offers)
	}
	if _, err := os.Stat(filepath.Join(dir, "keitaro-import.json")); !os.IsNotExist(err) {
		t.Fatal("empty purge wrote a manifest")
	}
}

func TestPurgeRemovesManifestLandings(t *testing.T) {
	dir := t.TempDir()
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local"}}}
	sink := &fakeSink{}
	s, admin := newKeitaroServers(t, "http://other.example", "fixture-key", groups, sink, &keitaroFiles{dir: dir})
	files := NewFileLandings(filepath.Join(dir, "site-landings"))
	s.cfg.LandingFiles = files
	if err := files.Write("701", "index.php", []byte("<?php")); err != nil {
		t.Fatal(err)
	}
	if err := files.Write("702", "index.html", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: "tracker.example", Landings: []string{"701"}}); err != nil {
		t.Fatal(err)
	}
	raw, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge %d %s", code, raw)
	}
	if len(sink.purged) != 1 || sink.purged[0] != "tracker.example" {
		t.Fatalf("host %+v", sink.purged)
	}
	if _, err := os.Stat(filepath.Join(dir, "site-landings", "701")); !os.IsNotExist(err) {
		t.Fatal("imported landing dir remains")
	}
	body, err := files.Read("702", "index.html")
	if err != nil || string(body) != "keep" {
		t.Fatalf("other landing %q %v", body, err)
	}
	if len(groups.list) != 1 {
		t.Fatalf("groups %+v", groups.list)
	}
	loaded, err := s.cfg.Manifest.Load()
	if err != nil || !loaded.Empty() {
		t.Fatalf("manifest %+v %v", loaded, err)
	}
}
