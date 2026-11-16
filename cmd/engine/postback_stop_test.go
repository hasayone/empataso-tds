package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/logbuf"
	"github.com/egerkuzma/emtds/internal/store"
)

// stopEnv returns engine deps with a postback queue, an outbound queue and a
// journal writer over one temp spool, a fake store that holds every conversion
// until its hold channel is closed, and a campaign with one outbound template.
func stopEnv(t *testing.T) (*engineDeps, *fakeConv) {
	t.Helper()
	cs := &fakeConv{
		res:     store.ConversionResult{GroupID: "g1", Fresh: true},
		hold:    make(chan struct{}),
		entered: make(chan struct{}, 8),
	}
	d := newOutboundEnv(t)
	d.convOverride = cs
	d.plog = newPostbackLog(d, nil, discardLog())
	return d, cs
}

// TestStopWaitsForWorkersBeforeClosingJournal checks the order of the stop: a
// postback being written when the stop begins is finished, its outbound job is
// on disk before its segment is removed, and its journal line is written because
// the journal is closed after the workers ended.
func TestStopWaitsForWorkersBeforeClosingJournal(t *testing.T) {
	d, cs := stopEnv(t)
	if err := d.pbq.accept(spooledPostback{CID: "abc", Status: "sale", Profit: 2, At: time.Now()}, kindStore, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.pbq.start(ctx)
	select {
	case <-cs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no worker picked the postback up")
	}

	stopped := make(chan struct{})
	go func() {
		d.stopBackground(cancel, time.Now().Add(10*time.Second), discardLog())
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("the stop returned while a postback was being written")
	case <-time.After(300 * time.Millisecond):
	}
	close(cs.hold)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the stop did not finish after the postback was written")
	}

	if got := len(d.pbq.q.List()); got != 0 {
		t.Fatalf("%d postback segments left, want 0 after the conversion was stored", got)
	}
	if got := d.outSpool.snapshot().Segments; got != 1 {
		t.Fatalf("%d outbound jobs on disk, want exactly 1", got)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var replayed bool
	for _, batch := range cs.logged {
		for _, r := range batch {
			replayed = replayed || r.Outcome == "replayed"
		}
	}
	if !replayed {
		t.Fatalf("the journal was closed before the worker wrote its line: %+v", cs.logged)
	}
}

// TestStopWithoutQueuesReturns checks that the stop of an engine with no queues
// and no journal cancels the context and returns at once.
func TestStopWithoutQueuesReturns(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	ctx, cancel := context.WithCancel(context.Background())
	d.goBG(func() { <-ctx.Done() })
	start := time.Now()
	d.stopBackground(cancel, time.Now().Add(5*time.Second), discardLog())
	if ctx.Err() == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("ctx.Err()=%v after %v", ctx.Err(), time.Since(start))
	}
}

// TestStopGivesUpAtTheDeadline checks that a worker that does not end holds the
// stop only until the deadline, and that the journal is still closed.
func TestStopGivesUpAtTheDeadline(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	d.plog = newPostbackLog(d, nil, discardLog())
	release := make(chan struct{})
	defer close(release)
	d.goBG(func() { <-release })
	_, cancel := context.WithCancel(context.Background())
	start := time.Now()
	d.stopBackground(cancel, time.Now().Add(300*time.Millisecond), discardLog())
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the stop took %v past a deadline of 300ms", took)
	}
	d.plog.note(store.PostbackLogRow{Subid: "late"})
	if d.plog.lost.Load() != 1 {
		t.Fatal("the journal was not closed")
	}
}

// TestPostbackHandlerHasTotalDeadline checks that the store call of the handler
// runs under a deadline no longer than the one of the handler, and that the
// handler deadline is the 3 s of the store plus a short allowance.
func TestPostbackHandlerHasTotalDeadline(t *testing.T) {
	if postbackHandlerTimeout <= postbackStoreTimeout || postbackHandlerTimeout > postbackStoreTimeout+2*time.Second {
		t.Fatalf("handler timeout %v is not the store timeout %v plus a short allowance", postbackHandlerTimeout, postbackStoreTimeout)
	}
	cs := &fakeConv{}
	d := &engineDeps{log: discardLog(), convOverride: cs}
	if rec := sendPostback(d, "/postback?subid=abc&status=lead"); rec.Code != http.StatusOK {
		t.Fatalf("answer %d, want 200", rec.Code)
	}
	if ttl := cs.ttls[0]; ttl <= 0 || ttl > postbackStoreTimeout {
		t.Fatalf("store call deadline %v, want within (0, %v]", ttl, postbackStoreTimeout)
	}
}

// TestEventFlowBusyAndHeldSegments checks eventFlowOf: a segment of events on
// disk is busy, a missing storage connection is busy, and a segment the buffer
// holds on purpose (sent, with no deduplication), one set aside in rejected, or
// an event waiting in a healthy buffer for the regular flush is not.
func TestEventFlowBusyAndHeldSegments(t *testing.T) {
	d := &engineDeps{log: discardLog()}
	sp, err := logbuf.OpenSpool(logbuf.SpoolConfig{
		Root: t.TempDir(), Instance: "engine", MaxBytes: 1 << 30, MinFreePct: 1,
		Free: func(string) (uint64, uint64, error) { return 900, 1000, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sp.Close)
	if f := d.eventFlowOf(sp); f.Busy {
		t.Fatalf("an engine with no events is busy: %+v", f)
	}

	eq, err := sp.Queue(logbuf.EventsKind)
	if err != nil {
		t.Fatal(err)
	}
	if err := eq.Put(logbuf.SegmentMeta{Token: "held", Count: 1, Sent: true}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if f := d.eventFlowOf(sp); f.Busy {
		t.Fatal("a segment held for the lack of deduplication keeps the flow busy")
	}
	if err := eq.Put(logbuf.SegmentMeta{Token: "wait", Count: 1}, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if f := d.eventFlowOf(sp); !f.Busy {
		t.Fatal("an unsent segment on disk does not make the flow busy")
	}
	for _, seg := range eq.List() {
		if seg.Token == "wait" {
			eq.Reject(seg, nil)
		}
	}
	if f := d.eventFlowOf(sp); f.Busy {
		t.Fatal("a rejected segment keeps the flow busy")
	}

	up := false
	d.logs = logbuf.NewWith(countingInserter{}, logbuf.Options{
		Capacity: 100, BatchSize: 10, FlushEvery: time.Hour, Log: discardLog(),
		RetryAttempts: 1, Connected: func() bool { return up },
	})
	d.logs.Push(logbuf.Event{CID: "c1"})
	if f := d.eventFlowOf(sp); !f.Busy {
		t.Fatal("a buffer with no storage connection is not busy")
	}
	up = true
	if f := d.eventFlowOf(sp); f.Busy {
		t.Fatal("an event waiting for the regular flush makes the flow busy")
	}
}
