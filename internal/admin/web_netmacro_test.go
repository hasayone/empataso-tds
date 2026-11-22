//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNetMacroOverlayLogic runs the macro colouring of the postback address under node. A fragment in braces
// such as {status} is wrapped in a span, plain text and markup typed into the address are only escaped, an empty
// field stays empty, and the overlay follows typing and a value set from code while the value read from the
// textarea is the plain text without any wrapper. It skips when node is absent.
func TestNetMacroOverlayLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the macro overlay checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(`const esc=s=>String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
class HTMLTextAreaElement{ constructor(){this._v=''; this.dataset={}; this.listeners={}; this.offsetWidth=300; this.clientWidth=300; this.scrollTop=0}
  get value(){return this._v} set value(v){this._v=String(v)}
  addEventListener(n,f){this.listeners[n]=f} }
const mk=()=>({className:'', attrs:{}, style:{props:{}, setProperty(k,v){this.props[k]=v}}, kids:[], innerHTML:'', scrollTop:0,
  classList:{items:[], add(c){this.items.push(c)}}, setAttribute(k,v){this.attrs[k]=v}, append(...k){this.kids.push(...k)}});
const document={createElement:()=>mk()};
const getComputedStyle=()=>({borderLeftWidth:'1px'});
`)
	for _, name := range []string{"braceMacroHTML", "bindMacroOverlay"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const ok = (c, w) => { if (!c) { failed++; console.log('FAIL ' + w); } };
const eq = (a, b, w) => ok(a === b, w + ': got ' + JSON.stringify(a) + ', want ' + JSON.stringify(b));
eq(braceMacroHTML(''), '', 'an empty field stays empty');
eq(braceMacroHTML(null), '', 'null is empty');
eq(braceMacroHTML('https://x.io/pb?a=1'), 'https://x.io/pb?a=1', 'plain text is not coloured');
eq(braceMacroHTML('a={status}&b'), 'a=<span class="hl-v">{status}</span>&amp;b', 'a macro is coloured, the ampersand escaped');
eq(braceMacroHTML('{sub1}{payout}'), '<span class="hl-v">{sub1}</span><span class="hl-v">{payout}</span>', 'neighbouring macros');
eq(braceMacroHTML('x={{income}}'), 'x=<span class="hl-v">{{income}}</span>', 'double braces are one macro');
eq(braceMacroHTML('{ }'), '{ }', 'braces with a space are not a macro');
eq(braceMacroHTML('{}'), '{}', 'empty braces are not a macro');
eq(braceMacroHTML('<b>{status}</b>'), '&lt;b&gt;<span class="hl-v">{status}</span>&lt;/b&gt;', 'typed markup is escaped');
eq(braceMacroHTML('a\n'), 'a\n ', 'a last empty line keeps its room');

const wrapParent = {inserted: null, insertBefore(w, ta) { this.inserted = w; }};
const ta = new HTMLTextAreaElement();
ta.parentNode = wrapParent;
ta.value = 'https://x.io/postback?subid={sub1}&status=REPLACE';
bindMacroOverlay(ta);
const wrap = wrapParent.inserted, back = wrap.kids[0];
ok(wrap.className === 'hl-field' && wrap.kids[1] === ta && back.className === 'hl-back', 'the copy lies under the textarea inside one wrapper');
ok(wrap.classList.items.includes('on'), 'the wrapper is switched on only after the copy exists');
ok(back.attrs['aria-hidden'] === 'true', 'the copy is hidden from readers');
ok(back.innerHTML.includes('<span class="hl-v">{sub1}</span>') && !back.innerHTML.includes('REPLACE</span>'), 'the first value is coloured: ' + back.innerHTML);
eq(ta.value, 'https://x.io/postback?subid={sub1}&status=REPLACE', 'the value is the plain text');
ta.value = 'u?s={status}';
ok(back.innerHTML === 'u?s=<span class="hl-v">{status}</span>', 'a value set from code refreshes the copy: ' + back.innerHTML);
eq(ta.value, 'u?s={status}', 'the value stays plain after a refresh');
ta._v = 'u?p={payout}'; ta.listeners.input();
ok(back.innerHTML === 'u?p=<span class="hl-v">{payout}</span>', 'typing refreshes the copy');
ta.value = '';
eq(back.innerHTML, '', 'an emptied field empties the copy');
ta.clientWidth = 285; ta.scrollTop = 7; ta.listeners.scroll();
eq(back.scrollTop, 7, 'the copy scrolls with the field');
ta.value = 'x';
eq(back.style.props['--sb'], '13px', 'the scroll bar width is taken off the copy');
bindMacroOverlay(ta);
ok(wrapParent.inserted === wrap, 'binding twice does nothing');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "netmacro.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("macro overlay checks failed:\n%s", out)
	}
}

// TestNetMacroMarkup checks the postback address field of the network dialog: it is coloured by the overlay and
// the dialog binds it, the label carries a question mark with a popup in the style of the offer parameters,
// every variable of the popup and every other name it lists is one the engine really reads, short names come
// first, and the texts exist in three languages. The note that the tracker does not call the address stays.
func TestNetMacroMarkup(t *testing.T) {
	html := spaSource()
	must(t, html, `<label class="lbl-h" for="nurl">${tr('net.url')}${netPostbackHelp()}</label><textarea id="nurl"`, "the label has the question mark")
	must(t, html, "bindMacroOverlay(q('#nurl',m));", "the dialog binds the address field")
	must(t, html, `<span class="qh" tabindex="0" role="note" aria-label="${esc(tr('net.pb.help.t'))}">?<span class="qh-pop">`, "the popup is the one of the offer parameters")
	must(t, html, ".hl-field.on>textarea{background:transparent;color:transparent;caret-color:var(--fg)}", "the text shows through the copy, the caret stays")
	must(t, html, ".hl-v{color:var(--accent)}", "a macro takes the theme accent")
	mustNot(t, spaFunctionLine(html, "bindMacroOverlay"), "ta.innerHTML", "no markup is written into the field")
	mustNot(t, spaFunctionLine(html, "bindMacroOverlay"), "ta.value=", "the field value is never rewritten")
	must(t, html, "tr('net.hint')", "the note that the tracker does not call the address stays")

	line := spaConstLine(html, "  const NET_PB_VARS=")
	if line == "" {
		t.Fatal("NET_PB_VARS not found in the SPA")
	}
	names := regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(line, -1)
	if len(names) < 3 || names[0][1] != "subid" || names[1][1] != "status" || names[2][1] != "payout" {
		t.Fatalf("short names must come first: %v", names)
	}
	engine, err := os.ReadFile(filepath.Join("..", "..", "cmd", "engine", "postback.go"))
	if err != nil {
		t.Skip("engine source is not in the test image")
	}
	src := string(engine)
	quoted := func(n string) bool { return strings.Contains(src, `"`+n+`"`) || strings.Contains(src, `Get("`+n+`")`) }
	for _, n := range names {
		k := n[1]
		if !quoted(k) {
			t.Errorf("the engine does not read %q", k)
		}
		for _, lang := range []string{"'net.pb.help." + k + "':"} {
			if got := strings.Count(html, lang); got != 3 {
				t.Errorf("%s is translated %d times, want 3", lang, got)
			}
		}
	}
	for _, alias := range []string{"clickid", "cid", "type", "goal", "profit", "sum", "DepSum"} {
		if !quoted(alias) {
			t.Errorf("the popup names %q but the engine does not read it", alias)
		}
		if !strings.Contains(html, alias) {
			t.Errorf("alias %q is missing in the popup text", alias)
		}
	}
	for _, key := range []string{"net.pb.help.t", "net.pb.help.a"} {
		if got := strings.Count(html, "'"+key+"':"); got != 3 {
			t.Errorf("%s is translated %d times, want 3", key, got)
		}
	}
}
