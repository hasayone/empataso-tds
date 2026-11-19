package admin

import (
	"strings"
	"testing"
)

// TestSPANetworkDialog checks the structure of the affiliate network dialog in the SPA:
// the template picker is a searchable select that starts empty, the offer parameters field
// sits above the postback textarea and starts empty for a new network, the postback field
// is a two-row textarea, the catalog comes from the API and not from the page, and the
// popup of the offer parameters names ваш_sub1={subid}.
func TestSPANetworkDialog(t *testing.T) {
	html := spaSource()
	mustNot(t, html, "NET_TEMPLATES", "no template list inside the page")
	must(t, html, "get('/api/affiliate-network-templates',[])", "catalog read from the API")
	must(t, html, "const tplOptions=()=>`<option value=\"\">${esc(tr('net.tpl.ph'))}</option>", "picker starts on the empty option")
	form := spaBetween(html, "const netForm=(cur, preGroup)=>", "const applyTpl=")
	if form == "" {
		t.Fatal("netForm missing")
	}
	must(t, form, `<select id="ntpl" data-pick>${tplOptions()}</select>`, "picker is searchable")
	must(t, form, `<textarea id="nurl" class="mono net-pb" rows="2"`, "postback is a two-row textarea")
	must(t, form, `value="${esc(cur?cur.offer_query||'':'')}"`, "offer parameters hold only the saved value")
	mustNot(t, form, `value="sub1={subid}"`, "no sub1 default in the value")
	if p, u := strings.Index(form, `id="nparam"`), strings.Index(form, `id="nurl"`); p < 0 || u < 0 || p > u {
		t.Errorf("offer parameters must sit above the postback field")
	}
	must(t, form, "tr('net.label')", "name caption is Название")
	must(t, html, "'net.param.help.a':'Параметры, которые нужно добавлять ко всем офферам.'", "popup lead text")
	must(t, html, "'net.param.help.ex1':'ваш_sub1={subid}'", "popup names the required parameter")
	must(t, html, "'net.param.help.ex2':'ваш_sub2={campaign_name}&...'", "popup names the optional parameter")
	must(t, html, "'net.param.help.ex2':'your_sub2={campaign_name}&...'", "en keeps the same macros")
	mustNot(t, html, "net.param.help.macros", "no extra macro line in the popup")
	apply := spaBetween(html, "const applyTpl=", "const closeTplEditor=")
	must(t, apply, "el.value===filled[id]", "a typed value is not overwritten by another template")
	must(t, apply, "net.tpl.state.none", "a template without fields says so")
	must(t, apply, "netInstructionHTML(t.instruction)", "the instruction is shown read-only through the safe renderer")
	mustNot(t, html, "t.notes", "the old notes array is gone")
}

// TestSPATemplateEditor checks the short template editor of the network dialog: it sits under
// the picker without a new menu section, is shown only to a person who may create networks, calls
// the three write routes, keeps the name of a catalog template out of the form, and renders the
// instruction through a whitelist that escapes everything else.
func TestSPATemplateEditor(t *testing.T) {
	html := spaSource()
	form := spaBetween(html, "const netForm=(cur, preGroup)=>", "const applyTpl=")
	must(t, form, "tplWrite?", "the buttons are shown only with the right to create")
	must(t, form, `id="ntpl-ed" hidden`, "the editor sits under the picker")
	editor := spaBetween(html, "const openTplEditor=", "const openNet=")
	if editor == "" {
		t.Fatal("openTplEditor missing")
	}
	must(t, editor, "call('PUT','/api/affiliate-network-templates/'+encodeURIComponent(t.id)", "edit saves with PUT")
	must(t, editor, "call('POST','/api/affiliate-network-templates'", "a new template is created with POST")
	must(t, editor, "call('DELETE','/api/affiliate-network-templates/'+encodeURIComponent(t.id)", "reset uses DELETE")
	must(t, editor, "t?'':`<div class=\"fld\"><label for=\"ted-name\">", "the name is asked only for a new template")
	render := spaBetween(html, "function netInstructionHTML(raw){", "function netConversionsHTML(")
	must(t, render, "esc(s)", "text is escaped")
	must(t, render, "(https?:\\/\\/[^\"\\s<>]*)", "only http and https addresses become links")
	must(t, render, `rel="noopener noreferrer"`, "links do not leak the opener")
	mustNot(t, render, "innerHTML", "the renderer builds a string and never touches the DOM")
	for _, key := range []string{"net.tpl.conv", "net.tpl.edit", "net.tpl.add", "net.tpl.edited", "net.tpl.ed.ins", "net.tpl.ed.hint", "net.tpl.ed.reset", "net.tpl.ed.del"} {
		if n := strings.Count(html, ",'"+key+"':'"); n != 3 {
			t.Errorf("%s is defined %d times, want one per language", key, n)
		}
	}
}

// TestSPATemplateRecognition checks what happens when a saved network is opened: a template with
// the same name is selected without writing any field, a missing one is added only from the network
// that was opened (no loop over the other networks), and the body keeps no foreign address.
func TestSPATemplateRecognition(t *testing.T) {
	html := spaSource()
	apply := spaBetween(html, "const applyTpl=", "const closeTplEditor=")
	must(t, apply, "if(!keep) Object.keys(next).forEach", "keep mode writes no field")
	open := spaBetween(html, "const openNet=(id)=>{", "const findTplByName=")
	must(t, open, "if(cur) recogniseTpl(m, filled, cur);", "only an opened saved network is recognised")
	rec := spaBetween(html, "const recogniseTpl=async(", "const networkRows=")
	if rec == "" {
		t.Fatal("recogniseTpl missing")
	}
	must(t, rec, "applyTpl(box, filled, true)", "the template is shown without overwriting the fields")
	must(t, rec, "findTplByName(n.name)", "recognition is by the name of the opened network")
	must(t, rec, "netTplFromNetwork(n, postbackOrigin())", "a missing template is built from the opened network")
	must(t, rec, "tplWrite?", "only a person who may change networks adds a template")
	mustNot(t, rec, "rows", "no other network is read")
	mustNot(t, rec, "forEach", "no loop over networks")
	find := spaBetween(html, "const findTplByName=", "const recogniseTpl=")
	must(t, find, "toLowerCase()", "the name match ignores case")
	fn := spaBetween(html, "function netTplFromNetwork(n, origin){", "function netInstructionHTML(")
	must(t, fn, "instruction:''", "no instruction is made up")
	must(t, fn, "/^from=/i", "the from marker is dropped")
	must(t, fn, "{origin}/postback", "the postback is written as the tracker's own address")
	mustNot(t, html, "Generic S2S template", "no leftover of the presets in the dialog")
}

// TestSPAInstructionStyles checks that the instruction box wraps long text inside the dialog, spaces
// paragraphs, sets code apart in the monospace font, underlines links and takes every colour from the
// theme variables of the admin, so the light and the dark theme both read.
func TestSPAInstructionStyles(t *testing.T) {
	html := spaSource()
	for _, rule := range []string{
		".net-notes p{margin:0 0 10px}",
		".net-notes code{display:inline;padding:1px 6px;border:1px solid var(--border)",
		"font-family:var(--mono)",
		"white-space:pre-wrap;overflow-wrap:anywhere",
		".net-notes a{color:var(--accent);text-decoration:underline",
		"max-width:100%;overflow-wrap:anywhere",
		"border-left:3px solid var(--accent)",
		"background:var(--surface);color:var(--accent);font-family:var(--mono)",
	} {
		must(t, html, rule, "instruction box style")
	}
	box := spaBetween(html, "  .net-notes{margin:0 0 14px", "  .net-tpl-row{")
	if strings.Contains(box, "#") {
		t.Error("the instruction box holds a fixed colour; it must use the theme variables")
	}
}