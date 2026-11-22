//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGroupRenameLogic runs the rename of a group label under node: an empty name and a name another group already
// has (in any letter case) are refused with their dictionary keys, the same name and a case-only change of the group
// itself pass, and the new label is written with a full PUT body to every row of the group only. A failed request
// stops the run and leaves the rows not yet reached with the old label. It skips when node is absent.
func TestGroupRenameLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the group rename checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("const calls=[]; let failAt=-1;\nconst call=async(m,u,b)=>{ if(calls.length===failAt) throw new Error('boom'); calls.push({m,u,b}); };\n")
	src.WriteString(spaFunctionLine(html, "groupRenameError") + "\n")
	src.WriteString("async " + spaFunctionLine(html, "renameGroupLabel") + "\n")
	src.WriteString(`
let failed = 0;
const eq = (a, b, w) => { if (JSON.stringify(a) !== JSON.stringify(b)) { failed++; console.log('FAIL ' + w + ': got ' + JSON.stringify(a) + ', want ' + JSON.stringify(b)); } };
const names = ['Alex', 'Dating', 'Finance'];
eq(groupRenameError('Dating', '', names), 'grp.rename.empty', 'empty name is refused');
eq(groupRenameError('Dating', '   ', names), 'grp.rename.empty', 'blank name is refused');
eq(groupRenameError('Dating', 'finance', names), 'grp.rename.taken', 'a name of another group is refused in any case');
eq(groupRenameError('Dating', ' Alex ', names), 'grp.rename.taken', 'the name is trimmed before the check');
eq(groupRenameError('Dating', 'Dating', names), '', 'the same name is not a clash');
eq(groupRenameError('Dating', 'DATING', names), '', 'a case-only change of the group itself passes');
eq(groupRenameError('Dating', 'Dating', ['Dating', 'dating', 'Finance']), '', 'another casing of this group is not a clash');
eq(groupRenameError('Gambling', 'gambling', ['Gambling', 'gambling', 'Dating']), '', 'saving the other casing of this group is not a clash');
eq(groupRenameError('Dating', 'Flirt', names), '', 'a free name passes');
let editing = {g: 'Gambling'};
const finish = () => {
  if (!editing) return 'closed';
  const from = String(editing.g || '').trim(), to = from;
  const bad = groupRenameError(from, to, ['Dating', 'Finance', 'Gambling']);
  if (bad) return bad;
  editing = null;
  return '';
};
eq(finish(), '', 'the first save of the same name closes the editor');
eq(editing, null, 'the editor is closed after that save');
eq(finish(), 'closed', 'a second save does not rename and is not taken');

(async () => {
  const rows = [
    {id: 'a', name: 'one', group: 'Dating'},
    {id: 'b/c', name: 'two', group: ' Dating '},
    {id: 'd', name: 'three', group: 'Finance'},
    {id: 'e', name: 'four'},
  ];
  await renameGroupLabel('/api/offers', rows, 'Dating', 'Flirt');
  eq(calls.map(c => c.m + ' ' + c.u), ['PUT /api/offers/a', 'PUT /api/offers/b%2Fc'], 'only the rows of the group are written');
  eq(calls.map(c => c.b.group), ['Flirt', 'Flirt'], 'the PUT body carries the new label');
  eq(calls[0].b.name, 'one', 'the rest of the body is kept');
  eq(rows.map(r => r.group), ['Flirt', 'Flirt', 'Finance', undefined], 'the rows in memory take the new label, others stay');

  calls.length = 0; failAt = 1;
  const rows2 = [{id: 'a', group: 'X'}, {id: 'b', group: 'X'}, {id: 'c', group: 'X'}];
  let err = '';
  try { await renameGroupLabel('/api/offers', rows2, 'X', 'Y'); } catch (e) { err = e.message; }
  eq(err, 'boom', 'a failed request rejects');
  eq(rows2.map(r => r.group), ['Y', 'X', 'X'], 'rows not yet reached keep the old label');

  calls.length = 0; failAt = -1;
  await renameGroupLabel('/api/offers', rows2, 'X', '');
  eq(calls.length, 0, 'an empty new label writes nothing');
  if (failed) process.exit(1);
})();
`)
	file := filepath.Join(t.TempDir(), "grouprename.js")
	if err := os.WriteFile(file, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

// TestGroupRenameWiring checks the pencil of the groups window: it stands next to the name and not in the place of
// Delete, it opens an editor that checks the name before onRename is called, Enter saves and Escape cancels, and every
// list that has the window (campaigns, offers, landings, networks, sources, domains) passes onRename. The messages are
// translated three times and the pencil has colours for both themes through theme variables.
func TestGroupRenameWiring(t *testing.T) {
	html := spaSource()
	win := spaFunctionLine(html, "openGroupWindow")
	must(t, win, `data-gedit="${esc(r.g)}"`, "the pencil carries the group name")
	must(t, win, "icon('edit')", "the pencil icon")
	must(t, win, "typeof opts.onRename==='function'", "a list without onRename draws no pencil")
	must(t, win, `data-gdel="${esc(r.g)}"`, "Delete stays in its own column")
	must(t, win, "e.key==='Enter'", "Enter saves")
	must(t, win, "e.key==='Escape'", "Escape cancels")
	check := strings.Index(win, "groupRenameError(from, to, groupLabels(list))")
	write := strings.Index(win, "await opts.onRename(from, to)")
	if check < 0 || write < 0 || check > write {
		t.Error("the name must be checked before onRename is called")
	}
	saveAt := strings.Index(win, "const save=async()")
	if saveAt < 0 || write < saveAt {
		t.Fatal("save closes the editor before onRename")
	}
	head := win[saveAt:write]
	must(t, head, "if(busy||!editing) return", "a second click does not start another rename")
	must(t, head, "editing=null", "the editor closes before the rename is sent")
	must(t, head, "busy=true", "the first save is the only one in flight")
	must(t, win, "saveBtn.onmousedown=e=>e.preventDefault()", "the first click on the check reaches save")
	for _, kind := range []string{"/api/offers", "/api/landings", "/api/affiliate-networks", "/api/traffic-sources", "/api/domains"} {
		must(t, html, "await renameGroupLabel('"+kind+"'", "rename of "+kind)
	}
	must(t, html, "await setCampaignFolder((GROUPS||[]).filter(c=>String(c.folder||'').trim()===from), to)", "campaigns rename their folder")
	if n := strings.Count(html, "onRename:async (from,to)=>{"); n != 6 {
		t.Errorf("onRename is passed by %d group windows, want 6", n)
	}
	for _, key := range []string{"grp.rename", "grp.rename.empty", "grp.rename.taken"} {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want 3", key, n)
		}
	}
	must(t, spaFunctionLine(html, "pickOwnsKey"), "t.closest('.grp-edit')", "the dialog leaves Enter and Escape of the name editor to it")
	must(t, html, ".grp-name .icon:hover,.grp-name .icon:focus-visible{color:var(--accent)}", "the pencil has a hover colour from the theme")
}
