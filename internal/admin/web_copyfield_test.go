package admin

import "testing"

// TestSPACampaignLinkCopyField checks that the campaign link in the drawer is a
// readonly input with an attached copy button built by the shared copyField
// helper, which reuses copyText (clipboard plus execCommand fallback), keeps the
// button disabled for an empty value and does not turn the link into an anchor.
func TestSPACampaignLinkCopyField(t *testing.T) {
	html := spaSource()
	must(t, html, `id="g_srclink" class="mono"',campaignURL(g)`, "drawer link rendered through copyField")
	must(t, html, "setCopyField(box, sourceCampaignLink(g, src))", "link repaint keeps the button state")
	must(t, html, "bindCopyFields(b);", "drawer binds copy buttons")

	field := spaFunctionLine(html, "copyField")
	must(t, field, `class="inpcopy"`, "input group wrapper")
	must(t, field, "readonly", "input is read-only")
	must(t, field, "data-copyfield", "copy button marker")
	must(t, field, "aria-label", "button has an accessible name")
	must(t, field, "'pb.copy.url'", "button label is the shared Copy translation")
	must(t, field, "disabled", "button disabled for an empty value")
	mustNot(t, field, "<a ", "the link field is not an anchor")

	set := spaFunctionLine(html, "setCopyField")
	must(t, set, "b.disabled=!value", "button follows the value")

	bind := spaFunctionLine(html, "bindCopyFields")
	must(t, bind, "copyText(inp.value,b)", "button reuses copyText")

	cp := spaFunctionLine(html, "copyText")
	must(t, cp, "execCommand('copy')", "clipboard fallback without navigator.clipboard")
	must(t, cp, "tr('cf.ns.copied')", "feedback says Copied")
	must(t, html, ".inpcopy button.icon{", "button styled as part of the input group")
}
