//go:build uitest

// The braces button of a stream URL, run under node. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestStreamURLMacroLogic
package admin

import "testing"

// TestStreamURLMacroLogic checks that a direct URL and a redirect draw the braces button on the address
// row, that a text output keeps a single button under the field, and that a chosen macro is written into
// the address at the caret (at the end when the caret was not saved, a selection replaced).
func TestStreamURLMacroLogic(t *testing.T) {
	prelude := nodeEq + `
const URLISH=new Set(['http_redirect','meta_refresh','js_redirect','iframe_redirect','js_selection','iframe_selection','curl']);
const tr=k=>k;
const esc=s=>String(s==null?'':s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const icon=k=>'['+k+']';
const variantsOf=out=>splitVariants(out).length;
const pageName=()=>'';
const liveOfferRows=()=>[];
const outLabel=t=>URLISH.has(t)?'URL':'Text';
const outPH=()=>'https://example.com/';
const sel=(id,opts)=>'<select id="'+id+'">'+opts+'</select>';
const opt=(v,l)=>'<option>'+l+'</option>';
const rewriteField=()=>'';
let notices=0;
const changed=()=>{notices++};
`
	runSPANode(t, prelude, []string{"splitVariants", "variantsField", "varRow", "thenURLField", "streamURLMacroInsert"}, `
const url=thenURLField({out:'https://lgmscfdss.com/3P9VJ', redirect:'http_redirect'}, 'http_redirect', {});
eq(url.includes('data-smacro'), true, 'the direct URL has the braces button');
eq(url.includes('id="s_macro"'), true, 'the button of the address keeps the id');
eq(url.includes('value="https://lgmscfdss.com/3P9VJ"'), true, 'the address is the field');
eq(url.indexOf('value="https://lgmscfdss.com/3P9VJ"')<url.indexOf('data-smacro'), true, 'the button sits after the address');
eq(url.includes('>[braces]stream.macro.open'), false, 'a URL does not also keep the button under the field');
eq((url.match(/data-smacro/g)||[]).length, 1, 'one address, one button');

const redir=thenURLField({out:'https://a.example/go', redirect:'meta_refresh'}, 'meta_refresh', {});
eq(redir.includes('data-smacro') && redir.includes('value="https://a.example/go"'), true, 'a redirect address has the same button');

const text=thenURLField({out:'hello', redirect:'show_text'}, 'show_text', {});
eq(text.includes('data-smacro'), false, 'a text output has no button on the row');
eq(text.includes('id="s_macro"'), true, 'a text output keeps the button under the field');

const two=thenURLField({out:'https://a.example/x|||https://b.example/y', redirect:'http_redirect'}, 'http_redirect', {});
eq((two.match(/data-smacro/g)||[]).length, 2, 'each URL variant has the button');
eq((two.match(/id="s_macro"/g)||[]).length, 1, 'only the first button takes the id');

const f={value:'https://lgmscfdss.com/3P9VJ', selectionStart:0, selectionEnd:0, setSelectionRange(a,b){this.selectionStart=a; this.selectionEnd=b}};
streamURLMacroInsert(f, '[SUBID]', 21, 21);
eq(f.value, 'https://lgmscfdss.com[SUBID]/3P9VJ', 'the token lands at the caret');
eq(f.selectionStart, 28, 'the caret follows the token');
streamURLMacroInsert(f, '{country}', null, null);
eq(f.value, 'https://lgmscfdss.com[SUBID]/3P9VJ{country}', 'without a caret the token goes at the end of the address');
const g={value:'https://a.example/old', selectionStart:0, selectionEnd:0, setSelectionRange(a,b){this.selectionStart=a; this.selectionEnd=b}};
streamURLMacroInsert(g, '[KEY]', 8, 17);
eq(g.value, 'https://[KEY]/old', 'a selection is replaced by the token');
streamURLMacroInsert(null, '[KEY]', 0, 0);
eq(notices, 3, 'a missing field is not a change, and each write tells the form');
if (failed) process.exit(1);
`)
}
