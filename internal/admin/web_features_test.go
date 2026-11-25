package admin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
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
	must(t, spaFunctionLine(html, "saveAll"), "toast(e,true)", "server answers, with their status and code, are translated by toast")
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
	mustIntegration(t, html, "namecheap", "namecheapPanel")
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
		`<div class="g2"><div class="fld"><label for="nc-username">`,
		`<label for="nc-key">`,
		`calloutHTML('warn',tr('nc.contacts.match'),{id:'nc-contacts-match'})`,
		`id="nc-contacts"`,
	} {
		must(t, panel, needle, "Namecheap account card")
	}
	accountAt := strings.Index(panel, `data-ncpanel="account"`)
	contactsAt := strings.Index(panel, `data-ncpanel="contacts"`)
	pairAt := strings.Index(panel, `<div class="g2"><div class="fld"><label for="nc-username">`)
	keyAt := strings.Index(panel, `<label for="nc-key">`)
	if accountAt < 0 || pairAt <= accountAt || keyAt <= pairAt || contactsAt <= keyAt {
		t.Errorf("user name and API key must share one row on the account tab")
	}
	matchAt := strings.Index(panel, `tr('nc.contacts.match')`)
	fieldsAt := strings.Index(panel, `id="nc-contacts"`)
	if contactsAt < 0 || matchAt < contactsAt || fieldsAt < matchAt {
		t.Errorf("the owner-match warning must sit on the contacts tab above the contact fields")
	}
	mustNot(t, panel, "cur.api_key", "the key is never read back into the page")
	must(t, panel, "api_user:name, username:name", "one user name is sent as api_user and username")
	must(t, panel, "copyField(", "the server IP is a copy field")
	must(t, panel, "setCopyField(q('#nc-ip',host)", "detect fills the copy field")
	mustNot(t, panel, `id="nc-name"`, "the account name is not on the form")
	mustNot(t, panel, `id="nc-apiuser"`, "there is no separate API user field")
	mustNot(t, html, "оставить NS регистратора", "an empty name-server list no longer claims to keep the registrar NS")
	must(t, html, "направить на IP этого сервера", "the purchase hint points an empty list at this server")
	must(t, spaFunctionLine(html, "registerDomainModal"), "trFill('nc.reg.nshint'", "the purchase hint can name the server IP")
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
		"tr('nc.reg.agree')",
		"DOM_BUY",
		"touchPageViews()",
		"st.domain.toLowerCase()===typed&&st.price>0",
	} {
		must(t, reg, needle, "purchase window")
	}
	for _, needle := range []string{
		"regs.length>1", `id="nr-reg-mark"`, "INT_LOGOS.namecheap", "const term=1",
		`class="chk" id="nr-confirm-row"`, `type="checkbox" id="nr-confirm"`,
		`data-nrtab="ns"`, `data-nrpane="buy"`, `id="nr-ns-box"`,
		"setAttribute('aria-invalid','true')", "tr('nc.reg.https')", "tr('nc.reg.admin')", "tr('nc.reg.robots')",
		"else if(ns.length) body.nameservers=ns",
	} {
		must(t, reg, needle, "purchase window layout")
	}
	mustNot(t, reg, `id="nr-years"`, "the term is not a field")
	mustNot(t, reg, `id="nr-confirm" disabled`, "consent does not wait for a price")
	mustNot(t, reg, "nc.reg.needcheck", "the checkbox is not a prompt to check the price")
	mustNot(t, reg, `class="tog"><input type="checkbox" id="nr-confirm"`, "payment consent is not a pill")
	mustNot(t, reg, "nr-index", "there is no index page field")
	must(t, spaFunctionLine(html, "collectPageViews"), "out.domains.buy=page('domains').buy===true", "consent is stored with the domains page")
	must(t, spaFunctionLine(html, "applyPageViews"), "out.domains.buy=domBuy.buy", "consent is read back from the domains page")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "buy:DOM_BUY===true", "consent leaves the account snapshot")
	must(t, spaFunctionLine(html, "applyPageViewSnapshot"), "DOM_BUY=dom.buy===true", "opening the window restores the saved consent")
	if strings.Count(reg, "INT_LOGOS.namecheap") != 1 {
		t.Errorf("the Namecheap mark is drawn once, next to the registrar")
	}
	buyAt := strings.Index(reg, `data-nrpane="buy"`)
	nsAt := strings.Index(reg, `data-nrpane="ns"`)
	nsBox := strings.Index(reg, `id="nr-ns"`)
	confirmAt := strings.Index(reg, `id="nr-confirm"`)
	if buyAt < 0 || nsAt < buyAt || confirmAt < 0 || confirmAt > nsAt || nsBox < nsAt {
		t.Errorf("consent stays on the purchase tab and name servers sit on theirs")
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
	createAt := strings.Index(dom, `createBtn('dom-new')`)
	regAt := strings.Index(dom, `class="primary mk" id="dom-register"`)
	liveAt := strings.Index(dom, `id="dom-check-live"`)
	if createAt < 0 || regAt < createAt || liveAt < regAt {
		t.Errorf("domain toolbar order create=%d register=%d live=%d", createAt, regAt, liveAt)
	}
	must(t, dom[regAt:liveAt], `icon('domains')`, "register uses the domain mark")
	mustNot(t, dom[regAt:liveAt], `icon('plus')`, "register is not a second plus")
	must(t, dom[liveAt:liveAt+120], `icon('eye')`, "live check uses the domain check mark")
	must(t, dom, "meIsAdmin()", "purchase actions are for administrators")
	must(t, html, "registrar_status==='pending'", "pending status from the domain row")
	must(t, spaFunctionLine(html, "call"), "apiError(msg,r.status,j,path,", "the error body reaches the caller")

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
		"nc.ip", "nc.ip.hint", "nc.sandbox", "nc.contacts", "nc.contacts.match", "nc.test", "nc.detect", "nc.test.ok", "nc.detect.found",
		"nc.detect.mismatch", "nc.reg.btn", "nc.reg.title", "nc.reg.check", "nc.reg.price", "nc.reg.confirm",
		"nc.reg.https", "nc.reg.admin", "nc.reg.robots", "nc.reg.nscf", "nc.reg.agree",
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
		"dom.tls", "dom.untrusted", "cf.connecting", "cf.connect", "cf.connect.title", "cf.connect.ask", "cf.connect.ask.nc",
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
	must(t, pure, "totals:paged&&obj.totals", "the totals come from the server for the whole set")
	total := spaFunctionLine(html, "reportTotalCells")
	must(t, total, "reportCellText(tot, c.id, machine)", "the total row shows the server sums")
	must(t, total, "'cr'", "the ratio is computed from the totals")
	must(t, total, "trFill('rep.total'", "the total row names how many rows it covers")
	paint := spaFunctionLine(html, "paintReport")
	must(t, paint, "reportTotalCells(tot,cols,false,total)", "the table has a total row of the whole set")
	must(t, paint, "id:'rep-cut'", "a report stopped at the server limit says so")
	must(t, paint, "REPORT.truncated?calloutHTML('info'", "the limit note is calm, not a warning")
	must(t, paint, "c.id!=='cost'||priced", "the cost column needs a priced row")
	must(t, paint, "trFill('rep.cost.sort',{cur:reportCostCur(tot)})", "the cost header names its sort currency")
	mustNot(t, paint, "applySort(", "the report is sorted on the server")
	mustNot(t, html, "function sortReportRows(", "no report sort runs in the page")
	must(t, paint, "c.on&&(c.id!=='cost'||priced)", "the cost column shows only when a row has a price")
	must(t, paint, `id="reppage-top"`, "the report has a pager above the table")
	must(t, paint, `id="reppage"`, "the report has a pager below the table")
	must(t, paint, "sizes:TABLE_PAGE_SIZES, numbers:true", "the report uses the numbered shared pager")
	must(t, paint, "bindSort(view(), REP_SORT, ()=>{REP_PAGE_I=0; loadReport(live)})", "a new sort reloads from page one")
	must(t, paint, "REP_PAGE_I=0;\n    await loadReport(live);", "a new filter reloads from page one")
	must(t, spaFunctionLine(html, "report"), "REP_PAGE_I=0;", "a new period or entry starts at page one")
	must(t, total, "'cost'", "the cost column has a total")
	must(t, total, "if(!tot||!(n>0)) return []", "an empty set has no total row")
	qs := spaFunctionLine(html, "reportQS")
	must(t, qs, "qs.set('limit'", "the page asks for one window")
	must(t, qs, "qs.set('offset'", "the page asks for its offset")
	must(t, qs, "qs.set('sort', REP_SORT.k)", "the sort is sent to the server")
	must(t, spaFunctionLine(html, "setReport"), "reportData(body)", "the answer is read through reportData")
	must(t, spaFunctionLine(html, "loadReport"), "markBusy(q('#rep-tw')", "the table shows that it waits")
	must(t, spaFunctionLine(html, "loadReport"), "my!==REP_GEN", "a late answer is dropped")
	exp := spaFunctionLine(html, "reportExport")
	must(t, exp, "reportQS(TABLE_EXPORT_MAX, 0)", "the export reads the whole set up to the limit")
	must(t, exp, "trFill('rep.export.cut'", "an export over the limit says so")
	mustNot(t, html, "REPORT_ROWS=next", "no answer bypasses reportData")
	must(t, paint, "<tfoot>", "the total is a footer row")
	for _, k := range []string{"rep.cut", "rep.total", "rep.export.cut", "pg.first", "pg.prev", "pg.next", "pg.last", "pg.page", "pg.size", "pg.rows"} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %s is defined %d time(s), want one per language", k, n)
		}
	}
	for _, k := range []string{"'rep.cut':'Showing the first 2000", "'kws.cut':", "'kws.prev':", "'kws.next':"} {
		mustNot(t, html, k, "the old cut warning and keyword pager keys are gone")
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
	must(t, camp, "'{'+k+'}'", "a filled sub id shows the outgoing mask")
	must(t, camp, "sub_id_", "the mask is the sub id slot")
	must(t, camp, "data-src-param=", "the name in the link is an input")
	must(t, camp, "data-src-token=", "the token is an input")
	must(t, camp, "sourceKeyLocked(ME,k)", "a hidden field disables its campaign parameter")
	mustNot(t, camp, `<input value="${esc(map[k]||'')}" readonly>`, "the name in the link is not read-only")
	mustNot(t, camp, `<input value="${esc(mask(k))}" readonly>`, "the token is not read-only")
	flow := spaFunctionLine(html, "collectFlow")
	must(t, flow, "campaignParamDraft(rows)", "save reads the parameter rows")
	must(t, flow, "g.parameters=draft.parameters", "the name in the link is stored on the campaign")
	must(t, flow, "g.param_tokens=draft.tokens", "the token is stored on the campaign")
	must(t, flow, "el.disabled", "a locked parameter is not read back")
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
	must(t, spaFunctionLine(html, "toast"), "errParts(msg)", "failed toasts are translated")
}

// TestSpaRolesUI checks the roles editor: it shares the access editor with the user form,
// sends no record lists, lists only hideable fields and reports an unavailable store.
func TestSpaRolesUI(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, name := range []string{"hiddenFields", "columnHidden", "offerPayoutHidden", "accessModes", "grantBody", "byFieldOrder", "hideChoices", "userRoleField", "roleChoices", "roleNameOf"} {
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
	must(t, spaFunctionLine(html, "boot"), "FIELDS.fields=byFieldOrder(info.fields", "the field catalog is sorted once by the display order")
	order := spaBetween(html, "const FIELD_ORDER=[", "];")
	for _, f := range store.StaticFields() {
		if !strings.Contains(order, "'"+f.Name+"'") && !strings.HasPrefix(f.Name, "sub_id_") && !spaHiddenFields[f.Name] {
			t.Errorf("field %s is missing from FIELD_ORDER", f.Name)
		}
	}
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
	offers := spaBetween(html, "const payHidden=offerPayoutHidden(ME);", "const capOn=")
	if offers == "" {
		t.Fatal("offer form payout switch not found")
	}
	must(t, html, "${payHidden?'':`<input id=\"opayout\"", "payout fields are left out when hidden")
	must(t, html, "if(!payHidden){", "payout is sent only when it was shown")
	mustNot(t, html, "payout:payRaw>0?payRaw:0, payout_currency:q('#ocur',box).value, payout_param", "payout is not part of the fixed body")
	must(t, spaFunctionLine(html, "renderPerf"), "shown('revenue'", "the performance table drops a hidden revenue column")
	must(t, spaFunctionLine(html, "recentRowHTML"), "dashRows('recent')", "recent clicks take their columns from the dashboard set")
	must(t, spaFunctionLine(html, "dashSetHidden"), "hiddenFields(ME)", "recent clicks drop hidden fields")
	must(t, spaFunctionLine(html, "paintMetricSum"), "hs('sales'", "the flows summary drops hidden metrics")
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

// TestSpaDepSum checks the deposit column label is depSum in ru, en and uk, that Dep2Sum is not in the interface, and that the deposit stays out of revenue and the chart.
// t is the test handle. It reads the SPA source. It fails the test when the label or an exclusion is wrong.
func TestSpaDepSum(t *testing.T) {
	html := spaSource()
	for _, key := range []string{"col.depsum", "fld.depsum"} {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want 3", key, n)
		}
	}
	must(t, html, "'col.depsum':'depSum'", "column title is depSum")
	must(t, html, "'fld.depsum':'depSum'", "field name is depSum")
	if n := strings.Count(html, "'col.depsum':'depSum'"); n != 3 {
		t.Errorf("col.depsum depSum is defined %d time(s), want 3", n)
	}
	if n := strings.Count(html, "'fld.depsum':'depSum'"); n != 3 {
		t.Errorf("fld.depsum depSum is defined %d time(s), want 3", n)
	}
	mustNot(t, html, "'col.depsum':'DepSum'", "column title is not DepSum")
	mustNot(t, html, "'fld.depsum':'DepSum'", "field name is not DepSum")
	mustNot(t, html, "Dep2Sum", "Dep2Sum is not in the interface")
	mustNot(t, html, "Deposits sum", "the column is not named Deposits sum")
	mustNot(t, html, "Сумма депозитов", "the column is not named Сумма депозитов")
	mustNot(t, html, "'col.depsum':'Депозит'", "column title is not Депозит")
	mustNot(t, html, "'fld.depsum':'Депозит'", "field name is not Депозит")
	must(t, html, "const DEP_COL=['depsum','col.depsum',0];", "offers list the DepSum column")
	must(t, html, "['revenue','col.revenue',1]", "revenue stays the payout column")
	mustNot(t, spaFunctionLine(html, "paintMetricSum"), "col.depsum", "catalog totals do not show DepSum")
	mustNot(t, spaFunctionLine(html, "paintPbDock"), "depsum", "the conversion dock does not show DepSum")
	must(t, spaFunctionLine(html, "loadCols"), "placeDepSumAfterCpc(out)", "a saved list still runs the deposit placement")
	must(t, spaFunctionLine(html, "hiddenCols"), "'depsum'", "hiding profit hides the deposit column")
	must(t, spaFunctionLine(html, "packMetrics"), "depsum:m.depsum", "deposit is packed beside revenue")
	mustNot(t, spaFunctionLine(html, "packMetrics"), "rev:m.rev+m.depsum", "deposit is not added into revenue")
	must(t, spaFunctionLine(html, "foldStatus"), "o.depsum+=+r.depsum||0", "lead and sale deposits are summed")
	must(t, html, "FLOW_USD_COLS=new Set(['revenue','depsum','uepc','epc','ec','ecpm'])", "deposit CSV is USD")
	if strings.Contains(html, "{id:'depsum',key:") {
		t.Fatal("chart series lists the deposit")
	}
}

// TestSpaCatalogDock checks that offers and networks pin the campaign-style dock:
// totals of the filter, a client pager, and CSV/HTML export. The catalog toolbar
// does not grow an export button. t reads the SPA source and does not fail the process.
func TestSpaCatalogDock(t *testing.T) {
	html := spaSource()
	off := spaFunctionLine(html, "offers")
	net := spaFunctionLine(html, "networks")
	must(t, off, "dockHTML('off','offexport')", "offers dock")
	must(t, off, "has-flowdock", "offers pins the dock")
	must(t, off, "paintCatalogDock(", "offers fills the dock")
	must(t, off, "exportOffers(", "offers export from the dock")
	must(t, off, "OFF_PAGE_I=0", "offers search and sort return to the first page")
	must(t, net, "dockHTML('net','netexport')", "networks dock")
	must(t, net, "has-flowdock", "networks pins the dock")
	must(t, net, "paintCatalogDock(", "networks fills the dock")
	must(t, net, "networkExportItems(", "networks export from the dock")
	must(t, net, "NET_PAGE_I=0", "networks search and sort return to the first page")
	mustNot(t, spaFunctionLine(html, "catalogBar"), "export", "catalog bar has no export")
	must(t, spaFunctionLine(html, "paintCatalogDock"), "touchPageViews()", "page size is stored on the account")
	mustNot(t, spaFunctionLine(html, "paintCatalogDock"), "localStorage", "catalog pager does not use localStorage")
	must(t, spaFunctionLine(html, "foldPackedMetrics"), "acc.depsum+=", "deposit is summed beside revenue")
	mustNot(t, spaFunctionLine(html, "foldPackedMetrics"), "acc.rev+=+m.depsum", "deposit is not added into revenue")
	must(t, spaFunctionLine(html, "exportOffers"), "exportCatalogTable('offers'", "offers export uses the catalog helper")
	must(t, spaFunctionLine(html, "exportNetworks"), "exportCatalogTable('networks'", "networks export uses the catalog helper")
	mustNot(t, pageFnBody(html, "landings"), "dockHTML(", "landings have no catalog dock")
	mustNot(t, pageFnBody(html, "sources"), "dockHTML(", "sources have no catalog dock")
}

// TestSpaNetworkOfferExport checks the networks export menu: one network found by search adds CSV and HTML
// of that network's offers, an empty search keeps the two network items, and the offer file is the offers
// export of that network only.
func TestSpaNetworkOfferExport(t *testing.T) {
	html := spaSource()
	net := spaFunctionLine(html, "networks")
	must(t, net, "searchedNetwork(rows)", "the menu follows the network the search found")
	must(t, net, "networkExportItems(hit, asHTML=>exportNetworkOffers(hit, offers, asHTML))", "offer export uses the offers loaded on this page")
	items := spaFunctionLine(html, "networkExportItems")
	must(t, items, "tr('Export CSV')", "without a found network the menu keeps Export CSV")
	must(t, items, "tr('Export HTML')", "without a found network the menu keeps Export HTML")
	must(t, items, "if(hit)", "offer items appear only when one network is found")
	must(t, items, "tr('net.export.csv')", "the found network has a CSV item")
	must(t, items, "tr('net.export.html')", "the found network has an HTML item")
	must(t, items, "tr('net.export.off.csv')", "offers of that network have a CSV item")
	must(t, items, "tr('net.export.off.html')", "offers of that network have an HTML item")
	must(t, items, "exportNetworks(false)", "the first item is still the network CSV")
	found := spaFunctionLine(html, "searchedNetwork")
	must(t, found, "if(!q) return null", "an empty search finds no network")
	must(t, found, "list.length===1", "several matches are not one network")
	rows := spaFunctionLine(html, "networkOfferRows")
	must(t, rows, "String(o.network_id)===id", "only offers of this network")
	exp := spaFunctionLine(html, "exportNetworkOffers")
	must(t, exp, "exportCatalogTable('offers'", "offers use the offers export")
	must(t, exp, "networkOfferRows(net, offers)", "the file is that network's offers")
	mustNot(t, exp, "postback_url", "the offer file has no postback")
	mustNot(t, exp, "o.url", "the offer file has no offer address")
	mustNot(t, exp, "CAT_Q.offers", "the offers page search is not applied")
	for _, label := range []string{
		"'net.export.csv':'Network CSV'",
		"'net.export.csv':'Партнёрка CSV'",
		"'net.export.csv':'Мережа CSV'",
		"'net.export.html':'Network HTML'",
		"'net.export.html':'Партнёрка HTML'",
		"'net.export.html':'Мережа HTML'",
		"'net.export.off.csv':'Offers of this network CSV'",
		"'net.export.off.csv':'Офферы этой партнёрки CSV'",
		"'net.export.off.csv':'Офери цієї мережі CSV'",
		"'net.export.off.html':'Offers of this network HTML'",
		"'net.export.off.html':'Офферы этой партнёрки HTML'",
		"'net.export.off.html':'Офери цієї мережі HTML'",
	} {
		must(t, html, label, "export menu label")
	}
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
	mustIntegration(t, html, "cloudflare", "cloudflarePanel")
	panel := spaFunctionLine(html, "cloudflarePanel")
	for _, needle := range []string{
		"get('/api/cloudflare'", "call('PUT','/api/cloudflare'", "call('POST','/api/cloudflare/test'",
		`id="cf-token"`, `type="password"`, `autocomplete="new-password"`, "cur.has_token", `id="cf-account"`,
		`id="cf-ip"`, " readonly", "cur.public_ip", "'cf.ip.empty'", `id="cf-save"`, `id="cf-test"`,
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
		"d.reason==='tls'", "d.reason==='untrusted'", "d.reason==='cf_pending'", "d.reason==='elsewhere'", "d.cf_status==='connect_pending'",
		"tr('cf.connecting')", "tr('dom.tls')", "tr('dom.untrusted')", "dom.cfpending", "dom.elsewhere", "CF_INFO.public_ip", "d.reason_detail",
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
	must(t, html, `'dom.untrusted':'Сертификат сервера не доверенный'`, "ru untrusted certificate text")
	must(t, html, `'dom.untrusted':'Server certificate is not trusted'`, "en untrusted certificate text")
	must(t, html, `'dom.untrusted':'Сертифікат сервера не довірений'`, "uk untrusted certificate text")
	must(t, html, ".dom-status .dom-verdict.warn{color:var(--warn)}", "untrusted verdict is yellow")
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
	must(t, html, ".jobs{column-width:460px;column-gap:14px}", "automation masonry adds columns with the width")
	must(t, html, `id="cron-arrange"`, "arrange settings control")
	if st, gear := strings.Index(html, `<span id="cronstate">`), strings.Index(html, `id="cron-arrange"`); st < 0 || gear < 0 || st > gear {
		t.Error("the service status stands left of the arrange gear in the automation header")
	}
	must(t, html, "hd-act", "job header actions wrapper")
	must(t, html, "function cronCardOrder(", "cron card order helper")
	must(t, html, "emtds-cron-order", "cron order storage key")
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
		"cron.ktsync.newcamps", "cron.ktsync.camps.pending", "cron.ktsync.camps.deleted",
		"cron.ktsync.camps.failed", "cron.ktsync.camps.paused",
	} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("missing %s in %s", key, lang)
			}
		}
	}
	must(t, html, "keitaro_sync:['cron.ktsync.title','cron.ktsync.hint']", "JOBINFO keitaro_sync")
	must(t, html, "'cron.ktsync.hint':'Догружает клики и конверсии с последнего курсора с запасом назад и добавляет новые кампании. Уже перенесённые кампании Keitaro тоже обновляются: название, потоки, ссылки, заметки. Чужие кампании, не из Keitaro, не трогаются.'", "ru hint: catch-up, new campaigns, imported ones are updated, foreign ones are not touched")
	mustNot(t, html, "'cron.ktsync.hint':'Догружает клики, конверсии и новые кампании", "ru hint does not open with the old wording")
	mustNot(t, html, "перенесённые не трогаются", "ru hint does not say imported campaigns stay untouched")
	must(t, html, "'cron.ktsync.hint':'Loads clicks and conversions from the last checkpoint with overlap and adds new campaigns. Campaigns already imported from Keitaro are updated too:", "en hint says imported campaigns are updated")
	must(t, html, "'cron.ktsync.hint':'Довантажує кліки та конверсії з останнього курсора із запасом назад і додає нові кампанії. Уже перенесені кампанії Keitaro теж оновлюються:", "uk hint says imported campaigns are updated")
	must(t, spaFunctionLine(html, "paintCronStatus"), "keitaroSyncCampaignsText(st)", "last run shows new campaigns")
	must(t, spaFunctionLine(html, "cronJobDisabled"), "job==='keitaro_sync'", "keitaro toggle guard")
	must(t, spaFunctionLine(html, "cronJobDisabled"), "job==='conversions'", "conversions toggle guard")
	must(t, spaFunctionLine(html, "cronFloor"), "CRONST.floors", "interval floor from API")
	must(t, spaFunctionLine(html, "cronApplyInterval"), "cronFloor(job)", "clamp interval to floor")
	must(t, spaFunctionLine(html, "cronJobCardHTML"), "cronJobDisabled(job, CRONST)", "automation disables jobs")
	must(t, spaFunctionLine(html, "paintAutomation"), "cronApplyInterval(j)", "all jobs clamped on paint")
	must(t, spaFunctionLine(html, "jobBody"), "case 'keitaro_sync'", "keitaro_sync card body")
	must(t, spaFunctionLine(html, "jobBody"), "keitaroSyncNeedImport(CRONST)", "full import banner")
	must(t, spaFunctionLine(html, "jobBody"), "cron.ktsync.noconv", "conversions hint without telegram")
	sched := spaFunctionLine(html, "convScheduleHTML")
	must(t, sched, "row('interval'", "interval mode stays on the card")
	must(t, sched, "row('daily'", "daily mode stays on the card")
	must(t, sched, "data-conv-at", "daily clock is a time field")
	must(t, sched, `step="60"`, "daily clock keeps minute steps")
	mustNot(t, sched, `type="time"`, "daily clock is not the native AM/PM field")
	period := spaFunctionLine(html, "convPeriodHTML")
	must(t, period, `id="conv-tz"`, "period zone uses the profile picker button")
	must(t, period, `id="conv-tz-list"`, "period zone uses the profile picker list")
	must(t, period, `class="tzpick"`, "period zone uses the profile picker shell")
	mustNot(t, period, `data-p="conversions.timezone"`, "period zone is not a text field")
	tzBind := spaFunctionLine(html, "bindConvTz")
	must(t, tzBind, "bindTzPick(", "period zone is the profile timezone component")
	must(t, tzBind, "conversions.timezone", "a pick stores conversions.timezone")
	must(t, spaFunctionLine(html, "paintAutomation"), "bindConvTz(v)", "period zone is wired when the card is painted")
	mustNot(t, sched, " hidden", "neither summary mode is hidden")
	must(t, spaFunctionLine(html, "convClockBind"), "time_24hr:true", "picker hours are 00-23")
	must(t, html, "/assets/flatpickr/flatpickr.min.js", "time picker is vendored")
	for _, lang := range []string{"en", "ru", "uk"} {
		if !trKeyDefined(html, lang, "cron.conv.daily") {
			t.Errorf("missing cron.conv.daily in %s", lang)
		}
	}
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
	must(t, spaFunctionLine(html, "flushPageViews"), "viewsOwner()!==owner", "a flush after the account changes is not sent")
	must(t, spaFunctionLine(html, "flushPageViews"), "e.status!==409", "a stale revision is read again")
	must(t, spaFunctionLine(html, "flushPageViews"), "pageViewsOverlay(", "the tab page is laid on the fresh copy")
	must(t, spaFunctionLine(html, "sendPageViewsOnHide"), "PAGE_VIEWS_REV", "the closing tab sends the revision")
	must(t, spaFunctionLine(html, "sendPageViewsOnHide"), "PAGE_VIEWS_OWNER", "the closing tab sends the owner")
	must(t, spaFunctionLine(html, "applyMePrefs"), "saveProfileTz(", "a time zone the account does not have yet is saved")
	mustNot(t, spaFunctionLine(html, "applyMePrefs"), "fresh||!haveLang", "the account language wins over the browser cache")
	must(t, spaFunctionLine(html, "applyTz"), "saveProfileTz(", "choosing a time zone is saved on the account")
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
	must(t, html, "offers:5000, networks:5000", "views cap offer and network page sizes")
	must(t, spaFunctionLine(html, "defaultPageViewState"), "offers:{page:50}", "offers default page size")
	must(t, spaFunctionLine(html, "defaultPageViewState"), "page:50}", "networks default page size")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "page:OFF_PAGE", "offers page size is collected")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "page:NET_PAGE", "networks page size is collected")
	mustNot(t, html, "localStorage.setItem('OFF_PAGE'", "offers page size is not in localStorage")
	mustNot(t, html, "localStorage.setItem('NET_PAGE'", "networks page size is not in localStorage")
	mustNot(t, spaFunctionLine(html, "paintServerPager"), "localStorage", "the pager saves the size with the account")
	must(t, spaFunctionLine(html, "paintServerPager"), "touchPageViews()", "a new page size is sent")
	numbered := spaFunctionLine(html, "paintNumberedPager")
	must(t, numbered, "touchPageViews()", "a new page size of the numbered pager is sent")
	must(t, numbered, "pagerPages(pageIndex, pages, 2)", "the numbered pager draws its page numbers through pagerPages")
	must(t, numbered, `aria-current="page"`, "the current page is marked for assistive tech")
	for _, id := range []string{"'first','pgfirst','pg.first'", "'prev','pgprev','pg.prev'", "'next','pgnext','pg.next'", "'last','pglast','pg.last'"} {
		must(t, numbered, id, "the numbered pager has first, previous, next and last")
	}
	mustNot(t, numbered, "localStorage", "the numbered pager saves the size with the account")
	must(t, html, "report:2000, keyword_stats:2000", "views cap report and keyword page sizes")
	must(t, spaFunctionLine(html, "defaultPageViewState"), "report:{conds:[], page:1000}", "the report defaults to 1000 rows")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "page:REP_PAGE", "the report page size is collected")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "page:KWS_PAGE", "the keyword page size is collected")
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
	if i := strings.Index(net, "\n/**"); i >= 0 {
		net = net[:i]
	}
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

// TestSpaBoardChromeIcon checks that the header section button shows the current section icon and tint.
func TestSpaBoardChromeIcon(t *testing.T) {
	html := spaSource()
	chrome := spaFunctionLine(html, "paintBoardChrome")
	must(t, chrome, "boardRowById()", "current section row")
	must(t, chrome, "boardRowIcon(row)", "icon with the board fallback from the section list")
	must(t, chrome, "boardIconTone(row||{id:boardEffectiveId()})", "tint matches the section menu, default section stays green")
	must(t, chrome, "glyph.outerHTML=face", "header glyph is replaced in place")
	must(t, chrome, "b.hidden=!boardChromeVisible(BOARDS)", "button shows only with a section besides the default one")
	must(t, html, "[hidden]{display:none!important}", "a hidden button leaves no gap in the header")
	for _, fn := range []string{"boardsPage", "deleteBoardRow", "patchBoard", "paintChrome"} {
		must(t, spaFunctionLine(html, fn), "paintBoardChrome()", fn+" repaints the button visibility")
	}
	must(t, spaFunctionLine(html, "deleteBoardRow"), "boardSaveId(''); resetBoardCaches();", "deleting the open section returns the tab to the default one")
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "route:'settings/boards'", "the first section is created from the settings tab of the menu")
	mustNot(t, spaFunctionLine(html, "appendBoardUserMenuItems"), "go('settings/boards')", "the sections switcher has no settings entry of its own")
	must(t, spaFunctionLine(html, "appendBoardUserMenuItems"), "if(!multi) return;", "with only the default section the switcher adds nothing")
	must(t, spaFunctionLine(html, "appendBoardUserMenuItems"), "const multi=boardChromeVisible(BOARDS);", "profile menu lists sections under the header button rule")
	must(t, spaFunctionLine(html, "appendBoardUserMenuItems"), "items.push(...boardSwitchRows());", "section rows only when there is something to switch to")
	mustNot(t, chrome, "iconToneClass(", "no separate tint rule on the header button")
	must(t, spaFunctionLine(html, "boardName"), "boardRowById(s)", "name and icon read the same row")
	must(t, spaFunctionLine(html, "switchBoard"), "paintBoardChrome()", "switch repaints the header")
	must(t, spaFunctionLine(html, "patchBoard"), "paintBoardChrome()", "icon edit repaints the header")
	must(t, html, ".ic.tone-clicks{color:var(--chart-clicks)}", "tints use theme variables")
}

// TestSpaBoardPanel checks the section switcher: API header, sessionStorage, cache reset and UI hooks.
func TestSpaBoardPanel(t *testing.T) {
	html := spaSource()
	must(t, html, "const BOARD_KEY='emtds_board'", "sessionStorage key")
	must(t, html, `id="board"`, "header section button")
	must(t, spaFunctionLine(html, "call"), "boardApplyHeaders(o.headers, path, opts.board)", "call sends X-Emtds-Board")
	must(t, spaFunctionLine(html, "callRaw"), "boardApplyHeaders(o.headers, path, opts.board)", "callRaw sends X-Emtds-Board")
	must(t, spaFunctionLine(html, "boardApplyHeaders"), "headers['X-Emtds-Board']", "header name")
	must(t, spaFunctionLine(html, "boardApplyHeaders"), "path==='/api/login'", "login is excluded")
	must(t, spaFunctionLine(html, "boardSwitchRows"), "boardIconTone(row)", "section row uses the saved tint")
	must(t, spaFunctionLine(html, "boardIconTone"), "return 'ok'", "default section without a tint stays green")
	must(t, html, ".board-icon-grid{display:grid;grid-template-columns:repeat(auto-fill,36px);gap:6px;max-height:240px;overflow-x:hidden", "icon grid scrolls vertically only")
	must(t, spaFunctionLine(html, "openBoardIconPicker"), "openBoardIconColorPop", "icon click opens the tint popup")
	mustNot(t, spaFunctionLine(html, "openBoardIconPicker"), "board-icon-colors", "main picker has no tint row")
	must(t, spaFunctionLine(html, "openBoardIconColorPop"), "board-icon-colors", "tint popup shows the ten colors")
	must(t, html, "if(BOARD_ICON_COLOR_POP){ closeBoardIconColorPop(); return; }", "escape closes the tint popup first")
	must(t, spaFunctionLine(html, "iconRecentPush"), "ICON_RECENT_KEY", "recent icons are a shared browser list")
	must(t, html, "const ICON_RECENT_KEY='emtds_icon_recent'", "recent list storage key")
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
	must(t, html, ".menu button.primary{background:var(--accent-fill)", "a primary button inside a menu keeps the accent fill")
	must(t, spaFunctionLine(html, "boardImportSectionHTML"), `class="bi-row"`, "import row stays within the dialog")
	must(t, html, ".bi-row input{width:16px", "import checkbox stays a checkbox")
	must(t, html, ".bi-row span{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis", "import name ellipsizes")
	mustNot(t, spaFunctionLine(html, "openBoardImport"), `hint">${esc(tr('board.empty'))}</p><div id="bi-body"`, "empty hint is not shown before the list loads")
	userMenuLine := spaFunctionLine(html, "userMenuItems")
	must(t, userMenuLine, "userTypeLabel(ME&&ME.type)", "account caption is the translated type")
	tabsAt := strings.Index(userMenuLine, "adminMenuEntries()")
	boardsAt := strings.Index(userMenuLine, "appendBoardUserMenuItems")
	if tabsAt < 0 || boardsAt < 0 || tabsAt > boardsAt {
		t.Errorf("settings entries sit above sections: tabs=%d boards=%d", tabsAt, boardsAt)
	}
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "icon:'send'", "postbacks icon send")
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "icon:'tools'", "integrations icon tools")
	must(t, spaFunctionLine(html, "adminMenuEntries"), "icon:t.icon", "settings entries take their icon from the tab")
	tabs := spaBetween(html, "const SETTINGS_TABS=[", "\n];")
	must(t, tabs, "icon:'user'", "users icon user")
	must(t, tabs, "icon:'settings'", "settings and section settings use the gear")
	must(t, tabs, "key:'board.settings'", "section settings tab caption")
	appendBoardLine := spaFunctionLine(html, "appendBoardUserMenuItems")
	must(t, appendBoardLine, "tr('board.sections')", "profile menu sections caption")
	mustNot(t, appendBoardLine, "tr('board.settings')", "the switcher has no section settings entry")
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
	must(t, profileLine, "settingsTabs().find(t=>t.id===arg)", "profile opens the tab named by the address")
	must(t, profileLine, "boards:boardsPage", "boards tab opens the sections page")
	must(t, profileLine, "go(settingsTabs().find(t=>t.id===b.dataset.ptab).route)", "tab navigation follows the tab route")
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "{id:'boards', key:'board.settings', route:'settings/boards', icon:'settings', allow:()=>meIsAdmin()}", "boards tab is admin-only")
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

// TestSpaBoardPartnerGroups checks the composition card titles partner groups
// and fills their checkboxes from the group field of affiliate networks.
// Saving still sends those labels in groups.networks.
func TestSpaBoardPartnerGroups(t *testing.T) {
	html := spaSource()
	card := pageFnBody(html, "boardPanelCard")
	must(t, card, "tr('board.compose.partners')", "partner groups caption")
	must(t, card, "const netGroups=groupLabels(netAll)", "partner groups come from the network group field")
	must(t, card, "boardPanelCheckGrid('networks', netGroups, netChecked)", "partner groups are checkboxes")
	must(t, card, "boardPanelSearchHTML(kind, chips[kind])", "partner groups keep the catalog search")
	grid := spaFunctionLine(html, "boardPanelCheckGrid")
	must(t, grid, "if(!list.length) return", "the none hint is only for an empty label list")
	collect := spaFunctionLine(html, "collectBoardPanelAssign")
	must(t, collect, "groups:{offers:[], landings:[], networks:[], sources:[]}", "save body keeps network groups")
	must(t, collect, "['offers','landings','networks','sources']", "checked partner groups are collected")
	for _, text := range []string{
		"'board.compose.partners':'Partner groups'",
		"'board.compose.partners':'Группы партнёров'",
		"'board.compose.partners':'Групи партнерів'",
	} {
		if n := strings.Count(html, text); n != 1 {
			t.Errorf("%s defined %d times, want 1", text, n)
		}
	}
}

// TestSpaBoardInclude checks the main view toggle, section filters and deletes before a full campaign save.
func TestSpaBoardInclude(t *testing.T) {
	html := spaSource()
	paint := spaFunctionLine(html, "paintBoardsTable")
	if paint == "" {
		t.Fatal("paintBoardsTable missing")
	}
	if i := strings.Index(paint, "list.length>1?`<button"); i < 0 {
		t.Fatal("main button formula missing")
	} else {
		rest := paint[i:]
		end := strings.Index(rest, ":'';")
		if end < 0 || !strings.Contains(rest[:end], "<button") {
			t.Fatal("one section must yield no main button")
		}
	}
	must(t, paint, "if(list.length<=1) MAIN_PANEL_OPEN=false", "one section closes the main panel")
	must(t, paint, "list.length>1?`<button", "two sections show the main button")
	must(t, paint, "tr('board.include')", "main button caption is the view label")
	must(t, paint, "tr('board.include_all')", "main panel is the include-all checkbox")
	must(t, paint, "if(row&&host&&!boardIsMain(row.id)) boardPanelCard(row, host)", "main row does not open the composition card")
	must(t, html, "let MAIN_PANEL_OPEN=false", "main panel has its own open flag")
	must(t, spaFunctionLine(html, "boardPanelToggle"), "MAIN_PANEL_OPEN=!MAIN_PANEL_OPEN", "main toggle does not use the empty section id")
	must(t, spaFunctionLine(html, "bindBoardInclude"), "call('PATCH','/api/boards/main'", "checkbox writes include_all at once")
	must(t, spaFunctionLine(html, "bindBoardInclude"), "resetBoardCaches()", "a saved flag clears cached catalogs")
	must(t, spaFunctionLine(html, "bindBoardInclude"), "box.checked=!want", "a failed write restores the checkbox")
	mustNot(t, spaFunctionLine(html, "bindBoardInclude"), "boardPanelCard", "include panel has no composition card")
	for _, text := range []string{
		"'board.include':'Section view'",
		"'board.include':'Вид раздела'",
		"'board.include':'Вид розділу'",
		"'board.include_all':'Show contents of all sections'",
		"'board.include_all':'Отображать содержимое всех разделов'",
		"'board.include_all':'Показувати вміст усіх розділів'",
	} {
		if n := strings.Count(html, text); n != 1 {
			t.Errorf("%s defined %d times, want 1", text, n)
		}
	}
	if n := strings.Count(html, "'board.include':"); n != 3 {
		t.Errorf("board.include defined %d times, want one per language", n)
	}
	cat := spaFunctionLine(html, "boardPanelFetchCatalog")
	must(t, cat, "boardRowsMain(groupRows(r))", "composition keeps main rows from the wide list")
	must(t, cat, "out.sec[k]=groupRows(r)", "the section's own rows are not cut")
	must(t, spaFunctionLine(html, "openBoardImport"), "boardRowsMain(", "import copies only the default section")
	must(t, spaFunctionLine(html, "boardPickRows"), "keep.has(id)", "a chosen id from another section stays")
	must(t, spaFunctionLine(html, "offerSelect"), "boardPickRows(OFFERS, CUR, id)", "offers stay in the campaign section")
	must(t, spaFunctionLine(html, "landingSelect"), "boardPickRows(LANDINGS, CUR, id)", "landings stay in the campaign section")
	settings := spaFunctionLine(html, "openFlowSettings")
	must(t, settings, "boardPickRows(list||[], g, g.source_id||'')", "sources stay in the campaign section")
	must(t, settings, "boardPickRows(GROUPS.filter(x=>x!==g), g,", "campaign targets stay in the campaign section")
	must(t, spaFunctionLine(html, "paintStream"), "boardPickRows(GROUPS.filter(x=>x!==g), g,", "stream targets stay in the campaign section")
	must(t, spaFunctionLine(html, "s2sGlobalHTML"), "boardPickRows(S2S_LIST||[], g,", "postback templates stay in the campaign section")
	must(t, spaFunctionLine(html, "duplicateFlow"), "c.board_id=boardEffectiveId()", "a copy takes the section open in this tab")
	body := spaFunctionLine(html, "saveFlows")
	del := strings.Index(body, "call('DELETE','/api/groups/")
	rename := strings.Index(body, "await put(g,prev.id)")
	full := strings.Index(body, "call('PUT','/api/groups',GROUPS)")
	if del < 0 || rename < 0 || full < 0 || del > rename || rename > full {
		t.Fatalf("saveFlows must delete, then rename, then write the array: delete=%d rename=%d full=%d", del, rename, full)
	}
	must(t, body, "e.status!==404", "a missing campaign delete still counts as done")
	must(t, body, "DIRTY.size>FLOW_POINT_MAX", "only many changes send the whole list")
	if strings.Contains(body, "drop.add(prev.id)") {
		t.Fatal("a changed id must be renamed on the server, not deleted")
	}
	if strings.Contains(spaFunctionLine(html, "duplicateFlow"), "ORDER_DIRTY") {
		t.Fatal("a copy must not force a whole-list save")
	}
}

// TestSpaIconKeysAreAllowed keeps the interface icon set (IC) and the board icon subset apart: the
// board icon picker (boardIconKeys) offers exactly the keys the API accepts, every board icon has a
// drawing in IC, and the interface-only icons (IC_UI_ONLY) are drawn by the SPA but refused for a board.
func TestSpaIconKeysAreAllowed(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the board icon checks")
	}
	html := spaSource()
	ic := spaBetween(html, "const IC={", "\n};")
	ui := spaBetween(html, "const IC_UI_ONLY=", "\n")
	pick := spaBetween(html, "function boardIconKeys(", "\n}\n")
	if ic == "" || ui == "" || pick == "" {
		t.Fatal("IC, IC_UI_ONLY or boardIconKeys missing in the SPA")
	}
	src := ic + "\n};\n" + ui + "\n" + pick + "\n}\n" +
		"console.log(JSON.stringify({ic:Object.keys(IC), ui:[...IC_UI_ONLY], board:boardIconKeys()}));\n"
	f := filepath.Join(t.TempDir(), "icons.js")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("icon script: %v\n%s", err, out)
	}
	var got struct{ IC, UI, Board []string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("icon script output: %v\n%s", err, out)
	}
	if len(got.IC) < 200 {
		t.Fatalf("interface icons = %d; want at least 200", len(got.IC))
	}
	inIC := make(map[string]bool, len(got.IC))
	for _, k := range got.IC {
		inIC[k] = true
	}
	if len(got.UI) == 0 {
		t.Fatal("IC_UI_ONLY is empty")
	}
	for _, k := range got.UI {
		if !inIC[k] {
			t.Errorf("interface-only icon %s has no drawing in IC", k)
		}
		if config.ValidateBoardIcon(k) == nil {
			t.Errorf("interface-only icon %s is accepted for a board", k)
		}
	}
	allowed := config.BoardIconKeys()
	for _, k := range allowed {
		if !inIC[k] {
			t.Errorf("board icon %s has no drawing in IC", k)
		}
	}
	if strings.Join(got.Board, ",") != strings.Join(allowed, ",") {
		t.Errorf("board icon picker offers %d icons, the API accepts %d; the sets must match", len(got.Board), len(allowed))
		for _, k := range got.Board {
			if config.ValidateBoardIcon(k) != nil {
				t.Errorf("picker offers %s, the API refuses it", k)
			}
		}
	}
}

// TestSpaKeywordStatsUI checks the keyword statistics tab: its dictionary keys exist in all three
// languages, the keyword reaches the markup only through esc, the routes and the saved view key
// are wired, no foreign columns appear, and csvTable runs every cell through csvSafe.
func TestSpaKeywordStatsUI(t *testing.T) {
	html := spaSource()
	keys := []string{
		"keys.hint", "kws.tab.stats", "kws.tab.lists", "kws.search", "kws.col.keyword", "kws.col.hits", "kws.col.uniq",
		"kws.col.conv", "kws.col.leads", "kws.col.sales", "kws.col.rej", "kws.col.cr", "kws.col.trend", "kws.nokey",
		"kws.err.heavy", "kws.err.busy", "kws.err.range", "kws.hint",
		"kws.by.last", "kws.by.origin", "kws.hint.origin",
	}
	for _, k := range keys {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
	hints := regexp.MustCompile(`'kws\.hint':'([^']*)'`).FindAllStringSubmatch(html, -1)
	if len(hints) != 3 {
		t.Fatalf("kws.hint is defined %d times, want 3", len(hints))
	}
	for _, h := range hints {
		for _, alias := range []string{"keyword=", "q=", "utm_term="} {
			if !strings.Contains(h[1], alias) {
				t.Errorf("kws.hint %q does not name %s", h[1], alias)
			}
		}
	}

	block := spaBetween(html, "let KWS={campaign:''", "async function keywordLists(")
	must(t, block, "'/api/keywords/stats?'", "the tab reads the statistics route")
	must(t, block, "'/api/keywords/trend?'", "the tab reads the trend route")
	must(t, spaFunctionLine(html, "kwsQS"), "qs.set('by','origin')", "origin attribution reaches both keyword requests")
	must(t, block, "hiddenFields(ME).has('origin_campaign_id')", "the origin switch follows field access")
	must(t, block, "data-kws-by", "the page draws the attribution switch")
	must(t, spaFunctionLine(html, "kwsPruneConds"), "KWS_ORIGIN_FIELDS.has(c.f)", "origin mode drops filters the API refuses")
	must(t, block, "kwsMode()==='origin'?KWS_ORIGIN_FIELDS:null", "the origin filter picker offers only supported fields")
	must(t, spaFunctionLine(html, "syncKwsModeUI"), "classList.toggle('on',on)", "the attribution switch updates its active class")
	must(t, spaFunctionLine(html, "syncKwsModeUI"), "setAttribute('aria-pressed'", "the attribution switch updates accessibility state")
	must(t, block, "KWS.by=b.dataset.kwsBy==='origin'?'origin':''; syncKwsModeUI(); reload()", "clicking attribution updates the switch before reload")
	must(t, block, "q('#kws-q').value='';\n    syncKwsModeUI();", "reset updates the attribution switch")
	must(t, block, "filterBuilder('events', KWS.conds", "the filter builder edits KWS.conds")
	mustNot(t, block, "applySort(", "the statistics are sorted on the server")
	must(t, block, "bindSort(wrap, KWS_SORT, ()=>{KWS_PAGE_I=0; loadKwStats(true)})", "a new sort reloads from page one")
	must(t, spaFunctionLine(html, "kwsPageQS"), "qs.set('sort', KWS_SORT.k)", "the sort is sent to the server")
	must(t, spaFunctionLine(html, "kwsExport"), "kwsPageQS(TABLE_EXPORT_MAX, 0)", "the export reads the whole set up to the limit")
	must(t, block, "csvTable(", "the export goes through csvTable")
	must(t, block, "downloadHTML(", "the HTML export goes through downloadHTML")
	must(t, block, "const my=++KWS_GEN", "a late answer is dropped by the generation counter")
	must(t, block, "my!==KWS_GEN", "a late answer is checked against the counter")
	must(t, block, "hiddenCols(ME)", "columns hidden by the role are not drawn")
	must(t, block, "'kws.nokey'", "the empty keyword has a caption")
	for _, raw := range []string{"${r.keyword}", "${x.keyword}", "${row.keyword}", "${s.keyword}"} {
		mustNot(t, block, raw, "a keyword must not enter markup unescaped: "+raw)
	}
	mustNot(t, block, "innerHTML=r.keyword", "no raw keyword in innerHTML")
	must(t, spaFunctionLine(html, "kwsKeyCell"), "esc(r.keyword)", "the keyword cell escapes the key")
	for _, foreign := range []string{"'roi'", "'uepc'", "'spend'", "'cost'", "'epc'"} {
		mustNot(t, spaBetween(html, "const KWS_COLS=", "\n"), foreign, "the statistics table has no foreign column "+foreign)
	}
	mustNot(t, block, "href=\"#/flows/", "a keyword row links nowhere")

	must(t, html, "'keyword_stats'];", "keyword_stats is a sort page of the saved views")
	must(t, spaFunctionLine(html, "defaultPageViewState"), "keyword_stats:{campaign:'', by:'', conds:[], page:1000}", "the default view holds keyword_stats")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "keyword_stats:{sort:sort(KWS_SORT)", "the snapshot holds keyword_stats")
	must(t, spaFunctionLine(html, "collectPageViews"), "out.keyword_stats.campaign", "collect keeps the campaign of keyword_stats")
	must(t, spaFunctionLine(html, "collectPageViews"), "out.keyword_stats.by", "collect keeps keyword attribution")
	must(t, spaFunctionLine(html, "applyPageViews"), "out.keyword_stats.conds", "apply reads the conditions of keyword_stats")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "keywords:{sort:sort(KEY_SORT)}", "the API key sort keeps its own page key")

	must(t, spaBetween(html, "const KWS_COLS=", "\n"), "['revenue','col.revenue']", "the revenue column has its own id, so a hidden cost does not remove it")
	mustNot(t, spaBetween(html, "const KWS_COLS=", "\n"), "['profit',", "no statistics column is called profit")
	must(t, block, "hiddenFields(ME).has('campaign_id')", "a role without campaign_id gets no campaign choice")
	must(t, block, "const camps=noCamp?''", "the campaign select is not drawn without campaign_id")
	must(t, block, "if(campSel) campSel.onchange", "the campaign handlers tolerate a missing select")
	must(t, spaFunctionLine(html, "kwsCampaign"), "hiddenFields(ME).has('campaign_id')?''", "the query ignores the saved campaign without campaign_id")
	must(t, block, "kwsPruneConds();", "the page entry prunes the hidden conditions before the chips are drawn")
	must(t, spaFunctionLine(html, "kwsQS"), "kwsCampaign()", "the query takes the campaign from kwsCampaign")
	must(t, spaFunctionLine(html, "applyPageViewSnapshot"), "KWS_SORT.k==='profit'", "a saved profit sort moves to revenue")
	must(t, block, "KWS_SORT.k=''", "a saved sort on a hidden column falls back to the server order")
	must(t, block, "new AbortController()", "a new statistics request cancels the previous one")
	must(t, block, "e.name==='AbortError'", "a cancelled request shows no error")
	must(t, spaFunctionLine(html, "render"), "kwsAbort()", "leaving the page cancels the requests")
	must(t, spaFunctionLine(html, "call"), "o.signal=opts.signal", "call passes the abort signal on")
	must(t, block, "sizes:TABLE_PAGE_SIZES, numbers:true", "the statistics use the numbered shared pager")
	mustNot(t, block, "tr('Newer')", "the keyword pager does not say Newer")
	mustNot(t, block, "tr('Older')", "the keyword pager does not say Older")
	mustNot(t, html, "tr('from ?q=')", "the lists tab no longer names only ?q=")
	mustNot(t, html, "'Collect keywords (?q=)'", "the collect switch no longer names only ?q=")
	must(t, spaFunctionLine(html, "exportPerf"), "csvTable(", "the performance CSV goes through csvTable")
	mustNot(t, spaFunctionLine(html, "exportPerf"), "join(',')", "the performance CSV is not joined by hand")
	must(t, spaFunctionLine(html, "csvTable"), "csvSafe(v)", "csvTable makes every cell formula-safe")
	must(t, spaFunctionLine(html, "csvSafe"), `/^[=+\-@\t\r]/`, "csvSafe covers = + - @ tab and carriage return like the server")
	if doc := routeDocs["GET /api/keywords/stats"]; doc.RU.Summary == "" || doc.Response == nil {
		t.Error("the API docs page has no Russian text or response sample for the keyword statistics")
	}
	must(t, spaFunctionLine(html, "keywords"), "arg==='lists'", "the lists tab lives at #/keywords/lists")
}

// TestSPAAboutWiring checks the static wiring of the settings tabs, the about tab and the first-run token field:
// the tab order, no trace of the old updates tab and its dictionary keys, the new keys in all three languages,
// the server block kept out of the polling loop and the token sent only when the field is on the form.
// t is the test handle. It returns nothing and fails on a missing marker.
func TestSPAAboutWiring(t *testing.T) {
	html := spaSource()
	tabs := spaBetween(html, "const SETTINGS_TABS=[", "\n];")
	last := -1
	for _, id := range []string{"profile", "users", "keys", "boards", "about"} {
		at := strings.Index(tabs, "id:'"+id+"'")
		if at <= last {
			t.Errorf("tab %q is missing or out of order in SETTINGS_TABS", id)
		}
		last = at
	}
	for _, id := range []string{"docs", "pbdocs", "updates"} {
		mustNot(t, tabs, "id:'"+id+"'", "docs pages and the old updates name are not tabs")
	}
	for _, gone := range []string{"settings/updates", "updatesPage", "data-ptab=\"updates\"", "'prof.title':", "'prof.tab':", "'upd.tab':", "'prof.updates':", "'menu.api':", "'menu.users':", "'upd.versions':"} {
		mustNot(t, html, gone, "the old updates tab and its keys are gone")
	}
	must(t, html, `href="#/settings/about"`, "the update banner leads to the about tab")
	must(t, spaFunctionLine(html, "userMenuItems"), "go('settings/about')", "the version line leads to the about tab")

	about := spaBetween(html, "async function aboutPage(", "\n// keyRevokedAt")
	if about == "" {
		t.Fatal("aboutPage not found in SPA")
	}
	if n := strings.Count(about, "get('/api/system',null)"); n != 1 {
		t.Errorf("aboutPage reads /api/system %d times, want once in loadServer", n)
	}
	must(t, about, "async function loadServer(){", "the server block has its own loader")
	poll := spaBetween(about, "setTimeout(()=>{", "},pollMs)")
	must(t, poll, "render();", "the polling loop redraws the update data")
	mustNot(t, poll, "loadServer", "the polling loop does not read the server block")
	must(t, about, "q('#about-sys-refresh',body).onclick=loadServer;", "the server block has its own button")
	must(t, about, "esc(ch.notes)", "what is new goes through esc")
	mustNot(t, about, "innerHTML=ch.notes", "what is new is never written raw")

	setup := spaFunctionLine(html, "setupView")
	must(t, setup, "tokenRequired=false", "the form takes the token flag")
	must(t, setup, "if(tokenInput) form.token=tokenInput.value.trim();", "the token is sent only when the field is on the form")
	must(t, setup, "setupView(setupMessage(e),'',ip,tokenRequired)", "the field stays after an error")
	must(t, spaFunctionLine(html, "boot"), "setupView('', '', setup.public_ip||'', !!setup.token_required)", "boot passes token_required")

	for _, key := range []string{
		"menu.settings", "about.tab", "about.version", "about.update", "about.server", "about.changelog", "about.stale", "about.steps.cmd", "about.server.err",
		"about.cpus", "about.load", "about.mem", "about.disk", "about.uptime", "about.free", "about.ms", "about.hb", "about.busy",
		"about.state.ok", "about.state.down", "about.state.not_configured", "setup.token", "setup.token.hint", "setup.token.bad",
		"upd.sign.off", "upd.sign.on", "upd.block.unsigned", "upd.notes",
	} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("key %q missing for lang %q", key, lang)
			}
		}
	}
	for lang, want := range map[string]string{
		"en": `'menu.settings':'Settings'`, "ru": `'menu.settings':'Настройки'`, "uk": `'menu.settings':'Налаштування'`,
	} {
		must(t, html, want, "the settings caption in "+lang)
	}
	must(t, html, `'about.tab':'About'`, "en about tab")
	must(t, html, `'about.tab':'О системе'`, "ru about tab")
	must(t, html, `'about.tab':'Про систему'`, "uk about tab")
	must(t, html, `'upd.block.unsigned':'Подпись включена, цель не подписана'`, "ru unsigned block text")
}

// TestSaveBusyAndFlowSkeleton checks the save button while a save is running and the empty campaign canvas.
// The button is disabled and named as in progress, a second click does not start another save, and an error
// hands the error to toast. The campaign page paints placeholder rows before it reads the streams.
func TestSaveBusyAndFlowSkeleton(t *testing.T) {
	html := spaSource()
	save := spaFunctionLine(html, "saveAll")
	if save == "" {
		t.Fatal("saveAll not found")
	}
	must(t, save, "if(SAVE_RUN) return", "a second click does not start another save")
	must(t, save, "SAVE_RUN=true; syncSaveBar()", "the button updates before the request")
	must(t, save, "if(flowsDirty()&&!block){await saveFlows(); await checkerFlushPending()}", "the save request is unchanged")
	must(t, save, "if(cronDirty())await saveCron()", "automation still saves from the same bar")
	must(t, save, "toast(e,true)", "an error is the translated toast")
	mustNot(t, save, "toast(e.message", "the raw answer is not the toast text")
	must(t, save, "finally{if(pending)SAVE_RUN=false; syncSaveBar()}", "the button returns when the request ends")
	bar := spaFunctionLine(html, "syncSaveBar")
	must(t, bar, "saveButtonFace(SAVE_RUN, !!block&&!scope.cron, tr('Save')+'<kbd>'+saveKbd((navigator.userAgentData&&navigator.userAgentData.platform)||navigator.platform, navigator.userAgent)+'</kbd>', tr('save.busy'))", "the button uses the in-progress face")
	must(t, bar, "saveBtn.disabled=face.disabled", "the in-progress button is disabled")
	must(t, bar, "saveBtn.setAttribute('aria-busy','true')", "the in-progress button is marked busy")
	face := spaFunctionLine(html, "saveButtonFace")
	must(t, face, `esc(busy)+'<span class="save-run" aria-hidden="true"><i></i></span>'`, "the in-progress caption keeps a visible bar")
	must(t, html, `.savebar #saveall[aria-busy="true"]{opacity:1;cursor:progress}`, "the busy button stays visible")
	must(t, html, `.savebar .save-run i{animation:none;width:100%;opacity:.7}`, "reduced motion keeps a still save bar")
	for _, pair := range []string{
		`'save.busy':'Saving…'`, `'save.busy':'Сохраняю…'`, `'save.busy':'Зберігаю…'`,
		`'flow.loading':'Loading streams…'`, `'flow.loading':'Загрузка потоков…'`, `'flow.loading':'Завантаження потоків…'`,
	} {
		must(t, html, pair, pair)
	}
	page := spaFunctionLine(html, "flowPage")
	waitAt := strings.Index(page, "flowPageWait(id)")
	loadAt := strings.Index(page, "await Promise.all")
	if waitAt < 0 || loadAt < 0 || waitAt > loadAt {
		t.Fatal("the empty campaign canvas does not show the skeleton before the load")
	}
	mustNot(t, page, "catch(", "a failed load stays the router's error")
	must(t, spaFunctionLine(html, "render"), `stateHTML(e&&e.message?errText(e):tr('ui.fail'),'err')`, "a failed load replaces the skeleton")
	skel := spaFunctionLine(html, "flowPageWait")
	must(t, skel, `class="frow skel-row"`, "the canvas has placeholder rows")
	must(t, skel, `aria-busy="true"`, "the canvas is marked loading")
	must(t, skel, `tr('flow.loading')`, "the canvas names the load")
	mustNot(t, skel, "data-s=", "the placeholders are not streams")
	must(t, html, `.skel-bar::after{animation:none;opacity:0}`, "reduced motion keeps the rows still")
	listFn := spaFunctionLine(html, "flows")
	listWait := strings.Index(listFn, "flowsListWait()")
	listLoad := strings.Index(listFn, "await Promise.all")
	if listWait < 0 || listLoad < 0 || listWait > listLoad {
		t.Fatal("the campaigns list does not show the skeleton before the load")
	}
	if strings.Contains(listFn[:listLoad], "try{") {
		t.Fatal("the campaigns list load is swallowed")
	}
	listSkel := spaFunctionLine(html, "flowsListWait")
	must(t, listSkel, `class="skel-row"`, "the campaigns list has placeholder rows")
	must(t, listSkel, `aria-busy="true"`, "the campaigns list is marked loading")
	must(t, listSkel, `tr('flows.list.loading')`, "the campaigns list names the load")
	mustNot(t, listSkel, "data-g=", "the placeholders are not campaigns")
	for _, pair := range []string{
		`'flows.list.loading':'Loading campaigns…'`, `'flows.list.loading':'Загрузка кампаний…'`, `'flows.list.loading':'Завантаження кампаній…'`,
		`'table.loading':'Loading…'`, `'table.loading':'Загрузка…'`, `'table.loading':'Завантаження…'`,
	} {
		must(t, html, pair, pair)
	}
	shared := spaFunctionLine(html, "tableWait")
	must(t, shared, `class="skel-row"`, "the shared skeleton has placeholder rows")
	must(t, shared, `aria-busy="true"`, "the shared skeleton is marked loading")
	must(t, shared, `tr('table.loading')`, "the shared skeleton names the load")
	mustNot(t, shared, "data-g=", "the shared placeholders are not records")
	hold := spaFunctionLine(html, "tableHold")
	must(t, hold, "return tableWait()", "a missing answer is the skeleton")
	emptyAt := strings.Index(hold, "if(!rows.length)")
	if emptyAt < 0 {
		t.Fatal("an empty answer has no branch")
	}
	mustNot(t, hold[emptyAt:], "skel-row", "an empty answer is not a skeleton")
	for _, name := range []string{"offers", "report", "networks"} {
		fn := spaFunctionLine(html, name)
		showAt := strings.Index(fn, "ListWait()")
		loadAt := strings.Index(fn, "await ")
		if showAt < 0 || loadAt < 0 || showAt > loadAt {
			t.Fatalf("%s does not show the skeleton before the load", name)
		}
	}
	must(t, spaFunctionLine(html, "offers"), "tr('off.empty')", "offers keep the empty state")
	must(t, spaFunctionLine(html, "paintReport"), "tr('rep.empty')", "the report keeps the empty state")
	must(t, html, `.skel-bar::after{animation:none;opacity:0}`, "reduced motion keeps the shared rows still")

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — markup checks already ran")
	}
	var b strings.Builder
	b.WriteString("let failed=0; const eq=(got,want,what)=>{const g=JSON.stringify(got),w=JSON.stringify(want); if(g!==w){failed++; console.log('FAIL '+what+'\\n  got  '+g+'\\n  want '+w);}};\n")
	b.WriteString("function esc(s){return String(s==null?'':s)} function tr(k){return k==='save.busy'?'Saving…':k} function icon(){return ''} function flowCompactClass(){return ''} let FLOW_COMPACT=false;\n")
	b.WriteString("function canCreate(){return true} function sectionAllowed(){return true} let ME=null;\n")
	b.WriteString(face)
	b.WriteString("\n")
	b.WriteString(skel)
	b.WriteString("\n")
	b.WriteString(listSkel)
	b.WriteString("\n")
	for _, name := range []string{"barStrip", "headStrips", "listBarWait", "dockWait"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found", name)
		}
		b.WriteString(fn)
		b.WriteString("\n")
	}
	b.WriteString(shared)
	b.WriteString("\n")
	b.WriteString(hold)
	b.WriteString("\n")
	b.WriteString("let SAVE_RUN=false, DRAWER=null, calls=0, release=null; const toasts=[]; const btn={disabled:false, html:'', attrs:{}, setAttribute(k,v){this.attrs[k]=v}, removeAttribute(k){delete this.attrs[k]}};\n")
	b.WriteString("function flowsDirty(){return true} function cronDirty(){return false} function saveBlock(){return ''} function markDirty(){}\n")
	b.WriteString("let flowsImpl=()=>{calls++; return new Promise(r=>{release=r})}; function saveFlows(){return flowsImpl()} function checkerFlushPending(){return Promise.resolve()} function saveCron(){return Promise.resolve()}\n")
	b.WriteString("function toast(e,err){toasts.push({e,err})} function syncSaveBar(){const f=saveButtonFace(SAVE_RUN,false,'Save<kbd>','Saving…'); btn.disabled=f.disabled; btn.html=f.html; if(f.busy)btn.setAttribute('aria-busy','true'); else btn.removeAttribute('aria-busy');}\n")
	if !strings.HasPrefix(save, "function saveAll(") {
		t.Fatal("saveAll extract did not start at the function")
	}
	b.WriteString("async ")
	b.WriteString(save)
	b.WriteString(`
(async()=>{
  const canvas=flowPageWait('camp-1');
  eq(canvas.includes('skel-row'), true, 'empty canvas has skeleton rows');
  eq(canvas.includes('aria-busy="true"'), true, 'empty canvas is busy');
  eq(canvas.includes('data-s='), false, 'skeleton rows are not streams');
  eq(canvas.includes('camp-1'), true, 'the open campaign id is the route');
  const list=flowsListWait();
  eq(list.includes('skel-row'), true, 'campaign list has skeleton rows');
  eq(list.includes('aria-busy="true"'), true, 'campaign list is busy');
  eq(list.includes('flows.list.loading'), true, 'campaign list names the load');
  eq(list.includes('data-g='), false, 'skeleton rows are not campaigns');
  eq((list.match(/skel-row/g)||[]).length>=8, true, 'several placeholder rows');
  eq(list.includes('id="fbar"'), true, 'campaign skeleton keeps the toolbar row');
  eq(list.includes('class="flowdock"'), true, 'campaign skeleton keeps the bottom panel');
  eq(list.includes('<button'), false, 'campaign skeleton controls are strips, not buttons');
  eq(list.includes('bar-strip n'), true, 'the counter is a short strip');
  const wait=tableWait();
  eq(wait.includes('skel-row'), true, 'shared skeleton has rows');
  eq(wait.includes('aria-busy="true"'), true, 'shared skeleton is busy');
  eq(wait.includes('table.loading'), true, 'shared skeleton names the load');
  eq(tableHold(null,'No offers yet').includes('skel-row'), true, 'loading offers is a skeleton');
  eq(tableHold([],'No offers yet').includes('skel-row'), false, 'empty offers is not a skeleton');
  eq(tableHold([],'No offers yet').includes('No offers yet'), true, 'empty offers says the list is empty');
  eq(tableHold([],'No rows').includes('skel-row'), false, 'empty report is not a skeleton');
  const p=saveAll();
  eq(calls, 1, 'one save request');
  eq(SAVE_RUN, true, 'the save is in progress');
  eq(btn.disabled, true, 'the button is disabled while saving');
  eq(btn.html.includes('Saving…'), true, 'the button says saving');
  eq(btn.html.includes('save-run'), true, 'the button shows progress');
  eq(btn.attrs['aria-busy'], 'true', 'the button is marked busy');
  saveAll();
  eq(calls, 1, 'a second click sends nothing');
  release();
  await p;
  eq(SAVE_RUN, false, 'success clears the in-progress mark');
  eq(btn.disabled, false, 'success returns the button');
  eq(btn.attrs['aria-busy'], undefined, 'success clears the busy mark');
  calls=0; toasts.length=0;
  const err=Object.assign(new Error('HTTP 500'),{status:500});
  flowsImpl=()=>{calls++; return Promise.reject(err)};
  await saveAll();
  eq(calls, 1, 'the failed save was one request');
  eq(SAVE_RUN, false, 'an error clears the in-progress mark');
  eq(btn.disabled, false, 'an error returns the button');
  eq(toasts[0]&&toasts[0].err, true, 'the error is an error toast');
  eq(toasts[0]&&toasts[0].e, err, 'the toast receives the error, not the raw code');
  if(failed){console.log(failed+' check(s) failed'); process.exit(1)}
})();
`)
	f := filepath.Join(t.TempDir(), "save-busy.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("save button and skeleton checks failed:\n%s", out)
	}
}

// TestListChromeSkeleton checks that a list keeps its own toolbar and bottom panel as strips while it loads.
// Pages that have no bottom panel do not gain one, and the strips are not buttons.
func TestListChromeSkeleton(t *testing.T) {
	html := spaSource()
	flows := spaFunctionLine(html, "flowsListWait")
	must(t, flows, `id:'fbar'`, "campaigns skeleton keeps the toolbar id")
	must(t, flows, "dockWait()", "campaigns skeleton keeps the bottom panel")
	mustNot(t, flows, "<button", "campaign skeleton controls are strips")
	for _, name := range []string{"offersListWait", "networksListWait"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s missing", name)
		}
		must(t, fn, "listBarWait(", name+" keeps the toolbar")
		must(t, fn, "dockWait()", name+" keeps the bottom panel")
		mustNot(t, fn, "<button", name+" controls are strips")
	}
	land := spaFunctionLine(html, "landingsListWait")
	must(t, land, "listBarWait(", "landings keep the toolbar")
	mustNot(t, land, "dockWait()", "landings have no bottom panel")
	mustNot(t, land, "<button", "landing controls are strips")
	dom := spaFunctionLine(html, "domainsListWait")
	must(t, dom, `id="dompage"`, "domains keep the pager row")
	must(t, dom, "listBarWait(", "domains keep the toolbar")
	mustNot(t, dom, "dockWait()", "domains have no export dock")
	rep := spaFunctionLine(html, "reportListWait")
	must(t, rep, `class="toolbar"`, "the report keeps its filter row")
	must(t, rep, "numberPagerWait(", "the report keeps both pagers")
	mustNot(t, rep, "dockWait()", "the report has no dock")
	logs := spaFunctionLine(html, "logsListWait")
	must(t, logs, `class="toolbar"`, "the log keeps its filter row")
	must(t, logs, "dockWait()", "the traffic log keeps the bottom panel")
	mustNot(t, logs, "<button", "log controls are strips")
	auto := spaFunctionLine(html, "automationWait")
	must(t, auto, "headStrips(", "automation keeps the heading control")
	mustNot(t, auto, "table-wait", "automation is not a table skeleton")
	mustNot(t, auto, "<button", "the automation control is a strip")
	for _, pair := range []struct{ name, call string }{
		{"offers", "offersListWait()"},
		{"networks", "networksListWait()"},
		{"landings", "landingsListWait()"},
		{"domains", "domainsListWait()"},
		{"report", "reportListWait()"},
		{"logs", "logsListWait()"},
	} {
		fn := spaFunctionLine(html, pair.name)
		showAt := strings.Index(fn, pair.call)
		loadAt := strings.Index(fn, "await ")
		if showAt < 0 || loadAt < 0 || showAt > loadAt {
			t.Fatalf("%s does not show the skeleton before the load", pair.name)
		}
	}
	page := spaFunctionLine(html, "automation")
	if strings.Index(page, "automationWait()") < 0 || strings.Index(page, "automationWait()") > strings.Index(page, "await ") {
		t.Fatal("automation skeleton is late")
	}
	mustNot(t, page, "pageTableWait(", "automation does not use the full-page table skeleton")
	users := spaFunctionLine(html, "usersPage")
	must(t, users, "barStrip(", "users keep the button row as strips while loading")
	must(t, users, `id="users-new"`, "the users button returns after the load")
}

// TestSaveBarPhone checks the unsaved-changes bar: a wide screen keeps one centred row,
// and up to 760px the label is the first row and the chips with Discard and Save
// are the second, both centred, 32px tall, above the home indicator.
// Discard is a filled secondary button, quieter than Save.
func TestSaveBarPhone(t *testing.T) {
	html := spaSource()
	wide := spaBetween(html, ".savebar{position:fixed;", "}")
	must(t, wide, "display:flex;align-items:center;", "a wide screen keeps the bar on one row")
	must(t, wide, "justify-content:center", "a wide screen centres the row in the bar")
	must(t, wide, "white-space:nowrap", "a wide screen does not wrap the bar")
	mustNot(t, wide, "flex-wrap:wrap", "a wide screen does not put the buttons on a second row")
	must(t, html, ".savebar .savehead,.savebar .saveacts{display:flex;align-items:center;justify-content:center;gap:12px;min-width:0}", "the label and the actions stay one centred row on a wide screen")
	disc := spaBetween(html, ".savebar .discard{", "}")
	must(t, disc, "background:var(--surface-2)", "discard uses a theme surface, not the primary fill")
	must(t, disc, "color:var(--fg)", "discard text contrasts with its fill")
	mustNot(t, disc, "background:transparent", "discard is not a bare label")
	mark := "@media (max-width:760px){\n    .savebar{left:max(8px, env(safe-area-inset-left, 0px));"
	i := strings.Index(html, mark)
	if i < 0 {
		t.Fatal("phone save bar rule not found")
	}
	phone := html[i:]
	if j := strings.Index(phone, "\n  button.aifab{"); j > 0 {
		phone = phone[:j]
	}
	must(t, phone, ".savebar .savehead{gap:8px;flex:1 0 100%;justify-content:center}", "the label takes the first row and stays centred")
	must(t, phone, ".savebar .saveacts{flex-wrap:wrap;align-items:center;justify-content:center;gap:8px;flex:1 0 100%}", "the buttons sit centred on the second row and wrap instead of clipping")
	must(t, phone, ".savebar .saveacts button,.savebar .dchip{flex:0 0 auto;height:32px;min-height:32px;line-height:1}", "the buttons keep a 32px touch height on a phone")
	must(t, phone, "bottom:calc(12px + env(safe-area-inset-bottom, 0px))", "the bar clears the home indicator")
	must(t, phone, "env(safe-area-inset-bottom, 0px)", "the page can scroll clear of the bar")
	if strings.Count(html, ".savebar .savehead{gap:8px;flex:1 0 100%;justify-content:center}") != 1 || strings.Count(html, ".savebar .saveacts{flex-wrap:wrap;align-items:center;justify-content:center;gap:8px;flex:1 0 100%}") != 1 {
		t.Error("the second row belongs only to the phone rule")
	}
	bar := spaFunctionLine(html, "syncSaveBar")
	must(t, bar, `class="savehead"`, "the label is its own row")
	must(t, bar, `class="saveacts"`, "the buttons are their own row")
	must(t, bar, `id="discard"`, "discard stays in the bar")
	must(t, bar, `id="saveall"`, "save stays in the bar")
}
