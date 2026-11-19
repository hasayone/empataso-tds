//go:build uitest

// Behaviour of loadDashOffers on stub objects, run under node. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestDashOffersLoad
package admin

import "testing"

// TestDashOffersLoad checks loadDashOffers: with no block on the page nothing is requested; with the block
// the report is asked for the dashboard query with meta=1; the rows are the folded offers by visits with the
// four report columns; only the first ten are listed and the count is shown; a failed request says no data.
func TestDashOffersLoad(t *testing.T) {
	runSPANode(t, nodeEq+`
const tr=k=>k, esc=s=>String(s), fmt=n=>String(+n||0), moneyText=(v)=>'$'+v, moneyCellCls=()=>'';
const stateHTML=s=>'<state>'+s+'</state>', hiddenCols=()=>new Set(), idLabel=(f,id)=>'Offer '+id;
const ME={}, dashQS=()=>'from=A&to=B&campaign_id=c1';
const ensureCatalogs=async()=>{};
const reportData=b=>({rows:Array.isArray(b)?b:(b&&b.rows)||[]});
const DASH_OFFERS_N=10; let DASH_OFFERS_GEN=0;
let els={}, calls=[], answer=null;
const q=sel=>els[sel]||null;
const call=async(m,p)=>{calls.push(m+' '+p); if(answer instanceof Error) throw answer; return answer;};
const mkEls=()=>{els={'#dash-offers-body':{innerHTML:''}, '#dash-offers-n':{textContent:''}};};
`, []string{"dashFoldOffers", "loadDashOffers"}, `
(async()=>{
els={}; calls=[];
await loadDashOffers();
eq(calls, [], 'no block on the page: no request');

mkEls(); answer={rows:[{offer:'1',hits:5,unique:4,conv:1,profit:2.5},{offer:'1',hits:5,unique:1,conv:0,profit:0},{offer:'2',hits:3,unique:3,conv:0,profit:0}]};
await loadDashOffers();
eq(calls, ['GET /api/report?from=A&to=B&campaign_id=c1&meta=1'], 'the report for the dashboard query, folded server-side');
const h=els['#dash-offers-body'].innerHTML;
eq(['Offer 1','Offer 2'].every(n=>h.includes(n)), true, 'both offers are listed');
eq(h.indexOf('Offer 1')<h.indexOf('Offer 2'), true, 'most visits first');
eq(['stream.offer','Hits','Unique','Conv','col.revenue'].every(k=>h.includes('>'+k+'<')), true, 'the five columns');
eq(/Leads|Sales|EPC|CPC|Profit|ROI|col\.cost/.test(h), false, 'no other metric column');
eq(h.includes('<td class="num">10</td>')&&h.includes('<td class="num">5</td>')&&h.includes('$2.5'), true, 'visits, uniques and revenue are the sums');
eq(els['#dash-offers-n'].textContent, '', 'all offers shown: no count');

mkEls(); answer={rows:Array.from({length:13},(_,i)=>({offer:String(i+1),hits:100-i,unique:1,conv:0,profit:0}))};
await loadDashOffers();
eq((els['#dash-offers-body'].innerHTML.match(/<tr>/g)||[]).length, 11, 'header row and ten offers');
eq(els['#dash-offers-n'].textContent, '10 of 13', 'the count of shown and all offers');

mkEls(); answer=new Error('x'); calls.length=0;
await loadDashOffers();
eq(els['#dash-offers-body'].innerHTML, '<state>no data</state>', 'a failed request says no data');
mkEls(); answer={rows:[]};
await loadDashOffers();
eq(els['#dash-offers-body'].innerHTML, '<state>No traffic in this period</state>', 'an empty period says so');
if (failed) process.exit(1);
})();
`)
}
