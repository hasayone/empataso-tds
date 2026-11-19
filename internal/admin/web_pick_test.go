package admin

import "testing"

// TestPickSelectWired checks that the searchable select is one shared component on the shared
// combobox and that the entity pickers use it: campaigns in keywords, the flow editor and stream
// targets, offers, landings, sources, domains, networks, groups, roles and countries.
func TestPickSelectWired(t *testing.T) {
	html := spaSource()
	for _, fn := range []string{"function pickSearch(", "function pickSegments(", "function enhancePickSelect(", "function enhancePicks(", "function flowOpt("} {
		must(t, html, fn, fn)
	}
	must(t, html, "const PICK_DEBOUNCE_MS=250;", "typing pause before filtering")
	must(t, html, "const PICK_SELECT_LIMIT=100;", "long lists draw at most 100 rows")
	must(t, html, "const api=bindPickCombo(inp, {", "the select search runs on the shared combobox")
	must(t, html, "const hit=pickSearch(poolFor(q), q, lim);", "the shared combobox ranks with pickSearch")
	must(t, html, "trFill('pick.more', {n:hit.total-hit.items.length})", "a cut list says how many rows are left")
	must(t, html, ".observe(document.documentElement, {childList:true, subtree:true});", "selects marked data-pick are enhanced when drawn")
	must(t, html, "const hits=pickSearch(rows, want, 8).items;", "the board composer search ranks with pickSearch")
	for _, sel := range []string{
		`<select id="k-group" data-pick`,
		`<select id="kws-camp" data-pick`,
		`flowPickOpts(others,o.out),'data-pick')`,
		`flowPickOpts(others,g.out),'data-pick')`,
		`<select data-oid data-pick`,
		`<select data-lid data-pick`,
		`<select id="g_source" data-pick`,
		`<select id="g_domain" data-pick`,
		`<select id="fsource" data-pick`,
		`<select id="ffolder" data-pick`,
		`<select id="${kind}-group" data-pick`,
		`<select id="onet" data-pick`,
		`<select id="nr-group" data-pick`,
		`<select id="pb-host" data-pick`,
		`<select id="u-role" data-pick`,
		`<select id="key-role" data-pick`,
		`<select id="bmem-add-sel" data-pick`,
		`<select id="tv_country_list" data-pick`,
	} {
		must(t, html, sel, "searchable picker "+sel)
	}
	mustNot(t, html, `<input id="tv_country"`, "visitor check has no country code field")
	must(t, html, `const country=q('#tv_country_list');TV.country=country?country.value.trim():'';`, "visitor check reads the country from the list")
	must(t, html, `<label for="tv_country_list">${tr('tester.country')}</label><select id="tv_country_list"`, "the country list keeps the country label")
	mustNot(t, html, "gs.forEach(g=>s.insertAdjacentHTML(", "keyword campaigns are added in one pass, not one parse per campaign")
	for _, key := range []string{"pick.more", "pick.search"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("tr(%q) missing for lang %q", key, lang)
			}
		}
	}
}
