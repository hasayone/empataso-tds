//go:build uitest

// Macro constructor of the stream output, run under node on stub objects. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestStreamMacro
package admin

import "testing"

// TestStreamMacroConstructor checks the constructor under the output of a stream: the hint no longer lists [CID] and
// has a button instead, the picker offers [SUBID] but not [CID], a click writes subid=[SUBID] at the end of the
// address (? first, & after) without repeating it, the macros the address already has are found, a macro in square
// brackets gets the key of its lowercase name, a text output gets the bare macro once, and the form is told about
// every change.
func TestStreamMacroConstructor(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "thenURLField"), `id="s_macro"`, "the button of the constructor under the output")
	mustNot(t, spaFunctionLine(html, "thenURLField"), "[CID]", "the old name in the hint of the output")
	mustNot(t, spaFunctionLine(html, "thenURLField"), "[KEY] [", "the wall of macros")
	mustNot(t, spaConstLine(html, "const outPH="), "[CID]", "the example of the output address")
	prelude := nodeEq + `
const tr=k=>k;
let notified=0;
const changed=el=>{notified++};
` + spaBetween(html, "const STREAM_MACROS=[", "];") + "];\n"
	runSPANode(t, prelude, []string{"offerMacroKey", "offerUrlSplit", "offerUrlHasKey", "appendOfferMacro", "offerMacroUsed", "streamMacroInsert"}, `
const all=STREAM_MACROS.flatMap(g=>g.tokens);
eq(all.includes('[SUBID]'), true, 'the picker offers [SUBID]');
eq(all.includes('[CID]'), false, 'the picker does not offer [CID]');
eq(STREAM_MACROS.map(g=>g.key), ['stream.macro.click','stream.macro.visitor','stream.macro.params','stream.macro.other'], 'sections');
eq(STREAM_MACROS[2].tokens, ['[PAR-1]','[PAR-2]','[PAR-3]','[PAR-4]','[PAR-5]'], 'five link parameters');
eq(new Set(all).size, all.length, 'no macro twice');

eq(offerMacroKey('[SUBID]'), 'subid', 'the key of [SUBID]');
eq(offerMacroKey('[PAR-1]'), 'par_1', 'the key of [PAR-1]');
eq(offerMacroKey('{root_subid}'), 'root_subid', 'the key of a macro in braces');
eq(new Set(all.map(offerMacroKey)).size, all.length, 'every macro has its own key');

const fld=v=>({value:v, selectionStart:v.length, selectionEnd:v.length, setSelectionRange(a,b){this.selectionStart=a; this.selectionEnd=b}});
let f=fld('https://example.com/');
streamMacroInsert(f, '[SUBID]', true, false);
eq(f.value, 'https://example.com/?subid=[SUBID]', 'the first pair goes after ?');
streamMacroInsert(f, '[SUBID]', true, false);
eq(f.value, 'https://example.com/?subid=[SUBID]', 'a repeat does not duplicate');
streamMacroInsert(f, '[COUNTRY]', true, false);
eq(f.value, 'https://example.com/?subid=[SUBID]&country=[COUNTRY]', 'the next pair goes after &');
eq(notified, 3, 'the form is told about every call');
eq(offerMacroUsed(f.value, '[SUBID]'), true, 'a macro in the address is found');
eq(offerMacroUsed(f.value, '[CITY]'), false, 'another macro is not');
f=fld('https://example.com/?sub=[CID]');
streamMacroInsert(f, '[SUBID]', true, false);
eq(f.value, 'https://example.com/?sub=[CID]&subid=[SUBID]', 'an old [CID] stays and the new pair is added');

f=fld('<a>go</a>');
f.selectionStart=f.selectionEnd=3;
streamMacroInsert(f, '[IP]', false, true);
eq(f.value, '<a>[IP]go</a>', 'a text output gets the bare macro at the caret');
eq(f.selectionStart, 7, 'the caret follows the macro');
streamMacroInsert(f, '[IP]', false, true);
eq(f.value, '<a>[IP]go</a>', 'a text output does not repeat the macro');
f=fld('x');
streamMacroInsert(f, '[KEY]', false, false);
eq(f.value, 'x[KEY]', 'a field never focused gets the macro at the end');
if (failed) process.exit(1);
`)
}
