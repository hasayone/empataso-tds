package admin

import (
	"strings"
	"testing"
)

// TestOfferDialogTabs checks the structure of the offer dialog: five tabs in the order Main, Action, Settings, Values,
// Notes; the redirect URL, the group and the network on the first one, with the status switch last; the action list on the
// second; the payout row and the daily limit on Settings; the values and the notes on their own tabs; the macro picker behind a button instead of a wall of chips;
// no stub of a local HTML offer; and the notes, values, action and limit sent with the offer.
func TestOfferDialogTabs(t *testing.T) {
	html := spaSource()
	must(t, html, "const OFFER_TABS=[['main','off.tab.main'],['action','off.tab.action'],['settings','off.tab.settings'],['values','off.tab.values'],['notes','off.tab.notes']];", "tabs in the order of the task")
	form := spaBetween(html, "const openOffer=(id)=>{", "const mkOff=canCreate")
	if form == "" {
		t.Fatal("openOffer not found")
	}
	pane := func(id string) string {
		start := strings.Index(form, "offerPaneOpen('"+id+"','main')")
		if start < 0 {
			t.Fatalf("the dialog has no %s pane", id)
		}
		rest := form[start+1:]
		if next := strings.Index(rest, "offerPaneOpen('"); next >= 0 {
			rest = rest[:next]
		}
		return rest
	}
	main, action, settings, values, notes := pane("main"), pane("action"), pane("settings"), pane("values"), pane("notes")
	for _, want := range []string{`id="oname"`, "groupFieldBlock('ogroup'", "offerStatusHTML(", `id="onet"`, `id="ourl"`, `id="omacro-btn"`} {
		must(t, main, want, "the Main tab has "+want)
	}
	mainOrder := []string{`id="oname"`, "groupFieldBlock('ogroup'", `id="onet"`, `id="ourl"`, `id="omacro-btn"`, "guardHint(true)", "offerStatusHTML("}
	for i := 1; i < len(mainOrder); i++ {
		if strings.Index(main, mainOrder[i-1]) > strings.Index(main, mainOrder[i]) {
			t.Errorf("the Main tab puts the status switch last: %s must come before %s", mainOrder[i-1], mainOrder[i])
		}
	}
	if n := strings.Count(main, "offerStatusHTML("); n != 1 {
		t.Errorf("the Main tab draws the status switch once, got %d", n)
	}
	for _, want := range []string{`id="opayout"`, `id="ocur"`, `id="oparam"`, "'omcpa'", "'omcpc'", "'oupson'", "'ocapyes'", `id="ocaptz"`, "offerLimitFields('cap'", "offerLimitFields('total'", "tagField('ocountries'", `id="ofallback"`, "off.fallback.none"} {
		must(t, settings, want, "the Settings tab has "+want)
	}
	order := []string{"tagField('ocountries'", "'omcpa'", `id="opayout"`, "'oupson'", "'ocapyes'", "offerLimitFields('cap'", "offerLimitFields('total'", `id="ocaptz"`, `id="ofallback"`}
	for i := 1; i < len(order); i++ {
		if strings.Index(settings, order[i-1]) > strings.Index(settings, order[i]) {
			t.Errorf("the Settings tab goes countries, payout, limit, spare: %s must come before %s", order[i-1], order[i])
		}
	}
	limsAt := strings.Index(settings, `<div class="off-lims">`)
	if limsAt < 0 {
		t.Fatal("the daily and total limits are not in one row")
	}
	limsEnd := strings.Index(settings[limsAt:], "</div>")
	if limsEnd < 0 {
		t.Fatal("the limit row is not closed")
	}
	lims := settings[limsAt : limsAt+limsEnd]
	must(t, lims, "offerLimitFields('cap'", "the daily limit is in its half")
	must(t, lims, "offerLimitFields('total'", "the total limit is in its half")
	mustNot(t, lims, `id="ocaptz"`, "the time zone stays under the limit row")
	must(t, html, `.off-lims{display:grid;grid-template-columns:50% 50%`, "each limit takes half the row")
	mustNot(t, main, `id="opayout"`, "the payout is not on the Main tab")
	must(t, action, "offerActionHTML(cur)", "the Action tab draws the answer")
	act := spaFunctionLine(html, "offerActionHTML")
	must(t, act, `id="oaction"`, "the Action tab has the list")
	for _, kind := range []string{"'404'", "'text'", "'html'", "'none'"} {
		must(t, act, kind, "the list has "+kind)
	}
	must(t, act, `id="oabody"`, "text and HTML share one plain field")
	must(t, act, "<textarea", "the action body is a textarea")
	mustNot(t, act, "ace.edit", "the action field is not the lander editor")
	mustNot(t, html, "off.action.campaign", "send to campaign is not an offer action")
	must(t, values, "offerValuesHTML(values)", "the Values tab draws the pairs")
	must(t, notes, `id="onotes"`, "the Notes tab has the large field")
	must(t, notes, "<textarea", "the notes field is a textarea")
	for _, gone := range []string{"off.local.soon", "HTML в разработке", `data-otab="local"`, "off.tab.local", "data-macro=\"${esc(token)}\"", "OFFER_MACROS.map(token", `id="omacro"`, "ocountry", "opaymodel"} {
		mustNot(t, html, gone, "the old dialog is gone: "+gone)
	}
	for _, want := range []string{
		"values:readOfferValues(box)", "notes:q('#onotes',box).value", "conv_cap:capYesNow&&cap.on", "conv_cap_limit:kept(cap.num, cur&&cur.conv_cap_limit)", "conv_cap_offer_id:(cur&&cur.conv_cap_offer_id)||''",
		"conv_total:capYesNow&&total.on", "conv_total_limit:kept(total.num, cur&&cur.conv_total_limit)", "conv_total_offer_id:(cur&&cur.conv_total_offer_id)||''",
		"fallback_id:q('#ofallback',box).value", "countries:readOfferCountries(box)", "payout_model:q('#omcpc',box).checked?'cpc':'cpa'", "upsell:!!(q('#oupson',box)&&q('#oupson',box).checked)", "cap_tz:(q('#ocaptz',box)&&q('#ocaptz',box).value)||''", "local:!!(cur&&cur.local)",
		"action:actionNow", "action_body:q('#oabody',box).value", "throw new Error(tr('off.action.empty'))",
	} {
		must(t, form, want, "the body of the save carries "+want)
	}
	must(t, form, "menu(macroBtn,[{node}],'macro-menu')", "the picker opens as a menu under the button")
	must(t, form, "token=>{urlIn.value=appendOfferMacro(urlIn.value,token)}", "a click writes key={macro} at the end of the address")
	must(t, form, "offerMacroNode(readOfferValues(m), ()=>urlIn.value,", "the picker reads the address to mark what is in it")
	must(t, form, "bindMacroOverlay(urlIn);", "the macros of the address are coloured")
	mustNot(t, form, `id="ourl" class="mono" style=`, "the address field is not stretched by its own style")
	must(t, form, `class="icon" id="onet-add"`, "the plus is as tall as the network field")
	mustNot(t, form, `class="icon sm" id="onet-add"`, "the plus is not the small button")
	must(t, form, "${offerStatusHTML(!cur||!!cur.status)}", "the status is the switch with the word on its left")
	mustNot(t, form, `<label class="tog"><input type="checkbox" id="ostatus"`, "the old pill is gone")
	mustNot(t, form, "syncOfferStatus", "a click only changes the status flag")
	must(t, spaFunctionLine(html, "offerStatusHTML"), `class="stat-sw"><span data-ostatus-lab>`, "the full word sits on the left of the switch")
	must(t, spaFunctionLine(html, "offerStatusHTML"), `id="ostatus"`, "saving still reads this checkbox")
	must(t, form, "status:!!(q('#ostatus',box)&&q('#ostatus',box).checked)", "saving still reads the switch")
	must(t, spaFunctionLine(html, "offerMacroGroups"), "OFFER_MACROS.filter(t=>!t.startsWith('{sub_id_'))", "the picker lists the macros the engine expands")
	must(t, spaFunctionLine(html, "offerMacroGroups"), "'{offer_value:'+n+'}'", "and one macro for each named value")
	lim := spaFunctionLine(html, "offerLimitFields")
	must(t, lim, "${on?'':' hidden'}", "a limit that is off hides the number")
	must(t, lim, `id="${id}n"`, "the number is there for a limit that is on")
	mustNot(t, lim, `id="${id}offer"`, "a limit has no offer list of its own")
	must(t, form, "bindLimit('ocap')", "the daily switch shows its number")
	must(t, form, "bindLimit('ototal')", "the total switch shows its number")
	must(t, form, "capSet.hidden=!q('#ocapyes',m).checked", "cap no hides the limits, the zone and the alternative")
	must(t, html, ".opane{min-height:260px}", "the short Main tab keeps a little empty band")
	for _, key := range []string{
		"off.tabs", "off.tab.main", "off.tab.action", "off.tab.settings", "off.tab.values", "off.tab.notes",
		"off.action", "off.action.help", "off.action.url", "off.action.404", "off.action.text", "off.action.html", "off.action.none", "off.action.body", "off.action.empty",
		"off.macro.open", "off.macro.search", "off.macro.none", "off.macro.main", "off.macro.sub", "off.macro.values",
		"off.values.hint", "off.values.name", "off.values.value", "off.values.add", "off.values.del", "off.notes.ph",
		"off.countries", "off.countries.ph", "off.countries.help", "off.cap", "off.cap.on", "off.cap.n", "off.cap.help", "off.cap.bad", "off.cap.offer", "off.cap.skip",
		"off.total", "off.total.on", "off.total.n", "off.total.help", "off.del.one",
		"off.paytype", "off.paytype.cpa", "off.paytype.cpc", "off.paytype.help",
		"off.upsell", "off.upsell.on", "off.upsell.off", "off.upsell.help",
		"off.status.on", "off.status.off",
		"off.capmode", "off.capmode.yes", "off.capmode.no", "off.capmode.help", "off.tz", "off.tz.help",
	} {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("%s is translated %d times, want 3", key, n)
		}
	}
	for _, key := range []string{"off.local", "off.tab.redirect", "off.tab.local", "off.local.soon", "off.model"} {
		if strings.Contains(html, "'"+key+"':") {
			t.Errorf("%s is still in a dictionary", key)
		}
	}
}

// TestOfferRowActions checks that the offers list has the same «More» button as campaigns and networks right
// after the name, with Edit, Turn on or off and a danger Delete that asks first; the head has the cell for it; there
// is no column of trash buttons; the cell is narrow; and DepSum stays in the picker of the offers page.
func TestOfferRowActions(t *testing.T) {
	html := spaSource()
	table := spaBetween(html, "const offActsTh=", "bindSort(grid, OFF_SORT")
	must(t, table, `'<th class="acts"></th>'`, "the head has the actions cell")
	must(t, table, `data-omore="${esc(o.id)}" title="${tr('More')}"`, "the row has the More button")
	must(t, table, "canEdit(ME,'offers',o)", "the button is for those who may edit")
	must(t, table, "offerRowHTML(o, netName, offActs(o), true, true)", "the cell is handed to the row, with the country button")
	mustNot(t, table, "icon('trash')", "no trash button in a cell")
	click := spaBetween(html, "qa('[data-omore]',grid)", "const openOffer=(id)=>{")
	must(t, click, "menu(e.currentTarget,[", "the same menu control as campaigns")
	must(t, click, "{label:tr('Edit'),icon:'edit',on:()=>openOffer(o.id)}", "the menu edits")
	must(t, click, "tr(o.status?'Turn off':'Turn on')", "the menu switches the offer")
	must(t, click, "{label:tr('Delete'),icon:'trash',danger:true,on:()=>deleteOffer(o)}", "delete lives in the menu")
	del := spaBetween(html, "const deleteOffer=async(o)=>{", "const paintTable=")
	must(t, del, "ask(trFill('off.del.one',{n:o.id}),'',tr('Delete'),true)", "the confirmation stays")
	must(t, del, "'DELETE','/api/offers/'+encodeURIComponent(o.id)", "the delete request")
	must(t, spaBetween(html, "const toggleOffer=async(o)=>{", "const deleteOffer"), "Object.assign({}, o, {status:!o.status})", "the switch sends the whole offer")
	must(t, spaFunctionLine(html, "offerRowHTML"), "${esc(o.name||'')}</span>${geoBtn}</span></td>${acts||''}", "the cell follows the name")
	must(t, spaFunctionLine(html, "offerRowHTML"), `<span class="dot ${offerDotClass(!!o.status)}"`, "the name has the status dot")
	for _, want := range []string{"kind+'-bulk-on'", "kind+'-bulk-off'"} {
		must(t, spaFunctionLine(html, "catalogBulkBar"), want, "the group bar of offers has "+want)
	}
	must(t, spaFunctionLine(html, "bindCatalogPick"), "catalogBulkStatus(kind, list, status, ctx)", "the buttons write the status of the ticked rows")
	must(t, spaFunctionLine(html, "catalogBulkStatus"), "Object.assign({}, r, {status})", "the whole record is sent with the new status")
	must(t, spaFunctionLine(html, "catalogBulkStatus"), "if(!!r.status!==status){", "a row that already has it is not written")
	must(t, html, "#offers-grid td.acts,#offers-grid th.acts{padding-left:4px;padding-right:8px}", "the cell is narrow")
	must(t, spaBetween(html, "const netActs=", "\n"), `data-nmore="${esc(n.id)}" title="${tr('More')}"`, "networks have the same More button")
	must(t, html, "{label:tr('Delete'),icon:'trash',danger:true,on:()=>deleteNet(n)}", "and delete lives in their menu")
	must(t, spaFunctionLine(html, "networkRowHTML"), "<td>${esc(n.name||'')}</td>${acts||''}", "the network cell follows the name")
	offers := spaFunctionLine(html, "colCatalog")
	start := strings.Index(offers, "if(kind==='offers') return [")
	if start < 0 {
		t.Fatal("offers catalog not found")
	}
	block := offers[start:]
	block = block[:strings.Index(block, "];")]
	must(t, block, "DEP_COL", "DepSum is in the picker of the offers page")
	must(t, html, "const DEP_COL=['depsum','col.depsum',0];", "and it is the deposit column")
	must(t, block, "['revenue','col.revenue',1]", "revenue stays the payout column")
}
