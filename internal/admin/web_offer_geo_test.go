//go:build uitest

package admin

import "testing"

// TestOfferGeoBreakdown checks the country rows of the offers table.
// No countries and one country draw no button. Two countries keep their own
// hits, sales and conversion rate; the rates are not added together.
func TestOfferGeoBreakdown(t *testing.T) {
	runSPANode(t, `
let ME = null;
let OFF_GEO = new Set();
const esc = s => String(s == null ? '' : s);
const tr = k => ({'off.geo':'Страны'})[k] || k;
const icon = n => '[' + n + ']';
function eq(got, want, what) {
  if (JSON.stringify(got) !== JSON.stringify(want)) { FAILED = true; console.log('FAIL ' + what + ': got ' + JSON.stringify(got) + ', want ' + JSON.stringify(want)); }
}
function ok(cond, what) { if (!cond) { FAILED = true; console.log('FAIL ' + what); } }
let FAILED = false;
`, []string{"hiddenFields", "offerGeoCodes", "offerGeoKey", "offerGeoButton", "curKey", "sumClicks", "foldStatus", "normBy", "spendBasis", "packMetrics", "clickCostByRows", "offerCountryMetrics"}, `
eq(offerGeoCodes(null), [], 'no offer, no countries');
eq(offerGeoCodes({}), [], 'a missing list is empty');
eq(offerGeoCodes({countries: []}), [], 'an empty list stays empty');
eq(offerGeoCodes({countries: ['RU']}), ['ru'], 'one country is kept');
let RANGE = {id: 'month', from: '', to: ''};
const geoKey = offerGeoKey({countries: ['RU', 'de']});
eq(geoKey, offerGeoKey({countries: ['ru', 'DE']}), 'the same period and the same countries are one key');
RANGE = {id: 'today', from: '', to: ''};
ok(offerGeoKey({countries: ['ru', 'de']}) !== geoKey, 'another period is another key');
RANGE = {id: 'month', from: '', to: ''};
eq(offerGeoButton({id: 'a'}), '', 'no countries, no button');
eq(offerGeoButton({id: 'a', countries: ['ru']}), '', 'one country, no button');
const btn = offerGeoButton({id: 'a', countries: ['ru', 'de']});
ok(btn.includes('data-ogeo="a"') && btn.includes('aria-expanded="false"') && btn.includes('Страны'), 'two countries: a closed button named through tr');
OFF_GEO.add('a');
ok(offerGeoButton({id: 'a', countries: ['ru', 'de']}).includes('aria-expanded="true"'), 'an open row says so');
ME = {hidden: ['country']};
eq(offerGeoButton({id: 'a', countries: ['ru', 'de']}), '', 'a hidden country draws no button');
ME = null;
const facts = {ready: true, clicks: [
  {country: 'ru', hits: 10, uniq: 4, revenue: 5, cost_cents: 100, cost_cur: 'USD'},
  {country: 'ru', hits: 1, uniq: 1, revenue: 1, cost_cents: 20, cost_cur: 'EUR'},
  {country: 'de', hits: 3, uniq: 2, revenue: 9, cost_cents: 50, cost_cur: 'EUR'}
], status: [
  {country: 'ru', status: 'sale', n: 2, profit: 5},
  {country: 'de', status: 'lead', n: 1, profit: 9}
]};
const ru = offerCountryMetrics(facts, 'ru');
const de = offerCountryMetrics(facts, 'de');
eq(ru.hits, 11, 'ru hits stay on ru, including both currencies');
eq(de.hits, 3, 'de hits stay on de');
ok(ru.hits > 0 && de.hits > 0, 'two countries with clicks are not zero');
const folded = offerCountryMetrics({ready: true, clicks: [{country: 'RU', hits: 7, uniq: 7}, {country: 'DE', hits: 2, uniq: 2}], status: []}, 'ru');
const foldedDe = offerCountryMetrics({ready: true, clicks: [{country: 'RU', hits: 7, uniq: 7}, {country: 'DE', hits: 2, uniq: 2}], status: []}, 'de');
eq(folded.hits, 7, 'an upper-case stored code is the same country as the engine lower-case code');
eq(foldedDe.hits, 2, 'the other upper-case country stays on its own row');
eq(ru.sales, 2, 'ru sales');
eq(ru.leads, 0, 'ru has no de lead');
eq(de.leads, 1, 'de lead');
eq(de.sales, 0, 'de has no ru sale');
eq(ru.rev, 11, 'ru revenue is its click revenue plus its sale');
eq(de.rev, 18, 'de revenue is its own');
eq(ru.costBy.USD, 100, 'ru keeps its dollar click price');
eq(ru.costBy.EUR, 20, 'ru keeps its euro click price');
eq(de.costBy.EUR, 50, 'de does not take ru euro');
const cr = m => (m.leads + m.sales) / m.hits;
eq(Math.round(cr(ru) * 1000) / 1000, Math.round((2 / 11) * 1000) / 1000, 'ru rate uses ru only');
eq(Math.round(cr(de) * 1000) / 1000, Math.round((1 / 3) * 1000) / 1000, 'de rate uses de only');
ok(Math.abs(cr(ru) - (3 / 14)) > 0.01, 'the rates are not the blended rate');
if (FAILED) process.exit(1);
`)
}
