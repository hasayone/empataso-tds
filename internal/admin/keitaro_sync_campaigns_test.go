package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/emtds/bridge/ktimport"
)

// newCampaignsTracker is a fake Keitaro for the new-campaign step of the sync.
// camps is the body of the campaign list. streams maps a campaign id to its streams body;
// reading the streams of any other campaign fails the test. status, when set, answers a path
// with that status. Every request that is not GET fails the test, except the log POSTs.
type newCampaignsTracker struct {
	t       *testing.T
	camps   string
	streams map[string]string
	mu      sync.Mutex
	status  map[string]int
	gets    int
	paths   []string
}

// server starts the fake. It is closed in t.Cleanup.
func (f *newCampaignsTracker) server() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/log") {
			_, _ = w.Write([]byte(`{"rows":[],"total":0}`))
			return
		}
		if r.Method != http.MethodGet {
			f.t.Errorf("tracker got %s %s", r.Method, r.URL.Path)
		}
		f.mu.Lock()
		f.gets++
		f.paths = append(f.paths, r.URL.Path)
		code := f.status[r.URL.Path]
		f.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"payment required"}`))
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/admin_api/v1/")
		switch {
		case p == "campaigns":
			_, _ = w.Write([]byte(f.camps))
		case strings.HasPrefix(p, "campaigns/") && strings.HasSuffix(p, "/streams"):
			id := strings.TrimSuffix(strings.TrimPrefix(p, "campaigns/"), "/streams")
			body, ok := f.streams[id]
			if !ok {
				f.t.Errorf("streams of campaign %s were read", id)
				body = `[]`
			}
			_, _ = w.Write([]byte(body))
		case p == "offers":
			_, _ = w.Write([]byte(`[{"id":11,"name":"Tracker A","state":"active","action_type":"http","action_payload":"https://offer.example/a"},
				{"id":12,"name":"B","state":"active","action_type":"http","action_payload":"https://offer.example/b"}]`))
		case p == "domains":
			_, _ = w.Write([]byte(`[{"id":5,"name":"d5.example","state":"active"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

// count returns how many GET requests reached the fake and how many of them read streams.
func (f *newCampaignsTracker) count() (gets, streams int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.paths {
		if strings.HasSuffix(p, "/streams") {
			streams++
		}
	}
	return f.gets, streams
}

// setStatus makes the fake answer path with code; 0 removes it.
func (f *newCampaignsTracker) setStatus(path string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == nil {
		f.status = map[string]int{}
	}
	f.status[path] = code
}

// botStreams is a Bot Protection stream with the bot filter in mode plus a flow to offerIDs.
func botStreams(mode string, offerIDs ...int) string {
	var refs []string
	for _, id := range offerIDs {
		refs = append(refs, fmt.Sprintf(`{"offer_id":%d,"share":%d,"state":"active"}`, id, 100/len(offerIDs)))
	}
	return `[{"id":1` + fmt.Sprint(offerIDs[0]) + `,"type":"forced","name":"Bot Protection","state":"active","action_type":"status404",
		"filters":[{"name":"bot","mode":"` + mode + `","payload":null}]},
		{"id":2` + fmt.Sprint(offerIDs[0]) + `,"type":"regular","name":"Flow","state":"active","action_type":"http",
		"offers":[` + strings.Join(refs, ",") + `]}]`
}

// newCampaignsServer returns an admin wired to tracker with an imported group test (campaign 7),
// a manifest that also lists campaign 8 whose group was deleted here, a local offer 11 and a
// sync state whose cursors need no bootstrap. now is the sync clock.
func newCampaignsServer(t *testing.T, tracker *httptest.Server, now *time.Time) (*Server, *memGroups, *keitaroSyncFile) {
	t.Helper()
	existing := config.Group{ID: "test", Num: 7, Name: "Edited here", Status: true, Redirect: "show_text",
		Streams: []config.Stream{{Name: "local edit", Status: true, Out: config.Output{Redirect: "404_not_found", Chance: 100}}}}
	groups := &memGroups{list: []config.Group{existing}}
	s, _ := newKeitaroServers(t, tracker.URL, "k-test", groups, &fakeSink{}, &keitaroFiles{dir: t.TempDir()})
	s.syncNow = func() time.Time { return *now }
	ctx := context.Background()
	if err := s.cfg.Offers.Save(ctx, []config.Offer{{ID: "11", Name: "Mine", URL: "https://offer.example/a", Status: true}}); err != nil {
		t.Fatal(err)
	}
	host := keitaroHost(tracker.URL)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Host: host, Groups: []string{"test", "gone"},
		Campaigns: map[string]string{"7": "test", "8": "gone"}, Offers: []string{"11"}}); err != nil {
		t.Fatal(err)
	}
	cursor := now.Add(-keitaroSyncLag - time.Minute)
	return s, groups, &keitaroSyncFile{Host: host, ClicksTo: cursor, ConversionsTo: cursor}
}

func TestKeitaroSyncImportsNewCampaigns(t *testing.T) {
	fake := &newCampaignsTracker{t: t,
		camps: `[{"id":7,"alias":"test","name":"T","state":"active"},{"id":8,"alias":"gone","name":"G","state":"active"},
			{"id":9,"alias":"fresh","name":"Fresh","state":"active"},{"id":10,"alias":"second","name":"Second","state":"active","domain_id":5}]`,
		streams: map[string]string{"9": botStreams("reject", 11, 12), "10": botStreams("accept", 12)},
	}
	tracker := fake.server()
	now := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	s, groups, st := newCampaignsServer(t, tracker, &now)
	before, _ := groupByID(groups.list, "test")
	target := syncTarget(s, s.keitaroCreds())

	res := s.runKeitaroSyncPass(context.Background(), target, st, true)
	if !res.OK || res.Campaigns != 2 {
		t.Fatalf("pass = %+v", res)
	}
	fresh, ok := groupByID(groups.list, "fresh")
	if !ok || fresh.Name != "Fresh" || len(fresh.Streams) != 2 || fresh.Streams[0].Rules.Bot != config.FlagB || len(fresh.Streams[1].Out.Offers) != 2 {
		t.Fatalf("fresh = %+v", fresh)
	}
	second, ok := groupByID(groups.list, "second")
	if !ok || second.Streams[0].Rules.Bot != config.FlagA || second.DomainID != "5" {
		t.Fatalf("second = %+v", second)
	}
	if got, _ := groupByID(groups.list, "test"); !reflect.DeepEqual(got, before) {
		t.Fatalf("existing group changed:\n%+v\n%+v", got, before)
	}
	if _, ok := groupByID(groups.list, "gone"); ok || len(groups.list) != 3 {
		t.Fatalf("groups = %+v", groupIDs(groups.list))
	}
	offers, _ := s.cfg.Offers.List(context.Background())
	if len(offers) != 2 || offers[0].ID != "11" || offers[0].Name != "Mine" || offers[1].ID != "12" {
		t.Fatalf("offers = %+v", offers)
	}
	if doms, _ := s.cfg.Domains.List(context.Background()); len(doms) != 1 || doms[0].ID != "5" {
		t.Fatalf("domains = %+v", doms)
	}
	m, _ := s.cfg.Manifest.Load()
	if m.Campaigns["9"] != "fresh" || m.Campaigns["10"] != "second" || m.Campaigns["8"] != "gone" || !m.Has(ktimport.KindOffers, "12") || !m.Has(ktimport.KindDomains, "5") {
		t.Fatalf("manifest = %+v", m)
	}
	if st.CampaignsCursor != 10 || st.CampaignsPending != 0 || st.CampaignsDeleted != 1 || len(st.CampaignsFailed) != 0 || st.CampaignsError != "" {
		t.Fatalf("state = %+v", st)
	}

	_, streamsBefore := fake.count()
	res = s.runKeitaroSyncPass(context.Background(), target, st, true)
	if _, streamsAfter := fake.count(); !res.OK || res.Campaigns != 0 || streamsAfter != streamsBefore {
		t.Fatalf("second pass %+v streams %d → %d", res, streamsBefore, streamsAfter)
	}
	s.keitaroSyncFinishRun(keitaroSyncFinishIn{State: st, Result: res, Interval: time.Minute, Began: now}, s.cfg.KeitaroSyncFile)
	if st.LastCampaigns != 0 {
		t.Fatalf("last campaigns %d", st.LastCampaigns)
	}
}

func TestKeitaroSyncNewCampaignsSkipKnownGroupID(t *testing.T) {
	fake := &newCampaignsTracker{t: t,
		camps:   `[{"id":7,"alias":"test","name":"T","state":"active"},{"id":20,"alias":"gone","name":"Same alias","state":"active"}]`,
		streams: map[string]string{"20": botStreams("accept", 12)},
	}
	tracker := fake.server()
	now := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	s, groups, st := newCampaignsServer(t, tracker, &now)
	m, _ := s.cfg.Manifest.Load()
	delete(m.Campaigns, "8")
	if err := s.cfg.Manifest.Save(m); err != nil {
		t.Fatal(err)
	}
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), st, true)
	if !res.OK || res.Campaigns != 0 || len(groups.list) != 1 {
		t.Fatalf("pass %+v groups %v", res, groupIDs(groups.list))
	}
	if st.CampaignsCursor != 20 || st.CampaignsDeleted != 1 || len(st.CampaignsFailed) != 0 {
		t.Fatalf("state = %+v", st)
	}
	if offers, _ := s.cfg.Offers.List(context.Background()); len(offers) != 1 {
		t.Fatalf("catalog written for a skipped campaign: %+v", offers)
	}
}

func TestKeitaroSyncNewCampaignsGETCap(t *testing.T) {
	var camps []string
	streams := map[string]string{}
	for id := 100; id < 100+keitaroSyncCampaignBatch+10; id++ {
		camps = append(camps, fmt.Sprintf(`{"id":%d,"alias":"c%d","name":"C%d","state":"active"}`, id, id, id))
		streams[fmt.Sprint(id)] = botStreams("accept", 12)
	}
	fake := &newCampaignsTracker{t: t, camps: "[" + strings.Join(camps, ",") + "]", streams: streams}
	tracker := fake.server()
	now := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	s, groups, st := newCampaignsServer(t, tracker, &now)
	target := syncTarget(s, s.keitaroCreds())

	res := s.runKeitaroSyncPass(context.Background(), target, st, true)
	gets, _ := fake.count()
	if !res.OK || res.Campaigns != keitaroSyncCampaignBatch || gets > keitaroSyncGETCap {
		t.Fatalf("pass %+v gets %d", res, gets)
	}
	if st.CampaignsPending != 10 || st.CampaignsCursor != 100+keitaroSyncCampaignBatch-1 {
		t.Fatalf("state = %+v", st)
	}
	res = s.runKeitaroSyncPass(context.Background(), target, st, true)
	if !res.OK || res.Campaigns != 10 || st.CampaignsPending != 0 || len(groups.list) != 1+keitaroSyncCampaignBatch+10 {
		t.Fatalf("second pass %+v state %+v groups %d", res, st, len(groups.list))
	}
}

func TestKeitaroGETBudgetRefusesOverLimit(t *testing.T) {
	fake := &newCampaignsTracker{t: t, camps: `[]`}
	tracker := fake.server()
	budget := newKeitaroGETBudget(2)
	c := (&Server{}).keitaroClient(ktCreds{Base: tracker.URL, Key: "k", gets: budget})
	for i := 0; i < 2; i++ {
		if _, err := c.FetchCampaigns(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.FetchCampaigns(context.Background()); err == nil {
		t.Fatal("third GET passed the budget")
	}
	if gets, _ := fake.count(); gets != 2 || budget.left() != 0 {
		t.Fatalf("gets %d left %d", gets, budget.left())
	}
	fake.setStatus("/admin_api/v1/campaigns", http.StatusPaymentRequired)
	budget = newKeitaroGETBudget(5)
	c = (&Server{}).keitaroClient(ktCreds{Base: tracker.URL, Key: "k", gets: budget})
	if _, err := c.FetchCampaigns(context.Background()); err == nil || budget.refusal() != http.StatusPaymentRequired {
		t.Fatalf("err %v refusal %d", err, budget.refusal())
	}
}

func TestKeitaroSyncNewCampaigns402Pauses(t *testing.T) {
	fake := &newCampaignsTracker{t: t,
		camps:   `[{"id":7,"alias":"test","name":"T","state":"active"},{"id":9,"alias":"fresh","name":"Fresh","state":"active"}]`,
		streams: map[string]string{"9": botStreams("reject", 12)},
	}
	tracker := fake.server()
	fake.setStatus("/admin_api/v1/affiliate_networks", http.StatusPaymentRequired)
	now := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	s, groups, st := newCampaignsServer(t, tracker, &now)
	target := syncTarget(s, s.keitaroCreds())

	res := s.runKeitaroSyncPass(context.Background(), target, st, true)
	if !res.OK || res.Campaigns != 0 || len(groups.list) != 1 {
		t.Fatalf("pass %+v groups %v", res, groupIDs(groups.list))
	}
	if !st.CampaignsPausedUntil.Equal(now.Add(keitaroCampaignsRetry)) || !strings.Contains(st.CampaignsError, "402") || st.CampaignsCursor != 0 {
		t.Fatalf("state = %+v", st)
	}
	if offers, _ := s.cfg.Offers.List(context.Background()); len(offers) != 1 {
		t.Fatalf("catalog written after 402: %+v", offers)
	}

	fake.setStatus("/admin_api/v1/affiliate_networks", 0)
	gets, _ := fake.count()
	now = now.Add(keitaroCampaignsRetry - time.Minute)
	if res := s.runKeitaroSyncPass(context.Background(), target, st, true); !res.OK || res.Campaigns != 0 {
		t.Fatalf("paused pass %+v", res)
	}
	if after, _ := fake.count(); after != gets {
		t.Fatalf("tracker read during the pause: %d → %d", gets, after)
	}
	now = now.Add(2 * time.Minute)
	if res := s.runKeitaroSyncPass(context.Background(), target, st, true); !res.OK || res.Campaigns != 1 {
		t.Fatalf("pass after the pause %+v state %+v", res, st)
	}
	if st.CampaignsError != "" || !st.CampaignsPausedUntil.IsZero() || st.CampaignsCursor != 9 {
		t.Fatalf("state = %+v", st)
	}
}

func TestKeitaroSyncNewCampaignsListRefused(t *testing.T) {
	fake := &newCampaignsTracker{t: t, camps: `[]`}
	tracker := fake.server()
	fake.setStatus("/admin_api/v1/campaigns", http.StatusPaymentRequired)
	now := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	s, _, st := newCampaignsServer(t, tracker, &now)
	res := s.runKeitaroSyncPass(context.Background(), syncTarget(s, s.keitaroCreds()), st, true)
	if !res.OK || st.CampaignsError != "Keitaro ответил 402" || st.CampaignsPausedUntil.IsZero() {
		t.Fatalf("pass %+v state %+v", res, st)
	}
}
