package admin

import (
	"strings"
	"testing"
)

// TestSPAShowIfMechanism checks the shared dependent-field mechanism: one function hides every [data-show-if]
// element whose selector does not match, a change anywhere and a newly drawn subtree re-run it, and hiding
// leaves the values alone. t is the test handle. It returns nothing and fails when the mechanism regresses.
func TestSPAShowIfMechanism(t *testing.T) {
	html := spaSource()
	fn := spaFunctionLine(html, "showIfSync")
	if fn == "" {
		t.Fatal("showIfSync missing")
	}
	must(t, fn, "querySelectorAll('[data-show-if]')", "scans the dependent elements")
	must(t, fn, "document.querySelector(el.dataset.showIf)", "the selector is read against the whole page")
	must(t, fn, "if(el.hidden===on) el.hidden=!on;", "only a changed state is written")
	mustNot(t, fn, ".value=", "hiding keeps the values")
	mustNot(t, fn, "dispatchEvent", "hiding fires no change for the dirty check")
	must(t, html, "document.addEventListener('change',()=>showIfSync());", "any change re-runs the mechanism")
	must(t, html, "if(n.nodeType===1) showIfSync(n)", "a newly drawn subtree gets its state at once")
}

// TestSPAKeitaroPeriod checks the Keitaro import period: the block shows only with conversions or clicks,
// the dates only with «Dates», the header period name follows an em dash and is repainted with the header,
// and the dates use the header range calendar in a popover with the Start/End row.
// t is the test handle. It returns nothing and fails when the block regresses.
func TestSPAKeitaroPeriod(t *testing.T) {
	html := spaSource()
	src := spaFunctionLine(html, "keitaroPanel")
	if src == "" {
		t.Fatal("keitaroPanel missing")
	}
	must(t, src, `<div class="fld" id="kt-period" data-show-if="#kt-conversions:checked,#kt-clicks:checked" hidden>`, "period shown only with conversions or clicks")
	must(t, src, `<div id="kt-dates" data-show-if="#kt-period-custom:checked" hidden>`, "dates shown only with Dates")
	must(t, src, "${tr('kt.period.global')} — <span data-period-label>${esc(periodLabel())}</span>", "header period after a dash")
	must(t, spaFunctionLine(html, "paintChrome"), "qa('[data-period-label]').forEach(el=>{el.textContent=periodLabel()});", "label follows the header period")
	must(t, src, `id="kt-range" aria-haspopup="dialog" aria-expanded="false"`, "one range field like the header")
	pop := spaBetween(src, `<div id="kt-range-pop" class="rangepop"`, `id="kt-range-ok"`)
	for _, want := range []string{`id="kt-range-cal" hidden`, `<div class="rangerow">`, `id="kt-from"`, `id="kt-to"`, "tr('spend.from')", "tr('spend.to')"} {
		must(t, pop, want, "range popover markup")
	}
	if strings.Count(pop, `<div class="fld">`) != 2 {
		t.Error("the Keitaro range row must hold exactly the Start and End fields")
	}
	mustNot(t, src, `type="date" id="kt-from"`, "no native date inputs behind a disabled state")
	mustNot(t, src, "el.disabled=!open", "the period is hidden, not disabled")
	must(t, src, "rangePopOpen(rangeParts())", "the field opens the shared range popover")
	must(t, src, "a.value>b.value){toast(tr('period.need'),true)", "OK rejects an empty or reversed range")
	must(t, src, "from:(q('#kt-from')||{}).value||'', to:(q('#kt-to')||{}).value||''", "custom dates go to the same payload fields")
	must(t, src, "const b=rangeBounds();", "the current period still comes from the header")
	for _, k := range []string{"'kt.period.global':'Current period'", "'kt.period.global':'Текущий период'", "'kt.period.global':'Поточний період'"} {
		must(t, html, k, "period caption in every language")
	}
}

// TestSPAKeitaroImportKept checks that the Keitaro import form survives a redraw: every choice, the dates and the
// campaign id are kept in sessionStorage under the emtds- prefix through the shared form helper, and OK stores the pick.
// t is the test handle. It returns nothing and fails when a control is not kept.
func TestSPAKeitaroImportKept(t *testing.T) {
	html := spaSource()
	keep := spaFunctionLine(html, "formKeepBind")
	must(t, keep, "const sk='emtds-form-'+key;", "stored per form under the emtds- prefix")
	must(t, keep, "sessionStorage.getItem(sk)", "restored from the tab session")
	must(t, keep, "root.addEventListener('change', save);", "stored on change")
	must(t, keep, "root.addEventListener('input', save);", "stored on typing")
	must(t, keep, "if(pop&&el===pop.a) return pop.keep[0];", "an open range popover stores the values it opened with")
	src := spaFunctionLine(html, "keitaroPanel")
	must(t, src, "formKeepBind(q('#kt-import',host), 'kt-import', [", "Keitaro import uses the shared helper")
	for _, id := range []string{"kt-domains", "kt-networks", "kt-offers", "kt-landings", "kt-sources", "kt-campaigns", "kt-users", "kt-conversions", "kt-clicks", "kt-period-global", "kt-period-custom", "kt-from", "kt-to", "kt-one-id"} {
		must(t, src, "'"+id+"'", "kept control "+id)
	}
	must(t, src, "q('#kt-range-text',host).textContent=q('#kt-from',host).value+' – '+q('#kt-to',host).value;", "the range field shows the restored dates")
	must(t, src, "rangePopClose(false);\n    q('#kt-range-text',host).textContent=a.value+' – '+b.value;\n    keepImport();", "OK stores the pick")
	must(t, src, "const KT_TAB_KEY='emtds-kt-tab';", "open panel kept under the emtds- prefix")
	must(t, src, "if(id==='kt-conn'||id==='kt-import'){try{sessionStorage.setItem(KT_TAB_KEY,id)}catch(e){}}", "connection and import panels are remembered")
	must(t, src, "if(keptTab==='kt-import'&&!following&&!(job0&&(job0.stage==='done'||job0.stage==='failed'))) ktSelect('kt-import');", "a redraw returns to the import panel unless a job is shown")
}

// TestSPARangeCalendarShared checks that the header and the field popovers draw the range calendar with one function,
// and that a field popover sits above drawers: fixed, placed in the window, Escape caught before other handlers.
// t is the test handle. It returns nothing and fails when the calendar code is duplicated or the popover leaks keys.
func TestSPARangeCalendarShared(t *testing.T) {
	html := spaSource()
	if n := strings.Count(html, "new AirDatepicker(cal,"); n != 1 {
		t.Errorf("the inline range calendar is built in %d places, want 1", n)
	}
	must(t, spaFunctionLine(html, "dpBindRange"), "dpRangeCal(a,b,cal,()=>RANGE.id===id)", "header uses the shared calendar")
	open := spaFunctionLine(html, "rangePopOpen")
	must(t, open, "dpRangeCal(p.a,p.b,p.cal,()=>RANGE_POP===pop)", "field popover uses the shared calendar")
	must(t, open, "placeBoardIconPop(p.box,p.anchor)", "placed inside the window")
	must(t, open, "window.addEventListener('keydown',pop.key,true)", "Escape is caught in the capture phase")
	must(t, open, "e.stopImmediatePropagation(); rangePopClose(true)", "Escape closes only the popover")
	must(t, spaFunctionLine(html, "rangePopClose"), "window.removeEventListener('keydown',pop.key,true)", "listeners leave with the popover")
	must(t, html, ".rangepop{position:fixed;", "the popover is fixed, so a scrolling pane does not clip it")
}

// TestSPADependentControls checks the controls that depend on another one: hidden while useless, or disabled
// with a visible reason. t is the test handle. It returns nothing and fails when one goes back to a bare disabled state.
func TestSPADependentControls(t *testing.T) {
	html := spaSource()
	must(t, html, `<button data-run="telegram_test" ${tgOK?'':'hidden'}>`, "Telegram test hidden until the bot is set")
	must(t, html, "btn.hidden=!st.telegram_configured", "Telegram test follows the status")
	mustNot(t, html, "btn.disabled=!st.telegram_configured", "no bare disabled Telegram test")
	must(t, spaFunctionLine(html, "syncKeyForm"), "showIfSync(box);", "key type switch updates the dependent actions")
	must(t, html, `<p class="hint" id="lsave-first"${lid?' hidden':''}>${tr('land.savefirst')}</p>`, "landing upload says why it waits")
	for _, k := range []string{"'land.savefirst':'Save the landing", "'land.savefirst':'Сохраните лендинг", "'land.savefirst':'Збережіть лендинг"} {
		must(t, html, k, "landing save hint in every language")
	}
}
