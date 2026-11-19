//go:build uitest

// Behaviour of the offers network filter, run under node. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestOffersNetworkFilterLogic
package admin

import "testing"

// TestOffersNetworkFilterLogic checks matchesNetwork and fillNetSelect on stub objects: an empty pick keeps
// every offer, a picked network keeps only its offers and drops one without a network, the options list the
// networks that offers use under their names, and a pick that no offer uses any more is cleared.
func TestOffersNetworkFilterLogic(t *testing.T) {
	runSPANode(t, nodeEq+`
const tr=k=>k==='off.allnets'?'All networks':k;
const opt=(v,l,sel)=>'<option value="'+v+'"'+(sel===v?' selected':'')+'>'+l+'</option>';
const document={createElement:()=>({innerHTML:''})};
const mkSel=()=>({innerHTML:'',hidden:false,value:''});
`, []string{"matchesNetwork", "fillNetSelect"}, `
const offers=[{id:'a',network_id:'n2'},{id:'b',network_id:'n1'},{id:'c',network_id:''},{id:'d'},{id:'e',network_id:'n2'}];
eq(offers.filter(o=>matchesNetwork(o,'')).map(o=>o.id), ['a','b','c','d','e'], 'no pick: all offers');
eq(offers.filter(o=>matchesNetwork(o,'n2')).map(o=>o.id), ['a','e'], 'a network keeps its offers only');
eq(offers.filter(o=>matchesNetwork(o,'n1')).map(o=>o.id), ['b'], 'offers without a network are hidden');

const nets=[{id:'n1',name:'Zeta'},{id:'n2',name:'Alpha'},{id:'n3',name:'Unused'}];
const sel=mkSel();
eq(fillNetSelect(sel,nets,offers,''), '', 'no pick stays empty');
eq(sel.innerHTML, '<option value="" selected>All networks</option><option value="n2">Alpha</option><option value="n1">Zeta</option>', 'options: all, then used networks by name');
eq(sel.hidden, false, 'shown while an offer has a network');
eq(fillNetSelect(sel,nets,offers,'n2'), 'n2', 'a used network stays picked');
eq(fillNetSelect(sel,nets,offers,'n3'), '', 'a network no offer uses is cleared');
eq(fillNetSelect(sel,[],[{id:'x',network_id:'gone'}],'gone'), 'gone', 'a network missing from the list keeps its id as the name');
const none=mkSel();
fillNetSelect(none,nets,[{id:'a'},{id:'b',network_id:''}],'n1');
eq(none.hidden, true, 'hidden when no offer has a network');
eq(fillNetSelect(null,nets,offers,'n1'), 'n1', 'no select: the pick is returned as is');
if (failed) process.exit(1);
`)
}
