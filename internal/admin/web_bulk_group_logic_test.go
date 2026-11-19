//go:build uitest

// Behaviour of the group actions of the catalog tables and campaigns, run under node. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestBulk
package admin

import "testing"

// TestBulkCatalogLogic checks the selection helpers on stub objects: only rows the role may change get a box and
// stay ticked, the header box follows the page, the bar exists only with a selection, and the group and delete
// loops write one request per row, stop at the first failure and keep the unwritten rows ticked.
func TestBulkCatalogLogic(t *testing.T) {
	runSPANode(t, nodeEq+`
const tr=k=>k, esc=s=>String(s), icon=k=>'['+k+']', trFill=(k,v)=>k+':'+v.n;
const bulkAct=(id,key,label,danger)=>'<b '+id+(danger?' danger':'')+'>';
const CAT_PICK={offers:new Set(), networks:new Set()};
const CAT_API={offers:'/api/offers', networks:'/api/affiliate-networks'};
const CAT_DEL_KEY={offers:'off.del.many', networks:'net.del.many'};
let ME={access:{grants:{offers:{mode:'selected', ids:['1','2','3']}, networks:{mode:'full'}}}};
const grantLabel=()=>'', grantHasList=(g,id)=>(g.ids||[]).includes(id);
const canEdit=(me,res,row)=>{const g=me.access.grants[res]; if(!g||g.mode==='read') return false; if(g.mode==='full') return true; return grantHasList(g,String(row.id));};
const calls=[]; let failAt=-1, askAnswer=true, reloaded=0, toasts=[];
const call=async(m,p,b)=>{calls.push(m+' '+p); if(calls.length===failAt) throw new Error('boom');};
const ask=async()=>askAnswer, toast=(m,err)=>toasts.push(m);
const q=()=>null, groupInputValue=()=>'', resolveGroupName=x=>x;
`, []string{"catalogPickSync", "catalogPickHead", "catalogPickCell", "catalogBulkBar", "catalogBulkDelete"}, `
(async()=>{
const rows=[{id:'1'},{id:'2'},{id:'9'}];
CAT_PICK.offers.add('1'); CAT_PICK.offers.add('9'); CAT_PICK.offers.add('77');
catalogPickSync('offers', rows);
eq([...CAT_PICK.offers], ['1'], 'sync drops a row the role may not change and one the filter hides');
eq(catalogPickCell('offers',{id:'9'}), '<td class="pick"></td>', 'no box on a foreign row');
eq(catalogPickCell('offers',{id:'1'}).includes('checked'), true, 'a ticked row shows ticked');
eq(catalogPickCell('offers',{id:'2'}).includes('checked'), false, 'an unticked row shows unticked');
eq(catalogPickHead('offers',[{id:'9'}]), '<th class="pick"></th>', 'no header box when nothing on the page can change');
eq(catalogPickHead('offers',rows).includes('checked'), false, 'header box is off while one changeable row is unticked');
CAT_PICK.offers.add('2');
eq(catalogPickHead('offers',rows).includes('checked'), true, 'header box is on when every changeable row of the page is ticked');
CAT_PICK.offers.clear();
eq(catalogBulkBar('offers',5), '', 'no bar without a selection');
CAT_PICK.offers.add('1');
eq(catalogBulkBar('offers',5).includes('offers-bulk-group')&&catalogBulkBar('offers',5).includes('offers-bulk-del')&&catalogBulkBar('offers',5).includes('1/5'), true, 'bar with both actions and the count');

const nets=[{id:'a'},{id:'b'},{id:'c'}];
CAT_PICK.networks.add('a'); CAT_PICK.networks.add('b'); CAT_PICK.networks.add('c');
const ctx={reload:async()=>{reloaded++}};
askAnswer=false; calls.length=0;
await catalogBulkDelete('networks', nets, ctx);
eq([calls.length, reloaded, CAT_PICK.networks.size], [0,0,3], 'a declined confirmation sends nothing and keeps the selection');
askAnswer=true; failAt=2;
await catalogBulkDelete('networks', nets, ctx);
eq(calls, ['DELETE /api/affiliate-networks/a','DELETE /api/affiliate-networks/b'], 'one DELETE per row, stopped at the failure');
eq([...CAT_PICK.networks], ['b','c'], 'the deleted row leaves the selection, the rest stay');
eq([reloaded, toasts], [1, ['boom']], 'the failure is shown and the page is reloaded');
failAt=-1; calls.length=0;
await catalogBulkDelete('networks', [{id:'b'},{id:'c'}], ctx);
eq(calls, ['DELETE /api/affiliate-networks/b','DELETE /api/affiliate-networks/c'], 'the rest go through');
eq(CAT_PICK.networks.size, 0, 'selection is empty after a full pass');
if (failed) process.exit(1);
})();
`)
}

// TestBulkCampaignFolderLogic checks setCampaignFolder: a saved campaign gets one PUT of its last saved body with the
// new folder, so a draft edit is not sent and the whole list never goes in one request; a campaign never saved or
// renamed changes in memory and becomes a draft; an empty label removes the folder; the first failure stops the loop.
func TestBulkCampaignFolderLogic(t *testing.T) {
	runSPANode(t, nodeEq+`
const groupParse=JSON.parse, groupJSON=JSON.stringify;
const SNAP=new Map(), DIRTY=new Set();
const calls=[]; let failAt=-1;
const call=async(m,p,b)=>{calls.push({m,p,b}); if(calls.length===failAt) throw new Error('boom');};
const markDirty=g=>{const s=SNAP.get(g); if(s&&s.json===groupJSON(g)) DIRTY.delete(g); else DIRTY.add(g);};
const mk=(o,saved)=>{SNAP.set(o,{id:saved.id,json:groupJSON(saved)}); return o;};
`, []string{"setCampaignFolder"}, `
(async()=>{
const saved={id:'c1',name:'old',streams:[]};
const a=mk({id:'c1',name:'old',streams:[]}, saved);
await setCampaignFolder([a], ' Dating ');
eq(calls.length, 1, 'one request for one saved campaign');
eq([calls[0].m, calls[0].p], ['PUT','/api/groups/c1'], 'PUT of that campaign');
eq(calls[0].b, {id:'c1',name:'old',streams:[],folder:'Dating'}, 'the body is the saved one with the folder, trimmed');
eq(a.folder, 'Dating', 'the folder is set in memory');
eq(DIRTY.has(a), false, 'an untouched campaign is not a draft afterwards');

calls.length=0;
const b=mk({id:'c2',name:'draft edit',streams:[]}, {id:'c2',name:'saved name',streams:[]});
DIRTY.add(b);
await setCampaignFolder([b], 'Finance');
eq(calls[0].b.name, 'saved name', 'an unsaved edit is not sent');
eq([b.name, b.folder, DIRTY.has(b)], ['draft edit','Finance',true], 'the edit stays as a draft, with the folder');

calls.length=0;
const fresh={id:'new1',name:'x'};
const renamed=mk({id:'c9',name:'x'}, {id:'old9',name:'x'});
await setCampaignFolder([fresh, renamed], 'Alex');
eq(calls.length, 0, 'a never saved or renamed campaign sends nothing');
eq([fresh.folder, renamed.folder, DIRTY.has(fresh), DIRTY.has(renamed)], ['Alex','Alex',true,true], 'both change in memory and become drafts');

calls.length=0;
await setCampaignFolder([a], '');
eq(calls[0].b.folder, undefined, 'an empty label removes the folder from the body');
eq('folder' in a, false, 'and from the campaign');

calls.length=0; failAt=2;
const l1=mk({id:'l1'},{id:'l1'}), l2=mk({id:'l2'},{id:'l2'}), l3=mk({id:'l3'},{id:'l3'});
let err=''; try{ await setCampaignFolder([l1,l2,l3], 'G') }catch(e){ err=e.message }
eq([err, calls.length], ['boom', 2], 'the first failure rejects and stops the loop');
eq([l1.folder, l2.folder, l3.folder], ['G',undefined,undefined], 'only the written campaign carries the label');
if (failed) process.exit(1);
})();
`)
}
