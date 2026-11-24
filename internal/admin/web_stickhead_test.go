package admin

import "testing"

// TestListTablesScrollWithThePage checks that the long lists scroll with the document.
// The column titles stick under the app bar. The list block is not its own vertical scroller.
// t is the test handle. It returns nothing and fails when a listed table scrolls inside itself or loses the sticky title.
func TestListTablesScrollWithThePage(t *testing.T) {
	html := spaSource()
	wrap := cssRuleBodies(t, html, `^\.tw\.stickhead$`)
	if len(wrap) != 1 {
		t.Fatalf("stickhead wrapper: found %d", len(wrap))
	}
	must(t, wrap[0], "overflow:visible", "the list wrapper is not a scrollport")
	head := cssRuleBodies(t, html, `^\.tw\.stickhead thead$`)
	if len(head) != 1 {
		t.Fatalf("sticky head rule: found %d", len(head))
	}
	for _, needle := range []string{"position:sticky", "top:calc(var(--top) + env(safe-area-inset-top, 0px))", "z-index:6", "background:var(--surface-2)"} {
		must(t, head[0], needle, "sticky head sits under the app bar")
	}
	th := cssRuleBodies(t, html, `^\.tw\.stickhead thead th$`)
	if len(th) != 1 {
		t.Fatalf("sticky cell rule: found %d", len(th))
	}
	must(t, th[0], "background:var(--surface-2)", "the header fill is the opaque theme surface")
	for _, body := range cssRuleBodies(t, html, `^main$`) {
		mustNot(t, body, "overflow", "the page block does not scroll inside itself")
	}
	mustNot(t, html, "body.stickpage{height:100vh;overflow:hidden}", "the document is not locked to the window")
	mustNot(t, html, "main{flex:1 1 auto;min-height:0;overflow:auto", "the view is not an inner scroller")
	for _, needle := range []string{
		`<div class="tw stickhead"><table class="ftable"><thead><tr><th class="pick">`,
		`catalogBulkBar('offers', list.length)}<div class="tw stickhead">`,
		`catalogBulkBar('networks', sorted.length)}<div class="tw stickhead">`,
		`<div class="tw stickhead" id="net-offers">`,
		`<div class="tw stickhead"><table class="ftable"><thead><tr>${head}`,
		`<div class="tw stickhead"><table><thead><tr>${sortableHead('logs'`,
		`<div class="tw stickhead"><table><thead><tr>${sortableHead('pblog'`,
		`<div class="tw stickhead"><table><thead><tr><th>${tr('log.system')}`,
		`<div class="tw stickhead"><table><thead><tr>${sortableHead('conv'`,
		`<div class="tw stickhead" id="rep-tw"><table>`,
		`<div class="tw stickhead" id="kws-tw">`,
	} {
		must(t, html, needle, "the table header is marked sticky")
	}
}
