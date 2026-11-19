package admin

import (
	"strings"
	"testing"
)

// TestSPAOffersNetworkFilter checks that the offers toolbar carries an affiliate network filter beside
// the group filter, that it cuts the table and the export by the offer's network_id the same way the group
// filter does, that it counts as an active filter, and that no other catalog page gets it.
func TestSPAOffersNetworkFilter(t *testing.T) {
	html := spaSource()
	bar := spaBetween(html, "function catalogBar(", "function bindCatalogBar(")
	must(t, bar, `id="${kind}-net" data-pick`, "the network select, searchable like the group one")
	must(t, bar, "opts.net?", "the network select only when a page asks for it")
	if net, group := strings.Index(bar, `${net}<select id="${kind}-group"`), strings.Index(bar, `<div class="rgt">`); net < 0 || group < 0 || net < group {
		t.Errorf("the network filter must open the same right-hand row as the group filter (net at %d, row at %d)", net, group)
	}
	page := spaBetween(html, "async function offers(arg,live){", "const openOffer=")
	must(t, page, "matchesNetwork(o, CAT_NET.offers)", "the offer rows are cut by network")
	must(t, page, "fillNetSelect(q('#offers-net'), nets, rows, CAT_NET.offers)", "the options follow the loaded offers")
	exp := spaBetween(html, "function exportOffers(", "const offMeta")
	must(t, exp, "matchesNetwork(o, CAT_NET.offers)", "the export keeps what the table shows")
	must(t, spaBetween(html, "function catalogFiltered(", "function resolveGroupName("), "CAT_NET[kind]", "an active network filter changes the empty message")
	if n := strings.Count(html, "net:true})"); n != 1 {
		t.Errorf("the network filter belongs to the offers page only, found %d toolbars asking for it", n)
	}
	for _, label := range []string{"'off.allnets':'Все сети'", "'off.allnets':'All networks'", "'off.allnets':'Усі мережі'"} {
		must(t, html, label, "the label of the default option")
	}
}
