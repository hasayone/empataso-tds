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
	m := pureRe.FindStringSubmatch(string(indexHTML))
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	f := filepath.Join(t.TempDir(), "logic.js")
	if err := os.WriteFile(f, []byte(m[1]+logicChecks), 0o644); err != nil {
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

eq(leadingMark('❌ SpinDog'), true, 'emoji before name');
eq(leadingMark('🟢 Bingo'), true, 'circle emoji before name');
eq(leadingMark('711 Casino'), false, 'digit first');
eq(leadingMark('alloffers'), false, 'letter first');
eq(leadingMark(''), false, 'empty string');
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
eq(homePage({access:{sections:['flows']}, pages:['flows']}), 'flows', 'homePage flows');
eq(homePage({access:{sections:[]}, pages:[]}), 'settings', 'homePage no dashboard');

eq(userIsEnv({source:'env'}), true, 'userIsEnv env');
eq(userIsEnv({source:'file'}), false, 'userIsEnv file');
eq(userIsEnv(null), false, 'userIsEnv null');

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

if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`

// TestMoneyDisplay runs the SPA currency helpers under node and checks the
// labels: a zero key is not a second currency, revenue without spend is an
// em dash, and two nonzero currencies stay a code list. It skips when node is absent.
// The uitest tag keeps this off the default go test, which does not need node.
func TestMoneyDisplay(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping money display checks")
	}
	html := string(indexHTML)
	var b strings.Builder
	for _, name := range []string{"moneyText", "bareMoney", "cellRatio", "moneyMap", "costCode", "costCodesList", "roiPct", "metricROI", "cpcCell", "moneyOf", "profitOf", "clickCostBy", "addBy", "pct", "periodSpendBy", "campaignCostOnly", "spendShare", "overlapShare", "rangeBounds"} {
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
const NA='—';
var FACTS={ready:true,clicks:[{group_id:'g1',offer_id:'o1',network_id:'n1',stream:'s',hits:2,uniq:1,cost_cents:150,cost_cur:'USD'},{group_id:'g2',offer_id:'o1',network_id:'n1',stream:'s',hits:1,uniq:1,cost_cents:0,cost_cur:''}],status:[]};
var SPEND=[];
rangeBounds=function(){return {from:new Date('2020-01-01T00:00:00Z'),to:new Date('2030-01-01T00:00:00Z')}};
eq(metricROI(12.34, {}, true), '—', 'revenue and no spend');
eq(metricROI(12.34, {EUR:100, USD:100}, true), 'EUR · USD', 'two currencies list codes');
eq(moneyOf(12.34, {EUR:100, USD:100}, true, 2), '12.34', 'multi revenue has no currency mark');
eq(profitOf(12.34, {EUR:100, USD:100}, true), '12.34 · 1.0000 EUR · 1.0000 $', 'multi profit lists spend');
eq(moneyOf(12.34, {EUR:0, GBP:100}, true, 2), '12.34 GBP', 'revenue keeps the nonzero currency');
eq(moneyMap({EUR:0, GBP:100}, 4), '0.0000 EUR · 1.0000 GBP', 'zero keys stay in the spend list');
eq(moneyOf(0, {EUR:100}, false, 2), '0.00 EUR', 'unready facts keep EUR');
eq(cpcCell({hits:0, costBy:{EUR:100}}), '—', 'zero clicks are not a price');
eq(moneyText(1, '', 2), '1.00', 'no currency at all is the number');
eq(moneyText(1, 'USD', 2), '1.00 $', 'USD is dollars');
eq(clickCostBy(), {USD:150}, 'click prices sum by currency');
eq(clickCostBy(function(r){return r.group_id==='g2'}), {}, 'zero cents add no key');
eq(moneyMap({'':300}, 4), '3.0000', 'empty currency is a bare number');
eq(costCode({'':300}).code, '', 'empty bucket is one currency');
eq(costCode({'':300}).multi, false, 'empty bucket is not multi');
eq(metricROI(6, {'':300}, true), '100.00%', 'roi of one empty bucket');
eq(campaignCostOnly('g1'), {USD:150}, 'campaign click price');
eq(periodSpendBy(), {USD:150}, 'period click price');
const keptClicks=FACTS.clicks;
FACTS.clicks=[{group_id:'g3',hits:1,cost_cents:300,cost_cur:''}];
eq(clickCostBy(), {'':300}, 'empty currency is a bucket');
FACTS.clicks=keptClicks;
SPEND=[{group_id:'g1',currency:'USD',cents:100,from:'2020-01-01',to:'2020-02-01'}];
spendShare=function(){return 1};
eq(campaignCostOnly('g1'), {USD:250}, 'manual window plus click price');
eq(pct(1,0), '—', 'pct zero base');
eq(cellRatio(0,0), '—', 'cellRatio zero base');
eq(roiPct(100,0), 0, 'roi no cost');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
