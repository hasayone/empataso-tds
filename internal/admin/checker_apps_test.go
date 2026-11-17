package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

// seedRecord stores rec as the record of campaign id, the way a loaded file would.
func (r *checkerRig) seedRecord(id string, rec checkerApp) {
	r.t.Helper()
	r.s.checker.mu.Lock()
	defer r.s.checker.mu.Unlock()
	r.s.checker.apps[id] = rec
}

func TestCheckerAppGetPrefillsFromTheName(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	code, raw := r.do(http.MethodGet, "/api/checker/apps/promo", nil)
	a := r.answer(raw)
	if code != http.StatusOK || a.App != nil || a.Foreign || !a.Integration.Enabled || !a.Integration.CreateApps {
		t.Fatalf("GET %d %s", code, raw)
	}
	want := checkerPrefill{
		Keyword: "SpinDog", BundleID: "com.playcobra.fasterthink", Name: "11381 - SpinDog",
		KeitaroURL:   chkAdmin + "/#/flows/promo",
		PlayStoreURL: "https://play.google.com/store/apps/details?id=com.playcobra.fasterthink",
	}
	if a.Prefill != want {
		t.Fatalf("prefill %+v, want %+v", a.Prefill, want)
	}
	if code, _ := r.do(http.MethodGet, "/api/checker/apps/nowhere", nil); code != http.StatusNotFound {
		t.Fatalf("unknown campaign: %d", code)
	}
	if r.fc.total() != 0 {
		t.Fatal("GET sent a request")
	}
}

func TestCheckerAppPutValidatesAndGuardsStates(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	ok := map[string]any{
		"keyword": "SpinDog", "bundle_id": "com.playcobra.fasterthink", "notion_url": "https://notion.so/x",
		"console_id": "dev-1", "console_name": "Console One",
	}
	code, raw := r.do(http.MethodPut, "/api/checker/apps/promo", ok)
	a := r.answer(raw)
	if code != http.StatusOK || a.App == nil || a.App.State != checkerStDraft || a.App.Keyword != "SpinDog" ||
		a.App.NotionURL != "https://notion.so/x" || a.App.ConsoleID != "dev-1" || a.App.Name != "11381 - SpinDog" || a.App.CreatedAt == "" {
		t.Fatalf("PUT %d %s", code, raw)
	}
	bad := map[string]map[string]any{
		"underscore bundle": {"keyword": "K", "bundle_id": "com.bapp.application2003_1"},
		"no dot bundle":     {"keyword": "K", "bundle_id": "nodot"},
		"colon keyword":     {"keyword": "a:b", "bundle_id": "com.a.b"},
		"newline keyword":   {"keyword": "a\nb", "bundle_id": "com.a.b"},
		"http notion":       {"keyword": "K", "bundle_id": "com.a.b", "notion_url": "http://notion.so/x"},
		"long console":      {"keyword": "K", "bundle_id": "com.a.b", "console_id": strings.Repeat("c", 101)},
	}
	for name, body := range bad {
		if code, raw := r.do(http.MethodPut, "/api/checker/apps/betclic", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	code, raw = r.do(http.MethodPut, "/api/checker/apps/betclic", map[string]any{"keyword": "Betclic", "bundle_id": "com.playcobra.fasterthink"})
	if code != http.StatusConflict || !strings.Contains(string(raw), "11381") {
		t.Fatalf("taken bundle: %d %s", code, raw)
	}
	if _, has := r.rec("betclic"); has {
		t.Fatal("a rejected PUT made a record")
	}
	if code, _ := r.do(http.MethodPut, "/api/checker/apps/nowhere", ok); code != http.StatusNotFound {
		t.Fatalf("unknown campaign: %d", code)
	}
	for _, st := range []string{checkerStLinked, checkerStSending} {
		rec, _ := r.rec("promo")
		rec.State = st
		r.seedRecord("promo", rec)
		if code, _ := r.do(http.MethodPut, "/api/checker/apps/promo", ok); code != http.StatusConflict {
			t.Errorf("PUT in %s: %d", st, code)
		}
	}
	if r.fc.total() != 0 {
		t.Fatal("PUT sent a request")
	}
}

func TestCheckerAppCreateQueuesOnce(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	code, raw := r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	a := r.answer(raw)
	wantTenant := chkBase + "|" + chkTenant
	if code != http.StatusAccepted || a.App == nil || a.App.State != checkerStQueued || a.App.Tenant != wantTenant ||
		a.App.Keyword != "SpinDog" || a.App.BundleID != "com.playcobra.fasterthink" || a.App.Attempts != 0 {
		t.Fatalf("create %d %s", code, raw)
	}
	if code, _ := r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{}); code != http.StatusOK {
		t.Fatalf("second create: %d", code)
	}
	if code, raw := r.do(http.MethodPost, "/api/checker/apps/under/create", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("underscore bundle: %d %s", code, raw)
	}
	if code, raw := r.do(http.MethodPost, "/api/checker/apps/plain/create", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("no keyword: %d %s", code, raw)
	}
	for _, id := range []string{"under", "plain"} {
		if _, has := r.rec(id); has {
			t.Errorf("a refused create made a record for %s", id)
		}
	}
	if r.fc.total() != 0 {
		t.Fatal("create sent a request before the queue ran")
	}
}

func TestCheckerTwoCreateButtonsMakeOnePost(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := r.do(http.MethodPost, "/api/checker/apps/betclic/create", map[string]any{})
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	accepted := 0
	for c := range codes {
		if c == http.StatusAccepted {
			accepted++
		} else if c != http.StatusOK {
			t.Errorf("status %d", c)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d creates were accepted, want 1", accepted)
	}
	var run sync.WaitGroup
	for i := 0; i < 2; i++ {
		run.Add(1)
		go func() {
			defer run.Done()
			r.run()
		}()
	}
	run.Wait()
	if n := r.fc.count("POST", "/api/apps"); n != 1 {
		t.Fatalf("%d POSTs, want 1", n)
	}
	if rec, _ := r.rec("betclic"); rec.State != checkerStLinked {
		t.Fatalf("state %s", rec.State)
	}
}

func TestCheckerCreateWithCreationOffQueuesWithoutSending(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.settings(map[string]any{"create_apps": false})
	code, raw := r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	a := r.answer(raw)
	if code != http.StatusAccepted || a.Integration.CreateApps || a.App == nil || a.App.State != checkerStQueued {
		t.Fatalf("create %d %s", code, raw)
	}
	r.run()
	r.clock.Advance(time.Hour)
	r.run()
	if r.fc.total() != 0 {
		t.Fatalf("%d requests with creation off", r.fc.total())
	}
	if rec, _ := r.rec("promo"); rec.State != checkerStQueued {
		t.Fatalf("state %s", rec.State)
	}
	r.settings(map[string]any{"create_apps": true})
	r.run()
	if rec, _ := r.rec("promo"); rec.State != checkerStLinked {
		t.Fatalf("after switching on: %s", rec.State)
	}
}

func TestCheckerQueueIsFullAt200(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	for i := 0; i < checkerMaxQueued; i++ {
		r.seedRecord("ghost"+string(rune('A'+i%26))+string(rune('a'+i/26)), checkerApp{State: checkerStQueued, Tenant: chkBase + "|" + chkTenant})
	}
	code, raw := r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	if code != http.StatusTooManyRequests || !strings.Contains(string(raw), "очередь заполнена") {
		t.Fatalf("201st create: %d %s", code, raw)
	}
	if _, has := r.rec("promo"); has {
		t.Fatal("a refused create made a record")
	}
}

func TestCheckerAppRightsFollowTheCampaign(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	read, readCSRF := r.user("reader", security.Access{
		Sections: []string{"flows"}, Grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeRead}},
	})
	if code, _ := r.doWith(read, readCSRF, http.MethodGet, "/api/checker/apps/promo", nil); code != http.StatusOK {
		t.Fatalf("reader GET: %d", code)
	}
	if code, _ := r.doWith(read, readCSRF, http.MethodGet, "/api/checker/status", nil); code != http.StatusOK {
		t.Fatalf("reader status: %d", code)
	}
	for _, p := range []struct{ method, path string }{
		{http.MethodPut, "/api/checker/apps/promo"},
		{http.MethodPost, "/api/checker/apps/promo/create"},
		{http.MethodPost, "/api/checker/apps/promo/refresh"},
	} {
		if code, _ := r.doWith(read, readCSRF, p.method, p.path, map[string]any{"keyword": "K"}); code != http.StatusForbidden {
			t.Errorf("reader %s %s: %d", p.method, p.path, code)
		}
	}
	picked, pickedCSRF := r.user("picked", security.Access{
		Sections: []string{"flows"}, Grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeSelected, IDs: []string{"betclic"}}},
	})
	if code, _ := r.doWith(picked, pickedCSRF, http.MethodGet, "/api/checker/apps/promo", nil); code != http.StatusNotFound {
		t.Fatalf("a campaign outside the grant: %d", code)
	}
	if code, _ := r.doWith(picked, pickedCSRF, http.MethodPost, "/api/checker/apps/betclic/create", map[string]any{}); code != http.StatusAccepted {
		t.Fatalf("a campaign inside the grant: %d", code)
	}
	nobody, nobodyCSRF := r.user("nobody", security.Access{Sections: []string{"offers"}, Grants: map[string]security.Grant{}})
	for _, p := range []string{"/api/checker/apps/promo", "/api/checker/status", "/api/checker/consoles"} {
		if code, _ := r.doWith(nobody, nobodyCSRF, http.MethodGet, p, nil); code != http.StatusForbidden {
			t.Errorf("no flows section, GET %s: %d", p, code)
		}
	}
	flows, flowsCSRF := r.user("flows", security.Access{Sections: []string{"flows"}, Grants: map[string]security.Grant{}})
	for _, p := range []string{"/api/checker/settings", "/api/checker/keywords/preview"} {
		if code, _ := r.doWith(flows, flowsCSRF, http.MethodGet, p, nil); code != http.StatusForbidden {
			t.Errorf("flows user, GET %s: %d", p, code)
		}
	}
}

func TestCheckerChangedTenantFreezesAndRebinds(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	r.run()
	if rec, _ := r.rec("promo"); rec.State != checkerStLinked || rec.AppID == "" {
		t.Fatalf("setup: %+v", rec)
	}
	r.do(http.MethodPost, "/api/checker/apps/betclic/create", map[string]any{})
	r.settings(map[string]any{"tenant_id": "tenant2", "app_url": chkBase + "/apps/{id}"})
	r.fc.tenant = "tenant2"
	before := r.fc.total()
	r.run()
	r.clock.Advance(time.Hour)
	r.run()
	if r.fc.total() != before {
		t.Fatalf("a record of the old tenant was sent: %d new requests", r.fc.total()-before)
	}
	if rec, _ := r.rec("betclic"); rec.State != checkerStQueued {
		t.Fatalf("frozen record is %s", rec.State)
	}
	code, raw := r.do(http.MethodGet, "/api/checker/apps/promo", nil)
	a := r.answer(raw)
	if code != http.StatusOK || !a.Foreign || a.AppURL != "" || a.App == nil || a.App.State != checkerStLinked {
		t.Fatalf("foreign GET %d %s", code, raw)
	}
	if code, _ := r.do(http.MethodPost, "/api/checker/apps/promo/refresh", map[string]any{}); code != http.StatusConflict {
		t.Fatalf("refresh of a foreign record: %d", code)
	}
	if r.fc.total() != before {
		t.Fatal("refresh sent a request for a foreign record")
	}
	code, raw = r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	a = r.answer(raw)
	if code != http.StatusAccepted || a.Foreign || a.App.State != checkerStQueued || a.App.AppID != "" || !strings.HasSuffix(a.App.Tenant, "|tenant2") {
		t.Fatalf("rebind %d %s", code, raw)
	}
	r.run()
	rec, _ := r.rec("promo")
	if rec.State != checkerStLinked || !rec.Existed || rec.KeywordsSent != 0 {
		t.Fatalf("after the rebind: %+v", rec)
	}
	if n := r.fc.count("POST", "/api/apps"); n != 1 {
		t.Fatalf("%d POSTs, the rebind must not create a duplicate", n)
	}
}

func TestCheckerRefreshReadsStatusOncePerMinute(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.settings(map[string]any{"app_url": chkBase + "/apps/{id}"})
	if code, _ := r.do(http.MethodPost, "/api/checker/apps/promo/refresh", map[string]any{}); code != http.StatusConflict {
		t.Fatalf("refresh without a record: %d", code)
	}
	r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	if code, _ := r.do(http.MethodPost, "/api/checker/apps/promo/refresh", map[string]any{}); code != http.StatusConflict {
		t.Fatalf("refresh of a queued record: %d", code)
	}
	r.run()
	r.fc.statusOf = "live"
	code, raw := r.do(http.MethodPost, "/api/checker/apps/promo/refresh", map[string]any{})
	a := r.answer(raw)
	if code != http.StatusOK || a.App.Status != "live" || a.App.StatusAt == "" || a.AppURL != chkBase+"/apps/app-1" {
		t.Fatalf("refresh %d %s", code, raw)
	}
	resp := csrfDo(t, r.c, http.MethodPost, r.web.URL+"/api/checker/apps/promo/refresh", r.csrf, map[string]any{})
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" || body["retry_after"] == nil {
		t.Fatalf("second refresh: %d %v %v", resp.StatusCode, resp.Header, body)
	}
	if n := r.fc.count("GET", "/api/apps/app-1"); n != 1 {
		t.Fatalf("%d status calls", n)
	}
	r.clock.Advance(61 * time.Second)
	if code, _ := r.do(http.MethodPost, "/api/checker/apps/promo/refresh", map[string]any{}); code != http.StatusOK {
		t.Fatalf("refresh after a minute: %d", code)
	}
	if n := r.fc.count("GET", "/api/apps/app-1"); n != 2 {
		t.Fatalf("%d status calls after a minute", n)
	}
	r.fc.script["GET /api/apps/app-1"] = []int{http.StatusInternalServerError}
	r.clock.Advance(61 * time.Second)
	if code, raw := r.do(http.MethodPost, "/api/checker/apps/promo/refresh", map[string]any{}); code != http.StatusBadGateway || strings.Contains(string(raw), chkToken) {
		t.Fatalf("failed refresh: %d %s", code, raw)
	}
}

func TestCheckerConsolesAreCachedForTenMinutes(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	if code, _ := r.do(http.MethodGet, "/api/checker/consoles", nil); code != http.StatusConflict {
		t.Fatalf("consoles while off: %d", code)
	}
	r.enable()
	for i := 0; i < 2; i++ {
		code, raw := r.do(http.MethodGet, "/api/checker/consoles", nil)
		var list []checkerConsole
		_ = json.Unmarshal(raw, &list)
		if code != http.StatusOK || len(list) != 2 || list[0].ID != "dev-1" {
			t.Fatalf("consoles %d %s", code, raw)
		}
	}
	if n := r.fc.count("GET", "/api/consoles"); n != 1 {
		t.Fatalf("%d calls for two requests", n)
	}
	r.clock.Advance(11 * time.Minute)
	r.do(http.MethodGet, "/api/checker/consoles", nil)
	if n := r.fc.count("GET", "/api/consoles"); n != 2 {
		t.Fatalf("%d calls after the cache expired", n)
	}
}

func TestCheckerOrphansAreHiddenAndPruned(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	r.setGroups(defaultCheckerGroups()[1:])
	if code, _ := r.do(http.MethodGet, "/api/checker/apps/promo", nil); code != http.StatusNotFound {
		t.Fatalf("orphan GET: %d", code)
	}
	r.run()
	if r.fc.total() != 0 {
		t.Fatal("a record of a deleted campaign was sent")
	}
	if _, has := r.rec("promo"); !has {
		t.Fatal("a young orphan must be kept")
	}
	r.clock.Advance(31 * 24 * time.Hour)
	r.run()
	if _, has := r.rec("promo"); has {
		t.Fatal("an old orphan must be dropped")
	}
	raw, err := os.ReadFile(filepath.Join(r.dir, "checker-apps.json"))
	if err != nil || strings.Contains(string(raw), `"promo"`) {
		t.Fatalf("file after the prune: %v %s", err, raw)
	}
}

func TestCheckerButtonWorkerAndRefreshTogether(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	r.run()
	var wg sync.WaitGroup
	do := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				f()
			}
		}()
	}
	do(func() { r.run() })
	do(func() { r.do(http.MethodPost, "/api/checker/apps/promo/refresh", map[string]any{}) })
	do(func() { r.do(http.MethodPost, "/api/checker/apps/betclic/create", map[string]any{}) })
	do(func() { r.do(http.MethodPut, "/api/checker/apps/under", map[string]any{"keyword": "Num"}) })
	do(func() { r.do(http.MethodGet, "/api/checker/apps/promo", nil) })
	do(func() { r.s.checkerQueueCounts() })
	wg.Wait()
	r.run()
	for _, id := range []string{"promo", "betclic"} {
		if rec, _ := r.rec(id); rec.State != checkerStLinked {
			t.Errorf("%s ended %s", id, rec.State)
		}
	}
	if n := r.fc.count("POST", "/api/apps"); n != 2 {
		t.Fatalf("%d POSTs, want one per campaign", n)
	}
	raw, err := os.ReadFile(filepath.Join(r.dir, "checker-apps.json"))
	var f checkerAppsFile
	if err != nil || json.Unmarshal(raw, &f) != nil || len(f.Apps) != len(r.s.checker.apps) {
		t.Fatalf("the file is not whole: %v %s", err, raw)
	}
}

func TestCheckerDamagedRecordsFileIsNotOverwritten(t *testing.T) {
	const junk = `{"version":1,"apps":`
	r := newCheckerRig(t, rigSpec{files: map[string]string{"checker-apps.json": junk}})
	r.enable()
	if code, _ := r.do(http.MethodPut, "/api/checker/apps/promo", map[string]any{"keyword": "K"}); code != http.StatusServiceUnavailable {
		t.Fatalf("PUT on a damaged file: %d", code)
	}
	if code, _ := r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{}); code != http.StatusServiceUnavailable {
		t.Fatalf("create on a damaged file: %d", code)
	}
	raw, _ := os.ReadFile(filepath.Join(r.dir, "checker-apps.json"))
	if string(raw) != junk {
		t.Fatalf("the damaged file was replaced: %s", raw)
	}
}
