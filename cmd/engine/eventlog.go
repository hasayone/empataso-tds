package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/egerkuzma/emtds/internal/logbuf"
	"github.com/egerkuzma/emtds/internal/store"
)

const (
	// eventInputCap, eventBatchSize and eventFlushEvery size the click event
	// buffer: events waiting for a batch, events per insert, and the longest an
	// event waits for its batch to fill.
	eventInputCap   = 10000
	eventBatchSize  = 1000
	eventFlushEvery = time.Second
	// dedupWatchEvery is how often the engine re-reads whether the events table
	// deduplicates, so an ALTER applied while it runs is noticed.
	dedupWatchEvery = time.Minute
)

// spoolIsMount tells whether a directory is a mount point. It is a variable so
// that tests, which work in plain temporary directories, can replace it.
var spoolIsMount = logbuf.IsMountPoint

// openEventSpool opens the disk queue of event batches from the EMTDS_SPOOL_*
// environment.
//
// It returns the spool, which the caller closes, and its events queue. When
// EMTDS_SPOOL_DIR is set, or when another live process holds the same instance,
// a spool that cannot be opened is an error: the deployment asked for it and
// starting without it would lose events silently. With the default directory
// unusable (a development machine without the volume) it logs a warning and
// returns nil, nil, nil, and the buffer runs without a disk queue. A value in
// the environment that is not a number is logged and replaced by its default.
// An explicit directory that is not a mount point (see
// logbuf.SpoolConfig.CheckMount) is opened all the same and logged as a warning:
// the queue would vanish when the container is recreated.
func openEventSpool(log *slog.Logger) (*logbuf.Spool, *logbuf.SpoolQueue, error) {
	cfg, explicit, err := logbuf.SpoolConfigFromEnv()
	if err != nil {
		log.Warn("spool settings ignored", "err", err)
	}
	cfg.Log = log
	cfg.IsMount = spoolIsMount
	sp, err := logbuf.OpenSpool(cfg)
	if err != nil {
		if explicit || errors.Is(err, logbuf.ErrSpoolLocked) {
			return nil, nil, fmt.Errorf("event spool %s: %w", cfg.Root, err)
		}
		log.Warn("event spool unavailable, events are held in memory only", "path", cfg.Root, "err", err)
		return nil, nil, nil
	}
	q, err := sp.Queue(logbuf.EventsKind)
	if err != nil {
		sp.Close()
		return nil, nil, fmt.Errorf("event spool %s: %w", cfg.Root, err)
	}
	return sp, q, nil
}

// lazyInserter writes event batches through the ClickHouse connection of d. It
// lets the buffer exist before ClickHouse does: until the connection is set it
// answers logbuf.ErrNotConnected, which tells the buffer the batch never left
// the process and may wait on disk.
type lazyInserter struct{ d *engineDeps }

// InsertEvents writes batch through the current connection. It returns
// logbuf.ErrNotConnected while there is none, and the error of the insert
// otherwise.
func (l lazyInserter) InsertEvents(ctx context.Context, batch []logbuf.Event) error {
	ch := l.d.clickhouse()
	if ch == nil {
		return logbuf.ErrNotConnected
	}
	return ch.InsertEvents(ctx, batch)
}

// chLink describes how the engine reaches ClickHouse in the background. addr is
// the address for the log. open makes one attempt to connect and may take long
// (store.OpenCHWaitingCtx waits a minute); it must return once ctx ends. pause
// separates two attempts. onConnect, if not nil, runs once after the connection
// is set.
type chLink struct {
	addr      string
	open      func(ctx context.Context) (*store.CH, error)
	pause     time.Duration
	onConnect func(*store.CH)
}

// startEventLog creates the click event buffer, starts it and starts the
// background connection to ClickHouse. spool is the disk queue, nil for none.
//
// The buffer exists from the first request, whether or not ClickHouse is up:
// until it connects, batches wait in the spool. noStepWave is raised until the
// connection shows that events has the step column. The returned buffer is also
// set as d.logs; the caller closes it. ctx ends the buffer's Run and the
// connection attempts.
func (d *engineDeps) startEventLog(ctx context.Context, link chLink, spool *logbuf.SpoolQueue) *logbuf.Buffer {
	d.noStepWave.Store(true)
	buf := logbuf.NewWith(lazyInserter{d}, logbuf.Options{
		Capacity: eventInputCap, BatchSize: eventBatchSize, FlushEvery: eventFlushEvery,
		Log: d.log, Spool: spool,
		DedupReady: func() bool { return d.clickhouse().DedupReady() },
		Connected:  func() bool { return d.clickhouse() != nil },
	})
	d.logs = buf
	go buf.Run(ctx)
	go d.connectClickHouse(ctx, link)
	return buf
}

// connectClickHouse tries link.open until it succeeds or ctx ends, then keeps
// the connection in d, recomputes noStepWave from it and wakes the event writer,
// so batches parked before the connection existed are written at once. A failed
// attempt is logged and followed by link.pause; an attempt cut short by ctx ends
// the loop without a warning.
func (d *engineDeps) connectClickHouse(ctx context.Context, link chLink) {
	for {
		ch, err := link.open(ctx)
		if err == nil {
			d.chConn.Store(ch)
			if d.logs != nil {
				d.logs.Wake()
			}
			step := !ch.StepReady()
			d.noStepWave.Store(step)
			if step {
				d.log.Warn("clickhouse: events.step is missing, the /offer step shares the first step's uniqueness; apply migrations/clickhouse/008_step.sql and restart")
			}
			d.log.Info("clickhouse connected", "addr", link.addr)
			if link.onConnect != nil {
				link.onConnect(ch)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("clickhouse not connected, will keep trying", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(link.pause):
		}
	}
}

// awaitRedis calls ping every pause until it succeeds or ctx ends, then calls
// onUp once. It is for a Redis client created without a successful ping at
// start: the client reconnects by itself, this only notices when it can.
func awaitRedis(ctx context.Context, ping func(context.Context) error, pause time.Duration, onUp func()) {
	for {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := ping(pctx)
		cancel()
		if err == nil {
			onUp()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
	}
}
