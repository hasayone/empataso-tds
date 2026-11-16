// Package logbuf buffers events and writes them to storage in batches.
//
// Log writes are asynchronous: Push puts an event into a channel and returns
// immediately, Run accumulates a batch, and a separate writer goroutine inserts
// it. The visitor response never waits for the log write; on overflow the event
// is dropped (with a counter).
//
// Accumulating and inserting are deliberately different goroutines. With the
// insert inlined into the accumulator, a storage that answers slowly — a
// ClickHouse busy merging parts will hold an insert for tens of seconds and
// then succeed — stops the reader for exactly that long, the channel fills, and
// every event in the window is dropped. Handing the batch over keeps the reader
// running at all times.
//
// A batch that cannot be written is not thrown away. Every batch gets an
// idempotency token when it is created, and the token travels with every attempt
// (see WithDedupToken), so repeating an insert whose answer was lost does not
// store the rows twice. The writer repeats a batch a few times with growing
// pauses, then parks it on disk (see Spool) and moves on; while the storage is
// down fresh batches are parked straight away and one batch at a time is tried
// as a probe. Parked batches are written back, oldest first, alternating with
// fresh ones, and removed only after the storage confirmed them. Close parks
// whatever it could not write, so a restart picks it up.
package logbuf

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Inserter inserts a batch of events into storage (implemented by store.CH).
type Inserter interface {
	InsertEvents(ctx context.Context, batch []Event) error
}

// insertTimeout bounds a single insert. It stays generous on purpose: a batch
// that misses five seconds under merge pressure will often land at twenty, and
// a short deadline turns recoverable slowness into a repeat of a batch that was
// about to succeed.
const insertTimeout = 30 * time.Second

// insertGrace bounds how long Close waits for a cancelled insert to unwind. A
// driver that honours its context needs microseconds; the timer exists only so
// that one which does not cannot hold the process open indefinitely.
const insertGrace = 2 * time.Second

// closeTry is how long Close lets a healthy storage take the queued batches
// before it cancels the inserts and parks the rest on disk. A batch that needs
// longer than this is repeated under its token after the restart.
const closeTry = 1500 * time.Millisecond

const (
	// defaultRetryAttempts is how many times the writer tries one fresh batch
	// before it parks the batch on disk and takes the next one.
	defaultRetryAttempts = 3
	// defaultPauseMin and defaultPauseMax bound the pause between attempts and
	// between probes of a storage that is down: it starts at the minimum and
	// doubles on every failure up to the maximum (1, 2, 4 ... 30 seconds).
	defaultPauseMin = time.Second
	defaultPauseMax = 30 * time.Second
	// spillDepth is the size of the channel that hands overflow batches to the
	// goroutine that writes them to disk. A full channel counts the batch as a
	// queue loss; the accumulator never waits for it.
	spillDepth = 16
	// poisonAfter is how many times a spool segment may fail while the storage
	// accepts other inserts before it is moved aside as unwritable.
	poisonAfter = 5
	// heldRecheck is how often an idle writer looks at segments it holds back
	// while the table cannot deduplicate.
	heldRecheck = time.Second
)

// Options configures a Buffer made by NewWith. A zero field takes its default.
//
// Capacity is the size of the input channel (10000), BatchSize the flush
// threshold (1000), FlushEvery the longest an event waits for a flush (1s).
// Spool, when set, is where batches wait while the storage is down, the queue is
// full or the process stops; without it those batches are lost, counted by cause.
// DedupReady reports whether the storage deduplicates inserts that carry a token;
// a batch that may already be stored is repeated only while it returns true, and
// nil means always true. Connected reports whether the storage connection exists
// and only feeds Stats; nil means true. RetryAttempts is the number of tries of
// a fresh batch (3); PauseMin and PauseMax bound the pauses (1s, 30s).
type Options struct {
	Capacity      int
	BatchSize     int
	FlushEvery    time.Duration
	Log           *slog.Logger
	Spool         *SpoolQueue
	DedupReady    func() bool
	Connected     func() bool
	RetryAttempts int
	PauseMin      time.Duration
	PauseMax      time.Duration
}

// batch is a group of events on its way to the storage. token is the
// idempotency token, issued when the batch is created and carried by every
// attempt and by the spool segment. sent is true once an attempt may have
// reached the storage.
type batch struct {
	ev    []Event
	token string
	sent  bool
}

// Buffer is an asynchronous event buffer.
type Buffer struct {
	in         chan Event
	batches    chan batch
	spillCh    chan batch
	ins        Inserter
	batchSize  int
	flushEvery time.Duration
	log        *slog.Logger

	spool      *SpoolQueue
	spoolWake  chan struct{}
	dedupReady func() bool
	connected  func() bool
	attempts   int
	pauseMin   time.Duration
	pauseMax   time.Duration
	ws         writerState

	// closed is owned by Close and by nobody else. Run only reads it. Two
	// closers on one channel is a panic, and it would land in the middle of a
	// shutdown, where a test that exercises one path at a time cannot see it.
	closed    chan struct{}
	closeOnce sync.Once
	runDone   chan struct{} // Run returned
	drained   chan struct{} // Close finished with b.in: the workers may wind down
	wrDone    chan struct{} // the writer returned
	spillDone chan struct{} // the spill goroutine returned
	allDone   chan struct{} // writer and spill goroutine returned: nothing is in flight

	// insertCtx is the parent of every insert, so Close can reach into a batch
	// that is already inside InsertEvents. Without it the last insert carries an
	// uncancellable 30s of its own: Close would either wait past its own
	// deadline or walk away from a live goroutine, and in the second case
	// nothing would ever count that batch.
	insertCtx    context.Context
	cancelInsert context.CancelFunc

	// Losses are counted per cause, because they are cured differently:
	// a full buffer means the accumulator could not keep up, a full queue means
	// the writer could not, a failed insert means storage is unhealthy and the
	// batch could not be repeated, a full spool means the disk refused it, and a
	// late push means events arrived during shutdown.
	lostFull      atomic.Int64
	lostQueue     atomic.Int64
	lostInsert    atomic.Int64
	lostLate      atomic.Int64
	lostSpoolFull atomic.Int64

	// Figures behind Stats. Each is touched by one goroutine at a time or by
	// a single atomic operation, so the snapshot never takes a lock that Push,
	// Run or the writer wait on.
	accum        atomic.Int64 // events held by the accumulator, not yet handed over
	queuedEv     atomic.Int64 // events inside batches waiting in b.batches
	spillEv      atomic.Int64 // events inside batches waiting for the spill goroutine
	inflight     atomic.Int64 // events of the batch the writer is inserting
	inserted     atomic.Int64 // events storage has accepted
	insertErrors atomic.Int64 // failed InsertEvents calls
	lastInsertOK atomic.Int64 // UnixNano of the last successful insert, 0 if none
	failingSince atomic.Int64 // UnixNano of the first failure of the current streak, 0 if none
	downNow      atomic.Bool  // the writer is in the down state; a copy of writerState.down for Close
	probeNow     atomic.Bool  // Wake asked the writer to probe at once
	abandoned    atomic.Int64 // events Close booked as queue loss when it gave up and the writer has not written or parked since
}

// New creates a buffer without a spool and without repeats: a batch the storage
// rejects is counted as lost at once. capacity — channel size, batchSize — flush
// threshold by size, flushEvery — maximum flush delay.
func New(ins Inserter, capacity, batchSize int, flushEvery time.Duration, log *slog.Logger) *Buffer {
	return NewWith(ins, Options{Capacity: capacity, BatchSize: batchSize, FlushEvery: flushEvery, Log: log, RetryAttempts: 1})
}

// NewWith creates a buffer from opts and starts its writer and, with a spool,
// the goroutine that parks overflow batches. See Options for the defaults.
func NewWith(ins Inserter, opts Options) *Buffer {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Capacity <= 0 {
		opts.Capacity = 10000
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 1000
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = time.Second
	}
	if opts.RetryAttempts <= 0 {
		opts.RetryAttempts = defaultRetryAttempts
	}
	if opts.PauseMin <= 0 {
		opts.PauseMin = defaultPauseMin
	}
	if opts.PauseMax < opts.PauseMin {
		opts.PauseMax = max(defaultPauseMax, opts.PauseMin)
	}
	// The queue holds roughly as many events as the in-channel, so a stalled
	// writer buys the same grace twice over instead of once.
	depth := opts.Capacity / opts.BatchSize
	if depth < 2 {
		depth = 2
	}
	ictx, icancel := context.WithCancel(context.Background())
	b := &Buffer{
		insertCtx:    ictx,
		cancelInsert: icancel,

		in:         make(chan Event, opts.Capacity),
		batches:    make(chan batch, depth),
		spillCh:    make(chan batch, spillDepth),
		ins:        ins,
		batchSize:  opts.BatchSize,
		flushEvery: opts.FlushEvery,
		log:        opts.Log,
		spool:      opts.Spool,
		spoolWake:  make(chan struct{}, 1),
		dedupReady: opts.DedupReady,
		connected:  opts.Connected,
		attempts:   opts.RetryAttempts,
		pauseMin:   opts.PauseMin,
		pauseMax:   opts.PauseMax,
		closed:     make(chan struct{}),
		runDone:    make(chan struct{}),
		drained:    make(chan struct{}),
		wrDone:     make(chan struct{}),
		spillDone:  make(chan struct{}),
		allDone:    make(chan struct{}),
	}
	b.ws = writerState{backoff: opts.PauseMin, cool: map[string]time.Time{}, fails: map[string]segFailure{}}
	// The writer belongs to the buffer, not to Run: Close has to be able to
	// finish the queue whatever happened to the accumulator.
	go b.write()
	if b.spool != nil {
		go b.spill()
	} else {
		close(b.spillDone)
	}
	go func() {
		<-b.wrDone
		<-b.spillDone
		close(b.allDone)
	}()
	return b
}

// Push puts an event into the buffer without blocking. On overflow — drops it
// and counts it.
//
// The shutdown check is best-effort: Close can shut the door in the nanoseconds
// between the check and the send, in which case the event lands in the channel.
// Close drains what is there, so such an event is usually still written.
func (b *Buffer) Push(e Event) {
	select {
	case <-b.closed:
		b.lostLate.Add(1)
		return
	default:
	}
	select {
	case b.in <- e:
	default:
		b.lostFull.Add(1)
	}
}

// Dropped returns the total number of events that never reached storage.
func (b *Buffer) Dropped() int64 { return b.Losses().Total() }

// Losses returns the loss counters broken down by cause. The causes are read
// separately, so a snapshot taken under load may be a few events out of step.
func (b *Buffer) Losses() Losses {
	return Losses{
		Full:      b.lostFull.Load(),
		Queue:     b.lostQueue.Load(),
		Insert:    b.lostInsert.Load(),
		Late:      b.lostLate.Load(),
		SpoolFull: b.lostSpoolFull.Load(),
	}
}

// Stats returns the buffer figures for metrics without blocking Push, Run or
// the writer. The counters are read one by one, so a snapshot taken under load
// may be a few events out of step; Pending is clamped at zero. It does not fail.
func (b *Buffer) Stats() Stats {
	s := Stats{
		InputLen:     len(b.in),
		InputCap:     cap(b.in),
		QueueLen:     len(b.batches),
		QueueCap:     cap(b.batches),
		Inserted:     b.inserted.Load(),
		InsertErrors: b.insertErrors.Load(),
		Losses:       b.Losses(),
		DedupReady:   b.dedupOK(),
		Connected:    b.connected == nil || b.connected(),
	}
	s.Pending = b.memoryPending()
	if b.spool != nil {
		s.SpoolEnabled = true
		s.Spool = b.spool.Stats()
		s.Pending += s.Spool.Events
	}
	if s.Pending < 0 {
		s.Pending = 0
	}
	if ns := b.lastInsertOK.Load(); ns != 0 {
		s.LastInsertOK = time.Unix(0, ns)
	}
	if ns := b.failingSince.Load(); ns != 0 {
		s.FailingSince = time.Unix(0, ns)
	}
	return s
}

// Wake tells the writer that the storage may be reachable now: the pause before
// the next probe of a down storage is dropped and the writer looks at the spool
// at once. The engine calls it right after the connection is set, so batches
// parked while there was none are not left waiting for the end of a backoff.
// It does not block and is safe to call from any goroutine.
func (b *Buffer) Wake() {
	b.probeNow.Store(true)
	select {
	case b.spoolWake <- struct{}{}:
	default:
	}
}

// memoryPending estimates the events that exist only in memory: the input
// channel, the accumulator, queued batches, batches waiting to be parked and the
// batch being inserted. The figures are read one by one, so the sum may be a few
// events out of step; it is never negative.
func (b *Buffer) memoryPending() int64 {
	n := int64(len(b.in)) + b.accum.Load() + b.queuedEv.Load() + b.spillEv.Load() + b.inflight.Load()
	return max(n, 0)
}

// dedupOK reports whether a batch that may already be stored may be repeated.
func (b *Buffer) dedupOK() bool { return b.dedupReady == nil || b.dedupReady() }

// canReachStorage reports whether an insert made now can get to the storage:
// there is a connection and Close has not cancelled the inserts. Marking a
// segment as sent before an attempt that cannot send anything would hold the
// segment back for no reason.
func (b *Buffer) canReachStorage() bool {
	return b.insertCtx.Err() == nil && (b.connected == nil || b.connected())
}

// Run accumulates events into batches and hands them to the writer. Blocking —
// run in a separate goroutine.
//
// ctx is a stop signal, not a deadline: cancelling it makes Run put down what it
// is holding and leave. It does not drain, does not flush and does not close
// anything — all of that belongs to Close, which owns one budget and one door.
// Two goroutines draining the same channel would tear a batch in half.
func (b *Buffer) Run(ctx context.Context) {
	defer close(b.runDone)
	t := time.NewTicker(b.flushEvery)
	defer t.Stop()
	pending := make([]Event, 0, b.batchSize)

	for {
		select {
		case <-ctx.Done():
			b.handoff(&pending, true)
			return
		case <-b.closed:
			b.handoff(&pending, true)
			return
		case e := <-b.in:
			pending = append(pending, e)
			b.accum.Store(int64(len(pending)))
			if len(pending) >= b.batchSize {
				b.handoff(&pending, false)
			}
		case <-t.C:
			b.handoff(&pending, false)
		}
	}
}

// Close stops accepting, drains what is left and waits for the writer, all
// within ctx. Whatever the storage does not take in time is written to the
// spool instead of being dropped: the input, the accumulator, the queue and the
// batch the writer is working on. Returns ctx.Err() if the deadline ran out
// first — in which case the caller is being told the truth: work is still in
// flight, and whatever the process does next will decide its fate.
//
// With a spool, a storage that is down, not connected or slow to answer is not
// waited for: an insert still running closeTry after the start (at once when the
// writer is already down) is cancelled, and everything it held goes to disk, so
// the process leaves within a couple of seconds instead of the whole budget.
// A healthy storage gets closeTry to take what is queued first. Without a spool
// there is nowhere to park, so the whole budget is spent on the inserts.
// Whatever is still in memory when the writer ignores the cancellation is added
// to the queue losses, so the final counters tell the truth about a process that
// exits now; a writer that gets to those events later takes the count back.
//
// Idempotent and safe to call from several goroutines.
func (b *Buffer) Close(ctx context.Context) error {
	b.closeOnce.Do(func() {
		close(b.closed)
		if b.spool != nil {
			delay := closeTry
			if b.downNow.Load() || (b.connected != nil && !b.connected()) {
				delay = 0
			}
			t := time.AfterFunc(delay, b.cancelInsert)
			go func() {
				<-b.allDone
				t.Stop()
			}()
		}
		// Wait for Run, deadline or not. Run answers b.closed from a loop that
		// never blocks, so this is short — and skipping it would mean draining
		// b.in alongside a live accumulator, which tears a batch in half. An
		// expired ctx is a reason to stop waiting for storage, never a reason
		// to skip the bookkeeping: everything below still has to be counted.
		select {
		case <-b.runDone:
		case <-time.After(insertGrace):
			// Run was never started, or is wedged. Press on: the queue still
			// has to be wound up, or the writer waits forever and nothing in
			// b.in is ever counted.
			b.log.Error("logbuf: the accumulator did not stop; draining alongside it")
		}
		pending := make([]Event, 0, b.batchSize)
		for drained := false; !drained; {
			select {
			case e := <-b.in:
				pending = append(pending, e)
				b.accum.Store(int64(len(pending)))
				if len(pending) >= b.batchSize {
					b.queue(ctx, &pending)
				}
			default:
				drained = true
			}
		}
		b.queue(ctx, &pending)
		close(b.drained)
	})
	select {
	case <-b.allDone:
		return nil
	case <-ctx.Done():
	}
	// The deadline is up with a batch still inside InsertEvents. Cancelling it
	// is the difference between a truthful farewell line and a silent one: the
	// writer comes back, parks or counts the batch and exits, and only then do
	// we admit we ran out of time.
	b.cancelInsert()
	select {
	case <-b.allDone:
	case <-time.After(insertGrace):
		n := b.memoryPending()
		b.abandoned.Store(n)
		b.lostQueue.Add(n)
		b.log.Error("logbuf: the writer ignored cancellation; the events still in memory are counted as lost", "n", n)
	}
	return ctx.Err()
}

// handoff passes the accumulated batch to the writer without blocking. A full
// queue means the writer is stuck or the storage is down; the batch then goes to
// the spill goroutine, which writes it to disk. Only when that channel is full
// too, or there is no spool, is the batch dropped, counted whole.
//
// final is true for the last batch Run holds before it leaves. There is no next
// flush to catch what falls through then, so a batch that fits neither the queue
// nor the spill channel is written to the spool right here, in Run, which is
// about to stop anyway; only a spool that refuses it counts as a loss.
func (b *Buffer) handoff(pending *[]Event, final bool) {
	cp := b.take(pending)
	if cp.ev == nil {
		return
	}
	n := int64(len(cp.ev))
	b.queuedEv.Add(n)
	b.accum.Store(0)
	select {
	case b.batches <- cp:
		return
	default:
	}
	b.queuedEv.Add(-n)
	if b.spool == nil {
		b.lostQueue.Add(n)
		b.log.Error("logbuf: batch queue full, dropping batch", "n", n)
		return
	}
	b.spillEv.Add(n)
	select {
	case b.spillCh <- cp:
	default:
		if final {
			b.park(cp, &b.lostQueue)
			b.spillEv.Add(-n)
			return
		}
		b.spillEv.Add(-n)
		b.lostQueue.Add(n)
		b.log.Error("logbuf: batch queue and spill channel full, dropping batch", "n", n)
	}
}

// queue is handoff for the shutdown path: it waits for room instead of
// spilling, because there is no next flush to catch what falls here. When ctx
// ends first the batch is written to the spool directly, in this goroutine.
func (b *Buffer) queue(ctx context.Context, pending *[]Event) {
	cp := b.take(pending)
	if cp.ev == nil {
		return
	}
	n := int64(len(cp.ev))
	b.queuedEv.Add(n)
	b.accum.Store(0)
	// Try without waiting first: a ctx that is already expired must still get
	// the batch as far as the queue, otherwise a shutdown that started late
	// throws away events the writer had room for.
	select {
	case b.batches <- cp:
		return
	default:
	}
	select {
	case b.batches <- cp:
	case <-ctx.Done():
		b.queuedEv.Add(-n)
		b.log.Error("logbuf: shutdown deadline before the batch was queued", "n", n)
		b.park(cp, &b.lostQueue)
	}
}

// take detaches the accumulated events into a new batch with its own token, so
// the caller can keep reusing pending. A batch without events has a nil ev.
// The copy is load-bearing: the writer reads the slice after this returns.
func (b *Buffer) take(pending *[]Event) batch {
	if len(*pending) == 0 {
		return batch{}
	}
	cp := make([]Event, len(*pending))
	copy(cp, *pending)
	*pending = (*pending)[:0]
	return batch{ev: cp, token: newToken()}
}

// park writes a batch to the spool, the one place events wait out an outage.
// bt is the batch; fallback is the loss counter used when there is no spool.
// A spool that refuses the batch (full, below the free-space floor, or a write
// error) counts the events as SpoolFull. A batch that was written wakes the
// writer to replay it. It does not return an error.
func (b *Buffer) park(bt batch, fallback *atomic.Int64) {
	n := int64(len(bt.ev))
	if b.spool == nil {
		fallback.Add(n)
		return
	}
	payload, err := encodeEvents(bt.ev)
	if err == nil {
		err = b.spool.Put(SegmentMeta{Token: bt.token, Count: len(bt.ev), Sent: bt.sent}, payload)
	}
	if err != nil {
		b.lostSpoolFull.Add(n)
		b.log.Error("logbuf: the spool did not take the batch, events lost", "n", n, "err", err)
		return
	}
	b.reclaim(n)
	select {
	case b.spoolWake <- struct{}{}:
	default:
	}
}

// spill parks the batches Run could not queue. It runs until Close has drained
// the input and then empties its channel, so nothing handed to it is left behind.
func (b *Buffer) spill() {
	defer close(b.spillDone)
	one := func(bt batch) {
		b.park(bt, &b.lostQueue)
		b.spillEv.Add(-int64(len(bt.ev)))
	}
	for {
		select {
		case bt := <-b.spillCh:
			one(bt)
		case <-b.drained:
			for {
				select {
				case bt := <-b.spillCh:
					one(bt)
				default:
					return
				}
			}
		}
	}
}

// isClosed reports whether ch is closed, without blocking.
func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
