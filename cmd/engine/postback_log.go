package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/store"
)

const (
	// postlogInput is the number of log lines waiting for a batch.
	postlogInput = 4096
	// postlogBatch is the most lines written in one insert.
	postlogBatch = 500
	// postlogFlushEvery is the longest a line waits for its batch to fill.
	postlogFlushEvery = time.Second
	// postlogInsertTimeout bounds one insert of the log.
	postlogInsertTimeout = 5 * time.Second
	// postlogReplayPerTick is how many parked batches are written back per tick.
	postlogReplayPerTick = 20
	// postlogPoisonAfter is how many failed inserts a parked batch may take
	// before it is moved to rejected.
	postlogPoisonAfter = 5
)

// postbackLog writes the postback journal lines in synchronous batches.
//
// note puts a line into a bounded channel and returns at once, so an answer to
// the network never waits for the journal. One goroutine collects the lines
// into batches and inserts each batch, waiting for the server to accept it. A
// batch the store does not take, or that meets a missing connection, is parked
// in the disk queue and written back oldest first, each line with the time it
// had when it was made. A line is lost only when the channel is full or the
// disk queue refuses the batch, and then it is counted and logged.
type postbackLog struct {
	d    *engineDeps
	q    *logbuf.SpoolQueue
	log  *slog.Logger
	in   chan store.PostbackLogRow
	stop chan struct{}
	done chan struct{}

	stopOnce sync.Once
	lost     atomic.Int64
	// rejects caps the rejected inbound lines journaled per second (see journalIn).
	rejects rejectGate

	// mu orders note against Close: note holds it for reading from the check
	// of closed to the send, Close sets closed under the write lock.
	mu     sync.RWMutex
	closed bool

	// stopCtx is the context Close passed. The run goroutine reads it only after
	// stop is closed, which happens after the assignment, so the read needs no
	// lock. The last insert uses it as its parent, and a context that has
	// already ended parks the batch instead of waiting on the store.
	stopCtx context.Context

	// The fields below belong to the run goroutine. okSeq counts accepted
	// inserts, to tell a batch the store refuses from a store that is down.
	downUntil time.Time
	backoff   time.Duration
	okSeq     uint64
	fails     map[string]logFail
}

// logFail records the failed inserts of one parked batch. A failure counts
// toward postlogPoisonAfter only when the store accepted another insert since
// the previous one.
type logFail struct {
	count int
	okSeq uint64
}

// newPostbackLog creates the journal writer and starts its goroutine.
// q is the disk queue for batches that cannot be written now; nil keeps no
// disk copy, and such batches are counted as lost.
func newPostbackLog(d *engineDeps, q *logbuf.SpoolQueue, log *slog.Logger) *postbackLog {
	if log == nil {
		log = slog.Default()
	}
	l := &postbackLog{
		d: d, q: q, log: log,
		in:    make(chan store.PostbackLogRow, postlogInput),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		fails: map[string]logFail{},
	}
	go l.run()
	return l
}

// note queues one journal line without blocking. A full channel, or a line
// that arrives after Close, is counted as lost.
func (l *postbackLog) note(r store.PostbackLogRow) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		l.lost.Add(1)
		return
	}
	select {
	case l.in <- r:
	default:
		l.lost.Add(1)
	}
}

// Close stops the writer after it has written, or parked, every line queued
// before the call, within ctx. It returns ctx.Err() when the deadline came
// first. It is safe to call more than once.
func (l *postbackLog) Close(ctx context.Context) error {
	l.stopOnce.Do(func() {
		// The write lock waits for every note that is between the check of
		// closed and the send, so a line is either in the channel before the
		// writer starts to drain it or counted as lost, never neither.
		l.mu.Lock()
		l.closed = true
		l.stopCtx = ctx
		l.mu.Unlock()
		close(l.stop)
	})
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run collects lines into batches, flushes a batch when it is full or old, and
// writes parked batches back on every tick.
func (l *postbackLog) run() {
	defer close(l.done)
	tick := time.NewTicker(postlogFlushEvery)
	defer tick.Stop()
	batch := make([]store.PostbackLogRow, 0, postlogBatch)
	flush := func() {
		if len(batch) > 0 {
			l.flush(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case r := <-l.in:
			batch = append(batch, r)
			if len(batch) >= postlogBatch {
				flush()
			}
		case <-tick.C:
			flush()
			l.replay()
		case <-l.stop:
			for {
				select {
				case r := <-l.in:
					batch = append(batch, r)
					if len(batch) >= postlogBatch {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// flush inserts rows, or parks them when the store is missing, paused after a
// failure, or fails now.
func (l *postbackLog) flush(rows []store.PostbackLogRow) {
	if err := l.insert(rows); err != nil {
		l.park(rows)
	}
}

// insert writes rows in one batch. It returns an error without touching the
// store while the writer pauses after a failure, and store.ErrNoStoreConn
// without a connection.
func (l *postbackLog) insert(rows []store.PostbackLogRow) error {
	cs := l.d.convStore()
	if cs == nil {
		return store.ErrNoStoreConn
	}
	if time.Now().Before(l.downUntil) {
		return errLogPaused
	}
	parent := context.Background()
	if l.stopCtx != nil {
		parent = l.stopCtx
	}
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, postlogInsertTimeout)
	err := cs.InsertPostbackLog(ctx, rows)
	cancel()
	if err != nil {
		l.markDown(err)
		return err
	}
	l.backoff, l.downUntil = 0, time.Time{}
	l.okSeq++
	return nil
}

// errLogPaused is the error of an insert skipped during the pause after a failure.
var errLogPaused = errors.New("postback log: paused after a store failure")

// markDown starts or extends the pause after a failed insert: one second, then
// twice the previous one up to postbackBackoffMax.
func (l *postbackLog) markDown(err error) {
	if l.backoff < postbackBackoffMin {
		l.backoff = postbackBackoffMin
	} else {
		l.backoff = min(l.backoff*2, postbackBackoffMax)
	}
	l.downUntil = time.Now().Add(l.backoff)
	l.log.Warn("postback log: insert failed, lines go to the disk queue", "err", err, "pause", l.backoff.String())
}

// park writes rows to the disk queue as one segment. Without a queue, or when
// the queue refuses them, the lines are counted as lost.
func (l *postbackLog) park(rows []store.PostbackLogRow) {
	n := int64(len(rows))
	if l.q == nil {
		l.lost.Add(n)
		l.log.Error("postback log: no disk queue, lines lost", "n", n)
		return
	}
	payload, err := json.Marshal(rows)
	if err == nil {
		err = l.q.Put(logbuf.SegmentMeta{Token: queueToken("l", ""), Count: len(rows)}, payload)
	}
	if err != nil {
		l.lost.Add(n)
		l.log.Error("postback log: the disk queue did not take the lines, lines lost", "n", n, "err", err)
	}
}

// replay writes parked batches back, oldest first, until one fails or
// postlogReplayPerTick were written. A batch is removed only after the insert
// was accepted. A batch that keeps failing is moved to rejected.
func (l *postbackLog) replay() {
	if l.q == nil || l.d.convStore() == nil || time.Now().Before(l.downUntil) {
		return
	}
	for range postlogReplayPerTick {
		seg, ok := l.q.Oldest(nil)
		if !ok {
			return
		}
		payload, err := l.q.Load(seg)
		if err != nil {
			if errors.Is(err, logbuf.ErrSegmentRejected) || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			l.log.Error("postback log: cannot read a parked batch", "segment", seg.Name, "err", err)
			return
		}
		var rows []store.PostbackLogRow
		if err := json.Unmarshal(payload, &rows); err != nil {
			l.q.Reject(seg, err)
			continue
		}
		if err := l.insert(rows); err != nil {
			f := l.fails[seg.Name]
			if f.count == 0 || f.okSeq != l.okSeq {
				f.count++
			}
			f.okSeq = l.okSeq
			l.fails[seg.Name] = f
			if f.count >= postlogPoisonAfter {
				l.q.Reject(seg, err)
				delete(l.fails, seg.Name)
				l.lost.Add(int64(len(rows)))
			}
			return
		}
		delete(l.fails, seg.Name)
		if err := l.q.Remove(seg); err != nil {
			l.log.Error("postback log: cannot remove a written batch", "segment", seg.Name, "err", err)
			return
		}
	}
}
