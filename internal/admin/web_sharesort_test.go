//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShareSortLogic runs the ordering of the offers and landings of a stream under node: two items swap and the
// new order is what the stream form reads, a list of one row has no drag handle and a list of two or more has one on
// every row, and removing a row down to one takes the handles away. It skips when node is absent.
func TestShareSortLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the share order checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(`const tr=s=>s, icon=()=>'<svg class="ic"></svg>';
const document={createElement:()=>({className:'', tabIndex:-1, attrs:{}, title:'', innerHTML:'', setAttribute(k,v){this.attrs[k]=v}})};
const mkRow=id=>{const r={id, grip:null, querySelector(s){return s==='.share-grip'?r.grip:null}, prepend(g){r.grip=g; g.remove=()=>{r.grip=null}}}; return r};
const mkBox=rows=>({rows, parentElement:{classList:{on:false, toggle(c,v){this.on=v}}}, querySelectorAll(){return this.rows}});
`)
	for _, name := range []string{"shareSortable", "reorderShare", "syncShareSort"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const ok = (c, w) => { if (!c) { failed++; console.log('FAIL ' + w); } };
const eq = (a, b, w) => ok(JSON.stringify(a) === JSON.stringify(b), w + ': got ' + JSON.stringify(a) + ', want ' + JSON.stringify(b));
const ids = list => list.map(x => x.id);
for (const kind of ['offers', 'landings']) {
  const a = {id: kind + '1', share: 50}, b = {id: kind + '2', share: 50}, c = {id: kind + '3', share: 0};
  eq(ids(reorderShare([a, b], 0, 1, true)), [kind + '2', kind + '1'], kind + ': two items swap, dropped after the second');
  eq(ids(reorderShare([a, b], 1, 0, false)), [kind + '2', kind + '1'], kind + ': two items swap, dropped before the first');
  eq(ids(reorderShare([a, b, c], 0, 2, true)), [kind + '2', kind + '3', kind + '1'], kind + ': the first goes to the end');
  eq(ids(reorderShare([a, b, c], 2, 0, false)), [kind + '3', kind + '1', kind + '2'], kind + ': the last goes to the front');
  eq(ids(reorderShare([a, b, c], 0, 1, false)), [kind + '1', kind + '2', kind + '3'], kind + ': dropped where it already stands');
  eq(ids(reorderShare([a, b, c], 1, 1, true)), [kind + '1', kind + '2', kind + '3'], kind + ': dropped on itself');
  eq(ids(reorderShare([a, b], 5, 0, true)), [kind + '1', kind + '2'], kind + ': a bad index changes nothing');
  eq([a.share, b.share], [50, 50], kind + ': the shares travel with their items');
  const src = [a, b]; reorderShare(src, 0, 1, true);
  eq(ids(src), [kind + '1', kind + '2'], kind + ': the source list is not changed');
}
eq([shareSortable(0), shareSortable(1), shareSortable(2), shareSortable(5)], [false, false, true, true], 'only more than one row sorts');
const one = mkBox([mkRow('x')]);
eq(syncShareSort(one), false, 'one row is not sortable');
ok(one.rows[0].grip === null, 'one row gets no handle');
ok(one.parentElement.classList.on === false, 'one row does not open the handle column');
const two = mkBox([mkRow('x'), mkRow('y')]);
eq(syncShareSort(two), true, 'two rows are sortable');
ok(two.rows.every(r => r.grip && r.grip.className === 'share-grip' && r.grip.tabIndex === 0 && r.grip.attrs.role === 'button'), 'every row has a focusable handle');
ok(two.parentElement.classList.on === true, 'two rows open the handle column');
const first = two.rows[0].grip;
syncShareSort(two);
ok(two.rows[0].grip === first, 'syncing again keeps the same handle');
two.rows.pop();
eq(syncShareSort(two), false, 'back to one row');
ok(two.rows[0].grip === null, 'the last row loses its handle');
ok(two.parentElement.classList.on === false, 'the handle column closes');
if (failed) process.exit(1);
`)
	dir := t.TempDir()
	file := filepath.Join(dir, "share.js")
	if err := os.WriteFile(file, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

// TestShareSortWiring checks that the offers and the landings of a stream use one and the same ordering code, that
// a drop or a key press moves the rows and writes the order through the stream form, which reads the rows in screen
// order into the same offers and landings arrays that are saved with the campaign, and that the handle, the drop
// line and the cursor are drawn from theme colours.
func TestShareSortWiring(t *testing.T) {
	html := spaSource()
	box := spaFunctionLine(html, "bindShareBox")
	must(t, box, "bindShareSort(box);", "every share list is bound for ordering")
	if n := strings.Count(box, "bindShareSort(box);"); n != 2 {
		t.Errorf("bindShareBox orders the list after a removal and on bind, found %d calls, want 2", n)
	}
	must(t, spaFunctionLine(html, "bindOfferNet"), "bindShareBox(body, 'offer-rows', 'data-oshare', 'data-oid');", "offers use the shared list code")
	must(t, spaFunctionLine(html, "bindLandingRows"), "bindShareBox(body, 'landing-rows', 'data-lshare', 'data-lid');", "landings use the shared list code")
	sortFn := spaFunctionLine(html, "bindShareSort")
	must(t, sortFn, "reorderShare(list, list.indexOf(row), list.indexOf(over), after).forEach(r=>box.append(r));", "the rows are moved in place with their values")
	must(t, sortFn, "collectStream();", "the new order goes into the stream")
	must(t, sortFn, "changed(box);", "the form is marked changed")
	must(t, sortFn, "e.key==='ArrowUp'?-1:e.key==='ArrowDown'?1:0", "the handle can be moved with the keys")
	must(t, sortFn, "grip.onpointerdown=()=>{row.draggable=true};", "a row is draggable only from its handle")
	must(t, sortFn, "SHARE_DRAG.parentElement!==box", "a row is dropped only inside its own list")
	collect := spaFunctionLine(html, "collectStream")
	must(t, collect, "offers:q('#offer-rows',d)?[...qa('.offer-row',d)].map(row=>readShare(row,'data-oshare','data-oid')).filter(r=>r.id):[]", "the offers are read in screen order")
	must(t, collect, "landings:q('#landing-rows',d)?[...qa('.land-row',d)].map(row=>readShare(row,'data-lshare','data-lid')).filter(r=>r.id):[]", "the landings are read in screen order")
	must(t, spaFunctionLine(html, "shareHead"), `<span class="share-gap"></span>`, "the caption row has the cell of the handle column")
	for _, css := range []string{
		".fld.sortable .share-row{grid-template-columns:20px minmax(0,1fr) 60px auto}",
		".share-grip{display:grid;place-items:center;height:32px;color:var(--subtle);cursor:grab;",
		".share-row.drop-before{box-shadow:0 -6px 0 -4px var(--accent);",
		".share-row.drop-after{box-shadow:0 6px 0 -4px var(--accent);",
		"body.share-dragging,body.share-dragging *{cursor:grabbing}",
	} {
		must(t, html, css, "the handle, the drop line and the cursor are styled")
	}
	if n := strings.Count(html, "'share.move':"); n != 3 {
		t.Errorf("share.move is translated %d times, want 3", n)
	}
}
