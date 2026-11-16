package admin

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// aiSSE writes the event stream of one turn. One mutex makes the writer single: the heartbeat
// and the events never interleave. A failed write marks the stream dead and later writes are
// dropped, so the turn can still finish and be stored.
type aiSSE struct {
	mu   sync.Mutex
	rc   *http.ResponseController
	w    http.ResponseWriter
	dead bool
}

// newAISSE returns the writer of w.
func newAISSE(w http.ResponseWriter) *aiSSE {
	return &aiSSE{w: w, rc: http.NewResponseController(w)}
}

// write sends chunk and flushes it. A write deadline keeps a client that stopped reading from
// holding the turn for long.
func (e *aiSSE) write(chunk string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dead {
		return
	}
	_ = e.rc.SetWriteDeadline(time.Now().Add(aiSSEWriteTimeout))
	if _, err := e.w.Write([]byte(chunk)); err != nil {
		e.dead = true
		return
	}
	if err := e.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		e.dead = true
	}
}

// comment sends a comment line, which clients ignore.
func (e *aiSSE) comment(text string) { e.write(": " + text + "\n\n") }

// event sends the event name with v as its JSON data. A value that cannot be encoded is dropped.
func (e *aiSSE) event(name string, v any) {
	data, err := aiMarshal(v)
	if err != nil {
		return
	}
	e.write("event: " + name + "\ndata: " + string(data) + "\n\n")
}

// heartbeat sends a ": ping" comment every period until the returned function is called. The
// function waits for the sender to stop, so nothing is written after the handler returns.
func (e *aiSSE) heartbeat(every time.Duration) (stop func()) {
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
				e.comment("ping")
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// finish clears the write deadline, which a kept-alive connection would carry into its next request.
func (e *aiSSE) finish() {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.rc.SetWriteDeadline(time.Time{})
}

// aiWantsSSE reports whether the client accepts an event stream.
func aiWantsSSE(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}
