package admin

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// aiTurnRun is a turn that runs: the chat it answers and the function that cancels it.
type aiTurnRun struct {
	chatID string
	cancel context.CancelFunc
}

// aiTurnState is the registry of running turns, one per user and at most aiTurnMaxProcess in all.
//
// mu guards runs, which maps the chat uid to its turn. heartbeat and timeout replace the ping
// period and the turn limit in tests; zero values mean aiHeartbeat and aiTurnTimeout.
type aiTurnState struct {
	mu        sync.Mutex
	runs      map[string]*aiTurnRun
	heartbeat time.Duration
	timeout   time.Duration
}

// beat returns the ping period.
func (t *aiTurnState) beat() time.Duration {
	if t.heartbeat > 0 {
		return t.heartbeat
	}
	return aiHeartbeat
}

// limit returns the duration a turn may run.
func (t *aiTurnState) limit() time.Duration {
	if t.timeout > 0 {
		return t.timeout
	}
	return aiTurnTimeout
}

// aiTurnBegin registers a turn of uid on chat chatID. cancel stops the turn. It returns 409
// ai_busy when the user already has a turn and 429 ai_busy when the process runs
// aiTurnMaxProcess turns. The caller ends the turn with aiTurnEnd.
func (s *Server) aiTurnBegin(uid, chatID string, cancel context.CancelFunc) (*aiTurnRun, error) {
	t := &s.ai.turns
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, busy := t.runs[uid]; busy {
		return nil, newAIError(http.StatusConflict, aiErrBusy, "the assistant is still answering your previous message")
	}
	if len(t.runs) >= aiTurnMaxProcess {
		return nil, newAIError(http.StatusTooManyRequests, aiErrBusy, "the assistant is busy with other users; try again in a moment")
	}
	if t.runs == nil {
		t.runs = make(map[string]*aiTurnRun)
	}
	run := &aiTurnRun{chatID: chatID, cancel: cancel}
	t.runs[uid] = run
	return run, nil
}

// aiTurnEnd removes run from the registry. A run that was replaced is left alone.
func (s *Server) aiTurnEnd(uid string, run *aiTurnRun) {
	t := &s.ai.turns
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runs[uid] == run {
		delete(t.runs, uid)
	}
}

// aiTurnCancelUser cancels the turn of uid, if it has one.
func (s *Server) aiTurnCancelUser(uid string) {
	t := &s.ai.turns
	t.mu.Lock()
	run := t.runs[uid]
	t.mu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// CancelTurns cancels every running turn so a shutdown does not wait for the model. Each turn
// stores what it has, marks its chat interrupted and ends. It is meant for
// http.Server.RegisterOnShutdown.
func (s *Server) CancelTurns() {
	t := &s.ai.turns
	t.mu.Lock()
	runs := make([]*aiTurnRun, 0, len(t.runs))
	for _, run := range t.runs {
		runs = append(runs, run)
	}
	t.mu.Unlock()
	for _, run := range runs {
		run.cancel()
	}
}
