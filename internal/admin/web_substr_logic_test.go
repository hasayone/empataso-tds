//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSubstringOpsLogic runs the operator helpers of the filter builder under
// node: substring operators only for text fields, one value per row, and the
// value goes into f= without the in/nin escaping. It skips when node is absent.
func TestSubstringOpsLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	html := spaSource()
	m := pureRe.FindStringSubmatch(html)
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	qs := spaFunctionLine(html, "condsQS")
	if qs == "" {
		t.Fatal("condsQS not found in SPA")
	}
	checks := `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
eq(fltOpsFor({name:'sub_id_1', text:true}).map(o=>o[0]), ['eq','ne','in','nin','contains','not_contains'], 'text field offers substring ops');
eq(fltOpsFor({name:'country', text:false}).map(o=>o[0]), ['eq','ne','in','nin'], 'non-text field hides substring ops');
eq(fltOpsFor(null).map(o=>o[0]), ['eq','ne','in','nin'], 'unknown field hides substring ops');
eq(fltOpFor('contains', {text:false}), 'eq', 'contains falls back on a non-text field');
eq(fltOpFor('not_contains', {text:true}), 'not_contains', 'not_contains stays on a text field');
eq(fltOpFor('nin', {text:false}), 'nin', 'list ops stay');
eq(fltOpMulti('contains'), false, 'contains keeps one value');
eq([fltValueHint('contains'), fltValueHint('not_contains'), fltValueHint('eq')], ['flt.part','flt.part','flt.value'], 'substring rows ask for a part of the value');
{const row={op:'contains',v:[]}; fltCommit(row,' ab,c '); eq(row.v, ['ab,c'], 'contains keeps the whole typed text');}
eq(condsQS([{f:'sub_id_1',op:'contains',v:['a,b\\c']}]).getAll('f'), ['sub_id_1:contains:a,b\\c'], 'contains value is sent unescaped');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
	f := filepath.Join(t.TempDir(), "substr.js")
	if err := os.WriteFile(f, []byte(qs+"\n"+m[1]+checks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("substring operator checks failed:\n%s", out)
	}
}
