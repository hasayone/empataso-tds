package admin

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// aiTurnRun is a turn that runs: the chat it answers, the function that cancels it and the feed
// of its events, which a page attaches to. feed is set once the turn has its feed.
type aiTurnRun struct {
	chatID string
	cancel context.CancelFunc
	feed   *aiSSE
}

// aiTurnState is the registry of running turns, one per user and at most aiTurnMaxProcess in all.
//
// mu guards runs, which maps the chat uid to its turn. wg counts the turn goroutines, so a
// shutdown can wait for them to store their chats. heartbeat and timeout replace the ping
// period and the turn limit in tests; zero values mean aiHeartbeat and aiTurnTimeout.
type aiTurnState struct {
	mu        sync.Mutex
	runs      map[string]*aiTurnRun
	wg        sync.WaitGroup
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

// aiTurnSetFeed gives run the feed pages attach to.
func (s *Server) aiTurnSetFeed(run *aiTurnRun, feed *aiSSE) {
	t := &s.ai.turns
	t.mu.Lock()
	defer t.mu.Unlock()
	run.feed = feed
}

// aiTurnFeedOf returns the feed of the turn that uid runs on chat chatID. It reports false when
// the user has no turn, the turn answers another chat, or it has no feed yet.
func (s *Server) aiTurnFeedOf(uid, chatID string) (*aiSSE, bool) {
	t := &s.ai.turns
	t.mu.Lock()
	defer t.mu.Unlock()
	run := t.runs[uid]
	if run == nil || run.chatID != chatID || run.feed == nil {
		return nil, false
	}
	return run.feed, true
}

// aiTurnCancelChat cancels the turn that uid runs on chat chatID, on the user's own request. It
// reports whether there was such a turn. A turn of another chat of the user is left alone.
func (s *Server) aiTurnCancelChat(uid, chatID string) bool {
	t := &s.ai.turns
	t.mu.Lock()
	run := t.runs[uid]
	t.mu.Unlock()
	if run == nil || run.chatID != chatID {
		return false
	}
	run.cancel()
	return true
}

// WaitTurns waits until every turn has stored its chat or the time ends. A shutdown calls it after
// CancelTurns, because a turn no longer lives inside the request that started it. It reports whether
// every turn ended in time.
func (s *Server) WaitTurns(limit time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.ai.turns.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		return false
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
