//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// groupsListLiteral matches a quoted /api/groups path literal with an optional query in the SPA source.
var groupsListLiteral = regexp.MustCompile(`'/api/groups(\?[^']*)?'`)

// TestSPAReadsGroupsShared checks that the SPA never reads the full campaign list in the plain form: every
// '/api/groups' literal without a query belongs to a PUT call, and every query is shared=1.
func TestSPAReadsGroupsShared(t *testing.T) {
	html := spaSource()
	found := 0
	for _, m := range groupsListLiteral.FindAllStringSubmatchIndex(html, -1) {
		found++
		lit, before := html[m[0]:m[1]], strings.TrimSpace(html[max(0, m[0]-40):m[0]])
		switch query := lit[len("'/api/groups") : len(lit)-1]; {
		case query == "" && !strings.HasSuffix(before, "call('PUT',"):
			t.Errorf("plain GET /api/groups in SPA after %q", before)
		case query != "" && query != "?shared=1":
			t.Errorf("GET /api/groups%s is not the shared form", query)
		}
	}
	if found == 0 {
		t.Fatal("no /api/groups literal found in SPA")
	}
}

// sharedListsDecl declares the shared list registry for node harnesses that run groupJSON or groupParse.
const sharedListsDecl = "const SHARED_LISTS=new Map(), SHARED_RAW=new Map(), SHARED_VALUES=new Map();\n"

// TestSharedGroupRowsLogic runs groupRows, groupJSON and groupParse under node: the shared answer gets its long
// lists back with each stream's own flag, a snapshot names a kept list by token and reads back equal, an edit of
// one stream does not reach the others, and a second load reuses the kept list.
func TestSharedGroupRowsLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the shared campaign list checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(sharedListsDecl)
	for _, name := range []string{"sharedListEntry", "groupRows", "groupJSON", "groupParse"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const vals = Array.from({length: 300}, (_, i) => 'Model ' + i);
const answer = () => ({
  groups: [
    {id: 'a', streams: [{name: 's', rules: {model: {flag: 2}, country: {flag: 2, raw: 'US', values: ['US']}}}]},
    {id: 'b', streams: [{name: 'x', rules: {}}, {name: 's', rules: {model: {flag: 1}}}]},
  ],
  lists: [{raw: vals.join(','), values: vals.slice()}],
  refs: [[0, 0, 'model', 0], [1, 1, 'model', 0], [5, 0, 'model', 0], [0, 0, 'model', 9]],
});
eq(groupRows([{id: 'plain'}]).map(g => g.id), ['plain'], 'a plain array is kept');
eq(groupRows(null), [], 'no answer is an empty list');
const rows = groupRows(answer());
eq(rows[0].streams[0].rules.model.flag, 2, 'the first stream keeps its flag');
eq(rows[1].streams[1].rules.model.flag, 1, 'the second stream keeps its flag');
eq(rows[0].streams[0].rules.model.values.length, 300, 'the list is back');
eq(rows[0].streams[0].rules.model !== rows[1].streams[1].rules.model, true, 'each stream has its own filter object');
eq(rows[0].streams[0].rules.model.values === rows[1].streams[1].rules.model.values, true, 'the values are one copy');
eq(rows[0].streams[0].rules.country, {flag: 2, raw: 'US', values: ['US']}, 'a short filter is untouched');

const snap = groupJSON(rows[0]);
eq(snap.length < 400, true, 'the snapshot names the list instead of copying it');
const back = groupParse(snap);
eq(JSON.stringify(back), JSON.stringify(rows[0]), 'the snapshot reads back equal');
eq(back.streams[0].rules.model.values === rows[0].streams[0].rules.model.values, true, 'the read snapshot uses the kept list');

rows[0].streams[0].rules.model = {flag: 2, raw: 'Pixel 8', values: ['Pixel 8']};
eq(groupJSON(rows[0]) !== snap, true, 'an edited filter changes the snapshot text');
eq(rows[1].streams[1].rules.model.values.length, 300, 'the edit does not reach another stream');

const again = groupRows(answer());
eq(again[0].streams[0].rules.model.values === rows[1].streams[1].rules.model.values, true, 'a second load reuses the kept list');
eq(SHARED_LISTS.size, 1, 'one list is kept');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "groups_shared.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("shared campaign list checks failed:\n%s", out)
	}
}
