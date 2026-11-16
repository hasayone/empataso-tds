package admin

import "testing"

// TestAssistantWidgetPerms checks the AI integration has the "Assistant permissions" block: a preset select, a level per
// resource that changes only under custom, the perms field of the save, and the translated ai_forbidden refusal.
func TestAssistantWidgetPerms(t *testing.T) {
	html := spaSource()
	panel := assistantFn(html, "aiPanel")
	must(t, panel, `<select id="ai-perm-preset">`, "the preset select")
	must(t, panel, `<div id="ai-perm-grid"></div>`, "the level table")
	must(t, panel, "perms:perm});", "the save sends the permissions")
	must(t, assistantFn(html, "aiSettingsBody"), "if(perms) body.perms=perms;", "the body carries perms only when they differ")
	must(t, html, "ai_forbidden:'ai.chat.err.ai_forbidden'", "ai_forbidden has a text")
	must(t, assistantFn(html, "aiOnEvent"), "d.code?' — '+aiErrText(d.code,'',d.params)", "a refused tool is worded in the step log")
	for _, k := range []string{"ai.set.perm", "ai.set.perm.hint", "ai.set.perm.preset", "ai.set.perm.preset.read", "ai.set.perm.preset.edit",
		"ai.set.perm.preset.full", "ai.set.perm.preset.custom", "ai.set.perm.res", "ai.set.perm.level", "ai.set.perm.lv.none",
		"ai.set.perm.lv.read", "ai.set.perm.lv.edit", "ai.set.perm.lv.delete", "ai.chat.err.ai_forbidden", "ai.chat.err.ai_forbidden.op"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}

	assistantNode(t, []string{"aiPermResName", "aiPermLevels", "aiPermGridHTML", "aiPermBody"}, `
const AI_PERM_PRESETS=['read','edit','full','custom'], AI_PERM_LEVELS=['none','read','edit','delete'];
const AI_PERM_ACC={affiliate_networks:'networks',traffic_sources:'sources'};
const USER_ACCESS_RESOURCES=['campaigns','offers','landings','sources','networks','domains'];
const tr=k=>k, esc=s=>String(s), aiVal=v=>v==null||v===''?'—':String(v);
const RES=[{name:'offers',max:'delete'},{name:'affiliate_networks',max:'delete'},{name:'domains',max:'read'}];
`, `
eq(aiPermLevels('edit',{},RES), {offers:'edit',affiliate_networks:'edit',domains:'read'}, 'edit is cut to read for domains');
eq(aiPermLevels('full',{},RES), {offers:'delete',affiliate_networks:'delete',domains:'read'}, 'full gives the highest level');
eq(aiPermLevels('read',{},RES).offers, 'read', 'read only');
eq(aiPermLevels('custom',{offers:'none'},RES), {offers:'none',affiliate_networks:'read',domains:'read'}, 'custom keeps its levels, read by default');
eq(aiPermResName('affiliate_networks'), 'acc.res.networks', 'the label of the role editor');
const grid=aiPermGridHTML(aiPermLevels('edit',{},RES),RES,'edit');
eq(grid.indexOf(' disabled')>0, true, 'a preset locks the selects');
eq(aiPermGridHTML(aiPermLevels('custom',{},RES),RES,'custom').indexOf(' disabled'), -1, 'custom unlocks them');
eq(/data-aiperm="domains"[^>]*>(?:(?!<\/select>).)*value="edit"/.test(grid), false, 'domains offer no write level');
const cur={perms:{preset:'edit',resources:{offers:'edit',affiliate_networks:'edit',domains:'read'}},perm_resources:RES};
eq(aiPermBody(cur,{preset:'edit',custom:{}}), null, 'unchanged preset is not sent');
eq(aiPermBody(cur,{preset:'read',custom:{}}), {preset:'read'}, 'a preset goes alone');
eq(aiPermBody(cur,{preset:'custom',custom:{offers:'delete'}}), {preset:'custom',resources:{offers:'delete',affiliate_networks:'read',domains:'read'}}, 'custom goes with every level');
`)
}
