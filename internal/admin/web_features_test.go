package admin

import (
	"regexp"
	"strings"
	"testing"
)

// TestSpaKeepsUnknownRules checks that saving a stream copies the rule keys the editor
// does not own, so a key written by another version or the API is not erased.
func TestSpaKeepsUnknownRules(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function ruleExtras(", "ruleExtras in pure block")
	collect := spaFunctionLine(html, "collectStream")
	must(t, collect, "ruleExtras(s.rules, ruleKeys())", "collectStream starts from the unknown keys")
	mustNot(t, collect, "const r={}", "collectStream must not start from an empty rules object")
	keys := spaFunctionLine(html, "ruleKeys")
	must(t, keys, "COND", "ruleKeys is built from the editor conditions")
	must(t, keys, "[c.k]", "ruleKeys takes the key of each condition, sub_ids and model included")
}

// TestSpaModelAndSubIDs checks the device model and Sub ID conditions, the model column
// of the click and conversion logs and the filter group.
func TestSpaModelAndSubIDs(t *testing.T) {
	html := spaSource()
	must(t, html, "{k:'model',l:'Device model',g:'Device',kind:'list'", "model condition in the Device group")
	must(t, html, "{k:'sub_ids',l:'Sub ID',g:'Source',kind:'subids'", "Sub ID condition in the Source group")
	must(t, html, "const SUBID_MAX=12", "Sub ID limit")
	for _, fn := range []string{"subIDRows", "subIDMap", "subIDActive", "condKeyOf", "modelColSwap", "adoptModelCol", "subIDOverflow", "errInfo"} {
		must(t, spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----"), "function "+fn+"(", fn+" in pure block")
	}
	must(t, spaFunctionLine(html, "configured"), "case 'subids'", "configured knows Sub ID rows")
	must(t, spaFunctionLine(html, "condChips"), "case 'subids'", "chips for Sub ID rules")
	must(t, spaFunctionLine(html, "condRow"), "subIDEditor(", "Sub ID editor row")
	must(t, spaFunctionLine(html, "paintStream"), "bindSubIDs(body)", "Sub ID editor handlers")
	must(t, spaFunctionLine(html, "collectStream"), "readSubIDs(d)", "Sub ID rows are saved")
	must(t, spaFunctionLine(html, "resetCond"), "case 'subids'", "Sub ID rows can be cleared")
	must(t, html, "id:'device_model',label:'fld.device_model',on:1,field:'device_model'", "click log column")
	must(t, html, "id:'device_model',label:'fld.device_model',on:0,field:'device_model'", "conversion log column")
	must(t, html, "'device_model','brand'", "model leads the device filter group")
	must(t, html, "fieldLabel('device_model')", "visit detail shows the model")
	must(t, spaFunctionLine(html, "loadCols"), "adoptModelCol(out,modelIsNew)", "the model column takes the brand place once, from the saved list")
	mustNot(t, html, "LOG_COLS_MODEL", "no browser-local swap flag")
}

// TestSpaSticky checks the campaign switch for returning visitors.
func TestSpaSticky(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "openFlowSettings"), `id="g_sticky"`, "sticky switch in campaign settings")
	must(t, spaFunctionLine(html, "openFlowSettings"), "tr('sticky.hint')", "sticky hint")
	must(t, spaFunctionLine(html, "openFlowSettings"), "tr('sticky.noweight')", "sticky needs choice by weight")
	collect := spaFunctionLine(html, "collectFlow")
	must(t, collect, "g.sticky_stream=true", "sticky saved as true")
	must(t, collect, "delete g.sticky_stream", "sticky removed when off")
}

// TestSpaShareCeiling checks that shares are typed by hand, summed per list, shown in red above
// 100 and block saving, and that the server answer is translated.
func TestSpaShareCeiling(t *testing.T) {
	html := spaSource()
	mustNot(t, html, "balanceShares", "automatic share balancing is gone")
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, fn := range []string{"shareSum", "shareInt", "sharesOver", "shareOverflow", "errKey"} {
		must(t, pure, "function "+fn+"(", fn+" in pure block")
	}
	must(t, spaFunctionLine(html, "shareListHTML"), `data-sharesum="${kind}"`, "sum line under each list")
	must(t, spaFunctionLine(html, "bindShareBox"), "paintShareSums(body)", "sums repaint on input")
	must(t, spaFunctionLine(html, "paintShareSums"), "classList.toggle('over'", "fields turn red over 100")
	must(t, spaFunctionLine(html, "saveBlock"), "shareBlock()", "saving is blocked over 100")
	must(t, spaFunctionLine(html, "saveBlock"), "subIDBlock()", "saving is blocked by a long Sub ID value")
	must(t, spaFunctionLine(html, "saveAll"), "saveBlock()", "saving campaigns checks the block")
	must(t, spaFunctionLine(html, "saveAll"), "if(cronDirty())await saveCron()", "the block does not hold back the cron settings")
	must(t, spaFunctionLine(html, "syncSaveBar"), "saveBlock()", "the save button is disabled over 100")
	must(t, spaFunctionLine(html, "collectStream"), "shareInt(share.value)", "a typed share is a whole non-negative percent")
	must(t, spaFunctionLine(html, "saveAll"), "toast(e.message,true)", "server answers are translated by toast")
	must(t, html, "'shares exceed 100':'err.shares'", "400 shares exceed 100 is translated")
	must(t, html, `max="100"`, "share percent ceiling")
}

// TestKeitaroSettingsUI checks the address and token form of the Keitaro card: the key is
// a password field that is never filled in, and the check runs with the typed values.
func TestKeitaroSettingsUI(t *testing.T) {
	html := spaSource()
	src := spaFunctionLine(html, "keitaroPanel")
	for _, needle := range []string{
		"/api/keitaro/settings",
		`id="kt-base"`,
		`id="kt-key"`,
		`type="password"`,
		`autocomplete="new-password"`,
		`id="kt-save"`,
		`id="kt-check"`,
		"kt.key.saved",
		"call('PUT','/api/keitaro/settings'",
	} {
		must(t, src, needle, "keitaro settings form")
	}
	mustNot(t, src, `id="kt-key" class="mono" type="text"`, "the key field is never plain text")
	mustNot(t, src, "conn.api_key", "the key is never read back into the page")
	for _, msg := range []string{"api_key required for new host", "manifest_host", "api_key is required", "import already running"} {
		must(t, html, "'"+msg+"':", "keitaro error text "+msg)
	}
	mustNot(t, html, "KEITARO_BASE_URL", "the page no longer asks for environment variables")
}

// TestNamecheapUI checks the account card, the purchase window with the confirmed price,
// the pending row actions and the name server change.
func TestNamecheapUI(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "integrations"), `id="int-namecheap"`, "Namecheap card on the integrations page")
	panel := spaFunctionLine(html, "namecheapPanel")
	for _, needle := range []string{
		"/api/registrars/namecheap",
		"/api/registrars/namecheap/test",
		`type="password"`,
		`autocomplete="new-password"`,
		"has_key",
		"has_contacts",
		"cur.error",
		"detect",
		"contacts:null",
		"call('PUT'",
	} {
		must(t, panel, needle, "Namecheap account card")
	}
	mustNot(t, panel, "cur.api_key", "the key is never read back into the page")
	must(t, html, "const NC_CONTACT_FIELDS=", "contact fields")

	reg := spaFunctionLine(html, "registerDomainModal")
	for _, needle := range []string{
		"/api/registrars/namecheap/check",
		"/api/registrars/namecheap/register",
		"max_price:total",
		"years:term",
		`id="nr-confirm"`,
		"nc.reg.pricechanged",
		"ncOutcome(e.status,e.body)",
		"checked=false",
	} {
		must(t, reg, needle, "purchase window")
	}
	rec := spaFunctionLine(html, "reconcileDomain")
	must(t, rec, "/api/registrars/namecheap/register", "reconcile uses the register route")
	must(t, rec, "reconcile:true", "reconcile asks the server to settle, never to buy")
	mustNot(t, rec, "max_price", "reconcile carries no price")
	must(t, rec, "DOM_BUSY.add(d.id)", "the menu item is off while the request runs")
	must(t, spaFunctionLine(html, "domains"), "disabled:DOM_BUSY.has(d.id)", "the reconcile item is disabled during the request")
	must(t, spaFunctionLine(html, "forceDeleteDomain"), "?force=1", "forced delete of a pending purchase")
	ns := spaFunctionLine(html, "nsModal")
	must(t, ns, "/ns'", "name server route")
	must(t, ns, "confirm:true", "name server change is confirmed")
	must(t, ns, "nsProblem(list,false)", "2 to 12 name servers")

	dom := spaFunctionLine(html, "domains")
	must(t, dom, "d.registrar==='namecheap'", "name servers only for Namecheap domains")
	must(t, dom, "domainPending(d)", "pending rows have their own actions")
	must(t, dom, "tr('nc.reconcile')", "reconcile action")
	must(t, dom, "tr('nc.force')", "forced delete action")
	must(t, dom, `id="dom-register"`, "register button")
	must(t, dom, "meIsAdmin()", "purchase actions are for administrators")
	must(t, html, "registrar_status==='pending'", "pending status from the domain row")
	must(t, spaFunctionLine(html, "call"), "e.body=j", "the error body reaches the caller")

	for _, forbidden := range []string{"api.cloudflare.com", "cloudflare", "certificate", "setHosts"} {
		mustNot(t, strings.ToLower(reg+panel+ns), strings.ToLower(forbidden), "DNS and certificates stay outside the registrar window")
	}
}

// TestSpaNewKeysAreTranslated checks that every dictionary key added for these screens
// exists in all three interface languages.
func TestSpaNewKeysAreTranslated(t *testing.T) {
	html := spaSource()
	keys := []string{
		"fld.device_model", "cond.model.hint", "cond.sid.hint", "cond.sid.ph", "sid.empty", "sid.add", "chip.empty",
		"sticky.label", "sticky.hint", "sticky.noweight", "share.sum", "share.over", "share.under", "share.block",
		"err.shares", "err.save", "kt.base", "kt.key", "kt.key.saved", "kt.check.hint", "kt.src.file", "kt.src.env",
		"kt.src.none", "kt.err.keyhost", "kt.err.keyreq", "kt.err.keybad", "kt.err.base", "kt.err.manifest",
		"kt.err.unavailable", "kt.err.purgebusy", "dom.pending", "nc.intro", "nc.name", "nc.apiuser", "nc.key",
		"nc.ip", "nc.sandbox", "nc.contacts", "nc.test", "nc.detect", "nc.test.ok", "nc.detect.found",
		"nc.detect.mismatch", "nc.reg.btn", "nc.reg.title", "nc.reg.check", "nc.reg.price", "nc.reg.confirm",
		"nc.reg.pricechanged", "nc.reg.buy", "nc.reconcile", "nc.force", "nc.force.ask", "nc.ns.change",
		"nc.ns.confirm", "nc.ns.few", "nc.ns.many", "nc.err.early", "nc.err.pending", "nc.err.released",
		"nc.err.owned", "nc.err.premium", "nc.err.progress", "nc.err.config", "nc.err.nofile",
		"nc.err.notpending", "nc.err.notfound", "nc.err.ipbad", "nc.err.name", "nc.err.user", "nc.err.clong",
		"nc.err.ccountry", "nc.err.cphone", "nc.err.cemail", "nc.err.creq", "err.sid.long", "err.sid.flag",
		"err.sid.unknown", "sid.block", "spend.from", "spend.to", "spend.byuniq", "spend.empty", "stream.share",
		"s2s.title", "s2s.hint", "s2s.url", "s2s.method", "s2s.status", "s2s.add", "s2s.lead", "s2s.sale",
		"s2s.rejected", "adv.summary", "cols.title", "cols.restore", "dom.groups", "dom.count", "dom.nogroup",
		"dom.group.clear",
	}
	for _, k := range keys {
		if n := strings.Count(html, "'"+k+"':"); n < 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	must(t, html, "'Device model':'Модель устройства'", "ru label of the model condition")
	must(t, html, "'Device model':'Модель пристрою'", "uk label of the model condition")
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, key := range ncErrKeys {
		if !strings.Contains(html, "'"+key+"':") {
			t.Errorf("error key %q has no dictionary entry", key)
		}
		if !strings.Contains(pure, ":'"+key+"'") {
			t.Errorf("error key %q is not mapped from a server text", key)
		}
	}
}

// roleKeys are the dictionary keys of the roles screens and the translated role errors.
var roleKeys = []string{
	"roles.title", "roles.new", "roles.edit", "roles.reload", "roles.reloaded", "roles.name", "roles.sections",
	"roles.hidden", "roles.users", "roles.empty", "roles.delete", "roles.delete.confirm", "roles.saved",
	"users.rolename", "users.role.own", "users.role.lost", "users.role.hint",
	"err.roles.off", "err.roles.bad", "err.user.role", "err.role.inuse", "err.role.unknown", "err.role.admin",
	"err.role.own", "err.role.name", "err.role.exists", "err.role.selected", "err.macro.hidden",
	"err.macro.curly", "err.macro", "err.param.source", "err.param.maps", "err.param.price",
	"err.hidden.filter", "err.access.hid", "err.access.long", "err.forbidden",
	"guard.hint", "src.locked", "src.locked.hint", "err.net.query", "err.net.querybad", "err.stream.frozen",
	"acc.mode.default", "guard.frozen",
}

// TestSpaReportUniqTotal checks that the report shows each row's unique, totals the first-step
// uniques, keeps uniq_first out of the columns, and does not feed report rows into PERF.
func TestSpaReportUniqTotal(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function reportTotals(", "reportTotals is in the pure block")
	must(t, pure, "r.uniq_first!=null?r.uniq_first:r.unique", "the total takes uniq_first, then unique")
	must(t, spaFunctionLine(html, "reportCellText"), "if(id==='uniq') return machine?csvNum(r.unique):fmt(r.unique)", "a row cell shows its own unique")
	total := spaFunctionLine(html, "reportTotalCells")
	must(t, total, "reportTotals(rows)", "the total row is built from the sums")
	must(t, total, "'cr'", "the ratio is computed from the totals")
	paint := spaFunctionLine(html, "paintReport")
	must(t, paint, "reportTotalCells(rows,cols,false,cut)", "the table has a total row")
	must(t, paint, "reportTotalCells(rows,cols,true,cut)", "the CSV has a total row")
	must(t, paint, "id=\"rep-cut\"", "a cut report says so")
	must(t, total, "if(cut||!rows.length) return []", "a cut report has no total row")
	must(t, spaFunctionLine(html, "reportQS"), "qs.set('meta','1')", "the page asks for the truncated flag")
	must(t, spaFunctionLine(html, "setReport"), "reportData(body)", "the answer is read through reportData")
	mustNot(t, html, "REPORT_ROWS=next", "no answer bypasses reportData")
	must(t, paint, "<tfoot>", "the total is a footer row")
	if n := strings.Count(html, "'rep.cut':"); n != 3 {
		t.Errorf("dictionary key rep.cut is defined %d time(s), want one per language", n)
	}
	rep := catalogBranch(html, "report")
	must(t, rep, "['uniq','col.uniq',1]", "the report has a unique column")
	mustNot(t, html, "id:'uniq_first'", "uniq_first is no column")
	mustNot(t, html, "['uniq_first'", "uniq_first is no column")
	mustNot(t, spaFunctionLine(html, "paintReport"), "PERF", "report rows stay out of the performance summary")
	mustNot(t, spaFunctionLine(html, "report"), "perfRows", "report rows stay out of the performance summary")
}

// TestSpaNoBackgroundUISave checks that loading a page never writes the shared /api/ui:
// the model column swap and the flows migration stay in memory, and columns are saved
// only by Apply in the columns window.
func TestSpaNoBackgroundUISave(t *testing.T) {
	html := spaSource()
	load := spaFunctionLine(html, "loadCols")
	mustNot(t, load, "saveCols(", "loadCols does not save")
	mustNot(t, load, "saveUI(", "loadCols does not call the server")
	mustNot(t, load, "localStorage.setItem", "loadCols does not end the migration itself")
	must(t, load, "adoptModelCol(out,modelIsNew)", "the model column swap stays in memory")
	must(t, load, "migrateFlowCols(out)", "the flows migration stays in memory")
	mustNot(t, spaFunctionLine(html, "migrateFlowCols"), "saveCols(", "the migration does not save")
	mustNot(t, spaFunctionLine(html, "adoptModelCol"), "saveCols(", "the swap does not save")
	if n := strings.Count(html, "saveCols("); n != 2 {
		t.Errorf("saveCols is called %d time(s) (declaration and Apply), want 2", n)
	}
	must(t, html, "onclick=async()=>{const saving=saveCols(kind,rows); close(); onApply&&onApply(); if(await saving) endFlowColsMigration(kind)}", "Apply applies at once and ends the migration only after the server stored the columns")
	save := spaFunctionLine(html, "saveUI")
	must(t, save, "return true", "saveUI reports success")
	must(t, save, "toast(e.message,true); return false", "saveUI keeps the toast and reports failure")
	must(t, spaFunctionLine(html, "saveCols"), "return saveUI()", "saveCols hands the result on")
	must(t, spaFunctionLine(html, "saveCols"), "UI.cols[kind]=", "the chosen columns stay in memory when the save fails")

	// The only save that is not a user action is the one-time transfer, and it needs the settings section.
	load2 := spaFunctionLine(html, "loadUI")
	must(t, load2, "uiSeedPlan(UI, sectionAllowed(ME,'settings'))", "the plan knows whether the user may write /api/ui")
	must(t, load2, "if(!plan.seed) return;", "non-empty server prefs always win")
	must(t, load2, "if(!moved.length||!plan.persist) return;", "a user without the right keeps the browser values in memory only")
	i, j, k := strings.Index(load2, "seedUI(UI"), strings.Index(load2, "!plan.persist"), strings.Index(load2, "saveUI()")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Error("loadUI must fill UI first, then stop for a reader, and only then save")
	}
	must(t, spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----"), "function uiSeedPlan(", "uiSeedPlan is in the pure block")
	must(t, load2, "if(await saveUI())", "browser keys are dropped only after a successful save")
	must(t, load2, "localStorage.removeItem(k)", "moved browser keys are removed")
	mustNot(t, load2, "toast(", "loadUI shows no toast of its own")
	must(t, spaFunctionLine(html, "seedUI"), "moved.push", "seedUI reports the moved keys")
	for _, name := range []string{"loadUI", "boot", "normalizeUI", "applyMePrefs", "loadCols"} {
		body := spaFunctionLine(html, name)
		if name != "loadUI" {
			mustNot(t, body, "saveUI(", name+" does not save the shared prefs")
		}
	}
	must(t, spaFunctionLine(html, "endFlowColsMigration"), "FLOW_COLS_MIGRATED", "the flag is set by the explicit save")
}

// TestSpaErrorsTranslated checks the server texts of the registrar, landing, role and network
// handlers against the SPA maps, and that no caller translates twice.
func TestSpaErrorsTranslated(t *testing.T) {
	html := spaSource()
	for _, text := range []string{
		"max_price required", "domain not found", "domains required", "at most 50 domains",
		"bad json", "bad request", "read error", "store unavailable", "internal error", "not found",
		"network read error", "bad id", "bad path", "not a text file", "bad archive", "file too large",
		"landing files unavailable", "entry must be a relative path inside the landing",
		"network offer_query holds a macro of a hidden field", "groups error", "stats error",
		"campaign currency is already set", "interval overlaps", "id required", "group id required",
		"unknown timezone", "bad from", "bad to", "bad time", "end must be after start", "end is in the future",
		"start is older than 21 days", "unknown currency", "amount must be positive", "amount must have at most two decimals",
		"share must not be negative", "duplicate offer url",
		"roles unavailable", "user has a role", "role in use", "unknown role", "role is not allowed for administrators",
		"user with a role has no own access", "security: invalid role name", "security: role already exists",
		"access list too long", "campaign price parameter belongs to a hidden field", "forbidden", "save error",
	} {
		must(t, html, "'"+text+"':'", "server text "+text+" has a key")
	}
	must(t, html, "landing entry (.+?): (.+)", "prefix rule for a macro in a landing entry")
	must(t, html, "unknown (offer|landing|domain|source|campaign): (.+)", "template for an unknown campaign link")
	must(t, html, "alias taken: (.+)", "template for a taken alias")
	must(t, html, "unknown access (section|resource|mode): (.+)", "template for an unknown access value")
	for _, k := range []string{
		"err.net.querymacro", "err.groups", "err.stats", "err.spend.cur", "err.spend.overlap", "err.idreq", "err.groupreq",
		"err.spend.zone", "err.spend.from", "err.spend.to", "err.spend.badtime", "err.spend.order", "err.spend.future",
		"err.spend.old", "err.spend.curbad", "err.spend.dec", "err.share.neg", "err.offer.dup", "err.alias",
		"err.unknown.offer", "err.unknown.landing", "err.unknown.domain", "err.unknown.source", "err.unknown.campaign",
		"err.access.section", "err.access.resource", "err.access.mode",
	} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	must(t, html, "invalid domain: (.+)", "prefix rule for a named invalid domain")
	for _, k := range []string{
		"nc.err.maxprice", "nc.err.domnotfound", "nc.err.domreq", "nc.err.dommany", "nc.err.domainx",
		"err.badreq", "err.read", "err.store", "err.internal", "err.notfound", "err.net.read",
		"err.land.id", "err.land.path", "err.land.text", "err.land.zip", "err.land.big", "err.land.off",
		"err.land.entry", "err.land.entryfile",
	} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	mustNot(t, html, "toast(errText(", "toast translates by itself")
}

// TestSpaEditorKeepsAccess checks that the access editor round-trips through grantRows and
// grantsFromRows, that the dashboard drops cards of hidden dimensions, and that the stream
// editor warns when its output may be frozen.
func TestSpaEditorKeepsAccess(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, name := range []string{"grantRows", "grantsFromRows", "visibleDims", "streamOutputGuarded"} {
		must(t, pure, "function "+name+"(", name+" is in the pure block")
	}
	editor := spaFunctionLine(html, "accessEditorHTML")
	must(t, editor, "grantRows(acc, forRole, USER_ACCESS_RESOURCES, USER_ACCESS_MODES)", "the editor shows the rows of grantRows")
	must(t, editor, `<option value=""`, "a resource without a grant has the default option")
	mustNot(t, editor, "mode:'read',ids:[]", "a missing grant is not turned into read")
	must(t, spaFunctionLine(html, "readAccessEditor"), "grantsFromRows(rows, !!forRole)", "the editor is read through grantsFromRows")
	must(t, spaFunctionLine(html, "dashDims"), "visibleDims(ME, DASH_CLASSIC)", "cards of hidden dimensions are not drawn")
	must(t, spaFunctionLine(html, "dashPanelsAll"), "visibleDims(ME, DASH_CLASSIC)", "hidden dimensions are not offered as panels")
	must(t, spaFunctionLine(html, "paintStream"), "streamOutputGuarded(ME,s)", "the stream editor warns near the destination")
	mustNot(t, spaFunctionLine(html, "streamOutputGuarded"), "macroFields", "the macro table stays on the server")
	cell := spaFunctionLine(html, "logCell")
	must(t, cell, "osBrowserText(ME, x)", "the OS / browser cell leaves hidden parts out")
	must(t, cell, "visitKind(ME, x)", "the visit cell guesses nothing for a hidden uniq")
	mustNot(t, cell, "x.uniq?", "the visit cell does not read uniq itself")
}

// TestSpaTrafficSourceKeys checks that the source editor lists every server-side parameter key.
func TestSpaTrafficSourceKeys(t *testing.T) {
	html := spaSource()
	src := spaBetween(html, "const SRC_KEYS=", ";")
	for _, k := range []string{"site", "ad_campaign_id", "creative_id", "external_id"} {
		must(t, src, "'"+k+"'", "SRC_KEYS includes "+k)
	}
	idxKeyword := strings.Index(src, "'keyword'")
	idxSite := strings.Index(src, "'site'")
	idxSub := strings.Index(src, "Array.from({length:15}")
	if idxKeyword < 0 || idxSite < 0 || idxSub < 0 || !(idxKeyword < idxSite && idxSite < idxSub) {
		t.Fatal("SRC_KEYS order: keyword, extra fields, sub_id_1…")
	}
	open := spaBetween(html, "const openSrc=(draft)=>{", "const mkSrc=")
	must(t, open, "SRC_KEYS.includes(k)) return", "save keeps parameters outside the form")
}

// TestSpaGuardedLinks checks that the source and campaign forms lock the parameters of hidden
// fields and that the link editors warn; the server stays the judge of what is refused.
func TestSpaGuardedLinks(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, name := range []string{"sourceKeyLocked", "priceParamLocked", "linksGuarded"} {
		must(t, pure, "function "+name+"(", name+" is in the pure block")
	}
	src := spaBetween(html, "const openSrc=(draft)=>{", "const mkSrc=")
	must(t, src, "sourceKeyLocked(ME,k)?` disabled", "a hidden keyword or Sub ID has a disabled parameter")
	must(t, src, "el.disabled?draft.parameters[el.dataset.src]", "a disabled parameter sends the saved value")
	camp := spaFunctionLine(html, "campaignParamHTML")
	must(t, camp, "priceParamLocked(ME)", "price and currency parameters are locked with cost hidden")
	flow := spaFunctionLine(html, "collectFlow")
	must(t, flow, "!v('g_cost_param').disabled", "a locked price parameter is not read back")
	must(t, flow, "!v('g_currency_param').disabled", "a locked currency parameter is not read back")
	must(t, spaFunctionLine(html, "openStream"), "${guardHint()}", "the stream editor warns")
	must(t, spaFunctionLine(html, "openFlowSettings"), "guardHint(true)", "the campaign links warn")
	must(t, spaFunctionLine(html, "guardHint"), "linksGuarded(ME)", "the warning only for a guarded user")
	mustNot(t, spaFunctionLine(html, "guardHint"), "macroFields", "the macro rules stay on the server")
}

// TestSpaRoleKeys checks that every role key exists in the three languages and that the
// server texts of roles, hidden fields and macros reach their key through the pure block.
func TestSpaRoleKeys(t *testing.T) {
	html := spaSource()
	for _, k := range roleKeys {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, k := range roleKeys {
		if strings.HasPrefix(k, "err.") && !strings.Contains(pure, "'"+k+"'") {
			t.Errorf("error key %q is not mapped from a server text", k)
		}
	}
	for _, text := range []string{"roles unavailable", "user has a role", "role in use", "unknown role"} {
		must(t, pure, "'"+text+"':", "fixed server text "+text)
	}
	must(t, pure, "may read a hidden field", "named macro text")
	must(t, pure, "uses hidden field", "hidden macro text")
	must(t, spaFunctionLine(html, "toast"), "errText(msg)", "failed toasts are translated")
}

// TestSpaRolesUI checks the roles editor: it shares the access editor with the user form,
// sends no record lists, lists only hideable fields and reports an unavailable store.
func TestSpaRolesUI(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, name := range []string{"hiddenFields", "columnHidden", "offerPayoutHidden", "accessModes", "grantBody", "hideChoices", "userRoleField", "roleChoices", "roleNameOf"} {
		must(t, pure, "function "+name+"(", name+" is in the pure block")
	}
	role := spaFunctionLine(html, "openRoleEditor")
	access := spaFunctionLine(html, "openUserAccess")
	must(t, role, "accessEditorHTML(r.access,{forRole:true})", "the role editor builds the shared editor for a role")
	must(t, role, "readAccessEditor(box, true)", "the role editor reads the shared editor")
	must(t, role, "'/api/roles/'", "the role editor edits by id")
	must(t, role, "call('POST','/api/roles'", "the role editor creates")
	must(t, access, "accessEditorHTML(acc,{catalog})", "the user editor builds the shared editor")
	must(t, access, "readAccessEditor(box, false)", "the user editor reads the shared editor")
	must(t, access, "if(u.role_id)", "a user with a role has no own access to edit")
	must(t, spaFunctionLine(html, "accessEditorHTML"), "hideChoices(FIELDS&&FIELDS.fields, hid)", "hidden fields come from the field catalog")
	must(t, spaFunctionLine(html, "deleteRole"), "'DELETE','/api/roles/'", "roles are deleted")
	must(t, spaFunctionLine(html, "reloadRoles"), "'POST','/api/roles/reload'", "roles are re-read")
	must(t, spaFunctionLine(html, "loadRoles"), "errText(e)", "an unavailable store gives a translated reason")
	must(t, spaFunctionLine(html, "paintRolesTable"), "stateHTML(err||tr('ui.fail'),'err')", "an unavailable store shows its reason")
	page := spaFunctionLine(html, "usersPage")
	must(t, page, "id=\"roles-table\"", "the users page holds the roles table")
	must(t, page, "'/api/users'", "the users page lists users")
	form := spaFunctionLine(html, "userFormHTML")
	must(t, form, `id="u-role"`, "the user form has a role choice")
	must(t, form, "tr('users.role.own')", "an empty role means own access")
	read := spaFunctionLine(html, "readUserForm")
	must(t, read, "userRoleField(", "the role goes through userRoleField")
	mustNot(t, read, "access", "the user form never sends access")
	must(t, spaFunctionLine(html, "paintUsersTable"), "userRoleLabel(u)", "the users table names the role")
	must(t, spaFunctionLine(html, "paintUsersTable"), "!u.role_id", "a user with a role has no own access item")
}

// TestSpaHiddenForRole checks that the pages react to the hidden list of /api/me:
// columns, the offer payout, the recent clicks and the chart point tip.
func TestSpaHiddenForRole(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "colShown"), "columnHidden(ME, names)", "journal columns of a hidden field are dropped")
	offers := spaBetween(html, "const payHidden=offerPayoutHidden(ME);", "const picked=[];")
	if offers == "" {
		t.Fatal("offer form payout switch not found")
	}
	must(t, html, "${payHidden?'':`<div class=\"g2\"><div class=\"fld\"><label>${tr('off.payout')}", "payout fields are left out when hidden")
	must(t, html, "if(!payHidden){", "payout is sent only when it was shown")
	mustNot(t, html, "payout:payRaw>0?payRaw:0, payout_currency:q('#ocur',box).value, payout_param", "payout is not part of the fixed body")
	must(t, spaFunctionLine(html, "renderPerf"), "shown('revenue'", "the performance table drops a hidden revenue column")
	must(t, spaFunctionLine(html, "recentRowHTML"), "hiddenFields(ME)", "recent clicks drop hidden fields")
	must(t, spaFunctionLine(html, "paintFlowSum"), "hs('sales'", "the flows summary drops hidden metrics")
	tip := spaFunctionLine(html, "chartTip")
	must(t, tip, "pointMoney(p, money, cur, ready)", "the tip takes the spend of the point")
	mustNot(t, tip, "periodSpendBy", "the tip never borrows the period spend for a point")
	mustNot(t, tip, "moneyMap(", "the tip writes no period spend list")
	must(t, spaFunctionLine(html, "annotateChart"), "pointSpendBy(spend.rows, p.cost, +start, +end)", "every point gets its own spend")
}

// ncErrKeys are the dictionary keys the Namecheap server texts map to.
var ncErrKeys = []string{
	"nc.err.progress", "nc.err.exists", "nc.err.unavailable", "nc.err.priceunknown", "nc.err.premium",
	"nc.err.config", "nc.err.nofile", "nc.err.domain", "nc.err.years", "nc.err.pending", "nc.err.released",
	"nc.err.owned", "nc.err.unconfirmed", "nc.err.notsaved", "nc.err.changed", "nc.err.notours",
	"nc.err.confirm", "nc.err.ipreq", "nc.err.userkey", "nc.err.notpending", "nc.err.notfound", "nc.err.ipbad",
	"nc.err.name", "nc.err.user", "nc.err.clong", "nc.err.ccountry", "nc.err.cphone", "nc.err.cemail",
}

// TestSpaMoneyUnified checks that every amount goes through moneyText: an empty currency code is
// USD and written as a dollar sign, the old no-currency helpers are gone, and CSV carries plain
// numbers with the currency in the column title.
func TestSpaMoneyUnified(t *testing.T) {
	html := spaSource()
	for _, gone := range []string{"function bareMoney(", "function periodMoney(", "function chartAmt(", "function kpiMoney("} {
		mustNot(t, html, gone, "the helper is replaced by moneyText")
	}
	mustNot(t, html, "money.nocur", "there is no no-currency label")
	if m := regexp.MustCompile(`moneyText\([^)]*,\s*''\)`).FindString(html); m != "" {
		t.Errorf("moneyText is called with an empty code: %s", m)
	}
	must(t, spaFunctionLine(html, "curKey"), "return c||'USD'", "an empty code is USD")
	for _, fn := range []string{"clickCostBy", "addBy", "foldCost", "campaignCostOnly", "periodSpendBy", "openSpend"} {
		must(t, spaFunctionLine(html, fn), "curKey(", fn+" keys its map by curKey")
	}
	for _, fn := range []string{"moneyText", "moneyMap", "moneyOf", "profitOf", "cpcCell", "offerCells", "flowMetricCell", "reportCellText", "exportPerf", "perfSheet"} {
		src := spaFunctionLine(html, fn)
		if src == "" {
			t.Fatalf("%s not found in SPA", fn)
		}
		mustNot(t, src, ".toFixed(2)", fn+" formats amounts through moneyText, toFixed is for CSV only")
	}
	must(t, spaFunctionLine(html, "csvMoney"), ".toFixed(2)", "CSV amounts are plain two-decimal numbers")
	for _, fn := range []string{"profitOf", "moneyOf", "cpcCell", "moneyMap"} {
		must(t, spaFunctionLine(html, fn), "moneyText(", fn+" writes amounts with moneyText")
	}
	for _, needle := range []string{"moneyOf(", "moneyMap(", "profitOf(", "cpcCell("} {
		must(t, spaFunctionLine(html, "offerCells"), needle, "offer and network money cells")
	}
	cell := spaFunctionLine(html, "flowMetricCell")
	for _, needle := range []string{"moneyOf(", "profitOf(", "campaignMoney(", "cpcCell(", "csvMoney("} {
		must(t, cell, needle, "campaign money cells")
	}
	must(t, spaFunctionLine(html, "reportCellText"), "moneyText(r.profit||0, 'USD', 2)", "report revenue on screen")
	must(t, spaFunctionLine(html, "reportCellText"), "csvMoney(r.profit)", "report revenue in CSV")
	must(t, spaFunctionLine(html, "exportFlows"), "curSign(cc.code)", "campaign CSV titles carry the currency")
	must(t, spaFunctionLine(html, "exportPerf"), "col.revenue", "performance CSV title says revenue, not profit")
	mustNot(t, spaFunctionLine(html, "exportPerf"), "tr('Profit')", "performance CSV does not call revenue profit")
	must(t, html, "td.num,.sumi b,.kpi .vn{white-space:nowrap}", "an amount does not wrap")
	prices := regexp.MustCompile(`'nc\.reg\.(confirm|pricechanged|over|price)':'([^']*)'`).FindAllStringSubmatch(html, -1)
	if len(prices) != 12 {
		t.Fatalf("want 4 purchase texts in 3 languages, found %d", len(prices))
	}
	for _, p := range prices {
		if strings.Contains(p[2], "USD") || strings.Contains(p[2], "{cur}") {
			t.Errorf("nc.reg.%s still names the currency: %s", p[1], p[2])
		}
		if p[1] == "confirm" && !strings.Contains(p[2], "{domain}") {
			t.Errorf("nc.reg.confirm lacks {domain}: %s", p[2])
		}
	}
}

// TestSpaUniqueFirst checks that a campaign counts uniq_first and the other tables count uniq.
func TestSpaUniqueFirst(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "campaignMetrics"), "sumClicks(idx.clicks.get(id)||[], true)", "a campaign counts each visitor once")
	mustNot(t, spaFunctionLine(html, "offerMetrics"), ", true)", "an offer counts uniq")
	mustNot(t, spaFunctionLine(html, "networkMetrics"), ", true)", "a network counts uniq")
	must(t, spaFunctionLine(html, "sumClicks"), "r.uniq_first!=null", "uniq_first is used where a row has it")
	must(t, html, "['uniq','col.uniq',0],['uepc','col.uepc',0]", "networks can show unique visitors")
}

// TestSpaRevenueAlwaysUSD checks that revenue and payouts are written in dollars whatever the
// currency of the spend, and that spend in another currency is listed beside revenue, not subtracted.
func TestSpaRevenueAlwaysUSD(t *testing.T) {
	html := spaSource()
	mustNot(t, spaFunctionLine(html, "moneyOf"), "costCode(", "revenue does not take the currency of the spend")
	must(t, spaFunctionLine(html, "moneyOf"), "'USD'", "revenue is in dollars")
	profit := spaFunctionLine(html, "profitOf")
	must(t, profit, "spendBasis(", "profit asks whether the spend is in USD")
	must(t, profit, "profitList(", "spend in another currency is listed beside revenue")
	mustNot(t, profit, "cc.code", "profit does not take the currency of the spend")
	must(t, spaFunctionLine(html, "metricROI"), "nonzero[0]!=='USD'", "ROI is not computed against spend in another currency")
	must(t, spaFunctionLine(html, "packMetrics"), "spendBasis(costBy)", "campaign profit subtracts USD spend only")
	must(t, spaFunctionLine(html, "annotateChart"), "spendBasis(spend.by).mixed", "the money axis is off while spend is in another currency")
	must(t, spaFunctionLine(html, "chartTip"), "moneyText(revN, 'USD', 2)", "the tooltip writes revenue in dollars")
	mustNot(t, html, "moneyOf(cm.rev, cm.costBy, ready, 2, true)", "no spend list in revenue cells")
	cell := spaFunctionLine(html, "flowMetricCell")
	must(t, cell, "usdSpent", "CSV profit subtracts USD spend only")
	exp := spaFunctionLine(html, "exportFlows")
	must(t, exp, "FLOW_USD_COLS.has(c.id)", "revenue titles always carry $")
	must(t, exp, "curSign('USD')", "revenue title is in dollars")
	must(t, html, "<span>${tr('col.revenue')}</span></div></div>", "flow cards label revenue as revenue")
	mustNot(t, html, "moneyText(p.profit||0,'USD')}</b><span>${tr('flows.profit')}", "flow cards do not call revenue profit")
}
