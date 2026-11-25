package logbuf

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/hasayone/empataso-tds/internal/config"
)

// The spool is a directory of segments on disk where a process parks batches
// it cannot deliver right now: the storage is down, the queue is full, the
// process is stopping. A segment is one batch, written whole and flushed to the
// disk before the writer reports success, so a power cut, a kill or a restart
// leaves it for the next start.
//
// Layout, one root per volume:
//
//	<root>/<instance>/<kind>/<name>.seg      pending segments, oldest first by name
//	<root>/<instance>/<kind>/rejected/       segments the reader refused
//
// <kind> is the sort of data ("events"; the postback and outbound queues use
// the same library). <instance> is EMTDS_INSTANCE: stable across restarts, so a
// restarted process finds its own segments. Directories are 0700, segments 0600.

const (
	// DefaultSpoolDir is the spool root when EMTDS_SPOOL_DIR is empty.
	DefaultSpoolDir = "/var/lib/emtds-spool"
	// DefaultSpoolMaxBytes is the ceiling of everything under the root, 2 GiB.
	DefaultSpoolMaxBytes int64 = 2 << 30
	// DefaultSpoolMinFreePct is the share of the volume that must stay free.
	DefaultSpoolMinFreePct = 10

	spoolDirMode  = 0o700
	spoolFileMode = 0o600
	// segmentExt is the suffix of one spool segment file.
	segmentExt = ".seg"

	// rejectedDir is the subdirectory that keeps what could not be read.
	rejectedDir = "rejected"
	// usageTTL is how long the size of the whole root is trusted before it is
	// measured again; other processes write to the root as well.
	usageTTL = 2 * time.Second
)

var (
	// ErrSpoolFull is returned by Put when a segment would pass the size
	// ceiling or push the free space of the volume below its floor.
	ErrSpoolFull = errors.New("logbuf: spool is full")
	// ErrSpoolLocked is returned by OpenSpool when a live process already holds
	// the instance directory.
	ErrSpoolLocked = errors.New("logbuf: spool instance is held by another process")
	// ErrSegmentRejected is returned by Load when the segment was unreadable
	// and has been moved to the rejected directory.
	ErrSegmentRejected = errors.New("logbuf: spool segment rejected")
)

// Spool is the directory of one engine instance under the spool root. It holds
// the lock on that directory until Close. Methods are safe for concurrent use.
type Spool struct {
	cfg     SpoolConfig
	dir     string
	log     *slog.Logger
	release func()

	mu     sync.Mutex
	queues map[string]*SpoolQueue

	usageMu    sync.Mutex
	usageBytes int64
	usageAt    time.Time

	nameMu   sync.Mutex
	lastName int64
}

// OpenSpool opens the spool of cfg.Instance under cfg.Root.
//
// It creates the directories (0700), takes the exclusive lock of the instance
// directory, removes temporary files a crash left behind, proves that the
// volume accepts a durable write, adopts the segments of instance directories
// nobody holds any more, and logs the path, the size, the number of segments and
// the free space.
//
// It returns ErrSpoolLocked when a live process holds the same instance, since
// two writers on one directory would replay each other's segments. Any other
// error names the path and the operation, for example a volume that is
// read-only or owned by another user.
func OpenSpool(cfg SpoolConfig) (*Spool, error) {
	if cfg.Root == "" {
		return nil, errors.New("spool: no root directory")
	}
	if !config.ValidInstance(cfg.Instance) {
		return nil, fmt.Errorf("spool: instance %q must be 1-32 characters from [a-z0-9-] and not start with '-'", cfg.Instance)
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultSpoolMaxBytes
	}
	if cfg.MinFreePct < 0 || cfg.MinFreePct > 90 {
		return nil, fmt.Errorf("spool: the free-space floor must be 0 to 90 percent, got %d", cfg.MinFreePct)
	}
	if cfg.Free == nil {
		cfg.Free = freeSpace
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	if cfg.CheckMount {
		isMount := cfg.IsMount
		if isMount == nil {
			isMount = IsMountPoint
		}
		switch mounted, err := isMount(cfg.Root); {
		case err != nil:
			log.Warn("spool: cannot tell whether the root is a mount point", "path", cfg.Root, "err", err)
		case !mounted:
			log.Warn("spool: the root is a plain directory of the container's own layer, so the queue is lost when the container is recreated; mount a volume at EMTDS_SPOOL_DIR", "path", cfg.Root)
		}
	}
	dir := filepath.Join(cfg.Root, cfg.Instance)
	if err := os.MkdirAll(dir, spoolDirMode); err != nil {
		return nil, fmt.Errorf("spool: cannot create %s: %w (the volume must be writable by the engine user)", dir, err)
	}
	release, err := lockDir(dir)
	if err != nil {
		if errors.Is(err, ErrSpoolLocked) {
			return nil, fmt.Errorf("spool: instance %q under %s: %w; give every engine its own EMTDS_INSTANCE", cfg.Instance, cfg.Root, err)
		}
		return nil, fmt.Errorf("spool: cannot lock %s: %w", dir, err)
	}
	s := &Spool{cfg: cfg, dir: dir, log: log, release: release, queues: map[string]*SpoolQueue{}}
	if err := s.probe(); err != nil {
		release()
		return nil, err
	}
	removeTemp(dir)
	s.adoptForeign()
	segments, bytesUsed := countSegments(dir)
	free, total, ferr := cfg.Free(dir)
	attrs := []any{"path", dir, "bytes", bytesUsed, "segments", segments, "max_bytes", cfg.MaxBytes, "min_free_pct", cfg.MinFreePct}
	if ferr == nil && total > 0 {
		attrs = append(attrs, "free_bytes", free, "free_pct", int(free*100/total))
	}
	log.Info("spool ready", attrs...)
	return s, nil
}

// probe proves that a durable write into the instance directory works, so a
// missing permission shows at start and not at the first outage. It returns an
// error that names the directory.
func (s *Spool) probe() error {
	p := filepath.Join(s.dir, ".probe")
	if err := atomicfile.WriteDurable(p, []byte("ok"), spoolFileMode); err != nil {
		return fmt.Errorf("spool: %s does not accept writes: %w (the volume must be writable by the engine user)", s.dir, err)
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("spool: cannot remove the probe file in %s: %w", s.dir, err)
	}
	return nil
}

// Dir returns the instance directory.
func (s *Spool) Dir() string { return s.dir }

// Close releases the lock of the instance directory. Segments stay on disk.
// It is safe to call more than once.
func (s *Spool) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release != nil {
		s.release()
		s.release = nil
	}
}

// Queue returns the queue of the given kind, creating its directory and
// reading the segments that are already there. kind must be 1-32 characters
// from [a-z0-9-]. The same queue is returned on every call. Segments that fail
// the header check are moved to rejected and counted. It returns an error when
// the directory cannot be created or read.
func (s *Spool) Queue(kind string) (*SpoolQueue, error) {
	if !config.ValidInstance(kind) {
		return nil, fmt.Errorf("spool: kind %q is not a valid name", kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if q, ok := s.queues[kind]; ok {
		return q, nil
	}
	q := &SpoolQueue{s: s, kind: kind, dir: filepath.Join(s.dir, kind)}
	if err := os.MkdirAll(filepath.Join(q.dir, rejectedDir), spoolDirMode); err != nil {
		return nil, fmt.Errorf("spool: cannot create %s: %w", q.dir, err)
	}
	if err := q.scan(); err != nil {
		return nil, err
	}
	s.queues[kind] = q
	return q, nil
}

// rootUsageLocked returns the bytes held by every file under the root, measured
// at most once per usageTTL. The root is shared by all instances on the volume.
// The caller holds usageMu.
func (s *Spool) rootUsageLocked() int64 {
	if time.Since(s.usageAt) > usageTTL {
		var n int64
		_ = filepath.WalkDir(s.cfg.Root, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, ierr := d.Info(); ierr == nil {
				n += info.Size()
			}
			return nil
		})
		s.usageBytes, s.usageAt = n, time.Now()
	}
	return s.usageBytes
}

// addUsage changes the cached size of the root by delta bytes.
func (s *Spool) addUsage(delta int64) {
	s.usageMu.Lock()
	s.usageBytes += delta
	if s.usageBytes < 0 {
		s.usageBytes = 0
	}
	s.usageMu.Unlock()
}

// reserve checks that a segment of size bytes fits and, when it does, books the
// bytes before anything is written: under the size ceiling of the whole root
// and, where the volume statistics are readable, leaving the free-space floor
// intact. Check and booking happen under one lock, so two writers racing for the
// last bytes cannot both pass. dir is the directory the segment goes to.
// It returns ErrSpoolFull when the segment does not fit and books nothing. The
// caller gives the bytes back with addUsage(-size) when the write fails.
func (s *Spool) reserve(dir string, size int64) error {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.rootUsageLocked()+size > s.cfg.MaxBytes {
		return fmt.Errorf("%w: the ceiling of %d bytes is reached", ErrSpoolFull, s.cfg.MaxBytes)
	}
	if free, total, err := s.cfg.Free(dir); err == nil && total > 0 {
		if free < uint64(size) || (free-uint64(size))*100 < total*uint64(s.cfg.MinFreePct) {
			return fmt.Errorf("%w: less than %d%% of the volume would stay free", ErrSpoolFull, s.cfg.MinFreePct)
		}
	}
	s.usageBytes += size
	return nil
}

// nextName returns a segment name that sorts after every name this process has
// produced: the time in nanoseconds, zero padded, and a random suffix. Names
// order the queue, oldest first.
func (s *Spool) nextName() string {
	s.nameMu.Lock()
	defer s.nameMu.Unlock()
	n := time.Now().UnixNano()
	if n <= s.lastName {
		n = s.lastName + 1
	}
	s.lastName = n
	return fmt.Sprintf("%020d-%04x%s", n, rand.Uint32()&0xffff, segmentExt)
}

// SpoolStats describes one queue: the pending segments, their bytes and
// records, the age of the oldest, and how many segments sit in rejected.
type SpoolStats struct {
	Segments  int
	Bytes     int64
	Events    int64
	OldestAge time.Duration
	Rejected  int64
}

// segmentEntry is the index record of a pending segment.
type segmentEntry struct {
	Segment
	created time.Time
}

// SpoolQueue is the segments of one kind of data of one instance. Methods are
// safe for concurrent use. The index lives in memory and is built by one scan
// at open, so a hot call never lists the directory.
type SpoolQueue struct {
	s    *Spool
	kind string
	dir  string

	mu       sync.Mutex
	segs     []segmentEntry // oldest first
	bytes    int64
	events   int64
	rejected int64
}

// scan indexes the segments already in the directory and counts the rejected
// ones. A segment whose header is not version 1 is moved to rejected. A segment
// that cannot be read because of an I/O error is logged and left in place, out
// of the index, to be looked at again at the next start.
func (q *SpoolQueue) scan() error {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		return fmt.Errorf("spool: cannot read %s: %w", q.dir, err)
	}
	if rej, rerr := os.ReadDir(filepath.Join(q.dir, rejectedDir)); rerr == nil {
		q.rejected = int64(len(rej))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), segmentExt) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		meta, herr := readHeaderFile(filepath.Join(q.dir, e.Name()))
		if herr != nil {
			if !errors.Is(herr, errBadHeader) {
				// The disk failed, not the file: leave it where it is for the
				// next start instead of setting aside a segment that may be fine.
				q.s.log.Error("spool: cannot read a segment header, left in place", "kind", q.kind, "segment", e.Name(), "err", herr)
				continue
			}
			q.quarantineFile(e.Name(), herr)
			continue
		}
		q.add(segmentEntry{Segment: Segment{SegmentMeta: meta, Name: e.Name(), Size: info.Size()}, created: createdOf(e.Name(), info.ModTime())})
	}
	return nil
}

// add inserts an index record at its place by name, so the index stays oldest
// first even when two writers finish their files out of order, and adds it to
// the totals. Callers hold q.mu or own the queue exclusively.
func (q *SpoolQueue) add(e segmentEntry) {
	i := sort.Search(len(q.segs), func(i int) bool { return q.segs[i].Name > e.Name })
	q.segs = append(q.segs, segmentEntry{})
	copy(q.segs[i+1:], q.segs[i:])
	q.segs[i] = e
	q.bytes += e.Size
	q.events += int64(e.Count)
}

// createdOf returns the creation time encoded in a segment name, or fallback
// when the name does not carry one.
func createdOf(name string, fallback time.Time) time.Time {
	if len(name) >= 20 {
		if ns, err := strconv.ParseInt(name[:20], 10, 64); err == nil && ns > 0 {
			return time.Unix(0, ns)
		}
	}
	return fallback
}

// Put writes one segment durably and adds it to the queue.
// meta.Token must be 1 to 255 bytes. payload is stored compressed.
//
// It returns an error wrapping ErrSpoolFull when the segment would pass the
// size ceiling of the root or leave less free space on the volume than the
// floor allows; the caller then counts the records as lost. A failed write is
// returned as it is and leaves no partial segment: the file appears whole or
// not at all. A failure of the directory sync after the rename is not such a
// failure: the segment is in place and complete, so it is indexed, the failure
// is logged and Put returns nil. The bytes are booked against the ceiling before
// the write, so concurrent callers cannot overshoot it, and given back when the
// write fails. The segment takes its place in the index by name, not at the end.
// Put blocks for the duration of one fsync of the file and one of the
// directory: milliseconds on a healthy disk, much longer on a stalled one. The
// click path never calls it, because events reach the disk through the buffer.
// The postback path does call it inside the request on purpose: a postback or an
// outbound job is acknowledged only after it is durable, and the answer waits
// for the disk. A caller that cannot afford that wait must queue the write
// itself.
func (q *SpoolQueue) Put(meta SegmentMeta, payload []byte) error {
	data, err := encodeSegment(meta, payload)
	if err != nil {
		return err
	}
	size := int64(len(data))
	if err := q.s.reserve(q.dir, size); err != nil {
		return err
	}
	name := q.s.nextName()
	if err := atomicfile.WriteDurable(filepath.Join(q.dir, name), data, spoolFileMode); err != nil {
		if !errors.Is(err, atomicfile.ErrSyncDir) {
			q.s.addUsage(-size)
			return fmt.Errorf("spool: write %s: %w", name, err)
		}
		q.s.log.Error("spool: the directory was not synced after writing a segment; it stays queued", "kind", q.kind, "segment", name, "err", err)
	}
	q.mu.Lock()
	q.add(segmentEntry{Segment: Segment{SegmentMeta: meta, Name: name, Size: size}, created: createdOf(name, time.Now())})
	q.mu.Unlock()
	return nil
}

// Oldest returns the oldest pending segment for which skip returns false; skip
// may be nil. skip runs under the queue lock and must not call the queue. ok is
// false when there is none.
func (q *SpoolQueue) Oldest(skip func(Segment) bool) (seg Segment, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.segs {
		if skip == nil || !skip(e.Segment) {
			return e.Segment, true
		}
	}
	return Segment{}, false
}

// Load reads and inflates the segment and returns its payload.
//
// A segment that cannot be read as version 1 — an unknown version, a damaged
// header or body — is not deleted: it is moved to the rejected directory,
// counted in Stats().Rejected, logged, and Load returns an error wrapping
// ErrSegmentRejected. A segment whose file has vanished is dropped from the
// index and reported as fs.ErrNotExist. Any other read error is returned as it
// is and the segment stays queued.
func (q *SpoolQueue) Load(seg Segment) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(q.dir, seg.Name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			q.drop(seg.Name)
		}
		return nil, err
	}
	_, payload, derr := decodeSegment(data)
	if derr != nil {
		q.quarantine(seg.Name, derr)
		return nil, fmt.Errorf("%w: %s: %v", ErrSegmentRejected, seg.Name, derr)
	}
	return payload, nil
}

// Remove deletes a segment after its records were delivered and takes it off
// the queue. A file that is already gone is not an error. Other errors are
// returned and the segment stays queued.
func (q *SpoolQueue) Remove(seg Segment) error {
	err := os.Remove(filepath.Join(q.dir, seg.Name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	q.drop(seg.Name)
	q.s.addUsage(-seg.Size)
	return nil
}

// MarkSent records, before an attempt to deliver seg, that the attempt may reach
// the storage: it rewrites the segment with the sent flag set, atomically and
// durably, so a crash during the attempt leaves a segment that says so. The
// index is updated first and stays updated even when the rewrite fails, which is
// reported in the error: the caller holds the segment back when it cannot risk
// an attempt that is not on disk. A segment that is already marked is not
// touched. A segment whose file has vanished is dropped and reported as
// fs.ErrNotExist.
func (q *SpoolQueue) MarkSent(seg Segment) error {
	if seg.Sent {
		return nil
	}
	q.NoteSent(seg.Name)
	path := filepath.Join(q.dir, seg.Name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			q.drop(seg.Name)
		}
		return err
	}
	if _, perr := parseHeader(bytes.NewReader(data)); perr != nil {
		return perr
	}
	data[flagsOffset] |= segmentSent
	if err := atomicfile.WriteDurable(path, data, spoolFileMode); err != nil && !errors.Is(err, atomicfile.ErrSyncDir) {
		return fmt.Errorf("spool: mark %s sent: %w", seg.Name, err)
	}
	return nil
}

// NoteSent sets the sent flag of the named segment in the index only. It is for
// a segment that was delivered but could not be removed, whose file stays: the
// next attempt is a repeat and must be treated as one.
func (q *SpoolQueue) NoteSent(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.segs {
		if q.segs[i].Name == name {
			q.segs[i].Sent = true
			return
		}
	}
}

// ClearSent records that an attempt to deliver the named segment did not reach
// the storage. name is the segment file. The sent flag is cleared on disk
// first and then in the index, so the batch can be tried again when the table
// cannot deduplicate a repeat.
// It returns an error when the file cannot be read or rewritten. A missing
// file is dropped from the index and reported as fs.ErrNotExist. A header that
// cannot be parsed is returned as that error and the flag is left as it is.
func (q *SpoolQueue) ClearSent(name string) error {
	path := filepath.Join(q.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			q.drop(name)
		}
		return err
	}
	if _, perr := parseHeader(bytes.NewReader(data)); perr != nil {
		return perr
	}
	if data[flagsOffset]&segmentSent != 0 {
		data[flagsOffset] &^= segmentSent
		if err := atomicfile.WriteDurable(path, data, spoolFileMode); err != nil && !errors.Is(err, atomicfile.ErrSyncDir) {
			return fmt.Errorf("spool: clear sent on %s: %w", name, err)
		}
	}
	q.noteUnsent(name)
	return nil
}

// noteUnsent clears the sent flag of the named segment in the index only.
func (q *SpoolQueue) noteUnsent(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.segs {
		if q.segs[i].Name == name {
			q.segs[i].Sent = false
			return
		}
	}
}

// drop takes the named segment off the index.
func (q *SpoolQueue) drop(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, e := range q.segs {
		if e.Name == name {
			q.bytes -= e.Size
			q.events -= int64(e.Count)
			q.segs = append(q.segs[:i], q.segs[i+1:]...)
			return
		}
	}
}

// quarantine moves a pending segment to the rejected directory and drops it
// from the index. It does not fail; a failed rename is logged and the segment
// is left out of the index so that it is not retried forever.
func (q *SpoolQueue) quarantine(name string, reason error) {
	q.drop(name)
	q.quarantineFile(name, reason)
}

// quarantineFile renames a file of the queue directory into rejected and counts
// it. Callers keep the index in step.
func (q *SpoolQueue) quarantineFile(name string, reason error) {
	if err := os.Rename(filepath.Join(q.dir, name), filepath.Join(q.dir, rejectedDir, name)); err != nil {
		q.s.log.Error("spool: cannot move a bad segment aside", "kind", q.kind, "segment", name, "err", err)
		return
	}
	q.mu.Lock()
	q.rejected++
	q.mu.Unlock()
	q.s.log.Error("spool: segment rejected and kept in rejected/", "kind", q.kind, "segment", name, "reason", reason.Error())
}

// Stats returns the pending segments, bytes and records, the age of the oldest
// pending segment and the number of rejected files. It does not touch the disk.
func (q *SpoolQueue) Stats() SpoolStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := SpoolStats{Segments: len(q.segs), Bytes: q.bytes, Events: q.events, Rejected: q.rejected}
	if len(q.segs) > 0 {
		st.OldestAge = time.Since(q.segs[0].created)
	}
	return st
}
