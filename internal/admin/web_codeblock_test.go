package admin

import (
	"strings"
	"testing"
)

// TestSPACodeLook checks that code has one look across the admin: the inline chip and the block share the
// same fill from a theme token, a block has no border, and no part of the interface restyles code with a
// border or a fixed colour of its own. The accent of the network template notes stays.
func TestSPACodeLook(t *testing.T) {
	html := spaSource()
	chip := spaBetween(html, "  code{padding:", "}")
	must(t, chip, "background:var(--surface-3)", "the chip fill is a theme token")
	mustNot(t, chip, "border:", "the chip has no border")

	start := strings.Index(html, "  .lnk-url,pre.mono,")
	if start < 0 {
		t.Fatal("shared code block rule not found")
	}
	block := html[start : start+strings.Index(html[start:], "}")]
	for _, sel := range []string{".lnk-url", "pre.mono", ".upd-page pre.notes", ".upd-page .cli-row code", ".key-once"} {
		must(t, block, sel, "the block rule covers "+sel)
	}
	must(t, block, "background:var(--surface-3)", "the block fill is the chip fill")
	must(t, block, "font-family:var(--mono)", "the block is monospace")
	must(t, block, "overflow-wrap:anywhere", "a long address wraps inside the card")
	mustNot(t, block, "border:", "the block has no border")
	mustNot(t, block[strings.Index(block, "{"):], "#", "the block holds no fixed colour")

	for _, old := range []string{"code.lnk-url{", ".lnk-map code{", ".s2s-hint code{", ".callout code{", ".apidoc-pre pre.mono code{"} {
		mustNot(t, html, old, "no second style for code: "+old)
	}
	must(t, html, ".lnk-url{user-select:all}", "an address selects whole with one click")
	must(t, html, ".net-notes code{display:inline;color:var(--accent);", "the accent of the template notes stays")
	mustNot(t, spaBetween(html, ".net-notes code{", "}"), "border:", "the template notes use the shared fill")
}
