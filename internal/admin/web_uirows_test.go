package admin

import (
	"strings"
	"testing"
)

// TestOfferPayoutRow checks that the offer dialog keeps the amount, the currency, the payout parameter tick and
// its question mark in one row, that the currency is not stretched, and that the help says what the flag does:
// the offer only stores it and neither the engine nor the reports read it.
func TestOfferPayoutRow(t *testing.T) {
	html := spaSource()
	start := strings.Index(html, "const openOffer=(id)=>{")
	if start < 0 {
		t.Fatal("openOffer not found")
	}
	form := html[start:]
	form = form[:strings.Index(form, "payout_param:!!(q('#oparam',box)")]
	row := form[strings.Index(form, `<div class="pay-row">`):]
	row = row[:strings.Index(row, "</div></div>")]
	idx := func(s string) int {
		i := strings.Index(row, s)
		if i < 0 {
			t.Fatalf("the payout row has no %q", s)
		}
		return i
	}
	if !(idx(`id="opayout"`) < idx(`id="ocur"`) && idx(`id="ocur"`) < idx(`id="oparam"`) && idx(`id="oparam"`) < idx("helpMark(")) {
		t.Error("the row order is amount, currency, tick, question mark")
	}
	mustNot(t, form, `<div class="g2"><div class="fld"><label>${tr('off.payout')}</label>`, "the amount and the currency are no longer two grid cells")
	must(t, form, "OFFER_CURRENCIES.map(c=>opt(c,c,payCur)).join('')", "the list of currencies is whole")
	must(t, html, ".fld .pay-row select{width:auto;flex:none}", "the currency is as wide as its longest code")
	must(t, html, ".fld .pay-row input.pay-amt[type=number]{width:120px;flex:none}", "the amount is narrow and beats the full-width field rule")
	must(t, html, "function helpPopBox(", "the popup chooses the side that fits inside the block")
	must(t, spaFunctionLine(html, "helpPopBox"), "const toRight=w<=roomRight||roomRight>=roomLeft;", "no room on the right opens the popup to the left")
	must(t, spaFunctionLine(html, "helpPopBox"), "const down=h<=roomBelow||roomBelow>=roomAbove;", "no room below opens the popup upward")
	mustNot(t, html, ".qh-up .qh-pop{", "the popup is not pinned to the bottom-right corner")
	must(t, spaFunctionLine(html, "helpMark"), `role="note"`, "the mark is a note")
	must(t, form, "helpMark(tr('off.payout.param'), esc(tr('off.payout.help')))", "the tick has a question mark")
	for _, key := range []string{"off.payout.help"} {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("%s is translated %d times, want 3", key, n)
		}
	}
	ru := html[strings.Index(html, "'off.payout.help':'Включено"):]
	ru = ru[:strings.Index(ru, "',")]
	for _, word := range []string{"параметра payout постбека", "если его нет или он пуст", "сумма из этой строки", "только в USD", "ничего не подставляется"} {
		must(t, ru, word, "the help tells what the flag does in the engine")
	}
	for _, old := range []string{"не читают", "только хранится", "only stored"} {
		mustNot(t, html, old, "the help no longer says the engine ignores the tick")
	}
}

// TestNetworkRowActions checks that the networks list has no column of red trash buttons: the row has the same
// «More» button as campaigns right after the name, the menu carries Edit and a danger Delete that still asks
// first, the delete is only offered to a person who may edit the network, and the percent cells are untouched.
func TestNetworkRowActions(t *testing.T) {
	html := spaSource()
	mustNot(t, html, "data-ndel", "no trash column of the networks list")
	table := spaBetween(html, "const netHead=cols.filter", "bindSort(grid, NET_SORT")
	must(t, table, `'<th class="acts"></th>'`, "the head has the actions cell after the name")
	must(t, table, `data-nmore="${esc(n.id)}" title="${tr('More')}"`, "the row has the More button")
	must(t, table, "canEdit(ME,'networks',n)", "the button is for those who may edit")
	mustNot(t, table, "icon('trash')", "no trash button in a cell")
	must(t, table, "{label:tr('Delete'),icon:'trash',danger:true,on:()=>deleteNet(n)}", "delete lives in the menu")
	must(t, table, "menu(e.currentTarget,[", "the same menu control as campaigns")
	del := spaBetween(html, "const deleteNet=async(n)=>{", "const networkRows=")
	must(t, del, "ask(tr('Delete network')+' '+n.id+'?','',tr('Delete'),true)", "the confirmation stays")
	must(t, del, "'DELETE','/api/affiliate-networks/'+encodeURIComponent(n.id)", "the same request")
	must(t, spaFunctionLine(html, "networkRowHTML"), "<td>${esc(n.name||'')}</td>${acts||''}", "the actions cell follows the name")
	must(t, spaFunctionLine(html, "networkRowHTML"), "return networkCells(m, c.id);", "the percent and money cells are untouched")
	must(t, html, "#networks-grid td.acts,#networks-grid th.acts{padding-left:4px;padding-right:8px}", "the cell is narrow")
}

// TestGroupsWindowParts checks that the groups window is one component for every page that opens it and that
// the name of a new group, the search and the table are separate nodes: the name field is on top and is the
// only source of the new group, the search sits below a divider over the list, and the buttons are the footer of
// the dialog, outside the table with room above them.
func TestGroupsWindowParts(t *testing.T) {
	html := spaSource()
	fn := spaFunctionLine(html, "openGroupWindow")
	if n := strings.Count(html, `id="gnew"`); n != 1 {
		t.Errorf("the name field is written %d times, want one component", n)
	}
	if n := strings.Count(html, "openGroupWindow({"); n < 3 {
		t.Errorf("only %d pages open the shared window, want at least 3", n)
	}
	i, s, l, tb := strings.Index(fn, `id="gnew"`), strings.Index(fn, `id="gsearch"`), strings.Index(fn, `class="grp-list"`), strings.Index(fn, "<table>")
	if !(i >= 0 && i < l && l < s && s < tb) {
		t.Errorf("order is name, list block, search, table: %d %d %d %d", i, l, s, tb)
	}
	table := fn[tb:strings.Index(fn, "</table>")]
	for _, bad := range []string{"data-yes", "data-no", `id="gnew"`, `id="gsearch"`} {
		mustNot(t, table, bad, "the table holds only the groups")
	}
	must(t, fn, `<label for="gnew">${tr('off.group')}</label>`, "the name field has its label")
	must(t, fn, `type="search"`, "the search is a search field")
	must(t, fn, "const name=q('#gnew',box).value.trim();", "a group is created from the name field")
	must(t, fn, "if(name&&opts.onCreate) opts.onCreate(name);", "an empty name creates nothing")
	must(t, fn, "tr('Create'))", "the footer button keeps its label")
	must(t, html, ".modal .grp-list{margin:18px 0 16px;padding-top:16px;border-top:1px solid var(--border)}", "the list is set apart and keeps room before the buttons")
}

// TestAssistantButtonExpands checks the assistant button: at rest only the icon is visible and the button is
// translucent, the text and the beta mark live in a label that is collapsed until hover or keyboard focus, then
// the label opens and the button turns opaque. The button is still the opener of the panel with its title.
func TestAssistantButtonExpands(t *testing.T) {
	html := spaSource()
	fn := spaFunctionLine(html, "aiMountFab")
	lbl := strings.Index(fn, `<span class="aif-lbl">`)
	if lbl < 0 || strings.Index(fn, `class="aib"`) < lbl || strings.Index(fn, "tr('ai.chat.fab')") < lbl {
		t.Error("the text and the beta mark sit only inside the collapsible label")
	}
	if strings.Count(fn, `class="aib"`) != 1 {
		t.Error("the beta mark is written once")
	}
	must(t, fn, `title="${esc(tr('ai.chat.open'))}" aria-label="${esc(tr('ai.chat.open'))}"`, "the name stays for the icon alone")
	must(t, html, "font-weight:650;opacity:.7;transition:opacity .18s ease,background-color .12s}", "translucent at rest")
	must(t, html, "button.aifab:hover,button.aifab:focus-visible{opacity:1}", "opaque on hover and focus")
	must(t, html, ".aifab .aif-lbl{display:inline-grid;grid-template-columns:0fr;transition:grid-template-columns .2s ease}", "the label is collapsed at rest")
	must(t, html, ".aifab:hover .aif-lbl,.aifab:focus-visible .aif-lbl{grid-template-columns:1fr}", "hover and focus open it the same way")
	must(t, html, "button.aifab{position:fixed;right:20px;", "the button stays at its right edge and grows to the left")
	must(t, html, "@media (prefers-reduced-motion:reduce){button.aifab,.aifab .aif-lbl{transition:none}}", "reduced motion is respected")
}
