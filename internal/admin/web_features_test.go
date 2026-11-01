package admin

import (
	"regexp"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
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

// TestBotConditionSwitch checks that the stream bot filter is a switch, and the other
// three-way filters stay selects. Values stay 0 off, 1 bots, 2 humans.
func TestBotConditionSwitch(t *testing.T) {
	html := spaSource()
	row := spaFunctionLine(html, "condRow")
	must(t, row, `id="r_bot"`, "bot condition has a switch")
	must(t, row, `class="sw"`, "bot condition uses the switch control")
	must(t, row, `data-mode=`, "the switch keeps any, yes and no")
	must(t, row, `sel('r_'+c.k`, "other three-way conditions stay a select")
	paint := spaFunctionLine(html, "paintStream")
	must(t, paint, "if(!cur.rules.bot) cur.rules.bot=1", "a new bot condition starts on")
	must(t, paint, "botSw.dataset.mode", "the switch writes its mode")
	collect := spaFunctionLine(html, "collectStream")
	must(t, collect, "el.dataset.mode", "saving the stream reads the switch mode")
	mustNot(t, collect, `+val('r_bot')`, "the bot value is not read as a select")
}

// TestSpaModelAndSubIDs checks the device model and Sub ID conditions, the model column
// of the click and conversion logs and the filter group.
func TestSpaModelAndSubIDs(t *testing.T) {
	html := spaSource()
	must(t, html, "{k:'model',l:'Device model',g:'Device',kind:'list'", "model condition in the Device group")
	must(t, html, "{k:'sub_ids',l:'Sub ID',g:'Source',kind:'subids'", "Sub ID condition in the Source group")
	must(t, html, "const SUBID_MAX=30", "Sub ID limit")
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
		`data-kttab="kt-conn"`,
		`id="kt-conn"`,
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
		`data-nctab="account"`,
		`data-nctab="contacts"`,
		"nc.tab.account",
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

	for _, forbidden := range []string{"api.cloudflare.com", "certificate", "setHosts"} {
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
		"dom.group.clear", "err.network", "board.main", "board.switch", "board.create", "board.import", "board.empty",
		"board.page.title", "board.add", "board.icon", "board.name", "board.id", "board.delete", "board.delete.confirm", "board.inuse", "board.saved",
		"kt.stage.landing_files", "kt.stage.lists", "kt.stage.pages", "kt.stage.users",
		"cf.intro", "cf.token", "cf.account", "cf.ip", "cf.ip.empty", "cf.test", "cf.test.ok", "cf.test.fail",
		"cf.file.err", "cf.err.load", "cf.reg.use", "cf.reg.err", "cf.reg.hosterr", "cf.reg.manual", "cf.buy.title",
		"cf.buy.sum", "cf.dns.cf", "cf.dns.ns", "cf.dns.a", "cf.dns.none", "dom.state.on", "dom.state.off",
		"dom.state.pending", "dom.elsewhere", "dom.elsewhere.short", "dom.cfpending", "dom.cfpending.short",
		"dom.tls", "cf.connecting", "cf.connect", "cf.connect.title", "cf.connect.ask", "cf.connect.ask.nc",
		"cf.connect.done", "cf.connect.set", "cf.ns.title", "cf.ns.hint", "cf.ns.copy", "cf.ns.copied",
		"cf.ns.select", "dom.conn.title", "dom.conn.title.many", "dom.conn.text", "dom.conn.ok", "dom.conn.go",
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
	must(t, spaFunctionLine(html, "saveCols"), "flushPageViews()", "saveCols sends the account views at once")
	mustNot(t, spaFunctionLine(html, "saveCols"), "UI.cols", "columns no longer go to the shared /api/ui")
	mustNot(t, spaFunctionLine(html, "saveCols"), "saveUI(", "columns no longer go to the shared /api/ui")
	must(t, spaFunctionLine(html, "saveCols"), "VIEW_COLS[kind]=", "the chosen columns stay in memory when the save fails")
	must(t, spaFunctionLine(html, "loadCols"), "VIEW_COLS[kind]", "the account's columns come first")
	must(t, spaFunctionLine(html, "loadCols"), "UI.cols[kind]", "the shared prefs are the fallback")

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
	dashPanels := spaFunctionLine(html, "dashboard")
	must(t, dashPanels, "DASH_VIEW.panels", "dashboard panels go to the account snapshot")
	mustNot(t, dashPanels, "saveUI(", "dashboard panels gear does not write shared ui.json")
	must(t, dashPanels, "flushPageViews()", "dashboard panels gear saves account views")
	must(t, spaFunctionLine(html, "cronOrderSave"), "flushPageViews()", "cron order saves to the account")
	must(t, spaFunctionLine(html, "moneyCellCls"), "cpcHasSpend", "money tone uses spend map")
	domAdd := spaBetween(html, "const openDomainAdd=(group)=>{", "const openDomainEdit=(d)=>{")
	mustNot(t, domAdd, "stackedPrompt", "domain group create is inline")
	must(t, domAdd, "bindGroupInlineCreate", "domain add uses inline group create")
	for _, lang := range []string{"en", "ru", "uk"} {
		if !trKeyDefined(html, lang, "action.done") {
			t.Errorf("missing action.done in %s", lang)
		}
	}
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
	idxSub := strings.Index(src, "Array.from({length:SUBID_MAX}")
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

// TestCloudflareCardUI checks the Cloudflare card on the integrations page: its routes, the
// token field that is never filled in, the public IP hint and the check result.
func TestCloudflareCardUI(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "integrations"), `id="int-cloudflare"`, "Cloudflare card on the integrations page")
	must(t, spaFunctionLine(html, "integrations"), "cloudflarePanel(", "the card is painted with the others")
	panel := spaFunctionLine(html, "cloudflarePanel")
	for _, needle := range []string{
		"get('/api/cloudflare'", "call('PUT','/api/cloudflare'", "call('POST','/api/cloudflare/test'",
		`id="cf-token"`, `type="password"`, `autocomplete="new-password"`, "cur.has_token", `id="cf-account"`,
		`id="cf-ip"`, " readonly", "cur.public_ip", "tr('cf.ip.empty')", `id="cf-save"`, `id="cf-test"`,
		"trFill('cf.test.ok',{n:r.zones})", "r.zones", "r.error",
	} {
		must(t, panel, needle, "Cloudflare card")
	}
	mustNot(t, panel, "cur.token", "the token is never read back into the page")
}

// TestCloudflarePurchaseAndRow checks the purchase window (Cloudflare box, confirmation before the
// request, follow-up check) and the row action that connects a domain.
func TestCloudflarePurchaseAndRow(t *testing.T) {
	html := spaSource()
	reg := spaFunctionLine(html, "registerDomainModal")
	for _, needle := range []string{
		"cfInfo(true)", "cf.configured&&cf.public_ip", `id="nr-cf"`, `id="nr-ns-box"`, "body.cloudflare=true",
		"cf.reg.use", "res.cloudflare_error", "res.hosts_error", `id="nr-confirm"`, "checkDomain(fresh",
	} {
		must(t, reg, needle, "purchase window")
	}
	askAt := strings.Index(reg, "await ask(")
	postAt := strings.Index(reg, "call('POST','/api/registrars/namecheap/register',body)")
	if askAt < 0 || postAt < 0 || askAt > postAt {
		t.Errorf("the purchase must be confirmed with ask( before the request (ask at %d, request at %d)", askAt, postAt)
	}
	must(t, reg, "return 'keep'", "a declined confirmation keeps the window open")
	must(t, spaFunctionLine(html, "formModal"), "res==='keep'", "formModal leaves the window open on keep")
	must(t, html, "const m=formModal(tr('nc.reg.title')", "purchase window is a form modal")
	if strings.Contains(reg, "body.nameservers=ns") && !strings.Contains(reg, "else if(ns.length) body.nameservers=ns") {
		t.Error("nameservers must not be sent together with cloudflare")
	}

	conn := spaFunctionLine(html, "connectCloudflare")
	must(t, conn, "'/api/domains/'+encodeURIComponent(d.id)+'/cloudflare'", "row action route")
	must(t, conn, "{confirm:true}", "the connection is confirmed")
	askAt = strings.Index(conn, "await ask(")
	postAt = strings.Index(conn, "call('POST'")
	if askAt < 0 || postAt < 0 || askAt > postAt {
		t.Error("the row action must ask before it posts")
	}
	must(t, conn, "res.ns_manual", "manual name servers are shown")
	must(t, conn, "nsListModal(", "manual name server list")
	dom := spaFunctionLine(html, "domains")
	must(t, dom, "connectCloudflare(d,paintTable)", "row menu action")
	must(t, dom, "d.cf_status==='active'&&d.ok", "the action is hidden for a connected domain")
	must(t, dom, "cfInfo()", "public IP is read once per page")
}

// TestDomainStatusTexts checks the state chip and the check verdict phrases for the new reasons.
func TestDomainStatusTexts(t *testing.T) {
	html := spaSource()
	text := spaFunctionLine(html, "domainStatusText")
	for _, needle := range []string{
		"d.reason==='tls'", "d.reason==='cf_pending'", "d.reason==='elsewhere'", "d.cf_status==='connect_pending'",
		"tr('cf.connecting')", "tr('dom.tls')", "dom.cfpending", "dom.elsewhere", "CF_INFO.public_ip", "d.reason_detail",
	} {
		must(t, text, needle, "domain verdict")
	}
	chip := spaFunctionLine(html, "domainStateChip")
	for _, key := range []string{"dom.state.on", "dom.state.off", "dom.state.pending"} {
		must(t, chip, "tr('"+key+"')", "state chip")
	}
	must(t, spaFunctionLine(html, "checkDomain"), "applyDomainRow(d, res)", "a check applies the whole row")
	must(t, spaFunctionLine(html, "applyDomainRow"), "t.reason_detail=''", "an omitted detail does not keep the old value")
	must(t, html, `'dom.elsewhere':'Не направлен: A указывает на {ips}, нужен {ip}'`, "ru elsewhere text")
	must(t, html, `'dom.cfpending':'Ждём Cloudflare: NS {ns}'`, "ru Cloudflare wait text")
	must(t, html, `'dom.tls':'Сертификат сервера отклонён'`, "ru certificate text")
}

// TestSpaAdminHost checks the admin.<domain> badge, DNS action, form hints and the links default role.
func TestSpaAdminHost(t *testing.T) {
	html := spaSource()
	for _, key := range []string{
		"dom.adminhost.label", "dom.adminhost.hint", "dom.adminhost.create", "dom.adminhost.created",
		"dom.adminhost.unchecked", "dom.adminhost.why.dns", "dom.adminhost.why.foreign",
		"dom.adminhost.why.tls", "dom.adminhost.why.connect",
		"dom.adminhost.dns.nozone", "dom.adminhost.dns.noroot", "dom.adminhost.dns.cname",
		"dom.adminhost.dns.many", "dom.adminhost.dns.foreign",
	} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("missing %s in %s", key, lang)
			}
		}
	}
	badge := spaFunctionLine(html, "domainAdminHostHTML")
	for _, needle := range []string{
		"d.admin_host", "d.admin_ok", "adminHostDnsText(d)", "adminHostStatusText(", "adminHostPillClass(",
		"trFill('dom.adminhost.label'", "https://${esc(host)}/", "esc(note)",
	} {
		must(t, badge, needle, "admin host badge")
	}
	must(t, html, "'admin record points to another server':'dom.adminhost.dns.foreign'", "foreign admin_dns maps to tr key")
	status := spaFunctionLine(html, "adminHostStatusText")
	for _, needle := range []string{
		"adminHostDnsText(d)", "d.admin_ok", "d.admin_reason||''", "r==='dns'", "r==='foreign'", "r==='tls'", "r==='connect'",
		"dom.adminhost.why.dns", "dom.adminhost.why.foreign", "dom.adminhost.why.tls", "dom.adminhost.why.connect",
		"dom.adminhost.unchecked", "tr('dom.ok')",
	} {
		must(t, status, needle, "admin host status")
	}
	pill := spaFunctionLine(html, "adminHostPillClass")
	must(t, pill, "d.admin_reason", "pill reads admin_reason")
	must(t, pill, "['dns','connect','foreign','tls'].includes(d.admin_reason)", "pill marks probe failures bad")
	dns := spaFunctionLine(html, "ensureAdminDNS")
	must(t, dns, "'/api/domains/'+encodeURIComponent(d.id)+'/admin-dns'", "admin DNS route")
	must(t, dns, "applyDomainRow(d, res)", "admin DNS refreshes the row")
	must(t, dns, "toast(", "admin DNS errors use toast")
	must(t, dns, "await ensureDomains()", "admin DNS failure reloads catalog")
	apply := spaFunctionLine(html, "applyDomainRow")
	must(t, apply, "DOMAIN_ROW_KEEP", "computed row fields are kept when omitted")
	must(t, html, "DOMAIN_ROW_KEEP=['admin_host','campaigns']", "admin_host and campaigns are kept")
	mustNot(t, apply, "t.admin_host=''", "admin_host is not cleared before merge")
	must(t, spaFunctionLine(html, "saveDomain"), "domainPatchNeedsCatalog(p)", "primary or role reloads the catalog")
	must(t, spaFunctionLine(html, "saveDomain"), "await ensureDomains()", "catalog reload after primary or role")
	must(t, spaFunctionLine(html, "saveDomain"), "scheduleDomainCatalogRefresh()", "delayed catalog refresh after primary or role")
	must(t, spaFunctionLine(html, "scheduleDomainCatalogRefresh"), "4000", "delayed refresh waits for DNS task")
	must(t, spaFunctionLine(html, "adminHostDnsText"), "ADMIN_DNS_ERR", "admin_dns translation map")
	must(t, spaFunctionLine(html, "domainAdminHostHTML"), "if(!host) return `<span class=\"dom-verdict\">${esc(note)}</span>`", "zone root note without admin_host")
	dom := spaFunctionLine(html, "domains")
	must(t, dom, "window.__domCatalogPaint=paintTable", "catalog repaint hook for delayed refresh")
	must(t, dom, "ensureAdminDNS(d,paintTable)", "row menu admin DNS action")
	must(t, dom, "d.admin_host&&(d.admin_dns!=='ok'||!d.admin_ok)", "admin DNS action when record missing or probe failed")
	must(t, dom, "syncDomAdminHints(", "domain form admin hints")
	must(t, dom, "opt('links',tr('dom.role.links'),'links')", "add form default role links")
	must(t, dom, "d.role||'links'", "edit form default role links")
	must(t, spaFunctionLine(html, "domainPayload"), "role:n.role||'links'", "save payload default role links")
	must(t, spaFunctionLine(html, "domainCell"), "domainAdminHostHTML(d)", "host cell shows admin badge")
}

// TestSpaAutomationLayout checks automation masonry, card headers, arrange mode and keitaro hints.
func TestSpaAutomationLayout(t *testing.T) {
	html := spaSource()
	must(t, html, "column-count:2", "automation two-column masonry")
	must(t, html, `id="cron-arrange"`, "arrange settings control")
	must(t, html, "hd-act", "job header actions wrapper")
	must(t, html, "function cronCardOrder(", "cron card order helper")
	must(t, html, "kuztds-cron-order", "cron order storage key")
	for _, key := range []string{"cron.arrange", "cron.arrange.done", "cron.arrange.reset", "cron.arrange.hint"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("missing %s in %s", key, lang)
			}
		}
	}
	syncFn := spaFunctionLine(html, "keitaroSyncCursorsHTML")
	mustNot(t, syncFn, "last_clicks", "keitaro sync card does not repeat last run counts")
	mustNot(t, syncFn, "cron.ktsync.last", "keitaro sync card does not repeat last run message")
}

// TestSpaKeitaroCron checks the Keitaro catch-up card on automation and import dock behaviour.
func TestSpaKeitaroCron(t *testing.T) {
	html := spaSource()
	for _, key := range []string{
		"cron.ktsync.title", "cron.ktsync.hint", "cron.ktsync.off", "cron.ktsync.needkt",
		"cron.ktsync.needimport", "cron.ktsync.noconv", "cron.ktsync.cursor.clicks", "cron.ktsync.cursor.convs",
		"cron.ktsync.lag", "cron.ktsync.last", "kt.sync.requested", "kt.sync.busy",
	} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("missing %s in %s", key, lang)
			}
		}
	}
	must(t, html, "keitaro_sync:['cron.ktsync.title','cron.ktsync.hint']", "JOBINFO keitaro_sync")
	must(t, spaFunctionLine(html, "cronJobDisabled"), "job==='keitaro_sync'", "keitaro toggle guard")
	must(t, spaFunctionLine(html, "cronJobDisabled"), "job==='conversions'", "conversions toggle guard")
	must(t, spaFunctionLine(html, "cronFloor"), "CRONST.floors", "interval floor from API")
	must(t, spaFunctionLine(html, "cronApplyInterval"), "cronFloor(job)", "clamp interval to floor")
	must(t, spaFunctionLine(html, "cronJobCardHTML"), "cronJobDisabled(job, CRONST)", "automation disables jobs")
	must(t, spaFunctionLine(html, "paintAutomation"), "cronApplyInterval(j)", "all jobs clamped on paint")
	must(t, spaFunctionLine(html, "jobBody"), "case 'keitaro_sync'", "keitaro_sync card body")
	must(t, spaFunctionLine(html, "jobBody"), "keitaroSyncNeedImport(CRONST)", "full import banner")
	must(t, spaFunctionLine(html, "jobBody"), "cron.ktsync.noconv", "conversions hint without telegram")
	must(t, spaFunctionLine(html, "cronRunToastText"), "errInfo", "run-now errors translated")
	must(t, html, "'идёт актуализация':'kt.sync.busy'", "sync busy server text")
	must(t, spaFunctionLine(html, "ktDockTick"), "!job.sync_running", "import dock ignores sync_running")
	must(t, spaFunctionLine(html, "ktDockPaint"), "!j.sync_running", "import progress ignores sync_running")
}

// TestSpaPageViews checks server-backed page views: load route, debounced save hooks, no range in localStorage.
func TestSpaPageViews(t *testing.T) {
	html := spaSource()
	must(t, html, "'/api/me/views'", "views API route in SPA")
	must(t, spaFunctionLine(html, "loadUserPageViews"), "'/api/me/views'", "views load after login")
	must(t, spaFunctionLine(html, "flushPageViews"), "call('PUT','/api/me/views'", "flush sends the PUT")
	must(t, spaFunctionLine(html, "flushPageViews"), "PAGE_VIEWS_LAST_SENT=json", "a failed PUT is not remembered as sent")
	must(t, spaFunctionLine(html, "touchPageViews"), "flushPageViews()", "touch is a debounced flush")
	mustNot(t, spaFunctionLine(html, "touchPageViews"), "call('PUT'", "touch does not send by itself")
	must(t, spaFunctionLine(html, "bindSort"), "flushPageViews()", "sort clicks are sent at once")
	must(t, spaFunctionLine(html, "sendPageViewsOnHide"), "keepalive:true", "a closing tab still sends the snapshot")
	must(t, spaFunctionLine(html, "sendPageViewsOnHide"), "method:'PUT'", "the hide send is a PUT, not a beacon")
	must(t, spaFunctionLine(html, "sendPageViewsOnHide"), "'X-CSRF-Token':CSRF", "the hide send carries the CSRF header")
	must(t, html, "addEventListener('pagehide', sendPageViewsOnHide)", "pagehide sends views")
	must(t, html, "document.visibilityState==='hidden'", "hidden tab sends views")
	pull := spaFunctionLine(html, "pullPageViews")
	must(t, pull, "PAGE_VIEWS_QUEUE.then(", "the re-read runs after the queued PUT requests")
	must(t, pull, "call('GET','/api/me/views')", "the views are read again")
	must(t, pull, "pageViewsPull(", "the pure rule decides whether to apply")
	must(t, pull, "PAGE_VIEWS_PULL_GAP", "re-reads are rate limited")
	must(t, pull, "PAGE_VIEWS_TIMER", "unsent local changes block the re-read")
	must(t, pull, "pageViewsRepaintable()", "the page is redrawn only when that is safe")
	must(t, spaFunctionLine(html, "pageViewsRepaintable"), "anyDirty()", "unsaved edits block the redraw")
	must(t, spaFunctionLine(html, "pageViewsRepaintable"), "r.page==='flows'&&r.arg", "the campaign editor is not redrawn")
	must(t, spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----"), "function pageViewsPull(", "pageViewsPull is in the pure block")
	must(t, html, "else pullPageViews();", "a visible tab re-reads the views")
	must(t, html, "addEventListener('focus', pullPageViews)", "window focus re-reads the views")
	logout := spaFunctionLine(html, "logout")
	must(t, logout, "await flushPageViews()", "logout sends the pending views first")
	if i, j := strings.Index(logout, "flushPageViews()"), strings.Index(logout, "'/api/logout'"); i < 0 || j < 0 || i > j {
		t.Error("logout must flush the views before POST /api/logout")
	}
	for _, name := range []string{"loadLogs", "loadPostbacks"} {
		body := spaFunctionLine(html, name)
		mustNot(t, body, "_SORT={", name+" keeps the sort object and the saved sort")
		must(t, body, "(true)", name+" retries without the sort")
	}
	mustNot(t, html, "PSORT={k:key", "the summary sort is changed in place")
	for _, key := range []string{"FLOW_VIEW_KEY", "FLOW_PAGE_KEY", "FLOW_COMPACT_KEY", "LOG_PAGE_KEY", "PB_PAGE_KEY", "DOM_PAGE_KEY"} {
		mustNot(t, html, "localStorage.setItem("+key, key+" is no longer written")
	}
	mustNot(t, spaFunctionLine(html, "paintServerPager"), "localStorage", "the pager saves the size with the account")
	must(t, spaFunctionLine(html, "paintServerPager"), "touchPageViews()", "a new page size is sent")
	must(t, spaFunctionLine(html, "pageViewLegacy"), "FLOW_PAGE_KEY", "browser sizes seed an account without them")
	must(t, spaFunctionLine(html, "applyRange"), "touchPageViews()", "period changes touch views")
	mustNot(t, spaFunctionLine(html, "applyRange"), "localStorage.setItem(RANGE_KEY", "range is not stored locally")
	must(t, html, "LOGF.conds=[]", "logs reset clears filters explicitly")
	mustNot(t, spaFunctionLine(html, "logs"), "if(!hold){LOG_PAGE_I=0; LOGF={conds:[]}", "logs keep filters on normal entry")
	mustNot(t, spaFunctionLine(html, "conversions"), "if(!hold){PB={conds:[]", "conversions keep filters on normal entry")
	mustNot(t, spaFunctionLine(html, "report"), "if(!hold) REP={conds:[]", "report keeps filters on normal entry")
}

// TestConnectedPopup checks the popup that reports newly connected domains: one timer per tab,
// the acknowledge routes and the stop on logout.
func TestConnectedPopup(t *testing.T) {
	html := spaSource()
	must(t, html, "const CONN_EVERY=5*60*1000", "five minutes between reads")
	must(t, spaFunctionLine(html, "connRead"), "'/api/domains/connected'", "list route")
	must(t, spaFunctionLine(html, "connRead"), "catch(e){return}", "errors, 403 included, are ignored")
	must(t, spaFunctionLine(html, "connShow"), "'/api/domains/connected/ack'", "acknowledge route")
	must(t, spaFunctionLine(html, "connShow"), "go('domains')", "second button opens the domains page")
	must(t, spaFunctionLine(html, "connShow"), "if(CONN.box) return", "one window at a time")
	must(t, spaFunctionLine(html, "connStart"), "pageAllowed(me,'domains')", "only for users who see the domains page")
	must(t, spaFunctionLine(html, "connTick"), "gen!==CONN.gen", "a stale tick does nothing")
	must(t, spaFunctionLine(html, "connStop"), "CONN.gen++", "stopping bumps the generation")
	must(t, spaFunctionLine(html, "app"), "connStart(ME)", "the poll starts after login")
	must(t, spaFunctionLine(html, "loginView"), "connStop()", "the poll stops on logout")
	if n := strings.Count(html, "setTimeout(()=>connTick("); n != 1 {
		t.Errorf("the connected poll has %d timers, want one", n)
	}
}

// TestNetworkErrorMapping checks that a failed fetch becomes the translated no-connection error
// for both request helpers, with status 0, and that writes are not retried.
func TestNetworkErrorMapping(t *testing.T) {
	html := spaSource()
	net := spaFunctionLine(html, "fetchOrNetErr")
	must(t, net, "instanceof TypeError", "only a fetch TypeError is mapped")
	must(t, net, "tr('err.network')", "translated text")
	must(t, net, "e.status=0", "status 0")
	mustNot(t, net, "retry", "no automatic retry")
	must(t, spaFunctionLine(html, "call"), "await fetchOrNetErr(path,o)", "call maps the failure")
	must(t, spaFunctionLine(html, "callRaw"), "await fetchOrNetErr(path,o)", "callRaw maps the failure")
	mustNot(t, spaFunctionLine(html, "call"), "await fetch(", "call does not bypass the mapping")
	mustNot(t, spaFunctionLine(html, "callRaw"), "await fetch(", "callRaw does not bypass the mapping")
	must(t, html, `'err.network':'Нет связи с сервером. Проверьте соединение и повторите.'`, "ru text")
}

// TestSpaBoardPanel checks the section switcher: API header, sessionStorage, cache reset and UI hooks.
func TestSpaBoardPanel(t *testing.T) {
	html := spaSource()
	must(t, html, "const BOARD_KEY='kuztds_board'", "sessionStorage key")
	must(t, html, `id="board"`, "header section button")
	must(t, spaFunctionLine(html, "call"), "boardApplyHeaders(o.headers, path, opts.board)", "call sends X-Kuztds-Board")
	must(t, spaFunctionLine(html, "callRaw"), "boardApplyHeaders(o.headers, path, opts.board)", "callRaw sends X-Kuztds-Board")
	must(t, spaFunctionLine(html, "boardApplyHeaders"), "headers['X-Kuztds-Board']", "header name")
	must(t, spaFunctionLine(html, "boardApplyHeaders"), "path==='/api/login'", "login is excluded")
	must(t, spaFunctionLine(html, "boardSwitchRows"), "boardIconTone(row)", "section row uses the saved tint")
	must(t, spaFunctionLine(html, "boardIconTone"), "return 'ok'", "default section without a tint stays green")
	must(t, html, ".board-icon-grid{display:grid;grid-template-columns:repeat(auto-fill,36px);gap:6px;max-height:240px;overflow-x:hidden", "icon grid scrolls vertically only")
	must(t, spaFunctionLine(html, "openBoardIconPicker"), "openBoardIconColorPop", "icon click opens the tint popup")
	mustNot(t, spaFunctionLine(html, "openBoardIconPicker"), "board-icon-colors", "main picker has no tint row")
	must(t, spaFunctionLine(html, "openBoardIconColorPop"), "board-icon-colors", "tint popup shows the ten colors")
	must(t, html, "if(BOARD_ICON_COLOR_POP){ closeBoardIconColorPop(); return; }", "escape closes the tint popup first")
	must(t, spaFunctionLine(html, "iconRecentPush"), "ICON_RECENT_KEY", "recent icons are a shared browser list")
	must(t, html, "const ICON_RECENT_KEY='kuztds_icon_recent'", "recent list storage key")
	must(t, html, "const ICON_COLORS=['clicks','uniq','conv','rev','profit','cost','bots','roi','cr','warn']", "ten icon tints")
	must(t, html, ".menu svg.ic.ok{color:var(--ok)", "green icon in the menu")
	must(t, spaFunctionLine(html, "boardName"), "tr('board.main')", "main section label")
	must(t, spaFunctionLine(html, "resetBoardCaches"), "DASH_PERF=null", "dashboard perf cleared")
	must(t, spaFunctionLine(html, "resetBoardCaches"), "FACTS={ready:false", "facts cleared")
	must(t, spaFunctionLine(html, "resetBoardCaches"), "SPEND=[]", "spend cleared")
	must(t, spaFunctionLine(html, "switchBoard"), "resetBoardCaches()", "switch resets caches")
	must(t, spaFunctionLine(html, "sendPageViewsOnHide"), "boardApplyHeaders", "keepalive views carry the header")
	must(t, spaFunctionLine(html, "userMenuItems"), "appendBoardUserMenuItems", "profile menu lists sections")
	boardSwitchLine := spaFunctionLine(html, "boardSwitchItems")
	mustNot(t, boardSwitchLine, "boardCreateMenuNode", "header board menu is switch-only")
	must(t, html, ".menu button.primary{background:var(--accent)", "a primary button inside a menu keeps the accent fill")
	must(t, spaFunctionLine(html, "boardImportSectionHTML"), `class="bi-row"`, "import row stays within the dialog")
	must(t, html, ".bi-row input{width:16px", "import checkbox stays a checkbox")
	must(t, html, ".bi-row span{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis", "import name ellipsizes")
	mustNot(t, spaFunctionLine(html, "openBoardImport"), `hint">${esc(tr('board.empty'))}</p><div id="bi-body"`, "empty hint is not shown before the list loads")
	userMenuLine := spaFunctionLine(html, "userMenuItems")
	must(t, userMenuLine, "userTypeLabel(ME&&ME.type)", "account caption is the translated type")
	profileAt := strings.Index(userMenuLine, "tr('menu.settings')")
	boardsAt := strings.Index(userMenuLine, "appendBoardUserMenuItems")
	if profileAt < 0 || boardsAt < 0 || profileAt > boardsAt {
		t.Errorf("profile sits above sections: profile=%d boards=%d", profileAt, boardsAt)
	}
	must(t, userMenuLine, "icon:'send'", "postbacks icon send")
	must(t, userMenuLine, "icon:'tools'", "integrations icon tools")
	must(t, userMenuLine, "icon:'user'", "users icon user")
	appendBoardLine := spaFunctionLine(html, "appendBoardUserMenuItems")
	must(t, appendBoardLine, "tr('board.sections')", "profile menu sections caption")
	must(t, appendBoardLine, "tr('board.settings')", "profile menu section settings")
	must(t, appendBoardLine, "icon:'settings'", "section settings uses the gear")
	must(t, spaFunctionLine(html, "boardIconButtonHTML"), "board-icon-btn", "icon control is a button")
	must(t, spaFunctionLine(html, "openBoardIconPicker"), "board-icon-grid", "icon picker is a grid")
	must(t, spaFunctionLine(html, "openBoardIconPicker"), "tr('board.all')", "icon grid caption")
	must(t, html, "'board.all':'Все иконки'", "ru all icons")
	must(t, html, "'board.all':'All icons'", "en all icons")
	must(t, html, "'board.all':'Усі іконки'", "uk all icons")
	must(t, html, ".board-icon-colors-pop .board-icon-row{display:grid;grid-template-columns:repeat(5,36px)", "tint popup is two rows of five")
	must(t, spaFunctionLine(html, "openBoardIconPicker"), "tr('Search…')", "icon picker has search")
	must(t, spaFunctionLine(html, "boardsPage"), "boardIconButtonHTML", "create form uses the icon button")
	mustNot(t, spaFunctionLine(html, "paintBoardsTable"), "boardIconSelect", "table does not use a name select")
	must(t, html, "addEventListener('resize'", "menu resize listener")
	must(t, spaFunctionLine(html, "ensureMenuResize"), "if(MENU) menuPlace()", "menu repositions on resize")
	mustNot(t, html, "#/flows/{", "campaign num is not turned into a hash link")
	must(t, spaFunctionLine(html, "boardSaveId"), "boardStorageEncode(id)", "main stored as main in sessionStorage")
	mustNot(t, spaFunctionLine(html, "boardSaveId"), "setItem(BOARD_KEY, boardNormId", "save does not write empty main")
	must(t, spaFunctionLine(html, "boardEffectiveId"), "boardHasStored()", "effective id respects an explicit tab choice")
	must(t, spaFunctionLine(html, "boardEffectiveId"), "stored!=null?stored:''", "main choice does not fall back to BOARDS.current")
	must(t, spaFunctionLine(html, "applyBoardsPayload"), "!boardHasStored()&&BOARDS.current", "payload seeds storage only before first choice")
	mustNot(t, spaFunctionLine(html, "applyBoardsPayload"), "boardStoredId()", "payload does not treat main as unset")
	must(t, spaFunctionLine(html, "boardStoredId"), "setItem(BOARD_KEY,'main')", "legacy empty storage migrates to main")
	profileLine := spaFunctionLine(html, "profile")
	must(t, profileLine, "arg==='boards'", "profile opens boards tab")
	must(t, profileLine, "#/settings/boards", "profile boards hash")
	must(t, profileLine, "meIsAdmin()", "boards tab is admin-only")
	must(t, profileLine, "go('settings/boards')", "boards tab navigation")
	must(t, html, "function boardsPage(", "boards settings page")
	must(t, spaFunctionLine(html, "patchBoard"), "call('PATCH','/api/boards/", "boards PATCH API")
	must(t, spaFunctionLine(html, "deleteBoardRow"), "call('DELETE','/api/boards/", "boards DELETE API")
	must(t, spaFunctionLine(html, "deleteBoardRow"), "boardIsMain", "main board delete guard")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "boardIsMain", "main row without delete button")
	must(t, spaFunctionLine(html, "deleteBoardRow"), "tr('board.inuse')", "409 board in use toast")
	card := pageFnBody(html, "boardPanelCard")
	must(t, card, "'/assign'", "board panel assign API")
	must(t, card, "'/members'", "board panel members API")
	must(t, spaFunctionLine(html, "bindBoardPanelCard"), "data-bassign-save", "board assign save control")
	must(t, spaFunctionLine(html, "bindBoardPanelCard"), "data-bmembers-save", "board members save control")
	mustNot(t, spaFunctionLine(html, "paintBoardsTable"), "boardRowIcon(row)", "table has no preview icon column")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "key:'board', cls:'ok'", "default section button shows the green board glyph")
	must(t, spaFunctionLine(html, "paintBoardsTable"), `colspan="5"`, "composition row spans the remaining columns")
	must(t, spaFunctionLine(html, "placeBoardIconPop"), "popLeft(r, w, false)", "icon popup stays inside the window")
	must(t, spaFunctionLine(html, "viewBox"), "clientWidth", "icon popup uses the visible window")
	mustNot(t, spaFunctionLine(html, "placeBoardIconPop"), "q('main')", "icon popup is not tied to the content column")
	must(t, spaFunctionLine(html, "openBoardIconPicker"), "placeBoardIconPop", "icon picker uses the column clamp")
	must(t, spaFunctionLine(html, "openBoardIconColorPop"), "placeBoardIconPop", "tint popup uses the column clamp")
	must(t, html, ".board-icon-pop{position:fixed;z-index:86;width:448px;max-width:calc(100vw - 16px);box-sizing:border-box;display:flex;flex-direction:column;gap:10px;padding:12px;background:var(--surface);border:1px solid var(--border);border-radius:var(--r);box-shadow:var(--shadow-2);max-height:min(520px,calc(100vh - 16px));overflow-x:hidden", "icon popup clips horizontal overflow")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "boardPanelToggle", "boards table expand toggle")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "boardIsMain", "main row without panel button")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "data-bdrag", "section row has a drag handle")
	must(t, spaFunctionLine(html, "bindBoardRowDrag"), "onpointerdown", "drag follows the pointer")
	must(t, spaFunctionLine(html, "bindBoardRowDrag"), "board-drag-slot", "drag shows a dashed slot")
	must(t, spaFunctionLine(html, "bindBoardRowDrag"), "board-drag-ghost", "drag row follows the pointer")
	must(t, spaFunctionLine(html, "bindBoardRowDrag"), "ghost.style.width", "drag ghost is as wide as the table")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "board-col-icon", "icon column stays as wide as its header")
	must(t, spaFunctionLine(html, "bindBoardPanelCard"), "tr('board.people.remove')", "member row uses the translator")
	mustNot(t, spaFunctionLine(html, "bindBoardPanelCard"), "const tr=document.createElement('tr')", "member row must not shadow tr()")
	must(t, spaFunctionLine(html, "bindBoardRowDrag"), "board-panel-row", "open composition moves with its section")
	must(t, spaFunctionLine(html, "collectBoardPanelAssign"), "if(lab&&labels.has(lab)) return", "checked folder is not also sent as an id")
	must(t, spaFunctionLine(html, "call"), "j.error==='csrf'", "csrf failure is recognized")
	must(t, spaFunctionLine(html, "call"), "CSRF=me.csrf", "csrf retries once with a fresh token")
	must(t, html, "'err.csrf':'Сессия устарела. Обновите страницу.'", "ru csrf message")
	must(t, html, ".fld input:not([type=checkbox])", "checkboxes are not stretched to full width")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "icon('grip')", "drag handle is the grip icon")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "icon('trash')", "delete is an icon")
	must(t, spaFunctionLine(html, "paintBoardsTable"), "data-bpanel", "composition stays a button")
	must(t, spaFunctionLine(html, "bindBoardRowDrag"), "PUT','/api/boards/order'", "drag saves the section order")
	must(t, html, "'board.move':'Переместить'", "ru move")
}

// TestSpaIconKeysAreAllowed checks the picker lists at least 200 icons and the API accepts each key.
func TestSpaIconKeysAreAllowed(t *testing.T) {
	html := spaSource()
	i := strings.Index(html, "const IC={")
	j := strings.Index(html, "\nconst icon=")
	if i < 0 || j < i {
		t.Fatal("IC block missing")
	}
	re := regexp.MustCompile(`(?m)^\s*(?:'([^']+)'|([A-Za-z][A-Za-z0-9]*)):`)
	ms := re.FindAllStringSubmatch(html[i:j], -1)
	if len(ms) < 200 {
		t.Fatalf("picker icons = %d; want at least 200", len(ms))
	}
	for _, m := range ms {
		k := m[1]
		if k == "" {
			k = m[2]
		}
		if err := config.ValidateBoardIcon(k); err != nil {
			t.Fatalf("icon %s: %v", k, err)
		}
	}
}
