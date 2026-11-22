//go:build uitest

package admin

import "testing"

// TestTextMatchIgnoresCase checks the shared catalog search: the query stop matches the names
// stop, STOP and Stop, an uppercase query matches a lowercase name, and a different word does not match.
func TestTextMatchIgnoresCase(t *testing.T) {
	runSPANode(t, `
let failed = 0;
const eq = (got, want, what) => {
  if (got !== want) { failed++; console.log('FAIL ' + what + ': got ' + got + ', want ' + want); }
};
`, []string{"textMatch"}, `
eq(textMatch('stop', ['stop']), true, 'lowercase name');
eq(textMatch('stop', ['STOP']), true, 'uppercase name');
eq(textMatch('stop', ['Stop']), true, 'mixed-case name');
eq(textMatch('STOP', ['stop']), true, 'uppercase query');
eq(textMatch('Stop', ['STOP']), true, 'mixed query');
eq(textMatch('stop', ['other']), false, 'another word');
eq(textMatch('  stop  ', ['Stop']), true, 'spaces around the query');
if (failed) process.exit(1);
`)
}

// TestOfferSearchShowsFuego checks that the query stop includes the offer name as stored and
// lists that exact spelling before names that match only in another case, on the first page of 50.
func TestOfferSearchShowsFuego(t *testing.T) {
	runSPANode(t, `
let failed = 0;
const eq = (got, want, what) => {
  if (got !== want) { failed++; console.log('FAIL ' + what + ': got ' + got + ', want ' + want); }
};
`, []string{"textMatch", "orderOfferSearch"}, `
const fuego = 'stop - A - FuegoCasino - AR - cap 40 - 25$';
eq(textMatch('stop', ['1025', fuego, 'Alex', 'https://click.example/offer']), true, 'the stored name matches stop');
eq(textMatch('stop', ['other']), false, 'another word does not match');
const rows = [];
for (let i = 1; i <= 200; i++) rows.push({id: String(i), name: 'STOP - A - Brand ' + i});
rows.push({id: '3', name: 'stop - A - Alpha'});
rows.push({id: '1025', name: fuego});
rows.push({id: '8', name: 'Stop - A - Zulu'});
rows.push({id: 'nomatch', name: 'plain casino'});
const hit = rows.filter(o => textMatch('stop', [o.id, o.name]));
eq(hit.some(o => o.id === 'nomatch'), false, 'a different name stays out');
eq(hit.some(o => o.id === '1025'), true, '1025 is in the filtered set');
const ordered = orderOfferSearch(hit, 'stop');
const idx = ordered.findIndex(o => o.id === '1025');
eq(idx >= 0 && idx < 50, true, '1025 is on the first page');
eq(ordered[0].id, '3', 'an exact name that sorts earlier stays first');
eq(idx < ordered.findIndex(o => o.id === '8'), true, 'title case follows the exact spelling');
eq(idx < ordered.findIndex(o => String(o.name).startsWith('STOP')), true, 'uppercase STOP follows the exact spelling');
eq(ordered.some(o => String(o.name).startsWith('STOP')), true, 'uppercase names stay in the result');
const kept = orderOfferSearch([{id:'2', name:'B'}, {id:'1', name:'A'}], '');
eq(kept.map(o => o.id).join(','), '2,1', 'an empty search keeps the column order');
if (failed) process.exit(1);
`)
}
