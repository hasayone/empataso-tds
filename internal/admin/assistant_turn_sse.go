package admin

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// aiFeedMaxEvents is how many events of one turn a feed keeps for a page that joins late. The
// oldest status and tool lines are dropped past it; the latest reply text is always kept.
const aiFeedMaxEvents = 600

// aiSub is one page reading the feed of a turn. gone is closed when the page is detached, when a
// write to it failed or when the turn ended, and tells the handler of that page to return.
type aiSub struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	gone chan struct{}
	once sync.Once
}

// leave closes gone once.
func (s *aiSub) leave() { s.once.Do(func() { close(s.gone) }) }

// aiFeedEntry is one event kept for replay: its name and the chunk that was written.
type aiFeedEntry struct {
	name  string
	chunk string
}

// aiSSE is the event feed of one turn. The turn writes every event once; the feed keeps the events
// and sends them to each page that is attached, so a page that leaves does not stop the turn and a
// page that comes back, a reload or a new tab, is given what it missed and then follows live.
//
// One mutex makes the writer single: the heartbeat, the events and a page joining never interleave.
// A failed write drops that page only; the turn still finishes and is stored. The reply text event
// carries the whole reply so far, so only its latest copy is kept for replay.
type aiSSE struct {
	mu     sync.Mutex
	subs   map[*aiSub]struct{}
	log    []aiFeedEntry
	closed bool
}

// newAIFeed returns a feed with no page attached.
func newAIFeed() *aiSSE {
	return &aiSSE{subs: map[*aiSub]struct{}{}}
}

// newAISSE returns a feed with the page that writes to w already attached.
func newAISSE(w http.ResponseWriter) *aiSSE {
	e := newAIFeed()
	e.attach(w)
	return e
}

// writeSub sends chunk to one page and flushes it. A write deadline keeps a page that stopped
// reading from holding the turn for long. It reports false when the page can no longer be written.
func (e *aiSSE) writeSub(s *aiSub, chunk string) bool {
	_ = s.rc.SetWriteDeadline(time.Now().Add(aiSSEWriteTimeout))
	if _, err := s.w.Write([]byte(chunk)); err != nil {
		return false
	}
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	return true
}

// attach joins the page that reads w. It sends ": open", then every kept event in order, and from
// then on every new one. On a feed that already ended the page gets the kept events and is released
// at once. The headers of w must be written before the call.
func (e *aiSSE) attach(w http.ResponseWriter) *aiSub {
	s := &aiSub{w: w, rc: http.NewResponseController(w), gone: make(chan struct{})}
	e.mu.Lock()
	defer e.mu.Unlock()
	ok := e.writeSub(s, ": open\n\n")
	for i := 0; ok && i < len(e.log); i++ {
		ok = e.writeSub(s, e.log[i].chunk)
	}
	if !ok || e.closed {
		_ = s.rc.SetWriteDeadline(time.Time{})
		s.leave()
		return s
	}
	e.subs[s] = struct{}{}
	return s
}

// detach removes a page. It waits for a write to that page in progress, so nothing is written to w
// after the call, and clears the write deadline a kept-alive connection would carry on.
func (e *aiSSE) detach(s *aiSub) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.subs, s)
	_ = s.rc.SetWriteDeadline(time.Time{})
	s.leave()
}

// write sends chunk to every attached page, dropping a page whose write failed. A non-empty name
// also keeps the chunk for replay.
func (e *aiSSE) write(chunk, name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	if name != "" {
		e.keep(name, chunk)
	}
	for s := range e.subs {
		if !e.writeSub(s, chunk) {
			delete(e.subs, s)
			s.leave()
		}
	}
}

// keep adds an event to the replay log. A reply text replaces the earlier one. Past
// aiFeedMaxEvents the oldest event that is not the reply text is dropped.
func (e *aiSSE) keep(name, chunk string) {
	if name == "text" {
		for i := range e.log {
			if e.log[i].name == "text" {
				e.log = append(e.log[:i], e.log[i+1:]...)
				break
			}
		}
	}
	e.log = append(e.log, aiFeedEntry{name: name, chunk: chunk})
	if len(e.log) <= aiFeedMaxEvents {
		return
	}
	for i := range e.log {
		if e.log[i].name != "text" {
			e.log = append(e.log[:i], e.log[i+1:]...)
			return
		}
	}
}

// comment sends a comment line, which clients ignore. It is not kept for replay.
func (e *aiSSE) comment(text string) { e.write(": "+text+"\n\n", "") }

// event sends the event name with v as its JSON data. A value that cannot be encoded is dropped.
func (e *aiSSE) event(name string, v any) {
	data, err := aiMarshal(v)
	if err != nil {
		return
	}
	e.write("event: "+name+"\ndata: "+string(data)+"\n\n", name)
}

// heartbeat sends a ": ping" comment every period until the returned function is called. The
// function waits for the sender to stop, so nothing is written after the turn ended.
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

// close ends the feed: every attached page is released and its write deadline cleared, and later
// events are dropped. The kept events stay for a page that attaches afterwards.
func (e *aiSSE) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for s := range e.subs {
		_ = s.rc.SetWriteDeadline(time.Time{})
		s.leave()
	}
	e.subs = map[*aiSub]struct{}{}
}

// aiWantsSSE reports whether the client accepts an event stream.
func aiWantsSSE(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}
