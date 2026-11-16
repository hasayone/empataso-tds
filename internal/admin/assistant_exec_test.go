package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

// asToolError reports whether err is an *aiToolError and stores it in target.
func asToolError(err error, target **aiToolError) bool {
	return errors.As(err, target)
}

// runTool runs the named read tool for sess with args and returns the result and the error.
func (e *aiToolEnv) runTool(t *testing.T, sess security.Session, name, args string) (json.RawMessage, error) {
	t.Helper()
	tool, ok := aiToolNamed(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return e.s.aiRunRead(context.Background(), sess, "", tool, json.RawMessage(args))
}

// wantToolCode fails unless err is an *aiToolError with the given code.
func wantToolCode(t *testing.T, label string, err error, code string) {
	t.Helper()
	var te *aiToolError
	if !asToolError(err, &te) || te.Code != code {
		t.Errorf("%s: error %v, want code %s", label, err, code)
	}
}

func TestAssistantExecRefusesRoutesWithoutTheMark(t *testing.T) {
	env := newAIToolEnv(t)
	ctx := context.Background()
	if env.s.inner["GET /api/users"] == nil {
		t.Fatal("the test needs a registered route outside the allowlist")
	}
	forged := aiTool{Name: "list_users", Pattern: "GET /api/users", Method: "GET", Path: "/api/users", Mode: asstRead}
	if _, err := env.s.aiRunRead(ctx, env.root, "", forged, nil); !errors.Is(err, errAIToolForbidden) {
		t.Errorf("forged tool: %v", err)
	}
	list, _ := aiToolNamed("list_offers")
	moved := list
	moved.Pattern = "GET /api/users"
	if _, err := env.s.aiRunRead(ctx, env.root, "", moved, nil); !errors.Is(err, errAIToolForbidden) {
		t.Errorf("tool moved to another route: %v", err)
	}
	get, _ := aiToolNamed("get_offer")
	renamed := get
	renamed.Name = "list_offers"
	if _, err := env.s.aiRunRead(ctx, env.root, "", renamed, json.RawMessage(`{"id":"o1"}`)); !errors.Is(err, errAIToolForbidden) {
		t.Errorf("renamed tool: %v", err)
	}
	for _, name := range []string{"update_offer", "delete_campaign", "create_campaign"} {
		tool, _ := aiToolNamed(name)
		if _, err := env.s.aiRunRead(ctx, env.root, "", tool, json.RawMessage(`{"id":"o1","patch":{}}`)); !errors.Is(err, errAIToolForbidden) {
			t.Errorf("propose tool %s ran: %v", name, err)
		}
		asRead := tool
		asRead.Mode = asstRead
		if _, err := env.s.aiRunRead(ctx, env.root, "", asRead, json.RawMessage(`{"id":"o1","patch":{}}`)); !errors.Is(err, errAIToolForbidden) {
			t.Errorf("propose tool %s passed as read: %v", name, err)
		}
	}
	offers, _ := env.s.cfg.Offers.List(ctx)
	if len(offers) == 0 || offers[0].Name != "Alpha" {
		t.Errorf("a refused call changed the data: %+v", offers)
	}
}

func TestAssistantExecArguments(t *testing.T) {
	env := newAIToolEnv(t)
	cases := []struct{ tool, args, code string }{
		{"get_offer", `{}`, "bad_request"},
		{"get_offer", `{"id":""}`, "bad_request"},
		{"get_offer", `{"id":"../x"}`, "bad_request"},
		{"get_offer", `{"id":"a/b"}`, "bad_request"},
		{"get_offer", `{"id":["o1"]}`, "bad_request"},
		{"get_offer", `{"id":"o1","extra":1}`, "bad_request"},
		{"get_offer", `[1]`, "bad_request"},
		{"stats_breakdown", `{}`, "bad_request"},
		{"list_offers", `{"nope":1}`, "bad_request"},
		{"get_offer", `{"id":"missing"}`, "not_found"},
	}
	for _, c := range cases {
		_, err := env.runTool(t, env.root, c.tool, c.args)
		wantToolCode(t, c.tool+" "+c.args, err, c.code)
	}
	if res, err := env.runTool(t, env.root, "list_offers", `null`); err != nil || !strings.Contains(string(res), "Alpha") {
		t.Errorf("null arguments: %s %v", res, err)
	}
	if res, err := env.runTool(t, env.root, "get_offer", `{"id":"o1"}`); err != nil || !strings.Contains(string(res), `"id":"o1"`) {
		t.Errorf("get_offer: %s %v", res, err)
	}
}

func TestAssistantExecLimitIsCappedAtFifty(t *testing.T) {
	env := newAIToolEnv(t)
	for args, want := range map[string]int64{`{}`: 50, `{"limit":5000}`: 50, `{"limit":0}`: 50, `{"limit":-3}`: 50, `{"limit":5}`: 5, `{"limit":50}`: 50} {
		if _, err := env.runTool(t, env.root, "list_logs", args); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if got := env.stats.lastLimit.Load(); got != want {
			t.Errorf("list_logs %s: the journal got limit %d, want %d", args, got, want)
		}
	}
}

func TestAssistantExecResultTooLarge(t *testing.T) {
	env := newAIToolEnv(t)
	offers := make([]config.Offer, 100)
	for i := range offers {
		offers[i] = config.Offer{ID: fmt.Sprintf("big%d", i), Name: strings.Repeat("n", 100<<10), URL: "https://x.test/o", Status: true}
	}
	if err := env.s.cfg.Offers.Save(context.Background(), offers); err != nil {
		t.Fatal(err)
	}
	res, err := env.runTool(t, env.root, "list_offers", `{}`)
	if res != nil {
		t.Errorf("an over-size answer returned data: %.100s", res)
	}
	wantToolCode(t, "list_offers", err, aiErrResultTooLarge)
	if err == nil || !strings.Contains(err.Error(), "narrow filters") {
		t.Errorf("the error does not ask for narrower filters: %v", err)
	}
}

func TestAssistantExecWriterRefusesPastTheLimit(t *testing.T) {
	w := newAIBoundedWriter(10)
	if n, err := w.Write([]byte("12345")); n != 5 || err != nil {
		t.Fatalf("first write: %d %v", n, err)
	}
	if _, err := w.Write([]byte("123456")); err == nil || !w.over {
		t.Fatal("the write past the limit was accepted")
	}
	if _, err := w.Write([]byte("1")); err == nil {
		t.Error("a write after the overflow was accepted")
	}
	if w.status != 200 {
		t.Errorf("status %d", w.status)
	}
}

func TestAssistantExecCompressesToEightKiB(t *testing.T) {
	env := newAIToolEnv(t)
	offers := make([]config.Offer, 300)
	for i := range offers {
		offers[i] = config.Offer{ID: fmt.Sprintf("o%d", i), Name: strings.Repeat("n", 600), URL: "https://x.test/o", Status: true}
	}
	if err := env.s.cfg.Offers.Save(context.Background(), offers); err != nil {
		t.Fatal(err)
	}
	res, err := env.runTool(t, env.root, "list_offers", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) > aiResultMax {
		t.Errorf("result is %d bytes", len(res))
	}
	var got struct {
		Items     []map[string]any `json:"items"`
		Total     int              `json:"total"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
	if !got.Truncated || got.Total != 300 || len(got.Items) == 0 || len(got.Items) >= 300 {
		t.Errorf("truncated=%v total=%d items=%d", got.Truncated, got.Total, len(got.Items))
	}

	env.stats.bigRows = 200
	logs, err := env.runTool(t, env.root, "list_logs", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Total     int              `json:"total"`
		Rows      []map[string]any `json:"rows"`
		RowsTotal int              `json:"rows_total"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal(logs, &page); err != nil {
		t.Fatalf("logs result is not valid JSON: %v (%d bytes)", err, len(logs))
	}
	if len(logs) > aiResultMax || page.Total != 200 || page.RowsTotal != 200 || !page.Truncated || len(page.Rows) >= 200 {
		t.Errorf("logs: %d bytes, total=%d rows_total=%d truncated=%v rows=%d", len(logs), page.Total, page.RowsTotal, page.Truncated, len(page.Rows))
	}
}

func TestAssistantExecCompressKeepsSmallValuesAndShortensStrings(t *testing.T) {
	small := map[string]any{"a": []any{"x"}}
	if got, err := aiCompress(small, aiResultMax); err != nil || aiJSONLen(got) != aiJSONLen(map[string]any{"a": []any{"x"}}) {
		t.Errorf("a small value changed: %v %v", got, err)
	}
	long := map[string]any{"text": strings.Repeat("я", 6000)}
	got, err := aiCompress(long, aiResultMax)
	if err != nil {
		t.Fatal(err)
	}
	if n := aiJSONLen(got); n > aiResultMax {
		t.Errorf("%d bytes", n)
	}
	if text := got.(map[string]any)["text"].(string); !strings.HasSuffix(text, "…") || strings.ContainsRune(text, '\uFFFD') {
		t.Errorf("string not cut on a rune boundary: %q", text[len(text)-8:])
	}
	if _, err := aiCompress(strings.Repeat("x", 100), 10); err == nil {
		t.Error("an impossible limit was met")
	}
}

func TestAssistantExecStopsWhenTheUserIsGone(t *testing.T) {
	env := newAIToolEnv(t)
	create := func(login string) security.Session {
		if _, err := env.s.createUser(userDraft{Login: login, Password: "secret1", Type: security.UserAdmin, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		u, _ := env.s.users.ByLogin(login)
		return security.Session{User: login, UID: u.ID, PwFP: u.Fingerprint()}
	}
	disabled := create("adm1")
	if _, err := env.runTool(t, disabled, "list_offers", `{}`); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := env.s.users.Update(disabled.UID, func(u *security.User) error { u.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := env.runTool(t, disabled, "list_offers", `{}`); !errors.Is(err, errAISessionGone) {
		t.Errorf("disabled user: %v", err)
	}

	changed := create("adm2")
	if err := env.s.setUserPassword(changed.UID, "another-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.runTool(t, changed, "list_offers", `{}`); !errors.Is(err, errAISessionGone) {
		t.Errorf("new password: %v", err)
	}

	removed := create("adm3")
	if err := env.s.users.Remove(removed.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.runTool(t, removed, "list_offers", `{}`); !errors.Is(err, errAISessionGone) {
		t.Errorf("removed user: %v", err)
	}
	stale := security.Session{User: "admin", PwFP: "old-fingerprint"}
	if _, err := env.runTool(t, stale, "list_offers", `{}`); !errors.Is(err, errAISessionGone) {
		t.Errorf("old root password: %v", err)
	}
	api := env.root
	api.API = true
	if _, err := env.runTool(t, api, "list_offers", `{}`); !errors.Is(err, errAISessionGone) {
		t.Errorf("API session: %v", err)
	}
	if _, err := env.runTool(t, env.root, "list_offers", `{}`); err != nil {
		t.Errorf("the administrator is refused: %v", err)
	}
}

func TestAssistantExecHeavyReadsShareOneSlot(t *testing.T) {
	env := newAIToolEnv(t)
	release, err := aiAcquireHeavy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	tool, _ := aiToolNamed("stats_summary")
	start := time.Now()
	_, err = env.s.aiRunRead(ctx, env.root, "", tool, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < 50*time.Millisecond {
		t.Errorf("a heavy read ran while the slot was taken: %v", err)
	}
	if _, err := env.runTool(t, env.root, "list_offers", `{}`); err != nil {
		t.Errorf("a light read waited for the heavy slot: %v", err)
	}
	release()
	released = true
	if _, err := env.runTool(t, env.root, "stats_summary", `{}`); err != nil {
		t.Errorf("heavy read after release: %v", err)
	}
	for _, name := range []string{"stats_summary", "stats_timeseries", "stats_breakdown", "stats_performance", "list_logs", "list_postbacks", "get_report", "simulate_visitor"} {
		if tool, ok := aiToolNamed(name); !ok || !tool.heavy {
			t.Errorf("%s is not marked heavy", name)
		}
	}
	for _, name := range []string{"list_offers", "list_campaigns", "list_log_fields", "lookup_ip"} {
		if tool, _ := aiToolNamed(name); tool.heavy {
			t.Errorf("%s is marked heavy", name)
		}
	}
	select {
	case aiHeavySlot <- struct{}{}:
		<-aiHeavySlot
	default:
		t.Error("the heavy slot was not freed")
	}
}

func TestAssistantExecBoardHeaderGoesThroughBoardScope(t *testing.T) {
	env := newAIToolEnv(t)
	lim := env.limitedSession(t, "lim", security.Access{
		Sections: []string{"offers"},
		Grants:   map[string]security.Grant{"offers": {Mode: security.ModeFull}},
	})
	tool, _ := aiToolNamed("list_offers")
	if _, err := env.s.aiRunRead(context.Background(), lim, "someone-elses-board", tool, nil); err == nil {
		t.Error("a limited user opened a section it has no access to")
	} else {
		wantToolCode(t, "foreign board", err, "forbidden")
	}
	if _, err := env.s.aiRunRead(context.Background(), lim, "", tool, nil); err != nil {
		t.Errorf("no board: %v", err)
	}
	if _, err := env.s.aiRunRead(context.Background(), env.root, "any-board", tool, nil); err != nil {
		t.Errorf("the administrator is refused a section: %v", err)
	}
}

func TestAssistantExecToolErrorsAreRedacted(t *testing.T) {
	env := newAIToolEnv(t)
	_, err := env.runTool(t, env.root, "get_offer", `{"id":"missing-`+aiCanaryCF+`"}`)
	if err == nil {
		t.Fatal("expected an error")
	}
	env.assertClean(t, "error", err.Error())
}

func TestAssistantExecSurvivesAHandlerPanic(t *testing.T) {
	env := newAIToolEnv(t)
	const pattern = "GET /api/offers/{id}"
	saved := env.s.inner[pattern]
	env.s.inner[pattern] = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom " + aiCanaryCF) })
	defer func() { env.s.inner[pattern] = saved }()
	_, err := env.runTool(t, env.root, "get_offer", `{"id":"o1"}`)
	wantToolCode(t, "panic", err, "internal")
	env.assertClean(t, "panic error", err.Error())
}

func TestAssistantExecStripFieldsAtAnyDepth(t *testing.T) {
	in := aiDecode([]byte(`[{"id":"g","postbacks":[{"url":"u"}],"streams":[{"curl":"c","b_curl":"b","name":"s"}],"Postback_IDs":["x"],"cost_token":"{p}"}]`))
	raw, _ := aiMarshal(aiStripFields(in, aiCampaignHiddenFields))
	text := string(raw)
	for _, gone := range []string{"postbacks", "curl", "b_curl", "Postback_IDs"} {
		if strings.Contains(text, gone) {
			t.Errorf("%s survived: %s", gone, text)
		}
	}
	for _, keep := range []string{`"id":"g"`, `"name":"s"`, `"cost_token":"{p}"`} {
		if !strings.Contains(text, keep) {
			t.Errorf("%s lost: %s", keep, text)
		}
	}
}
