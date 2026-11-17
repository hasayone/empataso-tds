package admin

import (
	"strings"
	"testing"
)

// TestSPAApiMenu checks the API keys entry of the settings tabs and of the user menu: both come from SETTINGS_TABS under
// the same api_keys right, the entry opens #/settings/keys and sits after the settings entry, the sections switcher
// follows the tab entries, and the keys tab leads to the API docs with the OpenAPI file in a new tab.
// t is the test handle. It returns nothing and fails when the entry, its guard or the docs link is missing.
func TestSPAApiMenu(t *testing.T) {
	html := spaSource()
	tabs := spaBetween(html, "const SETTINGS_TABS=[", "\n];")
	item := "{id:'keys', key:'prof.keys', route:'settings/keys', icon:'link', allow:()=>sectionAllowed(ME,'api_keys')}"
	must(t, tabs, item, "API keys tab guarded by the api_keys right")
	if profileAt, keysAt := strings.Index(tabs, "id:'profile'"), strings.Index(tabs, "id:'keys'"); profileAt < 0 || keysAt < profileAt {
		t.Errorf("the API keys tab must sit after the settings tab")
	}

	menu := spaFunctionLine(html, "userMenuItems")
	tabsAt := strings.Index(menu, "adminMenuEntries()")
	boardsAt := strings.Index(menu, "appendBoardUserMenuItems(items)")
	if tabsAt < 0 || boardsAt < tabsAt {
		t.Errorf("the tab entries must sit before the sections switcher")
	}
	mustNot(t, menu, "tr('menu.api')", "the menu has no entry of its own for the API keys")
	must(t, html, "\n  link:'", "link icon in IC")

	page := spaFunctionLine(html, "profile")
	must(t, page, "keys:apiKeys", "keys route opens the API keys page")
	must(t, page, "if(!cur.allow()){ go('settings'); return; }", "a tab without the right leads back to settings")
	keys := spaFunctionLine(html, "apiKeys")
	must(t, keys, `id="key-docs"`, "keys tab has the docs button")
	must(t, keys, "go('settings/docs')", "docs button opens the API docs")
	must(t, spaFunctionLine(html, "apiDocs"), `<a href="/api/openapi.json" target="_blank" rel="noopener">`, "docs open OpenAPI in a new tab")

	for _, lang := range []string{"en", "ru", "uk"} {
		if !trKeyDefined(html, lang, "prof.keys") {
			t.Errorf("tr(%q) missing for lang %q", "prof.keys", lang)
		}
	}
	if strings.Contains(html, "'menu.api':") {
		t.Errorf("menu.api is unused and must be removed from the dictionaries")
	}
}
