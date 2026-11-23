package admin

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// paramsTracker serves one campaign. rich selects the parameter payload of the second phase.
// It records every path.
func paramsTracker(t *testing.T, rich *atomic.Bool) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	plain := `[{"id":7,"alias":"keep","name":"Keep","state":"active","parameters":[]}]`
	full := `[{"id":7,"alias":"keep","name":"Keep","state":"active","parameters":{
		"keyword":{"name":"keyword","placeholder":"{keyword}","alias":"utm_term"},
		"sub_id_1":{"name":"sub_id_1","placeholder":"{sub1}","alias":"sub1"},
		"cost":{"name":"cost","placeholder":"{price}","alias":"cost"}
	}}]`
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
			if rich.Load() {
				_, _ = w.Write([]byte(full))
				return
			}
			_, _ = w.Write([]byte(plain))
		case "/admin_api/v1/campaigns/7/streams":
			_, _ = w.Write([]byte(`[{"id":11,"name":"keep-me","state":"active","action_type":"http","action_payload":"https://offer.example/a"}]`))
		case "/admin_api/v1/traffic_sources":
			_, _ = w.Write([]byte(`[]`))
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

func TestKeitaroParametersOnly(t *testing.T) {
	var rich atomic.Bool
	tracker, paths := paramsTracker(t, &rich)
	groups := &memGroups{}
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", groups, nil, &keitaroFiles{dir: t.TempDir()})
	postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	if job := waitKeitaroJob(t, admin); job.Error != "" || job.Counts["groups"] != 1 {
		t.Fatalf("import = %+v", job)
	}
	g, ok := groupByID(groups.list, "keep")
	if !ok || g.Streams[0].Name != "keep-me" || len(g.Parameters) != 0 {
		t.Fatalf("imported %+v", g)
	}
	for i := range groups.list {
		if groups.list[i].ID != "keep" {
			continue
		}
		groups.list[i].Parameters = map[string]string{"keyword": "manual"}
		groups.list[i].ParamTokens = map[string]string{"keyword": "{mine}"}
	}
	rich.Store(true)
	before := len(paths())

	postKeitaroImport(t, admin, map[string]any{"parameters_only": true, "all_imported": true, "dry_run": true})
	job := waitKeitaroJob(t, admin)
	if job.Error != "" || job.Counts["dry_run"] != 1 || job.Counts["changed"] != 1 || job.Counts["groups"] != 0 {
		t.Fatalf("dry run = %+v", job)
	}
	g, _ = groupByID(groups.list, "keep")
	if g.Parameters["keyword"] != "manual" || g.Parameters["sub_id_1"] != "" || g.Streams[0].Name != "keep-me" {
		t.Fatalf("dry run wrote %+v", g)
	}

	postKeitaroImport(t, admin, map[string]any{"parameters_only": true, "all_imported": true})
	job = waitKeitaroJob(t, admin)
	if job.Error != "" || job.Counts["changed"] != 1 || job.Counts["groups"] != 1 {
		t.Fatalf("apply = %+v losses %+v", job.Counts, job.Losses)
	}
	g, _ = groupByID(groups.list, "keep")
	if g.Parameters["keyword"] != "manual" || g.ParamTokens["keyword"] != "{mine}" {
		t.Fatalf("manual parameter replaced: %+v %+v", g.Parameters, g.ParamTokens)
	}
	if g.Parameters["sub_id_1"] != "sub1" || g.ParamTokens["sub_id_1"] != "{sub1}" {
		t.Fatalf("sub id = %+v %+v", g.Parameters, g.ParamTokens)
	}
	if g.CostParam != "cost" || g.CostToken != "{price}" {
		t.Fatalf("price = %q %q", g.CostParam, g.CostToken)
	}
	if g.Streams[0].Name != "keep-me" || g.Streams[0].Out.Out != "https://offer.example/a" {
		t.Fatalf("stream rewritten: %+v", g.Streams[0])
	}
	for _, p := range paths()[before:] {
		if p != "/admin_api/v1/campaigns" && p != "/admin_api/v1/traffic_sources" {
			t.Fatalf("parameters_only read %s", p)
		}
	}

	postKeitaroImport(t, admin, map[string]any{"parameters_only": true, "campaign_ids": []int{7}})
	if job = waitKeitaroJob(t, admin); job.Error != "" || job.Counts["changed"] != 0 {
		t.Fatalf("second apply = %+v", job)
	}
}

// A parameter key that is already stored stays, including sub_id_1=bundle_id and its token.
// The tracker alias sub1 does not replace it. An empty keyword is still filled.
func TestParametersOnlyKeepsManualBundle(t *testing.T) {
	var rich atomic.Bool
	rich.Store(true)
	tracker, paths := paramsTracker(t, &rich)
	groups := &memGroups{}
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", groups, nil, &keitaroFiles{dir: t.TempDir()})
	postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	if job := waitKeitaroJob(t, admin); job.Error != "" || job.Counts["groups"] != 1 {
		t.Fatalf("import = %+v", job)
	}
	for i := range groups.list {
		if groups.list[i].ID != "keep" {
			continue
		}
		groups.list[i].Parameters = map[string]string{"sub_id_1": "bundle_id"}
		groups.list[i].ParamTokens = map[string]string{"sub_id_1": "com.com.com"}
	}
	before := len(paths())
	postKeitaroImport(t, admin, map[string]any{"parameters_only": true, "all_imported": true})
	job := waitKeitaroJob(t, admin)
	if job.Error != "" || job.Counts["changed"] != 1 {
		t.Fatalf("apply = %+v losses %+v", job.Counts, job.Losses)
	}
	g, _ := groupByID(groups.list, "keep")
	if g.Parameters["sub_id_1"] != "bundle_id" || g.ParamTokens["sub_id_1"] != "com.com.com" {
		t.Fatalf("manual bundle replaced: %+v %+v", g.Parameters, g.ParamTokens)
	}
	if g.Parameters["keyword"] != "utm_term" || g.ParamTokens["keyword"] != "{keyword}" {
		t.Fatalf("empty keyword not filled: %+v %+v", g.Parameters, g.ParamTokens)
	}
	if g.Streams[0].Name != "keep-me" {
		t.Fatalf("stream rewritten: %+v", g.Streams[0])
	}
	for _, p := range paths()[before:] {
		if p != "/admin_api/v1/campaigns" && p != "/admin_api/v1/traffic_sources" {
			t.Fatalf("parameters_only read %s", p)
		}
	}
	postKeitaroImport(t, admin, map[string]any{"parameters_only": true, "all_imported": true})
	if job = waitKeitaroJob(t, admin); job.Error != "" || job.Counts["changed"] != 0 {
		t.Fatalf("second apply = %+v", job)
	}
	g, _ = groupByID(groups.list, "keep")
	if g.Parameters["sub_id_1"] != "bundle_id" || g.ParamTokens["sub_id_1"] != "com.com.com" {
		t.Fatalf("second pass replaced bundle: %+v %+v", g.Parameters, g.ParamTokens)
	}
}

func TestKeitaroParametersBadBody(t *testing.T) {
	tracker, _ := paramsTracker(t, &atomic.Bool{})
	admin := newKeitaroAdmin(t, tracker.URL, "k-test", nil, nil, nil)
	for _, body := range []map[string]any{
		{"parameters_only": true},
		{"parameters_only": true, "campaign_ids": []int{7}, "all_imported": true},
		{"parameters_only": true, "campaign_ids": []int{0}},
		{"parameters_only": true, "all_imported": true, "clicks": true},
		{"parameters_only": true, "all_imported": true, "filters_only": true},
		{"parameters_only": true, "all_imported": true, "campaign_id": 7},
	} {
		raw, code := keitaroPOST(t, admin, "/api/keitaro/import", body)
		if code != http.StatusBadRequest {
			t.Errorf("%v → %d %s", body, code, raw)
		}
	}
}

func TestMergeImportedGroupsKeepsFilledParams(t *testing.T) {
	existing := []config.Group{{
		ID: "a", Name: "A", Num: 3,
		Parameters:  map[string]string{"keyword": "manual"},
		ParamTokens: map[string]string{"keyword": "{mine}"},
		Streams:     []config.Stream{{Name: "s", Status: true}},
	}}
	incoming := []ktimport.Group{{
		ID: "a", Name: "A2",
		Parameters:  map[string]string{"keyword": "utm_term", "sub_id_1": "sub1"},
		ParamTokens: map[string]string{"keyword": "{keyword}", "sub_id_1": "{sub1}"},
		CostParam:   "cost", CostToken: "{price}",
	}}
	prev := ktimport.Manifest{}
	prev.Add(ktimport.KindGroups, "a")
	out, _, err := mergeImportedGroups(existing, incoming, prev, "", config.MainBoardID)
	if err != nil {
		t.Fatal(err)
	}
	g := out[0]
	if g.Parameters["keyword"] != "manual" || g.ParamTokens["keyword"] != "{mine}" {
		t.Fatalf("manual lost %+v %+v", g.Parameters, g.ParamTokens)
	}
	if g.Parameters["sub_id_1"] != "sub1" || g.ParamTokens["sub_id_1"] != "{sub1}" || g.CostParam != "cost" || g.CostToken != "{price}" {
		t.Fatalf("empty slots not filled %+v", g)
	}
}
