package logbuf

import "time"

// Stats is a point-in-time view of the buffer for metrics.
// InputLen and InputCap are the fill and size of the input channel; QueueLen and
// QueueCap are the same for the batch queue, counted in batches. Pending
// estimates the events not yet written: input channel, accumulator, queued
// batches, the batch being inserted, batches waiting to be parked and the
// events parked in the spool. Inserted counts events storage accepted and
// InsertErrors counts failed insert calls, one per batch attempt.
// LastInsertOK is zero until an insert succeeds. FailingSince is the time of
// the first failed insert of the current streak and is zero while the last
// insert succeeded. Losses is the per-cause loss breakdown. Spool describes the
// segments on disk (zero without a spool); SpoolEnabled is false when the buffer
// has no spool at all. DedupReady tells whether a batch that
// may already be stored may be repeated. Connected tells whether the storage
// connection exists.
type Stats struct {
	InputLen, InputCap int
	QueueLen, QueueCap int
	Pending            int64
	Inserted           int64
	InsertErrors       int64
	LastInsertOK       time.Time
	FailingSince       time.Time
	Losses             Losses
	Spool              SpoolStats
	SpoolEnabled       bool
	DedupReady         bool
	Connected          bool
}

// Losses is a per-cause breakdown of events that never reached storage.
type Losses struct {
	Full      int64 // in-channel was full: the accumulator could not keep up
	Queue     int64 // batch queue and spill channel were full, or no spool took the batch
	Insert    int64 // InsertEvents failed and the batch could not be repeated, or its segment was set aside as unreadable or poisoned
	Late      int64 // pushed after Close shut the door
	SpoolFull int64 // the spool refused the batch: size ceiling, free-space floor or a write error
}

// Total returns the number of lost events across all causes.
func (l Losses) Total() int64 { return l.Full + l.Queue + l.Insert + l.Late + l.SpoolFull }
