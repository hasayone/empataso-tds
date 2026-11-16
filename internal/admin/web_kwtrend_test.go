package admin

import (
	"regexp"
	"strings"
	"testing"
)

// kwTrendKeyRe finds the dictionary keys of the keyword trend dialog where the code names them.
var kwTrendKeyRe = regexp.MustCompile(`'(kws\.tr\.[a-z.]+)'`)

// TestKeywordTrendDialog checks the markup of the keyword trend dialog: the sparkline is a button bound
// to the dialog, the dialog is the shared modal over a scrim with Esc and outside-click close, it reads
// the last trend answer and closes when the table reloads, the pure helpers sit in the pure block, and
// every kws.tr key is defined once in en, ru and uk.
func TestKeywordTrendDialog(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, name := range []string{"kwsTrendMetrics", "kwsTrendPoints", "kwsTrendSummary", "kwsTrendBasis", "kwsTrendFilters", "kwsChartGeom", "kwsNearest"} {
		must(t, pure, "function "+name+"(", name+" is in the pure block")
	}
	must(t, pure, "const KWS_TREND_METRICS=", "the trend metrics are in the pure block")
	metrics := spaBetween(html, "const KWS_TREND_METRICS=", "];")
	for _, foreign := range []string{"roi", "cost", "uepc", "cr", "uniq", "depsum"} {
		mustNot(t, metrics, "id:'"+foreign+"'", "the trend offers no metric without its own daily series: "+foreign)
	}

	cell := spaFunctionLine(html, "kwsTrendCell")
	must(t, cell, `<button type="button" class="kws-trend" data-kwtrend=`, "the sparkline is a button")
	must(t, cell, "aria-label=", "the trend button has a label")
	must(t, cell, "trFill('kws.tr.of'", "the label names the keyword")
	must(t, html, "button.kws-trend:hover{", "the trend button shows a hover state")
	must(t, html, "button:focus-visible,", "the trend button gets the shared focus ring")
	must(t, spaFunctionLine(html, "paintKwStats"), "qa('[data-kwtrend]',wrap).forEach(b=>b.onclick=()=>openKwTrend(b.dataset.kwtrend, b))", "a click on the sparkline opens the dialog")

	dlg := spaFunctionLine(html, "openKwTrend")
	must(t, dlg, `class="modal kwt" role="dialog" aria-modal="true" aria-labelledby="kwt-h"`, "the dialog is the shared modal")
	must(t, dlg, `$('<div class="scrim"`, "the dialog sits over the shared scrim")
	must(t, dlg, "sc.onclick=dlg.close", "a click beside the dialog closes it")
	must(t, dlg, "e.key==='Escape'", "Esc closes the dialog")
	must(t, dlg, "window.addEventListener('hashchange', dlg.close)", "a route change closes the dialog")
	must(t, dlg, "window.removeEventListener('hashchange', dlg.close)", "the route listener is removed on close")
	must(t, dlg, "document.removeEventListener('keydown', onKey, true)", "the key listener is removed on close")
	must(t, dlg, "opener.focus()", "focus goes back to the sparkline")
	must(t, dlg, "KWS_TREND_DAYS.slice()", "the dialog reads the days of the last trend answer")
	must(t, dlg, "kwsTrendMetrics(s, ready, hiddenCols(ME))", "the switch offers only metrics that are counted and visible")
	must(t, dlg, `class="ctip"`, "the point tooltip uses the chart tooltip style")
	must(t, dlg, "kwsTrendBasis(metric, days, {tz:uiTz(), top:KWS_TOP, ready})", "the caption follows the zone, the top and conv_ready")
	must(t, dlg, "kwsTrendFilters(kwsCampaign(), KWS_Q, KWS.conds)", "the caption names the page filters")
	mustNot(t, dlg, "call(", "the dialog makes no request of its own")

	load := spaBetween(html, "async function loadKwStats(", "async function keywordStats(")
	must(t, load, "closeKwTrend();", "a reload of the table closes the dialog")
	must(t, load, "KWS_TREND_DAYS=Array.isArray(t&&t.days)?t.days.map(String):[]", "the trend days are kept for the dialog")

	keys := map[string]struct{}{}
	for _, m := range kwTrendKeyRe.FindAllStringSubmatch(html, -1) {
		keys[m[1]] = struct{}{}
	}
	if len(keys) < 15 {
		t.Fatalf("too few kws.tr keys: %d", len(keys))
	}
	for key := range keys {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("%s missing for lang %q", key, lang)
			}
		}
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("%s is defined %d times, want once in en, ru and uk", key, n)
		}
	}
}
