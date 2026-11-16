package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/egerkuzma/emtds/internal/logbuf"
)

const (
	// shutdownTotal is the time the whole stop may take. It stays below the
	// stop_grace_period of 30 s, after which the supervisor kills the process.
	shutdownTotal = 27 * time.Second
	// queueStopWait is the longest the stop waits for the postback workers, the
	// outbound sender and the feeder: a job being sent takes up to the 5 s of the
	// HTTP client, a postback being written up to postbackStoreTimeout.
	queueStopWait = 6 * time.Second
)

// goBG runs f in a goroutine that stopBackground waits for. It adds to the
// counter before the goroutine starts, so a wait that begins right after the
// call cannot miss it.
func (d *engineDeps) goBG(f func()) {
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		f()
	}()
}

// startPostbackQueues opens the disk queues of postbacks, outbound jobs and
// journal lines in sp, sets them in d and starts their workers, which end with
// ctx. A postback without a click keeps waiting while click events are still on
// their way to the store (see eventFlowOf). A queue that cannot be opened is
// logged and left out: the postback path then answers from the store alone, as
// it does without a spool. The caller starts it before the server and before
// serveOutbound. A nil sp does nothing.
func (d *engineDeps) startPostbackQueues(ctx context.Context, sp *logbuf.Spool, log *slog.Logger) {
	if sp == nil {
		return
	}
	open := func(kind string) *logbuf.SpoolQueue {
		q, err := sp.Queue(kind)
		if err != nil {
			log.Error("postback spool queue not opened, it is off", "kind", kind, "err", err)
			return nil
		}
		return q
	}
	if q := open(outboundKind); q != nil {
		d.outSpool = newOutboundSpool(q, log)
	}
	postlog := open(postlogKind)
	d.plog = newPostbackLog(d, postlog, log)
	if q := open(postbackKind); q != nil {
		d.pbq = newPostbackQueue(d, q, func() eventFlow { return d.eventFlowOf(sp) }, log)
		d.pbq.start(ctx)
	}
	if d.outSpool != nil {
		d.goBG(func() { d.runOutboundFeeder(ctx) })
	}
}

// eventFlowOf describes the click events on their way to the store. They are
// busy when the storage is not taking them: there is no connection, an insert
// is failing, or a segment of events waits on disk in any instance directory
// of sp. Events sitting in a healthy buffer until the next flush are not busy;
// the two-minute window covers that race. A segment the buffer holds on
// purpose, because the store cannot deduplicate it, and a segment set aside in
// rejected are not waited for.
func (d *engineDeps) eventFlowOf(sp *logbuf.Spool) eventFlow {
	var f eventFlow
	if d.logs != nil {
		st := d.logs.Stats()
		f.LastInsertOK = st.LastInsertOK
		f.Busy = !st.Connected || !st.FailingSince.IsZero()
	}
	if !f.Busy && sp != nil {
		f.Busy = sp.AnyPending(logbuf.EventsKind, !d.clickhouse().DedupReady())
	}
	return f
}

// stopBackground ends the background work after the click events were flushed.
// It cancels the context of the workers, waits for them within queueStopWait
// and the time left until deadline, and only then closes the postback journal,
// so a line written by a postback that finished during the wait is flushed or
// parked, within what is left until deadline but at least a second. It logs
// what did not finish and does not fail. The caller closes the spool after it
// returns. cancel is the function that ends the context the workers run on.
func (d *engineDeps) stopBackground(cancel context.CancelFunc, deadline time.Time, log *slog.Logger) {
	cancel()
	done := make(chan struct{})
	go func() {
		d.bg.Wait()
		close(done)
	}()
	t := time.NewTimer(max(min(queueStopWait, time.Until(deadline)), 0))
	select {
	case <-done:
	case <-t.C:
		log.Warn("postback workers did not finish within the shutdown deadline; their segments stay on disk")
	}
	t.Stop()
	if d.plog == nil {
		return
	}
	ctx, stop := context.WithTimeout(context.Background(), min(shutdownPhase, max(time.Until(deadline), time.Second)))
	defer stop()
	if err := d.plog.Close(ctx); err != nil {
		log.Warn("postback journal: final flush did not finish within the shutdown deadline", "err", err)
	}
}
