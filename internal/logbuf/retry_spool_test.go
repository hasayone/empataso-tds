package logbuf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// storeInserter is a storage that deduplicates by token, like the events table
// with its window on. It records every row it holds by CID, so a duplicate and
// a missing row are both visible, and every token it was asked to write.
type storeInserter struct {
	mu     sync.Mutex
	seen   map[string]int // CID -> times stored
	blocks map[string]bool
	tokens []string
	calls  int

	dedup atomic.Bool // drop a block whose token was stored already
	// down fails every call without storing; notConnected does so with the
	// error that proves nothing was sent.
	down, notConnected atomic.Bool
	// storeThenFail makes the next N calls store the rows and then fail, as an
	// insert does whose answer is lost to a timeout.
	storeThenFail atomic.Int32
	// poison fails every batch holding an event of this Stream, storing nothing.
	poison atomic.Value
	// poisonPlain makes the poison failure an ordinary error, a timeout say,
	// instead of one that comes from the data.
	poisonPlain atomic.Bool
}

func newStoreInserter() *storeInserter {
	s := &storeInserter{seen: map[string]int{}, blocks: map[string]bool{}}
	s.dedup.Store(true)
	return s
}

func (s *storeInserter) InsertEvents(ctx context.Context, batch []Event) error {
	tok := DedupToken(ctx)
	s.mu.Lock()
	s.calls++
	s.tokens = append(s.tokens, tok)
	s.mu.Unlock()
	if s.notConnected.Load() {
		return ErrNotConnected
	}
	if s.down.Load() {
		return errors.New("clickhouse is down")
	}
	if p, _ := s.poison.Load().(string); p != "" {
		for _, e := range batch {
			if e.Stream == p {
				if s.poisonPlain.Load() {
					return errors.New("timeout waiting for the answer")
				}
				return BadData(errors.New("cannot parse the block"))
			}
		}
	}
	s.mu.Lock()
	if !(s.dedup.Load() && tok != "" && s.blocks[tok]) {
		for _, e := range batch {
			s.seen[e.CID]++
		}
		s.blocks[tok] = true
	}
	s.mu.Unlock()
	if n := s.storeThenFail.Load(); n > 0 {
		s.storeThenFail.Store(n - 1)
		return errors.New("timeout waiting for the answer")
	}
	return nil
}

// distinct returns how many different CIDs are stored, and how many extra
// copies of any of them.
func (s *storeInserter) distinct() (rows, dups int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.seen {
		rows++
		dups += n - 1
	}
	return rows, dups
}

func (s *storeInserter) tokenLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...)
}

// waitLong polls cond for up to ten seconds.
func waitLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("not met within 10s: %s", what)
}

// newSpoolBuffer starts a buffer with short pauses on queue q. Its Run stops
// and the buffer is closed with the test.
func newSpoolBuffer(t *testing.T, ins Inserter, q *SpoolQueue, dedup func() bool) *Buffer {
	t.Helper()
	b := NewWith(ins, Options{
		Capacity: 1000, BatchSize: 20, FlushEvery: 10 * time.Millisecond, Log: quietLogger(),
		Spool: q, DedupReady: dedup, PauseMin: 5 * time.Millisecond, PauseMax: 40 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go b.Run(ctx)
	t.Cleanup(func() {
		cancel()
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = b.Close(cctx)
	})
	return b
}

// pushN pushes n events with CIDs <prefix>0 .. <prefix>n-1.
func pushN(b *Buffer, prefix string, n int) {
	for i := 0; i < n; i++ {
		b.Push(Event{CID: fmt.Sprintf("%s%d", prefix, i), Stream: "s"})
	}
}

// An outage in the middle of traffic loses nothing: what the storage refuses
// waits on disk and is written exactly once after it comes back.
func TestSpoolOutageLosesNothing(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newStoreInserter()
	b := newSpoolBuffer(t, ins, q, nil)

	pushN(b, "a", 100)
	waitLong(t, "first events stored", func() bool { r, _ := ins.distinct(); return r == 100 })

	ins.down.Store(true)
	for w := 0; w < 12; w++ {
		pushN(b, fmt.Sprintf("o%d-", w), 50)
		time.Sleep(40 * time.Millisecond)
	}
	waitLong(t, "events parked on disk", func() bool { return b.Stats().Spool.Events > 0 })
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses during the outage: %+v", l)
	}
	if s := b.Stats(); s.Pending < 600 {
		t.Fatalf("pending %d during the outage, the parked events must count", s.Pending)
	}

	ins.down.Store(false)
	waitLong(t, "everything written", func() bool { r, _ := ins.distinct(); return r == 700 })
	waitLong(t, "spool empty", func() bool { return b.Stats().Spool.Segments == 0 })
	if _, dups := ins.distinct(); dups != 0 {
		t.Fatalf("%d duplicate rows", dups)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v", l)
	}
	if s := b.Stats(); s.Pending != 0 {
		t.Fatalf("pending %d after recovery", s.Pending)
	}
}

// newPair opens a spool and its events queue under root for instance.
func newPair(t *testing.T, root, instance string) (*Spool, *SpoolQueue) {
	t.Helper()
	s := openTestSpool(t, root, instance, nil)
	return s, testQueue(t, s)
}

// A connection that breaks on one batch is repeated with the same token, and a
// repeat of an insert that did land does not write it twice.
func TestRetryKeepsTheTokenAndDoesNotDuplicate(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newStoreInserter()
	ins.storeThenFail.Store(1) // the first insert is stored, then reports a timeout
	b := newSpoolBuffer(t, ins, q, nil)

	pushN(b, "x", 20)
	waitLong(t, "two attempts", func() bool { return len(ins.tokenLog()) >= 2 })
	waitLong(t, "stats show the success", func() bool { return b.Stats().Inserted == 20 })
	tokens := ins.tokenLog()
	if tokens[0] == "" || tokens[0] != tokens[1] {
		t.Fatalf("attempt tokens %q: the repeat must carry the first token", tokens)
	}
	if rows, dups := ins.distinct(); rows != 20 || dups != 0 {
		t.Fatalf("rows=%d dups=%d, want 20 and 0", rows, dups)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v", l)
	}
}

// A batch that fails and is not stored at all is repeated and lands.
func TestRetryAfterPlainFailure(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newStoreInserter()
	ins.down.Store(true)
	b := newSpoolBuffer(t, ins, q, nil)
	pushN(b, "p", 20)
	waitLong(t, "a failed attempt", func() bool { return b.Stats().InsertErrors >= 1 })
	ins.down.Store(false)
	waitLong(t, "stored", func() bool { r, _ := ins.distinct(); return r == 20 })
	if _, dups := ins.distinct(); dups != 0 {
		t.Fatalf("%d duplicates", dups)
	}
}

// Without deduplication a batch that may be stored is not repeated: the loss
// is counted, no row is written twice. A batch that was never sent still waits
// on disk and is written.
func TestNoDedupSentBatchIsNotRepeated(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newStoreInserter()
	ins.dedup.Store(false)
	ins.storeThenFail.Store(1)
	b := newSpoolBuffer(t, ins, q, func() bool { return false })

	pushN(b, "s", 20)
	waitLong(t, "the loss is counted", func() bool { return b.Losses().Insert == 20 })
	if rows, dups := ins.distinct(); rows != 20 || dups != 0 {
		t.Fatalf("rows=%d dups=%d: the stored batch must stay single", rows, dups)
	}
	if calls := len(ins.tokenLog()); calls != 1 {
		t.Fatalf("%d attempts, want exactly one", calls)
	}

	ins.notConnected.Store(true)
	pushN(b, "n", 40)
	waitLong(t, "never-sent batches parked", func() bool { return b.Stats().Spool.Events == 40 })
	if l := b.Losses(); l.Insert != 20 || l.Total() != 20 {
		t.Fatalf("losses %+v, the unsent batches must not be lost", l)
	}
	ins.notConnected.Store(false)
	waitLong(t, "unsent batches written", func() bool { r, _ := ins.distinct(); return r == 60 })
	if _, dups := ins.distinct(); dups != 0 {
		t.Fatalf("%d duplicates", dups)
	}
}

// A segment marked as possibly stored stays on disk while the table cannot
// deduplicate, and is written once it can.
func TestNoDedupHoldsSentSegments(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	payload, err := encodeEvents([]Event{{CID: "held", Stream: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Put(SegmentMeta{Token: "t-held", Count: 1, Sent: true}, payload); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	ins := newStoreInserter()
	newSpoolBuffer(t, ins, q, ready.Load)
	time.Sleep(150 * time.Millisecond)
	if rows, _ := ins.distinct(); rows != 0 || q.Stats().Segments != 1 {
		t.Fatalf("a possibly-stored segment was replayed without dedup: rows=%d segments=%d", rows, q.Stats().Segments)
	}
	ready.Store(true)
	waitLong(t, "held segment written", func() bool { r, _ := ins.distinct(); return r == 1 })
	waitLong(t, "segment removed", func() bool { return q.Stats().Segments == 0 })
}

// Stopping during an outage writes everything the buffer holds to the spool,
// and a restart on the same directory writes it to the storage.
func TestCloseDuringOutageParksEverythingAndRestartReplays(t *testing.T) {
	root := t.TempDir()
	s, q := newPair(t, root, "engine")
	ins := newStoreInserter()
	ins.down.Store(true)
	b := NewWith(ins, Options{
		Capacity: 1000, BatchSize: 20, FlushEvery: time.Hour, Log: quietLogger(), Spool: q,
		PauseMin: 5 * time.Millisecond, PauseMax: 40 * time.Millisecond,
	})
	go b.Run(context.Background())

	pushN(b, "c", 130) // six full batches and a partial one held by the accumulator
	waitLong(t, "a failed attempt", func() bool { return b.Stats().InsertErrors >= 1 })
	pushN(b, "d", 7) // still in the input channel or accumulator

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses on shutdown: %+v", l)
	}
	if got := q.Stats().Events; got != 137 {
		t.Fatalf("%d events in the spool, want 137", got)
	}
	s.Close()

	// A new process: same directory, a storage that works.
	_, q2 := newPair(t, root, "engine")
	if got := q2.Stats().Events; got != 137 {
		t.Fatalf("%d events found after restart, want 137", got)
	}
	ins2 := newStoreInserter()
	b2 := newSpoolBuffer(t, ins2, q2, nil)
	waitLong(t, "replay", func() bool { r, _ := ins2.distinct(); return r == 137 })
	waitLong(t, "spool empty", func() bool { return b2.Stats().Spool.Segments == 0 })
}

// An in-flight batch that Close has to cancel is parked, not counted as lost.
func TestCloseParksTheBatchInFlight(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newCtxRespectingInserter()
	b := NewWith(ins, Options{
		Capacity: 100, BatchSize: 1000, FlushEvery: time.Hour, Log: quietLogger(), Spool: q,
		PauseMin: 5 * time.Millisecond, PauseMax: 40 * time.Millisecond,
	})
	go b.Run(context.Background())
	pushN(b, "f", 9)
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := b.Close(short); err == nil {
		t.Fatal("Close must report that the deadline ran out")
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v, the batch must be parked", l)
	}
	if got := q.Stats(); got.Events != 9 {
		t.Fatalf("spool %+v, want the 9 events", got)
	}
	seg, _ := q.Oldest(nil)
	if !seg.Sent {
		t.Fatal("a batch cancelled in flight may be stored: the segment must say so")
	}
}

// The segments of a stopped instance are taken by the next engine and written.
func TestForeignSegmentsAreWrittenByTheLiveEngine(t *testing.T) {
	root := t.TempDir()
	old, oq := newPair(t, root, "engine2")
	ins0 := newStoreInserter()
	ins0.down.Store(true)
	b0 := NewWith(ins0, Options{
		Capacity: 100, BatchSize: 10, FlushEvery: 10 * time.Millisecond, Log: quietLogger(), Spool: oq,
		PauseMin: 5 * time.Millisecond, PauseMax: 40 * time.Millisecond,
	})
	go b0.Run(context.Background())
	pushN(b0, "z", 30)
	waitLong(t, "parked", func() bool { return oq.Stats().Events == 30 })
	if err := b0.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	old.Close()

	_, q := newPair(t, root, "engine")
	ins := newStoreInserter()
	newSpoolBuffer(t, ins, q, nil)
	waitLong(t, "adopted events written", func() bool { r, _ := ins.distinct(); return r == 30 })
	if _, err := os.Stat(filepath.Join(root, "engine2")); !os.IsNotExist(err) {
		t.Fatalf("the adopted directory is still there: %v", err)
	}
}

// A full spool refuses the batch and the events are counted as SpoolFull.
func TestSpoolFullIsACountedLoss(t *testing.T) {
	root := t.TempDir()
	s := openTestSpool(t, root, "engine", func(c *SpoolConfig) { c.MaxBytes = 600 })
	q := testQueue(t, s)
	ins := newStoreInserter()
	ins.notConnected.Store(true)
	b := newSpoolBuffer(t, ins, q, nil)
	for w := 0; w < 20; w++ {
		pushN(b, fmt.Sprintf("w%d-", w), 20)
		time.Sleep(15 * time.Millisecond)
	}
	waitLong(t, "SpoolFull counted", func() bool { return b.Losses().SpoolFull > 0 })
	if l := b.Losses(); l.Insert != 0 || l.Queue != 0 {
		t.Fatalf("losses %+v, the spool is the only cause", l)
	}
	if st := q.Stats(); st.Bytes > 600 {
		t.Fatalf("spool holds %d bytes over a ceiling of 600", st.Bytes)
	}
}

// The free-space floor, read from the (replaced) volume statistics, also
// refuses a batch.
func TestSpoolFreeSpaceFloorIsACountedLoss(t *testing.T) {
	s := openTestSpool(t, t.TempDir(), "engine", func(c *SpoolConfig) {
		c.MinFreePct = 10
		c.Free = func(string) (uint64, uint64, error) { return 50, 1000, nil }
	})
	ins := newStoreInserter()
	ins.notConnected.Store(true)
	b := newSpoolBuffer(t, ins, testQueue(t, s), nil)
	pushN(b, "f", 20)
	waitLong(t, "SpoolFull counted", func() bool { return b.Losses().SpoolFull == 20 })
	if b.Stats().Spool.Segments != 0 {
		t.Fatal("a segment was written below the free-space floor")
	}
}

// When the queue behind a stuck writer is full, batches go to disk through the
// spill goroutine instead of being dropped.
func TestQueueOverflowGoesToTheSpool(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newBlockingInserter()
	b := NewWith(ins, Options{
		Capacity: 40, BatchSize: 10, FlushEvery: time.Hour, Log: quietLogger(), Spool: q,
	})
	ctx, cancel := context.WithCancel(context.Background())
	var release sync.Once
	t.Cleanup(func() {
		release.Do(func() { close(ins.release) })
		cancel()
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = b.Close(cctx)
	})
	go b.Run(ctx)
	pushN(b, "first", 10)
	select {
	case <-ins.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}
	for i := 0; i < 12; i++ {
		pushN(b, fmt.Sprintf("o%d-", i), 10)
		time.Sleep(30 * time.Millisecond)
	}
	waitLong(t, "overflow parked", func() bool { return q.Stats().Events > 0 })
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v, the overflow belongs on disk", l)
	}
	release.Do(func() { close(ins.release) })
	waitLong(t, "all inserted", func() bool { s := b.Stats(); return s.Inserted == 130 && s.Pending == 0 })
}

// A segment the storage refuses for good is moved aside after a few failures
// and does not hold up the rest.
func TestPoisonSegmentIsQuarantined(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	payload, err := encodeEvents([]Event{{CID: "bad", Stream: "poison"}, {CID: "bad2", Stream: "poison"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Put(SegmentMeta{Token: "t-poison", Count: 2}, payload); err != nil {
		t.Fatal(err)
	}
	ins := newStoreInserter()
	ins.poison.Store("poison")
	b := newSpoolBuffer(t, ins, q, nil)

	stop := make(chan struct{})
	var pushed atomic.Int64
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			b.Push(Event{CID: fmt.Sprintf("g%d", i), Stream: "s"})
			pushed.Add(1)
			time.Sleep(2 * time.Millisecond)
		}
	}()
	waitLong(t, "the poison segment is rejected", func() bool { return q.Stats().Rejected == 1 })
	close(stop)
	if l := b.Losses(); l.Insert != 2 || l.Total() != 2 {
		t.Fatalf("losses %+v, want the two poisoned events as Insert", l)
	}
	waitLong(t, "the healthy events are written", func() bool {
		r, _ := ins.distinct()
		return int64(r) == pushed.Load() && b.Stats().Pending == 0
	})
}

// An outage of the whole storage must not quarantine the segment that happens
// to be probed: with no other insert succeeding it is not poison.
func TestOutageDoesNotQuarantineSegments(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	payload, _ := encodeEvents([]Event{{CID: "keep", Stream: "s"}})
	if err := q.Put(SegmentMeta{Token: "t-keep", Count: 1}, payload); err != nil {
		t.Fatal(err)
	}
	ins := newStoreInserter()
	ins.down.Store(true)
	newSpoolBuffer(t, ins, q, nil)
	waitLong(t, "many probes", func() bool { return len(ins.tokenLog()) >= 3*poisonAfter })
	if st := q.Stats(); st.Rejected != 0 || st.Segments != 1 {
		t.Fatalf("spool %+v: the outage rejected a good segment", st)
	}
	ins.down.Store(false)
	waitLong(t, "segment written", func() bool { r, _ := ins.distinct(); return r == 1 })
}

// A segment of an unknown version is set aside at start, counted, kept, and
// the buffer works around it.
func TestUnknownVersionSegmentDoesNotBlockTheBuffer(t *testing.T) {
	root := t.TempDir()
	data, err := encodeSegment(SegmentMeta{Token: "old", Count: 1}, []byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	data[4] = 7
	writeRaw(t, root, "engine", "00000000000000000009-0009.seg", data)
	_, q := newPair(t, root, "engine")
	ins := newStoreInserter()
	b := newSpoolBuffer(t, ins, q, nil)
	pushN(b, "h", 20)
	waitLong(t, "healthy events written", func() bool { r, _ := ins.distinct(); return r == 20 })
	if st := b.Stats().Spool; st.Rejected != 1 || st.Segments != 0 {
		t.Fatalf("spool %+v, want the old segment rejected and kept", st)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v: a header that cannot be read has no count to lose", l)
	}
	if _, err := os.Stat(filepath.Join(root, "engine", EventsKind, rejectedDir, "00000000000000000009-0009.seg")); err != nil {
		t.Fatalf("the old segment was not kept: %v", err)
	}
}

// Stats carries the dedup and connection flags the metrics file shows.
func TestStatsReportDedupAndConnection(t *testing.T) {
	var dedup, conn atomic.Bool
	b := NewWith(newStoreInserter(), Options{Log: quietLogger(), DedupReady: dedup.Load, Connected: conn.Load})
	go b.Run(context.Background())
	if s := b.Stats(); s.DedupReady || s.Connected {
		t.Fatalf("stats %+v, want both false", s)
	}
	dedup.Store(true)
	conn.Store(true)
	if s := b.Stats(); !s.DedupReady || !s.Connected {
		t.Fatalf("stats %+v, want both true", s)
	}
	_ = b.Close(context.Background())
}

func TestEventsRoundTripThroughASegment(t *testing.T) {
	in := []Event{{CID: "a", Stream: "s", Ts: time.Unix(1700000000, 5).UTC(), Subs: [30]string{0: "x", 29: "y"}, CostCents: 7}}
	raw, err := encodeEvents(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeEvents(raw, 1)
	if err != nil || len(out) != 1 || out[0] != in[0] {
		t.Fatalf("round trip: %+v err=%v", out, err)
	}
	if _, err := decodeEvents(raw, 2); err == nil {
		t.Fatal("a count that differs from the header must be refused")
	}
}

// pushUntil pushes one healthy event every two milliseconds until stop is
// called, so the storage keeps accepting inserts while a segment fails. It
// returns stop and the number of events pushed.
func pushUntil(b *Buffer) (stop func(), pushed *atomic.Int64) {
	quit := make(chan struct{})
	done := make(chan struct{})
	pushed = new(atomic.Int64)
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-quit:
				return
			default:
			}
			b.Push(Event{CID: fmt.Sprintf("h%d", i), Stream: "s"})
			pushed.Add(1)
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return func() { close(quit); <-done }, pushed
}

// putSegment parks events on q under token, marked sent or not.
func putSegment(t *testing.T, q *SpoolQueue, token string, sent bool, events ...Event) {
	t.Helper()
	payload, err := encodeEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Put(SegmentMeta{Token: token, Count: len(events), Sent: sent}, payload); err != nil {
		t.Fatal(err)
	}
}

// hasToken reports whether a pending segment of q carries token.
func hasToken(q *SpoolQueue, token string) bool {
	found := false
	q.Oldest(func(s Segment) bool {
		found = found || s.Token == token
		return true
	})
	return found
}

// When Close runs out of time with a slow insert in flight and the table cannot
// deduplicate, only the batch in flight is lost; the batches still queued were
// never sent and go to the spool.
func TestCloseKeepsUnsentBatchesWithoutDedup(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	ins := newCtxRespectingInserter()
	b := NewWith(ins, Options{
		Capacity: 100, BatchSize: 10, FlushEvery: time.Hour, Log: quietLogger(), Spool: q,
		DedupReady: func() bool { return false },
		PauseMin:   5 * time.Millisecond, PauseMax: 40 * time.Millisecond,
	})
	go b.Run(context.Background())
	pushN(b, "q", 50) // five batches of ten: one goes in flight, four wait
	select {
	case <-ins.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}
	waitLong(t, "the batches reach the queue", func() bool { return b.Stats().QueueLen >= 3 })

	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := b.Close(short); err == nil {
		t.Fatal("Close must report that the deadline ran out")
	}
	l := b.Losses()
	if l.Insert != 10 || l.Total() != 10 {
		t.Fatalf("losses %+v, want exactly the 10 events in flight as Insert", l)
	}
	if got := q.Stats().Events; got != 40 {
		t.Fatalf("%d events in the spool, want the 40 that were never sent", got)
	}
	seg, _ := q.Oldest(nil)
	if seg.Sent {
		t.Fatal("a batch that was never sent must not carry the sent flag")
	}
}

// Failures that are not about the data - a timeout here - never set a segment
// aside, however many times they repeat while the storage takes other inserts.
func TestOnlyDataErrorsPoisonASegment(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	putSegment(t, q, "t-slow", false, Event{CID: "slow", Stream: "poison"})
	ins := newStoreInserter()
	ins.poison.Store("poison")
	ins.poisonPlain.Store(true)
	b := newSpoolBuffer(t, ins, q, nil)
	stop, pushed := pushUntil(b)
	attempts := func() int {
		n := 0
		for _, tok := range ins.tokenLog() {
			if tok == "t-slow" {
				n++
			}
		}
		return n
	}
	waitLong(t, "many attempts", func() bool { return attempts() >= 3*poisonAfter })
	stop()
	// The buffer may spill segments of healthy events while the writer keeps
	// retrying the slow one, so the count of segments proves nothing: the slow
	// segment itself must still be queued and nothing may be in rejected.
	if st := q.Stats(); st.Rejected != 0 || !hasToken(q, "t-slow") {
		t.Fatalf("spool %+v: a timeout must not set a segment aside", st)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v", l)
	}
	ins.poison.Store("")
	waitLong(t, "the segment is written once the storage is well", func() bool {
		r, _ := ins.distinct()
		return int64(r) == pushed.Load()+1 && b.Stats().Pending == 0
	})
}

// The header of a segment says "may be stored" before the first attempt, so a
// crash in the middle of it leaves the truth on disk.
func TestReplayMarksTheSegmentSentBeforeTheAttempt(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	putSegment(t, q, "t-mark", false, Event{CID: "m", Stream: "s"})
	ins := newBlockingInserter()
	b := newSpoolBuffer(t, ins, q, nil)
	select {
	case <-ins.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the segment was never replayed")
	}
	seg, ok := q.Oldest(nil)
	if !ok || !seg.Sent {
		t.Fatalf("the index says %+v during the attempt, want the sent flag", seg)
	}
	meta, err := readHeaderFile(filepath.Join(q.dir, seg.Name))
	if err != nil || !meta.Sent || meta.Token != "t-mark" {
		t.Fatalf("on disk %+v err=%v, want the sent flag and the token kept", meta, err)
	}
	close(ins.release)
	waitLong(t, "segment removed", func() bool { return b.Stats().Spool.Segments == 0 })
}

// Where the table cannot deduplicate, a replay that fails after the rows may
// have been written holds the segment: no rejected/, no loss, no second write
// while the table cannot deduplicate, and one write once it can.
func TestFailedReplayWithoutDedupHoldsTheSegment(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	putSegment(t, q, "t-held", false, Event{CID: "held", Stream: "s"})
	ins := newStoreInserter()
	ins.dedup.Store(false)
	ins.storeThenFail.Store(1)
	var ready atomic.Bool
	b := newSpoolBuffer(t, ins, q, ready.Load)

	waitLong(t, "the one attempt", func() bool { return len(ins.tokenLog()) >= 1 })
	time.Sleep(200 * time.Millisecond)
	if n := len(ins.tokenLog()); n != 1 {
		t.Fatalf("%d attempts while the table cannot deduplicate, want 1", n)
	}
	if st := q.Stats(); st.Rejected != 0 || st.Segments != 1 {
		t.Fatalf("spool %+v, want the segment held and nothing rejected", st)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v, a held segment is not lost", l)
	}
	seg, _ := q.Oldest(nil)
	meta, err := readHeaderFile(filepath.Join(q.dir, seg.Name))
	if err != nil || !meta.Sent {
		t.Fatalf("on disk %+v err=%v, want the sent flag", meta, err)
	}

	ins.dedup.Store(true)
	ready.Store(true)
	waitLong(t, "segment removed", func() bool { return q.Stats().Segments == 0 })
	if rows, dups := ins.distinct(); rows != 1 || dups != 0 {
		t.Fatalf("rows=%d dups=%d, want one row written once", rows, dups)
	}
}

// A replay cancelled by Close leaves the segment where it is, whatever the
// table can do.
func TestCancelledReplayIsNotRejected(t *testing.T) {
	_, q := newPair(t, t.TempDir(), "engine")
	putSegment(t, q, "t-cancel", true, Event{CID: "c", Stream: "s"})
	ins := newCtxRespectingInserter()
	b := NewWith(ins, Options{
		Capacity: 10, BatchSize: 10, FlushEvery: time.Hour, Log: quietLogger(), Spool: q,
		DedupReady: func() bool { return true },
		PauseMin:   5 * time.Millisecond, PauseMax: 40 * time.Millisecond,
	})
	go b.Run(context.Background())
	select {
	case <-ins.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the segment was never replayed")
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = b.Close(short)
	if st := q.Stats(); st.Rejected != 0 || st.Segments != 1 {
		t.Fatalf("spool %+v, want the segment kept", st)
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses %+v", l)
	}
}
