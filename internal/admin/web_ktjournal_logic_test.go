//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestKeitaroJournalHTML runs ktJournalHTML under node: nothing before the first import, the live stage
// and rows while a run goes, the stopped stage, the restart notice and Continue for a resumable run,
// and no Continue for a finished one. It skips when node is absent.
func TestKeitaroJournalHTML(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	html := spaSource()
	errFn := spaFunctionLine(html, "ktJobError")
	journal := spaFunctionLine(html, "ktJournalHTML")
	if errFn == "" || journal == "" {
		t.Fatal("ktJobError or ktJournalHTML not found in SPA")
	}
	checks := `
const esc = s => String(s).replace(/[&<>"']/g, c => '&#' + c.charCodeAt(0) + ';');
const tr = k => k;
const trFill = (k, o) => k + JSON.stringify(o);
const fmt = n => String(n);
const when = s => 'at ' + s;
const calloutHTML = (k, t) => '<callout ' + k + '>' + esc(t) + '</callout>';
let failed = 0;
const ok = (cond, what) => { if (!cond) { failed++; console.log('FAIL ' + what); } };
ok(ktJournalHTML(null) === '', 'no job, no journal');
ok(ktJournalHTML({running:false, stage:'idle', started_at:''}) === '', 'idle job, no journal');
const live = ktJournalHTML({running:true, stage:'clicks', done:2, total:5, started_at:'S', updated_at:'U', logs:{clicks:{rows:2000, done:false}}});
ok(live.includes('kt.stage.clicks') && live.includes('kt.c.clicks <b>2000</b> …') && live.includes('kt.journal.updated at U'), 'live stage, rows and change time: ' + live);
ok(!live.includes('kt-resume'), 'no Continue while running');
const cut = ktJournalHTML({running:false, stage:'failed', last_stage:'clicks', interrupted:true, resumable:true, error:'interrupted: admin restarted',
  started_at:'S', finished_at:'F', counts:{conversions:7}, logs:{clicks:{rows:2000, done:false}}});
ok(cut.includes('kt.dock.failed: kt.stage.clicks'), 'stopped stage: ' + cut);
ok(cut.includes('<callout err>kt.journal.interrupted</callout>'), 'restart notice');
ok(cut.includes('kt.c.conversions <b>7</b>') && cut.includes('kt.c.clicks <b>2000</b></span>'), 'rows of both logs');
ok(cut.includes('id="kt-resume"') && cut.includes('kt.journal.ended at F'), 'Continue and stop time');
const timeout = ktJournalHTML({running:false, stage:'failed', last_stage:'clicks', resumable:true, error:'context deadline exceeded', started_at:'S', finished_at:'F'});
ok(timeout.includes('<callout err>context deadline exceeded</callout>') && timeout.includes('kt-resume'), 'timeout error and Continue');
const done = ktJournalHTML({running:false, stage:'done', started_at:'S', finished_at:'F', counts:{clicks:3}});
ok(done.includes('kt.dock.done') && !done.includes('kt-resume') && !done.includes('callout'), 'finished run: ' + done);
const xss = ktJournalHTML({running:false, stage:'failed', error:'<img>', started_at:'S'});
ok(!xss.includes('<img>'), 'error text is escaped');
if (failed) { console.log(failed + ' check(s) failed'); process.exit(1); }
`
	f := filepath.Join(t.TempDir(), "ktjournal.js")
	if err := os.WriteFile(f, []byte(errFn+"\n"+journal+"\n"+checks), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("Keitaro journal checks failed:\n%s", out)
	}
}
