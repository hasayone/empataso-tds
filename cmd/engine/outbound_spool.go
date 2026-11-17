package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

const (
	// outboundLockTTL is how long the lock of one click id and status holds.
	outboundLockTTL = time.Hour
	// outboundLockTimeout bounds one lock call to Redis.
	outboundLockTimeout = time.Second
	// outboundLockGen is the number of locks one generation of the memory
	// fallback holds; it keeps at most two generations.
	outboundLockGen = 32768
	// outboundFeedPoll is how often an idle feeder looks at the queue.
	outboundFeedPoll = time.Second
	// outboundRetryMax is how many times one job is sent when the network does
	// not answer. The disk copy is removed after that and the job is counted
	// as dropped.
	outboundRetryMax = 8
	// outboundRetryPause is the wait before a job the network did not answer
	// is handed to the sender again.
	outboundRetryPause = 5 * time.Second
)

// outboundLocks is the memory fallback of the Redis lock that keeps two equal
// postbacks arriving together from queueing the same outbound job twice.
// It keeps keys in two generations: a key in either one is held, a hit in the
// older one moves it into the newer, and when the newer one is full it becomes
// the older and the previous older one is dropped. Insertion is O(1) and the
// size is bounded by two generations, as in ptrCache. The zero value is ready.
type outboundLocks struct {
	mu       sync.Mutex
	cur, old map[string]struct{}
}

// take reports whether key was free and marks it held.
func (l *outboundLocks) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.cur[key]; ok {
		return false
	}
	if _, ok := l.old[key]; ok {
		l.insertLocked(key)
		return false
	}
	l.insertLocked(key)
	return true
}

// insertLocked adds key to the newer generation, rotating the generations when
// it is full. The caller holds the mutex.
func (l *outboundLocks) insertLocked(key string) {
	if l.cur == nil || len(l.cur) >= outboundLockGen {
		l.old, l.cur = l.cur, make(map[string]struct{})
	}
	l.cur[key] = struct{}{}
}

// drop frees key.
func (l *outboundLocks) drop(key string) {
	l.mu.Lock()
	delete(l.cur, key)
	delete(l.old, key)
	l.mu.Unlock()
}

// outboundLockKey is the Redis key of the lock for a click id and status.
func outboundLockKey(cid, status string) string { return "outb:" + cid + ":" + status }

// takeOutbound takes the lock of a click id and status for an hour and reports
// whether this caller got it. It uses Redis for at most outboundLockTimeout, or
// until ctx ends if that is sooner, and the memory fallback when Redis is not
// configured or fails. The lock only separates calls that run together: that a
// pair was seen before is decided by the stored conversions.
func (d *engineDeps) takeOutbound(ctx context.Context, cid, status string) bool {
	key := outboundLockKey(cid, status)
	if d.counters != nil {
		lctx, cancel := context.WithTimeout(ctx, outboundLockTimeout)
		ok, err := d.counters.TryLock(lctx, key, outboundLockTTL)
		cancel()
		if err == nil {
			return ok
		}
	}
	return d.outLocks.take(key)
}

// releaseOutbound frees the lock of a click id and status after none of its
// outbound jobs could be queued. It does not make a resend possible: a later
// repeat of the postback is not new in the store (Fresh is false) and queues
// nothing, so the dropped jobs are lost and counted in outbound.dropped. What
// the release allows is a postback of the same arrival second that the store
// also finds first. ctx bounds the Redis call as in takeOutbound.
func (d *engineDeps) releaseOutbound(ctx context.Context, cid, status string) {
	key := outboundLockKey(cid, status)
	d.outLocks.drop(key)
	if d.counters != nil {
		lctx, cancel := context.WithTimeout(ctx, outboundLockTimeout)
		_ = d.counters.Unlock(lctx, key)
		cancel()
	}
}

// outboundSpool is the disk copy of the outbound jobs. A job is written here
// before it is handed to the sender and removed after the network answered, so
// a restart or a stop with jobs waiting loses none of them. fed holds the
// segments already handed to the sender.
type outboundSpool struct {
	q    *logbuf.SpoolQueue
	log  *slog.Logger
	wake chan struct{}

	mu  sync.Mutex
	fed map[string]struct{}
	// tries counts sends that the network did not answer, per segment.
	// until is the earliest time that segment may be handed over again.
	tries map[string]int
	until map[string]time.Time
}

// newOutboundSpool creates the outbound disk queue over q.
func newOutboundSpool(q *logbuf.SpoolQueue, log *slog.Logger) *outboundSpool {
	if log == nil {
		log = slog.Default()
	}
	return &outboundSpool{q: q, log: log, wake: make(chan struct{}, 1), fed: map[string]struct{}{}}
}

// put writes job to disk and wakes the feeder. It returns the spool error when
// the disk refuses the segment; nothing is queued then.
func (o *outboundSpool) put(job outboundJob) error {
	payload, err := json.Marshal(job)
	if err == nil {
		err = o.q.Put(logbuf.SegmentMeta{Token: queueToken("o", ""), Count: 1}, payload)
	}
	if err != nil {
		return err
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return nil
}

// next picks the oldest segment not yet handed to the sender and marks it.
func (o *outboundSpool) next() (logbuf.Segment, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	seg, ok := o.q.Oldest(func(s logbuf.Segment) bool {
		if _, fed := o.fed[s.Name]; fed {
			return true
		}
		return now.Before(o.until[s.Name])
	})
	if ok {
		o.fed[seg.Name] = struct{}{}
	}
	return seg, ok
}

// unfeed forgets that a segment was handed to the sender.
func (o *outboundSpool) unfeed(name string) {
	o.mu.Lock()
	delete(o.fed, name)
	o.mu.Unlock()
}

// finish removes the segment of a job the network answered, or that will not
// be tried again. A segment that cannot be removed is logged and stays on
// disk; the job is then sent again after the next start.
func (o *outboundSpool) finish(seg logbuf.Segment) {
	if err := o.q.Remove(seg); err != nil {
		o.log.Error("outbound spool: cannot remove a sent job", "segment", seg.Name, "err", err)
		return
	}
	o.mu.Lock()
	delete(o.tries, seg.Name)
	delete(o.until, seg.Name)
	o.mu.Unlock()
	o.unfeed(seg.Name)
}

// deferOutbound keeps a job the network did not answer and reports whether it
// will be sent again. A job with no disk copy, and a job that has already been
// sent outboundRetryMax times, is not kept: the caller removes it, and it is
// counted in outbound.dropped. Otherwise the segment waits outboundRetryPause
// and is handed to the sender again.
func (d *engineDeps) deferOutbound(job outboundJob) bool {
	o := d.outSpool
	if o == nil || job.seg.Name == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tries == nil {
		o.tries = map[string]int{}
		o.until = map[string]time.Time{}
	}
	o.tries[job.seg.Name]++
	if o.tries[job.seg.Name] >= outboundRetryMax {
		delete(o.tries, job.seg.Name)
		delete(o.until, job.seg.Name)
		d.stats.outbound.dropped.Add(1)
		return false
	}
	o.until[job.seg.Name] = time.Now().Add(outboundRetryPause)
	delete(o.fed, job.seg.Name)
	return true
}

// load reads the job of a segment and attaches the segment to it. A segment
// that cannot be decoded is moved to rejected.
func (o *outboundSpool) load(seg logbuf.Segment) (outboundJob, error) {
	payload, err := o.q.Load(seg)
	if err != nil {
		return outboundJob{}, err
	}
	var job outboundJob
	if err := json.Unmarshal(payload, &job); err != nil || job.URL == "" {
		o.q.Reject(seg, errors.New("outbound payload is not a job"))
		return outboundJob{}, logbuf.ErrSegmentRejected
	}
	job.seg = seg
	return job, nil
}

// snapshot returns the disk figures of the queue.
func (o *outboundSpool) snapshot() logbuf.SpoolStats { return o.q.Stats() }

// runOutboundFeeder hands the jobs waiting on disk to the sender, oldest first,
// until ctx ends. A job reaches the channel here whether it was written a
// moment ago or left by an earlier process. The channel is the only place the
// sender reads from, so serveOutbound stays the single sender. A job that is
// in the channel when the process stops stays on disk and is handed over again
// at the next start.
func (d *engineDeps) runOutboundFeeder(ctx context.Context) {
	o := d.outSpool
	if o == nil || d.outbound == nil {
		return
	}
	for ctx.Err() == nil {
		seg, ok := o.next()
		if ok {
			job, err := o.load(seg)
			if err == nil {
				select {
				case d.outbound <- job:
				case <-ctx.Done():
					return
				}
				continue
			}
			o.unfeed(seg.Name)
			if errors.Is(err, logbuf.ErrSegmentRejected) || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			o.log.Error("outbound spool: cannot read a job", "segment", seg.Name, "err", err)
		}
		t := time.NewTimer(outboundFeedPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-o.wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// finishOutbound removes the disk copy of a job after the network answered it,
// whatever the answer. A job that was never written to disk has nothing to remove.
func (d *engineDeps) finishOutbound(job outboundJob) {
	if d.outSpool != nil && job.seg.Name != "" {
		d.outSpool.finish(job.seg)
	}
}
