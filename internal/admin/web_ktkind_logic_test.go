//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// lossKindRe finds the kind of a Keitaro import loss literal in Go source.
var lossKindRe = regexp.MustCompile(`Loss\{\{?\s*Kind:\s*"([a-z_]+)"`)

// goLossKinds collects every loss kind the import code writes.
// dirs are the package directories to read; test files are left out.
// It returns the sorted distinct kinds and fails the test when a directory cannot be read.
func goLossKinds(t *testing.T, dirs ...string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range lossKindRe.FindAllStringSubmatch(string(b), -1) {
				seen[m[1]] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestKeitaroKindTone runs the status pill helpers of the Keitaro import report under node:
// every loss kind the import writes has a tone and a label in each language, skipped and
// approximated are yellow, conflict and disabled red, a saved change green and a planned one info.
// It skips when node is absent.
func TestKeitaroKindTone(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	html := spaSource()
	m := pureRe.FindStringSubmatch(html)
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	pill := spaFunctionLine(html, "ktKindPill")
	if pill == "" {
		t.Fatal("ktKindPill not found in SPA")
	}
	kinds := goLossKinds(t, ".", filepath.Join("..", "bridge", "ktimport"))
	for _, k := range kinds {
		if n := strings.Count(html, "'kt.kind."+k+"':"); n != 3 {
			t.Errorf("kt.kind.%s has %d translations, want 3 (en, ru, uk)", k, n)
		}
	}
	list := `['` + strings.Join(kinds, `','`) + `']`
	checks := `
const esc = s => String(s).replace(/[&<>"']/g, c => '&#' + c.charCodeAt(0) + ';');
const tr = k => k;
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
for (const k of ` + list + `) {
  eq(Object.prototype.hasOwnProperty.call(KT_KIND_TONES, k), true, 'kind ' + k + ' has its own tone');
}
eq(['skipped','approximated','conflict','disabled','changed'].map(k => ktKindTone(k)), ['warn','warn','bad','bad','ok'], 'tones of the import kinds');
eq(['skipped','approximated','changed'].map(k => ktKindTone(k, true)), ['warn','warn','info'], 'a dry run keeps warnings and plans changes');
eq([ktKindTone('later'), ktKindTone(''), ktKindTone(null), ktKindTone('toString')], ['info','info','info','info'], 'an unknown kind is info');
eq(ktKindPill('skipped', 4), '<span class="pill tone warn">kt.kind.skipped 4</span>', 'skipped pill is yellow with its count');
eq(ktKindPill('approximated'), '<span class="pill tone warn">kt.kind.approximated</span>', 'approximated pill is yellow');
eq(ktKindPill('conflict', ''), '<span class="pill tone bad">kt.kind.conflict</span>', 'conflict pill is red');
eq(ktKindPill('changed', null, true), '<span class="pill tone info">kt.kind.changed</span>', 'planned change is info');
eq(ktKindPill('skipped', '<b>'), '<span class="pill tone warn">kt.kind.skipped &#60;b&#62;</span>', 'the tail is escaped');
eq([ktDryRun({dry_run:1}), ktDryRun({groups:2}), ktDryRun(null)], [true,false,false], 'dry run comes from the counts');
eq(streamNoteParts('keitaro_stream_id=5'), {id:'5', rest:'', skipped:''}, 'plain import note');
eq(streamNoteParts('keitaro_stream_id=5; ручная пометка; не перенесены фильтры Keitaro: bot, limit'), {id:'5', rest:'ручная пометка', skipped:'bot, limit'}, 'note with a hand mark and skipped filters');
eq(streamNoteParts('keitaro_stream_id=30001; не перенесены фильтры Keitaro: country'), {id:'30001', rest:'', skipped:'country'}, 'note with skipped filters');
eq(streamNoteParts(' my note '), {id:'', rest:'my note', skipped:''}, 'any other note stays');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
	f := filepath.Join(t.TempDir(), "ktkind.js")
	if err := os.WriteFile(f, []byte(m[1]+"\n"+pill+checks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("Keitaro kind pill checks failed:\n%s", out)
	}
}
