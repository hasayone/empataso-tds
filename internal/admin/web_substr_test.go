package admin

import (
	"strings"
	"testing"
)

// TestSpaSubstringOps checks that the filter builder offers contains and
// not_contains from FLT_OPS only for text fields and that the labels exist in
// the three languages.
func TestSpaSubstringOps(t *testing.T) {
	html := spaSource()
	must(t, html, "['contains','flt.contains',true],['not_contains','flt.not_contains',true]", "FLT_OPS lists the substring operators")
	must(t, spaFunctionLine(html, "fltOpsFor"), "meta&&meta.text", "substring operators depend on the text flag of the field")
	must(t, html, "fltOpsFor(meta).map(", "the operator select is built per field")
	must(t, html, "opSelect(row.op, meta)", "each row passes its field to the operator select")
	must(t, html, "draft[i].op=fltOpFor(draft[i].op, fieldMeta(sel.value))", "a new field drops an operator it does not offer")
	must(t, spaFunctionLine(html, "fltCommit"), "!fltOpMulti(", "single-value operators keep one value")
	for _, k := range []string{"flt.contains", "flt.not_contains", "flt.part"} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	must(t, html, "'flt.contains':'Содержит','flt.not_contains':'Не содержит'", "Russian labels")
}
