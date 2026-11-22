//go:build uitest

// Behaviour of failed statistics reads on stub objects, run under node. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestStatsFailLogic
package admin

import "testing"

// TestStatsFailLogic checks statsGet, statOr, statsFailHTML, loadMoney and loadPerf: a failed facts read keeps
// its error, leaves FACTS empty and not ready, draws figures as a dash and shows an error notice with a retry
// button; real zeros stay zeros; an answered read clears the error and the notice; a failed performance read
// is asked again on the next load.
func TestStatsFailLogic(t *testing.T) {
	runSPANode(t, nodeEq+`
const STATS_ERR={}, STAT_NONE='—';
const tr=k=>k, esc=s=>String(s), icon=()=>'', heavyNote=()=>{};
const trFill=(k,o)=>k+':'+o.err, errText=e=>e.message;
const calloutHTML=(kind,text,o)=>'<callout '+kind+'>'+text+o.actions+'</callout>';
const rangeQS=()=>'from=A&to=B';
let answers={}, calls=[];
const call=async(m,p)=>{calls.push(p); const a=answers[p.split('?')[0]]; if(a instanceof Error) throw a; return a;};
const get=(p,def)=>call('GET',p).catch(()=>def);
let FACTS={ready:false,clicks:[],status:[]}, SPEND=[], PERF=null;
`, []string{"statsGet", "statsFailed", "statOr", "statsFailHTML", "loadMoney", "loadPerf"}, `
(async()=>{
answers={'/api/stats/facts':new Error('query too heavy'), '/api/spend':[]};
await loadMoney();
eq(FACTS, {ready:false,clicks:[],status:[]}, 'a failed facts read leaves FACTS empty and not ready');
eq(statsFailed(['facts']), true, 'the failure is kept');
eq(statOr(['facts'],'0'), '—', 'a figure that rests on it is a dash');
eq(statOr(['perf'],'0'), '0', 'other reads still draw their figures');
const box=statsFailHTML(['summary','facts']);
eq(box.startsWith('<callout err>stats.fail:query too heavy'), true, 'the notice names the error');
eq(box.includes('data-stats-retry')&&box.includes('stats.retry'), true, 'the notice has a retry button');

answers['/api/stats/facts']={ready:true,clicks:[{group_id:'g',hits:0}],status:[]};
await loadMoney();
eq(FACTS.ready, true, 'an answered read fills FACTS');
eq(statsFailed(['facts']), false, 'an answered read clears the failure');
eq(statOr(['facts'],'0'), '0', 'a real zero stays a zero');
eq(statsFailHTML(['summary','facts']), '', 'no notice once every read answered');

answers['/api/stats/performance']=new Error('down'); calls=[];
await loadPerf();
eq(PERF, [], 'a failed performance read leaves no rows');
eq(statsFailed(['perf']), true, 'the performance failure is kept');
answers['/api/stats/performance']=[{group_id:'g',hits:3}];
await loadPerf();
eq(calls.filter(p=>p.startsWith('/api/stats/performance')).length, 2, 'a failed performance read is asked again');
eq(PERF, [{group_id:'g',hits:3}], 'the second read fills PERF');
await loadPerf();
eq(calls.filter(p=>p.startsWith('/api/stats/performance')).length, 2, 'an answered read is kept until forced');
if (failed) process.exit(1);
})();
`)
}

// TestStatsFailWired checks that the campaigns list and the dashboard draw failed reads as a dash and show the
// notice with a working retry instead of zeros.
func TestStatsFailWired(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "flowMetricCell"), "if(FLOW_METRIC.has(id)&&statsFailed(['facts'])) return", "campaign metric cells are a dash when facts failed")
	must(t, spaFunctionLine(html, "paintMetricSum"), "fail?STAT_NONE:val", "the totals row is a dash when facts failed")
	must(t, spaFunctionLine(html, "paintFlowCards"), "statsFailHTML(FLOW_VIEW==='table'?['facts']:['perf'])", "the campaigns list shows the notice")
	must(t, spaFunctionLine(html, "flows"), "if(e.target.closest('[data-stats-retry]')) reload()", "the campaigns notice retries the reads")
	dash := spaFunctionLine(html, "dashboard")
	must(t, dash, "statsFailHTML(['summary','timeseries','facts','perf'])", "the dashboard shows the notice")
	must(t, dash, "qa('[data-stats-retry]', dashBody||view()).forEach(b=>b.onclick=()=>busy(b, reloadDash))", "the dashboard notice retries the reads")
	must(t, dash, "visits:kpi(statOr(SUM,fmt(total))", "dashboard tiles are a dash when their read failed")
}
