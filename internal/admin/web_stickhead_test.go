package admin

import "testing"

// TestListTablesScrollInsideTheirBlock checks that a wide list table scrolls sideways inside its own block.
// The .tw wrapper keeps the width of main and owns the horizontal bar, so the page never widens.
// Column titles are not sticky: no .tw.stickhead rule, no locked document and no inner page scroller.
// t is the test handle. It returns nothing and fails when the wrapper loses its bar, grows the page or a sticky head comes back.
func TestListTablesScrollInsideTheirBlock(t *testing.T) {
	html := spaSource()
	wrap := cssRuleBodies(t, html, `^\.tw$`)
	if len(wrap) != 1 {
		t.Fatalf("table wrapper: found %d", len(wrap))
	}
	must(t, wrap[0], "box-sizing:border-box", "the border stays inside the block width")
	must(t, wrap[0], "max-width:100%", "the table block does not grow past main")
	must(t, wrap[0], "min-width:0", "a wide table cannot force its block wider than main")
	must(t, wrap[0], "overflow-x:auto", "a table wider than its block gets a horizontal bar inside the block")
	mustNot(t, wrap[0], "overflow-x:clip", "extra columns are scrolled to, not cut")
	mustNot(t, wrap[0], "overflow-x:hidden", "extra columns are scrolled to, not cut")
	mustNot(t, wrap[0], "overflow:hidden", "extra columns are scrolled to, not cut")
	mustNot(t, wrap[0], "overflow-y:auto", "the table block is not a vertical scroller")
	mustNot(t, wrap[0], "max-height", "the table block is not a vertical scroller")
	for _, sel := range []string{`^\.tw\.stickhead$`, `^\.tw\.stickhead thead$`, `^\.tw\.stickhead thead th$`} {
		if n := len(cssRuleBodies(t, html, sel)); n != 0 {
			t.Errorf("%s: found %d rules, want none", sel, n)
		}
	}
	mustNot(t, html, "overflow-x:clip", "no block clips a wide table")
	mustNot(t, html, "body.stickpage", "the document is not locked to the window")
	for _, body := range cssRuleBodies(t, html, `^main$`) {
		mustNot(t, body, "overflow", "the page block does not scroll inside itself")
	}
	for _, body := range cssRuleBodies(t, html, `^body$`) {
		mustNot(t, body, "overflow", "the window keeps its own vertical scroll")
	}
}
