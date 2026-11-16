//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLandingFilesLogic runs the landing file helpers of the SPA under node: the icon
// by extension, the human size, the entry choices and the entry mark. It skips when
// node is absent.
func TestLandingFilesLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	m := pureRe.FindStringSubmatch(spaSource())
	if m == nil {
		t.Fatal("the pure helpers block is not in the SPA")
	}
	checks := `
let failed = 0;
const eq = (got, want, what) => {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { failed++; console.log('FAIL ' + what + '\n  got  ' + g + '\n  want ' + w); }
};
const U = ['B','KB','MB','GB'];
eq(['index.php','a/b.HTML','s/app.js','x.css','i/logo.PNG','pack.zip','v.mp4','s.mp3','log','.env','font.woff2'].map(landFileIcon),
  ['code','code','code','palette','image','archive','video','music','file','file','file'], 'icon by extension');
eq(byteSize(0,'en-US',U), '0\u00a0B', 'zero bytes');
eq(byteSize(-5,'en-US',U), '0\u00a0B', 'negative is zero');
eq(byteSize('x','en-US',U), '0\u00a0B', 'non-number is zero');
eq(byteSize(164,'en-US',U), '164\u00a0B', 'bytes stay whole');
eq(byteSize(2129,'en-US',U), '2.1\u00a0KB', 'one decimal below ten');
eq(byteSize(305012,'en-US',U), '298\u00a0KB', 'no decimal from ten');
eq(byteSize(5*1024*1024,'en-US',U), '5\u00a0MB', 'megabytes');
eq(byteSize(3*1024**4,'en-US',U), '3,072\u00a0GB', 'gigabytes is the largest unit');
const files = [{path:'sub/page.html'},{path:'log.json'},{path:'index.php'},{path:'about.htm'},{path:'a.css'}];
eq(landEntryChoices(files, ''), ['about.htm','index.php','sub/page.html'], 'root pages first, then nested');
eq(landEntryChoices(files, 'index.php'), ['about.htm','index.php','sub/page.html'], 'listed entry is not repeated');
eq(landEntryChoices(files, 'gone.php'), ['gone.php','about.htm','index.php','sub/page.html'], 'missing entry stays first');
eq(landEntryChoices(null, ''), [], 'no files');
eq(landEntryShown(files, 'sub/page.html'), 'sub/page.html', 'stored entry wins');
eq(landEntryShown(files, ''), 'index.php', 'index.php by default');
eq(landEntryShown([{path:'index.html'},{path:'x.php'}], ' '), 'index.html', 'index.html after index.php');
eq(landEntryShown([{path:'x.php'}], ''), '', 'nothing to name');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
	f := filepath.Join(t.TempDir(), "landfiles.js")
	if err := os.WriteFile(f, []byte(m[1]+checks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("landing file helper checks failed:\n%s", out)
	}
}
