package admin

import (
	"strconv"
	"strings"
	"testing"
)

// TestRouteDocSPAPage checks that #/settings/docs is built from /api/openapi.json through the pure
// helpers, with search, a method filter, a section sidebar, anchors, copy buttons, the OpenAPI
// download and the page origin as the base address, and that every dictionary key it uses exists
// once per language, including the label of each error status the registry may list.
// t is the test handle. It returns nothing and fails on a missing marker or key.
func TestRouteDocSPAPage(t *testing.T) {
	html := spaSource()
	page := spaFunctionLine(html, "apiDocs")
	for _, want := range []string{
		"const openapi=pub?'/api/docs/openapi.json':'/api/openapi.json';", "get(openapi, null)", "get('/api/docs/settings', null)",
		"apiDocsModel(doc, lang)", "apiDocsHits(model, API_DOCS_FILTER.q, API_DOCS_FILTER.method)",
		"apiDocsCurl(o, origin)", "const origin=location.origin;", `id="docs-q"`, `id="docs-m"`, `id="docs-toc"`, `id="docs-hits"`,
		"PICK_DEBOUNCE_MS", "e.key==='ArrowDown'", "apiDocsMark(h.path, API_DOCS_FILTER.q)", "goHit(",
		"data-docjump", "data-doccopy", "copyText(copies[+b.dataset.doccopy], b)",
		`href="'+openapi+'" download="emtds-openapi.json"`, `href="'+postman+'" download="emtds.postman_collection.json"`,
		"const postman=pub?'/api/docs/postman.json':'/api/postman.json';",
		`<a href="/api/openapi.json" target="_blank" rel="noopener">`, `curl -H "Api-Key: em_..."`,
		"tr('key.docs.err.'+c)", "tr('key.act.'+o.action)", "deniedCard(model.denied)", "const base=pub?'#':'#/settings/docs/';",
		"history.replaceState(null, '', base+a.dataset.docjump)", "const publicURL=origin+'/docs';", "copyText(publicURL, e.currentTarget)",
		"call('PUT','/api/docs/settings',{public:pubBox.checked})", `id="docs-md" href="'+base+'md"`,
		"if(anchor==='md'){ await apiDocsMarkdownView(pub); return; }",
		"apiDocsHighlight(text, lang)", "API_DOC_METHOD_TONES[o.method]", "o.destructive?badge('bad'",
	} {
		must(t, page, want, "apiDocs: "+want)
	}
	if strings.Contains(page, "127.0.0.1") {
		t.Error("the docs page must use location.origin, not a fixed address")
	}
	if strings.Contains(page, "apidoc-side") {
		t.Error("the docs page lists sections, not every route")
	}
	must(t, page, `<div class="apidoc-layout"><nav class="apidoc-toc" id="docs-toc"`, "sections sit in a sidebar beside the text")
	must(t, html, ".apidoc-layout{display:grid;grid-template-columns:248px minmax(0,1fr)", "docs sidebar column")
	must(t, html, ".apidoc-toc{display:flex;flex-direction:column", "docs contents are a vertical list")
	must(t, html, ".apidoc-layout{grid-template-columns:minmax(0,1fr)", "a narrow screen keeps the contents on one scrolling row")
	must(t, spaFunctionLine(html, "profile"), "arg.startsWith('docs/')?arg.slice(5):''", "docs deep link passes the anchor")
	for _, css := range []string{".apidoc-mt.ok{color:var(--ok)}", ".apidoc-mt.bad{color:var(--bad)}", "code.hl .hl-key{", "code.hl .hl-flag{", ".apidoc-share{", ".apidoc-op>.bt,.apidoc-share>.bt{font-weight:700"} {
		must(t, html, css, "docs style "+css)
	}
	must(t, page, "API_DOCS_FILTER.q=''; API_DOCS_FILTER.method='';", "every open starts with all methods")
	if strings.Contains(page, `target="_blank" rel="noopener">'+icon('landings')`) {
		t.Error("the Markdown button must open the viewer inside the admin, not the raw text in a new tab")
	}
	view := spaFunctionLine(html, "apiDocsMarkdownView")
	for _, want := range []string{
		"'/api/docs.md?lang='+encodeURIComponent(lang)", "apiDocsMarkdown(text)", `download="emtds-api-'+esc(lang)+'.md"`,
		"location.hash==='#/settings/docs/md'", "location.pathname==='/docs'&&location.hash==='#md'", "data-mdjump", `id="docs-back"`,
		`<div class="apidoc-layout"><nav class="apidoc-toc apidoc-mdtoc"`,
		`download="emtds-openapi.json"`, `download="emtds.postman_collection.json"`,
	} {
		must(t, view, want, "apiDocsMarkdownView: "+want)
	}
	must(t, spaFunctionLine(html, "publicDocsView"), "window.addEventListener('hashchange', paint)", "the public page follows #md")
	must(t, html, ".apidoc-md h3{", "markdown viewer style")
	must(t, spaFunctionLine(html, "boot"), "if(location.pathname==='/docs'){ publicDocsView(); return; }", "/docs skips sign-in")
	must(t, spaFunctionLine(html, "publicDocsView"), "apiDocs(location.hash.slice(1), true)", "the public page reuses apiDocs")
	must(t, spaFunctionLine(html, "render"), "if(location.pathname==='/docs') return;", "the router leaves the public page alone")
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, fn := range []string{"apiDocText", "apiDocOp", "apiDocsModel", "apiDocsFilter", "apiDocsHits", "apiDocsMark", "apiDocsCurl", "apiDocsSample", "apiDocsHighlight", "apiDocsMarkdown"} {
		must(t, pure, "function "+fn+"(", fn+" in the pure block")
	}
	keys := []string{
		"key.docs", "key.docs.auth", "key.docs.b.auth", "key.docs.b.errors", "key.docs.denied", "key.docs.b.denied",
		"key.docs.mcp", "key.docs.b.mcp", "key.docs.b.mcp2", "key.docs.engine", "key.docs.b.engine",
		"key.docs.search", "key.docs.method", "key.docs.method.all", "key.docs.toc", "key.docs.count", "key.docs.empty",
		"key.docs.copy", "key.docs.anchor", "key.docs.params", "key.docs.p.name", "key.docs.p.in", "key.docs.p.type",
		"key.docs.p.req", "key.docs.p.desc", "key.docs.in.path", "key.docs.in.query", "key.docs.yes", "key.docs.request",
		"key.docs.body", "key.docs.response", "key.docs.noresp", "key.docs.errors", "key.docs.action", "key.docs.admin",
		"key.docs.heavy", "key.docs.heavy.hint", "key.docs.public", "key.docs.rights", "key.docs.b.rights",
		"key.docs.destructive", "key.docs.destructive.hint", "key.docs.share", "key.docs.share.on", "key.docs.share.hint",
	}
	for code := range errorDescriptions {
		keys = append(keys, "key.docs.err."+strconv.Itoa(code))
	}
	for _, k := range keys {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	for _, gone := range []string{"key.docs.b.campaigns", "key.docs.b.catalogs", "key.docs.b.keys", "key.docs.b.stats", "key.docs.b.keywords"} {
		if strings.Contains(html, "'"+gone+"':") {
			t.Errorf("hand-written docs key %q is still in the dictionary", gone)
		}
	}
}
