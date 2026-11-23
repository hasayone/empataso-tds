package config

import (
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultMetricsDir is where an engine writes its metrics file when EMTDS_METRICS_DIR is empty.
	DefaultMetricsDir = "/var/lib/emtds/metrics"
	// DefaultInstance is the engine instance name when EMTDS_INSTANCE is empty.
	DefaultInstance = "engine"
	// maxInstanceLen bounds the instance name, which becomes a file name.
	maxInstanceLen = 32
)

// MetricsDir returns the directory of the engine metrics files:
// EMTDS_METRICS_DIR, or DefaultMetricsDir when it is empty.
func MetricsDir() string { return EnvOr("EMTDS_METRICS_DIR", DefaultMetricsDir) }

// Instance returns the engine instance name from EMTDS_INSTANCE, trimmed, or
// DefaultInstance when it is empty. The value is not validated: a caller that
// uses it as a file name checks ValidInstance first.
func Instance() string {
	if v := strings.TrimSpace(Env("EMTDS_INSTANCE")); v != "" {
		return v
	}
	return DefaultInstance
}

// ValidInstance reports whether name is safe to use as a metrics file name:
// 1 to 32 characters from [a-z0-9-], not starting with "-".
func ValidInstance(name string) bool {
	if name == "" || len(name) > maxInstanceLen || name[0] == '-' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// MetricsPath returns the metrics file of instance inside dir: "<dir>/<instance>.json".
func MetricsPath(dir, instance string) string {
	return filepath.Join(dir, strings.TrimSpace(instance)+".json")
}

// EngineMetrics is the snapshot an engine publishes: one JSON file per
// instance, refreshed every few seconds, and GET /internal/metrics of the
// engine. The cron service reads the files for the loss alert, so the field
// names are a contract between the two processes. Every counter is cumulative
// since StartedAt; a new StartedAt means a new process and a new base.
type EngineMetrics struct {
	Instance  string           `json:"instance"`
	StartedAt time.Time        `json:"started_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Events    EventMetrics     `json:"events"`
	Postbacks PostbackMetrics  `json:"postbacks"`
	Outbound  OutboundMetrics  `json:"outbound"`
	Spool     QueueMetrics     `json:"spool"`
	RedisTrim RedisTrimMetrics `json:"redis_trim"`
	Landing LandingMetrics `json:"landing"`
	// TrafficLog counts the click paths written to Redis for the campaign
	// traffic log. They are a diagnostic aid, so a loss is not alerted on.
	TrafficLog TrafficLogMetrics `json:"traffic_log"`
	// OriginLinkAdded is how many own 3xx locations received a path token.
	// OriginLinkUnknown is how many pointed at an active campaign on a host
	// that is not in the domain catalog. RootMissing is how many conversions
	// named a chain root that was not in the lookback window.
	OriginLinkAdded   int64 `json:"origin_link_added"`
	OriginLinkUnknown int64 `json:"origin_link_unknown_host"`
	RootMissing       int64 `json:"root_missing"`
	Previous            *PreviousRun   `json:"previous,omitempty"`
}

// TrafficLogMetrics counts click paths since StartedAt: Written reached Redis,
// Dropped found the write queue full, Failed could not be packed or stored.
type TrafficLogMetrics struct {
	Written int64 `json:"written"`
	Dropped int64 `json:"dropped"`
	Failed  int64 `json:"failed"`
}

// LandingMetrics counts the engine's requests to the PHP sidecar that runs PHP
// landings, cumulative since StartedAt. Requests is every request sent, also
// the failed ones. Timeouts are requests that ran out of time, ConnErrors other
// transport failures (refused or reset connection, an unreadable or oversized
// body) and Status5xx answers with a 5xx status; a request the visitor
// abandoned is counted in Requests only. The four Latency fields split the
// requests by their total time (connect, PHP work and body): up to 100 ms, up to
// 1 s, up to 5 s and above; LatencySumMs is the sum of the times in
// milliseconds, so the mean over an interval is the growth of LatencySumMs
// divided by the growth of Requests. An engine without a PHP sidecar reports
// zeros. No alert is built on these figures.
type LandingMetrics struct {
	Requests     int64 `json:"requests"`
	Timeouts     int64 `json:"timeouts"`
	ConnErrors   int64 `json:"conn_errors"`
	Status5xx    int64 `json:"status_5xx"`
	LatencyLe100 int64 `json:"latency_le_100ms"`
	LatencyLe1s  int64 `json:"latency_le_1s"`
	LatencyLe5s  int64 `json:"latency_le_5s"`
	LatencyGt5s  int64 `json:"latency_gt_5s"`
	LatencySumMs int64 `json:"latency_sum_ms"`
}

// QueueMetrics is the disk queues of the postback path, next to the event queue
// in Events.Spool: Postbacks holds the postbacks accepted while the store was
// down or the click was not there yet, Outbound the outbound jobs not yet
// answered by the network, PostbackLog the journal lines not yet written.
// PostbackLogLost counts journal lines that could not be queued or parked.
type QueueMetrics struct {
	Postbacks       SpoolMetrics `json:"postbacks"`
	Outbound        SpoolMetrics `json:"outbound"`
	PostbackLog     SpoolMetrics `json:"postback_log"`
	PostbackLogLost int64        `json:"postback_log_lost"`
}

// PreviousRun is the last snapshot of the process this one replaced: its
// StartedAt and loss counters, read by the new engine from the old file before
// it writes its own. The cron service counts them once, so losses of the old
// process between its last check and its end are not forgotten at a restart.
type PreviousRun struct {
	StartedAt time.Time  `json:"started_at"`
	Lost      LostEvents `json:"lost"`
	Drops     QueueDrops `json:"drops"`
}

// QueueDrops are the counters of postback work that is gone for good, all
// cumulative since the process start: journal lines lost (PostbackLog),
// outbound jobs dropped (Outbound: the memory queue was full and there was no
// disk copy, or the network never answered and the attempt ceiling was
// reached), postbacks written off because their click never appeared
// (RetryExpired), signed clicks refused because the retry ceiling was full
// (RetryOverflow) and store failures refused because a queue quota was full
// (QueueFull). The cron service alerts on their growth.
type QueueDrops struct {
	PostbackLog   int64 `json:"postback_log"`
	Outbound      int64 `json:"outbound"`
	RetryExpired  int64 `json:"retry_expired"`
	RetryOverflow int64 `json:"retry_overflow"`
	QueueFull     int64 `json:"queue_full"`
}

// Total returns the sum of the counters.
func (d QueueDrops) Total() int64 {
	return d.PostbackLog + d.Outbound + d.RetryExpired + d.RetryOverflow + d.QueueFull
}

// Plus returns the counters of d and b added up.
func (d QueueDrops) Plus(b QueueDrops) QueueDrops {
	return QueueDrops{
		PostbackLog:   d.PostbackLog + b.PostbackLog,
		Outbound:      d.Outbound + b.Outbound,
		RetryExpired:  d.RetryExpired + b.RetryExpired,
		RetryOverflow: d.RetryOverflow + b.RetryOverflow,
		QueueFull:     d.QueueFull + b.QueueFull,
	}
}

// Minus returns d minus b per counter. A counter that went down counts as no
// growth: inside one process the counters only rise.
func (d QueueDrops) Minus(b QueueDrops) QueueDrops {
	return QueueDrops{
		PostbackLog:   max(0, d.PostbackLog-b.PostbackLog),
		Outbound:      max(0, d.Outbound-b.Outbound),
		RetryExpired:  max(0, d.RetryExpired-b.RetryExpired),
		RetryOverflow: max(0, d.RetryOverflow-b.RetryOverflow),
		QueueFull:     max(0, d.QueueFull-b.QueueFull),
	}
}

// Drops returns the loss counters of the postback path in this snapshot.
func (m EngineMetrics) Drops() QueueDrops {
	return QueueDrops{
		PostbackLog:   m.Spool.PostbackLogLost,
		Outbound:      m.Outbound.Dropped,
		RetryExpired:  m.Postbacks.RetryExpired,
		RetryOverflow: m.Postbacks.RetryOverflow,
		QueueFull:     m.Postbacks.QueueFull,
	}
}

// RedisTrimMetrics is the progress of the one-time trim that caps the lifetime of
// old uniq:, uniqo: and st: keys in Redis. Scanned counts keys looked at and
// Trimmed keys whose TTL was lowered or set. Done is true once every pattern was
// walked (or an earlier run with the same ceiling was found). Error is true when
// the walk stopped on a Redis failure or a lost lock. Skipped is "locked" when
// another engine holds the lock and "done_before" when the marker already names
// this ceiling; it is empty otherwise. An engine without Redis reports zeros.
type RedisTrimMetrics struct {
	Scanned int64  `json:"scanned"`
	Trimmed int64  `json:"trimmed"`
	Done    bool   `json:"done"`
	Error   bool   `json:"error"`
	Skipped string `json:"skipped,omitempty"`
}

// EventMetrics describes the click event buffer. Configured is true when a
// ClickHouse address is set (EMTDS_CLICKHOUSE_ADDR); Connected is false while
// the engine has no ClickHouse connection. The buffer exists either way and
// keeps taking events, which wait on the spool, so the figures stay real.
// Configured with Connected false for long is a failure, no matter that no
// loss is counted. Pending estimates the events not yet written. Inserted counts
// events ClickHouse accepted and InsertErrors failed insert calls (one per
// batch). LastInsertOK is null until an insert succeeds. FailingSince is null
// while the last insert succeeded, otherwise the start of the current streak of
// failed inserts. Spool is the disk queue of batches the buffer could not write
// at once; DedupReady is true while the events table deduplicates repeated
// batches by token, which is what lets a batch of unknown fate be written again.
type EventMetrics struct {
	Configured   bool         `json:"configured"`
	Connected    bool         `json:"connected"`
	DedupReady   bool         `json:"dedup_ready"`
	Spool        SpoolMetrics `json:"spool"`
	InputLen     int          `json:"input_len"`
	InputCap     int          `json:"input_cap"`
	QueueLen     int          `json:"queue_len"`
	QueueCap     int          `json:"queue_cap"`
	Pending      int64        `json:"pending"`
	Inserted     int64        `json:"inserted"`
	InsertErrors int64        `json:"insert_errors"`
	LastInsertOK *time.Time   `json:"last_insert_ok"`
	FailingSince *time.Time   `json:"failing_since"`
	Lost         LostEvents   `json:"lost"`
}

// SpoolMetrics describes one disk queue: the event batches, the postbacks, the
// outbound jobs or the journal lines. Enabled is false when the engine runs
// without that queue. The rest is zero then, and otherwise: segment files and
// bytes waiting, entries in them (Events: events in the event queue, journal
// lines in the journal, and one per segment in the postback and outbound
// queues), the age of the oldest in seconds, and Rejected, the files in the
// rejected directory of the queue (set aside as unreadable or unwritable). It is
// counted from the directory at the process start and grows while the process
// runs; it does not fall by itself.
type SpoolMetrics struct {
	Enabled          bool  `json:"enabled"`
	Segments         int   `json:"segments"`
	Bytes            int64 `json:"bytes"`
	Events           int64 `json:"events"`
	OldestAgeSeconds int64 `json:"oldest_age_seconds"`
	Rejected         int64 `json:"rejected"`
}

// LostEvents counts events that never reached ClickHouse, by cause: the input
// was full, the batch queue was full, the insert failed, the push came after
// shutdown, the disk queue was full.
type LostEvents struct {
	Full      int64 `json:"full"`
	Queue     int64 `json:"queue"`
	Insert    int64 `json:"insert"`
	Late      int64 `json:"late"`
	SpoolFull int64 `json:"spool_full"`
}

// Total returns the events lost across all causes.
func (l LostEvents) Total() int64 { return l.Full + l.Queue + l.Insert + l.Late + l.SpoolFull }

// PostbackMetrics counts the outcomes of inbound /postback requests: stored,
// refused for a bad payout, without a click id, without a store, click not
// found, and store failure. The network got 200 for Queued (accepted into the
// disk queue because the store failed, timed out or was not connected) and for
// Retry (accepted because the signed click was not there yet). Replayed counts
// queued postbacks later stored. RetryExpired counts postbacks whose click
// never appeared and that were written off as click_not_found. RetryOverflow
// counts missing signed clicks refused with 404 because a retry ceiling was
// reached, and QueueFull counts store failures refused with 503 because a quota
// of the queue or its byte ceiling was reached. A request counted in
// RetryOverflow or QueueFull is not counted again in ClickNotFound, NoStore or
// StoreError: they replace the class postbackResult gave it. BadKey counts
// requests rejected with 403 because the postback key was absent or wrong.
type PostbackMetrics struct {
	OK            int64 `json:"ok"`
	InvalidPayout int64 `json:"invalid_payout"`
	NoSubID       int64 `json:"no_subid"`
	NoStore       int64 `json:"no_store"`
	ClickNotFound int64 `json:"click_not_found"`
	StoreError    int64 `json:"store_error"`
	Queued        int64 `json:"queued"`
	Retry         int64 `json:"retry"`
	Replayed      int64 `json:"replayed"`
	RetryExpired  int64 `json:"retry_expired"`
	RetryOverflow int64 `json:"retry_overflow"`
	QueueFull     int64 `json:"queue_full"`
	BadKey        int64 `json:"bad_key"`
}

// OutboundMetrics counts outbound postback jobs: queued, delivered (answer
// below 400), failed (blocked address, transport error or answer 400 and
// above) and dropped. Dropped is a loss: the job found the memory queue full and
// had no disk copy, so it is never sent. The cron service alerts on its growth.
type OutboundMetrics struct {
	Queued  int64 `json:"queued"`
	Sent    int64 `json:"sent"`
	Failed  int64 `json:"failed"`
	Dropped int64 `json:"dropped"`
}
