package admin

import "testing"

// TestDashboardPreferences checks the structure of the dashboard preferences window:
// the gear sits right of the refresh button, the window edits the same account data
// as the "Who visits" gear and the page views, and every visible part of the dashboard
// is drawn from the stored sets.
func TestDashboardPreferences(t *testing.T) {
	html := spaSource()
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, `id="dash-refresh" title="${tr('Refresh')}" aria-label="${tr('Refresh')}">${icon('refresh')}</button><button type="button" class="icon" id="dash-prefs"`, "the gear is the icon button right after refresh")
	must(t, dash, "openDashPrefs(", "the gear opens the preferences window")
	must(t, dash, "dashRows('kpis')", "cards come from the stored set")
	must(t, dash, "dashRows('blocks')", "sections come from the stored set")
	must(t, dash, "dashPanelsPicker()", "the block gear shares the card picker with the window")
	must(t, dash, "dashPanelsPicked(m)", "the block gear reads the shared picker")
	must(t, dash, "if(q('#perfwrap')){", "a hidden performance block is not bound")
	must(t, dash, "const panelsGear=q('#dash-panels');", "a hidden visitors block is not bound")
	mustNot(t, dash, "saveUI(", "the window does not write the shared ui.json")

	prefs := spaFunctionLine(html, "openDashPrefs")
	must(t, prefs, "formModal(tr('dash.pref.title')", "the window uses the shared form window")
	must(t, prefs, "dashSetStored(", "only a non-default set is stored")
	must(t, prefs, "DASH_VIEW.panels=dashPanelsPicked(", "the window writes the same panels value as the block gear")
	must(t, prefs, "flushPageViews()", "the window saves to the account views")
	must(t, prefs, "tr('cols.restore')", "restore defaults button")
	must(t, prefs, "fill(null)", "restore refills the window with the defaults")
	mustNot(t, prefs, "fetch(", "the window has no persistence of its own")

	must(t, spaFunctionLine(html, "dashSetHidden"), "hiddenCols(ME)", "cards that rest on a hidden field are not offered")
	must(t, spaFunctionLine(html, "dashSetHidden"), "pageAllowed(ME,'logs')", "the recent block needs the logs section")
	must(t, spaFunctionLine(html, "loadRecent"), "dashRows('recent')", "the recent table draws the stored columns")

	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, name := range []string{"pageViewDashSet", "dashSetRows", "dashSetStored"} {
		must(t, pure, "function "+name+"(", name+" is in the pure block")
	}
	must(t, spaFunctionLine(html, "collectPageViews"), "pageViewDashSet(name, state[DASH_SET_KEYS[name]])", "the sets go to the account body")
	must(t, spaFunctionLine(html, "applyPageViews"), "pageViewDashSet(name, dash[name])", "the sets are read back from the account")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "DASH_SET_KEYS[name]", "the sets are part of the live snapshot")

	for _, key := range []string{"dash.pref.title", "dash.pref.kpis", "dash.pref.blocks", "dash.pref.recent"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("missing %s in %s", key, lang)
			}
		}
	}
}
