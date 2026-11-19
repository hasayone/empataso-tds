package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// aiCompactStub answers a compaction call (recognized by its system prompt) with summary, a
// tool-step call with a long tool preamble for the first steps tool calls, and then with the
// final text. It counts the two kinds of calls apart.
type aiCompactStub struct {
	*aiStub
	steps     int
	compacted int
	// emptyFirst is how many compaction calls are answered with no text before summary is used.
	emptyFirst int
}

// newAICompactStub starts the stub. summary is the compaction answer; steps is how many model
// calls ask for a tool before the final text.
func newAICompactStub(t *testing.T, steps int, summary string) *aiCompactStub {
	t.Helper()
	cs := &aiCompactStub{steps: steps}
	long := strings.Repeat("data ", 4800)
	cs.aiStub = newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		body := string(cs.aiStub.req(n - 1).Body)
		switch {
		case strings.Contains(body, "compact the working memory"):
			cs.compacted++
			if cs.emptyFirst > 0 {
				cs.emptyFirst--
				aiJSONReply(w, http.StatusOK, aiAnthText("   ", 50, 20))
				return
			}
			aiJSONReply(w, http.StatusOK, aiAnthText(summary, 50, 20))
		case cs.steps > 0:
			cs.steps--
			aiJSONReply(w, http.StatusOK, aiAnthTool(long, "tu", "list_offers", `{"limit":1}`))
		default:
			aiJSONReply(w, http.StatusOK, aiAnthText("all done", 30, 5))
		}
	})
	return cs
}

func TestAIClampSteps(t *testing.T) {
	for _, c := range []struct{ stored, want int }{
		{0, aiDefaultMaxSteps}, {-3, aiDefaultMaxSteps}, {aiLegacyMaxSteps, aiDefaultMaxSteps},
		{3, 3}, {7, 7}, {aiMaxStepsCap, aiMaxStepsCap}, {aiMaxStepsCap + 1, aiMaxStepsCap}, {99999, aiMaxStepsCap},
	} {
		if got := aiClampSteps(c.stored); got != c.want {
			t.Errorf("aiClampSteps(%d) = %d, want %d", c.stored, got, c.want)
		}
	}
	if aiDefaultMaxSteps < 30 || aiMaxStepsCap < aiDefaultMaxSteps {
		t.Fatalf("the step limit must leave room for real work: default %d, ceiling %d", aiDefaultMaxSteps, aiMaxStepsCap)
	}
}

func TestAIWithoutSummary(t *testing.T) {
	with := aiSummaryOpen + "old summary" + aiSummaryClose + "the request"
	if got := aiWithoutSummary(with); got != "the request" {
		t.Errorf("summary not removed: %q", got)
	}
	if got := aiWithoutSummary("plain request"); got != "plain request" {
		t.Errorf("a request without a summary changed: %q", got)
	}
	if got := aiWithoutSummary(aiSummaryOpen + "no end"); got != aiSummaryOpen+"no end" {
		t.Errorf("an unfinished frame must stay: %q", got)
	}
}

func TestAICompactBase(t *testing.T) {
	msgs := []aiTurnMsg{
		{Role: aiRoleUser, Text: "a"}, {Role: aiRoleAssistant, Text: "b"},
		{Role: aiRoleUser, Text: "c"}, {Role: aiRoleAssistant, Text: "d"}, {Role: aiRoleTool, Text: "e"},
	}
	if got := aiCompactBase(msgs); got != 2 {
		t.Errorf("base %d, want 2", got)
	}
	if got := aiCompactBase(msgs[3:]); got != -1 {
		t.Errorf("no user message: base %d, want -1", got)
	}
}

// TestAssistantTurnPassesTheOldSixStepLimit runs twelve tool steps with no max_steps set: the turn
// must reach its final text instead of stopping at the former limit of six.
func TestAssistantTurnPassesTheOldSixStepLimit(t *testing.T) {
	stub := newAICompactStub(t, 12, "unused")
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, "", 1)
	e := newTurnEnv(t, stub.aiStub, settings)
	chat := e.newChat(t)
	resp := e.send(t, chat, `{"text":"work"}`)
	defer resp.Body.Close()
	events, _ := readSSE(t, resp.Body)
	if last := lastEvent(t, events); last.name != "done" {
		t.Fatalf("events %v", eventNames(events))
	}
	if got := stub.calls() - stub.compacted; got != 13 {
		t.Fatalf("model steps %d, want 12 tool steps and the final one", got)
	}
}

// TestAssistantTurnCompactsItsOwnContext grows the conversation with long steps and checks that the
// turn sums it up by itself, goes on with the same task and finishes without a message to the user.
func TestAssistantTurnCompactsItsOwnContext(t *testing.T) {
	stub := newAICompactStub(t, 5, "SUMMARY-OK found offer o1")
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, `"max_steps":20,"turn_input_tokens":40000,`, 1)
	e := newTurnEnv(t, stub.aiStub, settings)
	chat := e.newChat(t)
	resp := e.send(t, chat, `{"text":"collect the offers"}`)
	defer resp.Body.Close()
	events, raw := readSSE(t, resp.Body)
	if last := lastEvent(t, events); last.name != "done" {
		t.Fatalf("the turn must finish, events %v", eventNames(events))
	}
	if stub.compacted == 0 || stub.compacted > aiMaxCompactions {
		t.Fatalf("compactions %d, want 1..%d", stub.compacted, aiMaxCompactions)
	}
	if !strings.Contains(raw, aiCompactTexts["en"]) {
		t.Error("the stream does not say the context is being compacted")
	}
	if strings.Contains(raw, "/compact") {
		t.Error("the user is asked for a command")
	}
	var after string
	for i := 0; i < stub.calls(); i++ {
		body := string(stub.req(i).Body)
		if strings.Contains(body, "SUMMARY-OK") && !strings.Contains(body, "compact the working memory") {
			after = body
			break
		}
	}
	if after == "" {
		t.Fatal("no model call carried the summary")
	}
	if !strings.Contains(after, "collect the offers") {
		t.Error("the request of the user is lost after the compaction")
	}
	if strings.Count(after, "data data data") > 1 {
		t.Error("the long steps are still in the conversation after the compaction")
	}
	c := e.chatOf(t, chat)
	if c.Status != aiChatIdle || len(c.Messages) != 2 || c.Messages[1].Error != "" {
		t.Fatalf("chat %+v", c)
	}
}

// TestAssistantTurnCompactionKeepsTheCeiling checks that the step ceiling still stops a model that
// never answers, whatever the compactions.
func TestAssistantTurnCompactionKeepsTheCeiling(t *testing.T) {
	stub := newAICompactStub(t, 1000, "SUMMARY-OK")
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, `"max_steps":7,"turn_input_tokens":40000,`, 1)
	e := newTurnEnv(t, stub.aiStub, settings)
	chat := e.newChat(t)
	resp := e.send(t, chat, `{"text":"loop"}`)
	defer resp.Body.Close()
	events, _ := readSSE(t, resp.Body)
	if last := lastEvent(t, events); last.name != "error" || errorCode(t, last) != aiErrStepsLimit {
		t.Fatalf("events %v", eventNames(events))
	}
	if got := stub.calls() - stub.compacted; got != 7 {
		t.Fatalf("model steps %d, want the ceiling of 7", got)
	}
	if stub.compacted > aiMaxCompactions {
		t.Fatalf("compactions %d, want at most %d", stub.compacted, aiMaxCompactions)
	}
}

// aiRunCompactTurn runs one turn of text against stub with the given steps ceiling and an input
// room of 40000 tokens, and returns the events, the chat id and the environment.
func aiRunCompactTurn(t *testing.T, stub *aiCompactStub, maxSteps int) ([]sseEvent, string, *turnEnv) {
	t.Helper()
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, `"max_steps":`+itoa(maxSteps)+`,"turn_input_tokens":40000,`, 1)
	e := newTurnEnv(t, stub.aiStub, settings)
	chat := e.newChat(t)
	resp := e.send(t, chat, `{"text":"loop"}`)
	defer resp.Body.Close()
	events, _ := readSSE(t, resp.Body)
	return events, chat, e
}

// TestAssistantTurnEmptySummaryIsAskedAgain checks that one empty summary does not fail the turn:
// the compaction asks again, with a larger output room, and the second answer is used.
func TestAssistantTurnEmptySummaryIsAskedAgain(t *testing.T) {
	stub := newAICompactStub(t, 5, "SUMMARY-OK found offer o1")
	stub.emptyFirst = 1
	events, chat, e := aiRunCompactTurn(t, stub, 20)
	if last := lastEvent(t, events); last.name != "done" {
		t.Fatalf("events %v", eventNames(events))
	}
	if stub.compacted < 2 {
		t.Fatalf("compaction calls %d, want the empty one and its retry", stub.compacted)
	}
	if c := e.chatOf(t, chat); c.Status != aiChatIdle || c.Messages[1].Error != "" {
		t.Fatalf("chat %+v", c)
	}
}

// TestAssistantTurnEmptySummaryGoesOnWhenItFits checks that two empty summaries in a row do not end
// a turn whose conversation still fits the input room: the turn goes on without compacting, does
// not pay for a new summary at every step and ends with its text.
func TestAssistantTurnEmptySummaryGoesOnWhenItFits(t *testing.T) {
	stub := newAICompactStub(t, 3, "unused")
	stub.emptyFirst = 1000
	events, chat, e := aiRunCompactTurn(t, stub, 20)
	if last := lastEvent(t, events); last.name != "done" {
		t.Fatalf("events %v", eventNames(events))
	}
	if stub.compacted != aiCompactAttempts {
		t.Fatalf("compaction calls %d, want exactly one compaction of %d attempts", stub.compacted, aiCompactAttempts)
	}
	if c := e.chatOf(t, chat); c.Status != aiChatIdle || c.Messages[1].Error != "" {
		t.Fatalf("chat %+v", c)
	}
}

// TestAssistantTurnEmptySummaryEndsWithTheReason checks that a compaction that returns nothing,
// while the conversation no longer fits, ends the turn with ai_provider_error and the reason
// empty_summary, and that the text collected so far stays in the stored reply.
func TestAssistantTurnEmptySummaryEndsWithTheReason(t *testing.T) {
	stub := newAICompactStub(t, 1000, "   ")
	events, chat, e := aiRunCompactTurn(t, stub, 20)
	last := lastEvent(t, events)
	if last.name != "error" || errorCode(t, last) != aiErrProvider {
		t.Fatalf("events %v", eventNames(events))
	}
	var v struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(last.data), &v); err != nil || v.Params["reason"] != aiReasonEmptySummary {
		t.Fatalf("error event %s", last.data)
	}
	if stub.compacted < aiCompactAttempts {
		t.Fatalf("compaction calls %d, want at least %d", stub.compacted, aiCompactAttempts)
	}
	c := e.chatOf(t, chat)
	if c.Status != aiChatIdle || c.Messages[1].Error != aiErrProvider || !strings.Contains(c.Messages[1].Text, "data data") {
		t.Fatalf("the collected text is lost: %+v", c)
	}
}

// TestAIProviderErrorReasons checks the reason a provider failure carries for the panel: a 5xx
// answer is overload, an unreachable host is unreachable, and other answers carry none.
func TestAIProviderErrorReasons(t *testing.T) {
	c := newAIClient("https://x", aiTestKey, http.DefaultClient, func(http.Header) {})
	for _, tc := range []struct {
		status int
		want   any
	}{{500, aiReasonOverload}, {503, aiReasonOverload}, {529, aiReasonOverload}, {400, nil}, {404, nil}} {
		e := c.statusErr(tc.status, []byte(`{"error":{"message":"boom"}}`))
		if e.code != aiErrProvider || e.Params["reason"] != tc.want {
			t.Errorf("status %d: code %s params %v, want reason %v", tc.status, e.code, e.Params, tc.want)
		}
	}
	e := c.transportErr(context.Background(), errors.New("connection refused"))
	if ae := aiErrOf(t, e); ae.code != aiErrProvider || ae.Params["reason"] != aiReasonUnreachable {
		t.Errorf("unreachable: %+v", ae)
	}
}
