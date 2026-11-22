//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// layoutChecks are the assertions of TestLayoutPickLogic: the stored value picks the content width, a click saves
// and paints it on the root at once, and the profile card marks the saved choice.
const layoutChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
eq(layoutPick('wide'), 'wide', 'wide stays wide');
eq(layoutPick('box'), 'box', 'box stays box');
eq(layoutPick(null), 'box', 'nothing saved is the container');
eq(layoutPick('huge'), 'box', 'an unknown value is the container');
eq(currentLayout(), 'box', 'a fresh browser shows the container');
applyLayout('wide');
eq(STORE['emtds-layout'], 'wide', 'the full width is saved');
eq(viewTouches, 1, 'the full width schedules the account view');
eq(ROOT.attrs['data-layout'], 'wide', 'the root gets the full width at once');
eq(resized, 1, 'the page lays out again');
eq(currentLayout(), 'wide', 'the saved choice reads back');
eq(BTN.map(b => b.on), [false, true], 'the full width button is marked');
eq(BTN.map(b => b.attrs['aria-checked']), ['false', 'true'], 'the radio state follows');
eq(HINT.textContent, 'layout.wide.hint', 'the hint names the full width');
const card = layoutCardHTML();
eq(card.includes('id="layout-card"'), true, 'the card has its id');
eq(card.includes('data-layout-pick="wide" class="on" aria-checked="true"'), true, 'the card marks the saved choice');
applyLayout('nope');
eq(STORE['emtds-layout'], 'box', 'an unknown pick saves the container');
eq(viewTouches, 2, 'the container schedules the account view');
eq('data-layout' in ROOT.attrs, false, 'the container drops the root attribute');
eq(BTN.map(b => b.on), [true, false], 'the container button is marked');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`

// layoutHarness declares the browser objects the layout functions touch.
const layoutHarness = `
const STORE = {};
const localStorage = {getItem: k => (k in STORE ? STORE[k] : null), setItem: (k, v) => { STORE[k] = String(v); }};
const ROOT = {attrs: {}, setAttribute(k, v) { this.attrs[k] = String(v); }, removeAttribute(k) { delete this.attrs[k]; }};
const document = {documentElement: ROOT};
const mkBtn = id => ({dataset: {layoutPick: id}, on: false, attrs: {},
  classList: {toggle(c, v) { if (c === 'on') mkBtn.self[id].on = !!v; }}, setAttribute(k, v) { this.attrs[k] = v; }});
mkBtn.self = {};
const BTN = ['box', 'wide'].map(id => (mkBtn.self[id] = mkBtn(id)));
const HINT = {textContent: ''};
const qa = sel => (sel === '[data-layout-pick]' ? BTN : []);
const q = sel => (sel === '[data-layout-hint]' ? HINT : null);
let resized = 0;
let viewTouches = 0;
function touchPageViews(){ viewTouches++; }
class Event { constructor(t) { this.type = t; } }
const window = {dispatchEvent: e => { if (e.type === 'resize') resized++; }};
const tr = s => s;
const esc = s => String(s);
`

// TestLayoutPickLogic runs the content width choice under node. It skips when node is absent.
func TestLayoutPickLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the content width checks")
	}
	html := spaSource()
	var b strings.Builder
	b.WriteString(layoutHarness)
	b.WriteString("const LAYOUT_KEY='emtds-layout';\nconst LAYOUTS=['box','wide'];\n")
	for _, name := range []string{"layoutPick", "currentLayout", "applyLayout", "layoutCardHTML", "paintLayoutPick"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(layoutChecks)
	f := filepath.Join(t.TempDir(), "layout.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("content width checks failed:\n%s", out)
	}
}

// TestLayoutWiring checks the single width source, the paint before the first render, the profile card and its
// translations in every language.
func TestLayoutWiring(t *testing.T) {
	html := spaSource()
	must(t, html, "const LAYOUT_KEY='emtds-layout';", "one storage key")
	must(t, html, "if(localStorage.getItem('emtds-layout')==='wide')document.documentElement.setAttribute('data-layout','wide')", "the head script paints the width before the first render")
	must(t, html, `:root[data-layout="wide"]{--main-w:100%;`, "the full width changes the one main width variable")
	must(t, html, "main{max-width:var(--main-w);", "main follows the width variable")
	must(t, html, ".sysnote{max-width:var(--main-w);", "notices follow the width variable")
	must(t, html, ".upd-page{display:flex;flex-direction:column;gap:12px;max-width:var(--read-w)}", "the update page uses the shared reading width")
	set := spaFunctionLine(html, "settings")
	if set == "" {
		t.Fatal("settings not found in SPA")
	}
	must(t, set, "${rateCard}\n    ${layoutCardHTML()}", "the width card stands right after the rating card")
	must(t, set, "qa('[data-layout-pick]').forEach(b=>{b.onclick=()=>applyLayout(b.dataset.layoutPick)});", "a click applies the width")
	for _, lang := range []string{"en", "ru", "uk"} {
		for _, key := range []string{"layout.card", "layout.box", "layout.wide", "layout.box.hint", "layout.wide.hint"} {
			if _, ok := spaDictKeysCached(html, "I18N", lang)[key]; !ok {
				t.Errorf("I18N.%s has no %q", lang, key)
			}
		}
	}
}

// TestAccentPickLogic runs currentAccent and applyAccent under node: yellow and fire are accepted and painted,
// an unknown stored accent falls back to violet, and every accent has a label in en, ru and uk.
func TestAccentPickLogic(t *testing.T) {
	html := spaSource()
	for _, id := range []string{"violet", "blue", "green", "graphite", "yellow", "fire", "brand"} {
		must(t, html, `.swatch[data-accent-pick="`+id+`"]`, "swatch colour for "+id)
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, "accent."+id) {
				t.Errorf("%s has no label for accent.%s", lang, id)
			}
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the accent checks")
	}
	var b strings.Builder
	b.WriteString(`
const STORE = {};
const localStorage = {getItem: k => (k in STORE ? STORE[k] : null), setItem: (k, v) => { STORE[k] = String(v); }};
const ROOT = {attrs: {}, getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }, setAttribute(k, v) { this.attrs[k] = String(v); }, removeAttribute(k) { delete this.attrs[k]; }};
const document = {documentElement: ROOT};
let painted = 0; const themeChanged = () => { painted++; };
let viewTouches = 0;
function touchPageViews(){ viewTouches++; }
const ACCENT_KEY='emtds-accent';
`)
	b.WriteString(spaBetween(html, "const ACCENTS=", "\nfunction isDark(") + "\n")
	for _, name := range []string{"currentAccent", "applyAccent"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn + "\n")
	}
	b.WriteString(`
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
applyAccent('yellow');
eq(STORE['emtds-accent'], 'yellow', 'yellow is saved');
eq(ROOT.attrs['data-accent'], 'yellow', 'yellow is painted on the root');
eq(currentAccent(), 'yellow', 'yellow reads back');
applyAccent('fire');
eq(ROOT.attrs['data-accent'], 'fire', 'fire is painted on the root');
eq(currentAccent(), 'fire', 'fire reads back');
applyAccent('lava');
eq(STORE['emtds-accent'], 'violet', 'an unknown accent saves violet');
eq('data-accent' in ROOT.attrs, false, 'violet drops the root attribute');
STORE['emtds-accent'] = 'neon';
eq(currentAccent(), 'violet', 'an unknown stored accent reads as violet');
eq(painted, 3, 'each pick repaints the theme');
eq(viewTouches, 3, 'each accent pick schedules the account view');
STORE['emtds-accent'] = '#3366cc';
delete ROOT.attrs['data-accent'];
eq(currentAccent(), 'custom', 'a stored hex is the custom accent');
applyAccent('blue');
eq(STORE['emtds-accent'], 'blue', 'a preset replaces the custom colour');
eq(viewTouches, 4, 'a preset after a custom colour schedules the account view');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "accent.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("accent checks failed:\n%s", out)
	}
}
