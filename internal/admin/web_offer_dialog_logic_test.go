//go:build uitest

// Behaviour of the offer dialog helpers and the offers row, run under node on stub objects. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestOfferDialogLogic
package admin

import "testing"

// TestOfferDialogLogic runs the dialog helpers under node: the tab bar marks the open tab, the macro picker lists the
// engine macros and one macro per distinct named value, values are read without empty names and are escaped when
// drawn, a macro click writes key={macro} at the end of the address once (? or & as needed, text kept, the fragment
// last) and marks the macros the address already has, the status word sits left of the switch and the switch is on when the offer is enabled, the
// country chips are read as lowercase codes, the macros in braces of the address field are coloured on a layer
// while its value stays plain, a square key such as [PAR-1] takes the same accent when the offer URL asks for it,
// and the row puts the actions cell after the name (first when the name column is off).
func TestOfferDialogLogic(t *testing.T) {
	html := spaSource()
	prelude := nodeEq + `
const SUBID_MAX=30;
` + spaConstLine(html, "const OFFER_MACROS=") + "\n" + spaConstLine(html, "const OFFER_TABS=") + `
const tr=k=>k;
const esc=s=>String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
const icon=k=>'['+k+']';
let COLS=[{id:'id'},{id:'name'},{id:'clicks'}];
const loadCols=()=>COLS.map(c=>Object.assign({on:true},c));
const offerCells=(m,id)=>'<td>'+id+'</td>';
const offerMetrics=()=>null;
const qa=(sel,box)=>sel==='.tag'?box.chips:box.rows;
const q=(sel,r)=>r&&r.map?r.map[sel]:({value:sel==='[data-oval-name]'?r.n:r.v});
const tagNew=t=>t.typed;
const countryCode=raw=>({poland:'pl'})[String(raw).toLowerCase()]||'';
`
	runSPANode(t, prelude, []string{"offerTabsHTML", "offerPaneOpen", "offerMacroGroups", "offerValueRowHTML", "offerValuesHTML", "readOfferValues", "offerMacroKey", "offerUrlSplit", "offerUrlHasKey", "appendOfferMacro", "offerMacroUsed", "offerDotClass", "offerStatusHTML", "offerChoice", "readOfferCountries", "braceMacroHTML", "bindMacroOverlay", "offerRowHTML", "offerLimitFields"}, `
const bar=offerTabsHTML('values');
eq([...bar.matchAll(/data-otab="(\w+)"/g)].map(m=>m[1]), ['main','action','settings','values','notes'], 'tab order');
eq(/data-otab="values" aria-selected="true" tabindex="0" class="on"/.test(bar), true, 'the open tab is marked');
eq(/data-otab="main" aria-selected="false" tabindex="-1">/.test(bar), true, 'the others are not');
eq(offerPaneOpen('notes','main').includes(' hidden'), true, 'a closed pane is hidden');
eq(offerPaneOpen('main','main').includes(' hidden'), false, 'the open pane is shown');

const groups=offerMacroGroups([{name:' promo ',value:'1'},{name:'promo',value:'2'},{name:'',value:'x'},{name:'geo',value:'3'}]);
eq(groups.map(g=>g.key), ['off.macro.main','off.macro.sub','off.macro.values'], 'sections');
eq(groups[0].tokens, ['{subid}','{root_subid}','{country}','{keyword}'], 'click macros');
eq(groups[1].tokens.length, 30, 'thirty sub ids');
eq(groups[1].tokens[29], '{sub_id_30}', 'the last sub id');
eq(groups[2].tokens, ['{offer_value:promo}','{offer_value:geo}'], 'one macro per distinct named value, empty names left out');
eq(offerMacroGroups([])[2].tokens, [], 'no values, no value macros');

const rows=[{n:' a ',v:'1'},{n:'',v:'lost'},{n:'   ',v:'lost too'},{n:'b',v:' keeps spaces '}];
eq(readOfferValues({rows}), [{name:'a',value:'1'},{name:'b',value:' keeps spaces '}], 'values are read without empty names');
const drawn=offerValuesHTML([{name:'x"<y',value:'a&b'}]);
eq(drawn.includes('value="x&quot;&lt;y"') && drawn.includes('value="a&amp;b"'), true, 'values are escaped');
eq((offerValuesHTML([]).match(/oval-row/g)||[]).length, 1, 'no pairs draws one empty row');

eq(offerMacroKey('{subid}'), 'subid', 'the key is the macro without braces');
eq(offerMacroKey('{sub_id_12}'), 'sub_id_12', 'a sub id key');
eq(offerMacroKey('{offer_value:promo}'), 'promo', 'a value macro has the name of the value as key');
eq(appendOfferMacro('https://x.example/l/1', '{subid}'), 'https://x.example/l/1?subid={subid}', 'an address without query gets ?key={macro}');
eq(appendOfferMacro('', '{subid}'), '?subid={subid}', 'an empty address gets the pair with ?');
eq(appendOfferMacro('https://x.example/l/1?a=1', '{country}'), 'https://x.example/l/1?a=1&country={country}', 'an address with query gets &key={macro}');
eq(appendOfferMacro('https://x.example/l/1?sub_id=[PAR-1]{subid}', '{country}'), 'https://x.example/l/1?sub_id=[PAR-1]{subid}&country={country}', 'what is typed stays untouched');
eq(appendOfferMacro('https://x.example/l/1?', '{subid}'), 'https://x.example/l/1?subid={subid}', 'a bare ? is continued without &');
eq(appendOfferMacro('https://x.example/l/1?a=1&', '{subid}'), 'https://x.example/l/1?a=1&subid={subid}', 'a trailing & is continued without a second one');
eq(appendOfferMacro('https://x.example/l/1?a=1#top', '{subid}'), 'https://x.example/l/1?a=1&subid={subid}#top', 'the pair goes before the fragment');
const once=appendOfferMacro('https://x.example/l/1', '{subid}');
eq(appendOfferMacro(once, '{subid}'), once, 'a second click does not repeat the key');
eq(appendOfferMacro('https://x.example/?subid=5', '{subid}'), 'https://x.example/?subid=5', 'a key already in the query is not written again');
eq(appendOfferMacro('https://x.example/?x=1&subid', '{subid}'), 'https://x.example/?x=1&subid', 'a key without value counts as present');
eq(appendOfferMacro('https://x.example/?sub_id_1=a', '{sub_id_10}'), 'https://x.example/?sub_id_1=a&sub_id_10={sub_id_10}', 'sub_id_1 is not sub_id_10');
eq(appendOfferMacro('https://x.example/?a={offer_value:promo}', '{offer_value:promo}'), 'https://x.example/?a={offer_value:promo}&promo={offer_value:promo}', 'a value macro is written under its own name');
eq(offerMacroUsed('https://x.example/?sub_id=[PAR-1]{subid}', '{subid}'), true, 'a bare macro in the address is marked');
eq(offerMacroUsed('https://x.example/?country={country}', '{country}'), true, 'key={macro} is marked');
eq(offerMacroUsed('https://x.example/?country={country}', '{keyword}'), false, 'another macro is not');
eq(offerMacroUsed('https://x.example/?a={sub_id_10}', '{sub_id_1}'), false, 'sub_id_1 is not marked by sub_id_10');

const o={id:'7',name:'Offer',group:'g',network_id:''};
const acts='<td class="acts">A</td>';
const off='<td><span class="offname"><span class="dot bad" title="Off"></span><span>Offer</span></span></td>';
eq(offerRowHTML(o,'N',acts), '<td class="num">7</td>'+off+'<td class="acts">A</td><td>clicks</td>', 'the actions cell follows the name; a disabled offer has a red dot centred on the name');
eq(offerRowHTML(Object.assign({status:true},o),'N'), '<td class="num">7</td><td><span class="offname"><span class="dot on" title="On"></span><span>Offer</span></span></td><td>clicks</td>', 'an enabled offer has a green dot and a row without the cell has none');
const opt=(v,l,cur)=>'<option value="'+v+'"'+(String(cur)===String(v)?' selected':'')+'>'+l+'</option>';
const helpMark=()=>'';
const missingOpt=()=>'';
const offLim=offerLimitFields('cap', false, '25', 'b', [{id:'a',name:'A'},{id:'b',name:'B'}], 'a');
eq(/ hidden/.test(offLim), true, 'a limit that is off hides the number');
eq(offLim.includes('id="ocapn"') && offLim.includes('value="25"'), true, 'the saved daily number stays in the field');
eq(offLim.includes('id="ocapoffer"'), false, 'a limit has no offer list of its own');
const onLim=offerLimitFields('total', true, '3', 'b', [{id:'a',name:'A'},{id:'b',name:'B'}], 'a');
eq(/ hidden/.test(onLim), false, 'a limit that is on shows the number');
eq(onLim.includes('id="ototaln"') && onLim.includes('value="3"') && !onLim.includes('selected'), true, 'the saved total number is shown without an offer list');
COLS=[{id:'id'},{id:'clicks'}];
eq(offerRowHTML(o,'N',acts), '<td class="acts">A</td><td class="num">7</td><td>clicks</td>', 'without the name column the cell comes first');

eq(offerDotClass(true), 'on', 'enabled is green');
eq(offerDotClass(false), 'bad', 'disabled is red');
const stOn=offerStatusHTML(true), stOff=offerStatusHTML(false);
eq(stOn.indexOf('data-ostatus-lab')<stOn.indexOf('class="dot on"') && stOn.includes('>off.status.on<') && stOn.includes('id="ostatus" checked') && !stOn.includes('lm-seg'), true, 'an enabled offer shows the word left of the green switch');
eq(stOff.includes('>off.status.off<') && stOff.includes('class="dot bad"') && !/id="ostatus" checked/.test(stOff), true, 'a disabled offer shows the off word and a red switch');
eq(stOn.includes('class="tog"') || stOn.includes('Вкл') || stOn.includes('>On<'), false, 'the short label and the old pill are gone');

const field={chips:[{dataset:{v:'PL'}},{dataset:{v:'de'}},{dataset:{v:'pl'}}], typed:['poland','zz']};
eq(readOfferCountries({map:{'#ocountries':field}}), ['pl','de'], 'chips and typed names are read as lowercase codes without repeats');
eq(readOfferCountries({map:{}}), [], 'no field, no countries');

const marked=braceMacroHTML('https://x.example/?a={subid}&b=[PAR-1]&c={offer_value:p}');
eq(marked, 'https://x.example/?a=<span class="hl-v">{subid}</span>&amp;b=[PAR-1]&amp;c=<span class="hl-v">{offer_value:p}</span>', 'only the macros in braces are coloured');
eq(braceMacroHTML('<b>{x}</b>'), '&lt;b&gt;<span class="hl-v">{x}</span>&lt;/b&gt;', 'nothing typed becomes markup');
const made=[];
class HTMLTextAreaElement{}
class HTMLInputElement{ get value(){return this._v||''} set value(v){this._v=String(v)} }
const mkEl=()=>{const e={className:'',innerHTML:'',scrollLeft:0,attrs:{},style:{setProperty(){}},classes:[],classList:{add(c){e.classes.push(c)}},setAttribute(k,v){e.attrs[k]=v},append(){}};made.push(e); return e};
const document={createElement:mkEl};
const requestAnimationFrame=fn=>fn();
const inp=Object.assign(new HTMLInputElement(), {tagName:'INPUT', dataset:{}, scrollLeft:0, parentNode:{insertBefore(){}}, addEventListener(){}});
bindMacroOverlay(inp);
inp.value='https://x.example/?a={subid}&b=[PAR-1]';
eq(inp.value, 'https://x.example/?a={subid}&b=[PAR-1]', 'the value stays plain text');
const [wrapEl, backEl]=made;
eq(backEl.innerHTML, 'https://x.example/?a=<span class="hl-v">{subid}</span>&amp;b=[PAR-1]', 'the layer under the input colours the macro');
eq([wrapEl.className, wrapEl.classes], ['hl-field one', ['on']], 'a one-line field gets the one-line layer');
inp.scrollLeft=40; inp.value=inp.value+'x';
eq(backEl.scrollLeft, 40, 'the layer scrolls with the text');
bindMacroOverlay(inp);
eq(made.length, 2, 'binding twice does nothing');
const ourl=Object.assign(new HTMLInputElement(), {tagName:'INPUT', dataset:{}, scrollLeft:0, parentNode:{insertBefore(){}}, addEventListener(){}});
bindMacroOverlay(ourl, true);
ourl.value='https://click-game.com/l/6aea037462cbf17d7f0570d2?sub_id=[PAR-1]&k={keyword}';
eq(ourl.value, 'https://click-game.com/l/6aea037462cbf17d7f0570d2?sub_id=[PAR-1]&k={keyword}', 'the offer URL value stays plain text');
eq(made[made.length-1].innerHTML, 'https://click-game.com/l/6aea037462cbf17d7f0570d2?sub_id=<span class="hl-v">[PAR-1]</span>&amp;k=<span class="hl-v">{keyword}</span>', 'a square key and a brace key in the offer URL take the accent');
eq(braceMacroHTML('https://x.example/?sub_id=[PAR-1]&k={subid}', true), 'https://x.example/?sub_id=<span class="hl-v">[PAR-1]</span>&amp;k=<span class="hl-v">{subid}</span>', 'the offer URL colours both key shapes');
if (failed) process.exit(1);
`)
}
