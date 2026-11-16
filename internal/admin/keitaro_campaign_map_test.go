package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/emtds/bridge/ktimport"
)

func TestRecordAcceptedImportManifestSkipsConflict(t *testing.T) {
	var m ktimport.Manifest
	incoming := []ktimport.Group{{ID: "promo", Name: "K", Num: 7}}
	saved := []config.Group{{ID: "promo", Name: "Local", Num: 1}}
	conflicts := map[string]bool{"promo": true}
	recordAcceptedImportManifest(&m, incoming, conflicts, saved, nil, campaignMapScope{board: config.MainBoardID})
	if m.Has(ktimport.KindGroups, "promo") || len(m.Campaigns) != 0 {
		t.Fatalf("manifest %+v", m)
	}
}

func TestResolveLogCampaignMapBackfill(t *testing.T) {
	s, _ := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, nil)
	manifest := ktimport.Manifest{Groups: []string{"promo"}}
	camps := []ktimport.Campaign{{ID: 7, Alias: "promo"}}
	store := []config.Group{{ID: "promo", Name: "P", Num: 7}}
	got, backfill, err := s.resolveLogCampaignMap(context.Background(), ktCreds{Base: "http://tracker.example", Key: "key"}, manifest, camps, store, campaignMapScope{board: config.MainBoardID})
	if err != nil || len(backfill) != 1 || backfill["7"] != "promo" || got[7] != "promo" {
		t.Fatalf("got=%v backfill=%v err=%v", got, backfill, err)
	}
}

func TestResolveLogCampaignMapKeepsStoredAlias(t *testing.T) {
	s, _ := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, nil)
	manifest := ktimport.Manifest{Campaigns: map[string]string{"7": "stable-id"}}
	camps := []ktimport.Campaign{{ID: 7, Alias: "renamed"}}
	got, backfill, err := s.resolveLogCampaignMap(context.Background(), ktCreds{}, manifest, camps, []config.Group{{ID: "stable-id", Num: 7}}, campaignMapScope{board: config.MainBoardID})
	if err != nil || len(backfill) != 0 || got[7] != "stable-id" {
		t.Fatalf("got=%v backfill=%v err=%v", got, backfill, err)
	}
}

func TestResolveLogCampaignMapDropsMissingStore(t *testing.T) {
	s, _ := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, nil)
	manifest := ktimport.Manifest{Campaigns: map[string]string{"7": "gone"}}
	got, backfill, err := s.resolveLogCampaignMap(context.Background(), ktCreds{}, manifest, nil, nil, campaignMapScope{board: config.MainBoardID})
	if err != nil || len(backfill) != 0 || len(got) != 0 {
		t.Fatalf("got=%v backfill=%v err=%v", got, backfill, err)
	}
}

func TestLegacyBackfillRejectsDupAliasTakeover(t *testing.T) {
	camps := []ktimport.Campaign{{ID: 11, Alias: "dup"}}
	groups := []config.Group{{ID: "dup-dup-11", Num: 11}}
	manifest := ktimport.Manifest{Groups: []string{"dup-dup-11"}}
	add, backfill := inferLegacyBackfill(manifest, groups, camps, map[int]string{}, campaignMapScope{board: config.MainBoardID})
	if len(add) != 0 || len(backfill) != 0 {
		t.Fatalf("add=%v backfill=%v", add, backfill)
	}
}

func TestLegacyBackfillRejectsShiftedNum(t *testing.T) {
	camps := []ktimport.Campaign{{ID: 7, Alias: "promo"}}
	groups := []config.Group{{ID: "promo", Num: 5}}
	manifest := ktimport.Manifest{Groups: []string{"promo"}}
	add, backfill := inferLegacyBackfill(manifest, groups, camps, map[int]string{}, campaignMapScope{board: config.MainBoardID})
	if len(add) != 0 || len(backfill) != 0 {
		t.Fatalf("add=%v backfill=%v", add, backfill)
	}
}

func TestImportGroupsManifestSaveBeforeGroups(t *testing.T) {
	groups := &memGroups{list: []config.Group{}}
	manifest := &recordingManifest{}
	s, _ := newKeitaroServers(t, "", "", groups, nil, nil)
	s.cfg.Manifest = manifest
	incoming := []ktimport.Group{{ID: "pad", Name: "Pad", Num: 3, Streams: []ktimport.Stream{{Name: "s", Status: true}}}}
	var run ktimport.Manifest
	manifest.fail = true
	_, _, err := s.importGroups(context.Background(), incoming, ktimport.Manifest{}, "", &run, "tracker.example", config.MainBoardID, manifest)
	if err == nil {
		t.Fatal("expected manifest error")
	}
	if len(groups.list) != 0 || manifest.saved {
		t.Fatalf("groups=%+v manifestSaved=%v", groups.list, manifest.saved)
	}
	manifest.fail = false
	_, _, err = s.importGroups(context.Background(), incoming, ktimport.Manifest{}, "", &run, "tracker.example", config.MainBoardID, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups.list) == 0 || !manifest.saved {
		t.Fatalf("groups=%+v manifestSaved=%v", groups.list, manifest.saved)
	}
}

func TestImportGroupsGroupsSaveFailManifestWritten(t *testing.T) {
	groups := &failGroupsStore{memGroups: memGroups{list: []config.Group{}}}
	manifest := &recordingManifest{}
	s, _ := newKeitaroServers(t, "", "", &memGroups{}, nil, nil)
	s.cfg.Groups = groups
	s.cfg.Manifest = manifest
	incoming := []ktimport.Group{{ID: "pad", Name: "Pad", Num: 3, Streams: []ktimport.Stream{{Name: "s", Status: true}}}}
	var run ktimport.Manifest
	_, _, err := s.importGroups(context.Background(), incoming, ktimport.Manifest{}, "", &run, "tracker.example", config.MainBoardID, manifest)
	if err == nil {
		t.Fatal("expected groups save error")
	}
	if !manifest.saved || !run.Has(ktimport.KindGroups, "pad") {
		t.Fatalf("manifestSaved=%v run=%+v", manifest.saved, run)
	}
}

func TestSaveCampaignMapBackfillPreservesCatalog(t *testing.T) {
	manifest := &recordingManifest{}
	s, _ := newKeitaroServers(t, "", "", &memGroups{}, nil, nil)
	s.cfg.Manifest = manifest
	merged := ktimport.Manifest{Offers: []string{"99"}, Groups: []string{"g1"}}
	if err := s.saveCampaignMapBackfill(manifest, merged, map[string]string{"7": "g1"}, "tracker.example"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(manifest.last.Offers, ","), "99") || manifest.last.Campaigns["7"] != "g1" {
		t.Fatalf("saved %+v", manifest.last)
	}
}

func TestImportGroupsCollisionLogsNumeric(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"promo","name":"Keitaro promo","state":"active"}]`))
		case "/admin_api/v1/campaigns/7/streams":
			_, _ = w.Write([]byte(`[{"id":1,"type":"regular","name":"main","state":"active","action_type":"blank"}]`))
		case "/admin_api/v1/clicks/log":
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"c1","datetime":"2026-10-01 00:00:00","campaign_id":7,"campaign":"Keitaro promo"}],"total":1}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	groups := &memGroups{list: []config.Group{{ID: "promo", Name: "Local promo"}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, sink, nil)
	postKeitaroImport(t, admin, map[string]any{
		"campaigns": true, "clicks": true, "from": "2026-10-01", "to": "2026-10-01",
	})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || len(sink.events) != 1 || sink.events[0].GroupID != "7" {
		t.Fatalf("job %+v event %+v", job, sink.events)
	}
	loaded, err := s.cfg.Manifest.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Campaigns != nil && loaded.Campaigns["7"] == "promo" {
		t.Fatalf("manifest linked conflict campaign: %+v", loaded.Campaigns)
	}
}

type recordingManifest struct {
	fail  bool
	saved bool
	last  ktimport.Manifest
}

func (r *recordingManifest) Load() (ktimport.Manifest, error) {
	return ktimport.Manifest{}, nil
}

func (r *recordingManifest) Save(m ktimport.Manifest) error {
	if r.fail {
		return errors.New("manifest write failed")
	}
	r.saved = true
	r.last = m
	return nil
}

type failGroupsStore struct {
	memGroups
}

func (f *failGroupsStore) Save(context.Context, []config.Group) error {
	return errors.New("groups write failed")
}
