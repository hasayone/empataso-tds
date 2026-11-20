//go:build uitest

// The dashboard skeleton follows the blocks the account has on. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestDashSkel
package admin

import "testing"

// TestDashSkel checks that the summary paints its own skeleton, not the shared table placeholder,
// and that the skeleton repeats the blocks switched on and leaves out a switched-off chart and switched-off offers.
func TestDashSkel(t *testing.T) {
	html := spaSource()
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	mustNot(t, dash, "pageTableWait(", "the summary does not use the full-page table skeleton")
	must(t, dash, "dashSkelHTML(dashSkelPlan())", "the summary paints its own skeleton while loading")
	must(t, dash, `id="dash-head"`, "the header stays on the page")
	must(t, dash, `id="dash-refresh" title="${tr('Refresh')}" aria-label="${tr('Refresh')}">${icon('refresh')}</button><button type="button" class="icon" id="dash-prefs"`, "the header keeps refresh and the gear")
	mustNot(t, spaFunctionLine(html, "dashSkelHTML"), "table-wait", "the summary skeleton is not the shared table placeholder")
	mustNot(t, spaFunctionLine(html, "dashSkelHTML"), "skel-note", "the strips have no loading caption")
	must(t, html, ".dash-skel-plot{height:260px", "the chart strip is the canvas height")
	must(t, html, ".kpi.skel .l{min-height:1.5em}", "a card strip keeps the label line")
	must(t, html, ".kpi.skel .v{min-height:1.2em}", "a card strip keeps the value line")
	must(t, html, `.dash-skel-plot::after{animation:none;opacity:0}`, "reduced motion keeps the chart strip still")

	runSPANode(t, nodeEq+`
const store={};
const localStorage={
  getItem(k){return Object.prototype.hasOwnProperty.call(store,k)?store[k]:null},
  setItem(k,v){store[k]=String(v)},
  removeItem(k){delete store[k]}
};
let ME={uid:'u1', user:'u1'};
let dashRows=()=>[];
let hiddenCols=()=>new Set();
let dashDims=()=>[];
let dashPanelOn=()=>false;
let dashExtraDim=()=>'';
`, []string{
		"dashSkelUser", "dashSkelKey", "dashSkelRead", "dashSkelNote", "dashSkelCount", "dashSkelShape",
		"dashSkelBar", "dashSkelGrid", "dashSkelHTML", "dashSkelPerfCols", "dashSkelOfferCols", "dashSkelPanels", "dashSkelPlan",
	}, `
const text = html => html.replace(/<[^>]*>/g, '').replace(/\s+/g, '');
const rows = html => (html.match(/<tr /g) || []).length;

// Enabled blocks are repeated. A switched-off chart and switched-off offers are not drawn.
dashRows = name => {
  if (name === 'kpis') return [{id:'visits',on:true},{id:'unique',on:true},{id:'conv',on:true},{id:'cost',on:true},{id:'cr',on:true}];
  if (name === 'blocks') return [{id:'chart',on:true},{id:'perf',on:true},{id:'offers',on:true},{id:'visitors',on:true},{id:'recent',on:false}];
  if (name === 'recent') return [{id:'ts',on:true},{id:'ip',on:true}];
  return [];
};
hiddenCols = () => new Set();
dashDims = () => [{id:'country'},{id:'device'}];
dashPanelOn = () => true;
const on = dashSkelPlan();
eq(on.blocks, ['chart','perf','offers','visitors'], 'the skeleton repeats the blocks that are on');
eq(on.kpis, ['visits','unique','conv','cost','cr'], 'one strip per card the role shows');
eq(on.panels, 2, 'who-visits cards follow the panels that are on');
const htmlOn = dashSkelHTML(on);
eq(htmlOn.includes('dash-skel-chart'), true, 'an enabled chart is a strip');
eq(htmlOn.includes('dash-skel-offers'), true, 'enabled offers are a strip');
eq(htmlOn.includes('dash-skel-vis'), true, 'enabled who-visits is a strip');
eq(htmlOn.includes('dash-skel-recent'), false, 'a switched-off recent block takes no space');
eq(htmlOn.includes('dash-skel-perf'), true, 'the campaigns table is a strip');
eq((htmlOn.match(/kpi skel/g)||[]).length, 5, 'five cards, five strips');
eq(text(htmlOn), '', 'no captions and no figures on the strips');

dashRows = name => {
  if (name === 'kpis') return [{id:'visits',on:true},{id:'conv',on:true},{id:'cr',on:true},{id:'cost',on:true}];
  if (name === 'blocks') return [{id:'chart',on:false},{id:'perf',on:true},{id:'offers',on:false},{id:'visitors',on:false},{id:'recent',on:false}];
  if (name === 'recent') return [];
  return [];
};
hiddenCols = () => new Set(['cost','revenue','profit','roi']);
dashSkelNote({kpis:['visits','cost','roi'], blocks:['chart','perf','offers'], perf:9, revenue:99});
const off = dashSkelPlan();
eq(off.blocks.includes('chart'), false, 'a switched-off chart is not drawn');
eq(off.blocks.includes('offers'), false, 'switched-off offers are not drawn');
eq(off.blocks, ['perf'], 'the campaigns table stays');
eq(off.kpis.includes('cost'), false, 'a hidden column is not a skeleton card');
eq(off.kpis, ['visits','conv','cr'], 'the cache does not bring a hidden card back');
eq(off.perf, 9, 'the remembered campaign rows are kept');
eq(dashSkelRead().revenue, undefined, 'the cache stores no figures');
const htmlOff = dashSkelHTML(off);
eq(htmlOff.includes('dash-skel-chart'), false, 'no chart strip when the chart is off');
eq(htmlOff.includes('dash-skel-offers'), false, 'no offers strip when offers are off');
eq(htmlOff.includes('dash-skel-vis'), false, 'no who-visits strip when it is off');
eq(htmlOff.includes('dash-skel-perf'), true, 'the campaigns table remains');
eq(rows(htmlOff.slice(htmlOff.indexOf('dash-skel-perf'))), 10, 'header and nine remembered rows');
eq((htmlOff.match(/kpi skel/g)||[]).length, 3, 'hidden cost is not a card');
eq(text(htmlOff), '', 'the off shape still has no figures');
eq(off.cols.includes('unique'), true, 'a visible unique column stays');
eq(off.cols.includes('revenue'), false, 'a hidden revenue column is not a strip');
eq(off.offerCols.includes('revenue'), false, 'offers drop a hidden revenue column');

// No memory yet: cards the role shows, the chart when it is on, the campaigns table, and not offers.
ME = {uid:'u2', user:'u2'};
eq(dashSkelRead(), null, 'another account does not inherit the shape');
dashRows = name => {
  if (name === 'kpis') return [{id:'visits',on:true},{id:'unique',on:true},{id:'cr',on:true}];
  if (name === 'blocks') return [{id:'chart',on:true},{id:'perf',on:true},{id:'offers',on:false},{id:'visitors',on:true},{id:'recent',on:true}];
  if (name === 'recent') return [{id:'ts',on:true}];
  return [];
};
hiddenCols = () => new Set();
dashDims = () => [{id:'country'}];
const fresh = dashSkelPlan();
eq(fresh.blocks.includes('chart'), true, 'the chart is in the default skeleton when it is on');
eq(fresh.blocks.includes('perf'), true, 'the campaigns table is in the default skeleton');
eq(fresh.blocks.includes('offers'), false, 'offers stay out until the profile switches them on');
eq(fresh.blocks.includes('visitors'), true, 'who visits is drawn when the profile has it on');
eq(fresh.perf, 6, 'a few campaign rows before the first visit');
eq(fresh.kpis, ['visits','unique','cr'], 'default cards are the ones the role shows');
const htmlFresh = dashSkelHTML(fresh);
eq(htmlFresh.includes('dash-skel-offers'), false, 'the default skeleton has no offers block');
eq(htmlFresh.includes('dash-skel-chart'), true, 'the default skeleton has the chart');
eq(text(htmlFresh), '', 'the default skeleton has no figures');
if (failed) process.exit(1);
`)
}
