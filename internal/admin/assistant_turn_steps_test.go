package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// aiStepCounter is a provider stub that asks for a tool on its first tools model calls and then
// answers with text. A compaction call is answered with a summary and counted apart, so the model
// steps are calls minus compacted. It keeps no request bodies, which lets a turn of a thousand steps
// run without holding every growing conversation in memory.
type aiStepCounter struct {
	*aiStub
	tools     int64
	calls     atomic.Int64
	compacted atomic.Int64
}

// newAIStepCounter starts the stub; tools is how many model calls ask for list_offers.
func newAIStepCounter(t *testing.T, tools int64) *aiStepCounter {
	t.Helper()
	c := &aiStepCounter{aiStub: &aiStub{}, tools: tools}
	c.aiStub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "compact the working memory") {
			c.compacted.Add(1)
			aiJSONReply(w, http.StatusOK, aiAnthText("SUMMARY: the work goes on", 50, 20))
			return
		}
		if c.calls.Add(1) <= c.tools {
			aiJSONReply(w, http.StatusOK, aiAnthTool("", "tu", "list_offers", `{"limit":1}`))
			return
		}
		aiJSONReply(w, http.StatusOK, aiAnthText("all done", 30, 5))
	}))
	t.Cleanup(c.aiStub.srv.Close)
	return c
}

// runSteps runs one turn of the settings against c and returns the last event of its stream.
func runSteps(t *testing.T, c *aiStepCounter, settings string) sseEvent {
	t.Helper()
	e := newTurnEnv(t, c.aiStub, settings)
	chat := e.newChat(t)
	resp := e.send(t, chat, `{"text":"go through everything"}`)
	defer resp.Body.Close()
	events, _ := readSSE(t, resp.Body)
	return lastEvent(t, events)
}

func TestAIClampStepsCeilingIsAThousand(t *testing.T) {
	for _, c := range []struct{ stored, want int }{
		{aiLegacyMaxSteps, 1000}, {aiLegacyMaxSteps30, 1000}, {0, 1000}, {-1, 1000},
		{31, 31}, {999, 999}, {1000, 1000}, {1001, 1000}, {1 << 30, 1000},
	} {
		if got := aiClampSteps(c.stored); got != c.want {
			t.Errorf("aiClampSteps(%d) = %d, want %d", c.stored, got, c.want)
		}
	}
	if aiDefaultMaxSteps != 1000 || aiMaxStepsCap != 1000 {
		t.Fatalf("default %d, ceiling %d", aiDefaultMaxSteps, aiMaxStepsCap)
	}
	if aiMaxCompactions < aiMaxStepsCap {
		t.Fatalf("a turn of %d steps may compact only %d times", aiMaxStepsCap, aiMaxCompactions)
	}
	if aiTurnTimeout < aiMaxStepsCap*aiStepTimeBudget {
		t.Fatalf("the turn time %v does not hold %d steps", aiTurnTimeout, aiMaxStepsCap)
	}
}

// TestAssistantTurnRunsAThousandSteps lets a model call a tool 999 times and answer on the 1000th
// call, with the step limit set to the ceiling.
func TestAssistantTurnRunsAThousandSteps(t *testing.T) {
	c := newAIStepCounter(t, 999)
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, `"max_steps":1000,`, 1)
	if last := runSteps(t, c, settings); last.name != "done" {
		t.Fatalf("the last event is %s: %s", last.name, last.data)
	}
	if got := c.calls.Load(); got != 1000 {
		t.Fatalf("model steps %d, want 1000", got)
	}
}

// TestAssistantTurnStopsAtThousandAndOne gives a model that never stops: the turn makes 1000 calls
// and no 1001st, and ends with ai_steps_limit.
func TestAssistantTurnStopsAtThousandAndOne(t *testing.T) {
	c := newAIStepCounter(t, 1<<30)
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, `"max_steps":1000,`, 1)
	last := runSteps(t, c, settings)
	if last.name != "error" || errorCode(t, last) != aiErrStepsLimit {
		t.Fatalf("the last event is %s: %s", last.name, last.data)
	}
	if got := c.calls.Load(); got != 1000 {
		t.Fatalf("model steps %d, want exactly 1000", got)
	}
}

// TestAssistantTurnOldThirtyStepsDoNotCut runs forty tool steps with the 30 an earlier save stored
// in ai.json: the value reads as the default and the turn is not cut at 30.
func TestAssistantTurnOldThirtyStepsDoNotCut(t *testing.T) {
	c := newAIStepCounter(t, 40)
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, `"max_steps":30,`, 1)
	if last := runSteps(t, c, settings); last.name != "done" {
		t.Fatalf("the last event is %s: %s", last.name, last.data)
	}
	if got := c.calls.Load(); got != 41 {
		t.Fatalf("model steps %d, want 40 tool steps and the final one", got)
	}
}
