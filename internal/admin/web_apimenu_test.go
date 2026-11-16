package admin

import (
	"strings"
	"testing"
)

// TestSPAApiMenu checks the API item of the profile menu: it is shown under the same right as the API keys tab,
// it opens #/settings/keys, it sits above the sections divider, and the keys tab leads to the API docs
// with the OpenAPI file in a new tab.
// t is the test handle. It returns nothing and fails when the item, its guard or the docs link is missing.
func TestSPAApiMenu(t *testing.T) {
	html := spaSource()
	menu := spaFunctionLine(html, "userMenuItems")
	item := "if(sectionAllowed(ME,'api_keys')) items.push({label:tr('menu.api'),icon:'link',on:()=>go('settings/keys')});"
	must(t, menu, item, "API item guarded by the api_keys right")
	usersAt := strings.Index(menu, "tr('menu.users')")
	apiAt := strings.Index(menu, "tr('menu.api')")
	boardsAt := strings.Index(menu, "appendBoardUserMenuItems(items)")
	if usersAt < 0 || apiAt < usersAt || boardsAt < apiAt {
		t.Errorf("API item must sit after users and before the sections divider")
	}
	must(t, html, "\n  link:'", "link icon in IC")

	page := spaFunctionLine(html, "profile")
	must(t, page, "const showKeys=sectionAllowed(ME,'api_keys');", "keys tab uses the same right")
	must(t, page, "if(!showKeys){ go('settings'); return; }", "keys route is closed without the right")
	keys := spaFunctionLine(html, "apiKeys")
	must(t, keys, `id="key-docs"`, "keys tab has the docs button")
	must(t, keys, "go('settings/docs')", "docs button opens the API docs")
	must(t, spaFunctionLine(html, "apiDocs"), `<a href="/api/openapi.json" target="_blank" rel="noopener">`, "docs open OpenAPI in a new tab")

	for _, lang := range []string{"en", "ru", "uk"} {
		if !trKeyDefined(html, lang, "menu.api") {
			t.Errorf("tr(%q) missing for lang %q", "menu.api", lang)
		}
	}
	if c := strings.Count(html, "'menu.api':"); c != 3 {
		t.Errorf("menu.api is defined %d times, want once per language", c)
	}
}
