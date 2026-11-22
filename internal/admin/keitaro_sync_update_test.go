package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// refreshStreams are two tracker streams of campaign 7 that both flow to offer 12.
const refreshStreams = `[
	{"id":21,"type":"regular","position":1,"name":"Flow A","state":"active","action_type":"http","offers":[{"offer_id":12,"share":100,"state":"active"}]},
	{"id":22,"type":"regular","position":2,"name":"Flow B","state":"active","action_type":"http","offers":[{"offer_id":12,"share":100,"state":"active"}]}]`

// refreshLocalGroups returns the stored campaigns of the refresh tests: "renamed" is the imported
// campaign 7 whose id was changed here, "local7" a local campaign that only shares its number,
// "plain" a campaign that never came from Keitaro and "orphan" an imported campaign 8 that Keitaro lacks.
func refreshLocalGroups() []config.Group {
	return []config.Group{
		{ID: "renamed", Num: 7, Name: "Old", Status: true, Redirect: "show_text", Folder: "mine", Aliases: []string{"x"},
			UniqBy: "ip_ua", UniqCookie: true, UniqSeconds: 7200, Notes: "",
			Streams: []config.Stream{{Name: "mine-flow", Status: true, Comment: "keitaro_stream_id=21", Bots: config.Bots{CheckUA: true},
				Out: config.Output{Redirect: "http_redirect", Out: "https://old.example", Chance: 100}}}},
		{ID: "local7", Num: 7, Name: "Local seven", Status: true, Redirect: "stop", Notes: "mine"},
		{ID: "plain", Num: 99, Name: "Plain", Status: true, Redirect: "stop", Notes: "keep me"},
		{ID: "orphan", Num: 8, Name: "Orphan", Status: true, Redirect: "stop",
			Streams: []config.Stream{{Name: "o", Status: true, Comment: "keitaro_stream_id=81", Out: config.Output{Redirect: "stop", Chance: 100}}}},
	}
}

// refreshServer returns an admin wired to tracker with the groups of refreshLocalGroups, a manifest
// that lists campaign 7 under its old id "test" and campaign 8 as "orphan", and a local offer 11.
// The sync cursors are one minute behind the lag, so no bootstrap runs. now is the sync clock.
func refreshServer(t *testing.T, tracker *httptest.Server, now *time.Time) (*Server, *memGroups, *keitaroSyncFile) {
	t.Helper()
	groups := &memGroups{list: refreshLocalGroups()}
	s, _ := newKeitaroServers(t, tracker.URL, "k-test", groups, &fakeSink{}, &keitaroFiles{dir: t.TempDir()})
	s.syncNow = func() time.Time { return *now }
	if err := s.cfg.Offers.Save(context.Background(), []config.Offer{{ID: "11", Name: "Mine", URL: "https://offer.example/a", Status: true}}); err != nil {
		t.Fatal(err)
	}
	host := keitaroHost(tracker.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Groups: []string{"test", "orphan"},
		Campaigns: map[string]string{"7": "test", "8": "orphan"}, Offers: []string{"11"}}); err != nil {
		t.Fatal(err)
	}
	cursor := now.Add(-keitaroSyncLag - time.Minute)
	return s, groups, &keitaroSyncFile{Host: host, ClicksTo: cursor, ConversionsTo: cursor}
}

// TestKeitaroSyncRefreshesImportedCampaign checks that a campaign renamed here still gets the name,
// the note, the streams and the links of the tracker, found by its Keitaro id, while uniqueness and
// the other local fields, campaigns outside Keitaro and a campaign missing in Keitaro stay as they are.
func TestKeitaroSyncRefreshesImportedCampaign(t *testing.T) {
	fake := &newCampaignsTracker{t: t,
		camps:   `[{"id":7,"alias":"test","name":"Renamed in Keitaro","notes":"call before launch","state":"active","cookies_ttl":48}]`,
		streams: map[string]string{"7": refreshStreams},
	}
	tracker := fake.server()
	now := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	s, groups, st := refreshServer(t, tracker, &now)
	plainBefore, _ := groupByID(groups.list, "plain")
	local7Before, _ := groupByID(groups.list, "local7")
	orphanBefore, _ := groupByID(groups.list, "orphan")
	clicksBefore := st.ClicksTo

	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), st, true)
	if !res.OK || res.Updated != 1 || res.Campaigns != 0 {
		t.Fatalf("pass = %+v", res)
	}
	got, ok := groupByID(groups.list, "renamed")
	if !ok || got.Name != "Renamed in Keitaro" || got.Notes != "call before launch" {
		t.Fatalf("renamed = %+v", got)
	}
	if got.UniqBy != "ip_ua" || !got.UniqCookie || got.UniqSeconds != 7200 || got.Folder != "mine" || !reflect.DeepEqual(got.Aliases, []string{"x"}) {
		t.Fatalf("local fields changed: %+v", got)
	}
	if len(got.Streams) != 2 {
		t.Fatalf("streams = %+v", got.Streams)
	}
	a, b := got.Streams[0], got.Streams[1]
	if a.Name != "mine-flow" || a.Out.Out != "https://offer.example/b" || !reflect.DeepEqual(a.Out.Offers, []config.ShareRef{{ID: "12", Share: 100}}) || !a.Bots.CheckUA {
		t.Fatalf("stream a = %+v", a)
	}
	if b.Name != "Flow B" || b.Comment != "keitaro_stream_id=22" || b.Out.Out != "https://offer.example/b" {
		t.Fatalf("stream b = %+v", b)
	}
	for id, before := range map[string]config.Group{"plain": plainBefore, "local7": local7Before, "orphan": orphanBefore} {
		if now, _ := groupByID(groups.list, id); !reflect.DeepEqual(now, before) {
			t.Fatalf("%s changed:\n%+v\n%+v", id, now, before)
		}
	}
	m, _ := s.cfg.Manifest.Load()
	if m.Campaigns["7"] != "renamed" || m.Campaigns["8"] != "orphan" || !m.Has(ktimport.KindGroups, "renamed") {
		t.Fatalf("manifest = %+v", m)
	}
	if st.CampaignsGone != 1 || st.UpdateCursor != 7 || len(st.UpdateFailed) != 0 || st.UpdateError != "" {
		t.Fatalf("state = %+v", st)
	}
	if !st.ClicksTo.After(clicksBefore) || !st.ConversionsTo.After(clicksBefore) {
		t.Fatalf("cursors did not move: clicks %v conversions %v", st.ClicksTo, st.ConversionsTo)
	}
	s.keitaroSyncFinishRun(keitaroSyncFinishIn{State: st, Result: res, Interval: time.Minute, Began: now}, s.cfg.KeitaroSyncFile)
	if st.LastUpdated != 1 {
		t.Fatalf("last updated %d", st.LastUpdated)
	}

	now = now.Add(time.Hour)
	res = s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), st, true)
	if !res.OK || res.Updated != 0 || len(groups.list) != 4 {
		t.Fatalf("second pass = %+v groups %v", res, groupIDs(groups.list))
	}
}

// TestKeitaroSyncRefreshReloadsEngine checks that the refreshed campaigns go through the file store
// whose save hook posts to the engine, and that one pass posts once.
func TestKeitaroSyncRefreshReloadsEngine(t *testing.T) {
	var reloads atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/internal/reload" {
			reloads.Add(1)
		}
	}))
	t.Cleanup(engine.Close)
	fake := &newCampaignsTracker{t: t,
		camps:   `[{"id":7,"alias":"test","name":"Renamed in Keitaro","notes":"n","state":"active"}]`,
		streams: map[string]string{"7": refreshStreams},
	}
	tracker := fake.server()
	now := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	s, _, st := refreshServer(t, tracker, &now)
	files := NewFileGroups(t.TempDir() + "/groups.json")
	if err := files.Save(context.Background(), refreshLocalGroups()); err != nil {
		t.Fatal(err)
	}
	files.OnSave(EngineReloader(engine.URL, nil))
	s.cfg.Groups = files

	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), st, true)
	if !res.OK || res.Updated != 1 || reloads.Load() != 1 {
		t.Fatalf("pass = %+v reloads %d", res, reloads.Load())
	}
	list, _ := files.List(context.Background())
	if g, _ := groupByID(list, "renamed"); len(g.Streams) != 2 || g.Notes != "n" {
		t.Fatalf("stored = %+v", g)
	}
}

// TestKeitaroSyncRefreshKeepsLocalNotes checks that a tracker without notes does not erase the local text.
func TestKeitaroSyncRefreshKeepsLocalNotes(t *testing.T) {
	old := config.Group{ID: "g", Name: "Old", Status: true, Notes: "my own text", UniqSeconds: 3600}
	next, err := overlayKeitaroGroup(old, ktimport.Group{ID: "g", Name: "New", Status: true})
	if err != nil {
		t.Fatal(err)
	}
	if next.Name != "New" || next.Notes != "my own text" || next.UniqSeconds != 3600 {
		t.Fatalf("next = %+v", next)
	}
	next, err = overlayKeitaroGroup(old, ktimport.Group{ID: "g", Status: true, Notes: "  from keitaro \n", UniqSeconds: 999})
	if err != nil {
		t.Fatal(err)
	}
	if next.Name != "Old" || next.Notes != "from keitaro" || next.UniqSeconds != 3600 {
		t.Fatalf("next = %+v", next)
	}
}

// TestKeitaroSyncOverlaySwitchesOffOnly checks that a campaign or stream the tracker switched off is
// switched off here, a stream that is on in the tracker is not switched on, and a stream that is off
// keeps its stored links.
func TestKeitaroSyncOverlaySwitchesOffOnly(t *testing.T) {
	old := config.Group{ID: "g", Status: false, Streams: []config.Stream{
		{Name: "a", Status: false, Comment: "keitaro_stream_id=1", Out: config.Output{Redirect: "http_redirect", Out: "https://mine.example", Chance: 100}},
		{Name: "b", Status: true, Comment: "keitaro_stream_id=2", Out: config.Output{Redirect: "http_redirect", Out: "https://mine.example/b", Chance: 100}},
	}}
	in := ktimport.Group{ID: "g", Status: true, Streams: []ktimport.Stream{
		{Name: "a", Status: true, Comment: "keitaro_stream_id=1", Out: ktimport.Output{Redirect: "http_redirect", Out: "https://tracker.example", Chance: 100}},
		{Name: "b", Status: false, Comment: "keitaro_stream_id=2", Out: ktimport.Output{Redirect: "show_text", Chance: 100}},
	}}
	next, err := overlayKeitaroGroup(old, in)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status || next.Streams[0].Status || next.Streams[0].Out.Out != "https://tracker.example" {
		t.Fatalf("a = %+v group on %v", next.Streams[0], next.Status)
	}
	if next.Streams[1].Status || next.Streams[1].Out.Out != "https://mine.example/b" {
		t.Fatalf("b = %+v", next.Streams[1])
	}
}

// TestKeitaroSyncFindCampaignGroup checks the match of a campaign to its stored group.
func TestKeitaroSyncFindCampaignGroup(t *testing.T) {
	in := ktimport.Group{Num: 7, Streams: []ktimport.Stream{{Comment: "keitaro_stream_id=21"}}}
	holder := func(id string, num int) config.Group {
		return config.Group{ID: id, Num: num, Streams: []config.Stream{{Comment: "keitaro_stream_id=21; note"}}}
	}
	scope := campaignMapScope{board: config.MainBoardID, allBoards: true}
	cases := []struct {
		name   string
		groups []config.Group
		gid    string
		want   string
	}{
		{"manifest id", []config.Group{holder("a", 7), holder("b", 7)}, "b", "b"},
		{"renamed by stream id", []config.Group{{ID: "x", Num: 7}, holder("moved", 7)}, "old", "moved"},
		{"slug reused by a local campaign", []config.Group{{ID: "old", Num: 3}, holder("moved", 7)}, "old", "moved"},
		{"number alone is not enough", []config.Group{{ID: "x", Num: 7}}, "old", ""},
		{"slug alone is not enough", []config.Group{{ID: "old", Num: 3}}, "old", ""},
		{"copies tie broken by number", []config.Group{holder("copy", 12), holder("orig", 7)}, "gone", "orig"},
		{"ambiguous copies", []config.Group{holder("c1", 12), holder("c2", 13)}, "gone", ""},
	}
	for _, c := range cases {
		i, ok := findCampaignGroup(c.groups, scope, 7, c.gid, in)
		if got := map[bool]string{true: c.groups[min(i, len(c.groups)-1)].ID}[ok]; got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
