//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLogicPickSearch runs the search behind every entity picker under node: case, ё and the
// other keyboard layout do not matter, words match in any order, an exact id or number comes
// first, then a prefix, a word start and a plain substring, long lists are cut with a count,
// and the matched parts are cut out for highlighting. It skips when node is absent.
func TestLogicPickSearch(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	m := pureRe.FindStringSubmatch(spaSource())
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	f := filepath.Join(t.TempDir(), "pick.js")
	if err := os.WriteFile(f, []byte(m[1]+pickChecks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("pick search checks failed:\n%s", out)
	}
}

const pickChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const keys = r => r.items.map(p => p.key);

eq(pickFold('  Ёлка   BIG '), 'елка big', 'fold: case, ё and spaces');
eq(pickLayout('ghbdtn'), 'привет', 'layout en to ru');
eq(pickLayout('руддщ'), 'hello', 'layout ru to en');
eq(pickLayout('ab-12'), 'фи-12', 'digits and dashes stay');
eq(pickQuery(''), {full:[], terms:[]}, 'empty query');
eq(pickQuery('Shop 1'), {full:['shop 1','ырщз 1'], terms:[['shop','ырщз'],['1']]}, 'phrase and words with the other layout');

const camps = [
  {key:'summer-shop', label:'summer-shop — Летняя распродажа', alt:'12'},
  {key:'shop', label:'shop — Main shop', alt:'7'},
  {key:'blackfriday', label:'blackfriday — Black Friday shop', alt:'120'},
  {key:'winter', label:'winter — Зимний магазин', alt:'3'},
  {key:'workshop', label:'workshop — Мастерская', alt:'44'},
];
eq(keys(pickSearch(camps, 'shop', 10)), ['shop','summer-shop','blackfriday','workshop'], 'exact id, then prefix, word start, substring');
eq(keys(pickSearch(camps, 'SHOP', 10)), keys(pickSearch(camps, 'shop', 10)), 'case does not matter');
eq(keys(pickSearch(camps, 'ырщз', 10)), keys(pickSearch(camps, 'shop', 10)), 'typed in the Russian layout');
eq(keys(pickSearch(camps, 'pbvybq', 10)), ['winter'], 'Russian name typed in the English layout');
eq(keys(pickSearch(camps, 'friday black', 10)), ['blackfriday'], 'words in any order');
eq(keys(pickSearch(camps, 'shop летняя', 10)), ['summer-shop'], 'every word must match');
eq(keys(pickSearch(camps, '12', 10)), ['summer-shop','blackfriday'], 'number: exact first, then prefix');
eq(keys(pickSearch(camps, 'елка', 10)), [], 'nothing found');
eq(keys(pickSearch([{key:'1', label:'Ёлочная акция'}], 'елочная', 10)), ['1'], 'ё folds to е');
eq(keys(pickSearch(camps, '', 2)), ['summer-shop','shop'], 'no query keeps the list order');

const big = Array.from({length:10000}, (_, i) => ({key:'c' + i, label:'Campaign ' + i}));
const prepped = pickPrep(big);
eq(pickPrep(prepped) === prepped, true, 'prepared rows are not folded again');
const r = pickSearch(prepped, 'campaign', 100);
eq([r.items.length, r.total, r.truncated], [100, 10000, true], 'long list is cut with the total');
eq(keys(pickSearch(prepped, 'c9999', 100)), ['c9999'], 'exact id in a long list');
eq(pickSearch(prepped, '', 100).total, 10000, 'empty query counts every row');

eq(pickSegments('Black Friday shop', 'shop black'), [{t:'Black',m:true},{t:' Friday ',m:false},{t:'shop',m:true}], 'both words marked');
eq(pickSegments('Зимний', 'pbv'), [{t:'Зим',m:true},{t:'ний',m:false}], 'mark in the other layout');
eq(pickSegments('abc', ''), [{t:'abc',m:false}], 'no query, no mark');
eq(pickSegments('aaa', 'aa'), [{t:'aa',m:true},{t:'a',m:false}], 'a repeated match is marked once per place');

eq(fltFilterOptions([{key:'x', label:'Better'}, {key:'bet', label:'Beta'}], 'bet', 50).items.map(i => i.key), ['bet','x'], 'filters rank an exact key first');
eq(fltFilterOptions([{key:'a', label:'A'}, {key:'b', label:'B'}], '', 50, ['a']).items, [{key:'b',label:'B',group:''}], 'filters skip excluded keys');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
