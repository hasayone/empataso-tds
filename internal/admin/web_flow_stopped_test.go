//go:build uitest

// Red marks of a stream that still sends traffic to a stopped offer, run under node on stub objects. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestFlowStoppedOffer
package admin

import "testing"

// TestFlowStoppedOfferMarkup runs the stream helpers under node: an offer with a share above zero that is switched
// off gets a red badge and a red dot on its row, an offer that is on keeps the gray badge and no dot, the stream
// card is red only for a stream that is on and sends a share to a stopped offer, and a zero share, a stream that is
// off, an unknown offer and a landing do not make it red. The collapsed badge row is red too.
func TestFlowStoppedOfferMarkup(t *testing.T) {
	html := spaSource()
	prelude := nodeEq + `
const esc=s=>String(s==null?'':s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
const tr=k=>k;
const trFill=(k,map)=>k;
let OFFERS=[], LANDINGS=[];
const flowStreamOfferMap=()=>null;
const streamHint=()=>'';
` + spaConstLine(html, "const FLOW_DEST_BADGES=") + "\n"
	runSPANode(t, prelude, []string{"thenURL", "thenLive", "thenMode", "nameOf", "flowSharePcts", "flowDestLabel", "flowOfferStopped", "flowDestItems", "flowDestOfferBadge", "flowOfferMark", "flowStreamStopped", "flowDestMarks", "flowDestStatsHTML"}, `
OFFERS=[{id:'on',name:'Alive',status:true},{id:'off',name:'Dead',status:false},{id:'on2',name:'Alive two',status:true}];
LANDINGS=[{id:'l1',name:'Land',status:false}];
const st=(status,out)=>({name:'s',status,out:Object.assign({redirect:'http_redirect'},out)});

eq(flowOfferStopped('off'), true, 'an offer that is off is stopped');
eq(flowOfferStopped('on'), false, 'an offer that is on is not');
eq(flowOfferStopped('nope'), false, 'an unknown offer is not called stopped');

const items=flowDestItems({redirect:'http_redirect', offers:[{id:'on',share:50},{id:'off',share:50}], landings:[{id:'l1',share:100}]});
eq(items.map(i=>[i.id,i.stopped]), [['on',false],['off',true],['l1',false]], 'only the stopped offer is marked, a landing never');

const dead=flowDestOfferBadge(items[1]), alive=flowDestOfferBadge(items[0]);
eq(dead.includes('class="pill bad"') && dead.includes('flow.offer.stopped'), true, 'the badge of a stopped offer is red and says so');
eq(alive.includes('class="pill"') && !alive.includes('bad'), true, 'the badge of a live offer stays gray');
eq(flowOfferMark(items[1]).includes('class="dot bad"'), true, 'a stopped offer has a red dot');
eq(flowOfferMark(items[0]), '', 'a live offer has no dot');
eq(flowOfferMark(null), '', 'no row, no dot');

const mixed={redirect:'http_redirect', offers:[{id:'on',share:50},{id:'off',share:50}]};
const rows=flowDestStatsHTML(mixed, {hits:0,bots:0,conv:0,profit:0}, {id:'g'}, 's', true, false);
const lines=rows.split('<div class="oline">').slice(1);
eq(lines.length, 2, 'one row per offer with a share');
eq(lines[0].includes('class="dot bad"') || lines[0].includes('pill bad'), false, 'the row of a live offer is not red');
eq(lines[1].startsWith('<i class="dot bad"') && lines[1].includes('pill bad'), true, 'the row of a stopped offer starts with a red dot and has a red badge');
const collapsed=flowDestMarks(mixed);
eq(collapsed.includes('pill bad') && collapsed.includes('class="pill"'), true, 'the collapsed badge row has one red and one gray badge');

eq(flowStreamStopped(st(true,{offers:[{id:'off',share:50},{id:'on',share:50}]})), true, 'a stream on with a share on a stopped offer is red');
eq(flowStreamStopped(st(true,{offers:[{id:'off',share:0},{id:'on',share:100}]})), false, 'a zero share on a stopped offer is not red');
eq(flowStreamStopped(st(false,{offers:[{id:'off',share:100}]})), false, 'a stream that is off takes no traffic');
eq(flowStreamStopped(st(true,{offers:[{id:'on',share:50},{id:'on2',share:50}]})), false, 'live offers are not red');
eq(flowStreamStopped(st(true,{offers:[{id:'gone',share:100}]})), false, 'an offer the catalog lacks is not red');
eq(flowStreamStopped(st(true,{redirect:'group',out:'other',offers:[{id:'off',share:100}]})), false, 'a hand-off to another campaign is not red');
eq(flowStreamStopped(st(true,{landings:[{id:'l1',share:100}]})), false, 'a stopped landing is not an offer');
eq(flowStreamStopped(null), false, 'no stream, no mark');
if (failed) process.exit(1);
`)
}

// TestFlowStoppedOfferWiring checks that the campaign canvas puts the red class on the stream card, that the
// stylesheet draws it from the danger colour of the theme, and that the row helpers are wired.
func TestFlowStoppedOfferWiring(t *testing.T) {
	html := spaSource()
	must(t, html, "${flowStreamStopped(s)?' stopped':''}\" data-node=\"${i}\"", "the card of a stream with a stopped offer gets the class")
	must(t, html, ".node.stopped,.node.stopped:hover{border-color:var(--bad);background:var(--bad-soft)}", "the red card uses the theme danger colour")
	stats := spaFunctionLine(html, "flowDestStatsHTML")
	must(t, stats, "${flowOfferMark(it)}${badge}", "the offer row carries the red dot")
	must(t, spaFunctionLine(html, "flowDestOfferBadge"), "pill${stopped?' bad':''}", "the badge turns red")
}
