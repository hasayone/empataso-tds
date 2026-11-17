package admin

import (
	"regexp"
	"strings"
	"testing"
)

// checkerSrc returns the part of the SPA script that holds the checker panel and the app block of a campaign:
// from its section comment up to the AI provider table that follows it.
// t is the test handle. It returns the source and fails the test when a marker is missing.
func checkerSrc(t *testing.T) string {
	t.Helper()
	return dictBetween(t, spaSource(), "/* ---------- Checker: settings panel and the app block of a campaign ---------- */", "/** AI_PROVIDERS lists")
}

// checkerDictKeys returns the keys of the checker block of each language dictionary.
// t is the test handle and html is the SPA source. It returns one key set per block in source order (en, ru, uk)
// and fails the test when the blocks are not exactly three.
func checkerDictKeys(t *testing.T, html string) []map[string]bool {
	t.Helper()
	parts := strings.Split(html, "// checker begin\n")
	if len(parts) != 4 {
		t.Fatalf("the dictionaries hold %d checker blocks, want 3", len(parts)-1)
	}
	keyRe := regexp.MustCompile(`'([A-Za-z0-9_.]+)':'`)
	var out []map[string]bool
	for _, p := range parts[1:] {
		block := p[:strings.Index(p, "// checker end")]
		set := map[string]bool{}
		for _, m := range keyRe.FindAllStringSubmatch(block, -1) {
			if set[m[1]] {
				t.Errorf("key %q is defined twice in one checker block", m[1])
			}
			set[m[1]] = true
		}
		out = append(out, set)
	}
	return out
}

// TestSPACheckerTranslations checks that the checker texts exist once in each of ru, uk and en with the same key set,
// that every literal tr key of the checker code resolves in all three, and that the status and state names are covered.
// t is the test handle. It returns nothing and fails when a key is missing or differs between languages.
func TestSPACheckerTranslations(t *testing.T) {
	html := spaSource()
	sets := checkerDictKeys(t, html)
	for i, name := range []string{"ru", "uk"} {
		for k := range sets[0] {
			if !sets[i+1][k] {
				t.Errorf("%q is in en but not in %s", k, name)
			}
		}
		for k := range sets[i+1] {
			if !sets[0][k] {
				t.Errorf("%q is in %s but not in en", k, name)
			}
		}
	}
	src := checkerSrc(t)
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`\btr(?:Fill)?\('(chk\.[A-Za-z0-9_.]+)'`).FindAllStringSubmatch(src, -1) {
		if !strings.HasSuffix(m[1], ".") { // a key built at run time ('chk.st.'+status) is covered by the status and state lists below
			used[m[1]] = true
		}
	}
	for _, m := range regexp.MustCompile(`'(chk\.(?:app|new|keys|queue)\.[A-Za-z0-9_.]+)'`).FindAllStringSubmatch(src, -1) {
		used[m[1]] = true
	}
	for _, s := range []string{"live", "paused", "pending_ban", "banned", "rejected", "archived", "blocked", "store_watch", "keyword_match", "content"} {
		used["chk.st."+s] = true
		must(t, src, s+":'", "tone of status "+s)
	}
	for _, s := range []string{"draft", "queued", "sending", "linked", "error"} {
		used["chk.state."+s] = true
	}
	used["int.name.checker"], used["int.desc.checker"] = true, true
	if len(used) < 60 {
		t.Fatalf("too few checker keys found: %d", len(used))
	}
	for key := range used {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("tr(%q) missing for lang %q", key, lang)
			}
		}
	}
	for _, text := range []string{"'chk.st.store_watch':'Awaiting release'", "'chk.st.store_watch':'Ожидание релиза'", "'chk.st.store_watch':'Очікування релізу'"} {
		must(t, html, text, "the store_watch status reads as awaiting release")
	}
}

// TestSPACheckerRoutes checks that the SPA calls only the routes of the checker contract and no hook or secret field.
// t is the test handle. It returns nothing and fails when a route is outside the contract.
func TestSPACheckerRoutes(t *testing.T) {
	src := checkerSrc(t)
	allowed := map[string]bool{
		"/api/checker/settings": true, "/api/checker/test": true, "/api/checker/keywords/preview": true, "/api/checker/keywords/fill": true,
		"/api/checker/status": true, "/api/checker/consoles": true, "/api/checker/apps/": true,
	}
	for _, m := range regexp.MustCompile(`'(/api/checker/[a-z/]*)'`).FindAllStringSubmatch(src, -1) {
		if !allowed[m[1]] {
			t.Errorf("route %s is outside the checker contract", m[1])
		}
	}
	for _, bad := range []string{"hook_secret", "has_hook_secret", "/api/checker/hook", "change-status", "target_keys", "campaign_alias"} {
		mustNot(t, src, bad, "the MVP has no "+bad)
	}
	must(t, src, "'/api/checker/apps/'+encodeURIComponent(", "the campaign id is escaped in the path")
	must(t, spaFunctionLine(spaSource(), "flowPage"), "checkerReset();", "the page forgets the previous reads")
	render := spaFunctionLine(spaSource(), "renderFlow")
	must(t, render, `<div id="checker-app"></div>`, "the block has its place on the campaign page")
	must(t, render, "checkerBlock(g);", "the block is painted with the page")
	mustNot(t, render, "#/flows/${", "the checker block adds no campaign link")
}

// checkerPrelude declares what the checker functions read under node: the status table and state of the SPA, stand-ins for the
// dictionary, the markup helpers and the requests. tr returns "T:" and the key, trFill the key and its values as JSON.
// Requests go to answers (keyed "METHOD path", an Error value is thrown) and are listed in calls; toasts are listed in toasts.
func checkerPrelude(t *testing.T, html string) string {
	t.Helper()
	return "const CHK_STATUSES=" + dictBetween(t, html, "const CHK_STATUSES=", "\n") + "\n" + `
const CHK = {status:null, apps:new Map(), consoles:null, drafts:new Map()};
const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const opt = (v, l, cur) => '<option value="' + esc(v) + '"' + (String(cur) === String(v) ? ' selected' : '') + '>' + esc(l) + '</option>';
const tr = k => 'T:' + k;
const trFill = (k, m) => k + JSON.stringify(m || {});
const errText = e => e.message;
const calloutHTML = (kind, text, o) => '<callout ' + kind + '>' + esc(text) + ((o && o.actions) || '') + '</callout>';
const calloutSet = (el, text, kind) => { el.callout = [text, kind]; };
const ago = () => 'ago';
const uiLocale = () => 'en-US';
const calls = [], toasts = [], answers = {};
let edit = true;
const canEdit = () => edit;
const ME = {};
const SNAP = new Map(), DIRTY = new Set();
let GROUPS = [], CUR = null;
const toast = (m, bad) => toasts.push([m, !!bad]);
const answer = (m, p) => { const a = answers[m + ' ' + p]; if (a instanceof Error) throw a; return a === undefined ? null : a; };
const call = async (m, p, b) => { calls.push(b === undefined ? [m, p] : [m, p, b]); return answer(m, p); };
const get = async (p, def) => { calls.push(['GET', p]); const a = answers['GET ' + p]; return a === undefined || a instanceof Error ? def : a; };
const reset = () => { calls.length = 0; toasts.length = 0; for (const k of Object.keys(answers)) delete answers[k]; SNAP.clear(); DIRTY.clear(); CHK.status = null; CHK.apps = new Map(); CHK.consoles = null; CHK.drafts.clear(); edit = true; };
`
}

// checkerHTMLFuncs lists the pure drawing functions of the app block.
var checkerHTMLFuncs = []string{
	"checkerAppName", "checkerNameLine", "checkerPutBody", "checkerDraftEmpty", "checkerDraftFrom", "checkerView", "checkerFact",
	"checkerStatusPill", "checkerFormHTML", "checkerBlockHTML",
}

// TestSPACheckerBlockViews runs the drawing of the app block under node: the view of each record state, the buttons of each view,
// the name preview, the link that opens only over https, the wait on refresh and the escaping of the checker's error text.
// t is the test handle. It returns nothing and fails when a view, a button or an escape regresses.
func TestSPACheckerBlockViews(t *testing.T) {
	html := spaSource()
	assistantNode(t, checkerHTMLFuncs, checkerPrelude(t, html), `
const A = over => Object.assign({name:'11381 - Betsson', keyword:'Betsson', bundle_id:'com.x.y', console_id:'c1', console_name:'Main', state:'linked', status:'store_watch', status_at:'2026-11-16T10:00:00Z', keywords_sent:41, existed:false, last_error:''}, over);
const INT = {enabled:true, create_apps:true};
const ctx = (r, over) => Object.assign({g:{num:11381}, view:checkerView(r), r, st:null, d:checkerDraftFrom(r), consoles:[{id:'c1', name:'Main'}], edit:true}, over);
eq(checkerView(null), 'new', 'no answer is the new campaign');
eq(checkerView({app:null}), 'form', 'no record');
eq(['draft', 'error'].map(s => checkerView({app:A({state:s})})), ['form', 'form'], 'draft and error are editable');
eq(['queued', 'sending'].map(s => checkerView({app:A({state:s})})), ['queued', 'queued'], 'queued and sending wait');
eq(checkerView({app:A({state:'linked'})}), 'linked', 'linked');
eq(checkerView({app:A({state:'draft'}), foreign:true}), 'foreign', 'another tenant wins over the state');

eq(checkerNameLine(11381, 'Betsson'), 'chk.app.name{"name":"11381 - Betsson"}', 'name is the number and the key');
eq([checkerNameLine(11381, '  '), checkerAppName(0, 'Betsson')], ['', 'Betsson'], 'no key no line; no number, the key alone');

let h = checkerBlockHTML(ctx({integration:INT, app:A(), app_url:'https://c.example/apps/1', retry_after:0}));
eq([h.includes('<span class="pill acc">T:chk.st.store_watch</span>'), h.includes('id="cka-refresh"'), h.includes('href="https://c.example/apps/1"')], [true, true, true], 'linked: status pill, refresh and the link');
eq([h.includes('chk.app.keys{&quot;n&quot;:41}'), h.includes('T:chk.app.existed')], [true, false], 'linked: the keys sent');
eq(h.includes(' disabled'), false, 'refresh is ready without a wait');
h = checkerBlockHTML(ctx({integration:INT, app:A({existed:true, keywords_sent:0}), app_url:'http://c.example/apps/1', retry_after:42}));
eq([h.includes('T:chk.app.existed'), h.includes('href='), h.includes('id="cka-refresh" disabled'), h.includes('chk.app.wait{&quot;n&quot;:42}')], [true, false, true, true], 'existing app, no link over http, refresh waits');
h = checkerBlockHTML(ctx({integration:INT, app:A({status:'future_status'}), app_url:''}));
eq(h.includes('<span class="pill">future_status</span>'), true, 'an unknown status is shown as sent');
h = checkerBlockHTML(ctx({integration:INT, app:A()}, {edit:false}));
eq(h.includes('cka-refresh'), false, 'a reader has no refresh button');

h = checkerBlockHTML(ctx({integration:{enabled:true, create_apps:false}, app:A({state:'queued'})}));
eq([h.includes('T:chk.state.queued'), h.includes('T:chk.app.off'), h.includes('id="cka-reload"')], [true, true, true], 'queued while creating is off');
h = checkerBlockHTML(ctx({integration:INT, app:A({state:'sending'})}));
eq([h.includes('T:chk.state.sending'), h.includes('T:chk.app.off')], [true, false], 'sending with creating on');

h = checkerBlockHTML(ctx({integration:INT, app:A({state:'error', last_error:'<b>422</b>'})}));
eq([h.includes('&lt;b&gt;422&lt;/b&gt;'), h.includes('<b>422</b>'), h.includes('T:chk.app.retry'), h.includes('id="cka-save"'), h.includes('id="cka-create"')], [true, false, true, true, true], 'error: escaped text, Retry, Save');
h = checkerBlockHTML(ctx({integration:INT, app:null, prefill:{keyword:'Betsson', bundle_id:'com.x.y'}}));
eq([h.includes('value="Betsson"'), h.includes('value="com.x.y"'), h.includes('<option value="c1">Main</option>'), h.includes('T:chk.app.create')], [true, true, true, true], 'no record: fields from the name, console select, Create');
h = checkerBlockHTML(ctx({integration:INT, app:A({state:'draft', console_id:'gone', console_name:'Old'})}));
eq(h.includes('<option value="gone" selected>Old</option>'), true, 'a console missing from the list keeps its saved name');
h = checkerBlockHTML(ctx({integration:INT, app:A({state:'linked'}), foreign:true}));
eq([h.includes('id="cka-relink"'), h.includes('id="cka-refresh"')], [true, false], 'another checker: Link again only');

h = checkerBlockHTML(ctx(null, {st:{enabled:true, create_apps:true}}));
eq([h.includes('id="cka-new"'), h.includes('role="switch" checked'), h.includes('id="cka-save"'), h.includes('id="cka-create"'), h.includes('placeholder="T:chk.app.fromname"')], [true, false, false, false, true], 'new campaign: the create switch is off, no Save or Create');
const d = checkerDraftFrom(null); d.create = true;
h = checkerBlockHTML(ctx(null, {st:{enabled:true, create_apps:false}, d}));
eq([h.includes('role="switch" checked'), h.includes('T:chk.app.off')], [true, true], 'the switch shows the draft; a disabled creation is explained');
eq(checkerPutBody({keyword:' K ', bundle_id:' a.b ', notion_url:' ', console_id:'', console_name:''}), {keyword:'K', bundle_id:'a.b', notion_url:'', console_id:'', console_name:''}, 'the body is trimmed');
eq([checkerDraftEmpty(checkerDraftFrom(null)), checkerDraftEmpty({keyword:'K'})], [true, false], 'an untouched draft is empty');
`)
}

// TestSPACheckerBlockOnlyWhenEnabled runs the block under node: it draws nothing while the integration is off, for a saved
// campaign (its record says enabled) and for a new one (the status says enabled), reads the console list only for an editable form,
// hides a form from a user who may not edit, stays silent on 403, 404 and 409 and shows other failures.
// t is the test handle. It returns nothing and fails when the block appears without the integration.
func TestSPACheckerBlockOnlyWhenEnabled(t *testing.T) {
	html := spaSource()
	funcs := append(append([]string{}, checkerHTMLFuncs...), "checkerReset", "checkerErrText", "checkerStatus", "checkerApp", "checkerConsoles", "checkerBlock", "checkerBind")
	assistantNode(t, funcs, checkerPrelude(t, html)+`
const host = {innerHTML:'', isConnected:true};
const q = s => s === '#checker-app' ? host : null;
const saved = {id:'flow1', num:11381}, fresh = {id:'flow2', num:11382};
const rec = (enabled, app) => ({integration:{enabled, create_apps:true}, app, prefill:{keyword:'Betsson', bundle_id:'com.x.y'}, foreign:false, app_url:'', retry_after:0});
const asked = path => calls.some(c => c[1] === path);
`, `
reset(); host.innerHTML = 'x'; SNAP.set(saved, {id:'flow1', json:'{}'});
answers['GET /api/checker/apps/flow1'] = rec(false, null);
await checkerBlock(saved);
eq([host.innerHTML, asked('/api/checker/consoles')], ['', false], 'saved campaign, integration off: no block, no console request');

reset(); host.innerHTML = '';
SNAP.set(saved, {id:'flow1', json:'{}'});
answers['GET /api/checker/apps/flow1'] = rec(true, null);
answers['GET /api/checker/consoles'] = [{id:'c1', name:'Main'}];
await checkerBlock(saved);
eq([host.innerHTML.includes('id="cka-save"'), host.innerHTML.includes('value="Betsson"'), asked('/api/checker/consoles')], [true, true, true], 'saved campaign, integration on: the form, consoles read');
eq(calls.filter(c => c[0] !== 'GET').length, 0, 'drawing sends nothing but reads');

reset(); host.innerHTML = 'x'; edit = false;
SNAP.set(saved, {id:'flow1', json:'{}'});
answers['GET /api/checker/apps/flow1'] = rec(true, null);
await checkerBlock(saved);
eq([host.innerHTML, asked('/api/checker/consoles')], ['', false], 'a user who may not edit sees no empty form');

reset(); host.innerHTML = 'x';
answers['GET /api/checker/status'] = {enabled:false, create_apps:false};
await checkerBlock(fresh);
eq([host.innerHTML, asked('/api/checker/apps/flow2'), asked('/api/checker/consoles')], ['', false, false], 'new campaign, integration off: no block, the unsaved id is never asked');

reset(); host.innerHTML = '';
answers['GET /api/checker/status'] = {enabled:true, create_apps:true};
answers['GET /api/checker/consoles'] = [];
await checkerBlock(fresh);
eq([host.innerHTML.includes('id="cka-new"'), host.innerHTML.includes('role="switch" checked'), asked('/api/checker/apps/flow2')], [true, false, false], 'new campaign, integration on: the switch is off by default');
eq(CHK.drafts.get(fresh).isNew, true, 'the draft of an unsaved campaign is marked');

reset(); host.innerHTML = 'x'; SNAP.set(saved, {id:'flow1', json:'{}'});
answers['GET /api/checker/apps/flow1'] = Object.assign(new Error('not found'), {status:404});
await checkerBlock(saved);
eq(host.innerHTML, '', 'a 404 leaves no block');
reset(); host.innerHTML = 'x'; SNAP.set(saved, {id:'flow1', json:'{}'});
answers['GET /api/checker/apps/flow1'] = Object.assign(new Error('boom'), {status:500});
await checkerBlock(saved);
eq(host.innerHTML, '<callout err>boom</callout>', 'a server failure is shown');
`)
}

// TestSPACheckerCreateOnlyAfterSave runs the deferred checker request of a new campaign under node: nothing goes while the
// campaign is not saved or still has unsaved changes; after the save PUT goes first and POST create only with the switch on;
// an empty untouched block sends nothing; a refusal of the checker is a toast and no create follows.
// It also checks that the save path calls the flush right after saveFlows.
// t is the test handle. It returns nothing and fails when a request can go before the campaign is saved.
func TestSPACheckerCreateOnlyAfterSave(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "saveAll"), "if(flowsDirty()&&!block){await saveFlows(); await checkerFlushPending()}", "the flush follows a successful save")
	flush := spaFunctionLine(html, "checkerFlushPending")
	must(t, flush, "!SNAP.has(g)||DIRTY.has(g)", "an unsaved or changed campaign is skipped")
	if n := strings.Count(checkerSrc(t), "'/create'"); n != 3 {
		t.Errorf("create is requested in %d places, want 3: the flush, Create and Link again", n)
	}
	for _, fn := range []string{"checkerBlock", "checkerBlockHTML", "checkerPanel", "renderFlow", "newFlow"} {
		mustNot(t, spaFunctionLine(html, fn), "checkerFlushPending", fn+" does not flush")
	}
	assistantNode(t, []string{"checkerPutBody", "checkerDraftEmpty", "checkerErrText", "checkerFlushPending"}, checkerPrelude(t, html)+`
const blocks = [];
const checkerBlock = g => { blocks.push(g.id); };
const camp = id => ({id, num:7});
const draft = over => Object.assign({isNew:true, keyword:'Betsson', bundle_id:'com.x.y', notion_url:'', console_id:'', console_name:'', create:false}, over);
const put = id => ['PUT', '/api/checker/apps/' + id, {keyword:'Betsson', bundle_id:'com.x.y', notion_url:'', console_id:'', console_name:''}];
`, `
let g = camp('flow1');
reset(); GROUPS = [g]; CUR = g; CHK.drafts.set(g, draft({create:true}));
await checkerFlushPending();
eq(calls, [], 'a campaign that is not saved sends nothing');
eq(CHK.drafts.has(g), true, 'and its draft waits');

SNAP.set(g, {id:'flow1', json:'{}'}); DIRTY.add(g);
await checkerFlushPending();
eq(calls, [], 'a saved campaign with unsaved changes sends nothing');

DIRTY.clear();
await checkerFlushPending();
eq(calls, [put('flow1'), ['POST', '/api/checker/apps/flow1/create']], 'after the save: PUT, then create');
eq([CHK.drafts.has(g), blocks], [false, ['flow1']], 'the draft is spent and the block is drawn again');

reset(); blocks.length = 0; GROUPS = [g]; CUR = g; SNAP.set(g, {id:'flow1', json:'{}'}); CHK.drafts.set(g, draft());
await checkerFlushPending();
eq(calls, [put('flow1')], 'the switch off: fields only, no create');

reset(); GROUPS = [g]; SNAP.set(g, {id:'flow1', json:'{}'}); CHK.drafts.set(g, draft({keyword:'', bundle_id:''}));
await checkerFlushPending();
eq(calls, [], 'an untouched block sends nothing');

reset(); GROUPS = [g]; SNAP.set(g, {id:'flow1', json:'{}'}); CHK.drafts.set(g, draft({keyword:'', bundle_id:'', create:true}));
await checkerFlushPending();
eq(calls.map(c => c[0] + ' ' + c[1]), ['PUT /api/checker/apps/flow1', 'POST /api/checker/apps/flow1/create'], 'an empty block with the switch on still creates: the server reads the name');

reset(); GROUPS = [g]; SNAP.set(g, {id:'flow1', json:'{}'}); CHK.drafts.set(g, draft({create:true}));
answers['PUT /api/checker/apps/flow1'] = Object.assign(new Error('bundle already used by campaign 5'), {status:409});
await checkerFlushPending();
eq([calls.length, toasts], [1, [['chk.new.fail{"err":"bundle already used by campaign 5"}', true]]], 'a refusal is a toast and no create follows');

reset(); const other = camp('flow9'); GROUPS = [g]; SNAP.set(other, {id:'flow9', json:'{}'}); CHK.drafts.set(other, draft({create:true}));
await checkerFlushPending();
eq(calls, [], 'a campaign that left the list sends nothing');

reset(); const old = camp('flow1'); GROUPS = [old]; SNAP.set(old, {id:'flow1', json:'{}'}); CHK.drafts.set(old, draft({isNew:false, create:true}));
await checkerFlushPending();
eq(calls, [], 'a draft of a saved campaign is not a deferred one');

reset(); const renamed = camp('new id'); GROUPS = [renamed]; SNAP.set(renamed, {id:'new id', json:'{}'}); CHK.drafts.set(renamed, draft());
await checkerFlushPending();
eq(calls[0][1], '/api/checker/apps/new%20id', 'the saved id is escaped in the path');
`)
}

// TestSPACheckerSettingsPanel runs the settings panel under node: the token field is empty and the stored token only shows as a
// hint; a save sends the typed token and the locales as a list and refreshes the badge; a check shows the server refusal;
// the preview fills the Fill button, and Fill sends {confirm:true} only after the question is answered yes.
// t is the test handle. It returns nothing and fails when the token reaches the markup or the fill skips its confirmation.
func TestSPACheckerSettingsPanel(t *testing.T) {
	html := spaSource()
	panel := spaFunctionLine(html, "checkerPanel")
	for _, needle := range []string{
		`id="ck-token" class="mono" type="password" value="" placeholder="${esc(cur.has_token?tr('chk.token.saved'):'')}" autocomplete="new-password"`,
		"call('GET','/api/checker/keywords/preview')",
		"call('POST','/api/checker/keywords/fill',{confirm:true})",
		"call('POST','/api/checker/test',body())",
		"call('PUT','/api/checker/settings',body())",
		`placeholder="https://empataso.checkmy.store"`,
	} {
		must(t, panel, needle, "checker settings panel")
	}
	for _, bad := range []string{"cur.token", "value=\"${esc(cur.tok", "token:cur"} {
		mustNot(t, panel, bad, "a stored token is never written into the markup: "+bad)
	}
	if strings.Index(panel, "if(!await ask(") > strings.Index(panel, "'/api/checker/keywords/fill'") {
		t.Error("the fill must ask before it posts")
	}
	assistantNode(t, []string{"checkerReset", "checkerErrText", "checkerLocaleLines", "checkerPreviewHTML", "checkerPanel"}, checkerPrelude(t, html)+`
const els = {};
const q = s => els[s] || (els[s] = {});
const host = {innerHTML:'', events:[], dispatchEvent(e) { this.events.push(e.detail); }};
function CustomEvent(type, o) { this.type = type; this.detail = o.detail; }
let asking = null, askAnswer = false;
const ask = async (title, text, ok) => { asking = [title, text, ok]; return askAnswer; };
const settings = {base_url:'https://x.checkmy.store', tenant_id:'empataso', has_token:true, token:'SECRET-TOKEN', enabled:true, create_apps:false,
  admin_url:'', app_url:'', locales:['af', 'sq'], default_locales:[{code:'af', label:'Afrikaans'}, {code:'vi', label:'Vietnamese'}],
  paused:true, error:'', queue:{queued:2, errors:1, created_hour:5},
  postback_url:'https://x.checkmy.store/track?token={checker_token}&status={checker_status}', postback_default:'https://x.checkmy.store/default?status={checker_status}',
  postback_method:'POST', has_postback_token:true, postback_token:'SECRET-PB-TOKEN'};
const live = () => true;
const click = async id => { await els[id].onclick({currentTarget:{}}); };
`, `
reset(); answers['GET /api/checker/settings'] = settings;
await checkerPanel(host, live);
const h = host.innerHTML;
eq([h.includes('SECRET-TOKEN'), h.includes('id="ck-token" class="mono" type="password" value=""'), h.includes('placeholder="T:chk.token.saved"')], [false, true, true], 'the token is not in the markup, the field is empty, the hint says it is saved');
eq([h.includes('<callout warn>T:chk.paused</callout>'), h.includes('T:chk.queue.queued</span>') || h.includes('T:chk.queue.queued: 2'), h.includes('af\nsq</textarea>')], [true, true, true], 'paused note, queue counters and the locale lines');
eq(h.includes('chk.locales.default{&quot;list&quot;:&quot;af, vi&quot;}'), true, 'the default list is shown as a hint');
eq(h.includes('id="ck-enabled" role="switch" checked'), true, 'the switch shows the saved state');
eq([h.includes('SECRET-PB-TOKEN'), h.includes('id="ck-pbtoken" class="mono" type="password" value="" placeholder="T:chk.token.saved"')], [false, true], 'the tracking token is not in the markup, the field is empty, the hint says it is saved');
eq([h.includes('status={checker_status}</textarea>'), h.includes('placeholder="https://x.checkmy.store/default?status={checker_status}"'), h.includes('<option value="POST" selected>POST</option>')], [true, true, true], 'the postback address, its default and the method are shown');

answers['GET /api/checker/settings'] = Object.assign({}, settings, {has_token:false, paused:false});
await checkerPanel(host, live);
eq([host.innerHTML.includes('placeholder=""'), host.innerHTML.includes('chk.paused')], [true, false], 'no token saved: empty hint');

reset(); answers['GET /api/checker/settings'] = settings; answers['PUT /api/checker/settings'] = {};
await checkerPanel(host, live);
els['#ck-enabled'] = {checked:false}; els['#ck-url'] = {value:' https://y.checkmy.store '}; els['#ck-tenant'] = {value:'t1'}; els['#ck-token'] = {value:' test-token '};
els['#ck-create'] = {checked:true}; els['#ck-admin'] = {value:'https://adm.example'}; els['#ck-appurl'] = {value:'https://y.checkmy.store/apps/{id}'}; els['#ck-locales'] = {value:' af \n\n ru-RU \n'};
els['#ck-pburl'] = {value:' https://y.checkmy.store/track?status={checker_status} '}; els['#ck-pbmethod'] = {value:'POST'}; els['#ck-pbtoken'] = {value:' test-token '};
await click('#ck-save');
const put = calls.find(c => c[0] === 'PUT');
eq(put, ['PUT', '/api/checker/settings', {enabled:false, base_url:'https://y.checkmy.store', tenant_id:'t1', token:'test-token', create_apps:true, admin_url:'https://adm.example', app_url:'https://y.checkmy.store/apps/{id}', locales:['af', 'ru-RU'],
  postback_url:'https://y.checkmy.store/track?status={checker_status}', postback_method:'POST', postback_token:'test-token'}], 'save sends the typed values, the locale list and the postback fields');
eq(host.events, ['checker'], 'the integrations page is told to read the badge');

reset(); answers['GET /api/checker/settings'] = settings;
await checkerPanel(host, live);
answers['POST /api/checker/test'] = Object.assign(new Error('integration disabled'), {status:409});
await click('#ck-test');
eq(els['#ck-status'].callout, ['T:chk.err.disabled', 'err'], 'a refused check shows the translated reason');
answers['POST /api/checker/test'] = {ok:true, user:{name:'Alex', email:'a@example.com'}};
await click('#ck-test');
eq(els['#ck-status'].callout, ['chk.test.ok{"who":"Alex, a@example.com"}', 'ok'], 'a good check names the user');

reset(); answers['GET /api/checker/settings'] = settings;
answers['GET /api/checker/keywords/preview'] = {campaigns:10, fill:3, keep:5, no_key:2, keys:[{keyword:'<Bet>', campaigns:3}], sample:[{group_id:'f1', num:1, name:'1 - <Bet> - x', keyword:'<Bet>'}]};
await checkerPanel(host, live);
eq(host.innerHTML.includes('id="ck-fill" disabled'), true, 'Fill waits for the preview');
await click('#ck-prev');
const out = els['#ck-keys-out'].innerHTML;
eq([out.includes('chk.keys.summary{&quot;fill&quot;:3,&quot;keep&quot;:5,&quot;no_key&quot;:2}'), out.includes('&lt;Bet&gt;'), out.includes('<Bet>'), els['#ck-fill'].disabled], [true, true, false, false], 'preview: summary, escaped keys, Fill ready');
eq(calls.filter(c => c[0] !== 'GET').length, 0, 'the preview sends no write');

askAnswer = false; asking = null;
await click('#ck-fill');
eq([asking && asking[1], calls.filter(c => c[1] === '/api/checker/keywords/fill').length], ['chk.keys.fill.ask{"n":3}', 0], 'no fill without the yes');
askAnswer = true; answers['POST /api/checker/keywords/fill'] = {filled:3, kept:5, no_key:2};
await click('#ck-fill');
eq(calls.filter(c => c[1] === '/api/checker/keywords/fill'), [['POST', '/api/checker/keywords/fill', {confirm:true}]], 'fill sends confirm:true after the yes');
eq(toasts, [['chk.keys.filled{"filled":3,"kept":5,"no_key":2}', false]], 'the result is a toast');
`)
}

// TestSPACheckerBindActions runs the buttons of the app block under node: Save writes the fields, Create writes them and then queues
// the app, Link again only queues, Refresh posts to the record; each one ends with a fresh read; a refused one is a toast and the
// button is ready again.
// t is the test handle. It returns nothing and fails when a button calls the wrong route or order.
func TestSPACheckerBindActions(t *testing.T) {
	html := spaSource()
	funcs := []string{"checkerReset", "checkerErrText", "checkerAppName", "checkerNameLine", "checkerPutBody", "checkerBind"}
	assistantNode(t, funcs, checkerPrelude(t, html)+`
const els = {};
const q = s => els[s] || (els[s] = {value:''});
const host = {};
const checkerBlock = async g => { calls.push(['REPAINT', g.id]); };
const g = {id:'flow1', num:11381};
const click = async id => { await els['#' + id].onclick(); };
const bind = d => { reset(); for (const k of Object.keys(els)) delete els[k]; SNAP.set(g, {id:'flow1', json:'{}'}); CHK.apps.set('flow1', Promise.resolve({})); checkerBind(host, {g, d}); };
`, `
const d0 = () => ({keyword:'Betsson', bundle_id:'com.x.y', notion_url:'', console_id:'', console_name:'', create:false});
const body = {keyword:'Betsson', bundle_id:'com.x.y', notion_url:'', console_id:'', console_name:''};
const routes = () => calls.map(c => c[0] + ' ' + c[1]);

bind(d0());
els['#cka-kw'] = {value:' Pin Up '}; els['#cka-bundle'] = {value:'com.pin.up'}; els['#cka-notion'] = {value:''}; els['#cka-name'] = {};
els['#cka-console'] = {value:'c9', selectedOptions:[{textContent:'Nine'}]};
host.oninput();
eq(els['#cka-name'].textContent, 'chk.app.name{"name":"11381 - Pin Up"}', 'typing updates the name preview');
await click('cka-save');
eq(calls[0], ['PUT', '/api/checker/apps/flow1', {keyword:'Pin Up', bundle_id:'com.pin.up', notion_url:'', console_id:'c9', console_name:'Nine'}], 'Save sends the typed fields with the console name');
eq(routes(), ['PUT /api/checker/apps/flow1', 'REPAINT flow1'], 'Save ends with a repaint');
eq(CHK.apps.has('flow1'), false, 'the stale record is dropped');

bind(d0());
await click('cka-create');
eq(routes(), ['PUT /api/checker/apps/flow1', 'POST /api/checker/apps/flow1/create', 'REPAINT flow1'], 'Create: fields first, then the queue');
eq(toasts, [['T:chk.app.queued.ok', false]], 'Create says it is queued');

bind(d0());
answers['PUT /api/checker/apps/flow1'] = Object.assign(new Error('bundle already used by campaign 5'), {status:409});
await click('cka-create');
eq(routes(), ['PUT /api/checker/apps/flow1'], 'a refused Save stops Create');
eq([toasts, els['#cka-create'].disabled], [[['bundle already used by campaign 5', true]], false], 'a refusal is a toast and the button is ready again');

bind(d0());
await click('cka-relink');
eq(routes(), ['POST /api/checker/apps/flow1/create', 'REPAINT flow1'], 'Link again only queues');

bind(d0());
await click('cka-refresh');
eq(routes(), ['POST /api/checker/apps/flow1/refresh', 'REPAINT flow1'], 'Refresh posts to the record');
bind(d0());
answers['POST /api/checker/apps/flow1/refresh'] = Object.assign(new Error('too many requests'), {status:429, body:{retry_after:30}});
await click('cka-refresh');
eq(toasts, [['chk.app.wait{"n":30}', true]], 'a refused refresh names the wait');
`)
}

// TestSPACheckerPostbackRow runs the "Checker" row of the S2S block of a campaign under node: the row exists only while the status
// offers the postback, shows the masked address and the method as read-only text, switches lead, sale and rejected follow
// postback_ids, and the saved list keeps every checker id when the row is not on screen. It also reads the code of the drawer:
// the row sits above the campaign's own rows, outside #s2s-rows, and the catalog neither shows nor sends the managed ids.
// t is the test handle. It returns nothing and fails when the row appears without the postback or an id gets lost.
func TestSPACheckerPostbackRow(t *testing.T) {
	html := spaSource()
	s2s := spaFunctionLine(html, "s2sHTML")
	host := strings.Index(s2s, `id="s2s-checker-host"`)
	if host < 0 || host > strings.Index(s2s, `id="s2s-rows"`) || host < strings.Index(s2s, "s2sGlobalHTML(") {
		t.Error("the checker row must sit between the shared templates and the campaign's own rows")
	}
	must(t, s2s, "checkerPostbackHTML(g||{},(CHK.status||{}).postback)", "the row is drawn from the status of the integration")
	must(t, spaFunctionLine(html, "s2sGlobalHTML"), ".filter(p=>!checkerPbId(p.id))", "the catalog list hides the managed templates")
	must(t, spaFunctionLine(html, "openS2SEditor"), ".filter(p=>!checkerPbId(p.id))", "the catalog editor neither shows nor sends the managed templates")
	must(t, spaFunctionLine(html, "collectFlow"), "checkerPbIds(shared,[...qa('[data-chk-pb]',d)],g.postback_ids)", "saving the campaign merges the switches into postback_ids")
	must(t, spaFunctionLine(html, "collectFlow"), "qa('#s2s-rows .s2s',d)", "own rows are read from #s2s-rows only")
	must(t, html, "checkerStatus().then(()=>{if(q('#gdrawer')&&CUR===g) paintS2SChecker()});", "the row is painted once the status is read")
	for _, bad := range []string{"checker_token", "has_postback_token", "postback_token"} {
		mustNot(t, spaFunctionLine(html, "checkerPostbackHTML"), bad, "the row has no token: "+bad)
	}
	assistantNode(t, []string{"checkerPbId", "checkerPostbackHTML", "checkerPbIds", "paintS2SChecker"}, checkerPrelude(t, html)+"\nconst CHK_PB="+dictBetween(t, html, "const CHK_PB=", "\n")+`
const HOST = {innerHTML:'old'};
const q = s => s === '#gdrawer' ? {} : s === '#s2s-checker-host' ? HOST : null;
const sw = (id, checked) => ({checked, dataset:{chkPb:id}});
const L = sw('checker-lead', true), S = sw('checker-sale', false), R = sw('checker-rejected', true);
`, `
eq([checkerPbId('checker-lead'), checkerPbId('checker-sale'), checkerPbId('checker-rejected'), checkerPbId('pb-59f7f66e85'), checkerPbId('checker-x')], [true, true, true, false, false], 'only the three managed ids');

const pb = {available:true, method:'POST', url_masked:'https://x.checkmy.store/track?token=••••&status={checker_status}'};
eq([checkerPostbackHTML({}, null), checkerPostbackHTML({}, undefined), checkerPostbackHTML({}, {available:false, method:'GET', url_masked:'x'})], ['', '', ''], 'no row while the postback is not available');
let h = checkerPostbackHTML({postback_ids:['pb-1', 'checker-sale']}, pb);
eq([h.includes('id="s2s-checker"'), h.includes('T:chk.pb.title'), h.includes('value="https://x.checkmy.store/track?token=••••&amp;status={checker_status}" readonly'), h.includes('<span class="pill">POST</span>')], [true, true, true, true], 'the row shows the masked address and the method, read-only');
eq([h.includes('data-chk-pb="checker-lead">'), h.includes('data-chk-pb="checker-sale" checked>'), h.includes('data-chk-pb="checker-rejected">')], [true, true, true], 'the switches follow postback_ids');
eq([h.includes('data-chk-pb="pb-1"'), h.includes('<select'), h.includes('<textarea')], [false, false, false], 'a catalog id gets no switch and the address is not editable');
h = checkerPostbackHTML({}, {available:true, method:'', url_masked:''});
eq([h.includes('<span class="pill">GET</span>'), h.includes('checked')], [true, false], 'GET by default, nothing on');

eq(checkerPbIds(['pb-1'], [L, S, R], ['checker-sale', 'pb-1']), ['pb-1', 'checker-lead', 'checker-rejected'], 'the switches replace the managed ids, the catalog ids stay');
eq(checkerPbIds(['pb-2'], [], ['pb-1', 'checker-lead']), ['checker-lead', 'pb-2'], 'no row on screen: the managed ids stay as they are');
eq(checkerPbIds(null, [sw('checker-sale', true)], ['pb-1', 'checker-lead']), ['pb-1', 'checker-sale'], 'the catalog is not on screen: its ids stay');
eq(checkerPbIds(null, [], ['checker-rejected', 'pb-1']), ['checker-rejected', 'pb-1'], 'nothing on screen: the list stays in its order');
eq(checkerPbIds([], [sw('checker-lead', false)], ['checker-lead']), [], 'every switch off empties the list');
eq(checkerPbIds([], [], undefined), [], 'a campaign without ids');

CHK.status = {enabled:true, create_apps:true, postback:pb}; CUR = {postback_ids:['checker-lead']};
paintS2SChecker();
eq([HOST.innerHTML.includes('data-chk-pb="checker-lead" checked>'), HOST.innerHTML.includes('id="s2s-checker"')], [true, true], 'the row is painted into its host');
CHK.status = {enabled:true, create_apps:true, postback:{available:false}};
paintS2SChecker();
eq(HOST.innerHTML, '', 'the row goes away when the postback is not available');
`)
}

// TestSPACheckerSettingsGroups checks the layout of the settings card: the header and the side list carry the product name
// CheckMy.Store in every language, the other integrations keep their names, and the fields sit in four groups in the order
// connection, apps, postback, counters with buttons, followed by the campaign keys.
// t is the test handle. It returns nothing and fails when a name is translated or a field leaves its group.
func TestSPACheckerSettingsGroups(t *testing.T) {
	html := spaSource()
	for _, lang := range []string{"en", "ru", "uk"} {
		for _, key := range []string{"chk.grp.link", "chk.grp.apps"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("%q missing for lang %q", key, lang)
			}
		}
	}
	if n := strings.Count(html, "'int.name.checker':'CheckMy.Store',"); n != 3 {
		t.Errorf("the product name CheckMy.Store appears %d times in the dictionaries, want 3", n)
	}
	mustNot(t, html, "'int.name.checker':'Чекер'", "the header is the product name, not the word Checker")
	mustNot(t, html, "'int.name.checker':'Checker'", "the header is the product name, not the word Checker")
	for _, keep := range []string{"{id:'keitaro', title:'Keitaro'", "{id:'namecheap', title:'Namecheap'", "{id:'cloudflare', title:'Cloudflare'", "{id:'ai', title:'AI'"} {
		must(t, html, keep, "other integrations keep their names")
	}

	panel := dictBetween(t, html, "async function checkerPanel(", "/** AI_PROVIDERS lists")
	marks := []string{
		`data-grp="link"`, `sw('ck-enabled'`, `field('ck-url'`, `field('ck-tenant'`, `id="ck-token"`,
		`data-grp="apps"`, `sw('ck-create'`, `field('ck-admin'`, `field('ck-appurl'`, `id="ck-locales"`,
		`data-grp="pb"`, `id="ck-pburl"`, `id="ck-pbmethod"`, `id="ck-pbtoken"`,
		`data-grp="foot"`, `id="ck-queue"`, `id="ck-save"`, `id="ck-test"`,
		`data-grp="keys"`, `id="ck-prev"`,
	}
	last := -1
	for _, m := range marks {
		at := strings.Index(panel, m)
		if at < 0 {
			t.Fatalf("the settings card lacks %s", m)
		}
		if at < last {
			t.Errorf("%s stands before the previous item: the group order is broken", m)
		}
		last = at
	}
	for _, grp := range []string{"link", "apps", "pb", "keys"} {
		must(t, panel, `data-grp="`+grp+`" role="group" aria-labelledby="ck-gt-`+grp+`"`, "the group "+grp+" is named for screen readers")
	}
	must(t, panel, `<label for="ck-pburl">${esc(tr('chk.pb.url'))}</label>`, "the hint of the address is outside its label")
	mustNot(t, panel, `<b>${esc(tr('chk.pb.section'))}</b>`, "the postback title is a group title")
	must(t, html, ".intsec .igrp{", "groups have their own spacing")
	must(t, html, ".intsec .igrp textarea{overflow-wrap:anywhere}", "a long postback address wraps inside its field")
}
