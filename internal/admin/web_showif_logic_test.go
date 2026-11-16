//go:build uitest

// Behaviour of the dependent-field mechanism and the field range popover, run under node on stub DOM objects.
// Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run 'TestShowIfLogic|TestRangePopLogic'
package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSPANode runs the named SPA functions followed by script under node.
// t is the test handle, prelude is code that runs first, names are the SPA functions to copy and script holds the checks.
// It returns nothing; it skips without node and fails when a function is missing or a check prints FAIL.
func runSPANode(t *testing.T, prelude string, names []string, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the SPA checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(prelude)
	for _, name := range names {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		if strings.Contains(html, "async function "+name+"(") {
			src.WriteString("async ")
		}
		src.WriteString(fn)
		src.WriteByte('\n')
	}
	src.WriteString(script)
	f := filepath.Join(t.TempDir(), "check.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("checks failed:\n%s", out)
	}
}

const nodeEq = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
`

// TestShowIfLogic runs showIfSync on stub elements: a matching selector shows, a missing one hides,
// an invalid one hides, an unchanged state is not written again and values stay.
func TestShowIfLogic(t *testing.T) {
	prelude := nodeEq + `
const state = new Set();
let writes = 0;
const mk = (sel, hidden) => {
  const el = {dataset:{showIf:sel}, value:'kept', _h:hidden, matches:()=>false};
  Object.defineProperty(el, 'hidden', {get(){return this._h}, set(v){writes++; this._h=v}});
  return el;
};
const els = [];
const document = {
  querySelectorAll: () => els,
  querySelector: sel => {
    if (sel.includes('!!')) throw new Error('bad selector');
    return sel.split(',').some(s => state.has(s.trim())) ? {} : null;
  },
  addEventListener: () => {},
  body: {},
};
function MutationObserver(){ return {observe(){}} }
const DP_BOUND = new Map();
let RANGE_POP = null;
function dpScan(){}
`
	runSPANode(t, prelude, []string{"showIfSync"}, `
const period = mk('#kt-conversions:checked,#kt-clicks:checked', true);
const dates = mk('#kt-period-custom:checked', true);
const bad = mk('#x!!', false);
els.push(period, dates, bad);
showIfSync();
eq([period.hidden, dates.hidden, bad.hidden], [true, true, true], 'nothing ticked: all hidden, a bad selector too');
eq(writes, 1, 'only the changed element was written');
state.add('#kt-clicks:checked');
showIfSync();
eq([period.hidden, dates.hidden], [false, true], 'clicks ticked: period shown, dates still hidden');
state.add('#kt-period-custom:checked');
showIfSync();
eq([period.hidden, dates.hidden], [false, false], 'Dates chosen: dates shown');
state.delete('#kt-clicks:checked');
showIfSync();
eq(period.hidden, true, 'clicks unticked: period hidden again');
eq([period.value, dates.value], ['kept', 'kept'], 'hiding keeps the values');
const sub = mk('#kt-clicks:checked', false);
sub.matches = () => true;
sub.querySelectorAll = () => [];
showIfSync(sub);
eq(sub.hidden, true, 'a subtree root with the attribute is updated too');
if (failed) process.exit(1);
`)
}

// TestFormKeepLogic runs formKeepBind on stub controls: a first bind stores nothing, a change stores every control,
// a second bind on a fresh form restores them, values of an open range popover are stored as they were on open
// and broken storage is ignored.
func TestFormKeepLogic(t *testing.T) {
	prelude := nodeEq + `
const store = {};
const sessionStorage = {getItem: k => (k in store ? store[k] : null), setItem: (k, v) => { store[k] = v; }};
let RANGE_POP = null;
function showIfSync(){}
const form = () => {
  const ctl = {
    clicks: {id:'clicks', type:'checkbox', checked:false},
    custom: {id:'custom', type:'radio', checked:false},
    from: {id:'from', type:'text', value:'2026-11-16'},
    cid: {id:'cid', type:'number', value:''},
  };
  const on = {};
  const root = {addEventListener: (t, fn) => { on[t] = fn; }};
  return {ctl, on, root};
};
const q = (sel, root) => root._ctl[sel.slice(1)] || null;
`
	runSPANode(t, prelude, []string{"formKeepBind"}, `
let f = form(); f.root._ctl = f.ctl;
let save = formKeepBind(f.root, 'kt', ['clicks', 'custom', 'from', 'cid', 'gone']);
eq(Object.keys(store), [], 'nothing stored before a change');
f.ctl.clicks.checked = true; f.ctl.custom.checked = true; f.ctl.from.value = '2026-11-03'; f.ctl.cid.value = '42';
f.on.change();
eq(JSON.parse(store['emtds-form-kt']), {clicks:true, custom:true, from:'2026-11-03', cid:'42'}, 'every control stored under emtds-form-');

f = form(); f.root._ctl = f.ctl;
save = formKeepBind(f.root, 'kt', ['clicks', 'custom', 'from', 'cid']);
eq([f.ctl.clicks.checked, f.ctl.custom.checked, f.ctl.from.value, f.ctl.cid.value], [true, true, '2026-11-03', '42'], 'a redrawn form gets the choices back');

RANGE_POP = {a: f.ctl.from, b: {}, keep: ['2026-11-03', '']};
f.ctl.from.value = '2026-10-01';
f.on.input();
eq(JSON.parse(store['emtds-form-kt']).from, '2026-11-03', 'a pick in an open popover is not stored');
RANGE_POP = null;
save();
eq(JSON.parse(store['emtds-form-kt']).from, '2026-10-01', 'OK stores the pick');

store['emtds-form-kt'] = '{broken';
f = form(); f.root._ctl = f.ctl;
formKeepBind(f.root, 'kt', ['clicks']);
eq(f.ctl.clicks.checked, false, 'broken storage leaves the defaults');
if (failed) process.exit(1);
`)
}

// TestRangePopLogic runs the field range popover on stub elements: open shows it, Escape restores the values
// and stops other handlers, a click outside cancels, a click on the trigger is left to the trigger, and close(false) keeps the picks.
func TestRangePopLogic(t *testing.T) {
	prelude := nodeEq + `
const handlers = {};
const target = name => ({
  addEventListener: (type, fn, cap) => { handlers[name+':'+type+':'+!!cap] = fn; },
  removeEventListener: (type, fn, cap) => { if (handlers[name+':'+type+':'+!!cap] === fn) delete handlers[name+':'+type+':'+!!cap]; },
});
const window = target('w'), document = target('d');
let RANGE_POP = null, destroyed = 0, focused = 0;
async function dpRangeCal(){ return {destroy(){ destroyed++; }} }
function placeBoardIconPop(){}
function dpHit(e, box){ return e.inBox === true; }
const attrs = {};
const anchor = {isConnected:true, contains: x => x === 'trigger', setAttribute:(k,v)=>{attrs[k]=v}, focus:()=>{focused++}};
const parts = () => ({box:{hidden:true}, anchor, a:{value:'2026-11-01'}, b:{value:'2026-11-16'}, cal:{hidden:true}});
`
	runSPANode(t, prelude, []string{"rangePopOpen", "rangePopClose"}, `
(async () => {
  let p = parts();
  await rangePopOpen(p);
  eq([p.box.hidden, attrs['aria-expanded'], !!handlers['w:keydown:true']], [false, 'true', true], 'open shows the popover and catches keys');
  p.a.value = '2026-10-01';
  let stopped = 0;
  handlers['w:keydown:true']({key:'Escape', preventDefault(){}, stopImmediatePropagation(){ stopped++; }});
  eq([p.box.hidden, p.cal.hidden, p.a.value, stopped, focused, destroyed], [true, true, '2026-11-01', 1, 1, 1], 'Escape closes, restores and stops the drawer handler');
  eq([RANGE_POP, Object.keys(handlers).length], [null, 0], 'listeners removed');

  p = parts();
  await rangePopOpen(p);
  handlers['d:click:true']({target:'trigger'});
  eq(p.box.hidden, false, 'a click on the trigger is left to the trigger');
  handlers['d:click:true']({target:'cell', inBox:true});
  eq(p.box.hidden, false, 'a click inside stays open');
  p.b.value = '2026-11-20';
  handlers['d:click:true']({target:'page'});
  eq([p.box.hidden, p.b.value], [true, '2026-11-16'], 'a click outside cancels');

  p = parts();
  await rangePopOpen(p);
  p.a.value = '2026-11-05';
  rangePopClose(false);
  eq([p.box.hidden, p.a.value], [true, '2026-11-05'], 'OK keeps the picks');
  handlers['w:keydown:true'] && eq(1, 0, 'no key handler after close');
  if (failed) process.exit(1);
})();
`)
}
