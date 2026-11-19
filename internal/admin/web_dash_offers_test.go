package admin

import (
	"strings"
	"testing"
)

// TestSPADashboardOffersBlock checks the offers block of the dashboard: it is one more entry of the stored
// «blocks» set that is off by default, the page draws it only when the set has it on, its request is the
// report for the dashboard query (same period and campaign), the columns are the ones the report already has,
// and the labels exist in the three languages.
func TestSPADashboardOffersBlock(t *testing.T) {
	html := spaSource()
	must(t, html, "blocks:['chart','perf','offers','visitors','recent']", "offers is a dashboard block")
	must(t, html, "const DASH_OFF_BY_DEFAULT={blocks:['offers']}", "the block is off until the account switches it on")
	must(t, spaFunctionLine(html, "dashSetRows"), "dashDefaultOn(name, id)", "an id the account never stored takes its default state")
	must(t, spaFunctionLine(html, "dashSetStored"), "r.on===dashDefaultOn(name, r.id)", "only a non-default state is stored")
	must(t, html, "offers:'dash.offers',visitors:'Who visits'", "the preferences window names the block")
	must(t, spaFunctionLine(html, "dashSetHidden"), "!pageAllowed(ME,'report')", "the block needs the report section")

	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, `id="dash-offers"`, "the block is drawn")
	must(t, dash, "dashRows('blocks').filter(r=>r.on).map(r=>blockHTML[r.id])", "only blocks switched on reach the page")
	must(t, dash, "loadDashOffers();", "the dashboard loads the block with the other cards")
	if n := strings.Count(html, "loadDashOffers("); n != 2 {
		t.Errorf("loadDashOffers must be defined once and called once, found %d mentions", n)
	}

	load := spaFunctionLine(html, "loadDashOffers")
	must(t, load, "if(!q('#dash-offers-body')) return;", "no block on the page, no request")
	must(t, load, "new URLSearchParams(dashQS())", "same period and campaign as the rest of the dashboard")
	must(t, load, "'/api/report?'", "the offers come from the report")
	must(t, load, "dashFoldOffers(reportData(body).rows)", "rows fold to the offer")
	for _, col := range []string{"tr('Hits')", "tr('Unique')", "tr('Conv')", "tr('col.revenue')"} {
		must(t, load, col, "column "+col)
	}
	for _, no := range []string{"Leads", "Sales", "EPC", "CPC", "col.cost", "Profit", "ROI", "col.roi"} {
		mustNot(t, load, no, "the block has no "+no+" column")
	}
	for _, lang := range []string{"'dash.offers':'Offers'", "'dash.offers':'Офферы'", "'dash.offers':'Офери'"} {
		must(t, html, lang, "label of the block")
	}
}
