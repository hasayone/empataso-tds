package admin

import (
	"strings"
	"testing"
)

// TestSPAEntityNamesAreLinks checks that journal tables draw offers, networks,
// landings, sources and campaigns through the one entityHTML helper: the name is
// the link, the id stays in the tooltip, a missing entity is plain text and the
// deleted mark needs an unrestricted session. It also checks that the catalog
// pages open an editor from #/page/{id} and that the label exists in every language.
func TestSPAEntityNamesAreLinks(t *testing.T) {
	html := spaSource()

	kinds := spaFunctionLine(html, "entityRow")
	if kinds == "" {
		t.Fatal("entityRow not found in SPA")
	}
	for _, f := range []string{"offer_id:{page:'offers'", "landing_id:{page:'landings'", "network_id:{page:'networks'", "source_id:{page:'sources'", "campaign_id:{page:'flows'"} {
		must(t, html, f, "entity kind "+f)
	}
	must(t, kinds, "groupById(String(id))", "campaign resolved through the shared lookup, aliases included")

	ent := spaFunctionLine(html, "entityHTML")
	must(t, ent, `<a class="elink" href="#/${kind.page}/${encodeURIComponent(row.id)}"`, "name is the link to the edit address")
	must(t, ent, `title="${esc(s)}"`, "id stays in the tooltip")
	must(t, ent, "pageAllowed(ME, kind.page)", "no link to a page the session may not open")
	must(t, ent, "!(ME&&ME.access)", "deleted mark only for an unrestricted session")
	must(t, ent, "tr('ent.gone')", "deleted mark is translated")
	must(t, ent, "if(!name) return esc(s)", "a nameless row is never a link")

	routed := spaFunctionLine(html, "openRouted")
	must(t, routed, "history.replaceState(null,'','#/'+page)", "the id leaves the address before the editor opens")
	must(t, routed, "canEdit(ME, res, row)", "editor opens only for a row the session may edit")

	conv := spaFunctionLine(html, "convCell")
	logc := spaFunctionLine(html, "logCell")
	for name, src := range map[string]string{"convCell": conv, "logCell": logc} {
		must(t, src, "entityHTML(id, x[id])", name+" offer, landing, network and source cells")
		must(t, src, "entityHTML('campaign_id', x.campaign_id, x.group)", name+" campaign cell")
		mustNot(t, src, "esc(idLabel(id, x[id]))", name+" no bare id cell")
	}
	must(t, html, "if(!e.target.closest('a')) logDetail(", "a link in a click-log row does not open the drawer")
	must(t, html, "const entityCol={offer:['offer_id','offer'], source:['source_id','source']};", "report offer and source columns")

	for _, page := range []string{"offers", "networks", "landings", "sources"} {
		must(t, html, "async function "+page+"(arg,live)", page+" page takes the address argument")
		must(t, html, "openRouted('"+page+"', '"+page+"', rows, arg,", page+" page opens the editor from the address")
	}

	if n := strings.Count(html, "'ent.gone':"); n != 3 {
		t.Errorf("ent.gone is defined %d times, want 3 (en, ru, uk)", n)
	}
}

// TestSPAOriginCampaignIDIsText checks that the origin-campaign id column of the click
// log and the conversion log prints the stored slug as text. The name column stays the
// link to #/flows/{slug}. Campaign, offer, landing, network and source id columns stay
// plain text as well; the report has no id column that draws a campaign name.
func TestSPAOriginCampaignIDIsText(t *testing.T) {
	html := spaSource()
	for name, src := range map[string]string{
		"logCell":  spaFunctionLine(html, "logCell"),
		"convCell": spaFunctionLine(html, "convCell"),
	} {
		if src == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		must(t, src, "entityHTML('campaign_id', x.origin_campaign_id, x.origin_campaign)", name+" origin name stays a link")
		must(t, src, "if(id==='origin_campaign_id') return esc(x.origin_campaign_id||'')", name+" origin id is the stored slug")
		mustNot(t, src, "if(id==='origin_campaign_id') return entityHTML", name+" origin id is not a campaign link")
		for _, id := range []string{"campaign_id", "offer_num", "landing_num", "network_num", "source_num"} {
			mustNot(t, src, "if(id==='"+id+"') return entityHTML", name+" "+id+" is not a name link")
		}
	}
	must(t, spaFunctionLine(html, "cellClass"), "id==='origin_campaign_id'", "origin id is monospace like the other ids")
	must(t, html, "const entityCol={offer:['offer_id','offer'], source:['source_id','source']};", "report links names, not campaign ids")
}
