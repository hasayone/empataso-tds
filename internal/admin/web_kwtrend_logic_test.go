//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestKeywordTrendLogic runs the pure helpers of the keyword trend dialog under node: which metrics a
// series offers, the points and sums of one metric, the sentences that say how the trend is built, the
// filter caption, the chart geometry and the nearest point. It skips when node is absent.
func TestKeywordTrendLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the keyword trend checks")
	}
	m := pureRe.FindStringSubmatch(spaSource())
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	html := spaSource()
	var prelude strings.Builder
	for _, name := range []string{"curKey", "curSign", "moneyUnits", "moneyText", "cpcHasSpend", "moneyCellCls", "asSortMeta", "applySort", "condsQS"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		prelude.WriteString(fn + "\n")
	}
	f := filepath.Join(t.TempDir(), "kwtrend.js")
	if err := os.WriteFile(f, []byte(prelude.String()+m[1]+keywordTrendChecks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("keyword trend checks failed:\n%s", out)
	}
}

const keywordTrendChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};

const full = {hits: [3, 0, 5], conv: [1, 0, 2], profit: [2.5, 0, -1]};
eq(KWS_TREND_METRICS.map(x => x.id), ['hits', 'conv', 'revenue'], 'only the metrics the server sends per day');
eq(KWS_TREND_METRICS.map(x => x.field), ['hits', 'conv', 'profit'], 'each metric reads its own series field');
eq(kwsTrendMetrics(full, true, new Set()), ['hits', 'conv', 'revenue'], 'a counted series offers every metric');
eq(kwsTrendMetrics(full, false, new Set()), ['hits'], 'without conv_ready only clicks are offered');
eq(kwsTrendMetrics({hits: [1], conv: null, profit: null}, true, new Set()), ['hits'], 'arrays the server left out are not offered');
eq(kwsTrendMetrics({hits: [1], conv: [0]}, true, new Set()), ['hits', 'conv'], 'a series without profit has no revenue');
eq(kwsTrendMetrics(full, true, new Set(['revenue'])), ['hits', 'conv'], 'a hidden revenue column removes the metric');
eq(kwsTrendMetrics(full, true, new Set(['conv'])), ['hits', 'revenue'], 'a hidden conv column removes the metric');
eq(kwsTrendMetrics(null, true, new Set()), [], 'no series, no metrics');
eq(kwsTrendMetrics({conv: [1]}, true, new Set()), [], 'a series without clicks is not a trend');

const days = ['2026-11-01', '2026-11-02', '2026-11-03'];
eq(kwsTrendPoints(days, full, 'hits'), [{day: '2026-11-01', v: 3}, {day: '2026-11-02', v: 0}, {day: '2026-11-03', v: 5}], 'one point per day');
eq(kwsTrendPoints(days, full, 'revenue').map(p => p.v), [2.5, 0, -1], 'revenue reads the profit array');
eq(kwsTrendPoints(days, {hits: [7]}, 'hits').map(p => p.v), [7, 0, 0], 'a missing cell is zero');
eq(kwsTrendPoints(days, full, 'roi'), [], 'an unknown metric has no points');
eq(kwsTrendPoints(days, {hits: [1]}, 'conv'), [], 'a missing array has no points');

eq(kwsTrendSummary(kwsTrendPoints(days, full, 'hits')), {total: 8, peak: {day: '2026-11-03', v: 5}, active: 2, days: 3}, 'sum, peak and days with a value');
eq(kwsTrendSummary(kwsTrendPoints(days, {hits: [4, 4, 1]}, 'hits')).peak.day, '2026-11-01', 'the first of equal peaks wins');
eq(kwsTrendSummary(kwsTrendPoints(days, {hits: [0, 0, 0]}, 'hits')).peak, null, 'an all-zero series has no peak');
eq(kwsTrendSummary(kwsTrendPoints(days, full, 'revenue')).total, 1.5, 'a negative payout lowers the sum');
eq(kwsTrendSummary(kwsTrendPoints(days, full, 'revenue')).active, 2, 'a negative day has a value');
eq(kwsTrendSummary([]), {total: 0, peak: null, active: 0, days: 0}, 'no points');

const basis = (metric, ready) => kwsTrendBasis(metric, days, {tz: 'Europe/Kyiv', top: 25, ready});
eq(basis('hits', true).map(b => b.k), ['kws.tr.b.hits', 'kws.tr.b.gran', 'kws.tr.b.period', 'kws.tr.b.scope'], 'clicks: metric, step, period, scope');
eq(basis('conv', true)[0].k, 'kws.tr.b.conv', 'conversions say they are dated by the conversion');
eq(basis('revenue', true)[0].k, 'kws.tr.b.revenue', 'revenue says it is the payout by the conversion date');
eq(basis('hits', false).map(b => b.k).pop(), 'kws.tr.b.noconv', 'without conv_ready the caption says why only clicks are shown');
eq(basis('hits', true)[1].p, {tz: 'Europe/Kyiv'}, 'the step names the interface zone');
eq(basis('hits', true)[2].p, {from: '2026-11-01', to: '2026-11-03', n: 3}, 'the period is the first and last trend day');
eq(basis('hits', true)[3].p, {n: 25}, 'the scope names how many keywords get a trend');
eq(kwsTrendBasis('hits', [], {ready: true})[1].p.tz, 'UTC', 'no zone is UTC, as on the server');
eq(kwsTrendBasis('hits', [], {ready: true})[2].p, {from: '', to: '', n: 0}, 'no days, an empty period');

eq(kwsTrendFilters('', '', []), [{k: 'kws.tr.f.all', p: {}}], 'all campaigns, no search, no conditions');
eq(kwsTrendFilters('promo', 'buy', [{f: 'country'}, {f: 'os'}]), [{k: 'kws.tr.f.camp', p: {id: 'promo'}}, {k: 'kws.tr.f.search', p: {q: 'buy'}}, {k: 'kws.tr.f.conds', p: {n: 2}}], 'campaign, search and the number of conditions');

const steps = [];
const g = kwsChartGeom([0, 10, 5], 600, 280, {int: true, charPx: 7, label: 40, fmt: (t, s) => { steps.push(s); return String(t); }});
eq(g.xs.length === 3 && g.xs[0] === g.L && Math.abs(g.xs[2] - (g.L + g.w)) < 1e-9, true, 'points span the plot from left to right');
eq(g.ys[0] === g.base && g.base === g.T + g.h, true, 'zero sits on the bottom of the plot');
eq(g.ys[1] < g.ys[2] && g.ys[1] >= g.T, true, 'a larger value is higher and stays inside the plot');
eq(g.yTicks[0].v, 0, 'the value axis starts at zero');
eq(g.yTicks.every(t => t.t === String(t.v)), true, 'tick labels come from fmt');
eq(steps.every(s => s === g.step) && steps.length === g.yTicks.length, true, 'fmt receives the axis step');
eq(g.xTicks, [0, 1, 2], 'three days fit three labels');
const one = kwsChartGeom([4], 600, 280, {int: true, charPx: 7, label: 40, fmt: String});
eq(Math.abs(one.xs[0] - (one.L + one.w / 2)) < 1e-9, true, 'a single day sits in the middle');
const none = kwsChartGeom([], 600, 280, {int: true, charPx: 7, label: 40, fmt: String});
eq(JSON.stringify(none).includes('NaN') || JSON.stringify(none).includes('null'), false, 'an empty series has no NaN');
const neg = kwsChartGeom([-2, 1.5], 600, 280, {int: false, charPx: 7, label: 40, fmt: String});
eq(neg.ys[0] > neg.base && neg.ys[1] < neg.base, true, 'a negative value is drawn under zero');
const many = kwsChartGeom(new Array(92).fill(1), 300, 220, {int: true, charPx: 7, label: 60, fmt: String});
eq(many.xTicks.length * 60 <= many.w, true, 'day labels of a long period fit the width');

eq(kwsNearest([10, 20, 30], 24), 1, 'the closest point');
eq(kwsNearest([10, 20, 30], -5), 0, 'left of the plot is the first point');
eq(kwsNearest([10, 20, 30], 99), 2, 'right of the plot is the last point');
eq(kwsNearest([], 5), -1, 'no points');
if (failed) process.exit(1);
`
