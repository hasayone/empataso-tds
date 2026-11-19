//go:build uitest

// Logic of the Tools geo downloads, the IP check history and the postback log page, run under node with the real dictionaries.
// Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TestToolsPostbackLogLogic
package admin

import "testing"

// TestToolsPostbackLogLogic checks the geo action cell for each slot and right, the reasons of a refused
// download, the postback log query and result labels, and that every new key has a text in each language.
func TestToolsPostbackLogLogic(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "openPostbacks"), "id=\"pb-log\"", "the postbacks popup has the log button")
	must(t, spaFunctionLine(html, "profile"), "postbackLogPage()", "settings/pblog opens the log page")
	must(t, spaFunctionLine(html, "tools"), "loadLookupHistory(run)", "the Tools page lists the history")
	prelude := nodeEq + `
let LANG='ru';
const uiLang=()=>LANG;
const esc=s=>String(s==null?'':s);
const icon=()=>'';
let GEO_WAIT=null, GEO_POLL_TIMER=0;
` + spaLangDicts(html, "PHRASE_EXTRA") + spaLangDicts(html, "I18N") + spaLangDicts(html, "PHRASE") +
		spaStatement(t, html, "const PBLOG=") + spaStatement(t, html, "const PBLOG_PAGE=") +
		spaStatement(t, html, "const PBLOG_PERIODS=") + spaStatement(t, html, "const PBLOG_OUTCOMES=")
	names := []string{"tr", "trFill", "isDBIPURL", "geoKindLabel", "geoErrText", "geoActionHTML", "pblogOutcomeLabel", "pblogTone", "pblogQS"}
	runSPANode(t, prelude, names, `
const keys = ['geo.kind.city','geo.kind.asn','geo.kind.dbip','geo.action','geo.download','geo.update','geo.dbip','geo.or','geo.needkey',
  'geo.ask','geo.nocron','geo.running','geo.waiting','geo.dead','geo.last.ok','geo.last.err','geo.requested','geo.done','geo.timeout',
  'geo.nocity','geo.err.needs_key','geo.err.no_path','geo.err.source_set','geo.dbip.by',
  'hist.title','hist.empty','hist.user','hist.all','hist.clear','hist.clear.q','hist.clear.all','hist.clear.user','hist.cleared',
  'hist.range','hist.newer','hist.older','hist.again','hist.proxy',
  'pblog.title','pblog.sub','pblog.period','pblog.result','pblog.network','pblog.net.all','pblog.apply','pblog.empty','pblog.range','pblog.denied',
  'pblog.o.all','pblog.method','pblog.path','pblog.ua','pblog.campaign','pblog.conversion','pblog.conv','pblog.conv.dup',
  'pblog.conv.none','pblog.param','pblog.value','pblog.noparams',
  ...PBLOG_OUTCOMES.map(o=>'pblog.o.'+o), ...Object.keys(PBLOG_PERIODS).map(p=>'pblog.p.'+p)];
for (const lang of ['en','ru','uk']) { LANG = lang; for (const k of keys) eq(tr(k) !== k, true, lang + ' text of ' + k); }
LANG = 'ru';

eq(isDBIPURL('https://download.db-ip.com/free/dbip-city-lite-{YYYY-MM}.mmdb.gz'), true, 'DB-IP host');
eq(isDBIPURL('https://download.maxmind.com/x'), false, 'MaxMind host');
eq(geoKindLabel('city'), 'Город и страна', 'city label');

const st = {cron:true, can_download:true, job:{}};
const city = {kind:'city', loaded:false, ready:false, needs_key:true, free_alt:'dbip'};
eq(geoActionHTML(city, st).includes('data-src="dbip"'), true, 'keyless city offers DB-IP');
eq(geoActionHTML(city, st).includes('#/automation'), true, 'keyless city links the key setting');
eq(geoActionHTML({kind:'asn', ready:false, needs_key:true}, st).includes('data-geo'), false, 'keyless ASN has no button');
eq(geoActionHTML({kind:'dbip_asn', ready:true, loaded:true}, st).includes('Обновить'), true, 'a loaded ready slot updates');
eq(geoActionHTML({kind:'dbip_asn', ready:true, loaded:false}, st).includes('Скачать'), true, 'an empty ready slot downloads');
eq(geoActionHTML(city, {cron:true, can_download:false}), '<span class="subtle">попросите администратора</span>', 'no right asks the administrator');
eq(geoActionHTML(city, {cron:false, can_download:true}).includes('cron'), true, 'no cron says so');
eq(geoActionHTML({kind:'dbip_asn', ready:true}, {cron:true, can_download:true, job:{running:true}}).includes('disabled'), true, 'a running job disables the button');

const refused = reason => ({message:'x', body:{params:{reason}}});
eq(geoErrText(refused('needs_key')).includes('MaxMind'), true, 'needs_key reason');
eq(geoErrText(refused('nope')), 'x', 'an unknown reason keeps the server text');

eq(pblogOutcomeLabel({outcome:'ok', duplicate:true}), 'Дубль', 'an accepted repeat is a duplicate');
eq(pblogOutcomeLabel({outcome:'bad_key'}), 'Неверный ключ или подпись', 'bad key');
eq(pblogOutcomeLabel({outcome:'404'}), '404', 'an unknown outcome stays');
eq(pblogTone({outcome:'ok'}), 'ok', 'accepted tone');
eq(pblogTone({outcome:'ok', duplicate:true}), 'warn', 'duplicate tone');
eq(pblogTone({outcome:'click_not_found'}), 'bad', 'rejected tone');

Object.assign(PBLOG, {period:'7d', outcome:'duplicate', network:'n1', ip:'', cid:'c 1', offset:50});
const qs = new URLSearchParams(pblogQS());
eq(qs.get('direction'), 'in', 'inbound lines only');
eq(qs.get('limit'), String(PBLOG_PAGE+1), 'one row more than a page');
eq(qs.get('offset'), '50', 'offset');
eq([qs.get('outcome'), qs.get('network'), qs.get('cid'), qs.has('ip')], ['duplicate','n1','c 1',false], 'filters');
eq(Math.round((Date.parse(qs.get('to'))-Date.parse(qs.get('from')))/3600e3), 168, 'seven days');
if (failed) process.exit(1);
`)
}
