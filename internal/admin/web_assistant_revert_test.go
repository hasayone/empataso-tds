package admin

import "testing"

// TestAssistantWidgetChatRevert checks the chat history offers "revert the edits of this chat" only for a chat with
// applied edits, asks with the list of edits newest first, calls POST /api/ai/chats/{id}/revert and tells what was
// reverted, where it stopped and why.
func TestAssistantWidgetChatRevert(t *testing.T) {
	html := spaSource()
	paint := assistantFn(html, "aiPaintHist")
	must(t, paint, "if(((AI.done&&AI.done[c.id])||[]).length){", "the button needs applied edits in the chat")
	must(t, paint, "back.innerHTML=icon('undo');", "the button has the undo icon")
	must(t, paint, "back.onclick=()=>aiRevertChat(c,back);", "the button starts the chat revert")
	must(t, html, `undo:'<path d="M9 14 4 9l5-5"/>`, "the undo icon exists")
	must(t, html, "const IC_UI_ONLY=new Set(['sortoff','sortdesc','sortasc','undo','pgfirst','pgprev','pgnext','pglast']);", "the undo icon is not offered for boards")
	must(t, assistantFn(html, "aiHistLoad"), "call('GET','/api/ai/changes?status=done')", "the applied edits are read with the list")
	run := assistantFn(html, "aiRevertChat")
	must(t, run, "list.map(aiChangeLine).join('\\n')", "the confirmation lists the edits")
	must(t, run, "await ask(", "the revert asks first")
	must(t, run, "call('POST','/api/ai/chats/'+encodeURIComponent(c.id)+'/revert')", "the chat revert endpoint")
	must(t, run, "aiRefreshPage(", "the page is redrawn for the reverted resources")
	for _, k := range []string{"ai.chat.hist.revert", "ai.chat.hist.revert.title", "ai.chat.hist.revert.ask", "ai.chat.hist.revert.line",
		"ai.chat.hist.revert.done", "ai.chat.hist.revert.stop"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}

	assistantNode(t, []string{"aiDoneByChat", "aiChangeLine", "aiRevertChatReport"}, `
const AI_RES_KEYS={offers:'res.offers'}, AI_OP_KEYS={update:'op.update',delete:'op.delete'};
const tr=k=>k, aiVal=v=>v==null||v===''?'—':String(v), uiLocale=()=>'en';
const trFill=(k,v)=>k+JSON.stringify(v);
const aiErrText=(code,msg,params)=>code+':'+(params&&params.reason||'');
`, `
const done=aiDoneByChat([
  {id:'a',chat_id:'c1',status:'done',applied:'2026-11-16T10:00:00Z'},
  {id:'b',chat_id:'c1',status:'done',applied:'2026-11-16T12:00:00Z'},
  {id:'x',chat_id:'c1',status:'reverted',applied:'2026-11-16T13:00:00Z'},
  {id:'p',chat_id:'c1',status:'proposed'},
  {id:'o',chat_id:'c2',status:'done',applied:'2026-11-16T11:00:00Z'}
]);
eq(Object.keys(done).sort(), ['c1','c2'], 'only chats with applied edits');
eq(done.c1.map(c=>c.id), ['b','a'], 'newest applied first, reverted and proposed left out');
eq(aiDoneByChat(undefined), {}, 'no list, no chats');
const line=aiChangeLine({resource:'offers',op:'delete',title:'Promo',applied:''});
eq(line.indexOf('"resource":"res.offers"')>0&&line.indexOf('"op":"op.delete"')>0&&line.indexOf('"title":"Promo"')>0, true, 'resource, action and title');
const list=[{id:'b',resource:'offers',op:'update',title:'B'},{id:'a',resource:'offers',op:'update',title:'A'}];
eq(aiRevertChatReport({reverted:['b','a']},list), {text:'ai.chat.hist.revert.done{"n":2}',bad:false}, 'all reverted');
const stop=aiRevertChatReport({reverted:['c'],stopped_at:'b',remaining:['a'],error_code:'ai_conflict',error_params:{reason:'changed'}},list);
eq(stop.bad, true, 'a stopped run is shown as a problem');
eq(stop.text.indexOf('"n":1')>0&&stop.text.indexOf('"left":2')>0&&stop.text.indexOf('ai_conflict:changed')>0&&stop.text.indexOf('B')>0, true,
  'reverted count, not reverted count, the edit it stopped at and the reason');
`)
}
