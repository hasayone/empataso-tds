package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/security"
)

// TestAIPermsLevels checks the level of each resource under every preset, the cut of domains to
// read, the normalization of a stored file and the operations each level allows.
func TestAIPermsLevels(t *testing.T) {
	cases := []struct {
		perms  aiPerms
		offers string
		domain string
	}{
		{aiPerms{Preset: aiPresetRead}, aiPermRead, aiPermRead},
		{aiPerms{Preset: aiPresetEdit}, aiPermEdit, aiPermRead},
		{aiPerms{Preset: aiPresetFull}, aiPermDelete, aiPermRead},
		{aiNormalizePerms(aiPerms{}), aiPermEdit, aiPermRead},
		{aiNormalizePerms(aiPerms{Preset: "root"}), aiPermEdit, aiPermRead},
		{aiNormalizePerms(aiPerms{Preset: aiPresetCustom, Resources: map[string]string{"offers": aiPermNone, "domains": aiPermDelete}}), aiPermNone, aiPermRead},
		{aiNormalizePerms(aiPerms{Preset: aiPresetCustom, Resources: map[string]string{"offers": "write"}}), aiPermRead, aiPermRead},
	}
	for i, c := range cases {
		if got := c.perms.level("offers"); got != c.offers {
			t.Errorf("case %d offers = %s, want %s", i, got, c.offers)
		}
		if got := c.perms.level("domains"); got != c.domain {
			t.Errorf("case %d domains = %s, want %s", i, got, c.domain)
		}
	}
	custom := aiNormalizePerms(aiPerms{Preset: aiPresetCustom, Resources: map[string]string{"campaigns": aiPermDelete, "junk": aiPermDelete}})
	if len(custom.Resources) != len(aiPermResources) || custom.Resources["junk"] != "" || custom.Resources["landings"] != aiPermRead {
		t.Fatalf("custom normalized: %v", custom.Resources)
	}
	if n := aiNormalizePerms(aiPerms{Preset: aiPresetFull, Resources: map[string]string{"offers": aiPermNone}}); n.Resources != nil {
		t.Fatalf("a preset keeps no resources: %v", n)
	}

	allow := []struct {
		perms   aiPerms
		res, op string
		want    bool
	}{
		{aiPerms{Preset: aiPresetRead}, "offers", "", true},
		{aiPerms{Preset: aiPresetRead}, "offers", aiOpUpdate, false},
		{aiPerms{Preset: aiPresetEdit}, "offers", aiOpCreate, true},
		{aiPerms{Preset: aiPresetEdit}, "campaigns", aiOpUpdate, true},
		{aiPerms{Preset: aiPresetEdit}, "campaigns", aiOpDelete, false},
		{aiPerms{Preset: aiPresetFull}, "landings", aiOpDelete, true},
		{aiPerms{Preset: aiPresetFull}, "domains", aiOpUpdate, false},
		{aiPerms{Preset: aiPresetRead}, "stats", "", true},
		{aiPerms{Preset: aiPresetFull}, "stats", aiOpUpdate, false},
		{aiPerms{Preset: aiPresetFull}, "offers", "merge", false},
		{custom, "offers", "", true},
		{custom, "campaigns", aiOpDelete, true},
		{aiNormalizePerms(aiPerms{Preset: aiPresetCustom, Resources: map[string]string{"offers": aiPermNone}}), "offers", "", false},
	}
	for _, c := range allow {
		if got := c.perms.allows(c.res, c.op); got != c.want {
			t.Errorf("%+v allows(%s, %q) = %v, want %v", c.perms, c.res, c.op, got, c.want)
		}
	}
}

// TestAIPermsClean checks a PUT accepts the presets and a valid custom map and refuses an unknown
// preset, resource or level and a level above the highest one of the resource.
func TestAIPermsClean(t *testing.T) {
	for _, p := range []aiPerms{
		{Preset: aiPresetRead}, {Preset: aiPresetEdit}, {Preset: aiPresetFull},
		{Preset: aiPresetCustom, Resources: map[string]string{"offers": aiPermDelete, "domains": aiPermNone}},
	} {
		if _, err := aiCleanPerms(p); err != nil {
			t.Errorf("%+v refused: %v", p, err)
		}
	}
	for _, p := range []aiPerms{
		{Preset: ""}, {Preset: "admin"},
		{Preset: aiPresetCustom, Resources: map[string]string{"users": aiPermRead}},
		{Preset: aiPresetCustom, Resources: map[string]string{"offers": "write"}},
		{Preset: aiPresetCustom, Resources: map[string]string{"domains": aiPermEdit}},
	} {
		_, err := aiCleanPerms(p)
		var ae *aiError
		if !errors.As(err, &ae) || ae.code != aiErrBadRequest || ae.Params["field"] != "perms" || ae.Params["reason"] != aiReasonInvalid {
			t.Errorf("%+v: %v", p, err)
		}
	}
	st, err := aiMergeSettings(aiNormalize(aiSettings{}), aiSettingsPut{Perms: &aiPerms{Preset: aiPresetRead}})
	if err != nil || st.Perms.Preset != aiPresetRead {
		t.Fatalf("merge: %+v %v", st.Perms, err)
	}
	if aiNormalize(aiSettings{}).Perms.Preset != aiPresetEdit {
		t.Fatal("the default of the beta is edit without delete")
	}
}

// aiToolNames returns the names of tools.
func aiToolNames(tools []aiTool) []string {
	out := make([]string, len(tools))
	for i, tl := range tools {
		out[i] = tl.Name
	}
	return out
}

// TestAIToolsForIntersection checks the tools of a turn are the intersection of the assistant
// permissions and the access of the user: an administrator gets what the preset allows, a user
// with read-only offers gets no offer writes even under the full preset.
func TestAIToolsForIntersection(t *testing.T) {
	env := newAIToolEnv(t)
	ctxOf := func(sess security.Session) context.Context {
		return context.WithValue(context.Background(), sessionKey, sess)
	}
	names := func(sess security.Session, p aiPerms) []string {
		return aiToolNames(env.s.aiToolsFor(ctxOf(sess), sess, "", p))
	}
	has := func(list []string, want ...string) bool {
		for _, w := range want {
			if !slices.Contains(list, w) {
				return false
			}
		}
		return true
	}
	read := names(env.root, aiPerms{Preset: aiPresetRead})
	if !has(read, "list_offers", "list_logs", "list_domains") || slices.ContainsFunc(read, func(n string) bool {
		tl, _ := aiToolNamed(n)
		return tl.Op != ""
	}) {
		t.Fatalf("read preset: %v", read)
	}
	edit := names(env.root, aiPerms{Preset: aiPresetEdit})
	if !has(edit, "create_offer", "update_offer", "update_campaign", "create_campaign") || has(edit, "delete_offer") || has(edit, "delete_campaign") {
		t.Fatalf("edit preset: %v", edit)
	}
	if full := names(env.root, aiPerms{Preset: aiPresetFull}); !has(full, "delete_offer", "delete_campaign", "delete_landing") {
		t.Fatalf("full preset: %v", full)
	}
	custom := aiNormalizePerms(aiPerms{Preset: aiPresetCustom, Resources: map[string]string{"offers": aiPermNone, "campaigns": aiPermDelete}})
	if got := names(env.root, custom); has(got, "list_offers") || has(got, "get_offer") || !has(got, "delete_campaign", "list_landings") || has(got, "update_landing") {
		t.Fatalf("custom: %v", got)
	}

	user := env.limitedSession(t, "reader", security.Access{
		Sections: []string{"offers", "landings"},
		Grants: map[string]security.Grant{
			"offers":   {Mode: security.ModeRead},
			"landings": {Mode: security.ModeFull},
		},
	})
	got := names(user, aiPerms{Preset: aiPresetFull})
	if !has(got, "list_offers", "create_landing", "update_landing", "delete_landing") || has(got, "create_offer") || has(got, "update_offer") || has(got, "delete_offer") {
		t.Fatalf("user with read-only offers under full: %v", got)
	}
	if got := names(user, aiPerms{Preset: aiPresetEdit}); has(got, "delete_landing") || !has(got, "update_landing") {
		t.Fatalf("user under edit: %v", got)
	}
}

// TestAIApplyForbidden checks an apply that the stored permissions no longer allow is 403
// ai_forbidden and changes nothing, while a revert of an applied change works under any preset.
func TestAIApplyForbidden(t *testing.T) {
	e := newChgEnv(t)
	setPerms := func(preset string) {
		t.Helper()
		if err := os.WriteFile(e.s.cfg.AIFile, []byte(`{"perms":{"preset":"`+preset+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	upd := e.proposeIn(t, "c1", "m1", "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Renamed"}}`)
	del := e.proposeIn(t, "c1", "m1", "offers", aiOpDelete, `{"id":"o9"}`)
	before := e.hashOf(t, "offers", "o9")

	setPerms(aiPresetEdit)
	_, err := e.s.applyChange(context.Background(), e.root, del.ID, aiApplyOpts{ConfirmDelete: true})
	var ae *aiError
	if !errors.As(err, &ae) || ae.status != http.StatusForbidden || ae.code != aiErrForbidden ||
		ae.Params["resource"] != "offers" || ae.Params["op"] != aiOpDelete {
		t.Fatalf("delete under edit: %v", err)
	}
	if e.hashOf(t, "offers", "o9") != before || e.status(t, del.ID) != aiChangeProposed {
		t.Fatal("a refused apply changed something")
	}
	e.mustApply(t, upd.ID)

	setPerms(aiPresetRead)
	if _, err := e.s.applyChange(context.Background(), e.root, del.ID, aiApplyOpts{ConfirmDelete: true}); !errors.As(err, &ae) || ae.code != aiErrForbidden {
		t.Fatalf("delete under read: %v", err)
	}
	e.mustRevert(t, upd.ID)
	if e.status(t, upd.ID) != aiChangeReverted {
		t.Fatal("a revert is the right of the person and is not limited by the preset")
	}
}

// TestAITurnRefusesForbiddenTool checks a call of a registry tool the turn was not given answers
// the model with ai_forbidden and sends the panel a tool event with the code and the params.
func TestAITurnRefusesForbiddenTool(t *testing.T) {
	rec := httptest.NewRecorder()
	turn := &aiTurn{tools: map[string]aiTool{}, sse: newAISSE(rec)}
	text, err := turn.refuseTool(aiToolCall{Name: "delete_offer"})
	if err != nil || !strings.HasPrefix(text, aiErrForbidden+":") {
		t.Fatalf("model text %q %v", text, err)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: tool", `"code":"ai_forbidden"`, `"resource":"offers"`, `"op":"delete"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream %q lacks %s", body, want)
		}
	}
	if text, _ := turn.refuseTool(aiToolCall{Name: "drop_database"}); text != "unknown_tool: there is no such tool" {
		t.Fatalf("unknown tool: %q", text)
	}
}
