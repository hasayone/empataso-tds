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
	for _, name := range []string{"curKey", "curSign", "moneyUnits", "moneyText", "asSortMeta", "applySort", "condsQS"} {
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
eq(hideChoices(null, null), ['profit','cost'], 'no field list gives the money groups');
eq(hideChoices([{name:'ts',hideable:false},{name:'ip',hideable:true},{name:'cost_cents',hideable:true},{name:'profit',hideable:true},{name:'country'}], ['ip','legacy']), ['profit','cost','ip','legacy'], 'only hideable fields, the cost members are the cost group, leftovers stay');
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
eq(reportTotals(repRows), {hits:30,unique:9,bots:1,conv:3,profit:6.75}, 'report total takes the unique of the first step');
eq(reportTotals([{hits:10,unique:4,conv:2},{hits:20,unique:9,conv:1}]).unique, 13, 'an old backend sums unique');
eq(reportTotals([{hits:1,unique:5,uniq_first:0},{hits:1,unique:5}]).unique, 5, 'a zero uniq_first is a value, not a missing field');
eq(reportTotals([{hits:1,uniq_first:2},{hits:1,unique:7}]).unique, 9, 'each row uses its own best field');
eq(reportTotals([{hits:'x',unique:null,conv:undefined,profit:'3'}]), {hits:0,unique:0,bots:0,conv:0,profit:3}, 'bad numbers count as zero');
eq(reportTotals([null,{hits:2}]).hits, 2, 'an empty row is skipped');
eq(reportTotals([]), {hits:0,unique:0,bots:0,conv:0,profit:0}, 'no rows give zero sums');
eq(reportTotals(null).hits, 0, 'no list gives zero sums');
const repCR=reportTotals(repRows);
eq(repCR.conv/repCR.hits, 0.1, 'the total ratio comes from the sums, not from the row ratios');
eq(reportTotals([{hits:1,unique:1,uniq_first:1}]).unique, 1, 'one row keeps its uniques');

// The report body is read with its truncated flag; an old bare array and junk are safe.
eq(reportData({rows:[{hits:1}],truncated:true}), {rows:[{hits:1}],truncated:true}, 'a cut report is flagged');
eq(reportData({rows:[],truncated:false}), {rows:[],truncated:false}, 'a whole report is not flagged');
eq(reportData({rows:[{hits:1}]}).truncated, false, 'a missing flag is not truncated');
eq(reportData({rows:[],truncated:'yes'}).truncated, false, 'only true means truncated');
eq(reportData([{hits:2}]), {rows:[{hits:2}],truncated:false}, 'an older server sends a bare array');
eq(reportData(null), {rows:[],truncated:false}, 'no body gives no rows');
eq(reportData({rows:'x'}), {rows:[],truncated:false}, 'bad rows give no rows');

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
eq(subIDRows({sub_ids:{sub_id_3:{flag:1,values:['a']}, sub_id_1:{flag:2,values:['x'],empty:true}, sub_id_13:{flag:1,values:['z']}}}),
  [{n:1,flag:2,values:['x'],empty:true},{n:3,flag:1,values:['a'],empty:false}], 'subIDRows: ordered, 1..12 only');
eq(subIDRows({}), [], 'subIDRows: none');
eq(subIDRows({sub_ids:{sub_id_2:{flag:7}}}), [{n:2,flag:2,values:[],empty:false}], 'subIDRows: a flag that is not 1 reads as only');
eq(subIDMap([{n:2,flag:1,values:[' a ','',' b'],empty:false},{n:2,flag:2,values:['z'],empty:true},{n:0,flag:1,values:['q']},{n:13,flag:1,values:['q']}]),
  {sub_id_2:{flag:2,values:['z'],empty:true}}, 'subIDMap: last row of a number wins, out-of-range dropped');
eq(subIDMap([{n:4,flag:1,values:['q'],empty:false}]), {sub_id_4:{flag:1,values:['q']}}, 'subIDMap: empty is written only when true');
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

eq(fltApplyDraft([{f:'campaign',op:'eq',v:['Away']}])[0].v, ['Away'], 'apply keeps strict without catalog');

{const inp={value:'Create \\"X\\"', dataset:{}}; groupInputSet(inp,'X'); eq(groupInputValue(inp), 'X', 'group key not caption');}
{const inp={value:'G1',dataset:{groupKey:'G1'}}; inp.value='G2'; comboClearStoredKeyOnEdit(inp,'groupKey'); eq(groupInputValue(inp), 'G2', 'group manual edit drops stale key');}
{const inp={value:'G1',dataset:{groupKey:'G1'}}; inp.value=''; comboClearStoredKeyOnEdit(inp,'groupKey'); eq(groupInputValue(inp), '', 'group clear empty');}
eq(comboKeyMatchesText('UTC · UTC+0', 'UTC', z=>'UTC · UTC+0'), true, 'combo label match');

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
	for _, name := range []string{"curKey", "curSign", "moneyUnits", "moneyText", "csvMoney", "plainMoney", "cellRatio", "normBy", "moneyMap", "costCode", "costCodesList", "spendBasis", "profitList", "roiPct", "metricROI", "roiCls", "cpcCell", "cpcHasSpend", "moneyOf", "profitOf", "clickCostBy", "addBy", "pct", "periodSpendBy", "campaignCostOnly", "spendShare", "overlapShare", "rangeBounds", "sumClicks", "foldStatus", "packMetrics", "factsIndex", "campaignMetrics", "pointSpendBy", "pointMoney", "nameSortKey", "nameCollator", "nameIconRank", "nameScript", "nameKey", "compareNameKeys", "asSortMeta", "applySort", "fltCommit", "logSortField"} {
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

if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
