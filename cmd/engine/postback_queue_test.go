package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/store"
)

// fakeConv is a conversionStore the tests drive: it answers every conversion
// with res and err, records the input and the deadline of each call, and sends
// journal rows to logFn when set.
type fakeConv struct {
	mu     sync.Mutex
	err    error
	res    store.ConversionResult
	calls  []store.PostbackIn
	ttls   []time.Duration
	logFn  func([]store.PostbackLogRow) error
	logged [][]store.PostbackLogRow

	// hold, when set, blocks RecordConversion until it is closed; entered is told
	// that a call has started waiting.
	hold    chan struct{}
	entered chan struct{}
}

// RecordConversion records the call and returns the configured answer.
func (f *fakeConv) RecordConversion(ctx context.Context, in store.PostbackIn) (store.ConversionResult, error) {
	if f.hold != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		<-f.hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	ttl := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		ttl = time.Until(dl)
	}
	f.ttls = append(f.ttls, ttl)
	if f.err != nil {
		return store.ConversionResult{}, f.err
	}
	return f.res, nil
}

// InsertPostbackLog records the rows, or fails through logFn.
func (f *fakeConv) InsertPostbackLog(_ context.Context, rows []store.PostbackLogRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logFn != nil {
		if err := f.logFn(rows); err != nil {
			return err
		}
	}
	f.logged = append(f.logged, append([]store.PostbackLogRow(nil), rows...))
	return nil
}

// set replaces the answer of RecordConversion.
func (f *fakeConv) set(res store.ConversionResult, err error) {
	f.mu.Lock()
	f.res, f.err = res, err
	f.mu.Unlock()
}

// callCount returns how many conversions were requested.
func (f *fakeConv) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// queueEnv returns engine deps with a temp spool and a postback queue over it.
// The queue's workers are not started: tests step it with drain. cs may be nil
// for an engine without a store.
func queueEnv(t *testing.T, cs *fakeConv) *engineDeps {
	t.Helper()
	sp, err := logbuf.OpenSpool(logbuf.SpoolConfig{
		Root: t.TempDir(), Instance: "engine", MaxBytes: 1 << 30, MinFreePct: 1,
		Free: func(string) (uint64, uint64, error) { return 900, 1000, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sp.Close)
	d := &engineDeps{log: discardLog(), outbound: make(chan outboundJob, 8)}
	if cs != nil {
		d.convOverride = cs
	}
	q, err := sp.Queue(postbackKind)
	if err != nil {
		t.Fatal(err)
	}
	d.pbq = newPostbackQueue(d, q, nil, discardLog())
	return d
}

// drain replays every segment the queue offers once, and returns how many it
// handled.
func drain(d *engineDeps) int {
	n := 0
	for {
		seg, ok := d.pbq.next()
		if !ok {
			return n
		}
		d.pbq.handle(seg)
		n++
	}
}

// forgetPauses clears the waits of the queue so the next drain looks at every
// segment again.
func forgetPauses(p *postbackQueue) {
	p.mu.Lock()
	p.state = map[string]*entryState{}
	p.downUntil, p.downBackoff = time.Time{}, 0
	p.mu.Unlock()
}

// sendPostback sends GET target through the postback handler.
func sendPostback(d *engineDeps, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	d.postback(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestPostbackStoreErrorQueuedAnd200 checks that a store failure is answered
// 200, queued with its arrival time, and later stored with that same time.
func TestPostbackStoreErrorQueuedAnd200(t *testing.T) {
	cs := &fakeConv{err: errors.New("boom")}
	d := queueEnv(t, cs)
	rec := sendPostback(d, "/postback?subid=abc&status=lead&payout=5")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "success") {
		t.Fatalf("answer %d %s, want 200 success", rec.Code, rec.Body.String())
	}
	if got := len(d.pbq.q.List()); got != 1 {
		t.Fatalf("%d segments queued, want 1", got)
	}
	if got := d.stats.postbacks.snapshot(); got.Queued != 1 || got.StoreError != 0 {
		t.Fatalf("counters %+v, want queued 1", got)
	}
	arrived := cs.calls[0].At
	if arrived.IsZero() {
		t.Fatal("the first attempt carried no arrival time")
	}

	cs.set(store.ConversionResult{GroupID: "g1"}, nil)
	if n := drain(d); n != 1 {
		t.Fatalf("drained %d segments, want 1", n)
	}
	if len(d.pbq.q.List()) != 0 {
		t.Fatal("a stored postback stayed in the queue")
	}
	last := cs.calls[len(cs.calls)-1]
	if !last.At.Equal(arrived) {
		t.Fatalf("replay time %v, want the arrival time %v", last.At, arrived)
	}
	if last.CID != "abc" || last.Status != "lead" || last.Profit != 5 {
		t.Fatalf("replayed input %+v", last)
	}
	if got := d.stats.postbacks.snapshot(); got.Replayed != 1 {
		t.Fatalf("counters %+v, want replayed 1", got)
	}
}

// TestPostbackStoreTimeout checks that the store call has a deadline of at most
// three seconds and that a timeout is answered 200 and queued.
func TestPostbackStoreTimeout(t *testing.T) {
	if postbackStoreTimeout != 3*time.Second {
		t.Fatalf("postbackStoreTimeout = %v, want 3s", postbackStoreTimeout)
	}
	cs := &fakeConv{err: context.DeadlineExceeded}
	d := queueEnv(t, cs)
	rec := sendPostback(d, "/postback?subid=abc&status=sale&payout=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("answer %d, want 200", rec.Code)
	}
	if ttl := cs.ttls[0]; ttl <= 0 || ttl > postbackStoreTimeout {
		t.Fatalf("store call deadline %v, want within (0, %v]", ttl, postbackStoreTimeout)
	}
	if len(d.pbq.q.List()) != 1 {
		t.Fatal("a timed out postback was not queued")
	}
}

// TestPostbackNoStoreQueuedUntilConnected checks that a postback that arrives
// before ClickHouse connects is queued and held until the store appears.
func TestPostbackNoStoreQueuedUntilConnected(t *testing.T) {
	d := queueEnv(t, nil)
	rec := sendPostback(d, "/postback?subid=abc&status=lead")
	if rec.Code != http.StatusOK {
		t.Fatalf("answer %d, want 200", rec.Code)
	}
	if got := d.stats.postbacks.snapshot(); got.Queued != 1 || got.NoStore != 0 {
		t.Fatalf("counters %+v, want queued 1", got)
	}
	if n := drain(d); n != 0 {
		t.Fatalf("drained %d without a store, want 0", n)
	}
	cs := &fakeConv{}
	d.convOverride = cs
	if n := drain(d); n != 1 || cs.callCount() != 1 || len(d.pbq.q.List()) != 0 {
		t.Fatalf("after connect: drained %d, store calls %d, left %d", n, cs.callCount(), len(d.pbq.q.List()))
	}
}

// TestPostbackWithoutQueueKeepsOldAnswers checks that without a disk queue the
// answers are the ones postbackResult gives.
func TestPostbackWithoutQueueKeepsOldAnswers(t *testing.T) {
	d := &engineDeps{log: discardLog(), convOverride: &fakeConv{err: errors.New("boom")}}
	if rec := sendPostback(d, "/postback?subid=abc&status=lead"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store error without a queue: %d, want 503", rec.Code)
	}
	d.convOverride = &fakeConv{err: sql.ErrNoRows}
	if rec := sendPostback(d, "/postback?subid="+signedCID()+"&status=lead"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing click without a queue: %d, want 404", rec.Code)
	}
}

// TestPostbackMissingSignedClickRetried checks that a signed click id without a
// click is accepted into the retry queue with 200, stays queued while the click
// may appear, and is stored once the click is there.
func TestPostbackMissingSignedClickRetried(t *testing.T) {
	cs := &fakeConv{err: sql.ErrNoRows}
	d := queueEnv(t, cs)
	cid := signedCID()
	rec := sendPostback(d, "/postback?subid="+cid+"&status=lead&payout=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("answer %d, want 200", rec.Code)
	}
	segs := d.pbq.q.List()
	if len(segs) != 1 || !strings.HasPrefix(segs[0].Token, "n:") {
		t.Fatalf("segments %+v, want one retry segment", segs)
	}
	if got := d.stats.postbacks.snapshot(); got.Retry != 1 || got.ClickNotFound != 0 {
		t.Fatalf("counters %+v, want retry 1", got)
	}

	if n := drain(d); n != 1 || len(d.pbq.q.List()) != 1 {
		t.Fatalf("a young postback without a click: drained %d, left %d, want 1 and 1", n, len(d.pbq.q.List()))
	}
	if n := drain(d); n != 0 {
		t.Fatalf("the segment was looked at again at once, drained %d", n)
	}
	forgetPauses(d.pbq)
	cs.set(store.ConversionResult{GroupID: "g1"}, nil)
	if drain(d) != 1 || len(d.pbq.q.List()) != 0 {
		t.Fatal("the postback was not stored after the click appeared")
	}
	if got := d.stats.postbacks.snapshot(); got.Replayed != 1 {
		t.Fatalf("counters %+v, want replayed 1", got)
	}
}

// TestPostbackUnsignedMissingClickIsFinal404 checks that an id the engine did
// not issue and without a click gets 404 and is not queued.
func TestPostbackUnsignedMissingClickIsFinal404(t *testing.T) {
	d := queueEnv(t, &fakeConv{err: sql.ErrNoRows})
	rec := sendPostback(d, "/postback?subid=made-up-id&status=lead")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("answer %d, want 404", rec.Code)
	}
	if len(d.pbq.q.List()) != 0 {
		t.Fatal("an unsigned missing click was queued")
	}
	if got := d.stats.postbacks.snapshot(); got.ClickNotFound != 1 {
		t.Fatalf("counters %+v, want click_not_found 1", got)
	}
}

// setFlow replaces the view of the click events of the queue and forgets the
// cached reading, so the next look at the postback calls the function again.
func setFlow(p *postbackQueue, f func() eventFlow) {
	p.flowMu.Lock()
	p.flow, p.flowAt = f, time.Time{}
	p.flowMu.Unlock()
}

// TestRetryExpires checks that a postback without a click is written off only
// after the window that counts from the first insert after a busy spell, and
// not while click events are still on their way.
func TestRetryExpires(t *testing.T) {
	cs := &fakeConv{err: sql.ErrNoRows}
	d := queueEnv(t, cs)
	old := spooledPostback{CID: signedCID(), Status: "lead", At: time.Now().Add(-postbackRetryWindow - time.Second)}

	flow := eventFlow{Busy: true}
	setFlow(d.pbq, func() eventFlow { return flow })
	if err := d.pbq.accept(old, kindRetry, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if drain(d) != 1 || len(d.pbq.q.List()) != 1 {
		t.Fatal("an old postback was written off while click events were on their way")
	}

	forgetPauses(d.pbq)
	setFlow(d.pbq, func() eventFlow { return flow })
	flow = eventFlow{LastInsertOK: time.Now().Add(-time.Second)}
	if drain(d) != 1 || len(d.pbq.q.List()) != 1 {
		t.Fatal("the window did not restart at the insert that ended the busy spell")
	}

	forgetPauses(d.pbq)
	d.pbq.flowMu.Lock()
	d.pbq.gate = time.Now().Add(-postbackRetryWindow - time.Second)
	d.pbq.flowMu.Unlock()
	if drain(d) != 1 || len(d.pbq.q.List()) != 0 {
		t.Fatal("an old postback was not written off after the window")
	}
	if got := d.stats.postbacks.snapshot(); got.RetryExpired != 1 {
		t.Fatalf("counters %+v, want retry_expired 1", got)
	}
	d.pbq.mu.Lock()
	defer d.pbq.mu.Unlock()
	if q := d.pbq.quotas[kindRetry]; q.total != 0 || len(q.senders) != 0 {
		t.Fatalf("places not given back: total %d senders %v", q.total, q.senders)
	}
}

// TestRetryFlickerDoesNotRestartTheWindow checks that a flow which flips
// between busy and idle, the way a healthy flush can, does not start the wait
// of an old postback over. Two busy samples in a row still do: that is an
// outage, and the window counts from the insert that ends it.
func TestRetryFlickerDoesNotRestartTheWindow(t *testing.T) {
	d := queueEnv(t, &fakeConv{err: sql.ErrNoRows})
	old := spooledPostback{CID: signedCID(), Status: "lead", At: time.Now().Add(-postbackRetryWindow - time.Second)}
	if err := d.pbq.accept(old, kindRetry, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	recovered := time.Now().Add(-time.Second)
	step := 0
	setFlow(d.pbq, func() eventFlow {
		step++
		switch step {
		case 1:
			return eventFlow{Busy: true, LastInsertOK: recovered}
		case 2:
			return eventFlow{Busy: false, LastInsertOK: recovered}
		default:
			return eventFlow{Busy: step%2 == 1, LastInsertOK: time.Now()}
		}
	})
	look := func() {
		t.Helper()
		forgetPauses(d.pbq)
		d.pbq.flowMu.Lock()
		d.pbq.flowAt = time.Time{}
		d.pbq.flowMu.Unlock()
		if drain(d) != 1 {
			t.Fatal("the segment was not looked at")
		}
	}
	look()
	if len(d.pbq.q.List()) != 1 {
		t.Fatal("written off during the first busy sample")
	}
	look()
	if len(d.pbq.q.List()) != 1 {
		t.Fatal("written off at the insert that ended a one-sample busy spell")
	}
	look()
	look()
	d.pbq.flowMu.Lock()
	gate := d.pbq.gate
	d.pbq.flowMu.Unlock()
	if !gate.Equal(recovered) {
		t.Fatalf("gate is %v after a flicker, want the first recovery %v", gate, recovered)
	}
	if len(d.pbq.q.List()) != 1 {
		t.Fatal("a flicker wrote the postback off or kept it for a new window")
	}
	d.pbq.flowMu.Lock()
	d.pbq.gate = time.Now().Add(-postbackRetryWindow - time.Second)
	d.pbq.flowMu.Unlock()
	look()
	if len(d.pbq.q.List()) != 1 {
		t.Fatal("a single busy sample after the window cleared the gate")
	}
	look()
	if len(d.pbq.q.List()) != 0 {
		t.Fatal("the postback was not written off once the window from the first recovery had passed")
	}
}

// TestRetryWaitsOutStoreOutage checks the sequence of an outage of the store
// longer than the window: the postback is old when the store is back, the click
// is not stored yet because its events are still in the buffer, so the segment
// stays; once the click is written the conversion is stored.
func TestRetryWaitsOutStoreOutage(t *testing.T) {
	cs := &fakeConv{err: sql.ErrNoRows}
	d := queueEnv(t, cs)
	arrived := time.Now().Add(-10 * postbackRetryWindow)
	if err := d.pbq.accept(spooledPostback{CID: signedCID(), Status: "sale", Profit: 4, At: arrived}, kindRetry, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	busy := true
	setFlow(d.pbq, func() eventFlow { return eventFlow{Busy: busy, LastInsertOK: time.Now()} })
	for range 3 {
		forgetPauses(d.pbq)
		d.pbq.flowMu.Lock()
		d.pbq.flowAt = time.Time{}
		d.pbq.flowMu.Unlock()
		if drain(d) != 1 || len(d.pbq.q.List()) != 1 {
			t.Fatal("the segment was dropped while the buffer still held click events")
		}
	}
	busy = false
	cs.set(store.ConversionResult{GroupID: "g1"}, nil)
	forgetPauses(d.pbq)
	d.pbq.flowMu.Lock()
	d.pbq.flowAt = time.Time{}
	d.pbq.flowMu.Unlock()
	if drain(d) != 1 || len(d.pbq.q.List()) != 0 {
		t.Fatal("the conversion was not written after the click was stored")
	}
	if last := cs.calls[len(cs.calls)-1]; !last.At.Equal(arrived) || last.Profit != 4 {
		t.Fatalf("written %+v, want the original arrival time and payout", last)
	}
	if got := d.stats.postbacks.snapshot(); got.Replayed != 1 || got.RetryExpired != 0 {
		t.Fatalf("counters %+v, want replayed 1 and nothing expired", got)
	}
}

// TestRetryCeilingsPerSender checks that one sender cannot hold more than its
// ceiling of waiting postbacks, that it then gets 404 counted once as
// retry_overflow, and that another sender is still accepted.
func TestRetryCeilingsPerSender(t *testing.T) {
	d := queueEnv(t, &fakeConv{err: sql.ErrNoRows})
	e := spooledPostback{CID: signedCID(), Status: "lead", At: time.Now()}
	for range postbackRetryPerSender {
		if err := d.pbq.accept(e, kindRetry, "unknown"); err != nil {
			t.Fatal(err)
		}
	}
	rec := sendPostback(d, "/postback?subid="+e.CID+"&status=lead")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("answer %d past the sender ceiling, want 404", rec.Code)
	}
	if got := d.stats.postbacks.snapshot(); got.RetryOverflow != 1 || got.ClickNotFound != 0 {
		t.Fatalf("counters %+v, want retry_overflow 1 and click_not_found 0", got)
	}
	if err := d.pbq.accept(e, kindRetry, "9.9.9.9"); err != nil {
		t.Fatalf("another sender refused: %v", err)
	}
	if err := d.pbq.accept(e, kindStore, "unknown"); err != nil {
		t.Fatalf("a store-failure postback must not count against the retry ceiling: %v", err)
	}
}

// TestRetryCeilingTotal checks the ceiling over all senders.
func TestRetryCeilingTotal(t *testing.T) {
	d := queueEnv(t, &fakeConv{err: sql.ErrNoRows})
	d.pbq.mu.Lock()
	d.pbq.quotas[kindRetry].total = postbackRetryMax
	d.pbq.mu.Unlock()
	err := d.pbq.accept(spooledPostback{CID: signedCID(), Status: "lead", At: time.Now()}, kindRetry, "1.1.1.1")
	if !errors.Is(err, errRetryFull) {
		t.Fatalf("accept past the total ceiling returned %v, want errRetryFull", err)
	}
}

// TestStoreQueueQuotas checks the quotas of postbacks waiting for the store: the
// small quota of unsigned click ids refuses with 503 and counts queue_full once,
// a signed id is still accepted, the per sender ceiling and the byte ceiling
// refuse too.
func TestStoreQueueQuotas(t *testing.T) {
	cs := &fakeConv{err: errors.New("boom")}
	d := queueEnv(t, cs)
	for i := range postbackUnsignedPerSender {
		rec := sendPostback(d, "/postback?subid=junk"+strconv.Itoa(i)+"&status=lead")
		if rec.Code != http.StatusOK {
			t.Fatalf("unsigned postback %d: %d, want 200 inside the quota", i, rec.Code)
		}
	}
	rec := sendPostback(d, "/postback?subid=junk-over&status=lead")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unsigned postback past the quota: %d, want 503", rec.Code)
	}
	got := d.stats.postbacks.snapshot()
	if got.QueueFull != 1 || got.StoreError != 0 || got.Queued != int64(postbackUnsignedPerSender) {
		t.Fatalf("counters %+v, want queue_full 1, store_error 0, queued %d", got, postbackUnsignedPerSender)
	}
	if rec := sendPostback(d, "/postback?subid="+signedCID()+"&status=lead"); rec.Code != http.StatusOK {
		t.Fatalf("a signed postback while the unsigned quota is used: %d, want 200", rec.Code)
	}

	e := spooledPostback{CID: signedCID(), Status: "lead", At: time.Now()}
	d.pbq.mu.Lock()
	d.pbq.quotas[kindStore].senders["1.1.1.1"] = postbackStorePerSender
	d.pbq.mu.Unlock()
	if err := d.pbq.accept(e, kindStore, "1.1.1.1"); !errors.Is(err, errStoreFull) {
		t.Fatalf("past the per sender ceiling: %v, want errStoreFull", err)
	}
	if err := d.pbq.accept(e, kindStore, "2.2.2.2"); err != nil {
		t.Fatalf("another sender refused: %v", err)
	}
}

// TestStoreQueueByteCeiling checks that the postbacks waiting for the store stop
// at the byte ceiling of the queue, while the retry queue is not cut by it.
func TestStoreQueueByteCeiling(t *testing.T) {
	d := queueEnv(t, &fakeConv{err: errors.New("boom")})
	e := spooledPostback{CID: signedCID(), Status: "lead", At: time.Now()}
	if err := d.pbq.accept(e, kindStore, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	d.pbq.maxBytes = d.pbq.q.Stats().Bytes
	if err := d.pbq.accept(e, kindStore, "1.1.1.1"); !errors.Is(err, errStoreFull) {
		t.Fatalf("past the byte ceiling: %v, want errStoreFull", err)
	}
	if err := d.pbq.accept(e, kindUnsigned, "1.1.1.1"); !errors.Is(err, errStoreFull) {
		t.Fatalf("an unsigned one past the byte ceiling: %v, want errStoreFull", err)
	}
	if err := d.pbq.accept(e, kindRetry, "1.1.1.1"); err != nil {
		t.Fatalf("the retry queue is not cut by the byte ceiling: %v", err)
	}
}

// TestQuotasRebuiltAfterRestart checks that a new queue over segments left on
// disk counts the waiting postbacks of every kind again.
func TestQuotasRebuiltAfterRestart(t *testing.T) {
	d := queueEnv(t, nil)
	e := spooledPostback{CID: signedCID(), Status: "lead", At: time.Now()}
	for _, s := range []string{"1.1.1.1", "1.1.1.1", "2.2.2.2"} {
		if err := d.pbq.accept(e, kindRetry, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.pbq.accept(e, kindStore, "3.3.3.3"); err != nil {
		t.Fatal(err)
	}
	if err := d.pbq.accept(e, kindUnsigned, "3.3.3.3"); err != nil {
		t.Fatal(err)
	}
	again := newPostbackQueue(d, d.pbq.q, nil, discardLog())
	r := again.quotas[kindRetry]
	if r.total != 3 || r.senders["1.1.1.1"] != 2 || r.senders["2.2.2.2"] != 1 {
		t.Fatalf("rebuilt retry %d senders %v, want 3 and 1.1.1.1:2 2.2.2.2:1", r.total, r.senders)
	}
	if again.quotas[kindStore].total != 1 || again.quotas[kindUnsigned].senders["3.3.3.3"] != 1 {
		t.Fatalf("rebuilt store %d unsigned %v", again.quotas[kindStore].total, again.quotas[kindUnsigned].senders)
	}
}

// TestReplayStoreFailureKeepsSegmentAndPauses checks that a failing store keeps
// the segment and pauses the whole replay.
func TestReplayStoreFailureKeepsSegmentAndPauses(t *testing.T) {
	cs := &fakeConv{err: errors.New("boom")}
	d := queueEnv(t, cs)
	if err := d.pbq.accept(spooledPostback{CID: "abc", Status: "lead", At: time.Now()}, kindStore, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if n := drain(d); n != 1 {
		t.Fatalf("drained %d, want 1", n)
	}
	if len(d.pbq.q.List()) != 1 {
		t.Fatal("a failed replay removed the segment")
	}
	if _, ok := d.pbq.next(); ok {
		t.Fatal("the replay did not pause after a store failure")
	}
	forgetPauses(d.pbq)
	cs.set(store.ConversionResult{}, nil)
	if drain(d) != 1 || len(d.pbq.q.List()) != 0 {
		t.Fatal("the postback was not stored after the store recovered")
	}
}

// TestReplayBadPayloadRejected checks that a segment that is not a postback is
// set aside and does not block the queue.
func TestReplayBadPayloadRejected(t *testing.T) {
	d := queueEnv(t, &fakeConv{})
	if err := d.pbq.q.Put(logbuf.SegmentMeta{Token: "p::1", Count: 1}, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if drain(d) != 1 || len(d.pbq.q.List()) != 0 {
		t.Fatal("a bad segment stayed in the queue")
	}
	if d.pbq.q.Stats().Rejected != 1 {
		t.Fatalf("rejected %d, want 1", d.pbq.q.Stats().Rejected)
	}
}

// outboundGroup is a campaign with one outbound template for sales.
func outboundGroup() *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{{
			URL: "https://pb.example/pb?cid={subid}&s1={sub_id_1}", Method: "GET", Statuses: []string{"sale"},
		}},
	})
}

// TestReplayQueuesOutboundOnlyForNewPair checks that a replayed postback sends
// the outbound templates when the store says the pair is new, with the sub ids
// of the original query, and sends nothing for a pair it already had.
func TestReplayQueuesOutboundOnlyForNewPair(t *testing.T) {
	cs := &fakeConv{err: errors.New("boom")}
	d := queueEnv(t, cs)
	d.groups = outboundGroup()
	sendPostback(d, "/postback?subid=abc&status=sale&payout=3&sub_id_1=x1")
	sendPostback(d, "/postback?subid=abc&status=sale&payout=3&sub_id_1=x1")
	if len(d.pbq.q.List()) != 2 {
		t.Fatalf("%d segments, want 2", len(d.pbq.q.List()))
	}

	cs.set(store.ConversionResult{GroupID: "g1", Fresh: true}, nil)
	seg, _ := d.pbq.next()
	d.pbq.handle(seg)
	cs.set(store.ConversionResult{GroupID: "g1", Fresh: false}, nil)
	drain(d)

	if len(d.outbound) != 1 {
		t.Fatalf("queued %d outbound jobs, want 1", len(d.outbound))
	}
	job := <-d.outbound
	if !strings.Contains(job.URL, "cid=abc") || !strings.Contains(job.URL, "s1=x1") {
		t.Fatalf("outbound url %q lost the sub id of the original query", job.URL)
	}
}

// newOutboundEnv returns engine deps with a temp spool and an outbound queue.
func newOutboundEnv(t *testing.T) *engineDeps {
	t.Helper()
	d := queueEnv(t, nil)
	sp, err := logbuf.OpenSpool(logbuf.SpoolConfig{
		Root: t.TempDir(), Instance: "engine", MaxBytes: 1 << 30, MinFreePct: 1,
		Free: func(string) (uint64, uint64, error) { return 900, 1000, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sp.Close)
	oq, err := sp.Queue(outboundKind)
	if err != nil {
		t.Fatal(err)
	}
	d.outSpool = newOutboundSpool(oq, discardLog())
	d.groups = outboundGroup()
	return d
}

// TestOutboundJobSurvivesRestart checks that a job written to the disk queue is
// handed to the sender by the feeder of a new process, and removed after the
// sender answered it.
func TestOutboundJobSurvivesRestart(t *testing.T) {
	d := newOutboundEnv(t)
	if !d.fireOutbound("g1", "abc", "", 1, "sale", [config.MaxSubID]string{"x1"}) {
		t.Fatal("job was not queued")
	}
	if len(d.outbound) != 0 {
		t.Fatal("a job went to memory while the disk queue was working")
	}
	if got := d.outSpool.snapshot().Segments; got != 1 {
		t.Fatalf("%d segments on disk, want 1", got)
	}

	restarted := &engineDeps{log: discardLog(), outbound: make(chan outboundJob, 8)}
	restarted.outSpool = newOutboundSpool(d.outSpool.q, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go restarted.runOutboundFeeder(ctx)
	var job outboundJob
	select {
	case job = <-restarted.outbound:
	case <-time.After(5 * time.Second):
		t.Fatal("the feeder did not hand the job over")
	}
	if !strings.Contains(job.URL, "cid=abc") || !strings.Contains(job.URL, "s1=x1") {
		t.Fatalf("job url %q", job.URL)
	}
	restarted.sendOutbound(&http.Client{Transport: answer(http.StatusNoContent)}, job)
	restarted.finishOutbound(job)
	if got := restarted.outSpool.snapshot().Segments; got != 0 {
		t.Fatalf("%d segments left after the answer, want 0", got)
	}
}

// TestOutboundRootSubIDSurvivesRestart checks that {root_subid} is expanded
// before the job is written, so a new process sends the root click id.
func TestOutboundRootSubIDSurvivesRestart(t *testing.T) {
	d := newOutboundEnv(t)
	d.groups = config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks: []config.OutboundPostback{{
			URL: "https://pb.example/pb?cid={root_subid}", Method: "GET", Statuses: []string{"sale"},
		}},
	})
	if !d.fireOutbound("g1", "abc", "root1", 1, "sale", [config.MaxSubID]string{}) {
		t.Fatal("job was not queued")
	}
	restarted := &engineDeps{log: discardLog(), outbound: make(chan outboundJob, 8)}
	restarted.outSpool = newOutboundSpool(d.outSpool.q, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go restarted.runOutboundFeeder(ctx)
	var job outboundJob
	select {
	case job = <-restarted.outbound:
	case <-time.After(5 * time.Second):
		t.Fatal("the feeder did not hand the job over")
	}
	if job.URL != "https://pb.example/pb?cid=root1" || job.RootCID != "root1" {
		t.Fatalf("job %+v", job)
	}
}

// TestOutboundFullChannelKeepsJobOnDisk checks that a full sender channel does
// not drop a job that is on disk.
func TestOutboundFullChannelKeepsJobOnDisk(t *testing.T) {
	d := newOutboundEnv(t)
	d.outbound = make(chan outboundJob)
	d.fireOutbound("g1", "abc", "", 1, "sale", [config.MaxSubID]string{})
	if got := d.stats.outbound.snapshot(); got.Dropped != 0 || got.Queued != 1 {
		t.Fatalf("counters %+v, want queued 1 dropped 0", got)
	}
	if d.outSpool.snapshot().Segments != 1 {
		t.Fatal("the job is not on disk")
	}
}

// TestOutboundLocksTwoGenerations checks that the memory lock holds a key,
// releases it, and keeps a bounded number of keys.
func TestOutboundLocksTwoGenerations(t *testing.T) {
	var l outboundLocks
	if !l.take("a") || l.take("a") {
		t.Fatal("a key must be taken once")
	}
	l.drop("a")
	if !l.take("a") {
		t.Fatal("a dropped key must be free")
	}
	for i := range 3 * outboundLockGen {
		l.take("k" + strconv.Itoa(i))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.cur) + len(l.old); n > 2*outboundLockGen {
		t.Fatalf("%d keys held, want at most %d", n, 2*outboundLockGen)
	}
}

// stoppedLog returns a journal writer over the postback queue of d whose run
// goroutine has ended, so a test can call its methods without a race.
func stoppedLog(t *testing.T, d *engineDeps) *postbackLog {
	t.Helper()
	l := newPostbackLog(d, d.pbq.q, discardLog())
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return l
}

// TestPostbackLogParksAndReplays checks that journal lines that cannot be
// inserted are parked on disk and later written with their original time.
func TestPostbackLogParksAndReplays(t *testing.T) {
	cs := &fakeConv{logFn: func([]store.PostbackLogRow) error { return errors.New("down") }}
	d := queueEnv(t, cs)
	sp := d.pbq.q
	l := newPostbackLog(d, sp, discardLog())
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	l.note(store.PostbackLogRow{Ts: at, Subid: "abc", Status: "lead", Outcome: "ok", Direction: "in"})
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(sp.List()); got != 1 {
		t.Fatalf("%d parked batches, want 1", got)
	}
	if l.lost.Load() != 0 {
		t.Fatalf("lost %d, want 0", l.lost.Load())
	}

	cs.mu.Lock()
	cs.logFn = nil
	cs.mu.Unlock()
	l.downUntil = time.Time{}
	l.replay()
	if len(sp.List()) != 0 {
		t.Fatal("the parked batch was not written back")
	}
	if len(cs.logged) != 1 || len(cs.logged[0]) != 1 || !cs.logged[0][0].Ts.Equal(at) {
		t.Fatalf("written rows %+v, want one with the original time %v", cs.logged, at)
	}
}

// TestPostbackLogBatchesRows checks that several lines go to the store in one
// insert.
func TestPostbackLogBatchesRows(t *testing.T) {
	cs := &fakeConv{}
	d := queueEnv(t, cs)
	l := newPostbackLog(d, d.pbq.q, discardLog())
	for range 5 {
		l.note(store.PostbackLogRow{Ts: time.Now(), Subid: "abc", Outcome: "ok", Direction: "in"})
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cs.logged) != 1 || len(cs.logged[0]) != 5 {
		t.Fatalf("inserts %d, want one with 5 rows", len(cs.logged))
	}
}

// TestPostbackLogCountsLoss checks that lines that cannot be kept are counted:
// no queue and no store, a closed writer.
func TestPostbackLogCountsLoss(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	l := newPostbackLog(d, nil, discardLog())
	l.note(store.PostbackLogRow{Subid: "abc"})
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.lost.Load() != 1 {
		t.Fatalf("lost %d without a store and a queue, want 1", l.lost.Load())
	}
	l.note(store.PostbackLogRow{Subid: "late"})
	if l.lost.Load() != 2 {
		t.Fatalf("lost %d after Close, want 2", l.lost.Load())
	}
}

// TestPostbackLogPoisonBatchRejected checks that a parked batch the store keeps
// refusing while it accepts other inserts is moved to rejected and counted.
func TestPostbackLogPoisonBatchRejected(t *testing.T) {
	cs := &fakeConv{logFn: func(rows []store.PostbackLogRow) error {
		for _, r := range rows {
			if r.Subid == "poison" {
				return errors.New("bad row")
			}
		}
		return nil
	}}
	d := queueEnv(t, cs)
	l := stoppedLog(t, d)
	l.park([]store.PostbackLogRow{{Ts: time.Now(), Subid: "poison"}})
	for range postlogPoisonAfter {
		if err := l.insert([]store.PostbackLogRow{{Ts: time.Now(), Subid: "fine"}}); err != nil {
			t.Fatal(err)
		}
		l.downUntil = time.Time{}
		l.replay()
		l.downUntil = time.Time{}
	}
	if len(d.pbq.q.List()) != 0 || d.pbq.q.Stats().Rejected != 1 {
		t.Fatalf("pending %d rejected %d, want 0 and 1", len(d.pbq.q.List()), d.pbq.q.Stats().Rejected)
	}
	if l.lost.Load() != 1 {
		t.Fatalf("lost %d, want 1", l.lost.Load())
	}
}

// TestPostbackLogNotRejectedDuringOutage checks that a long store outage does
// not count against a parked batch.
func TestPostbackLogNotRejectedDuringOutage(t *testing.T) {
	cs := &fakeConv{logFn: func([]store.PostbackLogRow) error { return errors.New("down") }}
	d := queueEnv(t, cs)
	l := stoppedLog(t, d)
	l.park([]store.PostbackLogRow{{Ts: time.Now(), Subid: "abc"}})
	for range 3 * postlogPoisonAfter {
		l.downUntil = time.Time{}
		l.replay()
	}
	if len(d.pbq.q.List()) != 1 || d.pbq.q.Stats().Rejected != 0 {
		t.Fatalf("pending %d rejected %d during an outage, want 1 and 0", len(d.pbq.q.List()), d.pbq.q.Stats().Rejected)
	}
}

// TestQueueMetricsShowSpools checks that the queue sizes and the journal loss
// reach the metrics snapshot.
func TestQueueMetricsShowSpools(t *testing.T) {
	d := newOutboundEnv(t)
	if err := d.pbq.accept(spooledPostback{CID: "abc", Status: "lead", At: time.Now()}, kindStore, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	d.fireOutbound("g1", "abc", "", 1, "sale", [config.MaxSubID]string{})
	d.plog = newPostbackLog(d, nil, discardLog())
	d.plog.lost.Add(4)
	t.Cleanup(func() { _ = d.plog.Close(context.Background()) })
	m := d.metricsSnapshot("engine", time.Now(), time.Now())
	if m.Spool.Postbacks.Segments != 1 || m.Spool.Outbound.Segments != 1 || m.Spool.PostbackLogLost != 4 {
		t.Fatalf("spool metrics %+v", m.Spool)
	}
}

// TestSplitToken checks the kind and the sender in the token of a segment.
func TestSplitToken(t *testing.T) {
	tok := queueToken(kindRetry, "2001:db8::1")
	if k, s, ok := splitToken(tok); !ok || k != kindRetry || s != "2001:db8::1" {
		t.Fatalf("kind %q sender %q ok %v from %q", k, s, ok, tok)
	}
	if k, s, ok := splitToken(queueToken(kindStore, "")); !ok || k != kindStore || s != "" {
		t.Fatalf("kind %q sender %q ok %v for an empty sender", k, s, ok)
	}
	if _, _, ok := splitToken("no-colons"); ok {
		t.Fatal("a token without separators was split")
	}
}
