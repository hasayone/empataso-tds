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

// TestSettingsTabsLogic runs the tabs of the campaign settings drawer under node: the tab bar lists the
// settings, links, S2S postbacks and, last, the notes; a panel is hidden unless its tab is open; the notes
// field is an editable textarea holding the stored text; showGTab opens one panel; and collectFlow puts what
// was typed into the campaign's notes (and drops a blank note) without touching the uniqueness fields.
// It skips when node is absent.
func TestSettingsTabsLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the settings tab checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(`const esc=s=>String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
const DICT={'flow.tabs':'Разделы кампании','flow.tab.notes':'Заметки','flow.notes.ph':'Заметки об этой кампании','Parameters':'Параметры','ed.links':'Ссылки','s2s.title':'S2S постбеки','Settings':'Настройки'};
const tr=k=>DICT[k]||k;
`)
	var gtabsLine string
	for _, marker := range []string{"const GTABS="} {
		i := strings.Index(html, marker)
		if i < 0 {
			t.Fatalf("%s not found in SPA", marker)
		}
		line := html[i:]
		gtabsLine = line[:strings.Index(line, "\n")]
		src.WriteString(gtabsLine + "\n")
	}
	for _, name := range []string{"linkBreaks", "linkRowHTML", "gTabsHTML", "gPaneOpen", "gNotesHTML", "showGTab", "collectFlow"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(strings.Replace(fn, gtabsLine, "", 1) + "\n")
	}
	src.WriteString(`
let failed = 0;
const ok = (cond, what) => { if (!cond) { failed++; console.log('FAIL ' + what); } };
const ids = h => [...h.matchAll(/id="gtab-([a-z0-9]+)"/g)].map(m => m[1]).join(',');
const bar = gTabsHTML('main');
ok(ids(bar) === 'main,params,links,s2s,notes', 'tab order: ' + ids(bar));
ok(bar.indexOf('>Заметки</button></div></div>') > 0, 'notes is the last tab');
ok(bar.indexOf('>Настройки<') < bar.indexOf('>Параметры<') && bar.indexOf('>Параметры<') < bar.indexOf('>Ссылки<') && bar.indexOf('>Ссылки<') < bar.indexOf('>S2S постбеки<'), 'labels follow the order');
ok(/id="gtab-main"[^>]*aria-selected="true"/.test(bar) && /id="gtab-notes"[^>]*aria-selected="false"/.test(bar), 'the open tab is selected');
ok(/id="gtab-notes"[^>]*aria-selected="true"/.test(gTabsHTML('notes')), 'the notes tab can be the open one');

ok(!/hidden/.test(gPaneOpen('main', 'main')) && /hidden/.test(gPaneOpen('notes', 'main')), 'only the open panel is shown');

const filled = gNotesHTML({notes: 'first line\n<b>second</b>'});
ok(filled.includes('<textarea id="g_notes"') && !/readonly|disabled|hidden/.test(filled), 'the notes field is an editable textarea: ' + filled);
ok(filled.includes('first line\n&lt;b&gt;second&lt;/b&gt;</textarea>'), 'the saved note sits in the field, escaped: ' + filled);
const empty = gNotesHTML({});
ok(empty.includes('placeholder="Заметки об этой кампании"') && empty.includes('>\n</textarea>'), 'an empty note is an empty field with a placeholder: ' + empty);
ok(gNotesHTML(null).includes('<textarea id="g_notes"'), 'the field is there without a campaign too');

const pbUrl = 'https://x.io/postback?subid={subid}&status={status}&payout={payout}';
const pb = linkRowHTML('/postback', pbUrl);
ok(pb.includes('<wbr>?subid={subid}<wbr>&amp;status={status}<wbr>&amp;payout={payout}'), 'an address may wrap only before ? and &: ' + pb);
ok(pb.replace(/<wbr>/g, '').includes('https://x.io/postback?subid={subid}&amp;status={status}&amp;payout={payout}'), 'the address text is unchanged');
ok(pb.includes('<code class="lnk-url">') && !pb.includes('lnk-map'), 'a plain address is code without a map line');
const noted = linkRowHTML('Старый пиксель', 'https://x.io/?pb=KEY&cid=[CID]', false, null, 'Только <старым>');
ok(noted.indexOf('class="lbl"') < noted.indexOf('class="lnk-note">Только &lt;старым&gt;</div>') && noted.indexOf('lnk-note') < noted.indexOf('lnk-url'), 'the note sits between the label and the address, escaped: ' + noted);
ok(!linkRowHTML('x', 'https://x.io/a').includes('lnk-note'), 'a card without a note has no note line');
const open = linkRowHTML('ссылка', 'https://x.io/abc', true);
ok(open.includes('<a class="mono lnk-url" href="https://x.io/abc" target="_blank" rel="noreferrer">'), 'the first address is a link');
const par = linkRowHTML('с параметрами', 'https://x.io/abc?p1=a&p2=b', false, ['[PAR-1]', '[PAR-2]']);
ok(par.includes('<div class="lnk-map"><span aria-hidden="true">→</span><code>[PAR-1]</code><code>[PAR-2]</code></div>') && par.indexOf('lnk-url') < par.indexOf('lnk-map'), 'the placeholders sit under the address: ' + par);

function mk(o) {
  const e = Object.assign({hidden: false, dataset: {}, attrs: {}, tabIndex: 0}, o);
  e.cls = new Set();
  e.classList = {toggle: (n, on) => (on ? e.cls.add(n) : e.cls.delete(n))};
  e.setAttribute = (k, v) => { e.attrs[k] = v; };
  return e;
}
const tabs = ['main', 'params', 'links', 's2s', 'notes'].map(id => mk({dataset: {gtab: id}}));
const panes = ['main', 'params', 'links', 's2s', 'notes'].map(id => mk({dataset: {gpane: id}, hidden: id !== 'main'}));
let q = () => null;
let qa = sel => (sel === '[data-gpane]' ? panes : sel === '[data-gtab]' ? tabs : []);
showGTab({}, 'notes');
ok(panes.map(p => p.hidden).join() === 'true,true,true,true,false', 'the notes panel alone is shown: ' + panes.map(p => p.hidden).join());
ok(tabs[4].cls.has('on') && tabs[4].attrs['aria-selected'] === 'true' && !tabs[0].cls.has('on'), 'the notes tab is marked');
showGTab({}, 's2s');
ok(panes.map(p => p.hidden).join() === 'true,true,true,false,true', 'the S2S panel alone is shown');

let CUR = {id: 'abc', notes: 'old', uniq_by: 'ip_ua', uniq_cookie: true, uniq_seconds: 7200};
const drawer = {dataset: {}};
const field = {value: 'typed here\nsecond line  '};
q = sel => (sel === '#gdrawer' ? drawer : sel === '#g_notes' ? field : null);
const GROUPS = [];
collectFlow();
ok(CUR.notes === 'typed here\nsecond line  ', 'typed text reaches the campaign: ' + JSON.stringify(CUR.notes));
ok(CUR.uniq_by === 'ip_ua' && CUR.uniq_cookie === true, 'the notes field does not rewrite the uniqueness mode');
field.value = '   \n ';
collectFlow();
ok(!('notes' in CUR), 'a blank note is dropped from the campaign');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "gtabs.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("settings tab checks failed:\n%s", out)
	}
}

// TestSettingsTabsMarkup checks that the flow canvas carries no tabs or notes panel, that the settings drawer
// builds its panels in the order settings (with the danger zone at the bottom), links, S2S, notes, that the
// notes field is read back by collectFlow, and that the labels exist in all three languages.
func TestSettingsTabsMarkup(t *testing.T) {
	html := spaSource()
	for _, gone := range []string{"flowpane-notes", "flowpane-streams", "flowtabs", "flownotes", "flowTabsHTML", "data-ftab"} {
		if strings.Contains(html, gone) {
			t.Errorf("%q is still in the SPA: the canvas holds only the streams", gone)
		}
	}
	render := spaFunctionLine(html, "renderFlow")
	if !strings.Contains(render, `<div id="tester"></div>`) || !strings.Contains(render, `<div id="checker-app"></div>`) {
		t.Error("the canvas page lost the tester or the checker block")
	}
	open := spaFunctionLine(html, "openFlowSettings")
	at := func(s string) int {
		i := strings.Index(open, s)
		if i < 0 {
			t.Fatalf("openFlowSettings has no %q", s)
		}
		return i
	}
	order := []int{at("gPaneOpen('main',gtab)"), at(`id="g_dup"`), at(`id="g_del"`), at("gPaneOpen('params',gtab)"), at(`<div id="g_params">`), at("gPaneOpen('links',gtab)"), at("tr('ed.links')"), at("gPaneOpen('s2s',gtab)"), at("s2sHTML("), at("gPaneOpen('notes',gtab)"), at("gNotesHTML(g)")}
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] {
			t.Fatalf("drawer order broken at step %d: %v", i, order)
		}
	}
	if strings.Index(open, `id="g_params"`) < strings.Index(open, "gPaneOpen('params',gtab)") {
		t.Error("the parameters list belongs to the parameters tab, not to the settings tab")
	}
	camp := spaFunctionLine(html, "campaignParamHTML")
	cost, currency, divider, subRows := strings.Index(camp, "${own('cost')}"), strings.Index(camp, "${own('currency')}"), strings.Index(camp, `class="param-sep"`), strings.Index(camp, "${subs.map(keyRow)")
	if !(cost > 0 && cost < currency && currency < divider && divider < subRows) {
		t.Errorf("price and currency come before the divider, the Sub ID rows after it (%d %d %d %d)", cost, currency, divider, subRows)
	}
	if strings.Count(camp, `class="param-sep"`) != 1 {
		t.Error("the Sub ID rows are one group behind a single divider")
	}
	if !strings.Contains(open, "gTabsHTML(gtab)") || !strings.Contains(open, "bindGTabs(el,") {
		t.Error("the drawer does not paint or wire its tab bar")
	}
	if !strings.Contains(spaFunctionLine(html, "collectFlow"), "v('g_notes')") {
		t.Error("collectFlow does not read the notes field")
	}
	links := regexp.MustCompile("linkRowHTML\\(tr\\('(lnk\\.pb\\.[a-z]+)'\\),base\\+'([^']+)',false,null,tr\\('(lnk\\.pb\\.[a-z]+\\.hint)'\\)\\)").FindAllStringSubmatch(open, -1)
	if len(links) != 2 || links[0][1] == links[1][1] || links[0][3] == links[1][3] {
		t.Fatalf("the two postback addresses need different labels and notes: %v", links)
	}
	if links[0][2] != "/?pb=KEY&cid=[SUBID]&profit=1.50" || links[1][2] != "/postback?subid={subid}&status={status}&payout={payout}" {
		t.Errorf("the postback addresses changed: %q and %q", links[0][2], links[1][2])
	}
	for _, key := range []string{"lnk.pb.old", "lnk.pb.old.hint", "lnk.pb.net", "lnk.pb.net.hint"} {
		if n := strings.Count(html, "'"+key+"':'"); n != 3 {
			t.Errorf("%s is translated %d times, want 3", key, n)
		}
	}
	if !strings.Contains(html, ".lnk-note{") {
		t.Error("style .lnk-note missing")
	}
	row, bar := spaFunctionLine(html, "s2sRow"), spaFunctionLine(html, "s2sBarHTML")
	field, barAt := strings.Index(row, `class="fld"`), strings.Index(row, "s2sBarHTML(p)")
	method, statuses, remove := strings.Index(bar, `<select class="s2s-method">`), strings.Index(bar, "s2sChecks(p)"), strings.Index(bar, "data-dels2s")
	if !strings.Contains(row, `class="s2s s2s-card"`) || !(field > 0 && field < barAt) || !(method > 0 && method < statuses && statuses < remove) {
		t.Errorf("a postback is a card: address, then method, statuses and remove in one bar (%d %d %d %d)", field, method, statuses, remove)
	}
	edit, editor := spaFunctionLine(html, "s2sEditorRow"), spaFunctionLine(html, "openS2SEditor")
	name, url, editBar := strings.Index(edit, `class="s2s-name"`), strings.Index(edit, `class="mono s2s-url"`), strings.Index(edit, "s2sBarHTML(p)")
	if !strings.Contains(edit, `class="gs2s s2s-card"`) || !(name > 0 && name < url && url < editBar) {
		t.Errorf("a shared template is a card: name, address, then the same bar (%d %d %d)", name, url, editBar)
	}
	if list, add := strings.Index(editor, `<div id="s2s-edit">`), strings.Index(editor, `<button type="button" id="s2s-edit-add">`); !(list > 0 && add > strings.Index(editor, "${rows}</div>")) {
		t.Errorf("the add button follows the list of templates (%d %d)", list, add)
	}
	for _, css := range []string{"#s2s-edit{display:flex;flex-direction:column;", "#s2s-edit-add{"} {
		if !strings.Contains(html, css) {
			t.Errorf("style %s missing", css)
		}
	}
	s2s := spaFunctionLine(html, "s2sHTML")
	if rows, add := strings.Index(s2s, `<div id="s2s-rows">`), strings.Index(s2s, `<button type="button" id="s2s-add">`); !(rows > 0 && add > strings.Index(s2s, "rows.map(s2sRow).join('')</div>")) {
		t.Errorf("the add button must follow the list, outside any card (%d %d)", rows, add)
	}
	for _, key := range []string{"'flow.tabs':'", "'flow.tab.notes':'", "'flow.notes.ph':'"} {
		if n := strings.Count(html, key); n != 3 {
			t.Errorf("%s is translated %d times, want 3", key, n)
		}
	}
	for _, css := range []string{".gtabs{", ".gpane[hidden]{", ".gnotes{"} {
		if !strings.Contains(html, css) {
			t.Errorf("style %s missing", css)
		}
	}
}
