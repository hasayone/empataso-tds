package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/store"
)

const (
	// postbackKind, outboundKind and postlogKind name the disk queues of the
	// engine under its spool instance directory.
	postbackKind = "postbacks"
	outboundKind = "outbound"
	postlogKind  = "postlog"

	// postbackStoreTimeout bounds the work of one postback with the store: the
	// click lookup, the check of earlier rows and the insert.
	postbackStoreTimeout = 3 * time.Second
	// postbackHandlerTimeout bounds the whole /postback handler: the store call
	// above plus the fast operations after it (the Redis lock and the journal
	// line). The writes to the disk queue are not cancellable and come on top.
	postbackHandlerTimeout = 4 * time.Second
	// postbackRetryWindow is how long after its arrival a postback whose click
	// is missing keeps waiting for the click: the event buffer holds a click
	// for a few seconds before it is written.
	postbackRetryWindow = 2 * time.Minute
	// postbackRetryMax is the ceiling of postbacks waiting for a click that has
	// not been found, over all senders.
	postbackRetryMax = 2000
	// postbackRetryPerSender is the ceiling of waiting postbacks of one sender
	// address, so one address sending made-up ids cannot take the whole queue.
	postbackRetryPerSender = 200
	// postbackRetryEvery is the pause before a postback without a click is
	// looked at again.
	postbackRetryEvery = 2 * time.Second
	// postbackBackoffMin and postbackBackoffMax bound the pause of the whole
	// replay after a store failure: it doubles from the first to the second.
	postbackBackoffMin = time.Second
	postbackBackoffMax = 30 * time.Second
	// postbackWorkers is the number of postbacks replayed at the same time.
	postbackWorkers = 4
	// postbackPoll is how often an idle worker looks at the queue.
	postbackPoll = 500 * time.Millisecond
	// pendingEventsEvery is how long the answer "the event spool is not empty"
	// is trusted before the directories are read again.
	pendingEventsEvery = time.Second
	// senderMaxLen bounds the sender address kept in a segment token.
	senderMaxLen = 64

	// postbackStoreMax is the ceiling of postbacks with a signed click id
	// waiting for the store, over all senders; the bytes ceiling usually comes
	// first.
	postbackStoreMax = 100000
	// postbackStorePerSender is the ceiling of postbacks with a signed click id
	// that one sender address may have waiting for the store. A signed id cannot
	// be made up, so the ceiling is high: it keeps one address from taking the
	// whole queue, not from sending real leads during an outage.
	postbackStorePerSender = 5000
	// postbackStoreMaxBytes is the ceiling of the disk bytes of the postback
	// queue, so postbacks waiting for the store cannot push clicks out of the
	// shared spool.
	postbackStoreMaxBytes = 64 << 20
	// postbackUnsignedMax and postbackUnsignedPerSender are the small quota of
	// postbacks whose click id this engine did not issue and that wait for the
	// store: made-up ids and ids imported from another tracker look the same.
	postbackUnsignedMax       = 100
	postbackUnsignedPerSender = 20

	// kindRetry, kindStore and kindUnsigned are the token prefixes of the three
	// kinds of waiting postbacks: a signed click not found yet, any postback
	// that met a store failure with a signed click id, and the same with an
	// unsigned one.
	kindRetry    = "n"
	kindStore    = "p"
	kindUnsigned = "u"
)

// errRetryFull is returned by accept when the queue of postbacks waiting for a
// click has no room for another one, in total or for that sender.
var errRetryFull = errors.New("postback retry queue is full")

// errStoreFull is returned by accept when the postbacks waiting for the store
// have used a quota: the bytes of the queue, the ceiling of the sender, or the
// small quota of unsigned click ids.
var errStoreFull = errors.New("postback store queue is full")

// quota counts the places taken in one kind of waiting postbacks, in total and
// per sender address. It is used under the mutex of the queue.
type quota struct {
	max, maxSender int
	total          int
	senders        map[string]int
}

// newQuota returns an empty quota with the given ceilings.
func newQuota(max, maxSender int) *quota {
	return &quota{max: max, maxSender: maxSender, senders: map[string]int{}}
}

// take reserves one place for sender and reports whether there was room.
func (q *quota) take(sender string) bool {
	if q.total >= q.max || q.senders[sender] >= q.maxSender {
		return false
	}
	q.total++
	q.senders[sender]++
	return true
}

// give returns a place taken for sender.
func (q *quota) give(sender string) {
	if q.total > 0 {
		q.total--
	}
	if n := q.senders[sender]; n > 1 {
		q.senders[sender] = n - 1
	} else {
		delete(q.senders, sender)
	}
}

// spooledPostback is one accepted postback as it waits on disk. At is the
// moment it reached the engine and becomes the time of the stored conversion.
// Subs are the sub ids of the query, kept so that the outbound templates expand
// the same way when the postback is written later; the rest of the query is not
// stored.
type spooledPostback struct {
	CID      string                  `json:"cid"`
	Status   string                  `json:"status"`
	Original string                  `json:"original,omitempty"`
	TID      string                  `json:"tid,omitempty"`
	Profit   float64                 `json:"profit"`
	DepSum   float64                 `json:"depsum,omitempty"`
	Dep2Sum  float64                 `json:"dep2sum,omitempty"`
	At       time.Time               `json:"at"`
	Subs     [config.MaxSubID]string `json:"subs"`
	// NoPayout marks a postback that carried no payout number. A record written
	// before this field has none marked and keeps its payout as stored.
	NoPayout bool `json:"no_payout,omitempty"`
}

// newSpooledPostback builds the queue record of in. q is the query of the
// request, from which the sub ids are taken; at is its arrival time.
func newSpooledPostback(in postbackIn, q url.Values, at time.Time) spooledPostback {
	_, subs := config.ApplySource(q, nil)
	return spooledPostback{
		CID: in.cid, Status: in.status, Original: in.original, TID: in.tid,
		Profit: in.profit, DepSum: in.depSum, Dep2Sum: in.dep2Sum, At: at, Subs: subs, NoPayout: in.payoutAbsent,
	}
}

// storeIn returns the store input of the record. offerPayout is the reserve
// lookup of the offer of the click; it is used only for a record that is marked
// NoPayout.
func (e spooledPostback) storeIn(offerPayout func(string) (float64, bool)) store.PostbackIn {
	return store.PostbackIn{
		CID: e.CID, Profit: e.Profit, DepSum: e.DepSum, Dep2Sum: e.Dep2Sum, Status: e.Status,
		Original: e.Original, TID: e.TID, At: e.At,
		PayoutAbsent: e.NoPayout, OfferPayout: offerPayout,
	}
}

// conversionStore is the part of the store the postback path uses.
// *store.CH implements it; tests supply a fake.
type conversionStore interface {
	RecordConversion(ctx context.Context, in store.PostbackIn) (store.ConversionResult, error)
	InsertPostbackLog(ctx context.Context, rows []store.PostbackLogRow) error
}

// convStore returns the store for postbacks, or nil while there is none. A nil
// d has none. The explicit nil keeps a missing connection from turning into a
// non-nil interface that holds a nil pointer.
func (d *engineDeps) convStore() conversionStore {
	if d == nil {
		return nil
	}
	if d.convOverride != nil {
		return d.convOverride
	}
	if ch := d.clickhouse(); ch != nil {
		return ch
	}
	return nil
}

// signedCID reports whether c is a click id this engine issued, by its
// signature: twelve base36 characters whose last three match. Ids imported from
// Keitaro and made-up ones fail. It only reads the signature.
func (d *engineDeps) signedCID(c string) bool {
	return d.cids.signedCID(c)
}

// queueToken returns a token for a segment. kind is one of kindRetry,
// kindStore and kindUnsigned for a postback, or another prefix for the other
// queues; sender is the address that sent the postback and may be empty.
func queueToken(kind, sender string) string {
	if len(sender) > senderMaxLen {
		sender = sender[:senderMaxLen]
	}
	return fmt.Sprintf("%s:%s:%016x", kind, sender, rand.Uint64())
}

// splitToken returns the kind and the sender address in the token of a segment
// of the postback queue. ok is false for a token that does not have that form.
func splitToken(token string) (kind, sender string, ok bool) {
	kind, rest, found := strings.Cut(token, ":")
	if !found {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		return "", "", false
	}
	return kind, rest[:i], true
}

// entryState is what the replay remembers about one segment: whether a worker
// has it, when it may be looked at again, and how many times the store failed.
type entryState struct {
	busy  bool
	until time.Time
	fails int
}

// postbackQueue is the disk queue of postbacks the engine accepted but has not
// written to the store: those that met a store failure and those whose click
// is not there yet. Workers replay them with their original arrival time and
// remove a segment only after the store confirmed the conversion or the wait
// for a click ended.
//
// Each kind of waiting postback has its own quota, in total and per sender, so
// a flood of made-up ids cannot push out the postbacks of real leads: a signed
// click not found yet (kindRetry), a postback with a signed id waiting for the
// store (kindStore) and one with an unsigned id waiting for the store
// (kindUnsigned, a small quota). The postbacks waiting for the store also share
// a ceiling in bytes. The counters are rebuilt from the tokens of the segments
// left on disk.
type postbackQueue struct {
	d    *engineDeps
	q    *logbuf.SpoolQueue
	flow func() eventFlow
	log  *slog.Logger
	wake chan struct{}

	// maxBytes is the byte ceiling of the whole queue for postbacks waiting for
	// the store; it starts at postbackStoreMaxBytes.
	maxBytes int64

	mu          sync.Mutex
	state       map[string]*entryState
	quotas      map[string]*quota
	downUntil   time.Time
	downBackoff time.Duration

	// flowMu guards the view of the event pipeline: when it was read, whether
	// it was busy, whether it was busy before, and the time from which a
	// postback without a click is counted again after a busy spell.
	flowMu   sync.Mutex
	flowAt   time.Time
	flowBusy bool
	wasBusy  bool
	gate     time.Time
}

// eventFlow is what the postback queue needs to know about the click events.
// Busy is true while the storage is not taking them (no connection, a failing
// insert, or a segment that will be written). A healthy buffer that merely
// waits for its flush is not busy. LastInsertOK is the time of the last
// successful insert of events.
type eventFlow struct {
	Busy         bool
	LastInsertOK time.Time
}

// newPostbackQueue creates the queue over q. flow reports the state of the click
// events; with it a postback without a click keeps waiting while events are
// still on their way to the store, and its window counts from the first
// insert after that; nil means events are never pending. The quotas are rebuilt
// from the segments already in q.
func newPostbackQueue(d *engineDeps, q *logbuf.SpoolQueue, flow func() eventFlow, log *slog.Logger) *postbackQueue {
	if log == nil {
		log = slog.Default()
	}
	p := &postbackQueue{
		d: d, q: q, flow: flow, log: log, maxBytes: postbackStoreMaxBytes,
		wake:  make(chan struct{}, postbackWorkers),
		state: map[string]*entryState{},
		quotas: map[string]*quota{
			kindRetry:    newQuota(postbackRetryMax, postbackRetryPerSender),
			kindStore:    newQuota(postbackStoreMax, postbackStorePerSender),
			kindUnsigned: newQuota(postbackUnsignedMax, postbackUnsignedPerSender),
		},
	}
	for _, s := range q.List() {
		if kind, sender, ok := splitToken(s.Token); ok && p.quotas[kind] != nil {
			p.quotas[kind].total++
			p.quotas[kind].senders[sender]++
		}
	}
	return p
}

// accept writes the postback to disk. kind is kindRetry for a postback whose
// click was not found, kindStore for one that met a store failure and has a
// signed click id, kindUnsigned for the same with an unsigned id. sender is the
// address that sent it. It returns errRetryFull or errStoreFull when a quota or
// the byte ceiling is reached and the spool error when the disk refused the
// segment; in all cases nothing was queued and the caller answers as it would
// without a queue.
func (p *postbackQueue) accept(e spooledPostback, kind, sender string) error {
	if len(sender) > senderMaxLen {
		sender = sender[:senderMaxLen]
	}
	if kind != kindRetry && p.q.Stats().Bytes >= p.maxBytes {
		return errStoreFull
	}
	if !p.reserve(kind, sender) {
		if kind == kindRetry {
			return errRetryFull
		}
		return errStoreFull
	}
	payload, err := json.Marshal(e)
	if err == nil {
		err = p.q.Put(logbuf.SegmentMeta{Token: queueToken(kind, sender), Count: 1}, payload)
	}
	if err != nil {
		p.unreserve(kind, sender)
		return err
	}
	for range postbackWorkers {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// reserve takes one place of the given kind for sender. It returns false when
// the quota of the kind, or of the sender in it, is used up.
func (p *postbackQueue) reserve(kind, sender string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.quotas[kind].take(sender)
}

// unreserve gives back a place taken by reserve.
func (p *postbackQueue) unreserve(kind, sender string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotas[kind].give(sender)
}

// release gives back the place of a segment that leaves the queue and forgets
// its state.
func (p *postbackQueue) release(seg logbuf.Segment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if kind, sender, ok := splitToken(seg.Token); ok && p.quotas[kind] != nil {
		p.quotas[kind].give(sender)
	}
	delete(p.state, seg.Name)
}

// start runs the workers until ctx ends. The workers are counted in d.bg, so
// the shutdown can wait for a postback that is being written.
func (p *postbackQueue) start(ctx context.Context) {
	for range postbackWorkers {
		p.d.goBG(func() { p.worker(ctx) })
	}
}

// worker replays segments one after another until ctx ends.
func (p *postbackQueue) worker(ctx context.Context) {
	for ctx.Err() == nil {
		seg, ok := p.next()
		if !ok {
			t := time.NewTimer(postbackPoll)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-p.wake:
			case <-t.C:
			}
			t.Stop()
			continue
		}
		p.handle(seg)
	}
}

// next picks the oldest segment that no worker holds and that is not waiting
// out a pause, and marks it as held. ok is false when there is none, when the
// store is not connected, or while the replay waits after a store failure.
func (p *postbackQueue) next() (logbuf.Segment, bool) {
	if p.d.convStore() == nil {
		return logbuf.Segment{}, false
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Before(p.downUntil) {
		return logbuf.Segment{}, false
	}
	seg, ok := p.q.Oldest(func(s logbuf.Segment) bool {
		st := p.state[s.Name]
		return st != nil && (st.busy || now.Before(st.until))
	})
	if !ok {
		return logbuf.Segment{}, false
	}
	st := p.state[seg.Name]
	if st == nil {
		st = &entryState{}
		p.state[seg.Name] = st
	}
	st.busy = true
	return seg, true
}

// later frees a segment and keeps it from being picked before until.
// failed counts a store failure against it.
func (p *postbackQueue) later(seg logbuf.Segment, until time.Time, failed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[seg.Name]
	if st == nil {
		st = &entryState{}
		p.state[seg.Name] = st
	}
	st.busy = false
	st.until = until
	if failed {
		st.fails++
	}
}

// storeDown pauses the whole replay after a store failure, for twice the
// previous pause up to postbackBackoffMax.
func (p *postbackQueue) storeDown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.downBackoff < postbackBackoffMin {
		p.downBackoff = postbackBackoffMin
	} else {
		p.downBackoff = min(p.downBackoff*2, postbackBackoffMax)
	}
	p.downUntil = time.Now().Add(p.downBackoff)
}

// storeUp ends the pause after a store failure.
func (p *postbackQueue) storeUp() {
	p.mu.Lock()
	p.downBackoff = 0
	p.downUntil = time.Time{}
	p.mu.Unlock()
}

// observeEvents returns whether click events are still held back by the store,
// and the gate: the time of the first successful insert after a busy spell that
// lasted more than one sample, or zero when this queue has not seen one end.
// The view is read at most once per pendingEventsEvery. A single busy sample
// between idle ones does not move the gate, so a healthy flush that the flow
// reports as busy for a moment does not start the wait over. Two busy samples
// in a row drop the gate: that is an outage, and the window counts from the
// insert that ends it. Without a flow function events are never pending.
func (p *postbackQueue) observeEvents() (busy bool, gate time.Time) {
	if p.flow == nil {
		return false, time.Time{}
	}
	p.flowMu.Lock()
	defer p.flowMu.Unlock()
	if now := time.Now(); now.Sub(p.flowAt) > pendingEventsEvery {
		f := p.flow()
		p.flowAt, p.flowBusy = now, f.Busy
		switch {
		case f.Busy:
			if p.wasBusy {
				p.gate = time.Time{}
			}
			p.wasBusy = true
		case p.wasBusy:
			p.wasBusy = false
			if p.gate.IsZero() {
				p.gate = f.LastInsertOK
				if p.gate.IsZero() {
					p.gate = now
				}
			}
		}
	}
	return p.flowBusy, p.gate
}

// clickMayArrive reports whether the click of e can still reach the store. It
// can while events are on their way, and for postbackRetryWindow after the later
// of the postback's arrival and the first insert of events that followed a
// busy spell: a click that was held back by an outage of the store is written
// only after the outage ends, so the window does not run during it.
func (p *postbackQueue) clickMayArrive(e spooledPostback) bool {
	busy, gate := p.observeEvents()
	if busy {
		return true
	}
	start := e.At
	if gate.After(start) {
		start = gate
	}
	return time.Since(start) < postbackRetryWindow
}

// handle replays one segment the worker holds.
//
// A stored conversion removes the segment, and the outbound templates are sent
// when the pair is new. The job of an outbound template is written to disk
// before the segment is removed, so a stop between the two repeats the
// postback and loses no job. A missing click keeps the segment while the click
// may still arrive: the id is signed and clickMayArrive says so. After that
// the postback is final: it is written to the log as click_not_found and the
// segment is removed. Any other failure keeps the segment and pauses the replay.
func (p *postbackQueue) handle(seg logbuf.Segment) {
	d := p.d
	payload, err := p.q.Load(seg)
	if err != nil {
		if errors.Is(err, logbuf.ErrSegmentRejected) || errors.Is(err, fs.ErrNotExist) {
			p.release(seg)
			return
		}
		p.log.Error("postback queue: cannot read a segment", "segment", seg.Name, "err", err)
		p.later(seg, time.Now().Add(postbackRetryEvery), false)
		return
	}
	var e spooledPostback
	if err := json.Unmarshal(payload, &e); err != nil || e.CID == "" {
		p.q.Reject(seg, fmt.Errorf("postback payload: %v", err))
		p.release(seg)
		return
	}
	cs := d.convStore()
	if cs == nil {
		p.later(seg, time.Now(), false)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), postbackStoreTimeout)
	res, err := cs.RecordConversion(ctx, e.storeIn(d.offerReserve))
	cancel()
	switch {
	case err == nil:
		p.storeUp()
		if res.ProfitFromOffer {
			e.Profit = res.Profit
		}
		d.noteOfferCap(context.Background(), res, e.Status)
		d.queueOutbound(res, e.CID, e.Profit, e.Status, e.Subs)
		d.stats.postbacks.note("replayed")
		d.notePostback(e.CID, e.Status, "replayed", "in", e.Profit)
		p.finish(seg)
	case errors.Is(err, sql.ErrNoRows):
		p.storeUp()
		if d.signedCID(e.CID) && p.clickMayArrive(e) {
			p.later(seg, time.Now().Add(postbackRetryEvery), false)
			return
		}
		// An unsigned id (a Keitaro import, or garbage) is final as soon as the
		// click is missing. It is not a written-off lead, so it does not raise
		// the retry alert.
		if d.signedCID(e.CID) {
			d.stats.postbacks.note("retry_expired")
		} else {
			d.stats.postbacks.note("click_not_found")
		}
		d.notePostback(e.CID, e.Status, "click_not_found", "in", e.Profit)
		p.finish(seg)
	default:
		d.log.Warn("postback replay failed", "cid", e.CID, "err", err)
		p.storeDown()
		p.later(seg, time.Now(), true)
	}
}

// finish removes a segment whose postback is settled. A segment that cannot be
// removed stays queued and is written again later. The stored row is the same
// (same key, same time), but the outbound jobs of the first write are held back
// only by the lock of the pair: one hour in Redis, until the restart in the
// memory fallback. After that a second set of jobs is possible, which is the
// price of never losing one.
func (p *postbackQueue) finish(seg logbuf.Segment) {
	if err := p.q.Remove(seg); err != nil {
		p.log.Error("postback queue: cannot remove a replayed segment", "segment", seg.Name, "err", err)
		p.later(seg, time.Now().Add(postbackRetryEvery), false)
		return
	}
	p.release(seg)
}

// snapshot returns the disk figures of the queue.
func (p *postbackQueue) snapshot() logbuf.SpoolStats { return p.q.Stats() }
