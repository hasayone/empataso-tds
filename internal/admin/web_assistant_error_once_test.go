package admin

import "testing"

// TestAssistantWidgetErrorOnce checks that a failed turn shows one error bubble: the error event names the stored reply in
// message_id, the reply with that id carries the error text of the event, and the note under the log is drawn only when no
// stored reply holds the error (a cut stream or a refused request).
func TestAssistantWidgetErrorOnce(t *testing.T) {
	html := spaSource()
	must(t, assistantFn(html, "aiOnEvent"), "AI.errMid=d.message_id||'';", "the error event remembers the stored reply")
	must(t, assistantFn(html, "aiSend"), "AI.err=''; AI.errMid='';", "a new turn forgets the last error")
	must(t, assistantFn(html, "aiAddMessage"), "m.id===AI.errMid&&AI.err?AI.err:aiErrText(m.error,'',null)", "the stored reply shows the text of the event")
	must(t, assistantFn(html, "aiPaintLog"), "if(AI.err&&!msgs.some(m=>m.id===AI.errMid&&m.error))", "the note is skipped when the reply holds the error")
}
