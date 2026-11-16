//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// saveBarScopeChecks are the assertions of TestSaveBarScope: the save bar shows only the drafts of the open page,
// a move off that page asks and drops them, and a bulk delete counts exactly the removed campaigns.
const saveBarScopeChecks = `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const both = {flows:true, cron:true};
eq(draftScope('flows', both), {flows:true, cron:false}, 'campaign drafts show on the campaign pages');
eq(draftScope('automation', both), {flows:false, cron:true}, 'automation drafts show on their own page');
eq(draftScope('integrations', both), {flows:false, cron:false}, 'integrations show no draft of another page');
eq(draftScope('flows', {flows:false, cron:true}), {flows:false, cron:false}, 'no campaign edit, no bar on the campaign list');
eq(draftScope('flows', null), {flows:false, cron:false}, 'no dirty state, no bar');
eq(draftsLeft('flows', 'flows', both), {flows:false, cron:false}, 'list to editor stays on the campaign page');
eq(draftsLeft('flows', 'integrations', both), {flows:true, cron:false}, 'leaving the campaigns abandons their drafts');
eq(draftsLeft('', 'integrations', both), {flows:false, cron:false}, 'the first draw leaves nothing');
eq(draftsLeft('offers', 'flows', both), {flows:false, cron:false}, 'a page without drafts leaves nothing');

GROUPS = [{id:'a'}, {id:'b'}, {id:'c'}, {id:'d'}, {id:'new'}];
GROUPS.slice(0, 4).forEach(g => SNAP.set(g, {id:g.id, json:JSON.stringify(g)}));
const keepRef = GROUPS;
const picked = [GROUPS[1], GROUPS[3], GROUPS[4]];
DIRTY.add(GROUPS[4]);
eq(dropGroups(picked), 3, 'three picked campaigns leave the list');
eq(GROUPS.map(g => g.id), ['a', 'c'], 'the rest keep their order');
eq(keepRef === GROUPS, true, 'the list is changed in place');
eq([...DELETED].sort(), ['b', 'd'], 'only saved campaigns count as removed');
eq(DIRTY.size, 0, 'a removed unsaved campaign leaves the drafts');
eq(dropGroups([{id:'x'}]), 0, 'a campaign not in the list removes nothing');
eq(DELETED.size, 2, 'the removed count stays the picked saved ones');

let asked = 0, answer = false, drawn = 0, replaced = '', flows = true, cron = false;
flowsDirty = () => flows; cronDirty = () => cron;
ask = () => { asked++; return Promise.resolve(answer); };
render = () => { drawn++; };
history = {replaceState: (a, b, url) => { replaced = url; }};
dropFlowDrafts = () => { flows = false; };
PAGE_NOW = 'flows'; PAGE_HASH = '#/flows';
eq(holdPage('flows'), false, 'the campaign editor opens without a prompt');
eq(asked, 0, 'no prompt inside the campaign pages');
(async () => {
  eq(holdPage('integrations'), true, 'leaving with drafts waits for the answer');
  eq(holdPage('offers'), true, 'a second move while asking waits too');
  eq(asked, 1, 'one prompt for both moves');
  await new Promise(r => setTimeout(r, 0));
  eq(replaced, '#/flows', 'no keeps the campaign page address');
  eq(flows, true, 'no keeps the drafts');
  eq(drawn, 0, 'no draws nothing');
  answer = true;
  eq(holdPage('integrations'), true, 'asks again on the next move');
  await new Promise(r => setTimeout(r, 0));
  eq(flows, false, 'yes drops the campaign drafts');
  eq(drawn, 1, 'yes draws the new page');
  eq(holdPage('integrations'), false, 'no drafts left, no prompt');
  if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
})();
`

// TestSaveBarScope runs the save bar scope, the leave prompt and the bulk removal under node.
// It skips when node is absent.
func TestSaveBarScope(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the save bar scope checks")
	}
	html := spaSource()
	var b strings.Builder
	b.WriteString("const SNAP=new Map(), DIRTY=new Set(), DELETED=new Set();\n")
	b.WriteString("let GROUPS=null, PAGE_NOW='', PAGE_HASH='', LEAVING=false, CRON=null, CRON_SAVED='null';\n")
	b.WriteString("var flowsDirty, cronDirty, ask, render, history, dropFlowDrafts; const tr=s=>s;\n")
	for _, name := range []string{"draftScope", "draftsLeft", "forgetGroup", "dropGroups", "holdPage"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		b.WriteString(fn)
		b.WriteByte('\n')
	}
	b.WriteString(saveBarScopeChecks)
	f := filepath.Join(t.TempDir(), "savebar-scope.js")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("save bar scope checks failed:\n%s", out)
	}
}

// TestSaveBarWiring checks that the bar, the router, sign-out and the bulk delete use the scoped draft logic.
func TestSaveBarWiring(t *testing.T) {
	html := spaSource()
	bar := spaFunctionLine(html, "syncSaveBar")
	must(t, bar, "draftScope(route().page,", "the bar shows the drafts of the open page")
	mustNot(t, bar, "if(!anyDirty())", "the bar does not show drafts of other pages")
	must(t, spaFunctionLine(html, "render"), "holdPage(route().page)", "a move off a page with drafts asks first")
	must(t, spaFunctionLine(html, "render"), "PAGE_NOW=page; PAGE_HASH=location.hash;", "the router remembers the drawn page")
	must(t, spaFunctionLine(html, "logout"), "dropFlowDrafts(); CRON=null;", "sign-out drops the drafts")
	must(t, spaFunctionLine(html, "boot"), "if(USER&&me.user!==USER){dropFlowDrafts(); CRON=null}", "another user does not inherit the drafts")
	cards := spaFunctionLine(html, "paintFlowCards")
	must(t, cards, "dropGroups(list);", "bulk delete removes in one pass")
	must(t, cards, "FLOW_PICK.clear(); syncSaveBar();\n      afterPaint(paintFlowCards);", "the bar shows before the table is redrawn")
	mustNot(t, cards, "GROUPS.splice(i,1)", "no splice per removed campaign")
	must(t, cards, "page.forEach(g=>box.checked?FLOW_PICK.add(g.id):FLOW_PICK.delete(g.id))", "the header box picks the visible page")
	must(t, cards, "const allOn=page.length>0&&page.every(", "the header box reflects the visible page")
}
