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

// extraColIDs are the derived columns of the campaign, offer and network tables.
var extraColIDs = []string{"crlead", "ucr", "uniqpct", "approve", "ec", "ecpm"}

// TestExtraMetricFormulas runs the derived columns under node. Each value comes from the numbers of its own row
// with its own denominator, a zero denominator or missing facts give no value (never a zero), and the text of an
// empty cell is a dash. It skips when node is absent.
func TestExtraMetricFormulas(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the derived column checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(`const moneyOf=(n,by,ready,d)=>(ready?n:0).toFixed(d==null?2:d)+' $';
const csvMoney=n=>(+n).toFixed(2);
const moneyCellCls=(k,o)=>o.ready&&+o.amount>0?'c-rev':'';
`)
	src.WriteString(spaConstLine(html, "const EXTRA_METRICS=") + "\n")
	for _, name := range []string{"extraMetric", "extraText", "extraCSV", "extraTone"} {
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
const m = {hits: 200, uniq: 50, leads: 6, sales: 3, rej: 1, rev: 90};
eq(extraMetric(m, 'crlead'), 3, 'CR leads is leads per click');
eq(extraMetric(m, 'ucr'), 18, 'CR of uniques is leads and sales per unique click');
eq(extraMetric(m, 'uniqpct'), 25, 'unique share is unique clicks per click');
eq(extraMetric(m, 'approve'), 75, 'approve is sales per sale and reject');
eq(extraMetric(m, 'ec'), 10, 'revenue per conversion is revenue per lead and sale');
eq(extraMetric(m, 'ecpm'), 450, 'revenue per 1000 clicks');
eq(extraText(m, 'crlead'), '3.00%', 'a share shows two digits and a percent sign');
eq(extraText(m, 'ec'), '10.00 $', 'money shows in dollars');
eq(extraCSV(m, 'approve'), '75.00%', 'a share exports with its sign');
eq(extraCSV(m, 'ecpm'), '450.00', 'money exports as a plain number');
eq(extraTone(m, 'ec'), 'c-rev', 'revenue above zero is tinted');
eq(extraTone(m, 'ucr'), '', 'a share is not tinted');
for (const k of Object.keys(EXTRA_METRICS)) {
  eq(extraMetric(null, k), null, k + ' has no value while facts are not ready');
  eq(extraText(null, k), '—', k + ' shows a dash without facts');
  eq(extraCSV(null, k), '', k + ' exports an empty cell without facts');
  eq(extraMetric({hits: 0, uniq: 0, leads: 0, sales: 0, rej: 0, rev: 0}, k), null, k + ' has no value on an empty row');
}
eq(extraMetric({hits: 10, uniq: 4, leads: 0, sales: 0, rej: 0, rev: 0}, 'crlead'), 0, 'a real zero stays a zero');
eq(extraText({hits: 10, uniq: 4, leads: 0, sales: 0, rej: 0, rev: 0}, 'crlead'), '0.00%', 'a real zero is shown as zero');
eq(extraMetric({hits: 10, uniq: 4, leads: 2, sales: 0, rej: 2, rev: 5}, 'approve'), 0, 'rejects without sales give zero approve');
eq(extraMetric({hits: 10, uniq: 4, leads: 2, sales: 0, rej: 0, rev: 5}, 'approve'), null, 'no sale and no reject gives no approve');
eq(extraMetric({hits: 0, uniq: 0, leads: 2, sales: 1, rej: 0, rev: 6}, 'ec'), 2, 'revenue per conversion needs no clicks');
if (failed) process.exit(1);
`)
	dir := t.TempDir()
	file := filepath.Join(dir, "extra.js")
	if err := os.WriteFile(file, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

// TestExtraColumnsWiring checks that every derived column is in the picker of campaigns, offers and networks below
// the old columns, off until chosen, is drawn, sorted and exported by one formula, is hidden together with the
// data it rests on, has ru, en and uk captions, and that no column without data of its own is among them.
func TestExtraColumnsWiring(t *testing.T) {
	html := spaSource()
	re := regexp.MustCompile(`const EXTRA_METRICS=\{([^}]*)\}`)
	m := re.FindStringSubmatch(html)
	if m == nil {
		t.Fatal("EXTRA_METRICS not found")
	}
	for _, id := range extraColIDs {
		if !strings.Contains(m[1], id+":'") {
			t.Errorf("EXTRA_METRICS has no %s", id)
		}
		if n := strings.Count(html, "'col."+id+"':"); n != 3 {
			t.Errorf("col.%s is translated %d times, want 3", id, n)
		}
	}
	if got := strings.Count(m[1], ":'"); got != len(extraColIDs) {
		t.Errorf("EXTRA_METRICS has %d columns, want %d", got, len(extraColIDs))
	}
	for _, banned := range []string{"cost", "roi", "uepc", "deposit", "reg", "lp", "time", "cpc"} {
		if strings.Contains(m[1], banned) {
			t.Errorf("EXTRA_METRICS mentions %q: a column without data of its own", banned)
		}
	}
	catalog := spaFunctionLine(html, "colCatalog")
	for _, kind := range []string{"flows", "offers", "networks"} {
		start := strings.Index(catalog, "if(kind==='"+kind+"') return [")
		if start < 0 {
			t.Fatalf("catalog of %s not found", kind)
		}
		block := catalog[start:]
		block = block[:strings.Index(block, "];")]
		if !strings.Contains(block, "...EXTRA_COLS,DEP_COL\n") {
			t.Errorf("%s: the derived columns are not appended", kind)
		}
		if strings.Index(block, "...EXTRA_COLS") < strings.Index(block, "['leads','col.leads'") {
			t.Errorf("%s: the derived columns must come below the leads column", kind)
		}
	}
	must(t, html, "const EXTRA_COLS=Object.keys(EXTRA_METRICS).map(id=>[id,'col.'+id,0]);", "all derived columns are off until chosen")
	must(t, spaFunctionLine(html, "flowMetricCell"), "if(EXTRA_METRICS[id]){", "the campaign table draws them")
	must(t, spaFunctionLine(html, "flowMetricCell"), "machine?extraCSV(own, id):extraText(own, id)", "the export of the campaign table is plain")
	must(t, spaFunctionLine(html, "offerCells"), "if(EXTRA_METRICS[id]) return td(extraText(own,id), extraTone(own,id));", "offers and networks draw them from the real metrics, not the zero stand-in")
	must(t, spaFunctionLine(html, "metricSortValue"), "if(EXTRA_METRICS[k]){const v=extraMetric(m, k); return v==null?-1:v}", "an empty value sorts below every real one")
	must(t, spaFunctionLine(html, "catalogCsvMetric"), "if(EXTRA_METRICS[id]) return extraCSV(m, id);", "offers and networks export them")
	must(t, html, "...Object.keys(EXTRA_METRICS)]);", "they take part in the banded colouring of the campaign table")
	must(t, html, "const FLOW_USD_COLS=new Set(['revenue','depsum','uepc','epc','ec','ecpm']);", "money columns say the dollar in the export title")
	hidden := spaFunctionLine(html, "hiddenCols")
	must(t, hidden, "add(['revenue','profit','roi','epc','uepc','epv','payout','depsum','ec','ecpm'])", "hidden revenue hides revenue per conversion and per 1000 clicks")
	must(t, hidden, "'crlead','ucr','approve','ec'])", "hidden status hides what needs the lead, sale and reject split")
	must(t, hidden, "'visit','ucr','uniqpct'])", "hidden unique clicks hides what divides by them")
	if n := strings.Count(spaFunctionLine(html, "extraMetric"), "uepc"); n != 0 {
		t.Error("extraMetric must not compute uEPC")
	}
}
