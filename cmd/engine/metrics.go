package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egerkuzma/emtds/internal/atomicfile"
	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/logbuf"
)

const (
	// metricsEvery is the period of the metrics file refresh.
	metricsEvery = 10 * time.Second
	// metricsDirMode is the mode of a metrics directory the engine has to create.
	metricsDirMode = 0o750
	// metricsFileMode is the mode of a metrics file; the counters are not secret.
	metricsFileMode = 0o644
)

// engineStats holds the counters the engine keeps for its metrics snapshot.
// The zero value is ready.
type engineStats struct {
	postbacks  postbackStats
	outbound   outboundStats
	originLink originLinkStats
}

// originLinkStats counts path tokens written onto our own 3xx locations.
type originLinkStats struct {
	added, unknownHost atomic.Int64
}

// postbackStats counts the outcomes of inbound /postback requests since the
// process started. The zero value is ready; the fields are atomics.
type postbackStats struct {
	ok, invalid, noSubID, noStore, notFound, storeError atomic.Int64
	queued, retry, replayed, retryExpired, retryFull    atomic.Int64
	queueFull, badKey                                   atomic.Int64
}

// note adds one request with the given outcome. outcome is the class
// postbackResult returns, or "invalid" for a payout that is not a finite
// number, or one of the queue classes: "queued" and "retry" for a postback
// accepted into the disk queue, "replayed" for a queued one later stored,
// "retry_expired" for one whose click never appeared, "retry_overflow" for a
// missing click refused because a retry ceiling was reached and "queue_full"
// for a store failure refused because a queue quota was reached; the last two
// replace the class postbackResult gave the request. An unknown class is not
// counted.
func (p *postbackStats) note(outcome string) {
	switch outcome {
	case "ok":
		p.ok.Add(1)
	case "invalid":
		p.invalid.Add(1)
	case "no_subid":
		p.noSubID.Add(1)
	case "no_store":
		p.noStore.Add(1)
	case "click_not_found":
		p.notFound.Add(1)
	case "store_error":
		p.storeError.Add(1)
	case "queued":
		p.queued.Add(1)
	case "retry":
		p.retry.Add(1)
	case "replayed":
		p.replayed.Add(1)
	case "retry_expired":
		p.retryExpired.Add(1)
	case "retry_overflow":
		p.retryFull.Add(1)
	case "queue_full":
		p.queueFull.Add(1)
	case "bad_key":
		p.badKey.Add(1)
	}
}

// snapshot returns the counters as the metrics file shows them.
func (p *postbackStats) snapshot() config.PostbackMetrics {
	return config.PostbackMetrics{
		OK: p.ok.Load(), InvalidPayout: p.invalid.Load(), NoSubID: p.noSubID.Load(),
		NoStore: p.noStore.Load(), ClickNotFound: p.notFound.Load(), StoreError: p.storeError.Load(),
		Queued: p.queued.Load(), Retry: p.retry.Load(), Replayed: p.replayed.Load(),
		RetryExpired: p.retryExpired.Load(), RetryOverflow: p.retryFull.Load(), QueueFull: p.queueFull.Load(),
		BadKey: p.badKey.Load(),
	}
}

// outboundStats counts outbound postback jobs since the process started.
// The zero value is ready; the fields are atomics.
type outboundStats struct {
	queued, sent, failed, dropped atomic.Int64
}

// snapshot returns the counters as the metrics file shows them.
func (o *outboundStats) snapshot() config.OutboundMetrics {
	return config.OutboundMetrics{Queued: o.queued.Load(), Sent: o.sent.Load(), Failed: o.failed.Load(), Dropped: o.dropped.Load()}
}

// landingStats counts the requests the engine sends to the PHP sidecar. The
// zero value is ready; the fields are atomics, so the proxy path takes no lock.
type landingStats struct {
	requests, timeouts, connErrors, status5xx atomic.Int64
	le100ms, le1s, le5s, gt5s                 atomic.Int64
	sumMs                                     atomic.Int64
}

// observe records one finished request to PHP. took is its total time. status
// is the HTTP status of the answer and is ignored when err is set. err is the
// transport error, or nil: a timeout counts as a timeout, a request cancelled
// by the visitor counts as no failure, any other error as a connection error. An
// answer with status 500 or above counts as a 5xx. It does not fail.
func (l *landingStats) observe(took time.Duration, status int, err error) {
	l.requests.Add(1)
	l.sumMs.Add(took.Milliseconds())
	switch {
	case took <= 100*time.Millisecond:
		l.le100ms.Add(1)
	case took <= time.Second:
		l.le1s.Add(1)
	case took <= 5*time.Second:
		l.le5s.Add(1)
	default:
		l.gt5s.Add(1)
	}
	switch {
	case err == nil:
		if status >= http.StatusInternalServerError {
			l.status5xx.Add(1)
		}
	case errors.Is(err, context.Canceled):
	case isTimeout(err):
		l.timeouts.Add(1)
	default:
		l.connErrors.Add(1)
	}
}

// isTimeout reports whether err is a deadline or a network timeout.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// snapshot returns the counters as the metrics file shows them.
func (l *landingStats) snapshot() config.LandingMetrics {
	return config.LandingMetrics{
		Requests: l.requests.Load(), Timeouts: l.timeouts.Load(), ConnErrors: l.connErrors.Load(),
		Status5xx:    l.status5xx.Load(),
		LatencyLe100: l.le100ms.Load(), LatencyLe1s: l.le1s.Load(), LatencyLe5s: l.le5s.Load(), LatencyGt5s: l.gt5s.Load(),
		LatencySumMs: l.sumMs.Load(),
	}
}

// eventMetrics converts a buffer snapshot to its file form. Zero times become nil.
func eventMetrics(s logbuf.Stats) config.EventMetrics {
	return config.EventMetrics{
		Connected: s.Connected, DedupReady: s.DedupReady,
		InputLen: s.InputLen, InputCap: s.InputCap, QueueLen: s.QueueLen, QueueCap: s.QueueCap,
		Pending: s.Pending, Inserted: s.Inserted, InsertErrors: s.InsertErrors,
		LastInsertOK: timePtr(s.LastInsertOK), FailingSince: timePtr(s.FailingSince),
		Spool: eventSpoolMetrics(s),
		Lost:  config.LostEvents{Full: s.Losses.Full, Queue: s.Losses.Queue, Insert: s.Losses.Insert, Late: s.Losses.Late, SpoolFull: s.Losses.SpoolFull},
	}
}

// eventSpoolMetrics is the file form of the event buffer's disk queue. Enabled
// is false when the buffer runs without one, which is not the same as an empty
// queue.
func eventSpoolMetrics(s logbuf.Stats) config.SpoolMetrics {
	m := spoolMetrics(s.Spool)
	m.Enabled = s.SpoolEnabled
	return m
}

// spoolMetrics converts the figures of one disk queue that exists to their file
// form; Enabled is true.
func spoolMetrics(s logbuf.SpoolStats) config.SpoolMetrics {
	return config.SpoolMetrics{
		Enabled:  true,
		Segments: s.Segments, Bytes: s.Bytes, Events: s.Events,
		OldestAgeSeconds: int64(s.OldestAge.Seconds()), Rejected: s.Rejected,
	}
}

// queueMetrics returns the disk queues of the postback path. A queue that does
// not exist reports zeros.
func (d *engineDeps) queueMetrics() config.QueueMetrics {
	var m config.QueueMetrics
	if d.pbq != nil {
		m.Postbacks = spoolMetrics(d.pbq.snapshot())
	}
	if d.outSpool != nil {
		m.Outbound = spoolMetrics(d.outSpool.snapshot())
	}
	if d.plog != nil {
		m.PostbackLogLost = d.plog.lost.Load()
		if d.plog.q != nil {
			m.PostbackLog = spoolMetrics(d.plog.q.Stats())
		}
	}
	return m
}

// timePtr returns nil for the zero time, otherwise a pointer to t in UTC.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// metricsSnapshot builds the metrics of this engine. instance is the name
// published in the file, started is the process start and now the snapshot
// time. Events.Configured follows EMTDS_CLICKHOUSE_ADDR; without a ClickHouse
// connection (no event buffer) the rest of the events part is zero with
// connected false. A nil d yields counters at zero.
func (d *engineDeps) metricsSnapshot(instance string, started, now time.Time) config.EngineMetrics {
	m := config.EngineMetrics{Instance: instance, StartedAt: started.UTC(), UpdatedAt: now.UTC()}
	m.Events.Configured = clickHouseConfigured()
	if d == nil {
		return m
	}
	if d.logs != nil {
		m.Events = eventMetrics(d.logs.Stats())
		m.Events.Configured = clickHouseConfigured()
	}
	m.Postbacks = d.stats.postbacks.snapshot()
	m.Outbound = d.stats.outbound.snapshot()
	m.OriginLinkAdded = d.stats.originLink.added.Load()
	m.OriginLinkUnknown = d.stats.originLink.unknownHost.Load()
	if ch := d.clickhouse(); ch != nil {
		m.RootMissing = ch.RootMissing()
	}
	if d.landServe != nil {
		m.Landing = d.landServe.stats.snapshot()
	}
	m.Spool = d.queueMetrics()
	m.RedisTrim = currentRedisTrimStats()
	return m
}

// startMetrics prepares the metrics of this engine: the instance name comes from
// EMTDS_INSTANCE (default "engine") and the directory from EMTDS_METRICS_DIR
// (default /var/lib/emtds/metrics). started is the process start time.
// collect returns a fresh snapshot, for the HTTP endpoint. w refreshes the file
// every ten seconds and is nil when the instance name is not valid ([a-z0-9-],
// 32 characters at most, no leading "-"): that is logged as an error and the
// file is not written, so a typo can neither stop the hot path nor overwrite the
// file of another instance. The endpoint still answers. Before the first write
// it reads the file this instance left behind and carries its last counters as
// Previous (see readPreviousRun). It does not fail.
func (d *engineDeps) startMetrics(log *slog.Logger, started time.Time) (collect func() config.EngineMetrics, w *metricsWriter) {
	instance := config.Instance()
	var prev *config.PreviousRun
	valid := config.ValidInstance(instance)
	dir := config.MetricsDir()
	if valid {
		prev = readPreviousRun(config.MetricsPath(dir, instance), started)
	}
	collect = func() config.EngineMetrics {
		m := d.metricsSnapshot(instance, started, time.Now())
		m.Previous = prev
		return m
	}
	if !valid {
		log.Error("metrics file off: EMTDS_INSTANCE must be 1-32 characters from [a-z0-9-] and not start with '-'", "instance", instance)
		return collect, nil
	}
	return collect, startMetricsWriter(dir, instance, collect, log)
}

// clickHouseConfigured reports whether a ClickHouse address is set in
// EMTDS_CLICKHOUSE_ADDR.
func clickHouseConfigured() bool { return config.Env("EMTDS_CLICKHOUSE_ADDR") != "" }

// metricsReadMax caps the size of the old metrics file readPreviousRun reads.
const metricsReadMax = 1 << 20

// readPreviousRun reads the metrics file of the process this one replaces.
// path is the file of this instance and started the start of the current
// process. It returns its StartedAt and loss counters, or nil when the file is
// missing, unreadable, larger than 1 MiB, not a metrics JSON, has no StartedAt
// or does not come from an earlier process. An older Previous inside the file is
// dropped: only the process right before this one is carried. It does not fail.
func readPreviousRun(path string, started time.Time) *config.PreviousRun {
	if fi, err := os.Stat(path); err != nil || fi.IsDir() || fi.Size() > metricsReadMax {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var old config.EngineMetrics
	if json.Unmarshal(b, &old) != nil || old.StartedAt.IsZero() || !old.StartedAt.Before(started) {
		return nil
	}
	return &config.PreviousRun{StartedAt: old.StartedAt, Lost: old.Events.Lost, Drops: old.Drops()}
}

// serveMetrics answers GET /internal/metrics with the snapshot collect returns.
// Any other method gets 405. The body is the same JSON the metrics file holds.
// The path is closed to the public by nginx (location ^~ /internal/).
func serveMetrics(collect func() config.EngineMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(collect())
	}
}

// metricsWriter refreshes the metrics file of one engine instance.
type metricsWriter struct {
	path    string
	collect func() config.EngineMetrics
	log     *slog.Logger

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	warned   bool // a write failure was logged (touched by the writer goroutine and Close, never together)
}

// startMetricsWriter creates dir (mode 0750) when it is missing and starts a
// goroutine that writes collect() to <dir>/<instance>.json now and then every
// metricsEvery. instance must pass config.ValidInstance. A directory that cannot
// be created is logged and the writer still runs, so a later fix of the volume
// takes effect without a restart. Close stops it and writes the last snapshot.
func startMetricsWriter(dir, instance string, collect func() config.EngineMetrics, log *slog.Logger) *metricsWriter {
	w := &metricsWriter{
		path: config.MetricsPath(dir, instance), collect: collect, log: log,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go w.run()
	return w
}

// run writes the snapshot immediately and then on every tick until Close.
func (w *metricsWriter) run() {
	defer close(w.done)
	w.write()
	t := time.NewTicker(metricsEvery)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.write()
		}
	}
}

// write stores one snapshot through atomicfile. A failure is logged once until
// a write succeeds again. It does not return an error.
func (w *metricsWriter) write() {
	b, err := json.Marshal(w.collect())
	if err == nil {
		err = writeMetricsFile(w.path, b)
	}
	switch {
	case err != nil && !w.warned:
		w.warned = true
		w.log.Warn("metrics file not written", "path", w.path, "err", err)
	case err == nil:
		w.warned = false
	}
}

// writeMetricsFile writes data to path, creating its directory with mode 0750.
// It returns the error of the directory creation or of the atomic write.
func writeMetricsFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), metricsDirMode); err != nil {
		return err
	}
	return atomicfile.Write(path, data, metricsFileMode)
}

// Close stops the writer, waits for it and writes the final snapshot. It is safe
// to call more than once and on a nil writer; later calls do nothing.
func (w *metricsWriter) Close() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stop)
		<-w.done
		w.write()
	})
}
