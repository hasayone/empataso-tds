package logbuf

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

// work is what the writer does next: insert a fresh batch, or replay a segment
// when replay is set.
type work struct {
	b      batch
	seg    Segment
	replay bool
}

// segFailure records the failures of one spool segment, see poisonAfter.
type segFailure struct {
	count int
	okSeq uint64
}

// writerState is owned by the writer goroutine and touched by no other.
type writerState struct {
	// down: the storage failed; fresh batches go to disk until a probe succeeds.
	down bool
	// nextProbe is when the next probe may run while down.
	nextProbe time.Time
	// backoff is the next pause; it doubles on failure and resets on success.
	backoff time.Duration
	// lastFresh: the previous unit was a fresh batch, so a replay is next.
	lastFresh bool
	// cool holds the segments that failed, with the time they may be tried again.
	cool map[string]time.Time
	// fails counts the failures of each segment, see segFailure.
	fails map[string]segFailure
	// okSeq counts successful inserts, to tell a poisoned segment from an outage.
	okSeq uint64
}

// write is the writer goroutine: it inserts fresh batches, replays the spool
// when the storage is healthy, and returns once Close has drained the queue.
//
// b.batches is never closed. Run and Close both send on it, and ordering two
// senders against a close is exactly the kind of shutdown-only race that a test
// exercising one path at a time will not catch — the price of getting it wrong
// is a panic in production. A separate done-signal costs one channel and cannot
// be got wrong.
func (b *Buffer) write() {
	defer close(b.wrDone)
	for {
		w, ok := b.next()
		if !ok {
			return
		}
		if w.replay {
			b.replaySegment(w.seg)
		} else {
			b.deliver(w.b)
		}
	}
}

// next picks the writer's next unit of work, blocking until there is one, and
// returns false when the writer is done.
//
// The mode decides the choice: healthy (pickHealthy), down (pickDown) or
// closing, where only the batches already queued are taken and nothing is
// replayed. When there is nothing to do yet, next waits in waitForWork.
func (b *Buffer) next() (work, bool) {
	w := &b.ws
	for {
		if b.probeNow.CompareAndSwap(true, false) {
			w.nextProbe = time.Time{}
			w.backoff = b.pauseMin
		}
		if isClosed(b.drained) {
			select {
			case bt := <-b.batches:
				return work{b: bt}, true
			default:
				return work{}, false
			}
		}
		closing := isClosed(b.closed)
		now := time.Now()
		var (
			picked work
			found  bool
			idle   time.Duration // how long to wait for something else to happen; 0 means no timer
		)
		switch {
		case closing:
			picked, found = b.pickQueued()
		case w.down && now.Before(w.nextProbe):
			idle = w.nextProbe.Sub(now)
		case w.down:
			picked, found, idle = b.pickDown(now)
		default:
			picked, found, idle = b.pickHealthy(now)
		}
		if found {
			return picked, true
		}
		if picked, found = b.waitForWork(idle, closing); found {
			return picked, true
		}
	}
}

// pickQueued takes the next batch that is already queued, without waiting.
// found is false when the queue is empty.
func (b *Buffer) pickQueued() (w work, found bool) {
	select {
	case bt := <-b.batches:
		return work{b: bt}, true
	default:
		return work{}, false
	}
}

// pickDown chooses the unit for a probe of a storage that is down and whose
// backoff is over: a fresh batch if there is one, else a segment. The first
// success ends the down state and the segments that may be stored go next.
// found is false when there is nothing to try; idle is then how long until the
// first cooling segment may be tried, 0 when none is cooling.
func (b *Buffer) pickDown(now time.Time) (w work, found bool, idle time.Duration) {
	if w, found = b.pickQueued(); found {
		return w, true, 0
	}
	if seg, ok := b.pickSegment(now); ok {
		return work{seg: seg, replay: true}, true, 0
	}
	return work{}, false, b.coolWait(now)
}

// pickHealthy chooses the unit for a healthy storage. A fresh batch and a spool
// segment alternate, so a backlog does not starve new clicks and new clicks do
// not starve the backlog; segments that may already be stored go before both,
// oldest first, so a repeat under the same token stays inside the deduplication
// window. found is false when there is nothing to do; idle is then how long
// until the first cooling segment may be tried, 0 when none is cooling.
func (b *Buffer) pickHealthy(now time.Time) (w work, found bool, idle time.Duration) {
	if seg, ok := b.pickSent(now); ok {
		return work{seg: seg, replay: true}, true, 0
	}
	if b.ws.lastFresh {
		if seg, ok := b.pickSegment(now); ok {
			return work{seg: seg, replay: true}, true, 0
		}
	}
	if w, found = b.pickQueued(); found {
		return w, true, 0
	}
	if seg, ok := b.pickSegment(now); ok {
		return work{seg: seg, replay: true}, true, 0
	}
	return work{}, false, b.coolWait(now)
}

// waitForWork blocks until a batch arrives, the spool is woken, Close starts or
// finishes draining, or idle passes. idle 0 means no timer, except that
// segments held back by the deduplication flag are looked at again every
// heldRecheck, so that the flag turning on releases them. closing is true once
// Close has shut the door; the stop signal is then no longer a reason to wake.
// found is true with the batch to deliver. A batch that arrives while the writer
// is down and its probe is not due is parked on the spot and found is false, as
// it is for every other wake-up: the caller looks again.
func (b *Buffer) waitForWork(idle time.Duration, closing bool) (w work, found bool) {
	if idle == 0 && !closing && b.spool != nil && b.spool.Stats().Segments > 0 {
		idle = heldRecheck
	}
	var timer <-chan time.Time
	var t *time.Timer
	if idle > 0 {
		t = time.NewTimer(idle)
		timer = t.C
	}
	stopCh := b.closed
	if closing {
		stopCh = nil
	}
	select {
	case bt := <-b.batches:
		if t != nil {
			t.Stop()
		}
		if b.ws.down && !closing && time.Now().Before(b.ws.nextProbe) {
			n := int64(len(bt.ev))
			b.queuedEv.Add(-n)
			b.inflight.Add(n)
			b.park(bt, &b.lostInsert)
			b.inflight.Add(-n)
			return work{}, false
		}
		return work{b: bt}, true
	case <-b.spoolWake:
	case <-b.drained:
	case <-stopCh:
	case <-timer:
	}
	if t != nil {
		t.Stop()
	}
	return work{}, false
}

// pickSegment returns the oldest segment that may be replayed now, those that
// may already be stored first (see pickSent), then the rest. ok is false when
// there is none or no spool.
func (b *Buffer) pickSegment(now time.Time) (Segment, bool) {
	if seg, ok := b.pickSent(now); ok {
		return seg, true
	}
	return b.pickReplay(now, false)
}

// pickSent returns the oldest segment that may already be stored and may be
// replayed now. Repeating a batch under its token is dropped by the table only
// while the token is inside its window of recent blocks, so these segments go
// ahead of the unsent ones and of fresh batches, which would push the window
// forward. ok is false when there is none or no spool.
func (b *Buffer) pickSent(now time.Time) (Segment, bool) { return b.pickReplay(now, true) }

// pickReplay returns the oldest segment that may be replayed now: not cooling
// after a failure, and not held back because it may already be stored while the
// table cannot deduplicate. sentOnly limits the choice to segments marked as
// possibly stored. ok is false when there is none or no spool.
func (b *Buffer) pickReplay(now time.Time, sentOnly bool) (Segment, bool) {
	if b.spool == nil {
		return Segment{}, false
	}
	w := &b.ws
	dedup := b.dedupOK()
	return b.spool.Oldest(func(s Segment) bool {
		if sentOnly && !s.Sent {
			return true
		}
		if until, ok := w.cool[s.Name]; ok {
			if now.Before(until) {
				return true
			}
			delete(w.cool, s.Name)
		}
		return s.Sent && !dedup
	})
}

// coolWait returns how long until the first cooling segment may be tried, or 0
// when none is cooling.
func (b *Buffer) coolWait(now time.Time) time.Duration {
	var wait time.Duration
	for _, until := range b.ws.cool {
		if d := until.Sub(now); d > 0 && (wait == 0 || d < wait) {
			wait = d
		}
	}
	return wait
}

// insert makes one attempt to write events under token, bounded by
// insertTimeout and by Close. It returns the error of InsertEvents, or
// errAbandoned without calling it when Close has already cancelled the context:
// nothing was sent then, which the caller must not count as a possible write.
func (b *Buffer) insert(token string, events []Event) error {
	if b.insertCtx.Err() != nil {
		return errAbandoned
	}
	ctx, cancel := context.WithTimeout(WithDedupToken(b.insertCtx, token), insertTimeout)
	defer cancel()
	return b.ins.InsertEvents(ctx, events)
}

// reclaim takes back n events from the queue loss Close booked when it gave up
// on a writer that ignored the cancellation: the writer got to them after all,
// by writing or parking them, so they are not lost. It never takes back more than
// Close booked.
func (b *Buffer) reclaim(n int64) {
	for {
		left := b.abandoned.Load()
		if left <= 0 {
			return
		}
		take := min(left, n)
		if b.abandoned.CompareAndSwap(left, left-take) {
			b.lostQueue.Add(-take)
			return
		}
	}
}

// markOK records a successful insert of n events and ends a failure streak.
func (b *Buffer) markOK(n int64) {
	b.reclaim(n)
	b.inserted.Add(n)
	b.lastInsertOK.Store(time.Now().UnixNano())
	b.failingSince.Store(0)
	w := &b.ws
	w.down = false
	b.downNow.Store(false)
	w.backoff = b.pauseMin
	w.okSeq++
}

// markFail records a failed insert call of n events. A storage that is not
// connected yet is not a failed insert: the call never left the process, the
// metrics already say "not connected", so it is logged at debug level and
// neither counted in InsertErrors nor starts a failure streak.
func (b *Buffer) markFail(err error, n int) {
	if errors.Is(err, ErrNotConnected) {
		b.log.Debug("logbuf: storage not connected, batch waits", "n", n)
		return
	}
	b.insertErrors.Add(1)
	b.failingSince.CompareAndSwap(0, time.Now().UnixNano())
	b.log.Error("logbuf: insert failed", "n", n, "err", err)
}

// enterDown switches the writer to the down state and schedules the next probe
// after the current backoff, which then doubles up to PauseMax. Without a spool
// there is nowhere to park batches, so the state is not entered.
func (b *Buffer) enterDown() {
	if b.spool == nil {
		return
	}
	w := &b.ws
	w.down = true
	b.downNow.Store(true)
	w.nextProbe = time.Now().Add(w.backoff)
	w.backoff = min(w.backoff*2, b.pauseMax)
}

// pause waits out the current backoff and doubles it. It returns false when
// Close started during the wait: the caller then gives up on the batch.
func (b *Buffer) pause() bool {
	w := &b.ws
	d := w.backoff
	w.backoff = min(d*2, b.pauseMax)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-b.closed:
		return false
	}
}

// deliver inserts one fresh batch and decides what a failure means.
//
// Every attempt carries the batch's token. A failure that certainly did not
// reach the storage (no connection, dial error) parks the batch at once. Any
// other failure may have written the rows; the batch is repeated after a pause
// only while the table deduplicates, otherwise it is counted as lost, because a
// duplicate cannot be told apart from the original later; the writer then goes
// down, so that the batches behind it wait on disk and are not each put through
// the same failure. A batch that fails
// RetryAttempts times, is a probe of a down storage, or meets Close is parked
// and the writer moves on, so an outage of any length holds at most this one
// batch in memory beyond the queue.
func (b *Buffer) deliver(bt batch) {
	w := &b.ws
	n := int64(len(bt.ev))
	b.queuedEv.Add(-n)
	b.inflight.Add(n)
	defer b.inflight.Add(-n)
	w.lastFresh = true

	if w.down && isClosed(b.closed) {
		b.park(bt, &b.lostInsert)
		return
	}
	probe := w.down
	for attempt := 1; ; attempt++ {
		if b.insertCtx.Err() != nil {
			// Close cancelled the inserts. This attempt would send nothing, so
			// the batch keeps the sent flag it had and goes to disk.
			b.park(bt, &b.lostInsert)
			b.enterDown()
			return
		}
		err := b.insert(bt.token, bt.ev)
		if err == nil {
			b.markOK(n)
			return
		}
		b.markFail(err, len(bt.ev))
		if !neverSent(err) {
			bt.sent = true
		}
		if bt.sent && !b.dedupOK() {
			b.lostInsert.Add(n)
			b.log.Error("logbuf: batch may be stored and the table does not deduplicate; not repeated", "n", n)
			b.enterDown()
			return
		}
		giveUp := neverSent(err) || probe || isClosed(b.closed) || attempt >= b.attempts
		if !giveUp && !b.pause() {
			giveUp = true
		}
		if giveUp {
			b.park(bt, &b.lostInsert)
			b.enterDown()
			return
		}
	}
}

// replaySegment inserts one spool segment under its own token and removes the
// file once the storage has confirmed it.
//
// A failure leaves the segment on disk and keeps it from being tried again
// until a cooldown passes; the writer goes to the down state to probe slowly.
// Before the first attempt the header is rewritten with the sent flag, so a
// crash during the attempt leaves a segment that says it may be stored; where
// the table cannot deduplicate such a segment is held, never repeated blindly
// and never set aside for that reason.
//
// Only failures that come from the data itself (see BadData) count toward
// poisonAfter, and only while the storage accepts other inserts; the segment is
// then moved to rejected and counted as an insert loss, since it will not be
// written again by itself. Every other failure — a timeout, the network, an
// overloaded server, a cancelled insert — only cools the segment down.
//
// A segment that cannot be read (a damaged body, a record count that differs
// from its header) is moved to rejected by Load or here, and the events its
// header promised are counted as an insert loss. A segment whose header cannot
// be read has no known count; it shows only in SpoolStats.Rejected.
func (b *Buffer) replaySegment(seg Segment) {
	w := &b.ws
	w.lastFresh = false
	payload, err := b.spool.Load(seg)
	if err != nil {
		switch {
		case errors.Is(err, ErrSegmentRejected):
			b.lostInsert.Add(int64(seg.Count))
		case !errors.Is(err, fs.ErrNotExist):
			b.log.Error("logbuf: cannot read a spool segment", "segment", seg.Name, "err", err)
			b.cooldown(seg.Name)
		}
		return
	}
	events, err := decodeEvents(payload, seg.Count)
	if err != nil {
		b.spool.quarantine(seg.Name, err)
		b.lostInsert.Add(int64(seg.Count))
		return
	}
	n := int64(len(events))
	if !seg.Sent && b.canReachStorage() {
		// The attempt may write the rows. If the process dies in the middle, the
		// next start must know the segment may be stored already.
		if merr := b.spool.MarkSent(seg); merr != nil {
			if errors.Is(merr, fs.ErrNotExist) {
				return
			}
			b.log.Error("logbuf: cannot mark a segment as sent before replaying it", "segment", seg.Name, "err", merr)
			if !b.dedupOK() {
				b.cooldown(seg.Name)
				return
			}
		}
	}
	err = b.insert(seg.Token, events)
	if err == nil {
		if rerr := b.spool.Remove(seg); rerr != nil {
			b.spool.NoteSent(seg.Name)
			b.log.Error("logbuf: a replayed segment could not be removed; its token keeps a replay from duplicating it", "segment", seg.Name, "err", rerr)
			b.cooldown(seg.Name)
		}
		delete(w.fails, seg.Name)
		b.markOK(n)
		return
	}
	b.markFail(err, len(events))
	if neverSent(err) {
		b.cooldown(seg.Name)
		b.enterDown()
		return
	}
	if b.insertCtx.Err() != nil {
		// Close cancelled the insert. The segment stays queued for the next
		// start, whatever the table can do; it is neither failed nor rejected.
		return
	}
	if !IsBadData(err) {
		// A timeout, a lost connection, an overloaded server or a table that
		// cannot deduplicate: the segment is fine and is tried again after a
		// cooldown. With no deduplication it is held (the header says it may be
		// stored) until the table can deduplicate; it is never set aside for this.
		b.cooldown(seg.Name)
		b.enterDown()
		return
	}
	f := w.fails[seg.Name]
	if f.count == 0 || f.okSeq != w.okSeq {
		f.count++
	}
	f.okSeq = w.okSeq
	if f.count >= poisonAfter {
		b.spool.quarantine(seg.Name, fmt.Errorf("the insert failed %d times while the storage took other batches: %w", f.count, err))
		b.lostInsert.Add(n)
		delete(w.fails, seg.Name)
		return
	}
	w.fails[seg.Name] = f
	b.cooldown(seg.Name)
	b.enterDown()
}

// cooldown keeps a segment out of the replay for twice the current backoff, so
// a segment that fails does not hold the queue behind it.
func (b *Buffer) cooldown(name string) {
	b.ws.cool[name] = time.Now().Add(2 * max(b.ws.backoff, b.pauseMin))
}
