package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFlowDockPhone checks the phone totals bar: the counters wrap into compact rows,
// Export is a download icon on the pager row, the bar hides while the campaign search
// is focused, and the wide-screen bar is left as it is. Summary cards stay four per row.
func TestFlowDockPhone(t *testing.T) {
	html := spaSource()
	must(t, html, ".flowsum{display:flex;flex-wrap:wrap;gap:14px 18px;align-items:center;margin:0;padding:8px 16px;", "wide screen keeps the wrapped totals")
	must(t, html, ".flowsum .sumi{display:flex;flex-direction:column;gap:1px;min-width:64px}", "wide screen stacks a label over its number")
	must(t, html, "body.has-flowdock main{padding-bottom:168px}", "wide screen keeps the tall dock clearance")
	must(t, html, ".kpis{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin-bottom:14px}", "summary cards stay four per row")
	mustNot(t, html, ".kpis{display:flex;flex-wrap:nowrap;", "summary cards are not one scrolling row")
	mustNot(t, html, ".kpis{grid-template-columns:repeat(3,minmax(0,1fr))}", "summary cards are not three per row")
	phone := spaBetween(html, "@media (max-width:760px){", "th .link{font:inherit;")
	if phone == "" {
		t.Fatal("phone dock rule not found")
	}
	must(t, phone, ".flowdock{display:flex;flex-direction:column;", "phone dock stacks the totals and the pager")
	must(t, phone, ".flowdock{display:flex;flex-direction:column;align-items:stretch;overflow:visible}", "the pager is not clipped off the dock")
	must(t, phone, ".flowsum{flex:none;display:flex;flex-wrap:wrap;", "phone totals wrap into rows")
	must(t, phone, ".flowsum .sumi{flex:none;flex-direction:row;", "phone counters sit beside their labels")
	must(t, phone, ".flowsum .sumi .hint{font-size:12px;", "phone totals stay readable")
	must(t, phone, ".flowsum .sumi b{font-size:12px}", "phone numbers stay readable")
	mustNot(t, phone, "overflow-x:auto", "phone totals do not scroll sideways")
	mustNot(t, phone, "font-size:11px", "phone totals are not smaller than 12px")
	must(t, phone, ".flowdock .flowpage{display:flex;flex-wrap:nowrap;", "export stays on the pager row")
	must(t, phone, ".flowdock .flowpage>button .exp-lbl,.flowdock .flowpage>button .exp-caret{display:none}", "the export word hides on a phone")
	must(t, phone, "body.has-flowdock main,body.has-pager main{padding-bottom:112px}", "phone dock clearance fits the compact rows")
	must(t, phone, "body.dock-search .flowdock{display:none}", "a focused campaign search hides the dock")
	must(t, phone, "body.dock-search.has-flowdock main{padding-bottom:16px}", "hiding the dock gives the list its space back")
	must(t, phone, ".canvas .entry{height:32px;padding:0 8px;gap:6px;font-size:10px}", "the visitor chip is 4px smaller on a phone")
	must(t, phone, ".canvas .entry .ell{overflow:visible;text-overflow:clip", "the campaign id is not cut on a phone")
	must(t, html, ".entry{grid-column:1/3;justify-self:start;margin-left:14px;display:inline-flex;align-items:center;gap:10px;height:40px;padding:0 14px;", "a wide screen keeps the visitor chip")
	if strings.Contains(phone, ".kpis{") {
		t.Error("the phone dock rule must not restyle the summary cards")
	}
	if strings.Count(html, "body.dock-search .flowdock{display:none}") != 1 {
		t.Error("the campaign search hides the dock only on a phone")
	}
	if strings.Count(html, ".canvas .entry{height:32px;padding:0 8px;gap:6px;font-size:10px}") != 1 {
		t.Error("the smaller visitor chip belongs only to the phone rule")
	}
	dock := spaFunctionLine(html, "dockHTML")
	must(t, dock, `aria-label="${esc(label)}" title="${esc(label)}"`, "export keeps its name for assistive tech")
	must(t, dock, `<span class="exp-lbl">${esc(label)}</span>`, "a wide screen still shows the export word")
	must(t, dock, `${icon('download')}`, "export keeps the download icon")
	fn := spaFunctionLine(html, "bindDockSearch")
	if fn == "" {
		t.Fatal("bindDockSearch not found")
	}
	must(t, fn, "classList.add('dock-search')", "focus marks the phone search")
	must(t, fn, "classList.remove('dock-search')", "blur brings the dock back")
	must(t, html, "bindDockSearch(q('#fq'))", "only the campaign search hides the dock")
	must(t, html, "classList.remove('has-flowdock','has-pagehover','has-pager','dock-search')", "leaving a page brings the dock back")
}

// TestFlowTrafficWord checks the canvas order button: off is the icon alone,
// asc and desc (the traffic modes) show the traffic word, rev-asc and rev-desc show the revenue word.
func TestFlowTrafficWord(t *testing.T) {
	html := spaSource()
	for _, key := range []string{"flow.traffic.word", "flow.revenue.word"} {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("%s: %d dictionary entries, want one per language", key, n)
		}
	}
	must(t, html, "'flow.traffic.word':'traffic'", "english traffic word")
	must(t, html, "'flow.traffic.word':'трафик'", "russian traffic word")
	must(t, html, "'flow.traffic.word':'трафік'", "ukrainian traffic word")
	must(t, html, "'flow.revenue.word':'revenue'", "english revenue word")
	must(t, html, "'flow.revenue.word':'доход'", "russian revenue word")
	must(t, html, "'flow.revenue.word':'дохід'", "ukrainian revenue word")
	word := spaFunctionLine(html, "flowTrafficWord")
	if word == "" {
		t.Fatal("flowTrafficWord not found")
	}
	must(t, word, "if(!mode) return '';", "off has no word")
	must(t, word, "mode.startsWith('rev') ? 'flow.revenue.word' : 'flow.traffic.word'", "revenue and traffic use their own words")
	btn := spaFunctionLine(html, "flowTrafficButton")
	must(t, btn, "flowTrafficWord(mode)", "the button asks for the short word")
	must(t, btn, "${word?`<span class=\"traf-w\">${esc(word)}</span>`:''}", "the word is left out when sorting is off")
	must(t, btn, `data-traffic="${key}"`, "the button names the real mode")
	must(t, btn, "aria-label=", "the accessible name still describes the mode")
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping the canvas order word checks")
	}
	var src strings.Builder
	src.WriteString("const tr=k=>({'flow.traffic.word':'traffic','flow.revenue.word':'revenue','flow.traffic.off':'Sorting: off','flow.traffic.asc':'Order by traffic: fewest first','flow.traffic.desc':'Order by traffic: most first','flow.revenue.asc':'Order by revenue: least first','flow.revenue.desc':'Order by revenue: most first'}[k]||k);\n")
	src.WriteString("const esc=s=>String(s==null?'':s); const icon=n=>'<svg class=\"ic\"></svg>';\n")
	for _, name := range []string{"FLOW_TRAFFIC_MODES"} {
		i := strings.Index(html, "const "+name)
		if i < 0 {
			t.Fatalf("%s not found", name)
		}
		line := html[i:]
		if n := strings.Index(line, "\n"); n >= 0 {
			line = line[:n]
		}
		src.WriteString(line + "\n")
	}
	for _, name := range []string{"flowTrafficMode", "flowTrafficWord", "flowTrafficButton"} {
		fn := spaFunctionLine(html, name)
		if fn == "" {
			t.Fatalf("%s not found", name)
		}
		src.WriteString(fn + "\n")
	}
	src.WriteString(`
let FLOW_TRAFFIC='';
let failed=0;
const ok=(c,w)=>{ if(!c){ failed++; console.log('FAIL '+w); } };
const wordOf=html=>{ const m=html.match(/<span class="traf-w">([^<]*)<\/span>/); return m?m[1]:''; };
const attr=html=>{ const m=html.match(/data-traffic="([^"]*)"/); return m?m[1]:''; };
const check=(mode,wantAttr,wantWord)=>{
  FLOW_TRAFFIC=mode;
  const html=flowTrafficButton();
  ok(attr(html)===wantAttr, mode+' data-traffic '+attr(html)+' want '+wantAttr);
  ok(wordOf(html)===wantWord, mode+' word '+JSON.stringify(wordOf(html))+' want '+JSON.stringify(wantWord));
  ok(html.includes('aria-label='), mode+' keeps an accessible name');
};
check('','off','');
check('asc','asc','traffic');
check('desc','desc','traffic');
check('rev-asc','rev-asc','revenue');
check('rev-desc','rev-desc','revenue');
if(failed) process.exit(1);
`)
	file := filepath.Join(t.TempDir(), "traffic.js")
	if err := os.WriteFile(file, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, file).CombinedOutput(); err != nil {
		t.Fatalf("canvas order word: %v\n%s", err, out)
	}
}
