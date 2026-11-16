//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestKeitaroSyncCardCampaigns runs the catch-up card helpers under node: the last run line gets
// «new campaigns N» after clicks and conversions only for a successful run that reported the count,
// and the hint rows show the queue, the campaigns deleted here, the failed ids and an active pause.
// It skips when node is absent.
func TestKeitaroSyncCardCampaigns(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the catch-up card checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("const esc=s=>String(s).replace(/</g,'&lt;'); const when=v=>'@'+v; const ago=v=>'ago:'+v; const errInfo=()=>null;\n")
	src.WriteString("const DICT={'cron.ktsync.newcamps':'новых кампаний {n}','cron.ktsync.camps.pending':'В очереди','cron.ktsync.camps.deleted':'Удалены у нас','cron.ktsync.camps.failed':'Не перенесены','cron.ktsync.camps.paused':'Пауза до {time}: {reason}'};\n")
	src.WriteString("const tr=k=>DICT[k]||k; const trFill=(k,m)=>{let s=tr(k); Object.keys(m||{}).forEach(n=>{s=s.split('{'+n+'}').join(String(m[n]))}); return s};\n")
	for _, name := range []string{"cronJobOutcome", "cronLagText", "keitaroSyncState", "keitaroSyncCursorsHTML", "keitaroSyncCampaignsText", "cronJobMsg", "cronJobLine"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const ok = (cond, what) => { if (!cond) { failed++; console.log('FAIL ' + what); } };
const job = {last_run:'2026-11-16T17:12:04Z', ok:true, message:'clicks 239, conversions 728'};
const st = {status:{jobs:{keitaro_sync:job}}, keitaro_sync_state:{last_campaigns:3, campaigns_pending:5, campaigns_deleted:11,
  campaigns_failed:[7401,7402], campaigns_paused_until:new Date(Date.now()+3600e3).toISOString(), campaigns_error:'Keitaro ответил 402'}};
const tail = keitaroSyncCampaignsText(st);
ok(tail === 'новых кампаний 3', 'tail text: ' + tail);
const line = cronJobLine(job, tail);
ok(line.includes('clicks 239, conversions 728, новых кампаний 3'), 'last run line: ' + line);
ok(!cronJobLine(job, '').includes('новых'), 'no tail without the count');
ok(keitaroSyncCampaignsText({status:{jobs:{keitaro_sync:{ok:false}}}, keitaro_sync_state:{last_campaigns:2}}) === '', 'failed run has no tail');
ok(keitaroSyncCampaignsText({status:{jobs:{keitaro_sync:{ok:true}}}, keitaro_sync_state:{}}) === '', 'old state has no tail');
ok(keitaroSyncCampaignsText({status:{jobs:{keitaro_sync:{ok:true}}}, keitaro_sync_state:{last_campaigns:0}}) === 'новых кампаний 0', 'zero is shown');
const rows = keitaroSyncCursorsHTML(st);
ok(rows.includes('В очереди:</span> 5'), 'pending row: ' + rows);
ok(rows.includes('Удалены у нас:</span> 11'), 'deleted row');
ok(rows.includes('7401, 7402'), 'failed ids');
ok(rows.includes('Keitaro ответил 402'), 'pause reason');
const past = {keitaro_sync_state:{campaigns_paused_until:'2000-01-01T00:00:00Z', campaigns_error:'old'}};
ok(!keitaroSyncCursorsHTML(past).includes('old'), 'an expired pause is hidden');
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "card.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("catch-up card checks failed:\n%s", out)
	}
}
