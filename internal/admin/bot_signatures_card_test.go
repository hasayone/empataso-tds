//go:build uitest

package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBotSignaturesCardState runs botSigStateHTML under node: before the first
// run the card says waiting and not yet; while running it says updating; after
// a run it shows success or error, the last successful write, the rule count
// and every source with its counts and its error. It skips when node is absent.
func TestBotSignaturesCardState(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the bot signature card checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString("const esc=s=>String(s).replace(/</g,'&lt;'); const when=v=>'@'+v;\n")
	src.WriteString("const DICT={'cron.botsig.state':'Статус','cron.botsig.st.wait':'ожидает','cron.botsig.st.running':'обновляется','cron.botsig.st.ok':'успешно','cron.botsig.st.err':'ошибка','cron.botsig.lastok':'Последний успех','cron.botsig.never':'ещё не было','cron.botsig.rules':'Загружено сигнатур','cron.botsig.src':'правил {n}, отклонено {r}'};\n")
	src.WriteString("const tr=k=>DICT[k]||k; const trFill=(k,m)=>{let s=tr(k); Object.keys(m||{}).forEach(n=>{s=s.split('{'+n+'}').join(String(m[n]))}); return s};\n")
	for _, name := range []string{"cronJobOutcome", "botSigStateHTML"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found in SPA", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let failed = 0;
const ok = (cond, what) => { if (!cond) { failed++; console.log('FAIL ' + what); } };
const fresh = botSigStateHTML({status:{jobs:{}}});
ok(fresh.includes('ожидает') && fresh.includes('ещё не было') && fresh.includes('Загружено сигнатур:</span> 0'), 'fresh: ' + fresh);
ok(botSigStateHTML({status:{jobs:{bot_signatures:{running:true}}}}).includes('обновляется'), 'running');
const db = {rules:2519, updated:'2026-11-17T03:00:00Z', sources:[
  {name:'matomo', rules:844, rejected:5},
  {name:'isbot', rules:189, rejected:17, error:'raw.githubusercontent.com: HTTP 503'}]};
const good = botSigStateHTML({status:{jobs:{bot_signatures:{last_run:'2026-11-17T03:00:00Z', ok:true}}, bot_signatures:db}});
ok(good.includes('успешно') && good.includes('@2026-11-17T03:00:00Z') && good.includes('Загружено сигнатур:</span> 2519'), 'success: ' + good);
ok(good.includes('matomo') && good.includes('правил 844, отклонено 5'), 'source counts');
ok(good.includes('HTTP 503'), 'source error is visible');
const bad = botSigStateHTML({status:{jobs:{bot_signatures:{last_run:'2026-11-17T04:00:00Z', ok:false, error:'x'}}, bot_signatures:db}});
ok(bad.includes('ошибка') && bad.includes('@2026-11-17T03:00:00Z'), 'error keeps the last success: ' + bad);
if (failed) process.exit(1);
`)
	f := filepath.Join(t.TempDir(), "botsig.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("bot signature card checks failed:\n%s", out)
	}
}
