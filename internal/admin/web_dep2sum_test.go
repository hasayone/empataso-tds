//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDep2SumColumnSums runs the DepSum column under node: lead and sale rows sum depsum, a rejected row
// and a dep2sum value add nothing, the cell shows the deposit in dollars, and revenue stays the payout.
// It skips when node is absent.
func TestDep2SumColumnSums(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the DepSum checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(`const moneyOf=(n,by,ready,d)=>(ready?n:0).toFixed(d==null?2:d)+' $';
const csvMoney=n=>(+n).toFixed(2);
const moneyCellCls=(k,o)=>o.ready&&+o.amount>0?'c-rev':'';
const spendBasis=()=>({amount:0,mixed:false});
const costCode=()=>({multi:false,amount:0});
`)
	src.WriteString(spaConstLine(html, "const EXTRA_METRICS=") + "\n")
	for _, name := range []string{"foldStatus", "packMetrics", "offerCells", "metricSortValue", "catalogCsvMetric"} {
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
const rows = [
  {status: 'sale', n: 1, profit: 10, depsum: 5, dep2sum: 30},
  {status: 'sale', n: 1, profit: 20, depsum: 1, dep2sum: 12.5},
  {status: 'lead', n: 2, profit: 0, depsum: 0},
  {status: 'rejected', n: 3, profit: 0, depsum: 7, dep2sum: 99},
];
const folded = foldStatus(rows);
eq(folded.depsum, 6, 'the deposit of leads and sales is summed');
eq(folded.dep2sum, undefined, 'dep2sum is not a second deposit');
eq(folded.rev, 30, 'the deposit is not part of the revenue');
const m = packMetrics({hits: 100, uniq: 50}, rows, {});
eq(m.depsum, 6, 'the packed metric carries the deposit');
eq(m.dep2sum, undefined, 'the packed metric has no second deposit');
eq(m.rev, 30, 'revenue stays the payout');
eq(offerCells(m, 'depsum'), '<td class="num c-rev">6.00 $</td>', 'the cell shows the deposit in dollars');
eq(offerCells(m, 'revenue'), '<td class="num c-rev">30.00 $</td>', 'revenue stays its own cell');
eq(metricSortValue(m, 'depsum'), 6, 'the column sorts by the deposit');
eq(catalogCsvMetric(m, 'depsum', null), '6.00', 'the export is the deposit');
eq(offerCells(m, 'dep2sum'), '<td></td>', 'there is no second-deposit cell');
if (failed) process.exit(1);
`)
	file := filepath.Join(t.TempDir(), "dep2sum.js")
	if err := os.WriteFile(file, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

// TestDep2SumColumnWiring checks that the offers picker lists DepSum (depsum), revenue stays the payout
// column, and the network postback hint, the docs and the translations do not name dep2sum.
func TestDep2SumColumnWiring(t *testing.T) {
	html := spaSource()
	must(t, html, "const DEP_COL=['depsum','col.depsum',0];", "the deposit column is DepSum and off until chosen")
	must(t, html, "const NET_PB_VARS=['subid','status','payout','depsum','tid','key'];", "the network hint lists only parameters /postback reads")
	must(t, html, "&depsum={deposit}&tid={txid}", "the example address names depsum")
	mustNot(t, html, "Dep2Sum", "the Dep2Sum name is gone")
	mustNot(t, html, "if(id==='dep2sum')", "no column draws a second deposit")
	if n := strings.Count(html, "dep2sum"); n != 2 {
		t.Errorf("dep2sum remains in %d places, want only the two journal exclusions", n)
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
			t.Errorf("%s: DepSum is not in the picker", kind)
		}
		if !strings.Contains(block, "['revenue','col.revenue',1]") {
			t.Errorf("%s: revenue column is missing", kind)
		}
	}
	must(t, spaFunctionLine(html, "offerCells"), "if(id==='depsum')", "offers draw the deposit column")
	mustNot(t, spaFunctionLine(html, "packMetrics"), "rev:m.rev+m.depsum", "the deposit is not added into revenue")
}
