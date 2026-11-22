package admin

import "testing"

// TestCodeEditorPlainOnLandingAndOfferLand checks that a landing file and a local
// offer landing both open in the code editor, and that the value saved from that
// editor is the editor's own text. Highlight markup must not be part of the value.
func TestCodeEditorPlainOnLandingAndOfferLand(t *testing.T) {
	html := spaSource()
	plain := spaFunctionLine(html, "codePlain")
	if plain == "" {
		t.Fatal("codePlain not found")
	}
	must(t, plain, "editor.getValue()", "the saved text is the editor value")
	mustNot(t, plain, "innerHTML", "highlight markup is not the saved value")
	mustNot(t, plain, "outerHTML", "highlight markup is not the saved value")

	ed := spaBetween(html, "async function codeEditor(", "async function openStreamPage(")
	if ed == "" {
		t.Fatal("codeEditor not found")
	}
	must(t, ed, "showGutter:true", "the editor shows the line-number gutter")
	must(t, ed, "showLineNumbers:true", "the editor shows line numbers")
	must(t, ed, "return codePlain(editor)", "the facade value is plain text")
	mustNot(t, ed, "innerHTML", "the editor does not read highlight markup")

	land := spaBetween(html, "async function openLandingFile(", "async function landings(")
	if land == "" {
		t.Fatal("openLandingFile not found")
	}
	must(t, land, "codeEditor(ta,path)", "a landing file opens in the code editor")
	must(t, land, "ed?ed.value", "a landing file saves the editor value")

	offer := spaBetween(html, "const openOffer=(id)=>{", "const mkOff=canCreate")
	if offer == "" {
		t.Fatal("openOffer not found")
	}
	must(t, offer, `id="oland-body"`, "a local offer landing has the code field")
	must(t, offer, "codeEditor(landTa,'index.html')", "an offer landing uses the same editor")
	must(t, offer, "offerLandEd.value", "an offer landing saves the editor value")
	must(t, offer, "local:!!(cur&&cur.local)", "a local offer stays local")

	act := spaFunctionLine(html, "offerActionHTML")
	mustNot(t, act, "codeEditor", "the action field stays a plain textarea")
	mustNot(t, act, "oland-body", "the landing editor is not the action field")
}
