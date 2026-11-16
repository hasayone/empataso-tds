package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/emtds/bridge/ktimport"
)

func TestMergeImportedGroupsKeepsBoardOnReimport(t *testing.T) {
	existing := []config.Group{{ID: "a", Name: "kt: A", Num: 3, BoardID: "seo"}}
	incoming := []ktimport.Group{{ID: "a", Name: "kt: A renamed"}}
	prev := ktimport.Manifest{}
	prev.Add(ktimport.KindGroups, "a")
	out, _, err := mergeImportedGroups(existing, incoming, prev, "", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].BoardID != "seo" {
		t.Fatalf("reimport changed board_id: %+v", out[0])
	}
}

func TestMergeImportedGroupsSetsBoardOnNew(t *testing.T) {
	incoming := []ktimport.Group{{ID: "b", Name: "kt: B"}}
	out, _, err := mergeImportedGroups(nil, incoming, ktimport.Manifest{}, "", "sales")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].BoardID != "sales" {
		t.Fatalf("new group board: %+v", out[0])
	}
}

func TestResolveLogCampaignMapIgnoresOtherBoardSameNum(t *testing.T) {
	s, _ := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, nil)
	manifest := ktimport.Manifest{Groups: []string{"seo-g", "main-g"}}
	camps := []ktimport.Campaign{{ID: 7, Alias: "seo-g"}}
	groups := []config.Group{
		{ID: "seo-g", Num: 7, BoardID: "seo"},
		{ID: "main-g", Num: 7, BoardID: "main"},
	}
	got, backfill, err := s.resolveLogCampaignMap(context.Background(), ktCreds{}, manifest, camps, groups, campaignMapScope{board: "seo"})
	if err != nil {
		t.Fatal(err)
	}
	if got[7] != "seo-g" || backfill["7"] != "seo-g" {
		t.Fatalf("got=%v backfill=%v", got, backfill)
	}
}

func TestImportGroupsHoldsGroupsMu(t *testing.T) {
	groups := &memGroups{}
	s, _ := newKeitaroServers(t, "", "", groups, nil, nil)
	s.groupsMu.Lock()
	done := make(chan struct{})
	go func() {
		_, _, _ = s.importGroups(context.Background(), []ktimport.Group{{
			ID: "x", Name: "kt: X", Num: 1, Streams: []ktimport.Stream{{Name: "s", Status: true}},
		}}, ktimport.Manifest{}, "", nil, "", config.MainBoardID, nil)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("import finished while groupsMu held")
	case <-time.After(200 * time.Millisecond):
	}
	s.groupsMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("import did not finish after unlock")
	}
}

func TestParseKeitaroFileSingleObject(t *testing.T) {
	raw := []byte(`{"base_url":"https://t.example/admin","api_key":"secret"}`)
	entries, list, err := parseKeitaroFile(raw)
	if err != nil || list || len(entries) != 1 || entries[0].BaseURL == "" {
		t.Fatalf("entries=%+v list=%v err=%v", entries, list, err)
	}
}

func TestReloadKeitaroConnsSingleObject(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keitaro.json")
	if err := os.WriteFile(file, []byte(`{"base_url":"https://t.example/admin","api_key":"k"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{KeitaroFile: file, Manifest: NewFileManifest(filepath.Join(dir, "keitaro-import.json"))}}
	if err := s.reloadKeitaroConns(); err != nil {
		t.Fatal(err)
	}
	s.ktMu.RLock()
	n := len(s.ktConns)
	list := s.ktFileList
	host := s.ktBase
	s.ktMu.RUnlock()
	if n != 1 || list || host != "https://t.example/admin" {
		t.Fatalf("conns=%d list=%v host=%q", n, list, host)
	}
}

func TestKeitaroSyncTargetsPerConnection(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keitaro.json")
	body := `[{"base_url":"https://a.example/admin","api_key":"k1","board_id":"seo"},{"base_url":"https://b.example/admin","api_key":"k2"}]`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{
		KeitaroFile:     file,
		KeitaroSyncFile: filepath.Join(dir, "keitaro-sync.json"),
		Manifest:        NewFileManifest(filepath.Join(dir, "keitaro-import.json")),
	}}
	if err := s.reloadKeitaroConns(); err != nil {
		t.Fatal(err)
	}
	targets := s.keitaroSyncTargets()
	if len(targets) != 2 {
		t.Fatalf("targets=%+v", targets)
	}
	if targets[0].syncPath == targets[1].syncPath || targets[0].board != "seo" || targets[0].allBoards {
		t.Fatalf("targets=%+v", targets)
	}
	if targets[1].syncPath != filepath.Join(dir, "keitaro-sync.json") || !targets[1].allBoards {
		t.Fatalf("global target: %+v", targets[1])
	}
}

func TestResolveLogCampaignMapGlobalAllBoards(t *testing.T) {
	s, _ := newKeitaroServers(t, "http://tracker.example", "key", nil, nil, nil)
	manifest := ktimport.Manifest{Campaigns: map[string]string{"7": "seo-g"}}
	groups := []config.Group{
		{ID: "seo-g", Num: 7, BoardID: "seo"},
		{ID: "main-g", Num: 7, BoardID: "main"},
	}
	got, _, err := s.resolveLogCampaignMap(context.Background(), ktCreds{}, manifest, nil, groups, campaignMapScope{allBoards: true})
	if err != nil {
		t.Fatal(err)
	}
	if got[7] != "seo-g" {
		t.Fatalf("got=%v", got)
	}
	seoOnly, _, _ := s.resolveLogCampaignMap(context.Background(), ktCreds{}, manifest, nil, groups, campaignMapScope{board: "seo"})
	if seoOnly[7] != "seo-g" {
		t.Fatalf("seo scope got=%v", seoOnly)
	}
	mainOnly, _, _ := s.resolveLogCampaignMap(context.Background(), ktCreds{}, manifest, nil, groups, campaignMapScope{board: "main"})
	if len(mainOnly) != 0 {
		t.Fatalf("main scope must not see seo-g: %v", mainOnly)
	}
}

func TestKeitaroSyncPassMapsBoardCampaignNum(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-2 * time.Hour)
	dir := t.TempDir()
	groups := &memGroups{list: []config.Group{
		{ID: "seo-g", Num: 7, BoardID: "seo"},
		{ID: "main-g", Num: 7, BoardID: "main"},
	}}
	sink := &fakeSink{}
	s, _ := newKeitaroServers(t, "http://tracker.example", "key", groups, sink, nil)
	s.cfg.KeitaroSyncFile = filepath.Join(dir, "keitaro-sync-seo.json")
	s.syncNow = func() time.Time { return now }
	seoManifest := NewFileManifest(filepath.Join(dir, "keitaro-import-seo.json"))
	if err := seoManifest.Save(ktimport.Manifest{Host: "tracker.example", Campaigns: map[string]string{"7": "seo-g"}}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"c1","datetime":"` + ts.Format(keitaroTimeLayout) + `","campaign_id":7}],"total":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"rows":[],"total":0}`))
	}))
	t.Cleanup(fake.Close)
	host := keitaroHost(fake.URL)
	if err := seoManifest.Save(ktimport.Manifest{Host: host, Campaigns: map[string]string{"7": "seo-g"}}); err != nil {
		t.Fatal(err)
	}
	s.ktMu.Lock()
	s.ktBase = fake.URL
	s.ktMu.Unlock()
	st := keitaroSyncFile{Host: host, ClicksTo: ts, ConversionsTo: ts}
	target := keitaroSyncTarget{
		syncPath: s.cfg.KeitaroSyncFile, board: "seo", creds: ktCreds{Base: fake.URL, Key: "key"},
		manifestStore: seoManifest,
	}
	res := s.runKeitaroSyncPass(context.Background(), target, &st, true)
	if !res.OK || res.Clicks != 1 || len(sink.events) != 1 || sink.events[0].GroupID != "seo-g" {
		t.Fatalf("res=%+v events=%+v", res, sink.events)
	}
}
