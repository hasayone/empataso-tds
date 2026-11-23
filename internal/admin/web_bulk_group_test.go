package admin

import (
	"strings"
	"testing"
)

// TestSPACatalogBulkActions checks the group actions of the offers and networks tables: a checkbox column with a
// header box for the page, a bar that exists only while rows are ticked, one PUT or DELETE per row without
// Promise.all, a confirmation with the number of rows, the right to change a row checked per row, and the
// labels in the three languages. The row click must ignore the checkbox cell.
func TestSPACatalogBulkActions(t *testing.T) {
	html := spaSource()
	for _, page := range []struct{ name, fn, kind string }{
		{"networks", spaBetween(html, "async function networks(", "async function offers("), "networks"},
		{"offers", spaBetween(html, "async function offers(arg,live){", "const openOffer="), "offers"},
	} {
		must(t, page.fn, "catalogPickSync('"+page.kind+"'", page.name+": ticked ids follow the filter")
		must(t, page.fn, "catalogPickHead('"+page.kind+"'", page.name+": header box")
		must(t, page.fn, "catalogPickCell('"+page.kind+"'", page.name+": row box")
		must(t, page.fn, "catalogBulkBar('"+page.kind+"'", page.name+": bar of group actions")
		must(t, page.fn, "bindCatalogPick(grid, '"+page.kind+"'", page.name+": checkboxes and buttons are wired")
		must(t, page.fn, ",.pick", page.name+": a click in the box cell does not open the editor")
	}
	must(t, spaFunctionLine(html, "catalogBulkBar"), "n=CAT_PICK[kind].size", "the bar needs a selection")
	must(t, spaFunctionLine(html, "catalogBulkBar"), "if(!n) return ''", "no bar without a selection")
	must(t, spaFunctionLine(html, "catalogBulkBar"), "bulkAct(kind+'-bulk-group','lists',tr('dom.group.set'),false)", "group button")
	must(t, spaFunctionLine(html, "catalogBulkBar"), "bulkAct(kind+'-bulk-del','trash',tr('flows.bulk.del'),true)", "delete button")
	must(t, spaFunctionLine(html, "catalogPickHead"), `data-cpick="*"`, "header box ticks the page")
	must(t, spaFunctionLine(html, "catalogPickHead"), "canEdit(ME,kind,r)", "header box counts only changeable rows")
	must(t, spaFunctionLine(html, "catalogPickCell"), "canEdit(ME,kind,row)", "a row the role may not change has no box")
	bind := spaFunctionLine(html, "bindCatalogPick")
	must(t, bind, "if(list.length)", "buttons do nothing without a selection")
	must(t, bind, "pickSpan(", "Shift ticks a span like the other tables")
	group := spaFunctionLine(html, "catalogBulkGroup")
	must(t, group, "'PUT', CAT_API[kind]+'/'+encodeURIComponent(r.id)", "group is written row by row")
	must(t, group, "for(const r of list)", "rows go one after another")
	mustNot(t, group, "Promise.all", "group must not fire all requests at once")
	del := spaFunctionLine(html, "catalogBulkDelete")
	must(t, del, "trFill(CAT_DEL_KEY[kind],{n:list.length})", "the confirmation carries the number")
	must(t, del, "'DELETE', CAT_API[kind]+'/'+encodeURIComponent(r.id)", "delete is written row by row")
	mustNot(t, del, "Promise.all", "delete must not fire all requests at once")
	must(t, html, "const CAT_API={offers:'/api/offers', networks:'/api/affiliate-networks'}", "collections of the two lists")
	for _, label := range []string{
		"'cat.pick.page':'Select all on this page'", "'cat.pick.page':'Выбрать все на этой странице'", "'cat.pick.page':'Вибрати все на цій сторінці'",
		"'net.del.many':'Delete {n} networks?'", "'net.del.many':'Удалить сетей: {n}?'", "'net.del.many':'Видалити мереж: {n}?'",
		"'off.del.many':'Delete {n} offers?'", "'off.del.many':'Удалить офферов: {n}?'", "'off.del.many':'Видалити оферів: {n}?'",
	} {
		must(t, html, label, "label")
	}
}

// TestSPAFlowBulkGroup checks the campaigns table: the bulk bar gets a group action for the ticked campaigns the role
// may change, it is written one PUT per campaign from the last saved body (never the whole list, which gave HTTP 413),
// and the bar still has no new way to delete campaigns.
func TestSPAFlowBulkGroup(t *testing.T) {
	html := spaSource()
	cards := spaBetween(html, "function paintFlowCards(){", "grid.className='fgrid'")
	must(t, cards, "bulkAct('bulk-group','lists',tr('dom.group.set'),false)", "group action in the bar")
	must(t, cards, "FLOW_PICK.has(g.id)&&canEdit(ME,'campaigns',g)", "the action needs a campaign the role may change")
	must(t, cards, "picked().filter(g=>canEdit(ME,'campaigns',g))", "only changeable campaigns are written")
	must(t, cards, "if(list.length) openFlowFolderPick(list)", "no selection, no action")
	if n := strings.Count(cards, "bulkAct('bulk-del'"); n != 1 {
		t.Errorf("campaigns keep exactly the one existing delete action, found %d", n)
	}
	set := spaFunctionLine(html, "setCampaignFolder")
	if set == "" {
		t.Fatal("setCampaignFolder missing")
	}
	must(t, set, "for(const g of list)", "campaigns go one after another")
	must(t, set, "await call('PUT','/api/groups/'+encodeURIComponent(prev.id), saved)", "one PUT per saved campaign")
	mustNot(t, set, "Promise.all", "must not fire all requests at once")
	mustNot(t, set, "call('PUT','/api/groups',", "the whole list is never sent in one PUT")
	must(t, set, "groupParse(prev.json)", "the body is the last saved one, so drafts stay local")
	must(t, spaFunctionLine(html, "openFlowFolderPick"), "setCampaignFolder(list, label)", "the dialog uses the shared writer")
}
