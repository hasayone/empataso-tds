package admin

import (
	"strings"
	"testing"
)

// TestSPAAIIntegrationBlocks checks the AI settings card is split into the tabs of AI_TABS: each block has its tab and its panel with
// the fields that belong to it, the open tab is kept in localStorage, Save sits under the blocks and is hidden on Usage, the key
// errors land in the Connection block and a refused save opens the block of its field.
func TestSPAAIIntegrationBlocks(t *testing.T) {
	html := spaSource()
	must(t, html, "const AI_TABS=['conn','models','prices','limits','perm','usage'], AI_TAB_KEY='emtds-ai-tab';", "the six blocks in order")
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
		"prices": {`<div id="ai-prices"></div>`, `id="ai-prices-none"`, "{id:'ai-unknown',"},
		"limits": {"${limits}", "{id:'ai-nolimit-warn',"},
		"perm":   {`id="ai-perm-preset"`, `<div id="ai-perm-grid"></div>`},
		"usage":  {"aiUseCardsHTML(cur)", "tr('ai.set.usage.none')"},
	} {
		body := pane(id)
		for _, n := range needles {
			must(t, body, n, "block "+id)
		}
	}
	mustNot(t, pane("usage"), `id="ai-save"`, "Save is not inside a block")
	must(t, panel, `<div id="ai-savebox">`, "one Save under the blocks")
	for _, needle := range []string{
		"try{localStorage.setItem(AI_TAB_KEY,tab)}catch(e){}", "savedTab=localStorage.getItem(AI_TAB_KEY)||''", "selectTab(savedTab);",
		"q('#ai-savebox',host).hidden=tab==='usage';", "selectTab('limits');", "selectTab('prices');",
		"bar.scrollLeft=Math.max(0,left-(bar.clientWidth-b.offsetWidth)/2);",
		"selectTab(refuse==='keys'?'conn':'models');", "if(tab==='conn') show(aiErrOf(err),'err');",
		"const showCat=(text,kind)=>calloutSet(q('#ai-cat-status',host),text,kind);",
	} {
		must(t, panel, needle, "tab behaviour")
	}
	for _, k := range []string{"ai.set.tab.conn", "ai.set.tab.models", "ai.set.tab.prices", "ai.set.tab.limits", "ai.set.tab.perm", "ai.set.tab.usage", "ai.set.prices.none"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	assistantNode(t, []string{"aiSettingsTab"}, `const AI_LIMITS = [['max_steps'], ['daily_usd']];`, `
eq(aiSettingsTab('ai_bad_key', ''), 'conn', 'a rejected key is shown by the key');
eq(aiSettingsTab('ai_bad_request', 'keys'), 'conn', 'a key of another provider too');
eq(aiSettingsTab('ai_bad_model', 'model'), 'models', 'the default model');
eq(aiSettingsTab('ai_bad_request', 'prices'), 'prices', 'a price');
eq(aiSettingsTab('ai_bad_limit', 'daily_usd'), 'limits', 'a limit');
eq(aiSettingsTab('ai_bad_request', 'perms'), 'perm', 'the permissions');
eq(aiSettingsTab('internal', ''), '', 'no field stays where it is');
`)
}
