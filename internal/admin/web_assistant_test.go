package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// assistantWidgetSrc returns the part of the SPA script that holds the AI assistant widget:
// from its section comment up to the Namecheap contact fields that follow it.
func assistantWidgetSrc(t *testing.T) string {
	t.Helper()
	html := spaSource()
	i := strings.Index(html, "/* ---------- AI assistant: round button and chat panel ---------- */")
	j := strings.Index(html, "/** NC_CONTACT_FIELDS lists")
	if i < 0 || j < i {
		t.Fatal("the AI assistant widget block is not in the SPA")
	}
	return html[i:j]
}

// assistantFn returns the source of one top-level SPA function, async or not, up to the next declaration or doc comment.
// html is the SPA source. name is the function name. It returns an empty string when the function is missing.
func assistantFn(html, name string) string {
	start := strings.Index(html, "\nasync function "+name+"(")
	if start < 0 {
		start = strings.Index(html, "\nfunction "+name+"(")
	}
	if start < 0 {
		return ""
	}
	rest := html[start+1:]
	end := len(rest)
	for _, m := range []string{"\nfunction ", "\nasync function ", "\nconst ", "\n/**"} {
		if i := strings.Index(rest[1:], m); i >= 0 && i+1 < end {
			end = i + 1
		}
	}
	return rest[:end]
}

// assistantNode runs a script under node made of the named SPA functions, a prelude and checks.
// funcs are top-level function names taken from the SPA source. prelude declares the globals they read.
// checks is the test body and must call fail(what) on a mismatch. It skips when node is missing and fails the test on any FAIL line.
func assistantNode(t *testing.T, funcs []string, prelude, checks string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: skipping the behaviour checks")
	}
	html := spaSource()
	var src strings.Builder
	src.WriteString(prelude)
	src.WriteByte('\n')
	for _, name := range funcs {
		fn := assistantFn(html, name)
		if fn == "" {
			t.Fatalf("%s not found in the SPA", name)
		}
		src.WriteString(fn)
		src.WriteByte('\n')
	}
	src.WriteString(`
let failed = 0;
const fail = (what, got, want) => { failed++; console.log('FAIL ' + what + (got === undefined ? '' : '\n  got  ' + JSON.stringify(got) + '\n  want ' + JSON.stringify(want))); };
const eq = (got, want, what) => { if (JSON.stringify(got) !== JSON.stringify(want)) fail(what, got, want); };
(async () => {
` + checks + `
if (failed) process.exit(1);
})().catch(e => { console.log('FAIL exception ' + e.stack); process.exit(1); });
`)
	f := filepath.Join(t.TempDir(), "assistant.js")
	if err := os.WriteFile(f, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("widget checks failed:\n%s", out)
	}
}

// TestAssistantWidgetButtonOnlyWhenEnabledAndAllowed checks that the round button exists only for a status
// with enabled and allowed both true, is mounted by app() and render(), and is removed by every sign-in screen.
func TestAssistantWidgetButtonOnlyWhenEnabledAndAllowed(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "aiVisible"), "st.enabled===true&&st.allowed===true", "the button needs enabled and allowed")
	sync := spaFunctionLine(html, "aiSync")
	must(t, sync, "!aiVisible(AI.status)", "the sync removes the button when the status does not allow it")
	must(t, sync, "aiMountFab()", "the sync mounts the button")
	must(t, spaFunctionLine(html, "aiStatusLoad"), "get('/api/ai/status',null)", "the status is read from the status route")
	must(t, spaFunctionLine(html, "app"), "aiStart()", "app mounts the widget")
	must(t, spaFunctionLine(html, "render"), "aiSync()", "render keeps the button in line with the status and the language")
	for _, fn := range []string{"setupView", "loginView", "recoverView"} {
		must(t, spaFunctionLine(html, fn), "aiStop()", fn+" removes the widget")
	}
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	if n := strings.Count(panel, "aiStatusLoad();"); n < 2 {
		t.Errorf("the integration page rereads the status after a save and after a key delete: %d calls, want 2", n)
	}
	must(t, spaFunctionLine(html, "aiMountFab"), `id="aifab"`, "button id")
	must(t, spaFunctionLine(html, "aiMountFab"), "ai.chat.beta", "the button carries the beta mark")
	must(t, spaFunctionLine(html, "aiShellHTML"), "ai.chat.beta", "the panel carries the beta mark")

	assistantNode(t, []string{"aiVisible"}, "", `
eq(aiVisible({enabled:true,allowed:true}), true, 'enabled and allowed');
eq(aiVisible({enabled:true,allowed:false}), false, 'not allowed');
eq(aiVisible({enabled:false,allowed:true}), false, 'switched off');
eq(aiVisible({enabled:'true',allowed:1}), false, 'only real booleans');
eq(aiVisible(null), false, 'no status');
eq(aiVisible({}), false, 'empty status');
`)
}

// TestAssistantWidgetLayout checks the CSS: the button rides above the Keitaro window through --dock-lift,
// the toast stack rises over the button, the save bar is cleared on narrow screens, the label and the beta
// mark share the centre of the button, and the panel is a bottom sheet on a phone.
func TestAssistantWidgetLayout(t *testing.T) {
	html := spaSource()
	must(t, html, "button.aifab{position:fixed;right:20px;bottom:calc(var(--edge-b,20px) + var(--dock-lift,0px) + var(--ai-sb,0px))", "the button stands above the Keitaro window and the save bar")
	must(t, html, ".toasts{position:fixed;right:20px;bottom:calc(var(--edge-b,20px) + var(--dock-lift,0px) + var(--ai-lift,0px))", "the toast stack rises over the button")
	must(t, html, "body.has-aifab{--ai-lift:calc(56px + var(--ai-sb,0px))}", "the lift is on while the button is mounted")
	must(t, html, "@media (max-width:1280px){body:has(#savebar){--ai-sb:60px}}", "the button clears the centred save bar")
	must(t, html, "body.has-aifab.ai-open{--ai-lift:0px}", "no lift while the panel stands in the button's place")
	must(t, html, "z-index:55;height:44px;display:inline-flex;align-items:center;justify-content:center;line-height:1;padding:0 14px;", "the label and the beta mark share the centre of the button")
	must(t, html, ".aifab .aib{display:inline-flex;align-items:center;justify-content:center;height:18px;margin:0;padding:0 7px;line-height:1;", "the beta mark is centred on the label")
	must(t, html, "bottom:calc(var(--edge-b,20px) + var(--dock-lift,0px));z-index:72", "the panel stands above the Keitaro window")
	must(t, html, "@media (max-width:640px){\n    button.aifab{right:12px}\n    .aipanel{left:0;right:0;bottom:0;width:100%", "a bottom sheet on a phone")
	must(t, html, "white-space:pre-wrap;overflow-wrap:anywhere;border-bottom-left-radius:4px", "reply text keeps its line breaks")
	must(t, html, "body.ai-open button.aifab{display:none}", "the panel takes the button's place")
	for _, tok := range []string{"var(--surface)", "var(--accent)", "var(--border)", "var(--bad-soft)", "var(--ok-soft)"} {
		must(t, html[strings.Index(html, ".aipanel{position:fixed"):strings.Index(html, ".ai-in .row select")], tok, "the panel reads the theme tokens")
	}
	css := html[strings.Index(html, "/* ---- AI assistant: round button"):strings.Index(html, ".ai-in .row select")]
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindString(css); m != "" {
		t.Errorf("the widget CSS holds a colour literal %s, it must read the theme tokens", m)
	}
}

// TestAssistantWidgetPageContext runs aiPageContext under node and checks the route, section, board, period, conditions, log tab and campaign.
func TestAssistantWidgetPageContext(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "aiStream")+spaFunctionLine(html, "aiSend"), "context:aiPageContext()", "every message carries the context")
	prelude := `
let CUR = {page:'logs', arg:''};
function route(){ return CUR; }
function boardEffectiveId(){ return 'b1'; }
let RANGE = {id:'dates'};
function rangeBounds(){ return {from:new Date('2026-01-01T00:00:00Z'), to:new Date('2026-01-02T00:00:00Z')}; }
let DASH = {conds:[{f:'device',op:'eq',v:['mobile']}]};
let LOGF = {conds:[{f:'country',op:'eq',v:['ru','us']},{f:'ip',op:'ne',v:'1.2.3.4'},null]};
let PB = {conds:[{f:'status',op:'eq',v:['lead']}], campaignId:'c9'};
let REP = {conds:[{f:'os',op:'eq',v:['ios']}]};
let LOG_TAB = 'postbacks', LOG_CAMPAIGN = 'g1';
const AI_CTX_CONDS = 20;
`
	assistantNode(t, []string{"aiCondText", "aiPageContext"}, prelude, `
let c = aiPageContext();
eq(c.page, 'logs', 'route page');
eq(c.board, 'b1', 'board');
eq(c.range, {id:'dates', from:'2026-01-01T00:00:00.000Z', to:'2026-01-02T00:00:00.000Z'}, 'period with resolved dates');
eq(c.filters, ['country eq ru|us', 'ip ne 1.2.3.4'], 'conditions of the log, a bad one dropped');
eq(c.tab, 'postbacks', 'log tab');
eq(c.campaign, 'g1', 'log campaign');
CUR = {page:'flows', arg:'my-slug'};
c = aiPageContext();
eq([c.page, c.arg, c.tab, c.campaign, c.filters], ['flows', 'my-slug', undefined, undefined, []], 'campaign address is the arg');
CUR = {page:'conversions', arg:''};
c = aiPageContext();
eq([c.filters, c.campaign], [['status eq lead'], 'c9'], 'conversions conditions and campaign');
CUR = {page:'dashboard', arg:''};
eq(aiPageContext().filters, ['device eq mobile'], 'dashboard conditions');
CUR = {page:'report', arg:''};
eq(aiPageContext().filters, ['os eq ios'], 'report conditions');
LOGF.conds = Array.from({length:50}, (_, i) => ({f:'a', op:'eq', v:[String(i)]}));
CUR = {page:'logs', arg:''};
eq(aiPageContext().filters.length, 20, 'conditions are capped');
rangeBounds = () => { throw new Error('no period'); };
eq(aiPageContext().range, {id:'dates'}, 'a failed period read keeps the id');
`)
}

// TestAssistantWidgetPlainText checks that the reply of the model, the card fields and the chat titles are written as plain text:
// the drawing functions never use innerHTML, the widget builds no link or image, and the reply keeps its breaks through CSS.
func TestAssistantWidgetPlainText(t *testing.T) {
	html := spaSource()
	widget := assistantWidgetSrc(t)
	for _, fn := range []string{"aiEl", "aiAddMessage", "aiPlaceCard", "aiFoot", "aiPaintLog", "aiDiffEl", "aiCardEl", "aiPaintUse", "aiPaintModels", "aiOnEvent", "aiGroup", "aiModelPicker"} {
		body := spaFunctionLine(html, fn)
		if body == "" {
			t.Fatalf("%s not found", fn)
		}
		for _, bad := range []string{"innerHTML", "insertAdjacentHTML", "outerHTML", "document.write"} {
			mustNot(t, body, bad, fn+" writes no markup")
		}
	}
	must(t, spaFunctionLine(html, "aiEl"), "e.textContent=text", "text is set through textContent")
	must(t, spaFunctionLine(html, "aiAddMessage"), "b.textContent=m.text", "the reply is set through textContent")
	must(t, spaFunctionLine(html, "aiCardEl"), "aiEl('div','aicard-t',c.title||c.entity_id||'')", "the card title is plain text")
	must(t, spaFunctionLine(html, "aiDiffEl"), "aiEl('span',null,aiVal(val))", "diff values are plain text")
	// The only markup built by script is the fixed shell, icons and the toolbar buttons.
	for _, fn := range []string{"aiStep", "aiPaintHist"} {
		body := spaFunctionLine(html, fn)
		for _, m := range regexp.MustCompile(`innerHTML=([^;]*);`).FindAllStringSubmatch(body, -1) {
			if !strings.HasPrefix(m[1], "icon('") {
				t.Errorf("%s writes markup other than an icon: %s", fn, m[1])
			}
		}
	}
	for _, bad := range []string{"<a ", "<img", "href=", "marked(", "linkify", "DOMParser"} {
		mustNot(t, widget, bad, "the widget builds no link, image or parsed markup")
	}
	must(t, html, ".ai-b{max-width:92%;", "reply bubble")
	must(t, html, "white-space:pre-wrap;overflow-wrap:anywhere", "the reply keeps line breaks and wraps long words")
}

// TestAssistantWidgetStreamRequest checks the streaming client: the POST route, Accept: text/event-stream, the CSRF
// header, the board header from boardApplyHeaders, the csrf retry, an AbortController behind the stop button and a ReadableStream reader.
func TestAssistantWidgetStreamRequest(t *testing.T) {
	html := spaSource()
	stream := spaFunctionLine(html, "aiStream")
	for _, needle := range []string{
		"'/api/ai/chats/'+encodeURIComponent(chatId)+'/messages'",
		"'Accept':'text/event-stream'",
		"'X-CSRF-Token':CSRF",
		"boardApplyHeaders(headers,path)",
		"method:'POST',credentials:'same-origin'",
		"signal})",
		"j.error==='csrf'&&!retried",
		"return aiStream(chatId,payload,signal,true)",
		"r.body.getReader()",
		"new TextDecoder('utf-8')",
		"aiSSEParse(buf)",
		"if(r.status===401){loginView()",
	} {
		must(t, stream, needle, "stream request")
	}
	send := spaFunctionLine(html, "aiSend")
	for _, needle := range []string{
		"new AbortController()", "AI.abort.signal", "body.model=AI.model", "context:aiPageContext()",
		"call('POST','/api/ai/chats',{title:text.slice(0,60)})", "e.name==='AbortError'", "aiReloadChat()", "aiStatusLoad()",
	} {
		must(t, send, needle, "send")
	}
	must(t, spaFunctionLine(html, "aiMountPanel"), "q('#ai-stop',p).onclick=()=>{if(AI.abort) AI.abort.abort()}", "the stop button cancels the request")
	must(t, html, "const AI_EVENTS=['status','tool','text','proposal','usage','done','error'];", "known events")
	for _, ev := range []string{"'status'", "'tool'", "'text'", "'proposal'", "'usage'", "'done'", "'error'"} {
		must(t, spaFunctionLine(html, "aiOnEvent"), "ev.type==="+ev, "handler for "+ev)
	}
	mustNot(t, spaFunctionLine(html, "aiOnEvent"), "text_delta", "the reserved event is not handled, only skipped")
}

// TestAssistantWidgetUnknownEvents runs the event parser and the event handler under node:
// comments and unknown events (text_delta is reserved after v1) are ignored, a split chunk is kept, known events act.
func TestAssistantWidgetUnknownEvents(t *testing.T) {
	prelude := `
const AI_EVENTS = ['status','tool','text','proposal','usage','done','error'];
const AI = {chat:{messages:[],changes:[]}, ended:false, err:'', tick:0};
const log = [];
const trFill = (k, m) => k + JSON.stringify(m);
const fmt = n => String(n);
const aiUSD = n => '$' + n;
const aiErrText = (c, m) => c || m || '';
const aiStep = s => log.push(['step', s]);
const aiAddMessage = m => log.push(['msg', m.id, m.text]);
const aiUpsertChange = v => log.push(['change', v.id]);
const aiPlaceCard = v => log.push(['card', v.id]);
const aiStatusLoad = () => log.push(['status-load']);
`
	assistantNode(t, []string{"aiSSEParse", "aiOnEvent"}, prelude, `
let p = aiSSEParse(': open\n\nevent: status\ndata: {"text":"Reading offers"}\n\n: ping\n\nevent: text_delta\ndata: {"text":"par"}\n\nevent: tool\ndata: {"name":"list_offers","summary":"3 rows"}\n\nevent: do');
eq(p.events.map(e => e.type), ['status', 'text_delta', 'tool'], 'comments dropped, the unfinished event kept');
eq(p.rest, 'event: do', 'the tail waits for the next chunk');
p = aiSSEParse(p.rest + 'ne\ndata: {"message_id":"m1"}\n\n');
eq(p.events, [{type:'done', data:{message_id:'m1'}}], 'a chunk split inside the event name');
p = aiSSEParse('event: text\r\ndata: {"message_id":"m2",\r\ndata: "text":"a\\nb"}\r\n\r\n');
eq(p.events[0].data.text, 'a\nb', 'CRLF and a multi-line data field');
eq(aiSSEParse('event: status\ndata: not json\n\n').events, [], 'bad JSON dropped');
eq(aiSSEParse('data: {"x":1}\n\n').events[0].type, 'message', 'no event name');

aiOnEvent({type:'text_delta', data:{text:'x'}});
aiOnEvent({type:'ping', data:{}});
aiOnEvent({type:'message', data:{}});
aiOnEvent(null);
eq(log, [], 'unknown events do nothing');
aiOnEvent({type:'status', data:{text:'Working'}});
aiOnEvent({type:'tool', data:{name:'list_offers', summary:'3 rows'}});
aiOnEvent({type:'text', data:{message_id:'m1', text:'Hello'}});
aiOnEvent({type:'text', data:{message_id:'m1', text:'Hello again'}});
aiOnEvent({type:'proposal', data:{id:'c1', message_id:'m1'}});
aiOnEvent({type:'usage', data:{input:10, output:5, usd:0.5}});
aiOnEvent({type:'done', data:{message_id:'m1'}});
eq(AI.chat.messages.length, 1, 'the same message id is replaced, not repeated');
eq(AI.chat.messages[0].text, 'Hello again', 'the last text wins');
eq(log.filter(x => x[0] === 'step').length, 3, 'status, tool and usage lines');
eq(log.filter(x => x[0] === 'card').length, 1, 'one card for the proposal');
eq(AI.ended, true, 'done ends the turn');
AI.ended = false;
aiOnEvent({type:'error', data:{code:'ai_quota', error:'limit'}});
eq([AI.ended, AI.err], [true, 'ai_quota'], 'an error carries its code');
`)
}

// TestAssistantWidgetKeyboardAndFocus checks the dialog contract: role and name, a polite live log, Enter and Shift+Enter,
// Esc closes and gives focus back to the button, focus goes to the input on open, and a modal confirmation keeps its own Esc.
func TestAssistantWidgetKeyboardAndFocus(t *testing.T) {
	html := spaSource()
	shell := spaFunctionLine(html, "aiShellHTML")
	for _, needle := range []string{
		`role="dialog" aria-label="${t('ai.chat.title')}"`,
		`role="log" aria-live="polite"`,
		`<textarea id="ai-text"`,
		`id="ai-chat-model"`, `id="ai-stop"`, `id="ai-q"`,
	} {
		must(t, shell, needle, "panel markup")
	}
	panel := spaFunctionLine(html, "aiMountPanel")
	must(t, panel, "ta.onkeydown=e=>{if(e.key==='Enter'&&!e.shiftKey&&!e.isComposing){e.preventDefault(); aiSend()}}", "Enter sends, Shift+Enter adds a line")
	must(t, panel, "p.addEventListener('keydown',e=>{if(e.key==='Escape'){e.preventDefault(); e.stopPropagation(); aiEscape()}})", "Esc goes to aiEscape")
	must(t, panel, "q('#ai-form',p).onsubmit=e=>{e.preventDefault(); aiSend()}", "the form never submits the page")
	toggle := spaFunctionLine(html, "aiToggle")
	must(t, toggle, "const f=q('#aifab'); if(f) f.focus();", "closing returns focus to the button")
	must(t, toggle, "q('#ai-text')", "opening focuses the input")
	must(t, spaFunctionLine(html, "aiMountFab"), `aria-haspopup="dialog" aria-expanded="false" aria-controls="aipanel"`, "button describes the dialog")
	must(t, spaFunctionLine(html, "aiApplyOpen"), "f.setAttribute('aria-expanded',String(AI.open))", "button state follows the panel")
	must(t, spaFunctionLine(html, "aiPaintLog"), "log.setAttribute('aria-live','off')", "a redraw is silent for a reader")
	must(t, spaFunctionLine(html, "aiPaintLog"), "log.setAttribute('aria-live','polite')", "the log is polite again after the redraw")
	// ask() handles Esc on the document in the capture phase and stops it, so Esc in a confirmation does not close the panel.
	must(t, spaFunctionLine(html, "ask"), "document.addEventListener('keydown',key,true)", "the confirmation owns its Esc")
}

// TestAssistantWidgetDeleteNeedsSecondConfirmation runs aiApply under node: a delete asks in the project dialog and sends
// confirm_delete only after yes; an external address change sends confirm_host only with the tick; a plain edit sends an empty body.
func TestAssistantWidgetDeleteNeedsSecondConfirmation(t *testing.T) {
	html := spaSource()
	apply := spaFunctionLine(html, "aiApply")
	must(t, apply, "await ask(trFill('ai.chat.del.title'", "the second confirmation uses the project dialog")
	must(t, apply, "body.confirm_delete=true", "the confirmation reaches the request")
	must(t, apply, "body.confirm_host=true", "the host confirmation reaches the request")
	card := spaFunctionLine(html, "aiCardEl")
	must(t, card, "ap.disabled=needAck&&!AI.ack[c.id]", "Apply is off until the external address tick is set")
	must(t, card, "c.op==='delete'?'danger':'primary'", "a delete is a danger button")
	must(t, card, "aiChangeCall(c,'reject',undefined)", "reject has no body")
	must(t, card, "aiChangeCall(c,'revert',undefined)", "revert has no body")
	must(t, card, "AI.cerr[c.id]", "an action failure is shown inside the card")
	must(t, spaFunctionLine(html, "aiChangeCall"), "'/api/ai/changes/'+encodeURIComponent(c.id)+'/'+action", "the change route")
	must(t, spaFunctionLine(html, "aiRevertAll"), "'/api/ai/messages/'+encodeURIComponent(mid)+'/revert',{chat_id:AI.chat.id}", "revert of a whole reply")
	must(t, spaFunctionLine(html, "aiFoot"), "done<2", "the revert-all button needs two applied edits")
	must(t, card, "c.landing_files", "landing volume is shown")
	must(t, card, "c.links", "dependencies of a delete are shown")
	must(t, card, "c.renumbered", "a campaign number issued again is marked")
	must(t, card, "c.external_host_change", "the external address mark")

	prelude := `
const AI = {ack:{}};
const calls = [];
let answer = true;
const trFill = (k, m) => k;
const tr = k => k;
async function ask(){ calls.push('ask'); return answer; }
async function aiChangeCall(c, action, body){ calls.push([action, body]); }
`
	assistantNode(t, []string{"aiApply"}, prelude, `
await aiApply({id:'c1', op:'delete', title:'T'});
eq(calls, ['ask', ['apply', {confirm_delete:true}]], 'delete asks first, then confirms');
calls.length = 0; answer = false;
await aiApply({id:'c1', op:'delete', title:'T'});
eq(calls, ['ask'], 'a refused second confirmation sends nothing');
calls.length = 0; answer = true;
await aiApply({id:'c2', op:'update', external_host_change:true});
eq(calls, [], 'without the tick nothing is sent');
AI.ack.c2 = true;
await aiApply({id:'c2', op:'update', external_host_change:true});
eq(calls, [['apply', {confirm_host:true}]], 'with the tick confirm_host is sent');
calls.length = 0;
await aiApply({id:'c3', op:'update'});
eq(calls, [['apply', {}]], 'a plain edit is applied without confirmations');
calls.length = 0;
AI.ack.c4 = true;
await aiApply({id:'c4', op:'delete', external_host_change:true, title:'x'});
eq(calls, ['ask', ['apply', {confirm_delete:true, confirm_host:true}]], 'both confirmations travel together');
`)
}

// TestAssistantWidgetErrorsAndKeys checks that every error code of the assistant has a text in each language, that the
// streaming and edit errors go through that table, and that every ai.chat key is defined once per language with the same set in all three.
func TestAssistantWidgetErrorsAndKeys(t *testing.T) {
	html := spaSource()
	widget := assistantWidgetSrc(t)
	table := dictBetween(t, html, "const AI_ERR_KEYS={", "\n};")
	for _, code := range []string{
		"ai_disabled", "ai_busy", "ai_quota", "ai_bad_model", "ai_provider_error", "ai_timeout", "ai_rate_limited", "ai_bad_key",
		"ai_disk_full", "keitaro_running", "ai_conflict", "ai_stale", "ai_confirm_required", "ai_has_links", "ai_landing_too_large",
		"ai_redacted_value", "ai_bad_limit", "ai_bad_patch", "ai_steps_limit", "ai_input_too_large", "ai_chat_full", "ai_not_acceptable",
		"result_too_large", "internal", "unauthorized",
	} {
		must(t, table, code+":'ai.chat.err."+code+"'", "error code "+code)
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, "ai.chat.err."+code) {
				t.Errorf("error text of %s missing for %s", code, lang)
			}
		}
	}
	must(t, spaFunctionLine(html, "aiOnEvent"), "AI.err=aiErrText(d.code||'',d.error,d.params)", "a stream error is translated by its code and params")
	must(t, spaFunctionLine(html, "aiChangeCall"), "AI.cerr[c.id]=aiErrOf(e)", "an apply or revert error is translated by its code")
	must(t, spaFunctionLine(html, "aiErrOf"), "body&&body.code||''", "the code is read from the error body")
	must(t, html, "'ai.chat.err.ai_conflict':'Changed by hand since, so the revert is impossible. Ask the assistant for a new edit.'", "ai_conflict text")

	used := map[string]struct{}{}
	for _, m := range regexp.MustCompile(`'(ai\.chat\.[A-Za-z0-9_.]+)'`).FindAllStringSubmatch(widget, -1) {
		used[m[1]] = struct{}{}
	}
	if len(used) < 40 {
		t.Fatalf("too few ai.chat keys found in the widget: %d", len(used))
	}
	blocks := strings.Split(html, "// ai.chat begin\n")
	if len(blocks) != 4 {
		t.Fatalf("want one ai.chat block per language, found %d", len(blocks)-1)
	}
	sets := make([]map[string]struct{}, 0, 3)
	for i, b := range blocks[1:] {
		end := strings.Index(b, "// ai.chat end")
		if end < 0 {
			t.Fatalf("ai.chat block %d has no end marker", i)
		}
		dictDupes(t, b[:end], "ai.chat block "+[]string{"en", "ru", "uk"}[i])
		sets = append(sets, dictKeys(b[:end]))
	}
	sameDictKeys(t, sets[0], sets[1], "ai.chat en", "ai.chat ru")
	sameDictKeys(t, sets[0], sets[2], "ai.chat en", "ai.chat uk")
	for k := range used {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	for k := range sets[0] {
		if _, ok := used[k]; !ok {
			t.Errorf("dictionary key %q is not used by the widget", k)
		}
	}
	must(t, blocks[2], "'ai.chat.res.offers':'Оффер'", "ru offer resource")
	must(t, blocks[3], "'ai.chat.res.offers':'Офер'", "uk offer resource")
}

// TestAssistantWidgetHistory checks the history list: search by title through the q parameter, open, delete after a
// confirmation, the chat created lazily, and a new chat that is blocked while a turn runs.
func TestAssistantWidgetHistory(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "aiHistLoad"), "'/api/ai/chats'+(AI.q?'?q='+encodeURIComponent(AI.q):'')", "search by title")
	must(t, spaFunctionLine(html, "aiHistLoad"), "if(seq!==AI.hseq) return", "an older search answer is dropped")
	must(t, spaFunctionLine(html, "aiOpenChat"), "call('GET','/api/ai/chats/'+encodeURIComponent(id))", "open a chat")
	del := spaFunctionLine(html, "aiDeleteChat")
	must(t, del, "await ask(", "deleting a chat asks first")
	must(t, del, "call('DELETE','/api/ai/chats/'+encodeURIComponent(c.id))", "delete a chat")
	must(t, spaFunctionLine(html, "aiNewChat"), "if(AI.busy) return", "no new chat while a turn runs")
	must(t, spaFunctionLine(html, "aiBusyUI"), "q('#ai-new',p).disabled=AI.busy", "the new chat button is off during a turn")
	must(t, spaFunctionLine(html, "aiMountPanel"), "AI.qtimer=setTimeout(aiHistLoad,250)", "the search waits for a pause in typing")
	must(t, spaFunctionLine(html, "aiPaintHist"), "ai.chat.hist.none", "an empty list has a text")
	must(t, spaFunctionLine(html, "aiStop"), "AI.abort.abort()", "sign-out cuts the running turn")
}

// TestAssistantWidgetUsageBlock checks the usage strip from the status: the user's and the team's tokens and dollars
// with limits (0 means none), the bar, and the warning for a model without a price.
func TestAssistantWidgetUsageBlock(t *testing.T) {
	html := spaSource()
	use := spaFunctionLine(html, "aiPaintUse")
	for _, needle := range []string{"u.limit_tokens", "u.limit_usd", "u.team_tokens", "u.team_limit_tokens", "u.team_limit_usd", "aiPriceUnknown(st,AI.model)", "ai.chat.usage.unknown", "aiLim(usd,ul,aiUSD)", "trFill('ai.chat.usage.line',{label,tokens:aiLim(tok,tl,fmt),unit:tr('ai.chat.usage.tokens'),usd:aiLim(usd,ul,aiUSD)})"} {
		must(t, use, needle, "usage block")
	}
	for _, gone := range []string{"st.price_unknown", "' · '", `": "`, "': '"} {
		mustNot(t, use, gone, "the usage line is one template of the dictionary: "+gone)
	}
	assistantNode(t, []string{"aiLim"}, `
const trFill = (k, m) => k + ':' + m.used + (m.limit !== undefined ? '/' + m.limit : '');
`, `
eq(aiLim(10, 300, String), 'ai.chat.usage.of:10/300', 'used of the limit');
eq(aiLim(10, null, String), 'ai.chat.usage.nolimit:10', 'null means no limit');
eq(aiLim(10, 0, String), 'ai.chat.usage.of:10/0', 'zero is a limit');
eq(aiLim(undefined, 5, String), 'ai.chat.usage.of:0/5', 'missing usage is zero');
`)
	assistantNode(t, []string{"aiLim", "aiPct", "aiPriceUnknown", "aiPaintUse"}, `
const trFill = (k, m) => k === 'ai.chat.usage.line' ? [m.label, m.tokens, m.unit, m.usd].join('|') : k + ':' + m.used + (m.limit !== undefined ? '/' + m.limit : '');
const tr = k => 'T:' + k, fmt = String, aiUSD = n => '$' + n;
class E { constructor(t, c, x){ this.tag = t; this.cls = c; this.text = x; this.children = []; this.style = {}; }
  appendChild(c){ this.children.push(c); return c; } setAttribute(){} get firstChild(){ return this.children[0]; }
  set textContent(v){ this.children = []; } }
const aiEl = (t, c, x) => new E(t, c, x);
let box = new E('div');
const q = () => box;
const AI = {model:'a', status:null};
`, `
const usage = {tokens:5, usd:1, limit_tokens:10, limit_usd:null};
AI.model = 'a';
AI.status = {usage, model_info:[{id:'a', price_unknown:false}, {id:'b', price_unknown:true}], price_unknown:true};
aiPaintUse();
eq(box.children.filter(c => c.tag === 'span' && !c.cls).map(c => c.text), ['T:ai.chat.usage.you|ai.chat.usage.of:5/10|T:ai.chat.usage.tokens|ai.chat.usage.nolimit:$1'], 'one template for the line, no warning for a model with a price');
eq(box.children.some(c => c.cls === 'warn'), false, 'the status flag of the default model is not read');
AI.model = 'b';
aiPaintUse();
eq(box.children.filter(c => c.cls === 'warn').map(c => c.text), ['T:ai.chat.usage.unknown'], 'the warning follows the chosen model');
AI.model = 'zzz';
aiPaintUse();
eq(box.children.some(c => c.cls === 'warn'), false, 'a model without model_info has no warning');
`)
}

// TestAssistantWidgetIDsDoNotClashWithSettingsPage checks that no element id of the chat panel repeats an id of the
// AI settings page, which is in the document at the same time, and that the model choice of the chat is looked up inside the panel.
func TestAssistantWidgetIDsDoNotClashWithSettingsPage(t *testing.T) {
	html := spaSource()
	idRe := regexp.MustCompile(`\bid(?:="|:')([A-Za-z0-9_-]+)`)
	ids := func(src string) map[string]struct{} {
		out := map[string]struct{}{}
		for _, m := range idRe.FindAllStringSubmatch(src, -1) {
			out[m[1]] = struct{}{}
		}
		return out
	}
	page := ids(dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n"))
	chat := ids(assistantFn(html, "aiShellHTML") + assistantFn(html, "aiMountFab"))
	if len(page) < 10 || len(chat) < 10 {
		t.Fatalf("too few ids found: settings page %d, chat %d", len(page), len(chat))
	}
	for id := range chat {
		if _, dup := page[id]; dup {
			t.Errorf("id %q is used by the AI settings page and by the chat panel", id)
		}
	}
	must(t, assistantFn(html, "aiShellHTML"), `id="ai-chat-model"`, "the chat model choice has its own id")
	must(t, assistantFn(html, "aiPaintModels"), "q('#ai-chat-model',panel)", "the chat model choice is looked up inside the panel")
	mustNot(t, assistantWidgetSrc(t), "q('#ai-model'", "the settings page id is never read by the chat")
}

// assistantErrPrelude declares what the error text functions of the SPA read: the tables, the dictionary stand-ins and the formatters.
// tr returns "T:" and the key; trFill returns the key and the values it was given, so a check sees the key and the placeholders.
const assistantErrPrelude = `
const AI_ERR_KEYS = %KEYS%;
const AI_ERR_VARIANTS = %VARIANTS%;
const AI_ERR_PICK = %PICK%;
const AI_FIELD_KEYS = %FIELDS%;
const AI_REQUIRED = %REQUIRED%;
const AI_RES_KEYS = {offers:'ai.chat.res.offers', campaigns:'ai.chat.res.campaigns'};
const AI_ST_KEYS = {done:'ai.chat.st.done'};
const AI_OP_KEYS = {create:'ai.chat.op.create', update:'ai.chat.op.update', delete:'ai.chat.op.delete'};
const AI_PERM_ACC = {affiliate_networks:'networks', traffic_sources:'sources'};
const USER_ACCESS_RESOURCES = ['campaigns', 'offers', 'landings', 'sources', 'networks', 'domains'];
const AI_LIMITS = [['max_steps','ai.set.l.steps',0,0], ['daily_usd','ai.set.l.dailyusd',1,1]];
const tr = k => 'T:' + k;
const trFill = (k, m) => k + JSON.stringify(m);
const errText = m => 'E:' + m;
const fmt = n => 'n' + n;
const aiBytes = n => n + 'B';
`

// assistantErrNode runs the checks against the real error tables and text functions of the SPA.
// checks is the test body, as for assistantNode.
func assistantErrNode(t *testing.T, checks string) {
	t.Helper()
	html := spaSource()
	prelude := strings.NewReplacer(
		"%KEYS%", strings.TrimSpace(dictBetween(t, html, "const AI_ERR_KEYS=", "\n};"))+"\n}",
		"%VARIANTS%", strings.TrimSpace(dictBetween(t, html, "const AI_ERR_VARIANTS=", "\n};"))+"\n}",
		"%PICK%", strings.TrimSpace(dictBetween(t, html, "const AI_ERR_PICK=", "\n};"))+"\n}",
		"%FIELDS%", strings.TrimSpace(dictBetween(t, html, "const AI_FIELD_KEYS=", "\n};"))+"\n}",
		"%REQUIRED%", strings.TrimSpace(dictBetween(t, html, "const AI_REQUIRED=", "};"))+"}",
	).Replace(assistantErrPrelude)
	assistantNode(t, []string{"aiVal", "aiLinks", "aiFieldName", "aiPermResName", "aiErrVars", "aiErrKey", "aiErrText", "aiErrOf"}, prelude, checks)
}

// TestAssistantWidgetErrorParams runs the error text functions under node: a code with params picks its variant and fills the
// placeholders (field as the translated label of a limit, min, max, model, links as "resource: name"), a code without params keeps
// the plain text, and the message of the server is used only when the code has no key.
func TestAssistantWidgetErrorParams(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiEl"), "textContent", "texts are put with textContent")
	must(t, assistantFn(html, "aiCardEl"), "if(Array.isArray(c.links)&&c.links.length){", "a card without links is drawn")
	assistantErrNode(t, `
eq(aiErrText('ai_bad_limit', 'x', {field:'max_steps', min:1, max:20}), 'ai.chat.err.ai_bad_limit.range{"field":"T:ai.set.l.steps","min":"n1","max":"n20","model":"—","id":"—","slug":"—","stream":"—","fields":"—","resource":"—","op":"—","status":"—","links":"—","limit":"—"}', 'a range error names the limit and its bounds');
eq(aiErrKey('ai_bad_limit', {field:'daily_usd', min:0, max:1000000}), 'ai.chat.err.ai_bad_limit.zero', 'a daily limit says 0 means no limit');
eq(aiErrKey('ai_bad_limit', {}), 'ai.chat.err.ai_bad_limit', 'no params keep the plain text');
for (const r of ['no_catalog', 'unknown', 'no_tools', 'not_allowed']) eq(aiErrKey('ai_bad_model', {model:'gpt-x', reason:r}), 'ai.chat.err.ai_bad_model.' + r, 'model reason ' + r);
eq(aiErrKey('ai_bad_model', {model:'gpt-x', reason:'new_one'}), 'ai.chat.err.ai_bad_model', 'an unknown model reason keeps the plain text');
eq(aiErrKey('ai_bad_model'), 'ai.chat.err.ai_bad_model', 'no params, no model name');
eq(aiErrText('ai_bad_model', '', {model:'gpt-x', reason:'no_tools'}), 'ai.chat.err.ai_bad_model.no_tools' + JSON.stringify(aiErrVars({model:'gpt-x'})), 'the model name is filled in');
for (const r of ['invalid', 'unknown_provider', 'too_many', 'bad_price', 'required', 'too_long']) eq(aiErrKey('ai_bad_request', {field:'prices', reason:r}), 'ai.chat.err.ai_bad_request.' + r, 'request reason ' + r);
eq(aiErrKey('ai_bad_request', {field:'x'}), 'ai.chat.err.ai_bad_request', 'no reason keeps the plain text');
eq(aiErrKey('ai_bad_request', {field:'allowed_models', reason:'required'}), 'ai.chat.err.ai_bad_request.models_required', 'no model ticked has its own text');
eq(aiErrKey('ai_bad_request', {field:'text', reason:'required'}), 'ai.chat.err.ai_bad_request.required', 'another required field keeps the general text');
eq(aiErrVars({field:'allowed_models'}).field, 'T:ai.set.models', 'a request field takes the label of the settings page');
eq(aiErrVars({field:'chat_id'}).field, 'T:ai.chat.field.chat_id', 'a request field of the chat');
eq(aiErrVars({field:'url'}).field, 'T:ai.chat.field.url', 'a resource field has its own name');
eq(aiErrVars({field:'nonsense'}).field, 'nonsense', 'a field with no name stays as the server wrote it');
eq(aiErrVars({field:'toString'}).field, 'toString', 'an inherited name is no field');
eq(aiFieldName(''), '—', 'an empty field is a dash');
eq(aiErrVars({model:'m1'}).model, 'm1', 'model');
eq(aiErrVars({resource:'offers', status:'done'}).resource + '|' + aiErrVars({resource:'offers', status:'done'}).status, 'T:ai.chat.res.offers|T:ai.chat.st.done', 'resource and status are translated');
eq(aiErrVars({reason:'size', limit:2048}).limit, '2048B', 'a landing size is written in bytes');
eq(aiErrVars({reason:'files', limit:300}).limit, 'n300', 'a file count is a number');
eq(aiErrVars({fields:['b_curl','curl']}).fields, 'b_curl, curl', 'fields joined');
const links = [{resource:'campaigns', id:'g1', name:'Demo'}, {resource:'offers', id:'o2'}, {resource:'x', id:'q'}];
eq(aiLinks(links), ['T:ai.chat.res.campaigns: Demo', 'T:ai.chat.res.offers: o2', 'x: q'], 'links are "resource: name", the id when there is no name');
eq(aiLinks(null), [], 'no links');
eq(aiErrKey('ai_has_links', {links}), 'ai.chat.err.ai_has_links.list', 'dependants are listed');
eq(aiErrKey('ai_has_links', {links:[]}), 'ai.chat.err.ai_has_links', 'an empty list keeps the plain text');
eq(aiErrVars({links}).links, 'T:ai.chat.res.campaigns: Demo; T:ai.chat.res.offers: o2; x: q', 'links text');
eq(aiErrKey('ai_conflict', {reason:'slug_taken', slug:'promo'}), 'ai.chat.err.ai_conflict.slug_taken', 'conflict reason');
eq(aiErrKey('ai_conflict', {reason:'brand_new'}), 'ai.chat.err.ai_conflict', 'an unknown reason keeps the plain text');
eq(aiErrKey('ai_conflict'), 'ai.chat.err.ai_conflict', 'a conflict with no params');
eq(aiErrKey('ai_forbidden', {resource:'offers', op:'delete'}), 'ai.chat.err.ai_forbidden.op', 'a refusal names the action');
eq(aiErrKey('ai_forbidden'), 'ai.chat.err.ai_forbidden', 'a refusal with no params');
{
  const s = aiErrText('ai_forbidden', '', {resource:'domains', op:'update'});
  eq([s.startsWith('ai.chat.err.ai_forbidden.op{'), s.includes('"resource":"T:acc.res.domains"'), s.includes('"op":"T:ai.chat.op.update"')], [true, true, true], 'a refusal names a resource outside the change cards: ' + s);
}
eq(aiErrKey('ai_confirm_required', {need:'host'}), 'ai.chat.err.ai_confirm_required.host', 'the lacking confirmation');
eq(aiErrKey('ai_bad_patch', {reason:'not_editable', field:'x', resource:'offers'}), 'ai.chat.err.ai_bad_patch.not_editable', 'patch reason');
eq(aiErrKey('ai_landing_too_large', {reason:'symlink'}), 'ai.chat.err.ai_landing_too_large.symlink', 'landing reason');
eq(aiErrKey('ai_redacted_value', {field:'url'}), 'ai.chat.err.ai_redacted_value.field', 'redacted field');
eq(aiErrKey('keitaro_running'), 'ai.chat.err.keitaro_running', 'a code without variants');
eq(aiErrKey('weird_code'), '', 'a code the panel does not know');
eq(aiErrKey('bad_request', {reason:'invalid'}), '', 'the old plain code is not the assistant one');
eq(aiErrText('ai_quota', 'daily limit is reached'), 'ai.chat.err.ai_quota' + JSON.stringify(aiErrVars()), 'a known code ignores the server text');
eq(aiErrText('weird_code', 'raw server text'), 'E:raw server text', 'an unknown code falls back to the server message');
eq(aiErrText('', ''), 'T:ai.chat.err.generic', 'nothing known');
eq(aiErrText('ai_conflict_old', '', null), 'T:ai.chat.err.generic', 'a saved message with an unknown code reads as a generic error');
eq(aiErrOf({message:'raw', body:{code:'ai_bad_model', params:{model:'z'}}}), aiErrText('ai_bad_model', 'raw', {model:'z'}), 'a thrown error reads the code and params of its body');
eq(aiErrOf({message:'raw'}), 'E:raw', 'an error without a body shows its message');
eq(aiErrOf(null), 'T:ai.chat.err.generic', 'no error');
`)
}

// TestAssistantWidgetErrorCodesFromBackend collects every error code of the Go assistant sources (ai_* constants and literals,
// keitaro_running, result_too_large) and every reason of ai_conflict, ai_bad_patch and ai_landing_too_large and the confirmations of
// ai_confirm_required, and checks that the SPA has a key for each, defined once in en, ru and uk.
func TestAssistantWidgetErrorCodesFromBackend(t *testing.T) {
	files, err := filepath.Glob("assistant_*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("assistant sources not found: %v", err)
	}
	codeRe := regexp.MustCompile(`"((?:ai_[a-z]+(?:_[a-z]+)*)|keitaro_running|result_too_large)"`)
	reasonBlockRe := regexp.MustCompile(`// Reasons of an (ai_[a-z_]+) (?:refusal|error)[^\n]*\nconst \(([\s\S]*?)\n\)`)
	reasonRe := regexp.MustCompile(`=\s*"([a-z_]+)"`)
	confirmRe := regexp.MustCompile(`aiChgConfirmRequired\("([a-z]+)"`)
	codes := map[string]struct{}{}
	reasons := map[string][]string{}
	var needs []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, m := range codeRe.FindAllStringSubmatch(src, -1) {
			codes[m[1]] = struct{}{}
		}
		for _, b := range reasonBlockRe.FindAllStringSubmatch(src, -1) {
			for _, r := range reasonRe.FindAllStringSubmatch(b[2], -1) {
				reasons[b[1]] = append(reasons[b[1]], r[1])
			}
		}
		for _, m := range confirmRe.FindAllStringSubmatch(src, -1) {
			needs = append(needs, m[1])
		}
	}
	for _, want := range []string{"ai_disabled", "ai_bad_key", "ai_quota", "ai_bad_model", "ai_bad_request", "ai_bad_limit", "ai_bad_patch", "ai_conflict", "ai_steps_limit", "ai_input_too_large", "ai_chat_full", "ai_not_acceptable", "keitaro_running", "result_too_large"} {
		if _, ok := codes[want]; !ok {
			t.Errorf("the Go sources no longer carry the code %q: the collector is stale", want)
		}
	}
	for _, c := range []string{"ai_conflict", "ai_bad_patch", "ai_landing_too_large", "ai_bad_model", "ai_bad_request"} {
		if len(reasons[c]) < 3 {
			t.Errorf("reasons of %s not found in the Go sources: %v", c, reasons[c])
		}
	}
	if len(needs) < 2 {
		t.Errorf("confirmations of ai_confirm_required not found: %v", needs)
	}
	html := spaSource()
	keys := dictBetween(t, html, "const AI_ERR_KEYS={", "\n};")
	variants := dictBetween(t, html, "const AI_ERR_VARIANTS={", "\n};")
	check := func(key, what string) {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("%s: key %q is defined %d time(s), want one per language", what, key, n)
		}
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("%s: key %q missing for %s", what, key, lang)
			}
		}
	}
	for code := range codes {
		key := "ai.chat.err." + code
		must(t, keys, code+":'"+key+"'", "the code "+code+" has a text")
		check(key, "code "+code)
	}
	variantLine := func(code string) string {
		i := strings.Index(variants, "  "+code+":{")
		if i < 0 {
			t.Fatalf("AI_ERR_VARIANTS has no entry for %s", code)
		}
		line := variants[i:]
		if j := strings.Index(line, "\n"); j >= 0 {
			line = line[:j]
		}
		return line
	}
	for code, list := range reasons {
		line := variantLine(code)
		for _, r := range list {
			key := "ai.chat.err." + code + "." + r
			must(t, line, r+":'"+key+"'", "the reason "+r+" of "+code)
			check(key, "reason "+r+" of "+code)
		}
	}
	line := variantLine("ai_confirm_required")
	for _, n := range needs {
		must(t, line, n+":'ai.chat.err.ai_confirm_required."+n+"'", "the confirmation "+n)
		check("ai.chat.err.ai_confirm_required."+n, "confirmation "+n)
	}
	for _, k := range []string{"ai.chat.err.ai_bad_limit.range", "ai.chat.err.ai_bad_limit.zero", "ai.chat.err.ai_bad_model.no_catalog", "ai.chat.err.ai_bad_model.no_tools", "ai.chat.err.ai_bad_request.invalid", "ai.chat.err.ai_bad_request.models_required", "ai.chat.err.ai_has_links.list", "ai.chat.err.ai_redacted_value.field"} {
		check(k, "param variant")
	}
}

// TestAssistantWidgetTestEndpointNote checks the mark shown when the server runs against a test provider address:
// on the integration page from the settings and in the chat header from the status, in all three languages.
func TestAssistantWidgetTestEndpointNote(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	must(t, panel, "cur.test_endpoint===true?calloutHTML('warn',tr('ai.set.testep')", "the settings page warns about a test address, it is no error")
	must(t, assistantFn(html, "aiShellHTML"), `id="ai-chat-test" role="alert" hidden>${t('ai.chat.test')}`, "the chat header carries the mark, hidden by default")
	must(t, assistantFn(html, "aiSync"), "note.hidden=AI.status.test_endpoint!==true", "the chat mark follows the status")
	for _, k := range []string{"ai.set.testep", "ai.chat.test"} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	must(t, html, "'ai.chat.test':'Тестовый адрес провайдера (локальная проверка), реальные запросы не уходят'", "ru text of the mark")
}

// TestAssistantWidgetRejectedDeleteAndReverting checks the cards of a delete refused for dependencies and of an edit
// being reverted: the dependants are listed as text, the status is named, and neither card has an action button.
func TestAssistantWidgetRejectedDeleteAndReverting(t *testing.T) {
	html := spaSource()
	card := assistantFn(html, "aiCardEl")
	for _, needle := range []string{
		"c.status==='rejected'&&c.op==='delete'&&Array.isArray(c.links)&&c.links.length>0",
		"blocked ? 'ai.chat.st.rejected_links' : AI_ST_KEYS[c.status]",
		"aiLinks(c.links).forEach(line=>ul.appendChild(aiEl('li',null,line)))",
		"aiErrText(c.error_code||c.error,c.error,c.error_params)",
		"if(proposed||done){",
	} {
		must(t, card, needle, "edit card")
	}
	mustNot(t, card, "innerHTML", "the card puts server texts with textContent only")
	must(t, html, "reverting:'ai.chat.st.reverting'", "the reverting status has a text")
	must(t, html, "reverting:'',reverted:''", "the reverting status is neutral")
	must(t, spaFunctionLine(html, "aiOnEvent"), "ev.type==='proposal'&&d.id", "a proposal event replaces the card")
	for _, k := range []string{"ai.chat.st.rejected_links", "ai.chat.st.reverting"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	must(t, html, "'ai.chat.st.rejected_links':'Отклонено: есть зависимости'", "ru status text")
	must(t, html, "'ai.chat.st.reverting':'Откатывается…'", "ru reverting text")
}

// TestAssistantWidgetRefreshesPage checks that an applied or reverted edit redraws the current page when it lists the
// resource, drops the catalog caches, and leaves the page alone when it has unsaved edits, an open window or a field in use.
func TestAssistantWidgetRefreshesPage(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiChangeCall"), "if(action!=='reject') aiRefreshPage([v&&v.resource||c.resource]);", "apply and revert redraw the page")
	revertAll := assistantFn(html, "aiRevertAll")
	must(t, revertAll, "x.message_id===mid&&x.status==='reverted'", "revert of a whole reply redraws for its reverted edits")
	must(t, revertAll, "aiRefreshPage(", "revert of a whole reply redraws the page")
	mustNot(t, assistantFn(html, "aiRefreshPage"), "aiPaintLog", "the panel log is not redrawn with the page")
	must(t, html, "'ai.chat.stale':'Данные изменились, обновите страницу'", "ru text of the manual refresh toast")

	table := dictBetween(t, html, "const AI_RES_PAGES=", ";\n")
	assistantNode(t, []string{"aiDropCaches", "aiRefreshPage"}, `
const AI_RES_PAGES=`+table+`;
let OFFERS=[1], NETS=[1], LANDINGS=[1], SOURCES=[1], GROUPS=[1], PERF=[1], FORM=null, DRAWER=null, MENU=null;
let dirty=false, flows=false, where={page:'offers',arg:''}, scrim=null, active=null, inView=false, renders=0, toasts=[];
const flowsDirty=()=>flows, anyDirty=()=>dirty||flows, route=()=>where, view=()=>({contains:()=>inView});
const q=()=>scrim, tr=k=>k, toast=m=>toasts.push(m), render=()=>{renders++};
const document={get activeElement(){return active}};
const reset=(page,arg)=>{OFFERS=[1];NETS=[1];LANDINGS=[1];SOURCES=[1];GROUPS=[1];PERF=[1];FORM=DRAWER=MENU=null;dirty=flows=false;scrim=null;active=null;inView=false;renders=0;toasts=[];where={page,arg:arg||''}};
`, `
reset('offers'); aiRefreshPage(['offers']);
eq([renders, OFFERS, NETS, PERF], [1, null, [1], null], 'offers page is redrawn and only the offers cache is dropped');
reset('offers'); aiRefreshPage(['landings']);
eq([renders, LANDINGS], [0, null], 'another resource does not redraw this page but drops its cache');
reset('networks'); aiRefreshPage(['affiliate_networks','offers']);
eq([renders, NETS, OFFERS], [1, null, null], 'one redraw for several resources');
reset('flows'); aiRefreshPage(['campaigns']);
eq([renders, GROUPS], [1, null], 'campaign list is redrawn');
reset('flows','abc'); aiRefreshPage(['campaigns']);
eq([renders, toasts], [0, ['ai.chat.stale']], 'an open campaign editor is not redrawn');
reset('flows'); flows=true; aiRefreshPage(['campaigns']);
eq([renders, GROUPS, toasts], [0, [1], ['ai.chat.stale']], 'unsaved campaigns keep the list and ask for a manual refresh');
reset('offers'); dirty=true; aiRefreshPage(['offers']);
eq([renders, toasts], [0, ['ai.chat.stale']], 'unsaved edits block the redraw');
reset('offers'); FORM={}; aiRefreshPage(['offers']);
eq(renders, 0, 'an open window blocks the redraw');
reset('offers'); scrim={}; aiRefreshPage(['offers']);
eq(renders, 0, 'an open dialog blocks the redraw');
reset('offers'); active={tagName:'INPUT'}; inView=true; aiRefreshPage(['offers']);
eq(renders, 0, 'a field of the page in use blocks the redraw');
reset('offers'); active={tagName:'TEXTAREA'}; inView=false; aiRefreshPage(['offers']);
eq(renders, 1, 'the assistant input does not block the redraw');
reset('offers'); aiRefreshPage([]); aiRefreshPage([undefined]);
eq([renders, toasts], [0, []], 'nothing to redraw without a resource');
`)
}

// TestAssistantWidgetHistoryNoMatch checks that a search without results says nothing was found, and the empty list says there are no chats.
func TestAssistantWidgetHistoryNoMatch(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiPaintHist"), "tr(AI.q ? 'ai.chat.hist.nomatch' : 'ai.chat.hist.none')", "the empty text follows the query")
	for _, k := range []string{"ai.chat.hist.nomatch", "ai.chat.hist.none"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	must(t, html, "'ai.chat.hist.nomatch':'Ничего не найдено'", "ru text")
}

// TestAssistantWidgetLeavesRoomUnderButton checks that the page content gets room below the last element while the round button is shown.
func TestAssistantWidgetLeavesRoomUnderButton(t *testing.T) {
	html := spaSource()
	must(t, html, `body.has-aifab main::after{content:"";display:block;height:calc(56px + var(--ai-sb,0px))}`, "extra scroll room under the button")
	must(t, assistantFn(html, "aiSync"), "document.body.classList.add('has-aifab')", "the class is set with the button")
}

// TestAssistantWidgetSendsLanguage runs aiSend under node and checks that the body of the message carries lang with the
// language of the interface (en, ru or uk) next to the text, the page context and the chosen model.
func TestAssistantWidgetSendsLanguage(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiSend"), "const body={text,context:aiPageContext(),lang:uiLang()};", "the body carries lang")
	prelude := `
const ta = {value:'', style:{}};
const q = sel => sel === '#ai-text' ? ta : null;
const AI = {busy:false, ended:false, err:'', chat:{id:'c1', messages:[], changes:[]}, abort:null, model:'m1', tick:0};
let LANG = 'ru';
const uiLang = () => LANG;
const sent = [];
async function aiStream(id, body){ sent.push([id, body]); AI.ended = true; }
const aiBusyUI = () => {}, aiPaintLog = () => {}, aiAddMessage = () => {}, aiStatusLoad = () => {}, toast = () => {}, aiMicAbort = () => {};
const aiPageContext = () => ({page:'offers'});
async function aiReloadChat(){}
const tr = k => k, aiErrOf = e => String(e);
`
	assistantNode(t, []string{"aiSend"}, prelude, `
for (const lang of ['ru', 'uk', 'en']) {
  LANG = lang; ta.value = 'hello ' + lang;
  await aiSend();
  const [id, body] = sent[sent.length - 1];
  eq([id, body.text, body.lang, body.model, body.context], ['c1', 'hello ' + lang, lang, 'm1', {page:'offers'}], 'the body for ' + lang);
}
eq(sent.length, 3, 'one request per message');
`)
}

// assistantFakeDOM is a small stand-in for the document the model list needs: elements with children, text, attributes and focus.
const assistantFakeDOM = `
class El {
  constructor(tag){ this.tagName = tag.toUpperCase(); this.children = []; this.className = ''; this.attrs = {}; this._text = ''; this.value = ''; this.name = ''; this.type = ''; this.checked = false; this.disabled = false; this.title = ''; this.parent = null; this.classList = {add: c => { if (!this.className.split(' ').includes(c)) this.className = (this.className + ' ' + c).trim(); }}; }
  get textContent(){ return this._text + this.children.map(c => c.textContent).join(' '); }
  set textContent(v){ this.children.forEach(c => c.parent = null); this.children = []; this._text = v == null ? '' : String(v); }
  appendChild(c){ c.parent = this; this.children.push(c); return c; }
  setAttribute(k, v){ this.attrs[k] = String(v); }
  contains(x){ for (let n = x; n; n = n.parent) if (n === this) return true; return false; }
  focus(){ document.activeElement = this; }
  all(){ return this.children.flatMap(c => [c, ...c.all()]); }
}
const document = {activeElement:null, createElement: t => new El(t)};
const qa = (sel, root) => root.all().filter(e => e.tagName === sel.toUpperCase());
const rows = host => host.all().filter(e => e.className.split(' ').includes('aimp-r'));
const rowInput = r => r.children[0].children[0];
const tr = k => k, trFill = (k, m) => k + JSON.stringify(m);
const uiLocale = () => 'en-US';
const icon = k => '[' + k + ']';
const $ = h => { const e = new El('svg'); e.attrs.markup = h; return e; };
`

// TestAssistantWidgetModelPicker runs the shared model list under node: the search matches id and name in any letter case, the
// tools filter hides a model known to lack tools and keeps one with unknown support (marked as such), a model without tools cannot be
// selected, the row shows the price per 1M and the context or their absence, and the default model follows the ticks.
func TestAssistantWidgetModelPicker(t *testing.T) {
	funcs := []string{"aiEl", "aiVal", "aiUSD", "aiModelNorm", "aiModelsMerge", "aiModelFilter", "aiModelPickable", "aiModelDefault", "aiCtxText", "aiModelMeta", "aiModelPicker"}
	prelude := assistantFakeDOM + `const AI = {tick:0};`
	assistantNode(t, funcs, prelude, `
const cat = [
  {id:'acme/big', name:'Big One', context:128000, input_per_m:2.5, output_per_m:10, tools:true},
  {id:'acme/mini', name:'', context:0, input_per_m:null, output_per_m:null, tools:false},
  {id:'beta/new', name:'Newcomer', context:1000000, input_per_m:0, output_per_m:0, tools:null},
  'plain-id'
];
const models = aiModelsMerge(cat, ['kept/missing', 'acme/big']);
eq(models.map(m => m.id), ['acme/big', 'acme/mini', 'beta/new', 'plain-id', 'kept/missing'], 'the catalog in its order, then the ids it lacks, no repeats');
eq(aiModelNorm('x'), {id:'x', name:'', context:0, input_per_m:null, output_per_m:null, tools:null}, 'a bare id');
eq(aiModelNorm({id:'y', tools:'yes', input_per_m:'3'}).tools, null, 'tools is a boolean or unknown');
eq(aiModelNorm({id:'y', input_per_m:'3'}).input_per_m, 3, 'a numeric price');

eq(aiModelFilter(models, 'MINI', false).map(m => m.id), ['acme/mini'], 'search by id, any letter case');
eq(aiModelFilter(models, 'newcomer', false).map(m => m.id), ['beta/new'], 'search by name');
eq(aiModelFilter(models, '  ', false).length, 5, 'a blank search shows all');
eq(aiModelFilter(models, '', true).map(m => m.id), ['acme/big', 'beta/new', 'plain-id', 'kept/missing'], 'the tools filter hides only tools:false');
eq(aiModelFilter(models, 'acme', true).map(m => m.id), ['acme/big'], 'search and filter together');
eq([aiModelPickable({tools:false}, false), aiModelPickable({tools:false}, true), aiModelPickable({tools:null}, false), aiModelPickable({tools:true}, false)], [false, true, true, true], 'a model without tools cannot be picked');
const sel = new Set(['acme/mini', 'beta/new']);
eq(aiModelDefault(models, sel, 'acme/mini'), 'beta/new', 'the default skips a model without tools');
eq(aiModelDefault(models, sel, 'beta/new'), 'beta/new', 'a valid default stays');
eq(aiModelDefault(models, new Set(), 'beta/new'), '', 'nothing ticked, no default');
eq(aiCtxText(0), '—', 'unknown context');
eq(aiCtxText(128000), '128K', 'short context');
eq(aiModelMeta(models[0]), {price:'ai.chat.model.price{"in":"$2.50","out":"$10.00"}', ctx:'ai.chat.model.ctx{"n":"128K"}', tools:'ai.chat.model.tools.yes'}, 'priced model');
eq(aiModelMeta(models[1]), {price:'ai.chat.model.price.unknown', ctx:'ai.chat.model.ctx{"n":"—"}', tools:'ai.chat.model.tools.no'}, 'a model with no price and no context');
eq(aiModelMeta(models[2]).tools, 'ai.chat.model.tools.unknown', 'unknown tool support is marked');
eq(aiModelMeta(models[2]).price, 'ai.chat.model.price{"in":"$0.00","out":"$0.00"}', 'a free model has a price, zero');

const host = new El('div');
const o = {models, multi:true, withDefault:true, sel:new Set(['acme/big']), defId:'acme/big', stamp:'stamp text', changes:0, onChange(){ this.changes++; }};
aiModelPicker(host, o);
const search = () => qa('input', host).find(e => e.type === 'search');
const only = () => qa('input', host).find(e => e.type === 'checkbox' && e.parent.className.includes('chk'));
eq(rows(host).length, 5, 'every model has a row');
eq(host.all().find(e => e.tagName === 'P').textContent, 'stamp text', 'the note under the list');
const mini = rows(host).find(r => r.textContent.includes('acme/mini'));
eq([rowInput(mini).disabled, mini.className.includes('off')], [true, true], 'a model without tools is disabled');
eq(rows(host)[0].textContent.includes('ai.chat.model.price{"in":"$2.50","out":"$10.00"}'), true, 'the row shows the price');
eq(rows(host)[2].textContent.includes('ai.chat.model.tools.unknown'), true, 'the row marks unknown tool support');
search().value = 'new'; search().oninput();
eq(rows(host).map(r => r.textContent.includes('Newcomer')), [true], 'search narrows the rows to the one that matches in name and id');
search().value = 'zzz'; search().oninput();
eq([rows(host).length, host.all().find(e => e.tagName === 'P').textContent], [0, 'ai.chat.model.nomatch'], 'no match says so');
search().value = ''; search().oninput();
only().checked = true; only().onchange();
eq(rows(host).length, 4, 'the tools filter hides the model without tools');
eq(rows(host).some(r => r.textContent.includes('acme/mini')), false, 'it is the one without tools');
eq(rows(host).some(r => r.textContent.includes('Newcomer')), true, 'unknown support stays visible');
only().checked = false; only().onchange();
const newcomer = () => rows(host).find(r => r.textContent.includes('beta/new'));
rowInput(newcomer()).checked = true; rowInput(newcomer()).onchange();
eq([...o.sel], ['acme/big', 'beta/new'], 'ticking adds to the selection');
eq([o.defId, o.changes], ['acme/big', 1], 'the default stays and the caller is told');
const defRadio = r => r.children[1].children[0];
eq(defRadio(newcomer()).disabled, false, 'the default radio of a ticked model is on');
defRadio(newcomer()).checked = true; defRadio(newcomer()).onchange();
eq(o.defId, 'beta/new', 'the default is chosen with the radio');
const big = () => rows(host).find(r => r.textContent.includes('acme/big'));
rowInput(big()).checked = false; rowInput(big()).onchange();
eq([...o.sel, o.defId], ['beta/new', 'beta/new'], 'unticking leaves the default valid');
eq(defRadio(big()).disabled, true, 'the default radio of an unticked model is off');

const one = {models, multi:false, one:'acme/big', changes:0, onChange(){ this.changes++; }};
const host2 = new El('div');
aiModelPicker(host2, one);
eq(rows(host2).map(r => rowInput(r).type), Array(5).fill('radio'), 'the chat list is single choice');
eq(rows(host2).map(r => r.children.length), Array(5).fill(1), 'no default column in the chat list');
rowInput(rows(host2)[2]).checked = true; rowInput(rows(host2)[2]).onchange();
eq([one.one, one.changes], ['beta/new', 1], 'a pick sets the model and tells the caller');
`)
}

// TestAssistantWidgetChatModelsFromStatus checks the model choice of the chat: the list is the allowed models of the status only,
// each gets price, context and tools from the catalog when this browser has it and from the status when that carries them; the
// catalog is requested only for an administrator who may open the integrations and only when the cached one is old.
func TestAssistantWidgetChatModelsFromStatus(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiToggle"), "aiCatalogEnsure();", "opening the panel reads the catalog when it may")
	must(t, assistantFn(html, "aiPaintModels"), "AI.mp.state.models=list; AI.mp.state.one=m;", "the chat list is the shared picker over the status models")
	must(t, assistantFn(html, "aiCanCatalog"), "meIsAdmin()&&pageAllowed(ME,'integrations')", "the catalog needs the integrations page of an administrator")
	funcs := []string{"aiModelNorm", "aiModelsMerge", "aiCatalogSet", "aiCatalogFresh", "aiChatModels", "aiCanCatalog", "aiCatalogEnsure"}
	prelude := `
const AI_CAT = {};
let ME = null, ADMIN = false, PAGES = false;
const meIsAdmin = () => ADMIN, pageAllowed = () => PAGES;
const AI = {gen:1, on:true, status:{provider:'openrouter', models:['acme/big', 'x/own', 'acme/gone'], model_info:[
  {id:'acme/big', name:'', context:0, input_per_m:4, output_per_m:20, tools:null, price_unknown:false},
  {id:'x/own', name:'Own', context:32000, input_per_m:1, output_per_m:2, tools:true, price_unknown:false},
  {id:'acme/gone', name:'', context:0, input_per_m:null, output_per_m:null, tools:null, price_unknown:true}]}};
const loads = []; let painted = 0;
async function aiCatalogLoad(p, refresh){ loads.push([p, !!refresh]); return aiCatalogSet(p, [{id:'acme/big', name:'Big', context:200000, input_per_m:3, output_per_m:15, tools:true}, {id:'acme/other', tools:false}], '2026-11-16T10:00:00Z', 3600); }
function aiPaintModels(){ painted++; }
`
	assistantNode(t, funcs, prelude, `
eq(aiChatModels(AI.status).map(m => [m.id, m.name, m.input_per_m, m.context, m.tools]), [['acme/big', '', 4, 0, null], ['x/own', 'Own', 1, 32000, true], ['acme/gone', '', null, 0, null]], 'without a catalog the model_info of the status gives the data, for every user');
eq(aiChatModels({provider:'openrouter', models:['plain']}).map(m => m.id), ['plain'], 'a status without model_info still lists the models');
eq(aiCanCatalog(), false, 'no session, no catalog');
ME = {}; ADMIN = true; PAGES = false;
eq(aiCanCatalog(), false, 'an administrator without the integrations page');
ADMIN = false; PAGES = true;
eq(aiCanCatalog(), false, 'a user who is no administrator');
await aiCatalogEnsure();
eq([loads.length, painted], [0, 0], 'no catalog request without the right');
ADMIN = true;
eq(aiCanCatalog(), true, 'an administrator with the integrations page');
await aiCatalogEnsure();
eq([loads, painted], [[['openrouter', false]], 1], 'the catalog is read once and the list redrawn');
await aiCatalogEnsure();
eq(loads.length, 1, 'a fresh catalog is not read again');
eq(aiChatModels(AI.status).map(m => [m.id, m.name, m.input_per_m, m.output_per_m, m.context, m.tools]), [['acme/big', 'Big', 4, 20, 200000, true], ['x/own', 'Own', 1, 2, 32000, true], ['acme/gone', '', null, null, 0, null]], 'the status wins where it knows, the catalog fills the rest, the list stays the allowed one');
eq(aiChatModels({provider:'openrouter'}), [], 'no models in the status');
AI.status.provider = 'gemini';
await aiCatalogEnsure();
eq(loads.length, 2, 'another provider is another catalog');
`)
}

// TestAssistantWidgetPickerEnterAndEscape checks the keys of the model list: Enter in the search never submits the chat form and
// takes the first model that can be taken; Escape closes an open list first and the panel only when no list is open.
func TestAssistantWidgetPickerEnterAndEscape(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiMountPanel"), "q('#ai-form',p).onsubmit=e=>{e.preventDefault(); aiSend()}", "the form is handled by the script")
	funcs := []string{"aiEl", "aiVal", "aiUSD", "aiModelNorm", "aiModelsMerge", "aiModelFilter", "aiModelPickable", "aiModelDefault", "aiCtxText", "aiModelMeta", "aiModelPicker"}
	prelude := assistantFakeDOM + `const AI = {tick:0};`
	assistantNode(t, funcs, prelude, `
const models = aiModelsMerge([{id:'acme/mini', tools:false}, {id:'acme/big', name:'Big', tools:true}, {id:'beta/new', tools:null}, {id:'acme/big2', tools:true}], []);
const key = (k) => { const e = {key:k, prevented:false, preventDefault(){ this.prevented = true; }}; return e; };
const search = host => qa('input', host).find(e => e.type === 'search');

const one = {models, multi:false, one:'', changes:0, onChange(){ this.changes++; }};
const host = new El('div'); aiModelPicker(host, one);
let e = key('Enter'); search(host).onkeydown(e);
eq([e.prevented, one.one, one.changes], [true, 'acme/big', 1], 'Enter takes the first model that has tools, never the form');
search(host).value = 'mini'; search(host).oninput();
e = key('Enter'); search(host).onkeydown(e);
eq([e.prevented, one.one, one.changes], [true, 'acme/big', 1], 'a model without tools is not taken, the form is still not sent');
search(host).value = 'beta'; search(host).oninput();
e = key('Enter'); search(host).onkeydown(e);
eq([one.one, one.changes], ['beta/new', 2], 'a filtered list gives its first row');
e = key('a'); search(host).onkeydown(e);
eq(e.prevented, false, 'other keys are left alone');

const many = {models, multi:true, withDefault:true, sel:new Set(['acme/big']), defId:'acme/big', changes:0, onChange(){ this.changes++; }};
const host2 = new El('div'); aiModelPicker(host2, many);
e = key('Enter'); search(host2).onkeydown(e);
eq([[...many.sel], many.defId, many.changes], [['acme/big', 'beta/new'], 'acme/big', 1], 'in a ticking list Enter ticks the first model not ticked yet');
`)
	assistantNode(t, []string{"aiEscape"}, `
const AI = {mpOpen:true, open:true};
const log = [];
const aiModelSet = on => { AI.mpOpen = on; log.push('list:' + on); };
const aiToggle = (open, focus) => { AI.open = open; log.push('panel:' + open + ':' + focus); };
const btn = {focus(){ log.push('focus'); }};
const q = sel => sel === '#ai-chat-model' ? btn : null;
`, `
aiEscape();
eq([AI.mpOpen, AI.open, log], [false, true, ['list:false', 'focus']], 'the first Esc closes the list and keeps the panel');
aiEscape();
eq([AI.open, log[log.length - 1]], [false, 'panel:false:true'], 'the next Esc closes the panel and returns focus');
`)
}

// jsScan returns the text of every string literal of JS source outside comments, and the literal text of every template literal
// with its ${...} parts cut out (those are scanned as code of their own). src is the source.
func jsScan(src string) (strs, tmpls []string) {
	var scan func(i int, stopAtBrace bool) int
	scan = func(i int, stopAtBrace bool) int {
		depth := 0
		for i < len(src) {
			c := src[i]
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				end := strings.Index(src[i+2:], "*/")
				if end < 0 {
					return len(src)
				}
				i += end + 4
			case c == '\'' || c == '"':
				j := i + 1
				for j < len(src) && src[j] != c && src[j] != '\n' {
					if src[j] == '\\' {
						j++
					}
					j++
				}
				if j >= len(src) {
					return len(src)
				}
				strs = append(strs, src[i+1:j])
				i = j + 1
			case c == '`':
				var text strings.Builder
				j := i + 1
				for j < len(src) && src[j] != '`' {
					switch {
					case src[j] == '\\':
						j += 2
					case src[j] == '$' && j+1 < len(src) && src[j+1] == '{':
						j = scan(j+2, true)
					default:
						text.WriteByte(src[j])
						j++
					}
				}
				tmpls = append(tmpls, text.String())
				i = j + 1
			case c == '{' && stopAtBrace:
				depth++
				i++
			case c == '}' && stopAtBrace:
				if depth == 0 {
					return i + 1
				}
				depth--
				i++
			default:
				i++
			}
		}
		return i
	}
	scan(0, false)
	return strs, tmpls
}

// TestAssistantWidgetNoHardcodedStrings scans the code of the assistant (the settings page, the model list, the error texts and the
// chat widget) for words a user can read that bypass tr: a string literal with Cyrillic, one with two words that are not CSS class
// names, or text between the tags of a markup template. Dictionary entries are outside the scanned code.
func TestAssistantWidgetNoHardcodedStrings(t *testing.T) {
	html := spaSource()
	i := strings.Index(html, "/** AI_PROVIDERS lists the providers")
	j := strings.Index(html, "/** NC_CONTACT_FIELDS lists")
	if i < 0 || j < i {
		t.Fatal("the assistant code block is not in the SPA")
	}
	code := html[i:j]
	classes := map[string]struct{}{}
	css := html[:strings.Index(html, "</style>")]
	for _, m := range regexp.MustCompile(`\.([A-Za-z_][\w-]*)`).FindAllStringSubmatch(css, -1) {
		classes[m[1]] = struct{}{}
	}
	letters := regexp.MustCompile(`\p{L}`)
	cyrillic := regexp.MustCompile(`\p{Cyrillic}`)
	tag := regexp.MustCompile(`(?s)<[^>]*>`)
	attr := regexp.MustCompile(`[\w:-]+="[^"]*"`)
	strs, tmpls := jsScan(code)
	if len(strs) < 300 || len(tmpls) < 10 {
		t.Fatalf("the scanner found too few literals (%d strings, %d templates): it lost its way", len(strs), len(tmpls))
	}
	for _, s := range strs {
		if cyrillic.MatchString(s) {
			t.Errorf("string literal with Cyrillic outside the dictionary: %q", s)
			continue
		}
		words := 0
		known := true
		for _, w := range strings.Fields(s) {
			if !letters.MatchString(w) {
				continue
			}
			words++
			if _, ok := classes[w]; !ok {
				known = false
			}
		}
		if words >= 2 && !known {
			t.Errorf("string literal with words that bypasses tr: %q", s)
		}
	}
	for _, s := range tmpls {
		s = attr.ReplaceAllString(tag.ReplaceAllString(s, " "), " ")
		if text := strings.TrimSpace(s); letters.MatchString(text) {
			t.Errorf("markup template with text between the tags that bypasses tr: %q", text)
		}
	}
}

// TestAssistantWidgetModelListClosing checks that the chat shows one list at a time: opening the model list closes the history and
// clears the search of the list, opening the history, closing the panel, choosing a model and a route change close the list.
func TestAssistantWidgetModelListClosing(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiHistShow"), "if(on) aiModelSet(false);", "the history closes the model list")
	must(t, assistantFn(html, "aiToggle"), "if(!AI.open) aiModelSet(false);", "closing the panel closes the model list")
	must(t, assistantFn(html, "aiEscape"), "aiModelSet(false);", "Escape closes the model list through the one function")
	must(t, assistantFn(html, "aiMountPanel"), "q('#ai-chat-model',p).onclick=()=>aiModelSet(!AI.mpOpen);", "the model button toggles the list through the one function")
	must(t, assistantFn(html, "aiMountPanel"), "AI.model=state.one; aiModelSet(false);", "a chosen model closes the list")
	must(t, html, "window.addEventListener('hashchange',()=>aiModelSet(false));", "a route change closes the model list")
	assistantNode(t, []string{"aiModelSet"}, `
const AI = {hist:true, mpOpen:false, mp:{picker:{reset(){ log.push('reset'); }}}};
const log = [];
const aiHistShow = on => { AI.hist = on; log.push('hist:' + on); };
const aiModelOpen = () => log.push('list:' + AI.mpOpen);
`, `
aiModelSet(true);
eq([AI.hist, AI.mpOpen, log], [false, true, ['hist:false', 'reset', 'list:true']], 'opening the list closes the history and clears the search');
log.length = 0;
aiModelSet(false);
eq([AI.hist, AI.mpOpen, log], [false, false, ['list:false']], 'closing the list leaves the history alone and clears nothing');
aiModelSet(true);
eq(log.filter(x => x.startsWith('hist')), [], 'no history to close: no call');
`)
}

// TestAssistantWidgetPickerResetsSearch checks that a reopened model list starts with every model: reset clears the search text, the
// tools filter and the rows they cut.
func TestAssistantWidgetPickerResetsSearch(t *testing.T) {
	funcs := []string{"aiEl", "aiVal", "aiUSD", "aiModelNorm", "aiModelsMerge", "aiModelFilter", "aiModelPickable", "aiModelDefault", "aiCtxText", "aiModelMeta", "aiModelPicker"}
	assistantNode(t, funcs, assistantFakeDOM+`const AI = {tick:0};`, `
const models = aiModelsMerge([{id:'acme/mini', tools:false}, {id:'acme/big', name:'Big', tools:true}, {id:'beta/new', tools:true}], []);
const host = new El('div');
const picker = aiModelPicker(host, {models, multi:false, one:'', onChange(){}});
const search = qa('input', host).find(e => e.type === 'search');
const rows = () => qa('input', host).filter(e => e.type === 'radio').length;
eq(rows(), 3, 'every model before a search');
search.value = 'beta'; search.oninput();
eq(rows(), 1, 'the search cuts the list');
picker.reset();
eq([search.value, rows()], ['', 3], 'reset clears the search and shows every model');
const only = qa('input', host).find(e => e.type === 'checkbox');
only.checked = true; only.onchange();
eq(rows(), 2, 'the tools filter cuts the list');
picker.reset();
eq([only.checked, rows()], [false, 3], 'reset clears the tools filter');
`)
}

// TestAssistantWidgetDictationMarkup checks that the dictation button is built only when the browser has speech recognition, sits
// in the row under the input with a dictionary label in en, ru and uk, and is released on send, close, language change and unmount.
func TestAssistantWidgetDictationMarkup(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiShellHTML"), "${aiMicHTML()}", "the shell takes the button from aiMicHTML")
	must(t, assistantFn(html, "aiShellHTML"), "${aiMicLineHTML()}", "the shell takes the waves line from aiMicLineHTML")
	mic := assistantFn(html, "aiMicHTML")
	must(t, mic, "if(!aiSpeechCtor()&&!aiLocalCan()) return '';", "no API and no recorder, no button")
	must(t, mic, `id="ai-mic"`, "the button id")
	must(t, mic, "aria-pressed", "the listening state is exposed")
	if strings.Count(mic, "<button") != 1 {
		t.Errorf("aiMicHTML has %d buttons, want exactly one: stop replaces the microphone", strings.Count(mic, "<button"))
	}
	line := assistantFn(html, "aiMicLineHTML")
	must(t, line, "if(!aiSpeechCtor()&&!aiLocalCan()) return '';", "no API and no recorder, no waves line")
	must(t, line, `id="ai-wave"`, "the waves are in the markup")
	must(t, line, "'<i></i>'.repeat(AI_WAVE_BARS)", "the waves are bars")
	must(t, line, `id="ai-mic-note"`, "the note of the line")
	paint := assistantFn(html, "aiMicPaint")
	must(t, paint, "b.innerHTML=icon(on?'square':'mic')", "stop replaces the microphone in the same button")
	must(t, paint, "w.hidden=!on", "the waves show only while listening")
	run := assistantFn(html, "aiMicRun")
	must(t, run, "r.continuous=true; r.interimResults=true;", "the recognition is continuous with drafts")
	must(t, run, "try{aiMicRun(s);", "the end of a browser session starts the next one until stop")
	wave := assistantFn(html, "aiWaveStart")
	must(t, wave, "getUserMedia({audio:true})", "the analyser listens to the microphone")
	must(t, wave, "createAnalyser()", "the bars follow the loudness")
	must(t, wave, "prefers-reduced-motion:reduce", "the bars stay still on request")
	must(t, html, "@media (prefers-reduced-motion:reduce){.ai-wave i{", "static bars in CSS for reduced motion")
	if i := strings.Index(html, "\n  .ai-mic-note{"); i < 0 || strings.Contains(html[i:i+strings.Index(html[i+1:], "\n")+1], "nowrap") {
		t.Error("the note of the dictation must not be cut to one line")
	}
	must(t, assistantFn(html, "aiSpeechCtor"), "window.SpeechRecognition||window.webkitSpeechRecognition", "both constructors are tried")
	must(t, assistantFn(html, "aiMountPanel"), "const mic=q('#ai-mic',p); if(mic) mic.onclick=aiMicToggle;", "the button is wired only when present")
	must(t, assistantFn(html, "aiSend"), "aiMicAbort();", "sending stops the dictation")
	must(t, assistantFn(html, "aiToggle"), "if(!AI.open) aiMicAbort();", "closing the panel stops the dictation")
	must(t, assistantFn(html, "aiUnmount"), "aiMicAbort();", "unmounting stops the dictation")
	must(t, assistantFn(html, "aiSync"), "if(AI.lang!==uiLang()){aiMicAbort();", "a language change stops the dictation")
	for _, key := range []string{"ai.chat.mic", "ai.chat.mic.stop", "ai.chat.mic.on", "ai.chat.mic.denied", "ai.chat.mic.none", "ai.chat.mic.err", "ai.chat.mic.local.load", "ai.chat.mic.local.work", "ai.chat.mic.local.fail", "ai.chat.mic.local.none", "ai.chat.mic.blocked"} {
		if n := strings.Count(html, "'"+key+"':'"); n != 3 {
			t.Errorf("dictionary key %s is in %d languages, want 3", key, n)
		}
	}
}

// TestAssistantWidgetDictation runs the dictation under node with a fake recogniser: the language follows the interface, a confirmed
// phrase is appended after a space with the cursor at the end and an input event, a draft stays out of the field, the button turns into
// stop and back, the end of a browser session starts the next recogniser until stop, a failure is shown in full only when the session
// brought no phrase, the bars move and follow the analyser, and nothing is sent.
func TestAssistantWidgetDictation(t *testing.T) {
	html := spaSource()
	consts := regexp.MustCompile(`(?m)^const AI_(?:SPEECH_(?:LANG|ERR|LOCAL)|ASR_(?:LIB|LANG)|WAVE_BARS)=.*$`).FindAllString(html, -1)
	if len(consts) != 6 {
		t.Fatalf("found %d dictation constants, want 6", len(consts))
	}
	prelude := strings.Join(consts, "\n") + `
const esc = s => s, icon = k => '<svg ' + k + '>', tr = k => k;
let LANG = 'ru';
const uiLang = () => LANG;
const mkEl = () => { const el = {hidden:true, textContent:'', innerHTML:'', title:'', attrs:{}, cls:{}, style:{height:''}, setAttribute(k, v){ el.attrs[k] = v; }};
  el.classList = {toggle:(c, on) => { el.cls[c] = !!on; }}; return el; };
const mic = mkEl(), note = mkEl(), line = mkEl(), wave = mkEl(), bars = [mkEl(), mkEl(), mkEl()];
let inputs = 0, caret = -1;
const ta = {value:'', maxLength:4000, setSelectionRange(a){ caret = a; }, dispatchEvent(){ inputs++; }, focus(){ document.activeElement = ta; }};
const document = {activeElement:null};
const q = sel => ({'#ai-mic':mic, '#ai-mic-note':note, '#ai-mic-line':line, '#ai-wave':wave, '#ai-text':ta})[sel] || null;
const qa = sel => sel === '#ai-wave>i' ? bars : [];
const AI = {rec:null};
let made = [], failStart = false, frames = [], cancelled = [], RM = false;
const matchMedia = () => ({matches:RM});
const requestAnimationFrame = f => { frames.push(f); return frames.length; };
const cancelAnimationFrame = id => { cancelled.push(id); };
class FakeRec { constructor(){ made.push(this); this.calls = []; } start(){ if(failStart) throw new Error('refused'); this.calls.push('start'); } stop(){ this.calls.push('stop'); } abort(){ this.calls.push('abort'); } }
var window = {};
var navigator = {};
`
	assistantNode(t, []string{"aiSpeechCtor", "aiLocalCan", "aiLocalPreferred", "aiMicHTML", "aiMicLineHTML", "aiMicPaint", "aiDictAppend", "aiWaveStop", "aiWaveStart", "aiLocalRelease", "aiMicAbort", "aiMicFinish", "aiMicRun", "aiMicToggle"}, prelude, `
const res = (text, isFinal) => ({resultIndex:0, results:[{isFinal, 0:{transcript:text}}]});
eq([aiMicHTML(), aiMicLineHTML()], ['', ''], 'no API, no button and no waves');
window = {webkitSpeechRecognition:FakeRec};
eq(aiMicHTML().includes('id="ai-mic"'), true, 'the API gives the button');
eq([aiMicHTML().split('<button').length - 1, aiMicLineHTML().includes('<i></i>'.repeat(9)), aiMicLineHTML().includes('id="ai-wave"')], [1, true, true], 'one button and the bars in the line');
window = {SpeechRecognition:FakeRec};
eq(aiSpeechCtor() === FakeRec, true, 'the standard constructor is found');

for (const [lang, tag] of [['ru', 'ru-RU'], ['uk', 'uk-UA'], ['en', 'en-US']]) {
  LANG = lang; made = [];
  aiMicToggle();
  eq([made[0].lang, made[0].calls], [tag, ['start']], 'recognition language for ' + lang);
  aiMicAbort();
}

LANG = 'ru'; made = []; ta.value = '';
aiMicToggle();
const r = made[0];
eq([r.continuous, r.interimResults, mic.cls.rec, mic.attrs['aria-pressed'], mic.attrs['aria-label']], [true, true, true, 'true', 'ai.chat.mic.stop'], 'the button shows listening');
eq([mic.innerHTML, wave.hidden, line.hidden, note.hidden], ['<svg square>', false, false, true], 'stop replaces the microphone and the waves show');
eq(made.length, 1, 'one click makes one recogniser');
r.onresult(res('привет', false));
eq([ta.value, note.textContent, inputs], ['', 'привет', 0], 'a draft stays out of the field');
r.onresult(res(' привет', true));
eq([ta.value, inputs, caret], ['привет', 1, 6], 'the first phrase is the whole text');
r.onresult(res('мир', true));
eq([ta.value, inputs, caret], ['привет мир', 2, 10], 'the next phrase follows after a space');
ta.value = 'a '; aiDictAppend(ta, 'b');
eq(ta.value, 'a b', 'no second space after a space');
ta.value = 'x'.repeat(3999); aiDictAppend(ta, 'yz');
eq(ta.value.length, 4000, 'the text is cut to maxlength');
ta.value = 'keep'; aiDictAppend(ta, '   ');
eq(ta.value, 'keep', 'an empty phrase changes nothing');

aiMicToggle();
eq([r.calls, mic.cls.rec, mic.attrs['aria-pressed']], [['start', 'stop'], false, 'false'], 'a second click stops and the button is idle');
eq([mic.innerHTML, wave.hidden, line.hidden], ['<svg mic>', true, true], 'the microphone is back and the waves are gone');
r.onresult(res('хвост', true));
eq(ta.value, 'keep хвост', 'the last phrase still arrives after stop');
document.activeElement = mic;
r.onend();
eq([AI.rec, document.activeElement === ta], [null, true], 'the end frees the state and returns focus to the field');

// The end of a browser session starts the next recogniser while the user has not pressed stop.
made = []; ta.value = ''; aiMicToggle();
const s1 = AI.rec;
made[0].onresult(res('раз', true));
made[0].onend();
eq([made.length, made[1].calls, made[1].continuous, AI.rec === s1, mic.cls.rec, wave.hidden], [2, ['start'], true, true, true, false], 'the end without stop restarts the recogniser and keeps listening');
made[1].onresult(res('два', true));
eq(ta.value, 'раз два', 'a phrase of the next recogniser is appended too');
made[1].onend();
eq(made.length, 3, 'and again');
aiMicToggle();
made[2].onend();
eq([AI.rec, made.length, mic.cls.rec], [null, 3, false], 'after stop the end does not restart');

// A refusal or a missing microphone, no phrase in the session: the full text of the failure, no restart.
const failures = [['not-allowed', 'ai.chat.mic.denied'], ['audio-capture', 'ai.chat.mic.none'], ['language-not-supported', 'ai.chat.mic.err']];
for (const [code, key] of failures) {
  made = []; aiMicToggle();
  made[0].onerror({error:code}); made[0].onend();
  eq([AI.rec, made.length, note.textContent, note.cls.bad, note.hidden, line.hidden, wave.hidden, mic.innerHTML], [null, 1, key, true, false, false, true, '<svg mic>'], 'failure ' + code);
}

// A phrase in the session hides the failure that ends it.
made = []; aiMicToggle();
made[0].onresult(res('слово', true));
made[0].onerror({error:'audio-capture'}); made[0].onend();
eq([AI.rec, note.hidden, line.hidden], [null, true, true], 'a failure after a phrase shows no failure text');

// Silence restarts; stop after silence only is an empty result, stop without any error shows nothing.
made = []; aiMicToggle();
made[0].onerror({error:'no-speech'}); made[0].onend();
eq([made.length, AI.rec !== null, note.hidden], [2, true, true], 'silence restarts and shows no text');
made[1].onerror({error:'no-speech'}); aiMicToggle(); made[1].onend();
eq([AI.rec, note.textContent, note.cls.bad], [null, 'ai.chat.mic.err', true], 'stop after silence only says the speech was not recognised');
made = []; aiMicToggle(); aiMicToggle(); made[0].onend();
eq([AI.rec, note.hidden, line.hidden], [null, true, true], 'stop with no error shows nothing');

// Quick empty ends do not loop for ever.
made = []; aiMicToggle();
for (let i = 0; i < 4 && AI.rec; i++) made[made.length - 1].onend();
eq([AI.rec, made.length, note.textContent], [null, 4, 'ai.chat.mic.err'], 'a quick series of ends closes the session');

// A browser that refuses to start.
made = []; failStart = true; aiMicToggle(); failStart = false;
eq([AI.rec, note.textContent, note.cls.bad, mic.cls.rec], [null, 'ai.chat.mic.err', true, false], 'a refused start is a failure text');

// A click on the microphone while the previous dictation finishes starts a new one.
made = []; aiMicToggle(); aiMicToggle();
const old = made[0];
aiMicToggle();
eq([old.calls, made.length, mic.cls.rec], [['start', 'stop', 'abort'], 2, true], 'a new dictation does not wait for the old end');
aiMicAbort();

// The bars: a smooth movement without an analyser, still with reduced motion, loudness with one.
made = []; frames = []; cancelled = []; bars.forEach(b => { b.style.height = ''; });
aiMicToggle();
eq(frames.length, 1, 'the animation starts with the listening');
frames[0](0); frames[1](400);
const hs = bars.map(b => parseFloat(b.style.height));
eq([hs.every(h => h >= 4 && h <= 22), new Set(hs).size > 1, String(bars[0].style.height).endsWith('px')], [true, true, true], 'without an analyser the bars still move');
aiMicToggle();
eq([cancelled.length > 0, bars[0].style.height], [true, ''], 'stop ends the animation and resets the bars');
made[0].onend();
RM = true; frames = []; made = []; aiMicToggle();
eq([frames.length, wave.hidden], [0, false], 'reduced motion: no running animation, the waves are still shown');
aiMicAbort(); RM = false;

let tracks = 0, closed = 0;
class FakeCtx { constructor(){ this.state = 'running'; } createAnalyser(){ this.an = {frequencyBinCount:128, getByteFrequencyData(b){ b.fill(255); }}; return this.an; } createMediaStreamSource(){ return {connect(){}}; } close(){ closed++; } }
window = {SpeechRecognition:FakeRec, AudioContext:FakeCtx};
navigator = {mediaDevices:{getUserMedia: async () => ({getTracks: () => [{stop(){ tracks++; }}]})}};
made = []; frames = []; aiMicToggle();
await new Promise(r => setTimeout(r, 0));
for (let i = 0; i < 40; i++) frames[frames.length - 1](i * 16);
eq(bars.every(b => parseFloat(b.style.height) > 20), true, 'a loud microphone lifts every bar');
aiMicToggle();
eq([tracks, closed], [1, 1], 'stop releases the microphone stream and the audio context');
made[0].onend();
made = []; aiMicToggle(); aiMicToggle();
await new Promise(r => setTimeout(r, 0));
eq(tracks, 2, 'a stream granted after stop is released at once');
made[0].onend();

made = []; aiMicToggle();
const a = made[0];
aiMicAbort();
eq([a.calls, a.onresult, a.onend, AI.rec], [['start', 'abort'], null, null, null], 'abort drops the recogniser and its handlers');
`)
}

// TestAssistantWidgetDictationLocal runs under node the fallback of a browser without its own speech service (the embedded Electron
// browser answers network): no text about the internet, the dictation goes on with the recognition inside the page, the model loads with
// a status that is not a failure, the recording becomes text in the language of the interface, the browser is remembered, and a model
// that does not load or a browser that cannot record have their own texts.
func TestAssistantWidgetDictationLocal(t *testing.T) {
	html := spaSource()
	mustNot(t, html, "ai.chat.mic.net", "no message about the network of the speech service")
	for _, s := range []string{"Check your internet", "Проверьте интернет", "Перевірте інтернет"} {
		if i := strings.Index(html, s); i >= 0 && strings.Contains(html[max(0, i-200):i], "'ai.chat.mic") {
			t.Errorf("a dictation text still says %q", s)
		}
	}
	must(t, html, "const AI_SPEECH_LOCAL=new Set(['network','service-not-allowed']);", "the codes that switch to the local recognition")
	must(t, assistantFn(html, "aiAsrLoad"), "import(AI_ASR_LIB)", "the library loads only when the dictation needs it")
	must(t, html, "AI_ASR_LIB='https://cdn.jsdelivr.net/npm/@huggingface/transformers@3.8.1/", "a pinned library version")
	consts := regexp.MustCompile(`(?m)^const AI_(?:SPEECH_(?:LANG|ERR|LOCAL)|ASR_(?:LIB|LANG|SILENCE)|MIC_BLOCKED|WAVE_BARS)=.*$`).FindAllString(html, -1)
	prelude := strings.Join(consts, "\n") + `
const esc = s => s, icon = k => '<svg ' + k + '>', tr = k => k, trFill = (k, m) => k + ':' + m.pct;
let LANG = 'ru';
const uiLang = () => LANG;
const mkEl = () => { const el = {hidden:true, textContent:'', innerHTML:'', title:'', attrs:{}, cls:{}, style:{height:''}, setAttribute(k, v){ el.attrs[k] = v; }};
  el.classList = {toggle:(c, on) => { el.cls[c] = !!on; }}; return el; };
const mic = mkEl(), note = mkEl(), line = mkEl(), wave = mkEl();
const ta = {value:'', maxLength:4000, setSelectionRange(){}, dispatchEvent(){}, focus(){}};
const document = {activeElement:null};
const q = sel => ({'#ai-mic':mic, '#ai-mic-note':note, '#ai-mic-line':line, '#ai-wave':wave, '#ai-text':ta})[sel] || null;
const qa = () => [];
const AI = {rec:null, asr:null, asrReady:false, asrPct:0, asrFiles:{}};
const matchMedia = () => ({matches:true});
const store = {};
var localStorage = {getItem: k => k in store ? store[k] : null, setItem: (k, v) => { store[k] = String(v); }};
let made = [], recs = [], tracks = 0, loads = 0, seen = null, model = null, modelFail = false;
class FakeRec { constructor(){ made.push(this); } start(){} stop(){} abort(){} }
class FakeMR { constructor(){ this.state = 'inactive'; this.mimeType = 'audio/webm'; recs.push(this); } start(){ this.state = 'recording'; } stop(){ this.state = 'inactive'; if (this.onstop) this.onstop(); } }
let trackState = 'live';
const stream = () => { const tk = {readyState:trackState, stop(){ tracks++; }}; return {getTracks: () => [tk], getAudioTracks: () => [tk]}; };
var window = {SpeechRecognition:FakeRec, MediaRecorder:FakeMR, AudioContext:function(){}};
var navigator = {mediaDevices:{getUserMedia: async () => stream()}};
const MediaRecorder = FakeMR;
let release = null;
function aiAsrLoad(){ loads++; if (modelFail) return Promise.reject(new Error('offline')); if (!model) model = new Promise(r => { release = () => { AI.asrReady = true; r((audio, opts) => { seen = opts; return {text:' привет мир '}; }); }; }); return model; }
let level = 0.5;
async function aiAsrAudio(){ return new Float32Array(16).fill(level); }
`
	assistantNode(t, []string{"aiSpeechCtor", "aiLocalCan", "aiLocalPreferred", "aiMicPaint", "aiDictAppend", "aiWaveStop", "aiWaveStart",
		"aiAsrProgress", "aiAsrNote", "aiAsrHeard", "aiLocalRelease", "aiLocalStart", "aiLocalDone", "aiMicAbort", "aiMicFinish", "aiMicRun", "aiMicToggle"}, prelude, `
const tick = () => new Promise(r => setTimeout(r, 0));
aiMicToggle();
eq(made.length, 1, 'a browser with SpeechRecognition tries its service first');
made[0].onerror({error:'network'}); made[0].onend();
eq([AI.rec && AI.rec.local, note.textContent, note.cls.bad, mic.cls.rec, store['emtds-asr-local']], [true, 'ai.chat.mic.local.load:0', false, true, '1'], 'network moves to the local recognition with a load status, not a failure');
await tick();
eq([recs.length, recs[0].state], [1, 'recording'], 'the microphone is recorded in the page');
aiAsrProgress({status:'progress', file:'a', loaded:30, total:100});
aiAsrProgress({status:'progress', file:'b', loaded:70, total:100});
eq(note.textContent, 'ai.chat.mic.local.load:50', 'the status sums the model files');
release(); await tick();
eq([note.hidden, mic.cls.rec], [true, true], 'a ready model hides the status while the user speaks');
recs[0].ondataavailable({data:new Blob(['voice'])});
aiMicToggle();
await tick(); await tick();
eq([ta.value, seen && seen.language, seen && seen.task, AI.rec, note.hidden, mic.cls.rec, tracks], ['привет мир', 'russian', 'transcribe', null, true, false, 1], 'stop turns the recording into text in the interface language and frees the microphone');

made = []; aiMicToggle();
eq([made.length, AI.rec && AI.rec.local], [0, true], 'the remembered browser goes straight to the local recognition');
await tick();
aiMicAbort(); await tick();
eq([AI.rec, tracks, ta.value], [null, 2, 'привет мир'], 'abort frees the microphone and takes no text');

AI.asrReady = false; model = null; modelFail = true; aiMicToggle(); await tick();
recs[recs.length - 1].ondataavailable({data:new Blob(['voice'])});
aiMicToggle(); await tick(); await tick();
eq([note.textContent, note.cls.bad], ['ai.chat.mic.local.fail', true], 'a model that does not load has its own text');
modelFail = false;

delete store['emtds-asr-local']; window = {SpeechRecognition:FakeRec}; made = [];
aiMicToggle(); made[0].onerror({error:'service-not-allowed'}); made[0].onend();
eq([AI.rec, note.textContent, note.cls.bad], [null, 'ai.chat.mic.local.none', true], 'a browser that cannot record says so, not the internet');

window = {SpeechRecognition:FakeRec, MediaRecorder:FakeMR, AudioContext:function(){}}; trackState = 'ended'; const before = tracks;
aiMicToggle(); await tick();
eq([AI.rec, note.textContent, note.cls.bad, tracks - before], [null, 'ai.chat.mic.blocked', true, 1], 'an ended microphone stream (embedded browser) says the browser gives no microphone and frees it');

trackState = 'live'; level = 0.001; seen = null; const textBefore = ta.value;
aiMicToggle(); await tick(); release(); await tick();
recs[recs.length - 1].ondataavailable({data:new Blob(['hum'])});
aiMicToggle(); await tick(); await tick();
eq([AI.rec, ta.value, note.textContent, seen], [null, textBefore, 'ai.chat.mic.err', null], 'a silent recording is not sent to the model, so Whisper invents no phrase');
`)
}

