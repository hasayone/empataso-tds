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

// TestFlowDefaultsLogic runs the new-campaign defaults and the placeholders of the Parameters tab under node.
// A new campaign answers 404 when no stream matches and tells visitors apart by address and user-agent; a
// campaign that was saved keeps its own type and its address-only uniqueness, also through collectFlow. The
// empty fields of the Parameters tab carry sample placeholders that are not values and are never saved,
// while the saved names and tokens stay in the fields. It skips when node is absent.
func TestFlowDefaultsLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the campaign defaults checks")
	}
	html := spaSource()
	consts := []string{spaConstLine(html, "const DEFAULT_ANSWER="), spaConstLine(html, "const DEFAULT_UNIQ_BY=")}
	if consts[0] == "" || consts[1] == "" {
		t.Fatal("DEFAULT_ANSWER or DEFAULT_UNIQ_BY is not declared in the SPA")
	}
	var src strings.Builder
	src.WriteString(`const esc=s=>String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
const DICT={'src.our':'Имя у нас','src.theirs':'Имя в ссылке','Token':'Токен'};
const tr=k=>DICT[k]||k;
const SUBID_MAX=3;
const SRC_KEYS=['keyword','site','ad_campaign_id','creative_id','external_id',...Array.from({length:SUBID_MAX},(_,i)=>'sub_id_'+(i+1))];
const ME=null, sourceKeyLocked=()=>false, priceParamLocked=()=>false;
let GROUPS=[], OPEN_SETTINGS=false, STAMP=0, CUR=null;
const ensureNums=()=>{}, nextNum=()=>++STAMP, defaultGeo=()=>'db', markDirty=()=>{}, go=()=>{};
`)
	src.WriteString(strings.Join(consts, "\n") + "\n")
	for _, name := range []string{"newFlow", "flowUniqBy", "flowAnswerType", "paramExample", "campaignParamHTML", "campaignParamDraft", "collectFlow"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		for _, c := range consts {
			fn = strings.Replace(fn, c, "", 1)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const ok = (cond, what) => { if (!cond) { failed++; console.log('FAIL ' + what); } };
const eq = (a, b, what) => ok(JSON.stringify(a) === JSON.stringify(b), what + ': ' + JSON.stringify(a) + ' != ' + JSON.stringify(b));

newFlow();
const fresh = GROUPS[0];
ok(fresh.redirect === '404_not_found', 'a new campaign answers 404, not text: ' + fresh.redirect);
ok(fresh.uniq_by === 'ip_ua', 'a new campaign starts on address and user-agent: ' + fresh.uniq_by);
ok(flowAnswerType(fresh) === '404_not_found' && flowUniqBy(fresh) === 'ip_ua', 'the form shows what a new campaign holds');
ok(flowAnswerType({}) === '404_not_found' && flowAnswerType({redirect: ''}) === '404_not_found', 'an empty type shows 404');

ok(flowAnswerType({redirect: 'show_text'}) === 'show_text' && flowAnswerType({redirect: 'http_redirect'}) === 'http_redirect', 'a saved type is kept');
ok(flowUniqBy({}) === 'ip' && flowUniqBy({uniq_method: 'ip'}) === 'ip', 'a campaign saved without the field stays on the address alone');
ok(flowUniqBy({uniq_by: 'ip_ua'}) === 'ip_ua', 'a saved user-agent mode is kept');

const els = {};
const drawer = {dataset: {}};
let q = sel => (sel === '#gdrawer' ? drawer : (sel[0] === '#' ? els[sel.slice(1)] || null : null));
let qa = () => [];
const saved = {id: 'abc', name: 'old', redirect: 'show_text', header: 'text/html', out: 'hello', uniq_seconds: 7200};
CUR = saved;
Object.assign(els, {g_id: {value: 'abc'}, g_name: {value: 'old'}, g_redirect: {value: flowAnswerType(saved)}, g_header: {value: 'text/html'}, g_geo: {value: 'db'}, g_uniqby: {value: flowUniqBy(saved)}, g_uniqh: {value: '2'}, g_uniqck: {checked: false}});
collectFlow();
ok(CUR.redirect === 'show_text' && CUR.out === 'hello', 'a loaded campaign keeps its type and text after a read of the form: ' + JSON.stringify(CUR));
ok(!('uniq_by' in CUR) && !('uniq_cookie' in CUR), 'a loaded campaign stays on the address alone after a read of the form: ' + JSON.stringify(CUR));

eq(paramExample('keyword'), {name: 'utm_term', token: '{keyword}'}, 'keyword sample');
eq(paramExample('sub_id_7'), {name: 'sub7', token: '{sub7}'}, 'a sub id sample follows its number');
eq(paramExample('cost'), {name: 'cost', token: '{price}'}, 'cost sample');
eq(paramExample('currency'), {name: 'currency', token: '{currency}'}, 'currency sample');
eq(paramExample('nothing'), {name: '', token: ''}, 'an unknown key has no sample');
ok(SRC_KEYS.every(k => paramExample(k).name && paramExample(k).token), 'every parameter has a sample');

const inputs = h => [...h.matchAll(/<input ([^>]*)>/g)].map(m => m[1]);
const attr = (a, n) => { const m = new RegExp(' ' + n + '="([^"]*)"').exec(' ' + a); return m ? m[1] : null; };
const byAttr = (h, n, v) => inputs(h).find(a => attr(a, n) === v) || '';

const blank = campaignParamHTML({}, null);
const editable = inputs(blank).filter(a => !/readonly/.test(a));
ok(editable.length === 2 * (SRC_KEYS.length + 2), 'a name and a token field for every row: ' + editable.length);
ok(editable.every(a => attr(a, 'value') === '' && attr(a, 'placeholder')), 'every empty field has a placeholder and no value');
ok(attr(byAttr(blank, 'data-src-param', 'site'), 'placeholder') === 'site' && attr(byAttr(blank, 'data-src-token', 'site'), 'placeholder') === '{site}', 'site placeholders');
ok(attr(byAttr(blank, 'id', 'g_cost_token'), 'placeholder') === '{price}' && attr(byAttr(blank, 'id', 'g_currency_token'), 'placeholder') === '{currency}', 'price placeholders');
ok(attr(byAttr(blank, 'data-src-token', 'sub_id_2'), 'data-mask') === '', 'an empty sub id has no outgoing mask, only a sample');
ok(attr(byAttr(blank, 'data-src-token', 'sub_id_2'), 'placeholder') === '{sub2}', 'sub id token sample');

const filled = campaignParamHTML({parameters: {keyword: 'kw'}, param_tokens: {keyword: '{kw}'}, cost_param: 'c', cost_token: '{p}'}, {parameters: {sub_id_1: 's1'}});
ok(attr(byAttr(filled, 'data-src-param', 'keyword'), 'value') === 'kw' && attr(byAttr(filled, 'data-src-token', 'keyword'), 'value') === '{kw}', 'a saved name and token stay in the fields');
ok(attr(byAttr(filled, 'id', 'g_cost_param'), 'value') === 'c' && attr(byAttr(filled, 'id', 'g_cost_token'), 'value') === '{p}', 'a saved price name and token stay');
ok(attr(byAttr(filled, 'data-src-param', 'sub_id_1'), 'value') === 's1', 'the name from the source stays in the field');
ok(attr(byAttr(filled, 'data-src-token', 'sub_id_1'), 'data-mask') === '{sub_id_1}' && attr(byAttr(filled, 'data-src-token', 'sub_id_1'), 'placeholder') === '{sub_id_1}', 'a filled sub id keeps its outgoing mask');

const rows = [
  {key: 'keyword', name: '', base: '', token: '', tokenHint: attr(byAttr(blank, 'data-src-token', 'keyword'), 'data-mask')},
  {key: 'sub_id_2', name: '', base: '', token: '', tokenHint: attr(byAttr(blank, 'data-src-token', 'sub_id_2'), 'data-mask')},
];
eq(campaignParamDraft(rows), {parameters: {}, tokens: {}}, 'a placeholder is never saved');
eq(campaignParamDraft([{key: 'keyword', name: 'utm_term', base: '', token: '{keyword}', tokenHint: ''}]), {parameters: {keyword: 'utm_term'}, tokens: {keyword: '{keyword}'}}, 'a token typed like its sample is saved');
if (failed) process.exit(1);
`)
	if flow := spaFunctionLine(html, "collectFlow"); !strings.Contains(flow, "tok.dataset.mask") || strings.Contains(flow, "getAttribute('placeholder')") {
		t.Error("collectFlow must read the outgoing mask from data-mask, not from the placeholder")
	}
	f := filepath.Join(t.TempDir(), "flowdefaults.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("campaign defaults checks failed:\n%s", out)
	}
}

// TestFlowDefaultsMarkup checks the order of the first tab of the campaign settings, the drawer and the
// parameter hint in three languages, and that the form takes the defaults from the helpers instead of a
// literal type. The danger zone stays last.
func TestFlowDefaultsMarkup(t *testing.T) {
	html := spaSource()
	open := spaFunctionLine(html, "openFlowSettings")
	at := func(s string) int {
		i := strings.Index(open, s)
		if i < 0 {
			t.Fatalf("openFlowSettings has no %q", s)
		}
		return i
	}
	order := []int{at("tr('ed.identity')"), at("tr('ed.visitors')"), at(`id="sec-default"`), at("tr('ed.flood')"), at("tr('Danger zone')")}
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] {
			t.Fatalf("first tab order: identity, visitors, default answer, antiflood, danger zone; broken at step %d: %v", i, order)
		}
	}
	for _, id := range []string{"g_geo", "g_uniqby", "g_uniqh", "g_uniqck", "g_sticky", "g_savekeys", "g_savekeys_se"} {
		i := strings.Index(open, `id="`+id+`"`)
		if i < 0 {
			i = at("sel('" + id + "'")
		}
		if i < order[1] || i > order[2] {
			t.Errorf("field %s must stay in the visitors block", id)
		}
	}
	for _, s := range []string{"flowUniqBy(g)", "flowAnswerType(g)"} {
		if !strings.Contains(open, s) {
			t.Errorf("the form does not use %s", s)
		}
	}
	if strings.Contains(open, "||'stop'") {
		t.Error("the form must not fall back to a literal type")
	}
	if n := strings.Count(html, "'par.hint':'"); n != 3 {
		t.Errorf("par.hint is translated %d times, want 3", n)
	}
	if !regexp.MustCompile(`class="hint param-hint">\$\{esc\(tr\('par\.hint'\)\)\}`).MatchString(open) {
		t.Error("the parameters tab has no hint")
	}
}
