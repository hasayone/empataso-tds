package admin

import (
	"strings"
	"testing"
)

// TestSPAAIIntegrationBlocks checks the AI settings card is split into the tabs of AI_TABS: each block has its tab and its panel with
// the fields that belong to it, the open tab is kept in localStorage, Save sits under the blocks and stays on Usage for the budget fields, the key
// errors land in the Connection block and a refused save opens the block of its field.
func TestSPAAIIntegrationBlocks(t *testing.T) {
	html := spaSource()
	must(t, html, "const AI_TABS=['conn','models','prices','perm','docs','usage'], AI_TAB_KEY='emtds-ai-tab';", "the six blocks in order, the limits sit folded under the prices")
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	must(t, panel, `<div class="vseg intabs mt" role="tablist">`, "the tabs reuse the integration tab bar")
	must(t, panel, `data-aitab="${t}" aria-controls="ai-pane-${t}"`, "a tab names its panel")

	pane := func(id string) string {
		start := strings.Index(panel, `<div id="ai-pane-`+id+`" role="tabpanel"`)
		if start < 0 {
			t.Fatalf("no panel %q", id)
		}
		rest := panel[start+1:]
		if end := strings.Index(rest, `<div id="ai-pane-`); end >= 0 {
			return rest[:end]
		}
		return rest[:strings.Index(rest, `<div id="ai-savebox">`)]
	}
	for id, needles := range map[string][]string{
		"conn":   {`id="ai-enabled"`, `id="ai-provider"`, `id="ai-key"`, `id="ai-test"`, `id="ai-keydel"`, "{id:'ai-status',"},
		"models": {`<div id="ai-models"></div>`, "{id:'ai-cat-status',"},
		"prices": {`<div id="ai-prices"></div>`, `id="ai-prices-none"`, "{id:'ai-unknown',", `<details class="sec aipr-lim" id="ai-limits">`, "${limits}"},
		"perm":   {`id="ai-perm-preset"`, `<div id="ai-perm-grid"></div>`},
		"docs":   {"${AI_DOCS.map(d=>", `data-aidoc="${d[0]}"`, `id="ai-doc-n-${d[0]}"`, "tr('ai.set.docs.hint')"},
		"usage":  {"aiUseCardsHTML(cur)", "tr('ai.set.usage.none')", `<div class="g2" id="ai-daily">${dailyLimits}</div>`, "tr('ai.set.limits.day.hint')", "{id:'ai-nolimit-warn',"},
	} {
		body := pane(id)
		for _, n := range needles {
			must(t, body, n, "block "+id)
		}
	}
	mustNot(t, pane("usage"), `id="ai-save"`, "Save is not inside a block")
	mustNot(t, panel, "q('#ai-savebox',host).hidden=", "Save stays on Usage: the budget fields sit there")
	must(t, panel, `<div id="ai-savebox">`, "one Save under the blocks")
	for _, needle := range []string{
		"try{localStorage.setItem(AI_TAB_KEY,tab)}catch(e){}", "savedTab=localStorage.getItem(AI_TAB_KEY)||''", "selectTab(savedTab);",
		"selectTab(aiSettingsTab('',bad));", "if(!AI_DAILY.has(bad)) openLimits();", "selectTab('prices');",
		"bar.scrollLeft=Math.max(0,left-(bar.clientWidth-b.offsetWidth)/2);",
		"selectTab(refuse==='keys'?'conn':'models');", "if(tab==='conn') show(aiErrOf(err),'err');",
		"const showCat=(text,kind)=>calloutSet(q('#ai-cat-status',host),text,kind);",
	} {
		must(t, panel, needle, "tab behaviour")
	}
	for _, k := range []string{"ai.set.tab.conn", "ai.set.tab.models", "ai.set.tab.prices", "ai.set.limits", "ai.set.tab.perm", "ai.set.tab.usage", "ai.set.prices.none",
		"ai.set.tab.docs", "ai.set.docs.hint", "ai.set.doc.soul", "ai.set.doc.soul.hint", "ai.set.doc.memory", "ai.set.doc.memory.hint",
		"ai.set.doc.expertise", "ai.set.doc.expertise.hint", "ai.set.doc.size", "ai.set.doc.size.over"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	assistantNode(t, []string{"aiSettingsTab"}, `const AI_LIMITS = [['max_steps',0,0,0], ['daily_usd',0,1,1]], AI_DAILY = new Set(['daily_usd']);`, `
eq(aiSettingsTab('ai_bad_key', ''), 'conn', 'a rejected key is shown by the key');
eq(aiSettingsTab('ai_bad_request', 'keys'), 'conn', 'a key of another provider too');
eq(aiSettingsTab('ai_bad_model', 'model'), 'models', 'the default model');
eq(aiSettingsTab('ai_bad_request', 'prices'), 'prices', 'a price');
eq(aiSettingsTab('ai_bad_limit', 'max_steps'), 'prices', 'a per-turn limit is in the folded block of the prices');
eq(aiSettingsTab('ai_bad_limit', 'daily_usd'), 'usage', 'a daily limit sits under the usage cards');
eq(aiSettingsTab('ai_bad_request', 'perms'), 'perm', 'the permissions');
eq(aiSettingsTab('ai_bad_request', 'docs.memory'), 'docs', 'a document that is too long');
eq(aiSettingsTab('internal', ''), '', 'no field stays where it is');
`)
}
