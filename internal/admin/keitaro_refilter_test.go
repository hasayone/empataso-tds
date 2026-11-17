package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// refilterTracker отдаёт кампанию 7 с потоком Bot Protection (device reject, isp, bot — ИЛИ)
// и обычным потоком «только люди» и записывает пути запросов.
//
// Параметры: t — тест.
// Результат: сервер трекера и функция, возвращающая пути запросов по порядку.
// Ошибки: тест падает, если запрос не GET.
func refilterTracker(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("tracker got %s %s", r.Method, r.URL.Path)
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"test","name":"For test","state":"active"}]`))
		case "/admin_api/v1/campaigns/7/streams":
			_, _ = w.Write([]byte(`[{"id":43041,"type":"forced","name":"Bot Protection","state":"active","action_type":"status404","filter_or":true,
				"filters":[{"name":"device_type","mode":"reject","payload":["mobile","tablet"]},
					{"name":"isp","mode":"accept","payload":["Google LLC","Google Fiber Inc."]},
					{"name":"bot","mode":"accept","payload":null}]},
				{"id":44844,"type":"regular","name":"Flow 4","state":"active","action_type":"http","action_payload":"https://offer.example/a",
				"filters":[{"name":"bot","mode":"reject","payload":null}],
				"offers":[{"offer_id":11,"share":100,"state":"active"}]}]`))
		case "/admin_api/v1/offers":
			_, _ = w.Write([]byte(`[{"id":11,"name":"A","state":"active","action_type":"http","action_payload":"https://offer.example/a"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// spoilBotProtection имитирует поток, импортированный старым кодом и поправленный руками:
// без device и bot, с ручным лимитом, с отношением И.
//
// Параметры: t — тест; groups — хранилище групп после досинка.
// Ошибки: тест падает, если группы test нет.
func spoilBotProtection(t *testing.T, groups *memGroups) {
	t.Helper()
	for i := range groups.list {
		if groups.list[i].ID != "test" {
			continue
		}
		bp := &groups.list[i].Streams[0]
		bp.Rules.Bot = config.FlagOff
		bp.Rules.Phone, bp.Rules.Tablet = config.FlagOff, config.FlagOff
		bp.Rules.Limit = config.LimitRule{Enabled: true, Type: 1, Count: 50}
		bp.FilterOr = false
		return
	}
	t.Fatal("group test missing")
}

func TestKeitaroRefilterDryRunThenApply(t *testing.T) {
	tracker, paths := refilterTracker(t)
	groups := &memGroups{}
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", groups, nil, &keitaroFiles{dir: t.TempDir()})
	postKeitaroImport(t, admin, map[string]any{"campaign_id": 7})
	if job := waitKeitaroJob(t, admin); job.Error != "" {
		t.Fatalf("dosync = %+v", job)
	}
	spoilBotProtection(t, groups)
	before := len(paths())

	postKeitaroImport(t, admin, map[string]any{"filters_only": true, "campaign_ids": []int{7, 99}, "dry_run": true})
	job := waitKeitaroJob(t, admin)
	if job.Error != "" || job.Counts["dry_run"] != 1 || job.Counts["changed"] != 1 || job.Counts["missing"] != 1 {
		t.Fatalf("dry run = %+v", job)
	}
	var changed string
	for _, l := range job.Losses {
		if l.Kind == "changed" && l.Ref == "stream 43041" {
			changed = l.Detail
		}
	}
	if !strings.Contains(changed, "добавлено: ") || !strings.Contains(changed, "phone") || !strings.Contains(changed, "bot") || !strings.Contains(changed, "ИЛИ") {
		t.Fatalf("changed detail = %q", changed)
	}
	g, _ := groupByID(groups.list, "test")
	if g.Streams[0].Rules.Bot != config.FlagOff || g.Streams[0].FilterOr {
		t.Fatalf("dry run wrote groups: %+v", g.Streams[0])
	}
	for _, p := range paths()[before:] {
		if p != "/admin_api/v1/campaigns/7/streams" && p != "/admin_api/v1/campaigns/99/streams" {
			t.Fatalf("filters_only read %s", p)
		}
	}

	postKeitaroImport(t, admin, map[string]any{"filters_only": true, "all_imported": true})
	if job := waitKeitaroJob(t, admin); job.Error != "" || job.Counts["changed"] != 1 || job.Counts["groups"] != 1 {
		t.Fatalf("apply = %+v", job)
	}
	g, _ = groupByID(groups.list, "test")
	bp, flow := g.Streams[0], g.Streams[1]
	if bp.Rules.Bot != config.FlagA || !bp.FilterOr || bp.Rules.Phone != config.FlagA || bp.Rules.Tablet != config.FlagA {
		t.Fatalf("bot protection = %+v", bp)
	}
	if len(bp.Rules.Org.Values) != 2 || !bp.Rules.Limit.Enabled || bp.Rules.Limit.Count != 50 {
		t.Fatalf("org or manual limit lost: %+v", bp.Rules)
	}
	if flow.Rules.Bot != config.FlagB || !flow.Status {
		t.Fatalf("regular stream = %+v", flow)
	}

	postKeitaroImport(t, admin, map[string]any{"filters_only": true, "campaign_ids": []int{7}})
	if job := waitKeitaroJob(t, admin); job.Error != "" || job.Counts["changed"] != 0 {
		t.Fatalf("second apply must be a no-op: %+v", job)
	}
}

func TestKeitaroRefilterBadBody(t *testing.T) {
	tracker, _ := refilterTracker(t)
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", nil, nil, nil)
	for _, body := range []map[string]any{
		{"filters_only": true},
		{"filters_only": true, "campaign_ids": []int{7}, "all_imported": true},
		{"filters_only": true, "campaign_ids": []int{0}},
		{"filters_only": true, "all_imported": true, "campaigns": true},
		{"filters_only": true, "all_imported": true, "campaign_id": 7},
		{"campaign_ids": []int{7}},
		{"dry_run": true, "campaigns": true},
	} {
		raw, code := keitaroPOST(t, admin, "/api/keitaro/import", body)
		if code != http.StatusBadRequest {
			t.Errorf("%v → %d %s", body, code, raw)
		}
	}
}

func TestRefilterStreamKeepsStatusAndDisablesEmpty(t *testing.T) {
	off := config.Stream{Status: false, Comment: "keitaro_stream_id=5"}
	off.Rules.Bot = config.FlagA
	if out, diff := refilterStream(off, refilterFilters(t, true)); diff != "" || out.Status {
		t.Fatalf("a disabled stream with the same filters changed: %q %+v", diff, out)
	}
	on := config.Stream{Status: true, Comment: "keitaro_stream_id=5"}
	out, diff := refilterStream(on, refilterFilters(t, false))
	if out.Status || !strings.Contains(diff, "поток выключен") {
		t.Fatalf("a stream with nothing mapped stays on: %q %+v", diff, out)
	}
}

// refilterFilters строит результат переноса фильтров одного потока.
//
// Параметры: t — тест; ok — перенёсся ли хоть один фильтр.
// Результат: при ok — правило «только боты», иначе пустые правила с причиной.
func refilterFilters(t *testing.T, ok bool) ktimport.StreamFilters {
	t.Helper()
	if !ok {
		return ktimport.StreamFilters{Why: "нет переносимых фильтров"}
	}
	return ktimport.StreamFilters{Rules: ktimport.Rules{Bot: 1}, OK: true}
}
