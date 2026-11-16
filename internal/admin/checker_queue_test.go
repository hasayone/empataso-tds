package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// create queues the creation of campaign id and fails the test unless the answer is 202.
func (r *checkerRig) create(id string) {
	r.t.Helper()
	if code, raw := r.do(http.MethodPost, "/api/checker/apps/"+id+"/create", map[string]any{}); code != http.StatusAccepted {
		r.t.Fatalf("create %s: %d %s", id, code, raw)
	}
}

func TestCheckerCreateSendsExactlyOneGetAndOnePost(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.do(http.MethodPut, "/api/checker/apps/promo", map[string]any{
		"keyword": "SpinDog", "bundle_id": "com.playcobra.fasterthink", "notion_url": "https://notion.so/x",
		"console_id": "dev-1", "console_name": "Console One",
	})
	r.create("promo")
	r.run()
	if r.fc.total() != 2 || r.fc.count("GET", "/api/apps") != 1 || r.fc.count("POST", "/api/apps") != 1 {
		t.Fatalf("requests: %+v", r.fc.reqs)
	}
	get := r.fc.requests("GET", "/api/apps")[0].Query
	if get.Get("bundle_id") != "com.playcobra.fasterthink" || get.Get("show_banned") != "true" || get.Get("show_archived") != "true" {
		t.Fatalf("lookup query %v", get)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(r.fc.requests("POST", "/api/apps")[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, 41)
	for _, l := range checkerDefaultLocales() {
		lines = append(lines, "SpinDog:"+l.Code)
	}
	want := map[string]any{
		"name": "11381 - SpinDog", "bundle_id": "com.playcobra.fasterthink",
		"play_store_url": "https://play.google.com/store/apps/details?id=com.playcobra.fasterthink",
		"keitaro_url":    chkAdmin + "/#/flows/promo", "notion_url": "https://notion.so/x",
		"status": "store_watch", "check_frequency": float64(360), "console_developer_id": "dev-1",
		"keywords_data": strings.Join(lines, "\n"),
	}
	if len(body) != len(want) {
		t.Fatalf("POST body has %d fields: %v", len(body), body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("POST %s = %v, want %v", k, body[k], v)
		}
	}
	if n := len(strings.Split(body["keywords_data"].(string), "\n")); n != 41 {
		t.Fatalf("%d keyword lines, want 41", n)
	}
	rec, _ := r.rec("promo")
	if rec.State != checkerStLinked || rec.AppID != "app-1" || rec.Status != "store_watch" || rec.KeywordsSent != 41 || rec.Existed || rec.StatusAt == "" {
		t.Fatalf("record %+v", rec)
	}
}

func TestCheckerPostLeavesOutEmptyOptionalFields(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.settings(map[string]any{"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": true, "create_apps": true})
	r.create("promo")
	r.run()
	var body map[string]any
	if err := json.Unmarshal([]byte(r.fc.requests("POST", "/api/apps")[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"notion_url", "keitaro_url", "console_developer_id", "console_name"} {
		if _, has := body[k]; has {
			t.Errorf("POST carries %s", k)
		}
	}
}

func TestCheckerOwnLocaleListIsSent(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.settings(map[string]any{"locales": []string{"ru-RU", "en-US"}})
	r.create("promo")
	r.run()
	var body map[string]any
	_ = json.Unmarshal([]byte(r.fc.requests("POST", "/api/apps")[0].Body), &body)
	if body["keywords_data"] != "SpinDog:ru-RU\nSpinDog:en-US" {
		t.Fatalf("keywords_data = %q", body["keywords_data"])
	}
	if rec, _ := r.rec("promo"); rec.KeywordsSent != 2 {
		t.Fatalf("keywords_sent = %d", rec.KeywordsSent)
	}
}

func TestCheckerLostPostAnswerIsFoundByTheLookup(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.fc.dropNext = true
	r.create("promo")
	r.run()
	rec, _ := r.rec("promo")
	if rec.State != checkerStQueued || rec.Attempts != 1 || rec.LastError == "" {
		t.Fatalf("after the lost answer: %+v", rec)
	}
	due := checkerParseTime(rec.NextAttemptAt)
	if got := due.Sub(r.clock.Now()); got < 50*time.Second || got > time.Minute {
		t.Fatalf("next attempt in %v, want about a minute", got)
	}
	r.run()
	if r.fc.count("POST", "/api/apps") != 1 {
		t.Fatal("the record was sent again before its time")
	}
	r.clock.Advance(time.Minute)
	r.run()
	rec, _ = r.rec("promo")
	if rec.State != checkerStLinked || !rec.Existed || rec.KeywordsSent != 0 || rec.AppID != "app-1" {
		t.Fatalf("after the retry: %+v", rec)
	}
	if n := r.fc.count("POST", "/api/apps"); n != 1 {
		t.Fatalf("%d POSTs, the retry must find the application by the lookup", n)
	}
}

func TestCheckerExistingAppLinksWithoutKeywords(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.fc.apps = []fcApp{{ID: "app-7", Bundle: "com.playcobra.fasterthink", Status: "live"}}
	r.create("promo")
	r.run()
	rec, _ := r.rec("promo")
	if rec.State != checkerStLinked || !rec.Existed || rec.KeywordsSent != 0 || rec.AppID != "app-7" || rec.Status != "live" {
		t.Fatalf("found by the lookup: %+v", rec)
	}
	if r.fc.count("POST", "/api/apps") != 0 {
		t.Fatal("a POST went out for an existing application")
	}
	r.fc.apps = []fcApp{{ID: "app-8", Bundle: "com.findgamestore.findpairapp", Status: "banned", Hidden: true}}
	r.create("betclic")
	r.run()
	rec, _ = r.rec("betclic")
	if rec.State != checkerStLinked || !rec.Existed || rec.KeywordsSent != 0 || rec.AppID != "app-8" {
		t.Fatalf("409 existing_app: %+v", rec)
	}
	if r.fc.count("POST", "/api/apps") != 1 {
		t.Fatalf("%d POSTs, want only the one answered 409", r.fc.count("POST", "/api/apps"))
	}
}

func TestCheckerUnprocessableIsAnErrorWithoutRetries(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.fc.script["POST /api/apps"] = []int{http.StatusUnprocessableEntity}
	r.create("promo")
	r.run()
	rec, _ := r.rec("promo")
	if rec.State != checkerStError || !strings.Contains(rec.LastError, "422") || rec.Attempts != 0 {
		t.Fatalf("422: %+v", rec)
	}
	r.clock.Advance(2 * time.Hour)
	r.run()
	if r.fc.count("POST", "/api/apps") != 1 {
		t.Fatal("a 422 was retried")
	}
	noSecrets(t, "last_error", rec.LastError)
	r.create("promo")
	if rec, _ = r.rec("promo"); rec.State != checkerStQueued || rec.Attempts != 0 || rec.LastError != "" {
		t.Fatalf("retry button: %+v", rec)
	}
	r.run()
	if rec, _ = r.rec("promo"); rec.State != checkerStLinked {
		t.Fatalf("after the retry button: %+v", rec)
	}
}

func TestCheckerServerErrorsBackOffThenFail(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.fc.script["POST /api/apps"] = []int{500, 502, 503}
	r.create("promo")
	for i, wait := range []time.Duration{time.Minute, 5 * time.Minute} {
		r.run()
		rec, _ := r.rec("promo")
		if rec.State != checkerStQueued || rec.Attempts != i+1 {
			t.Fatalf("try %d: %+v", i+1, rec)
		}
		if got := checkerParseTime(rec.NextAttemptAt).Sub(r.clock.Now()); got < wait-5*time.Second || got > wait {
			t.Fatalf("try %d waits %v, want %v", i+1, got, wait)
		}
		r.clock.Advance(wait - time.Second)
		r.run()
		if r.fc.count("POST", "/api/apps") != i+1 {
			t.Fatalf("try %d: the record was sent a second early", i+1)
		}
		r.clock.Advance(time.Second)
	}
	r.run()
	rec, _ := r.rec("promo")
	if rec.State != checkerStError || rec.Attempts != 3 || !strings.Contains(rec.LastError, "503") {
		t.Fatalf("after three tries: %+v", rec)
	}
	if r.fc.count("POST", "/api/apps") != 3 {
		t.Fatalf("%d POSTs, want 3", r.fc.count("POST", "/api/apps"))
	}
}

func TestCheckerTooManyRequestsWaitsForRetryAfter(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.fc.script["GET /api/apps"] = []int{http.StatusTooManyRequests}
	r.fc.retryAfter = "600"
	r.create("promo")
	r.run()
	rec, _ := r.rec("promo")
	if got := checkerParseTime(rec.NextAttemptAt).Sub(r.clock.Now()); rec.State != checkerStQueued || got < 9*time.Minute {
		t.Fatalf("429: %+v waits %v", rec, got)
	}
}

func TestCheckerRefusedTokenPausesTheQueueUntilSaved(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.fc.script["GET /api/apps"] = []int{http.StatusUnauthorized}
	r.create("promo")
	r.create("betclic")
	r.run()
	rec, _ := r.rec("promo")
	if rec.State != checkerStError || !strings.Contains(rec.LastError, "401") {
		t.Fatalf("401: %+v", rec)
	}
	if !r.s.checkerView().Paused {
		t.Fatal("the queue is not paused")
	}
	sent := r.fc.total()
	r.run()
	if r.fc.total() != sent {
		t.Fatal("a paused queue sent a request")
	}
	if other, _ := r.rec("betclic"); other.State != checkerStQueued {
		t.Fatalf("the other record is %s", other.State)
	}
	r.settings(map[string]any{"token": chkToken})
	if r.s.checkerView().Paused {
		t.Fatal("saving the settings must lift the pause")
	}
	r.run()
	if other, _ := r.rec("betclic"); other.State != checkerStLinked {
		t.Fatalf("after the pause: %s", other.State)
	}
}

func TestCheckerRestartContinuesSendingAndQueued(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	now := r.clock.Now()
	tenant := chkBase + "|" + chkTenant
	later := checkerStamp(now.Add(10 * time.Minute))
	file := map[string]any{"version": 1, "apps": map[string]any{
		"promo": map[string]any{
			"keyword": "SpinDog", "bundle_id": "com.playcobra.fasterthink", "name": "11381 - SpinDog",
			"state": "sending", "tenant": tenant, "next_attempt_at": checkerStamp(now),
		},
		"betclic": map[string]any{
			"keyword": "Betclic", "bundle_id": "com.findgamestore.findpairapp", "name": "11382 - Betclic",
			"state": "queued", "tenant": tenant, "next_attempt_at": later, "attempts": 1,
		},
	}}
	raw, _ := json.Marshal(file)
	r2 := newCheckerRig(t, rigSpec{dir: r.dir, fc: r.fc, clock: r.clock, files: map[string]string{"checker-apps.json": string(raw)}})
	if rec, _ := r2.rec("promo"); rec.State != checkerStQueued {
		t.Fatalf("a sending record must come back queued, got %s", rec.State)
	}
	r2.run()
	if rec, _ := r2.rec("promo"); rec.State != checkerStLinked {
		t.Fatalf("promo after the restart: %s", rec.State)
	}
	if rec, _ := r2.rec("betclic"); rec.State != checkerStQueued || r2.fc.count("POST", "/api/apps") != 1 {
		t.Fatalf("betclic was sent before its time: %+v", rec)
	}
	r2.clock.Advance(10 * time.Minute)
	r2.run()
	if rec, _ := r2.rec("betclic"); rec.State != checkerStLinked {
		t.Fatalf("betclic after its time: %s", rec.State)
	}
}

func TestCheckerThirtyFirstCreationWaitsAnHour(t *testing.T) {
	groups := make([]config.Group, 31)
	for i := range groups {
		groups[i] = config.Group{ID: fmt.Sprintf("g%d", i+1), Num: 100 + i, Name: fmt.Sprintf("❌ Key%d - Title - com.app.key%d", i+1, i+1)}
	}
	r := newCheckerRig(t, rigSpec{groups: groups})
	r.enable()
	for _, g := range groups {
		r.create(g.ID)
	}
	r.run()
	if n := r.fc.count("POST", "/api/apps"); n != checkerMaxPosts {
		t.Fatalf("%d POSTs in the first hour, want %d", n, checkerMaxPosts)
	}
	last, _ := r.rec("g31")
	if last.State != checkerStQueued {
		t.Fatalf("the 31st record is %s", last.State)
	}
	if got := checkerParseTime(last.NextAttemptAt).Sub(r.clock.Now()); got < 30*time.Minute || got > time.Hour+time.Minute {
		t.Fatalf("the 31st creation waits %v", got)
	}
	if v := r.s.checkerView().Queue; v.CreatedHour != checkerMaxPosts || v.Queued != 1 {
		t.Fatalf("queue view %+v", v)
	}
	r.clock.Advance(time.Hour + 2*time.Minute)
	r.run()
	if rec, _ := r.rec("g31"); rec.State != checkerStLinked || r.fc.count("POST", "/api/apps") != 31 {
		t.Fatalf("g31 after the hour: %+v", rec)
	}
}

func TestCheckerDeletedCampaignGetsNoPost(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.create("promo")
	r.setGroups(defaultCheckerGroups()[1:])
	r.run()
	if r.fc.total() != 0 {
		t.Fatal("a record of a deleted campaign was sent")
	}
	r.setGroups(defaultCheckerGroups())
	r.fc.onList = func() {
		r.fc.onList = nil
		if err := r.groups.Save(context.Background(), defaultCheckerGroups()[1:]); err != nil {
			t.Error(err)
		}
	}
	r.run()
	if n := r.fc.count("POST", "/api/apps"); n != 0 {
		t.Fatalf("a campaign deleted between the lookup and the POST got a POST")
	}
	if rec, _ := r.rec("promo"); rec.State != checkerStQueued {
		t.Fatalf("record %s", rec.State)
	}
}

func TestCheckerApplicationIsLinkedToOneCampaignOnly(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.create("promo")
	r.run()
	r.fc.conflict = &fcApp{ID: "app-1", Bundle: "com.playcobra.fasterthink", Status: "live"}
	r.create("betclic")
	r.run()
	rec, _ := r.rec("betclic")
	if rec.State != checkerStError || !strings.Contains(rec.LastError, "11381") || rec.AppID != "" {
		t.Fatalf("a second link to the same application: %+v", rec)
	}
}

func TestCheckerQueueSurvivesAWriteFailureOfTheFile(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.create("promo")
	bad := filepath.Join(r.dir, "gone", "checker-apps.json")
	r.s.cfg.CheckerAppsFile = bad
	r.run()
	if r.fc.total() != 0 {
		t.Fatal("a record that cannot be saved was sent")
	}
	if rec, _ := r.rec("promo"); rec.State != checkerStQueued {
		t.Fatalf("record %s", rec.State)
	}
	if _, err := os.Stat(bad); err == nil {
		t.Fatal("the file appeared")
	}
}

func TestCheckerRecordsFileIsPrivate(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.create("promo")
	st, err := os.Stat(filepath.Join(r.dir, "checker-apps.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("checker-apps.json: %v %v", st, err)
	}
}
