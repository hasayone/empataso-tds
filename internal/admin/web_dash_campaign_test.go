//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDashCampaignLogic runs the dashboard campaign helpers under node: the kept campaign becomes one
// campaign_id condition, only for an account that may filter by it, and its label and search rows name
// the campaign by name, id, number and aliases.
func TestDashCampaignLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the dashboard campaign checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("let DASH_VIEW={}, DASH={conds:[]}, ME=null, HIDDEN=new Set(), GROUPS=[];\n")
	src.WriteString("const hiddenFields=()=>HIDDEN;\n")
	src.WriteString("const groupById=(id)=>(GROUPS||[]).find(g=>g.id===id||(g.aliases||[]).includes(id))||null;\n")
	for _, name := range []string{"dashCampaignId", "dashConds", "dashCampaignLabel", "dashCampaignItems"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); } };

eq(dashCampaignId(), '', 'nothing kept: all campaigns');
eq(dashConds(), [], 'nothing kept: no condition');

DASH_VIEW.campaign = 'all-offers';
eq(dashCampaignId(), 'all-offers', 'the kept campaign is read');
eq(dashConds(), [{f:'campaign_id', op:'eq', v:['all-offers']}], 'the kept campaign is one campaign_id condition');
DASH.conds = [{f:'country', op:'eq', v:['ru']}];
eq(dashConds().map(c => c.f), ['country', 'campaign_id'], 'the kept campaign joins the other conditions');
eq(DASH.conds.length, 1, 'the dashboard own conditions are not changed');

HIDDEN = new Set(['campaign_id']);
eq(dashCampaignId(), '', 'an account that may not filter by campaign gets all campaigns');
eq(dashConds().map(c => c.f), ['country'], 'no campaign condition for it, so the server never answers 400');
HIDDEN = new Set();

DASH_VIEW.campaign = 7;
eq(dashCampaignId(), '', 'a kept value that is not text is no campaign');
DASH_VIEW.campaign = 'all-offers';

GROUPS = [{id:'all-offers', name:'All offers', num:12, aliases:['ao']}, {id:'solo', name:'solo'}, {id:'', name:'broken'}];
eq(dashCampaignLabel('all-offers'), 'All offers · all-offers', 'name and id');
eq(dashCampaignLabel('ao'), 'All offers · all-offers', 'an alias names its campaign');
eq(dashCampaignLabel('solo'), 'solo', 'a campaign named like its id shows once');
eq(dashCampaignLabel('gone'), 'gone', 'an unknown id shows as it is');
eq(dashCampaignItems(GROUPS), [
  {key:'all-offers', label:'All offers · all-offers', alt:'12 ao'},
  {key:'solo', label:'solo', alt:''},
], 'search rows skip broken campaigns and carry number and aliases');
eq(dashCampaignItems(null), [], 'no campaigns');
eq(dashCampaignItems([null, {id:'x', name:'x'}]).length, 1, 'an empty slot is skipped');

if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "dash_campaign.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("dashboard campaign checks failed:\n%s", out)
	}
}

// TestDashCampaignWiring checks that every dashboard request takes its filter from dashConds and that the
// kept campaign is stored on the account with the other dashboard views.
func TestDashCampaignWiring(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "dashQS"), "condsQS(dashConds())", "the summary, chart and facts share one filter")
	must(t, spaFunctionLine(html, "loadBreakdown"), "condsQS(dashConds())", "the breakdown cards use the kept campaign")
	must(t, spaFunctionLine(html, "loadRecent"), "dashQS()", "recent clicks use the kept campaign")
	must(t, spaFunctionLine(html, "pageViewFromGlobals"), "snap.dashCampaign", "the campaign goes into the saved views")
	must(t, spaFunctionLine(html, "applyPageViewSnapshot"), "DASH_VIEW.campaign=snap.dashCampaign", "the saved campaign comes back after a reload")
	must(t, spaFunctionLine(html, "resetPageViewsToDefaults"), "delete DASH_VIEW.campaign", "another account does not inherit the choice")
	must(t, spaFunctionLine(html, "dashSetCampaign"), "flushPageViews()", "a choice and its reset are written at once")
}
