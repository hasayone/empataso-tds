package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// oneCampaignTracker отдаёт кампании 7 и 8; потоки кампании 8 читать нельзя.
// botMode задаёт режим фильтра bot у потока Bot Protection кампании 7.
//
// Параметры: t — тест; botMode — accept или reject.
// Результат: сервер трекера; закрывается в t.Cleanup.
// Ошибки: тест падает, если запрос не GET или читаются потоки кампании 8.
func oneCampaignTracker(t *testing.T, botMode *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("tracker got %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"test","name":"For test","state":"active"},{"id":8,"alias":"other","name":"Other","state":"active"}]`))
		case "/admin_api/v1/campaigns/7/streams":
			_, _ = w.Write([]byte(`[{"id":43041,"type":"forced","name":"Bot Protection","state":"active","action_type":"status404",
				"filters":[{"name":"bot","mode":"` + botMode.Load().(string) + `","payload":null}]},
				{"id":44844,"type":"regular","name":"Flow 4","state":"active","action_type":"http","action_payload":"https://offer.example/a",
				"offers":[{"offer_id":11,"share":100,"state":"active"}]}]`))
		case "/admin_api/v1/campaigns/8/streams":
			t.Errorf("streams of campaign 8 were read")
			_, _ = w.Write([]byte(`[]`))
		case "/admin_api/v1/offers":
			_, _ = w.Write([]byte(`[{"id":11,"name":"A","state":"active","action_type":"http","action_payload":"https://offer.example/a"},
				{"id":12,"name":"B","state":"active","action_type":"http","action_payload":"https://offer.example/b"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKeitaroDosyncOneCampaign(t *testing.T) {
	var mode atomic.Value
	mode.Store("accept")
	tracker := oneCampaignTracker(t, &mode)
	other := config.Group{ID: "other", Name: "Local", Status: true, Redirect: "show_text",
		Streams: []config.Stream{{Name: "local edit", Status: true, Out: config.Output{Redirect: "404_not_found", Chance: 100}}}}
	groups := &memGroups{list: []config.Group{other}}
	dir := t.TempDir()
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", groups, nil, &keitaroFiles{dir: dir})

	postKeitaroImport(t, admin, map[string]any{"campaign_id": 7})
	job := waitKeitaroJob(t, admin)
	if job.Error != "" || job.Stage != "done" || job.Counts["groups"] != 1 {
		t.Fatalf("job = %+v", job)
	}
	g, ok := groupByID(groups.list, "test")
	if !ok || len(g.Streams) != 2 || g.Streams[0].Rules.Bot != config.FlagA || g.Streams[0].Comment != "keitaro_stream_id=43041" {
		t.Fatalf("test group = %+v", g)
	}
	if got, _ := groupByID(groups.list, "other"); got.Name != "Local" || len(got.Streams) != 1 || got.Streams[0].Name != "local edit" {
		t.Fatalf("other group changed: %+v", got)
	}
	offers := readOffers(t, dir+"/offers.json")
	if len(offers) != 1 || offers[0].ID != "11" {
		t.Fatalf("offers = %+v", offers)
	}

	mode.Store("reject")
	postKeitaroImport(t, admin, map[string]any{"campaign_id": 7})
	if job := waitKeitaroJob(t, admin); job.Error != "" {
		t.Fatalf("second dosync = %+v", job)
	}
	g, _ = groupByID(groups.list, "test")
	if len(groups.list) != 2 || len(g.Streams) != 2 || g.Streams[0].Rules.Bot != config.FlagB {
		t.Fatalf("after re-sync groups = %d test = %+v", len(groups.list), g)
	}
}

func TestKeitaroDosyncUnknownCampaign(t *testing.T) {
	var mode atomic.Value
	mode.Store("accept")
	tracker := oneCampaignTracker(t, &mode)
	groups := &memGroups{}
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", groups, nil, nil)
	postKeitaroImport(t, admin, map[string]any{"campaign_id": 99})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "failed" || !strings.Contains(job.Error, "кампания не найдена") || !strings.Contains(job.Error, "99") {
		t.Fatalf("job = %+v", job)
	}
	if len(groups.list) != 0 {
		t.Fatalf("groups written: %+v", groups.list)
	}
}

func TestKeitaroDosyncBadBody(t *testing.T) {
	var mode atomic.Value
	mode.Store("accept")
	tracker := oneCampaignTracker(t, &mode)
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", nil, nil, nil)
	for _, body := range []map[string]any{
		{"campaign_id": -1},
		{"campaign_id": 7, "users": true},
		{"campaign_id": 7, "clicks": true, "from": "2026-01-01", "to": "2026-01-02"},
	} {
		raw, code := keitaroPOST(t, admin, "/api/keitaro/import", body)
		if code != http.StatusBadRequest || !strings.Contains(string(raw), "campaign_id") {
			t.Errorf("%v → %d %s", body, code, raw)
		}
	}
}
