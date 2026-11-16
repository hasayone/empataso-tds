package logbuf

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hangThenFail holds its first insert until release and then fails it with an
// error that leaves the fate of the rows unknown. Later calls fail while failing
// is set and go to inner otherwise.
type hangThenFail struct {
	inner   *storeInserter
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	failing atomic.Bool
}

func newHangThenFail() *hangThenFail {
	return &hangThenFail{inner: newStoreInserter(), entered: make(chan struct{}), release: make(chan struct{})}
}

func (h *hangThenFail) InsertEvents(ctx context.Context, batch []Event) error {
	first := false
	h.once.Do(func() { first = true; close(h.entered) })
	if first {
		<-h.release
		return errors.New("timeout waiting for the answer")
	}
	if h.failing.Load() {
		return errors.New("timeout waiting for the answer")
	}
	return h.inner.InsertEvents(ctx, batch)
}

// closeWithin closes b with a generous budget and returns how long it took.
func closeWithin(t *testing.T, b *Buffer) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := b.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return time.Since(start)
}

// Where the table cannot deduplicate, a batch that may be stored is lost once,
// and the writer goes down: the batches behind it wait on disk instead of each
// going through the same failing insert and being lost in turn.
func TestNoDedupFailureParksTheBatchesBehindIt(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newHangThenFail()
	b := NewWith(ins, Options{
		Capacity: 1000, BatchSize: 20, FlushEvery: 10 * time.Millisecond, Log: quietLogger(), Spool: q,
		DedupReady: func() bool { return false },
		PauseMin:   700 * time.Millisecond, PauseMax: 700 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go b.Run(ctx)
	t.Cleanup(func() {
		cancel()
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = b.Close(cctx)
	})

	pushN(b, "a", 20)
	select {
	case <-ins.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}
	pushN(b, "b", 100)
	waitLong(t, "five batches queued behind the stuck insert", func() bool { return b.Stats().QueueLen == 5 })

	ins.failing.Store(true)
	close(ins.release)
	waitLong(t, "the queued batches parked", func() bool { return q.Stats().Events == 100 })
	ins.failing.Store(false)

	waitLong(t, "the parked batches written", func() bool { r, _ := ins.inner.distinct(); return r == 100 })
	if l := b.Losses(); l.Insert != 20 || l.Total() != 20 {
		t.Fatalf("losses %+v, want exactly the 20 events of the batch in doubt as Insert", l)
	}
	if _, dups := ins.inner.distinct(); dups != 0 {
		t.Fatalf("%d duplicate rows", dups)
	}
}

// A segment whose body is damaged or whose record count disagrees with its
// header is set aside and the events the header promised are counted as lost.
// One whose header is unreadable has no count and shows only as rejected.
func TestRejectedSegmentsAreCountedAsLosses(t *testing.T) {
	root := t.TempDir()
	two, err := encodeEvents([]Event{{CID: "x1", Stream: "s"}, {CID: "x2", Stream: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	damaged, err := encodeSegment(SegmentMeta{Token: "t-damaged", Count: 3}, bytes.Repeat([]byte("payload "), 50))
	if err != nil {
		t.Fatal(err)
	}
	damaged[len(damaged)-10] ^= 0xff
	writeRaw(t, root, "engine", "00000000000000000001-0001.seg", damaged)
	mismatch, err := encodeSegment(SegmentMeta{Token: "t-mismatch", Count: 3}, two)
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, root, "engine", "00000000000000000002-0002.seg", mismatch)
	unreadable, err := encodeSegment(SegmentMeta{Token: "t-old", Count: 5}, []byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	unreadable[4] = 9
	writeRaw(t, root, "engine", "00000000000000000003-0003.seg", unreadable)

	_, q := newPair(t, root, "engine")
	ins := newStoreInserter()
	b := newSpoolBuffer(t, ins, q, nil)
	waitLong(t, "both readable-header segments rejected", func() bool { return b.Losses().Insert == 6 })
	if st := b.Stats().Spool; st.Rejected != 3 || st.Segments != 0 {
		t.Fatalf("spool %+v, want all three rejected and kept", st)
	}
	if l := b.Losses(); l.Total() != 6 {
		t.Fatalf("losses %+v, want the 3+3 promised events as Insert and nothing for the unreadable header", l)
	}
	pushN(b, "h", 20)
	waitLong(t, "healthy events written", func() bool { r, _ := ins.distinct(); return r == 20 })
}

// gateInserter holds its first call until release and records every token.
type gateInserter struct {
	inner   *storeInserter
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gateInserter) InsertEvents(ctx context.Context, batch []Event) error {
	g.once.Do(func() { close(g.entered); <-g.release })
	return g.inner.InsertEvents(ctx, batch)
}

// Segments marked as possibly stored are written first, before older unsent
// ones and before fresh batches, so a repeat under the same token stays inside
// the deduplication window of the table.
func TestSentSegmentsAreWrittenFirst(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := &gateInserter{inner: newStoreInserter(), entered: make(chan struct{}), release: make(chan struct{})}
	b := newSpoolBuffer(t, ins, q, nil)

	pushN(b, "f", 20)
	select {
	case <-ins.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the fresh batch never reached the storage")
	}
	putSegment(t, q, "t-unsent-old", false, Event{CID: "u1", Stream: "s"})
	putSegment(t, q, "t-sent-1", true, Event{CID: "s1", Stream: "s"})
	putSegment(t, q, "t-unsent-new", false, Event{CID: "u2", Stream: "s"})
	putSegment(t, q, "t-sent-2", true, Event{CID: "s2", Stream: "s"})
	pushN(b, "g", 20)
	close(ins.release)

	waitLong(t, "everything written", func() bool { r, _ := ins.inner.distinct(); return r == 44 })
	index := map[string]int{}
	for i, tok := range ins.inner.tokenLog() {
		if _, seen := index[tok]; !seen {
			index[tok] = i
		}
	}
	for _, sent := range []string{"t-sent-1", "t-sent-2"} {
		for _, other := range []string{"t-unsent-old", "t-unsent-new"} {
			if index[sent] > index[other] {
				t.Fatalf("%s was written after %s: order %v", sent, other, ins.inner.tokenLog())
			}
		}
	}
	if index["t-sent-1"] > index["t-sent-2"] {
		t.Fatalf("sent segments are not oldest first: %v", ins.inner.tokenLog())
	}
	if toks := ins.inner.tokenLog(); toks[1] != "t-sent-1" {
		t.Fatalf("the first write after the fresh batch was %q, want t-sent-1: %v", toks[1], toks)
	}
}

// An insert that hangs does not hold Close for its whole budget: the batch in
// flight and the queued ones are parked within seconds and nothing is lost.
func TestCloseDoesNotWaitForAHungInsert(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newCtxRespectingInserter()
	b := NewWith(ins, Options{
		Capacity: 100, BatchSize: 10, FlushEvery: time.Hour, Log: quietLogger(), Spool: q,
		PauseMin: 5 * time.Millisecond, PauseMax: 40 * time.Millisecond,
	})
	go b.Run(context.Background())
	pushN(b, "h", 10)
	select {
	case <-ins.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}
	pushN(b, "q", 30)
	waitLong(t, "three batches queued", func() bool { return b.Stats().QueueLen == 3 })

	if took := closeWithin(t, b); took > 3*time.Second {
		t.Fatalf("Close took %v with a hung insert, want under 3s", took)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v, everything belongs on disk", l)
	}
	if got := q.Stats().Events; got != 40 {
		t.Fatalf("%d events in the spool, want 40", got)
	}
}

// scriptedInserter fails its first call, then hangs every later call until its
// context ends: a storage that went down and whose probe never answers.
type scriptedInserter struct {
	calls   atomic.Int32
	probing chan struct{}
	once    sync.Once
}

func (s *scriptedInserter) InsertEvents(ctx context.Context, _ []Event) error {
	if s.calls.Add(1) == 1 {
		return errors.New("clickhouse is down")
	}
	s.once.Do(func() { close(s.probing) })
	<-ctx.Done()
	return ctx.Err()
}

// A writer that is already down has no reason to wait for its probe: Close
// cancels it at once and parks.
func TestCloseCancelsAProbeOfADownStorageAtOnce(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := &scriptedInserter{probing: make(chan struct{})}
	b := NewWith(ins, Options{
		Capacity: 100, BatchSize: 10, FlushEvery: 10 * time.Millisecond, Log: quietLogger(), Spool: q,
		RetryAttempts: 1, PauseMin: 20 * time.Millisecond, PauseMax: 20 * time.Millisecond,
	})
	go b.Run(context.Background())
	pushN(b, "p", 10)
	select {
	case <-ins.probing:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never probed the down storage")
	}
	if took := closeWithin(t, b); took > closeTry/2 {
		t.Fatalf("Close took %v with the writer down, want it at once", took)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v", l)
	}
	if got := q.Stats().Events; got != 10 {
		t.Fatalf("%d events in the spool, want 10", got)
	}
}

// deafInserter ignores its context: it returns only when released.
type deafInserter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *deafInserter) InsertEvents(context.Context, []Event) error {
	d.once.Do(func() { close(d.entered) })
	<-d.release
	return nil
}

// When the writer ignores the cancellation, what is still in memory when Close
// gives up is counted as queue loss and not forgotten.
func TestCloseCountsWhatIsLeftWhenTheWriterIgnoresCancellation(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := &deafInserter{entered: make(chan struct{}), release: make(chan struct{})}
	b := NewWith(ins, Options{
		Capacity: 100, BatchSize: 10, FlushEvery: time.Hour, Log: quietLogger(), Spool: q,
	})
	go b.Run(context.Background())
	var release sync.Once
	finish := func() {
		release.Do(func() { close(ins.release) })
		select {
		case <-b.allDone:
		case <-time.After(5 * time.Second):
			t.Error("the writer did not finish after the insert was released")
		}
	}
	t.Cleanup(finish)
	pushN(b, "d", 10)
	select {
	case <-ins.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}
	pushN(b, "e", 20)
	waitLong(t, "two batches queued", func() bool { return b.Stats().QueueLen == 2 })

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := b.Close(short); err == nil {
		t.Fatal("Close must report that the deadline ran out")
	}
	if l := b.Losses(); l.Queue != 30 || l.Total() != 30 {
		t.Fatalf("losses %+v, want the 30 events still in memory as Queue", l)
	}

	// The writer gets to them after all: written or parked, they are not lost.
	finish()
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v after the writer handled everything, want none", l)
	}
	if got, stored := q.Stats().Events, b.Stats().Inserted; got+stored != 30 {
		t.Fatalf("%d parked and %d inserted, want 30 in all", got, stored)
	}
}

// Wake drops the pause before the next probe: batches parked while there was no
// connection are written as soon as one exists. A storage that is not connected
// is not a failed insert.
func TestWakeProbesAtOnceAndNotConnectedIsNotAFailure(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newStoreInserter()
	ins.notConnected.Store(true)
	b := NewWith(ins, Options{
		Capacity: 100, BatchSize: 20, FlushEvery: 10 * time.Millisecond, Log: quietLogger(), Spool: q,
		PauseMin: time.Hour, PauseMax: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go b.Run(ctx)
	t.Cleanup(func() {
		cancel()
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = b.Close(cctx)
	})

	pushN(b, "w", 20)
	waitLong(t, "the batch parked", func() bool { return q.Stats().Events == 20 })
	if s := b.Stats(); s.InsertErrors != 0 || !s.FailingSince.IsZero() {
		t.Fatalf("stats %+v: a missing connection must not count as a failed insert", s)
	}

	ins.notConnected.Store(false)
	b.Wake()
	waitLong(t, "the parked batch written without waiting for the pause", func() bool { r, _ := ins.distinct(); return r == 20 })
}
