package admin

import "testing"

// TestColumnGripLine checks the resize grip shows a line and headers follow their cells.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestColumnGripLine(t *testing.T) {
	html := spaSource()
	must(t, html, ".col-grip::after{", "the grip draws a line on the header edge")
	must(t, html, "th:hover>.col-grip::after{background:var(--border-strong)}", "a hovered header shows a muted grip line")
	must(t, html, "th>.col-grip:hover::after,th>.col-grip.on::after{top:0;bottom:0;background:var(--accent)}", "a hovered or dragged grip uses the accent")

	align := spaFunctionLine(html, "colAlignHeads")
	if align == "" {
		t.Fatal("colAlignHeads missing")
	}
	must(t, align, "classList.toggle('num'", "a header is num only when its cell is num")
	must(t, align, "colSpan>1", "group and empty rows do not decide alignment")
	watch := spaFunctionLine(html, "colWatch")
	must(t, watch, "colAlignHeads", "every drawn table aligns headers to cells")
	must(t, watch, "closest('table')", "a body repaint aligns the headers again")

	must(t, html, `sortableHead('domains', DOM_SORT, [c], domMeta)+(c.id==='host'?'<th class="acts"></th>':'')`, "the domains action header sits after host like its cell")
	mustNot(t, html, `sortableHead('domains', DOM_SORT, cols, domMeta)+`+"`"+`<th class="acts"></th>`+"`", "the domains action header is not shifted to the end")

	bind := spaFunctionLine(html, "colBindResize")
	must(t, bind, "addEventListener('click', swallow, true)", "a click on the grip does not reach a sorting header")
	must(t, bind, "addEventListener('dblclick', swallow, true)", "a double click on the grip does not reach the header")
	must(t, bind, "grip.classList.add('on')", "the grip stays lit while dragged")
	must(t, bind, "drag.grip.classList.remove('on')", "the grip dims when the drag ends")
}
