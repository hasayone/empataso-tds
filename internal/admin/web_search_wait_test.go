//go:build uitest

package admin

import (
	"strings"
	"testing"
)

// searchWaitPrelude is a virtual clock with setTimeout and clearTimeout, a stub text input that
// takes listeners, and the pause constant the SPA defines.
const searchWaitPrelude = `
let NOW = 0, SEQ = 0; const TIMERS = new Map();
function setTimeout(fn, ms) { const id = ++SEQ; TIMERS.set(id, { at: NOW + ms, fn }); return id; }
function clearTimeout(id) { TIMERS.delete(id); }
function advance(ms) {
  const end = NOW + ms;
  for (;;) {
    let next = null;
    for (const [id, t] of TIMERS) if (t.at <= end && (!next || t.at < next[1].at)) next = [id, t];
    if (!next) break;
    TIMERS.delete(next[0]); NOW = next[1].at; next[1].fn();
  }
  NOW = end;
}
function stubInput(value) {
  const on = {};
  return { value, addEventListener(n, f) { (on[n] = on[n] || []).push(f); },
    type(text) { this.value = text; (on.input || []).forEach(f => f({})); },
    key(key, composing) { (on.keydown || []).forEach(f => f({ key, isComposing: !!composing })); } };
}
const SEARCH_WAIT_MS = 1500;
let FAILED = false;
function eq(got, want, what) { if (got !== want) { FAILED = true; console.log('FAIL ' + what + ': got ' + got + ', want ' + want); } }
`

// TestBindSearchWaitsForAPause checks the shared search pause under a virtual clock: two quick inputs
// make one search, which starts 1500 ms after the last character and gets the last text; Enter searches
// at once and the pending wait is dropped; Enter without an edit and Enter inside an IME composition
// search nothing; clearing the field waits like any edit and then searches with the empty text.
func TestBindSearchWaitsForAPause(t *testing.T) {
	runSPANode(t, searchWaitPrelude, []string{"bindSearch"}, `
const runs = [];
const inp = stubInput('');
bindSearch(inp, v => runs.push([v, NOW]));
inp.type('st'); advance(400); inp.type('sto');
advance(1499); eq(runs.length, 0, 'no search before 1500 ms of quiet');
advance(1); eq(runs.length, 1, 'one search for two quick inputs');
eq(runs[0][0], 'sto', 'the search gets the last text');
eq(runs[0][1], 1900, 'the search starts 1500 ms after the last character');
advance(5000); eq(runs.length, 1, 'no second search over the same filter');

const fast = [];
const b = stubInput('');
bindSearch(b, v => fast.push([v, NOW]));
const t0 = NOW;
b.type('stop'); b.key('Enter');
eq(fast.length, 1, 'Enter searches at once');
eq(fast[0][1], t0, 'Enter does not wait');
advance(5000); eq(fast.length, 1, 'the pending wait is dropped by Enter');
b.key('Enter'); eq(fast.length, 1, 'Enter on a searched text does nothing');
b.type('x'); b.key('Enter', true); eq(fast.length, 1, 'Enter in an IME composition does not search');
advance(1500); eq(fast.length, 2, 'the composition ends and the wait runs');

const clr = [];
const c = stubInput('stop');
bindSearch(c, v => clr.push([v, NOW]));
c.type(''); advance(1499); eq(clr.length, 0, 'clearing waits');
advance(1); eq(clr.length, 1, 'clearing searches after the pause');
eq(clr[0][0], '', 'the search shows the full list');

const slow = [];
const d = stubInput('');
const h = bindSearch(d, v => slow.push(v));
d.type('a'); h.cancel(); advance(3000); eq(slow.length, 0, 'cancel drops the wait');
if (FAILED) process.exit(1);
`)
}

// TestSearchFieldsShareThePause checks that every search field of the SPA goes through bindSearch or
// through the combobox whose pause is the same constant, and that no field keeps its own timer.
func TestSearchFieldsShareThePause(t *testing.T) {
	html := spaSource()
	must(t, html, "const SEARCH_WAIT_MS=1500;", "the one pause of all searches")
	must(t, html, "const PICK_DEBOUNCE_MS=SEARCH_WAIT_MS;", "comboboxes wait the same")
	for fn, needle := range map[string]string{
		"bindCatalogBar":  "bindSearch(qi, v=>{CAT_Q[kind]=v; paintTable()})",
		"flows":           "bindSearch(q('#fq'),",
		"aiMountPanel":    "bindSearch(q('#ai-q',p),",
		"aiModelPicker":   "bindSearch(search,",
		"macroPickNode":   "bindSearch(find,",
		"openGroupWindow": "bindSearch(search,",
	} {
		src := spaFunctionLine(html, fn)
		if src == "" {
			t.Fatalf("%s not found in SPA", fn)
		}
		must(t, src, needle, fn+" searches through bindSearch")
	}
	offerMacros := spaFunctionLine(html, "offerMacroNode")
	if offerMacros == "" {
		t.Fatal("offerMacroNode not found in SPA")
	}
	must(t, offerMacros, "return macroPickNode(", "the offer macro picker uses the shared search")
	must(t, html, "bindSearch(q('#col-find',m),", "the column search")
	must(t, html, "KWS_SEARCH=bindSearch(q('#kws-q'),", "the keyword search")
	must(t, html, "bindSearch(input, paint);", "the board icon search")
	for _, gone := range []string{"qi.oninput=", "q('#fq').oninput=", "AI.qtimer", "KWS_SEARCH_TIMER", "q('#col-find',m).oninput="} {
		mustNot(t, html, gone, "a search field with its own handler or timer: "+gone)
	}
}

// TestOfferStatusDotToggle checks the status dot of the offers table: it is the only element that carries
// data-otoggle, the name is outside it, a viewer who may not edit the offer or a list that is read only gets
// a plain dot, and the row opens the dialog for every click except the dot, the menu and the tick.
func TestOfferStatusDotToggle(t *testing.T) {
	runSPANode(t, `
const ME = { id: 'u' }; let FAILED = false;
const loadCols = () => [{ id: 'name', on: 1 }];
const esc = s => String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
const tr = s => s; const canEdit = (me, kind, o) => o.id !== 'locked';
const offerMetrics = () => ({}); const offerCells = () => '';
function offerDotClass(on) { return on ? 'on' : 'bad'; }
function eq(got, want, what) { if (got !== want) { FAILED = true; console.log('FAIL ' + what + ': got ' + got + ', want ' + want); } }
`, []string{"offerRowHTML"}, `
const on = offerRowHTML({ id: 'o1', name: 'STOP Verde', status: true }, '', '', true);
eq(on.split('data-otoggle').length - 1, 1, 'one switch in the name cell');
const sw = on.slice(on.indexOf('<span class="offdot"'), on.indexOf('</span></span>', on.indexOf('<span class="offdot"')) + 14);
eq(sw.includes('STOP Verde'), false, 'the name is not inside the switch');
eq(sw.includes('class="dot on"'), true, 'an enabled offer has the green dot');
eq(on.includes('STOP Verde'), true, 'the name stays in the cell');
const off = offerRowHTML({ id: 'o2', name: 'B', status: false }, '', '', true);
eq(off.includes('class="dot bad"'), true, 'a disabled offer has the red dot');
eq(offerRowHTML({ id: 'locked', name: 'C', status: true }, '', '', true).includes('data-otoggle'), false, 'no switch without the right to edit');
eq(offerRowHTML({ id: 'o1', name: 'A', status: true }, '', '').includes('data-otoggle'), false, 'no switch in a read only list');
if (FAILED) process.exit(1);
`)
	html := spaSource()
	table := spaBetween(html, "const paintTable=()=>{\n    const grid=q('#offers-grid')", "const openOffer=(id)=>{")
	if table == "" {
		table = spaBetween(html, "const paintTable=()=>{\r\n    const grid=q('#offers-grid')", "const openOffer=(id)=>{")
	}
	if table == "" {
		t.Fatal("the offers table is not found")
	}
	table = strings.ReplaceAll(table, "\r\n", "\n")
	must(t, table, "offerRowHTML(o, netName, offActs(o), true)", "the offers table asks for the switch")
	must(t, table, "if(e.target.closest('[data-omore],[data-otoggle],.pick')) return;", "a click on the dot does not open the dialog")
	must(t, table, "qa('[data-otoggle]',grid).forEach(", "the dot has its own handler")
	must(t, table, "if(o&&canEdit(ME,'offers',o)) toggleOffer(o);", "the dot saves through the same toggle as the menu, with the same right")
	must(t, table, "e.stopPropagation();", "the dot click does not reach the row")
}
