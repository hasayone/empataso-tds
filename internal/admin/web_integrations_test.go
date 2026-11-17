package admin

import (
	"regexp"
	"strings"
	"testing"
)

// mustIntegration checks that the integrations page mounts one connector: the descriptor binds id to its panel,
// the page renders the section int-{id} with a body that the panel paints.
// t is the test handle, html is the SPA source, id is the route id and panel is the panel function name.
// It returns nothing and fails the test when the descriptor, the section or the paint call is missing.
func mustIntegration(t *testing.T, html, id, panel string) {
	t.Helper()
	must(t, html, "{id:'"+id+"', title:", "descriptor "+id)
	desc := regexp.MustCompile(`\{id:'` + regexp.QuoteMeta(id) + `', title:'[^']+', panel:` + regexp.QuoteMeta(panel) + `,`)
	if !desc.MatchString(html) {
		t.Errorf("descriptor %s does not bind panel %s", id, panel)
	}
	if spaFunctionLine(html, panel) == "" {
		t.Errorf("panel %s missing", panel)
	}
	page := spaFunctionLine(html, "integrations")
	must(t, page, `<section class="card pad intsec" id="int-${x.id}" role="tabpanel"`, "section int-"+id)
	must(t, page, `<div id="int-${x.id}-body"></div>`, "section body of "+id)
	must(t, page, "x.panel(q('#int-'+x.id+'-body'), live)", "every panel is painted into its section")
}

// TestSPAIntegrationsNav checks that the integrations page is a vertical tab list over one data table:
// each section stays mounted, the selected one is shown, the route carries the id and the badge states come from the dictionary.
// t is the test handle. It returns nothing and fails when the markup or the table regresses.
func TestSPAIntegrationsNav(t *testing.T) {
	html := spaSource()
	must(t, html, "const INTEGRATIONS=[", "one descriptor table")
	for _, id := range []string{"keitaro", "namecheap", "cloudflare", "checker", "ai"} {
		must(t, html, "{id:'"+id+"', title:", "descriptor "+id)
	}
	must(t, html, "allowed:me=>pageAllowed(me,'integrations')", "access check per integration")
	page := dictBetween(t, html, "async function integrations(arg,live){", "\n}\n")
	must(t, page, `role="tablist" aria-orientation="vertical"`, "vertical tab list")
	must(t, page, `role="tab"`, "tab role")
	must(t, page, `role="tabpanel"`, "panel role")
	must(t, page, `aria-selected`, "selected state")
	must(t, page, "ArrowDown", "arrow keys")
	must(t, page, "'Home'", "Home key")
	must(t, page, "'#/integrations/'+id", "deep link")
	must(t, page, "history.replaceState", "switching does not re-render the page")
	must(t, page, "hidden=!on", "inactive sections stay mounted")
	must(t, page, "'End'", "End key")
	must(t, page, "const vis=visible()", "arrows walk only the tabs in the side list")
	mustNot(t, page, "keitaroPanel(", "panels come from the table")
	mustNot(t, page, "innerHTML=''", "sections are never cleared on switch")
	mustNot(t, page, "<span class=\"im\"", "the monogram tile is replaced by the brand mark")
	must(t, page, "${intLogo(x)}", "every tab shows the brand mark")
	must(t, page, `class="inttabs" id="intnav" role="tablist"`, "the tab list holds tabs only")
	must(t, page, "</div>\n        ${addBtn('intadd')}\n      </nav>", "the Connect button sits under the tab list, outside it")
	must(t, page, "x.status()", "states come from the table")
	must(t, page, "integrationsShown(ids, INT_ADDED, state)", "the side list is the pure filter over the account list")
	must(t, html, ".intlay{display:grid;grid-template-columns:232px", "two-column layout")
	must(t, html, ".intnav{flex-direction:row;overflow-x:auto", "narrow screens use a scrollable row")
	must(t, html, ".inttabs{flex-direction:row;flex:none}", "narrow screens lay the tabs in the same row without squeezing them under the Connect button")
	for _, lang := range []string{"en", "ru", "uk"} {
		for _, key := range []string{"int.st.on", "int.st.off", "int.st.err", "int.st.run"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("%q missing for lang %q", key, lang)
			}
		}
	}
}

// TestIntegrationsAddGrid checks the "Connect" grid: it lists the allowed integrations with their mark, name,
// one-line description and the "Added" state, a click adds the integration and opens it, and the window works from the keyboard.
// t is the test handle. It returns nothing and fails when the grid or its keyboard handling regresses.
func TestIntegrationsAddGrid(t *testing.T) {
	html := spaSource()
	picker := dictBetween(t, html, "function intPicker(list, isAdded, onPick){", "\n}\n")
	for _, needle := range []string{
		`role="dialog" aria-modal="true" aria-labelledby="intpick-h"`,
		`<div class="intgrid">`,
		`class="intcard" data-pick="${x.id}">${intLogo(x)}`,
		"tr('int.desc.'+x.id)",
		"isAdded(x.id)?`<span class=\"pill ok\">${esc(tr('int.added'))}</span>`",
		"e.key==='Escape'",
		"document.addEventListener('keydown',key,true)",
		"document.removeEventListener('keydown',key,true)",
		"window.addEventListener('hashchange',onHash)",
		"q('[data-pick]',m).focus()",
		"back.focus()",
	} {
		must(t, picker, needle, "integration grid")
	}
	page := dictBetween(t, html, "async function integrations(arg,live){", "\n}\n")
	must(t, page, "const list=INTEGRATIONS.filter(x=>x.allowed(ME));", "only allowed integrations reach the page and the grid")
	must(t, page, "intPicker(list, id=>shown.has(id), pick)", "the grid gets the allowed list and the side list state")
	must(t, page, "if(!shown.has(id)) addToList(id);", "a pick outside the list adds it")
	must(t, page, "select(id, true);", "a pick opens the integration")
	must(t, page, "INT_ADDED=pageViewAutomationOrder((INT_ADDED||[]).concat(id), []);", "added ids go to the account list")
	must(t, page, "flushPageViews();", "the account list is saved")
	must(t, page, "${icon('plus')}<span>${esc(tr('int.add'))}</span>", "the Connect button uses the plus icon")

	drop := dictBetween(t, page, "qa('[data-intdrop]').forEach(b=>b.onclick=()=>{", "\n  });")
	must(t, drop, "if(INT_CONFIGURED.has(state[id])) return;", "a configured integration cannot be removed")
	must(t, drop, "INT_ADDED=(INT_ADDED||[]).filter(x=>x!==id);", "removal drops the id from the account list")
	must(t, page, "b.hidden=INT_CONFIGURED.has(state[b.dataset.intdrop])", "the remove button shows only for an unconfigured integration")
	must(t, html, "const INT_CONFIGURED=new Set(['on','err','run']);", "configured states")

	for _, id := range []string{"keitaro", "namecheap", "cloudflare", "checker", "ai"} {
		must(t, html, "  "+id+":'<svg viewBox=", "brand mark of "+id)
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, "int.desc."+id) {
				t.Errorf("int.desc.%s missing for lang %q", id, lang)
			}
		}
	}
	marks := dictBetween(t, html, "const INT_LOGOS={", "\n};")
	mustNot(t, marks, "http", "brand marks ask no third-party host")
	mustNot(t, marks, "currentColor", "brand marks keep their colours in both themes")
	for _, lang := range []string{"en", "ru", "uk"} {
		for _, key := range []string{"int.add", "int.add.title", "int.added", "int.empty", "int.drop"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("%q missing for lang %q", key, lang)
			}
		}
	}
	must(t, html, ".intgrid{display:grid;", "the grid layout")
}

// TestIntegrationsEmptyState checks the page without listed integrations: an info callout with the Connect button
// takes the place of the side list and the sections.
// t is the test handle. It returns nothing and fails when the empty state regresses.
func TestIntegrationsEmptyState(t *testing.T) {
	html := spaSource()
	page := dictBetween(t, html, "async function integrations(arg,live){", "\n}\n")
	must(t, page, "calloutHTML('info',tr('int.empty'),{id:'int-empty',hidden:true,actions:addBtn('primary')})", "empty state callout with the Connect button")
	must(t, page, "q('#int-empty').hidden=!!id;", "the callout shows only when nothing is selected")
	must(t, page, "q('.intnav').hidden=!shown.size;", "an empty side list is hidden")
	must(t, page, "q('#intlay').classList.toggle('empty', !shown.size);", "the empty page uses one column")
	must(t, html, ".intlay.empty{grid-template-columns:minmax(0,1fr)}", "one-column empty layout")
	must(t, page, "const want=id?'#/integrations/'+id:'#/integrations';", "the empty page keeps a clean address")
}

// TestIntegrationsDeepLink checks that a link to an integration outside the side list opens it and adds it,
// and that the old #/keitaro route still opens Keitaro.
// t is the test handle. It returns nothing and fails when a deep link loses its target.
func TestIntegrationsDeepLink(t *testing.T) {
	html := spaSource()
	page := dictBetween(t, html, "async function integrations(arg,live){", "\n}\n")
	must(t, page, "let cur=integrationTarget(arg, route().page, ids, ids.filter(id=>shown.has(id)));", "the route picks the target")
	must(t, page, "if(cur&&!shown.has(cur)) addToList(cur);", "a deep link adds its integration to the list")
	target := spaFunctionLine(html, "integrationTarget")
	must(t, target, "page==='keitaro'&&!arg?'keitaro':arg", "the old keitaro page opens Keitaro")
	must(t, target, "if(ids.includes(want)) return want;", "any allowed id opens, listed or not")
	must(t, target, "return shown[0]||'';", "unknown id falls back to the first listed one or the empty page")
	must(t, html, "keitaro:integrations,integrations}", "#/keitaro still routes to the integrations page")
	for _, name := range []string{"integrationsShown", "integrationTarget"} {
		at := strings.Index(html, "function "+name+"(")
		begin := strings.Index(html, "// ---- pure: begin ----")
		end := strings.Index(html, "// ---- pure: end ----")
		if at < begin || at > end {
			t.Errorf("%s must sit in the pure block so node runs its checks", name)
		}
	}
}

// cssRuleBodies returns the declaration blocks of every style rule whose selector matches sel.
// t is the test handle, html is the SPA source, sel is a regular expression applied to the selector text.
// It returns the bodies in source order; an empty slice means no rule matched.
func cssRuleBodies(t *testing.T, html, sel string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^[ \t]*([^{}\n@/][^{}\n]*)\{([^{}]*)\}`)
	match := regexp.MustCompile(sel)
	var out []string
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		if match.MatchString(m[1]) {
			out = append(out, m[2])
		}
	}
	return out
}

// TestButtonVariantsShareSize checks that the primary, danger and hover variants of a button
// change colours only, so a primary button and a plain one in one row have the same box.
// t is the test handle. It returns nothing and fails when a variant changes the geometry.
func TestButtonVariantsShareSize(t *testing.T) {
	html := spaSource()
	base := cssRuleBodies(t, html, `^button,\.btn$`)
	if len(base) != 1 {
		t.Fatalf("base button rule: found %d", len(base))
	}
	for _, needle := range []string{"height:32px", "border:1px solid", "padding:0 12px", "background:var(--surface-2)"} {
		must(t, base[0], needle, "base button rule")
	}
	geometry := regexp.MustCompile(`(^|;)\s*(height|min-height|padding|margin|border|border-width|font-size|font-weight|line-height|transform)\s*:`)
	for _, sel := range []string{`^button\.primary,`, `^button\.primary:hover,`, `^button\.danger,`} {
		bodies := cssRuleBodies(t, html, sel)
		if len(bodies) == 0 {
			t.Fatalf("rule %s not found", sel)
		}
		for _, b := range bodies {
			if geometry.MatchString(b) {
				t.Errorf("rule %s changes the button box: %s", sel, b)
			}
		}
	}
}

// TestIntegrationsFormLayout checks the two-column grid, the sandbox switch and the local-machine
// callouts inside the integration sections.
// t is the test handle. It returns nothing and fails when the form layout or a callout regresses.
func TestIntegrationsFormLayout(t *testing.T) {
	html := spaSource()
	must(t, html, ".intsec .g2{grid-template-columns:repeat(auto-fit,minmax(min(100%,260px),1fr))}", "fields pair up by card width, not by window width")
	must(t, html, ".swrow{", "switch row style")
	must(t, html, ".intsec .callout:last-child{margin-bottom:0}", "the last callout of a section sits flush")
	mustNot(t, html, "label.tog:has(#nc-sandbox)", "the sandbox is a switch, not a pill")

	nc := spaFunctionLine(html, "namecheapPanel")
	for _, needle := range []string{
		`<label class="sw"><input type="checkbox" id="nc-sandbox"`, `role="switch"`, `<i></i></label><label for="nc-sandbox">`,
		"cur.local?calloutHTML('warn',tr('nc.local'),{id:'nc-local-hint'}):''",
		"calloutHTML('info',tr('nc.intro'))",
		`<div class="g2"><div class="fld"><label for="nc-name">`,
		`<div class="g2"><div class="fld"><label for="nc-username">`,
		"q('#nc-sandbox',host).checked",
		"calloutSet(q('#nc-status',host),text,bad?'err':'ok')",
	} {
		must(t, nc, needle, "Namecheap card")
	}
	mustNot(t, nc, `class="tog"><input type="checkbox" id="nc-sandbox"`, "sandbox pill")
	mustNot(t, nc, `class="hint`, "Namecheap hints are callouts")
	account := strings.Index(nc, `data-ncpanel="account"`)
	contacts := strings.Index(nc, `data-ncpanel="contacts"`)
	for _, needle := range []string{"{id:'nc-local-hint'}", `id="nc-sandbox"`, `for="nc-name"`, `for="nc-ip"`} {
		at := strings.Index(nc, needle)
		if account < 0 || contacts < account || at < account || at > contacts {
			t.Errorf("%s must sit in the account tab of the Namecheap card", needle)
		}
	}

	cf := spaFunctionLine(html, "cloudflarePanel")
	for _, needle := range []string{
		`<div class="g2">`, "cur.local", "'cf.ip.local'", "'cf.ip.empty'",
		"{id:'cf-ip-hint',hidden:!!cur.public_ip}",
		"calloutSet(q('#cf-status',host),text,bad?'err':'ok')",
	} {
		must(t, cf, needle, "Cloudflare card")
	}
	mustNot(t, cf, `class="hint`, "Cloudflare hints are callouts")
}

// TestIntegrationsCalloutsSurviveSwitch checks that callouts inside the integration sections keep working
// after the side menu switches sections: the hidden attribute beats the callout display, every element id
// is unique across the mounted sections so calloutSet by id reaches the right box, and the Keitaro
// status callouts are rendered collapsed and filled through calloutSet.
// t is the test handle. It returns nothing and fails when a section or a callout can be shadowed.
func TestIntegrationsCalloutsSurviveSwitch(t *testing.T) {
	html := spaSource()
	must(t, html, "[hidden]{display:none!important}", "hidden wins over every display rule")
	must(t, html, ".callout[hidden]{display:none}", "a hidden callout is collapsed")
	must(t, spaFunctionLine(html, "calloutSet"), "el.hidden=!text;", "calloutSet toggles hidden only")

	ids := regexp.MustCompile(`id="([a-z][a-z0-9-]*)"|\{id:'([a-z][a-z0-9-]*)'`)
	owner := map[string]string{}
	for _, panel := range []string{"keitaroPanel", "namecheapPanel", "cloudflarePanel", "checkerPanel", "aiPanel"} {
		src := spaFunctionLine(html, panel)
		if src == "" {
			t.Fatalf("panel %s missing", panel)
		}
		for _, m := range ids.FindAllStringSubmatch(src, -1) {
			id := m[1] + m[2]
			if prev, ok := owner[id]; ok && prev != panel {
				t.Errorf("id %q is drawn by %s and %s; both sections stay mounted", id, prev, panel)
			}
			owner[id] = panel
		}
	}

	kt := spaFunctionLine(html, "keitaroPanel")
	for _, id := range []string{"kt-status", "kt-api", "kt-check-err"} {
		must(t, kt, "{id:'"+id+"',hidden:true,cls:'mt'}", id+" starts collapsed")
	}
	must(t, kt, "calloutSet(q('#kt-status'),", "check result goes through calloutSet")
	must(t, kt, "calloutSet(q('#kt-api'),ktApiText(r))", "API hint goes through calloutSet")
	must(t, kt, "calloutSet(q('#kt-check-err'),msg)", "check error goes through calloutSet")
}

// TestNoRetiredNamesInUI checks that no user-facing text carries a retired product
// name, variable prefix or key prefix. The names are assembled from parts so that no
// source file spells them out. t is the test handle; it fails when the SPA mentions one.
func TestNoRetiredNamesInUI(t *testing.T) {
	html := strings.ToLower(spaSource())
	for _, name := range []string{"ku" + "ztds", "k" + "z_"} {
		if strings.Contains(html, name) {
			t.Errorf("the SPA mentions the retired name %q", name)
		}
	}
}

// TestKeitaroAPIHintIsHonest checks that the hint after a successful check names the API, shows a
// version only from the answer, and says "not determined" in all three languages when there are no
// signs or they disagree. t is the test handle. It returns nothing and fails when a translation guesses the version.
func TestKeitaroAPIHintIsHonest(t *testing.T) {
	html := spaSource()
	for _, lang := range []string{
		`'kt.api':'Admin API v1. Keitaro version is not determined: the tracker gives no signs.'`,
		`'kt.api':'Admin API v1. Версия Keitaro не определена: признаков нет.'`,
		`'kt.api':'Admin API v1. Версію Keitaro не визначено: ознак немає.'`,
		`'kt.api.ver':'Admin API v1. Keitaro {v}. Signs: {signs}.'`,
		`'kt.api.ver':'Admin API v1. Keitaro {v}. Признаки: {signs}.'`,
		`'kt.api.ver':'Admin API v1. Keitaro {v}. Ознаки: {signs}.'`,
		`'kt.api.conflict':'Admin API v1. Keitaro version is not determined: the signs disagree ({signs}).'`,
		`'kt.api.conflict':'Admin API v1. Версия Keitaro не определена: признаки противоречат ({signs}).'`,
		`'kt.api.conflict':'Admin API v1. Версію Keitaro не визначено: ознаки суперечать ({signs}).'`,
	} {
		must(t, html, lang, "kt.api text")
	}
	kt := spaFunctionLine(html, "keitaroPanel")
	must(t, kt, "const ver=(r&&r.product_version)||'';", "the version comes from the check answer")
	must(t, kt, "if(r&&r.version_conflict) return trFill('kt.api.conflict'", "a conflict is named, not resolved")
	for _, bad := range []string{"10 или 11", "10 або 11", "version 10 or 11"} {
		mustNot(t, html, bad, "guessed product number")
	}
}

// TestSPACheckerIntegration checks the checker tile: the descriptor bound to its panel, the translated name,
// and a brand mark that is a flat inline svg of at most 1.5 KB with no host, no currentColor and no gradient.
// t is the test handle. It returns nothing and fails when the descriptor, the name or the mark regresses.
func TestSPACheckerIntegration(t *testing.T) {
	html := spaSource()
	mustIntegration(t, html, "checker", "checkerPanel")
	must(t, html, "name:'int.name.checker'", "the tile shows the translated name")
	must(t, html, "const cur=await get('/api/checker/settings',null);\n    return cur?(cur.error?'err':(cur.enabled?'on':'off')):'';", "the badge reads the settings")
	marks := dictBetween(t, html, "const INT_LOGOS={", "\n};")
	start := strings.Index(marks, "  checker:'")
	if start < 0 {
		t.Fatal("brand mark of checker missing")
	}
	mark := marks[start+len("  checker:'"):]
	mark = mark[:strings.Index(mark, "'")]
	if !strings.HasPrefix(mark, `<svg viewBox="`) {
		t.Errorf("the mark must start with <svg viewBox=, got %.30s", mark)
	}
	if len(mark) > 1536 {
		t.Errorf("the mark is %d bytes, want at most 1536", len(mark))
	}
	for _, bad := range []string{"http", "currentColor", "<defs", "linearGradient", "radialGradient"} {
		mustNot(t, mark, bad, "the checker mark is flat: "+bad)
	}
	for _, lang := range []string{"en", "ru", "uk"} {
		for _, key := range []string{"int.name.checker", "int.desc.checker"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("%q missing for lang %q", key, lang)
			}
		}
	}
}

// aiSetKeys are the dictionary keys of the AI assistant integration page; each one exists once per language.
var aiSetKeys = []string{
	"int.desc.ai", "ai.set.beta", "ai.set.intro", "ai.set.enabled", "ai.set.provider", "ai.set.key", "ai.set.test",
	"ai.set.keydel", "ai.set.keydel.ask", "ai.set.test.ok", "ai.set.models", "ai.set.models.none", "ai.set.model",
	"ai.set.limits", "ai.set.limits.hint", "ai.set.limits.day", "ai.set.limits.day.hint", "ai.set.l.maxout", "ai.set.l.steps", "ai.set.l.turnin", "ai.set.l.daily",
	"ai.set.l.userdaily", "ai.set.l.dailyusd", "ai.set.l.userdailyusd", "ai.set.nolimit", "ai.set.nolimit.warn",
	"ai.set.limit.range", "ai.set.limit.upto", "ai.set.usage.team", "ai.set.usage.you", "ai.set.usage.me", "ai.set.test.dropped", "ai.set.err.limit.range", "ai.set.err.limit.min", "ai.set.err.limit.daily", "ai.set.err.limit.daily.min", "ai.set.err.price", "ai.set.err.load", "ai.set.prices", "ai.set.price.model", "ai.set.price.in",
	"ai.set.price.out", "ai.set.price.unknown", "ai.set.usage", "ai.set.usage.tokens", "ai.set.usage.usd",
	"ai.set.usage.user", "ai.set.usage.none", "ai.set.threat",
	"int.name.ai", "ai.set.err_404", "ai.set.err_forbidden", "ai.set.err_server", "ai.set.err_file",
}

// TestSPAAIIntegrationName checks that the integration is shown as "AI assistant" with the Beta pill in the side list,
// the Connect grid and the page header, and that the name is translated in every language.
// t is the test handle. It returns nothing and fails when the name key, the pill or a translation is missing.
func TestSPAAIIntegrationName(t *testing.T) {
	html := spaSource()
	must(t, html, "name:'int.name.ai', beta:true", "the descriptor carries the name key and the beta flag")
	must(t, html, "function intNameHTML(x){", "one helper draws the name with the pill")
	must(t, html, `<span class="pill acc ibeta">${esc(tr('ai.set.beta'))}</span>`, "the pill reuses the project Beta badge")
	page := dictBetween(t, html, "async function integrations(arg,live){", "\n}\n")
	must(t, page, "<span>${intNameHTML(x)}</span>", "the side list shows the name with the pill")
	must(t, page, `<div class="bt">${intNameHTML(x)}</div>`, "the page header shows the name with the pill")
	must(t, dictBetween(t, html, "function intPicker(", "\n}\n"), `<span class="nm">${intNameHTML(x)}</span>`, "the Connect grid shows the name with the pill")
	for _, want := range []string{"'int.name.ai':'AI assistant'", "'int.name.ai':'AI-ассистент'", "'int.name.ai':'AI-асистент'"} {
		if n := strings.Count(html, want); n != 1 {
			t.Errorf("%s appears %d time(s), want 1", want, n)
		}
	}
}

// TestSPAAIIntegrationLoadErrors checks that a failed load of the settings names its reason by the status and the code of the
// answer, and that empty or damaged settings still draw the form instead of an error.
// t is the test handle. It returns nothing and fails when a reason branch or the form path is missing.
func TestSPAAIIntegrationLoadErrors(t *testing.T) {
	html := spaSource()
	reason := dictBetween(t, html, "function aiLoadError(e){", "\n}\n")
	for _, needle := range []string{
		"st===404", "tr('ai.set.err_404')",
		"st===401||st===403", "tr('ai.set.err_forbidden')",
		"st===503&&AI_ERR_KEYS[code]", "aiErrText(code,msg,e.body.params)",
		"tr('ai.set.err_server')+(msg?': '+msg:'')",
	} {
		must(t, reason, needle, "reason of a failed load")
	}
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	must(t, panel, "try{cur=await call('GET','/api/ai/settings')}catch(e){failure=e}", "the load keeps the status and the code of the error")
	must(t, panel, "aiLoadError(failure)", "a failed load shows its reason")
	must(t, panel, "if(!cur||typeof cur!=='object') cur={};", "empty settings are an empty form, not an error")
	must(t, panel, "typeof cur.error==='string'&&cur.error?calloutHTML('warn',trFill('ai.set.err_file',{text:cur.error})", "a damaged settings file shows a warning above the form")
	mustNot(t, panel, "tr('ai.set.err.load')", "the generic message no longer hides the reason")
	for _, k := range []string{"ai.set.err_404", "ai.set.err_forbidden", "ai.set.err_server", "ai.set.err_file"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
}

// TestSPAAIIntegration checks the AI assistant integration page: the descriptor and the brand mark, the admin-only access,
// the key field that never receives a stored value, the key check and delete calls, the model ticks, the limits with the
// explicit "no limit" confirmation, the price table and the usage block; and that every ai.set key is translated once per language.
// t is the test handle. It returns nothing and fails when the page, its contract calls or its dictionary entries regress.
func TestSPAAIIntegration(t *testing.T) {
	html := spaSource()
	mustIntegration(t, html, "ai", "aiPanel")
	must(t, html, "panel:aiPanel, allowed:me=>pageAllowed(me,'integrations')&&meIsAdmin()", "the beta is for administrators")
	must(t, html, "const cur=await get('/api/ai/settings',null);\n    return cur?(cur.enabled?'on':'off'):'';", "the badge reads the settings")
	mustNot(t, spaBetween(html, "const USER_ACCESS_SECTIONS=", "\n"), "assistant", "the beta section is not offered in the user roles")

	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	for _, needle := range []string{
		`id="ai-key" class="mono" type="password" value="" autocomplete="new-password"`,
		"input.value=typed[provider]||'';",
		"input.placeholder=info.has_key?(info.mask||tr('kt.key.saved')):'';",
		"call('GET','/api/ai/settings')",
		"call('POST','/api/ai/test',{provider,key:typed[provider]||''})",
		"call('PUT','/api/ai/settings',{clear_keys:[provider]})",
		"call('PUT','/api/ai/settings',body)",
		"aiModelPicker(q('#ai-models',host),pick)", "aiSettingsBody(cur,{enabled:q('#ai-enabled',host).checked,provider,models:list,model:pick.defId,prices:overrides,keys:typed,limits,perms:perm})",
		"Object.keys(typed).forEach(p=>delete typed[p]);", "q('#ai-key',host).value='';",
		"cur.usage_today", "cur.default_prices", "cur.prices",
		"trFill('ai.set.price.unknown'", "aiUseCardsHTML(cur)", `{id:'ai-nolimit-warn',hidden:true}`,
		"AI_PROVIDERS.some(p=>p[0]===cur.provider)",
	} {
		must(t, panel, needle, "AI settings page")
	}
	for _, gone := range []string{"ai-nolimit-ok", "ai.set.err.nolimit", "ai.set.nolimit.ask", "zeroLimits", "data-aimodel", "ai-model\""} {
		mustNot(t, panel, gone, "the old confirmation tick and the tick list are gone: "+gone)
	}
	for _, bad := range []string{"info.key", "keys[provider].key", `value="${esc(typed`, `value="${esc(info`, "innerHTML=typed"} {
		mustNot(t, panel, bad, "a stored or typed key is never written into the page markup")
	}
	must(t, html, "const AI_PROVIDERS=[['anthropic','Anthropic'],['openai','OpenAI'],['openrouter','OpenRouter'],['gemini','Gemini']];", "four providers")
	limits := dictBetween(t, html, "const AI_LIMITS=[", "\n];")
	for _, field := range []string{
		"max_output_tokens", "max_steps", "turn_input_tokens", "daily_tokens", "user_daily_tokens", "daily_usd", "user_daily_usd",
	} {
		must(t, limits, "'"+field+"'", "limit field "+field)
	}

	for _, k := range aiSetKeys {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	for _, label := range regexp.MustCompile(`'(ai\.set\.l\.[a-z]+)'`).FindAllStringSubmatch(limits, -1) {
		if n := strings.Count(html, "'"+label[1]+"':"); n != 3 {
			t.Errorf("limit label %q is defined %d time(s), want one per language", label[1], n)
		}
	}
	for _, m := range regexp.MustCompile(`tr(?:Fill)?\('(ai\.set\.[a-z.]+)'`).FindAllStringSubmatch(panel, -1) {
		if strings.HasSuffix(m[1], ".") {
			continue
		}
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, m[1]) {
				t.Errorf("tr(%q) missing for lang %q", m[1], lang)
			}
		}
	}
}

// aiLimitPrelude declares what the limit helpers of the settings page read: the limit table of the SPA and plain stand-ins for the
// dictionary and the formatters. tr returns "T:" and the key; trFill the key and its values.
func aiLimitPrelude(t *testing.T, html string) string {
	t.Helper()
	return "const AI_LIMITS=[" + dictBetween(t, html, "const AI_LIMITS=[", "\n];") + "\n];" + `
const esc = s => String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
const tr = k => 'T:' + k;
const trFill = (k, m) => k + JSON.stringify(m);
const fmt = n => 'n' + n;
const aiUSD = n => '$' + n;
const field = k => AI_LIMITS.find(f => f[0] === k);
const cur0 = {max_output_tokens:2048, max_steps:6, turn_input_tokens:60000, daily_tokens:2000000, user_daily_tokens:null, daily_usd:5, user_daily_usd:null,
  limit_ceilings:{max_output_tokens:32000, max_steps:20, turn_input_tokens:200000}, limit_max:{tokens:1000000000000, usd:1000000}};
`
}

// TestSPAAIIntegrationNoLimitBoxes runs the limit fields under node: each of the four daily limits has a "No limit" box that is
// ticked and disables the field when the limit is null, a ticked box or an empty field sends null, a typed number from 0 within
// limit_max is a real limit, and the per-turn limits have no box.
func TestSPAAIIntegrationNoLimitBoxes(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	for _, needle := range []string{
		`calloutHTML('warn','',{id:'ai-nolimit-warn',hidden:true})`,
		"AI_LIMITS.some(f=>f[3]&&noneBox(f[0]).checked)?tr('ai.set.nolimit.warn'):''",
		"input.disabled=box.checked;", "if(box.checked) input.value=''; else input.focus();",
		"none:!!f[3]&&noneBox(f[0]).checked",
	} {
		must(t, panel, needle, "no limit boxes")
	}
	must(t, assistantFn(html, "aiSettingsBody"), "if(n!==cur[k]) body[k]=n;", "an unchanged limit is not sent")
	for _, gone := range []string{"ai-nolimit-ok", "ai-nolimit-t", "ai.set.err.nolimit", "ai.set.nolimit.ask"} {
		mustNot(t, html[:strings.Index(html, "</style>")]+panel, gone, "the old confirmation tick is removed: "+gone)
	}
	assistantNode(t, []string{"aiLimitBounds", "aiLimitValue", "aiLimitFieldHTML"}, aiLimitPrelude(t, html), `
const daily = ['daily_tokens', 'user_daily_tokens', 'daily_usd', 'user_daily_usd'];
for (const k of AI_LIMITS.map(f => f[0])) {
  const h = aiLimitFieldHTML(field(k), cur0);
  eq([k, h.includes('id="ai-n-' + k + '"')], [k, daily.includes(k)], 'the No limit box is on the daily limits only');
}
let h = aiLimitFieldHTML(field('user_daily_tokens'), cur0);
eq([h.includes('id="ai-n-user_daily_tokens" checked'), h.includes('disabled'), h.includes('value=""')], [true, true, true], 'a null limit ticks the box, disables and empties the field');
h = aiLimitFieldHTML(field('daily_tokens'), cur0);
eq([h.includes('checked'), h.includes('disabled'), h.includes('value="2000000"')], [false, false, true], 'a stored number keeps the box off');
eq(h.includes('min="0" max="1000000000000"'), true, 'daily tokens: min 0, max limit_max.tokens');
h = aiLimitFieldHTML(field('user_daily_usd'), {...cur0, user_daily_usd:0});
eq([h.includes('checked'), h.includes('value="0"')], [false, true], 'a stored 0 is a real limit: the box stays off and the field shows 0');
h = aiLimitFieldHTML(field('daily_usd'), cur0);
eq([h.includes('min="0" max="1000000"'), h.includes('step="any"')], [true, true], 'daily USD: from 0, up to limit_max.usd, decimals');
eq(h.replace(/&quot;/g, '"').includes('ai.set.limit.upto{"min":"$0","max":"$1000000"}'), true, 'the hint gives the range of a daily USD limit');

eq(aiLimitValue(field('daily_tokens'), '', true, aiLimitBounds(field('daily_tokens'), cur0)), null, 'a ticked box sends null');
eq(aiLimitValue(field('daily_usd'), '12', true, aiLimitBounds(field('daily_usd'), cur0)), null, 'a ticked box wins over the text');
eq(aiLimitValue(field('daily_tokens'), '', false, aiLimitBounds(field('daily_tokens'), cur0)), null, 'an empty field is no limit');
const bt = aiLimitBounds(field('daily_tokens'), cur0), bu = aiLimitBounds(field('daily_usd'), cur0);
eq([aiLimitValue(field('daily_tokens'), '500', false, bt), aiLimitValue(field('daily_usd'), '0.25', false, bu)], [500, 0.25], 'a number');
eq([aiLimitValue(field('daily_tokens'), '0', false, bt), aiLimitValue(field('daily_usd'), '0', false, bu)], [0, 0], 'zero is a limit');
for (const bad of ['-3', '1.5', 'abc', '1000000000001']) eq(Number.isNaN(aiLimitValue(field('daily_tokens'), bad, false, bt)), true, 'daily tokens refuse "' + bad + '"');
for (const bad of ['-1', 'x', '1000001']) eq(Number.isNaN(aiLimitValue(field('daily_usd'), bad, false, bu)), true, 'daily USD refuse "' + bad + '"');
eq(aiLimitValue(field('max_steps'), '0', true, aiLimitBounds(field('max_steps'), cur0)), NaN, 'a per-turn limit has no No limit box, 0 is refused');
`)
}

// TestSPAAIIntegrationTurnLimitCeilings checks that each per-turn limit shows the ceiling of the server as a hint ("from 1 to N")
// and as the min and max of the field, and that nothing is shown for a server that sends no ceilings.
func TestSPAAIIntegrationTurnLimitCeilings(t *testing.T) {
	html := spaSource()
	assistantNode(t, []string{"aiLimitBounds", "aiLimitValue", "aiLimitFieldHTML"}, aiLimitPrelude(t, html), `
const want = {max_output_tokens:32000, max_steps:20, turn_input_tokens:200000};
for (const k of Object.keys(want)) {
  const h = aiLimitFieldHTML(field(k), cur0);
  eq(h.includes('min="1" max="' + want[k] + '"'), true, k + ' has min 1 and its ceiling as attributes');
  eq(h.replace(/&quot;/g, '"').includes('ai.set.limit.range{"min":"n1","max":"n' + want[k] + '"}'), true, k + ' hints from 1 to its ceiling');
  eq(aiLimitBounds(field(k), cur0), {min:1, max:want[k]}, k + ' bounds');
  const b = aiLimitBounds(field(k), cur0);
  eq([aiLimitValue(field(k), '1', false, b), aiLimitValue(field(k), String(want[k]), false, b)], [1, want[k]], k + ' accepts 1 and the ceiling');
  eq([aiLimitValue(field(k), '0', false, b), aiLimitValue(field(k), String(want[k] + 1), false, b), aiLimitValue(field(k), '2.5', false, b)].map(Number.isNaN), [true, true, true], k + ' refuses 0, above the ceiling and fractions');
}
const old = aiLimitFieldHTML(field('max_steps'), {max_steps:6});
eq([old.includes(' max="'), old.includes('ai.set.limit.range'), old.includes('min="1"')], [false, false, true], 'a server without ceilings gives no max and no hint');
eq(aiLimitValue(field('max_steps'), '9999', false, aiLimitBounds(field('max_steps'), {})), 9999, 'no ceiling, no upper check');
`)
}

// TestSPAAIIntegrationUsageCard runs the usage cards under node: one card for the team (usage_today against daily_tokens and
// daily_usd) and one for the signed-in user (usage_today.me against user_daily_*), "used of limit" in tokens and USD, "no limit"
// instead of a limit of 0 with no bar, a bar with its share for a limited row, and the table of users stays below.
func TestSPAAIIntegrationUsageCard(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	must(t, panel, "${aiUseCardsHTML(cur)}", "the card is drawn on the page")
	must(t, panel, "${users.length?`<div class=\"tw mt\"><table class=\"ftable\">", "the table by user stays")
	if strings.Index(panel, "${aiUseCardsHTML(cur)}") > strings.Index(panel, "<table class=\"ftable\"><thead><tr><th>${tr('ai.set.usage.user')}") {
		t.Error("the table by user must be below the usage card")
	}
	prelude := aiLimitPrelude(t, html)
	assistantNode(t, []string{"aiPct", "aiLim", "aiUseCards", "aiUseCardsHTML"}, strings.Replace(prelude, "const trFill = (k, m) => k + JSON.stringify(m);",
		"const trFill = (k, m) => k.replace('ai.chat.usage.', '') + ':' + (m.limit !== undefined ? m.used + '/' + m.limit : m.used !== undefined ? m.used : m.login);", 1), `
const cur = {daily_tokens:2000000, daily_usd:5, user_daily_tokens:null, user_daily_usd:2,
  usage_today:{day:'2026-11-16', tokens:500000, usd:4.5, users:[], me:{uid:'u1', login:'admin', tokens:1234, usd:0.5}}};
eq(aiPct(5, 10), 0.5, 'share');
eq([aiPct(20, 10), aiPct(0, 10), aiPct(5, 0), aiPct(5, null), aiPct(5, undefined)], [1, 0, 1, null, null], 'a share is capped at 1, a zero limit is used up, a missing limit has none');
const cards = aiUseCards(cur);
eq(cards.length, 2, 'two cards');
eq(cards[0].title, 'T:ai.set.usage.team', 'the team card');
eq(cards[0].rows.map(r => [r.label, r.text, r.pct]), [['T:ai.set.usage.tokens', 'of:n500000/n2000000', 0.25], ['T:ai.set.usage.usd', 'of:$4.5/$5', 0.9]], 'team against the daily limits');
eq(cards[1].title, 'ai.set.usage.me:admin', 'the card of the user names the login');
eq(cards[1].rows.map(r => [r.text, r.pct]), [['nolimit:n1234', null], ['of:$0.5/$2', 0.25]], 'the user against the per-user limits, null reads as no limit');
const off = aiUseCards({...cur, daily_tokens:null, daily_usd:null});
eq(off[0].rows.map(r => [r.text, r.pct]), [['nolimit:n500000', null], ['nolimit:$4.5', null]], 'a card without ceilings shows only what was spent');
const out = aiUseCardsHTML(cur);
eq((out.match(/role="progressbar"/g) || []).length, 3, 'a bar for every limited row only');
eq(out.includes('aria-valuenow="90"') && out.includes('ai-bar hot'), true, 'a row near its limit is marked hot');
eq(out.includes('aria-valuenow="25"'), true, 'share in percent');
const bare = aiUseCards({});
eq(bare.map(c => c.title), ['T:ai.set.usage.team', 'T:ai.set.usage.you'], 'no usage, no login');
eq(bare[0].rows.map(r => r.pct), [null, null], 'no limits, no bars');
`)
}

// TestSPAAIIntegrationKeyStorageNote checks that the line about where the key is kept is an info callout inside the key field,
// not a separate line under the page.
func TestSPAAIIntegrationKeyStorageNote(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	must(t, panel, `autocomplete="new-password" spellcheck="false">${calloutHTML('info',tr('ai.set.threat'),{id:'ai-threat'})}</div>`, "the note sits in the key field")
	mustNot(t, panel, "<p class=\"hint mt\">${tr('ai.set.threat')}</p>", "no separate line at the bottom")
	if n := strings.Count(panel, "ai.set.threat"); n != 1 {
		t.Errorf("the key note is drawn %d times, want 1", n)
	}
}

// TestSPAAIIntegrationModelCatalog checks the catalog behind the model list of the page: GET /api/ai/models with the provider and
// refresh=1 for the Refresh button, the models of "Check key" stored in the same cache, ids and objects both accepted, and the
// list on the page being the shared picker with the tools:false rule.
func TestSPAAIIntegrationModelCatalog(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	for _, needle := range []string{
		"aiModelPicker(q('#ai-models',host),pick)", "withDefault:true", "onRefresh:()=>refreshCatalog()",
		"await aiCatalogLoad(p,refresh)", "await loadCatalog(true,list=>applyCatalog(list,showCat),showCat)", "aiCatalogSet(provider,(r&&r.models)||[])",
		"call('POST','/api/ai/test',{provider,key:typed[provider]||''})", "aiModelsMerge(cat&&cat.models,Array.from(pick.sel))",
		"aiModelDefault(pick.models,pick.sel,pick.defId)",
	} {
		must(t, panel, needle, "model list on the page")
	}
	prelude := `
const AI_CAT = {};
const calls = [];
let answer = {provider:'openrouter', fetched_at:'2026-11-16T09:00:00Z', ttl_seconds:3600, models:[{id:'a/b', name:'AB', context:1000, input_per_m:1, output_per_m:2, tools:true}, {id:'a/c', tools:false}, {id:'a/b'}]};
async function call(m, p){ calls.push([m, p]); return answer; }
`
	assistantNode(t, []string{"aiModelNorm", "aiModelsMerge", "aiCatalogSet", "aiCatalogFresh", "aiCatalogLoad"}, prelude, `
let list = await aiCatalogLoad('openrouter');
eq(calls[0], ['GET', '/api/ai/models?provider=openrouter'], 'the catalog request');
await aiCatalogLoad('open router', true);
eq(calls[1], ['GET', '/api/ai/models?provider=open%20router&refresh=1'], 'refresh=1 and an escaped provider');
eq(list.map(m => [m.id, m.name, m.tools]), [['a/b', 'AB', true], ['a/c', '', false]], 'objects are normalised and a repeated id is dropped');
eq([AI_CAT.openrouter.fetched_at, AI_CAT.openrouter.ttl_seconds], ['2026-11-16T09:00:00Z', 3600], 'the cache keeps the server time and ttl');
eq(aiCatalogFresh('openrouter'), true, 'a catalog just read is fresh');
AI_CAT.openrouter.read -= 3601000;
eq(aiCatalogFresh('openrouter'), false, 'a catalog older than its ttl is not');
eq(aiCatalogFresh('gemini'), false, 'no catalog, not fresh');
list = aiCatalogSet('openai', ['gpt-a', 'gpt-b', 'gpt-a']);
eq(list.map(m => [m.id, m.tools, m.input_per_m]), [['gpt-a', null, null], ['gpt-b', null, null]], 'the models of Check key may be plain ids');
eq(AI_CAT.openai.models, list, 'Check key fills the same cache');
answer = null;
eq(await aiCatalogLoad('gemini'), [], 'an empty answer is an empty catalog');
`)
}

// TestSPAAIIntegrationSavesOnlyChanges runs the body of the settings save under node: what equals the loaded settings is not sent
// (enabled, provider, allowed models in any order, default model, prices, limits), a daily limit is null with its box ticked or an empty field, an
// invalid limit stops the save and names its field, and a key goes only when one was typed.
func TestSPAAIIntegrationSavesOnlyChanges(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	for _, needle := range []string{
		"input.setAttribute('aria-invalid','true');", "aiLimitError(AI_LIMITS.find(x=>x[0]===bad),cur)",
		"aiErrText('ai_bad_request','',{field:refuse,reason:'required'})",
		"input.focus();", "limitInput(f[0]).removeAttribute('aria-invalid')", "ev.target.removeAttribute('aria-invalid')",
		"if(Object.keys(body).length) await call('PUT','/api/ai/settings',body);",
	} {
		must(t, panel, needle, "settings save")
	}
	for _, gone := range []string{"body.provider", "body.allowed_models", "ai.set.err.limit"} {
		mustNot(t, panel, gone, "the body is built by aiSettingsBody: "+gone)
	}
	assistantNode(t, []string{"aiLimitBounds", "aiLimitValue", "aiSameList", "aiPriceRows", "aiPriceOverrides", "aiSettingsBody"}, aiLimitPrelude(t, html)+`
const cur = Object.assign({}, cur0, {enabled:true, provider:'openrouter', model:'a/b', allowed_models:['a/b', 'c/d'], keys:{openrouter:{has_key:true, mask:'sk-1'}, openai:{has_key:true, mask:'sk-2'}}, prices:{'a/b':{input_per_m:1, output_per_m:2}}});
const form = over => {
  const limits = {};
  for (const f of AI_LIMITS) limits[f[0]] = {raw: cur[f[0]] != null ? String(cur[f[0]]) : '', none: !!f[3] && cur[f[0]] == null};
  return Object.assign({enabled:true, provider:'openrouter', models:['c/d', 'a/b'], model:'a/b', prices:{'a/b':{input_per_m:1, output_per_m:2}}, keys:{}, limits}, over);
};
const withLimit = (k, v) => { const s = form(); s.limits[k] = v; return s; };
`, `
eq(aiSettingsBody(cur, form()), {body:{}, bad:'', refuse:''}, 'nothing changed, nothing sent');
eq(aiSettingsBody(cur, form({models:[]})).refuse, 'allowed_models', 'on with no model ticked: nothing is sent');
eq(aiSettingsBody(cur, form({models:[]})).body, null, 'the refused form has no body');
eq(aiSettingsBody(cur, form({enabled:false, models:[]})).body, {enabled:false, allowed_models:[]}, 'off with no model ticked is allowed');
const keyless = Object.assign({}, cur, {enabled:false, keys:{openrouter:{has_key:false}}});
eq([aiSettingsBody(keyless, form()).refuse, aiSettingsBody(keyless, form()).body], ['keys', null], 'on with no key saved or typed: nothing is sent');
eq(aiSettingsBody(keyless, form({keys:{openrouter:'sk-9'}})).body.keys, {openrouter:'sk-9'}, 'a typed key lets the assistant be turned on');
eq(aiSettingsBody(keyless, form({enabled:false})).refuse, '', 'off needs no key');
eq(aiSettingsBody(Object.assign({}, keyless, {keys:{openai:{has_key:true}}}), form()).refuse, 'keys', 'a key of another provider does not count');
eq(aiSettingsBody(Object.assign({}, keyless, {keys:undefined}), form()).refuse, 'keys', 'no keys map is no key');
eq(aiSettingsBody(keyless, form({models:[]})).refuse, 'allowed_models', 'the missing model is named before the missing key');
const priced = Object.assign({}, cur, {prices:{'a/b':{input_per_m:1, output_per_m:2}, 'c/d':{input_per_m:5, output_per_m:6}}, default_prices:{'c/d':{input_per_m:5, output_per_m:6}}});
eq(aiPriceOverrides(priced), {'a/b':{input_per_m:1, output_per_m:2}}, 'a saved price equal to the default one is no override');
eq(aiSettingsBody(priced, form()).body, {}, 'overrides equal to the stored ones: no prices in the body');
eq(aiSettingsBody(priced, form({prices:{'a/b':{input_per_m:1, output_per_m:2}, 'c/d':{input_per_m:7, output_per_m:8}}})).body, {prices:{'a/b':{input_per_m:1, output_per_m:2}, 'c/d':{input_per_m:7, output_per_m:8}}}, 'a new override is sent with the rest');
eq(aiSettingsBody(priced, form({prices:{}})).body, {prices:{}}, 'the stored override removed');
eq(aiSettingsBody(cur, form({enabled:false})).body, {enabled:false}, 'the switch');
eq(aiSettingsBody(cur, form({provider:'openai'})).body, {provider:'openai'}, 'the provider');
eq(aiSettingsBody(cur, form({models:['a/b']})).body, {allowed_models:['a/b']}, 'the allowed models, the default model stays out');
eq(aiSettingsBody(cur, form({model:'c/d'})).body, {model:'c/d'}, 'the default model');
eq(aiSettingsBody(cur, form({prices:{'a/b':{input_per_m:1, output_per_m:3}}})).body, {prices:{'a/b':{input_per_m:1, output_per_m:3}}}, 'the prices as a whole map');
eq(aiSettingsBody(cur, form({prices:{}})).body, {prices:{}}, 'prices cleared');
eq(aiSettingsBody(cur, withLimit('max_steps', {raw:'8', none:false})).body, {max_steps:8}, 'a changed per-turn limit');
eq(aiSettingsBody(cur, withLimit('max_steps', {raw:' 6 ', none:false})).body, {}, 'the same number typed with spaces is no change');
eq(aiSettingsBody(cur, withLimit('daily_tokens', {raw:'', none:true})).body, {daily_tokens:null}, 'No limit sends null');
eq(aiSettingsBody(cur, withLimit('daily_usd', {raw:'', none:false})).body, {daily_usd:null}, 'an emptied field sends null');
eq(aiSettingsBody(cur, withLimit('user_daily_tokens', {raw:'', none:true})).body, {}, 'a limit already off is not sent again');
eq(aiSettingsBody(cur, withLimit('user_daily_tokens', {raw:'500', none:false})).body, {user_daily_tokens:500}, 'a limit set where there was none');
eq(aiSettingsBody(cur, withLimit('daily_usd', {raw:'7.5', none:false})).body, {daily_usd:7.5}, 'dollars');
eq(aiSettingsBody(cur, withLimit('user_daily_usd', {raw:'0', none:false})).body, {user_daily_usd:0}, 'zero dollars is a limit');
for (const [k, v] of [['max_steps', {raw:'', none:true}], ['max_steps', {raw:'21', none:false}], ['daily_usd', {raw:'-1', none:false}], ['daily_tokens', {raw:'2.5', none:false}]]) {
  const r = aiSettingsBody(cur, withLimit(k, v));
  eq([r.body, r.bad], [null, k], 'an empty per-turn limit or an out of range value is not sent: ' + k + ' ' + v.raw);
}
eq(aiSettingsBody(cur, form({keys:{openrouter:'sk-1', openai:''}})).body, {keys:{openrouter:'sk-1'}}, 'only a typed key goes');
eq(aiSettingsBody(cur, form({keys:{openai:''}})).body, {}, 'an empty key goes nowhere');
eq(aiSettingsBody({}, {enabled:false, provider:'', models:[], model:'', prices:{}, keys:{}, limits:{}}).bad, 'max_output_tokens', 'a form with no limits is refused, not sent');
`)
}

// TestSPAAIIntegrationCheckKeyDropsModels checks the models that a refreshed catalog no longer offers: they are taken off the
// ticked list and named in the status box.
func TestSPAAIIntegrationCheckKeyDropsModels(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	for _, needle := range []string{
		"const gone=aiModelsGone(list,pick.sel);", "gone.forEach(m=>pick.sel.delete(m));",
		"if(gone.length) say(trFill('ai.set.test.dropped',{models:gone.join(', ')}),'warn');",
	} {
		must(t, panel, needle, "Check key")
	}
	for _, needle := range []string{"applyCatalog(aiCatalogSet(provider,(r&&r.models)||[]))", "await loadCatalog(true,list=>applyCatalog(list,showCat),showCat)", "(done||paintList)(list)"} {
		must(t, panel, needle, "Check key and Refresh share applyCatalog")
	}
	if n := strings.Count(panel, "aiModelsGone("); n != 1 {
		t.Errorf("aiModelsGone is called %d times in the settings page, want once (Check key and Refresh share it)", n)
	}
	assistantNode(t, []string{"aiModelsGone"}, "", `
const models = [{id:'a', tools:true}, {id:'b', tools:false}, {id:'c', tools:null}];
eq(aiModelsGone(models, new Set(['a', 'b', 'c', 'z'])), ['b', 'z'], 'a model without tools and a missing one are taken off');
eq(aiModelsGone(models, new Set(['a', 'c'])), [], 'nothing to take off');
eq(aiModelsGone([], new Set()), [], 'empty');
`)
}

// TestSPAAIIntegrationLimitError runs the text of a refused limit field under node: a daily limit mentions the "No limit" box, dollars
// are written as money, and a limit with no upper bound from the server gets the text without "to".
func TestSPAAIIntegrationLimitError(t *testing.T) {
	html := spaSource()
	assistantNode(t, []string{"aiLimitBounds", "aiLimitError"}, aiLimitPrelude(t, html), `
const none = '"none":"T:ai.set.nolimit"}';
eq(aiLimitError(field('daily_usd'), cur0), 'ai.set.err.limit.daily{"name":"T:ai.set.l.dailyusd","min":"$0","max":"$1000000",' + none, 'a daily limit in dollars');
eq(aiLimitError(field('daily_tokens'), cur0), 'ai.set.err.limit.daily{"name":"T:ai.set.l.daily","min":"n0","max":"n1000000000000",' + none, 'a daily limit in tokens');
eq(aiLimitError(field('max_steps'), cur0), 'ai.set.err.limit.range{"name":"T:ai.set.l.steps","min":"n1","max":"n20",' + none, 'a per-turn limit with its ceiling');
eq(aiLimitError(field('max_steps'), {}), 'ai.set.err.limit.min{"name":"T:ai.set.l.steps","min":"n1","max":"",' + none, 'a per-turn limit with no ceiling');
eq(aiLimitError(field('daily_usd'), {}), 'ai.set.err.limit.daily.min{"name":"T:ai.set.l.dailyusd","min":"$0","max":"",' + none, 'a daily limit with no upper bound');
`)
}

// TestSPAAIIntegrationBrowserFixes checks the fixes of the browser review: the invalid field has a red border in both themes, a
// refused save shows its message under the Save button and focuses the field, deleting the key repaints only the key block and
// both the delete and the save tell the integrations page to read the badge at once, and the assistant has one name in every language.
func TestSPAAIIntegrationBrowserFixes(t *testing.T) {
	html := spaSource()
	must(t, html, `input[aria-invalid="true"],input[aria-invalid="true"]:focus{border-color:var(--bad)}`, "a field marked invalid has the error border")
	must(t, html, `input[aria-invalid="true"]:focus{box-shadow:0 0 0 3px var(--bad-soft)}`, "and the error focus ring")
	if n := strings.Count(html, "--bad:"); n < 3 {
		t.Errorf("the error colour must be defined for the light theme, the dark one and the system dark one, found %d", n)
	}
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	for _, needle := range []string{
		`calloutHTML('info','',{id:'ai-save-status',hidden:true,cls:'mt'})`,
		"const showSave=(text,kind)=>calloutSet(q('#ai-save-status',host),text,kind);",
		"showSave(aiLimitError(AI_LIMITS.find(x=>x[0]===bad),cur),'err');", "input.focus();",
		"if(tab==='conn') show(aiErrOf(err),'err'); else showSave(aiErrOf(err),'err');", "showSave(trFill('ai.set.err.price',{model:m}),'err')",
		"host.dispatchEvent(new CustomEvent('intrefresh',{bubbles:true,detail:'ai'}))",
	} {
		must(t, panel, needle, "settings page")
	}
	del := dictBetween(t, panel, "q('#ai-keydel',host).onclick=async e=>{", "\n  };")
	must(t, del, "keys[provider]={has_key:false,mask:''};", "the key block is told the key is gone")
	must(t, del, "paintKey();", "only the key block is repainted")
	must(t, del, "touchState();", "the badge is read again at once")
	mustNot(t, del, "aiPanel(host,live)", "the form with unsaved edits is not drawn again")
	save := dictBetween(t, panel, "q('#ai-save',host).onclick=async e=>{", "\n  };")
	must(t, save, "touchState();", "a saved form refreshes the badge at once")
	must(t, panel, "cur.keys=keys;", "the save check reads the same keys the key block changes")
	must(t, html, "q('.intpane').addEventListener('intrefresh',e=>{if(live()) refresh([e.detail])});", "the integrations page refreshes the badge asked for")
	for _, k := range []string{"ai.chat.err.ai_bad_request.key_required"} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	must(t, html, "key_required:'ai.chat.err.ai_bad_request.key_required'", "the variant of a missing key")
	must(t, html, "const AI_REQUIRED={allowed_models:'models_required',keys:'key_required'};", "the field a required reason names picks the variant")
	must(t, html, "'ai.chat.err.ai_bad_request.key_required':'Сохраните ключ провайдера, прежде чем включать ассистента.'", "russian text of a missing key")
	must(t, html, "'ai.chat.err.ai_bad_request.key_required':'Збережіть ключ провайдера, перш ніж вмикати асистента.'", "ukrainian text of a missing key")
	for _, name := range []string{"'int.name.ai':'AI assistant'", "'ai.chat.title':'AI assistant'", "'int.name.ai':'AI-ассистент'", "'ai.chat.title':'AI-ассистент'",
		"'int.name.ai':'AI-асистент'", "'ai.chat.title':'AI-асистент'", "'ai.chat.open':'Відкрити AI-асистента'", "'ai.chat.open':'Открыть AI-ассистента'"} {
		must(t, html, name, "one name of the assistant in the tab and in the chat")
	}
	if n := strings.Count(html, "'ai.chat.fab':'AI'"); n != 3 {
		t.Errorf("the round button reads AI in every language, found %d", n)
	}
	for _, old := range []string{"ШІ", "ИИ"} {
		mustNot(t, html, old, "a second name of the assistant: "+old)
	}
}
