//go:build uitest

// Behaviour of the chip groups and the access presets of the access editor, run under node on stub DOM objects.
// Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run 'TestAccessPresetLogic|TestTogGroupLogic'
package admin

import (
	"strings"
	"testing"
)

// accDOMStub is a tiny DOM for the access editor: elements with attributes and a parent, and q/qa that match
// selectors made of [attr] / [attr="v"] / tag[attr=v] parts joined by spaces (descendant).
const accDOMStub = `
const ELS = [];
const mk = (tag, attrs, parent, extra) => {
  const el = Object.assign({tag, attrs, parent: parent || null, dataset: {}, disabled: false, checked: false, textContent: '', _cls: new Set(), onclick: null, onchange: null}, extra || {});
  Object.keys(attrs).forEach(k => {
    if (k.startsWith('data-')) el.dataset[k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = attrs[k];
  });
  el.classList = {toggle: (c, on) => on ? el._cls.add(c) : el._cls.delete(c), contains: c => el._cls.has(c)};
  el.setAttribute = (k, v) => { el.attrs[k] = String(v); };
  ELS.push(el);
  return el;
};
const part = s => {
  const m = s.match(/^([a-z]*)((?:\[[^\]]+\])*)$/);
  const tag = m[1], conds = [...m[2].matchAll(/\[([\w-]+)(?:="?([^"\]]*)"?)?\]/g)].map(x => [x[1], x[2]]);
  return el => (!tag || el.tag === tag) && conds.every(([k, v]) => k in el.attrs && (v === undefined || String(el.attrs[k]) === v));
};
const within = (el, root) => { if (!root || !root.tag) return true; for (let p = el.parent; p; p = p.parent) if (p === root) return true; return false; };
const matches = (el, sel) => {
  const parts = sel.trim().split(/\s+/).map(part);
  if (!parts[parts.length - 1](el)) return false;
  let p = el.parent;
  for (let i = parts.length - 2; i >= 0; i--) {
    while (p && !parts[i](p)) p = p.parent;
    if (!p) return false;
    p = p.parent;
  }
  return true;
};
const qa = (sel, root) => ELS.filter(el => within(el, root) && matches(el, sel));
const q = (sel, root) => qa(sel, root)[0] || null;
const tr = k => k;
const trFill = (k, m) => k + ':' + m.n + '/' + m.m;
const ROOT = {};
const select = (res, value, options) => {
  const sel = mk('select', {'data-amode': res}, null, {value});
  sel.options = options.map(v => ({value: v}));
  return sel;
};
const group = (key, ticks, countKey) => {
  const g = mk('div', {'data-tgroup': key});
  mk('button', {'data-tall': key});
  mk('button', {'data-tnone': key});
  if (countKey) mk('span', {'data-tcount': key, 'data-tcount-k': countKey});
  return ticks.map(([attr, id, on]) => mk('input', {type: 'checkbox', [attr]: id}, g, {checked: on}));
};
`

// accNames lists the SPA functions the access editor checks copy.
var accNames = []string{"accessPresetOf", "togGroupState", "togGroupsSync", "bindTogGroups", "accessEditorState", "markAccessPreset", "accessEditorSync", "applyAccessPreset"}

// accPrelude gives the stub DOM, the eq helper and the constants of the SPA the checks need.
func accPrelude(t *testing.T) string {
	t.Helper()
	html := spaSource()
	var b strings.Builder
	b.WriteString(nodeEq)
	b.WriteString(accDOMStub)
	for _, c := range []string{"const USER_ACCESS_SECTIONS=", "const USER_ACCESS_RESOURCES=", "const ACC_PRESETS="} {
		src := spaBetween(html, c, ";")
		if src == "" {
			t.Fatalf("%s not found in SPA", c)
		}
		b.WriteString(src + ";\n")
	}
	return b.String()
}

// TestTogGroupLogic checks the chip group: the counter and the bulk buttons follow the ticks, "Select all" and
// "Clear all" change only their own group and call back.
func TestTogGroupLogic(t *testing.T) {
	runSPANode(t, accPrelude(t), accNames, `
eq(togGroupState([true,false,false]), {n:1,m:3,canAll:true,canNone:true}, 'one of three ticked');
eq(togGroupState([]), {n:0,m:0,canAll:false,canNone:false}, 'an empty group offers nothing');
const hid = group('hid', [['data-ahid','cost',true],['data-ahid','profit',false],['data-ahid','city',false]], 'acc.hidden.count');
const sec = group('sec', [['data-asec','flows',false]]);
let calls = 0;
bindTogGroups(ROOT, () => calls++);
const cnt = q('[data-tcount="hid"]', ROOT), all = q('[data-tall="hid"]', ROOT), none = q('[data-tnone="hid"]', ROOT);
eq([cnt.textContent, all.disabled, none.disabled], ['acc.hidden.count:1/3', false, false], 'counter and buttons on bind');
eq([q('[data-tall="sec"]', ROOT).disabled, q('[data-tnone="sec"]', ROOT).disabled], [false, true], 'nothing ticked: Clear all is off');
all.onclick();
eq([hid.map(x => x.checked), cnt.textContent, all.disabled, calls], [[true,true,true], 'acc.hidden.count:3/3', true, 1], 'Select all ticks the group');
eq(sec[0].checked, false, 'another group is untouched');
none.onclick();
eq([hid.map(x => x.checked), cnt.textContent, none.disabled, calls], [[false,false,false], 'acc.hidden.count:0/3', true, 2], 'Clear all unticks the group');
if (failed) process.exit(1);
`)
}

// TestAccessPresetLogic checks the presets: the active one comes from the stored access on open and needs every
// section open, a preset sets every resource and opens every section but keeps the hidden metrics, a hand change
// falls back to Custom, Custom keeps the choice, and a role editor without the mode is left alone.
func TestAccessPresetLogic(t *testing.T) {
	runSPANode(t, accPrelude(t), accNames, `
const all = USER_ACCESS_SECTIONS, res = USER_ACCESS_RESOURCES;
const full = Object.fromEntries(res.map(r => [r, 'full']));
const resSecs = ['flows','offers','landings','networks','sources','domains'];
eq(accessPresetOf({}, all, all, res), 'read', 'no grants with every section open is read');
eq(accessPresetOf({campaigns:'read'}, all, all, res), 'read', 'explicit read and the default mix');
eq(accessPresetOf(full, all, all, res), 'full', 'full everywhere');
eq(accessPresetOf({...full, offers:'read'}, all, all, res), 'custom', 'mixed modes');
eq(accessPresetOf(full, resSecs, all, res), 'custom', 'only the resource sections open is custom');
eq(accessPresetOf(full, all.filter(s => s !== 'keywords'), all, res), 'custom', 'a closed section without a resource is custom');
eq(accessPresetOf({...full, domains:'created'}, all, all, res), 'custom', 'selected or created is custom');

const modes = ['', 'full', 'read', 'selected', 'created'];
const sels = res.map(r => select(r, '', modes));
sels[0].onchange = () => { sels[0].changed = (sels[0].changed || 0) + 1; };
const secs = group('sec', all.map(id => ['data-asec', id, false]));
const hid = group('hid', [['data-ahid','cost',true],['data-ahid','city',false]], 'acc.hidden.count');
const btn = ACC_PRESETS.map(p => mk('button', {'data-apreset': p}));
const lit = () => btn.filter(b => b.classList.contains('on')).map(b => b.dataset.apreset);

accessEditorSync(ROOT);
eq(lit(), ['custom'], 'closed sections on open: custom');
resSecs.forEach(id => { secs[all.indexOf(id)].checked = true; });
accessEditorSync(ROOT);
eq(lit(), ['custom'], 'resource sections alone do not light read');
applyAccessPreset(ROOT, 'full');
eq([sels.map(s => s.value), secs.map(s => s.checked), lit(), sels[0].changed], [sels.map(() => 'full'), all.map(() => true), ['full'], 1], 'full sets every mode and opens every section');
eq(hid.map(x => x.checked), [true, false], 'a preset keeps the hidden metrics');
eq(btn.find(b => b.dataset.apreset === 'full').attrs['aria-checked'], 'true', 'the lit preset is checked for assistive tech');
applyAccessPreset(ROOT, 'read');
eq([sels.map(s => s.value), secs.every(s => s.checked), lit()], [sels.map(() => 'read'), true, ['read']], 'read sets read everywhere');
hid[1].checked = true; accessEditorSync(ROOT);
eq(lit(), ['read'], 'a hidden metric keeps the preset');
sels[2].value = 'full'; accessEditorSync(ROOT);
eq(lit(), ['custom'], 'a changed mode falls back to custom');
sels[2].value = 'read'; accessEditorSync(ROOT);
eq(lit(), ['read'], 'back to the preset state lights it again');
secs[all.indexOf('dashboard')].checked = false; accessEditorSync(ROOT);
eq(lit(), ['custom'], 'an unticked section without a resource is custom');
const before = sels.map(s => s.value);
applyAccessPreset(ROOT, 'custom');
eq([sels.map(s => s.value), lit()], [before, ['custom']], 'custom keeps the choice');

ELS.length = 0;
const role = res.map(r => select(r, '', ['', 'read']));
const roleSecs = all.map(id => mk('input', {type: 'checkbox', 'data-asec': id}));
applyAccessPreset(ROOT, 'full');
eq([role.map(s => s.value), roleSecs.every(s => s.checked)], [role.map(() => ''), true], 'a mode the editor does not offer is not set; sections still open');
if (failed) process.exit(1);
`)
}
