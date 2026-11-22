//go:build uitest

// Colouring of square-bracket macros in the stream URL field, run under node on stub objects. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestStreamURLHighlight
package admin

import "testing"

// TestStreamURLHighlight checks the address field of a stream. [SUBID] and [PAR-1] are drawn in the accent colour
// on the same layer as an offer macro, braces stay coloured, a text variant is left alone, and the value read from
// the field stays plain text. It skips when node is absent.
func TestStreamURLHighlight(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "paintStream"), "bindStreamURLHighlight(body)", "the stream form colours its URL field")
	fn := spaFunctionLine(html, "bindStreamURLHighlight")
	must(t, fn, "q('#s_outv'", "only the stream output is coloured")
	must(t, fn, "dataset.multi==='1'", "a text variant is left alone")
	must(t, fn, "bindMacroOverlay(el,true)", "square macros use the same overlay")
	must(t, spaFunctionLine(html, "bindVariants"), "if(box.id==='s_outv') bindStreamURLHighlight(box);", "a new URL variant is coloured")
	must(t, html, ".hl-v{color:var(--accent)}", "a macro takes the theme accent")
	mustNot(t, spaFunctionLine(html, "bindMacroOverlay"), "ta.innerHTML", "no markup is written into the field")
	mustNot(t, spaFunctionLine(html, "bindMacroOverlay"), "ta.value=", "the field value is never rewritten")

	prelude := nodeEq + `
const esc=s=>String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
class HTMLInputElement{ get value(){return this._v||''} set value(v){this._v=String(v)} }
const made=[];
const mkEl=()=>{const e={className:'',innerHTML:'',scrollLeft:0,attrs:{},style:{setProperty(){}},classes:[],classList:{add(c){e.classes.push(c)}},setAttribute(k,v){e.attrs[k]=v},append(){}};made.push(e);return e};
const document={createElement:mkEl};
const requestAnimationFrame=fn=>fn();
const mk=v=>Object.assign(new HTMLInputElement(),{tagName:'INPUT',dataset:{},scrollLeft:0,parentNode:{insertBefore(){}},addEventListener(){},_v:v});
const q=(sel,root)=>sel==='#s_outv'&&root?root.box:null;
const qa=(sel,box)=>sel==='input.mono'&&box&&box.fields?box.fields:[];
`
	runSPANode(t, prelude, []string{"braceMacroHTML", "bindMacroOverlay", "bindStreamURLHighlight"}, `
const url='https://google.com/?subid=[SUBID]&par_1=[PAR-1]';
eq(braceMacroHTML(url,true), 'https://google.com/?subid=<span class="hl-v">[SUBID]</span>&amp;par_1=<span class="hl-v">[PAR-1]</span>', 'square macros of the stream URL are coloured');
eq(braceMacroHTML('a={origin}&b=[KEY]', true), 'a=<span class="hl-v">{origin}</span>&amp;b=<span class="hl-v">[KEY]</span>', 'braces stay coloured when square macros are on');
eq(braceMacroHTML('x=[PAR-1]&y={subid}'), 'x=[PAR-1]&amp;y=<span class="hl-v">{subid}</span>', 'without the flag square brackets stay plain');
eq(braceMacroHTML('[] [ ]', true), '[] [ ]', 'empty brackets and a space are not a macro');
eq(braceMacroHTML('<b>[SUBID]</b>', true), '&lt;b&gt;<span class="hl-v">[SUBID]</span>&lt;/b&gt;', 'typed markup is escaped');

const inp=mk(url);
bindMacroOverlay(inp, true);
eq(inp.value, url, 'the value stays plain text');
const back=made[1];
eq(back.innerHTML, 'https://google.com/?subid=<span class="hl-v">[SUBID]</span>&amp;par_1=<span class="hl-v">[PAR-1]</span>', 'the layer under the input colours both macros');
inp.value=url+'&c={origin}';
eq(inp.value, url+'&c={origin}', 'a value set from code stays plain');
eq(back.innerHTML, 'https://google.com/?subid=<span class="hl-v">[SUBID]</span>&amp;par_1=<span class="hl-v">[PAR-1]</span>&amp;c=<span class="hl-v">{origin}</span>', 'a value set from code refreshes the copy');

const text=mk('[SUBID]');
const before=made.length;
bindStreamURLHighlight({id:'s_outv', dataset:{multi:'1'}, fields:[text]});
eq(made.length, before, 'a text variant is not coloured');
eq(text.dataset.hl, undefined, 'a text variant is not marked');

const live=mk('https://x/?a=[SUBID]');
bindStreamURLHighlight({box:{dataset:{multi:'0'}, fields:[live]}});
eq(live.dataset.hl, '1', 'the URL field of the stream is bound');
eq(live.value, 'https://x/?a=[SUBID]', 'binding the stream field keeps the plain value');
eq(made[made.length-1].innerHTML, 'https://x/?a=<span class="hl-v">[SUBID]</span>', 'the stream field colours [SUBID]');

const added=mk('https://x/?b=[PAR-1]');
bindStreamURLHighlight({id:'s_outv', dataset:{multi:'0'}, fields:[added]});
eq(added.dataset.hl, '1', 'a new URL variant is bound');
eq(added.value, 'https://x/?b=[PAR-1]', 'a new variant keeps the plain value');
if (failed) process.exit(1);
`)
}
