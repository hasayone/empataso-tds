package logbuf

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// List returns a copy of the pending segments of the queue, oldest first. It
// serves a caller that has to rebuild its own figures from the segments left by
// an earlier process, such as the token of each one. It does not touch the disk.
func (q *SpoolQueue) List() []Segment {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Segment, len(q.segs))
	for i, e := range q.segs {
		out[i] = e.Segment
	}
	return out
}

// Reject moves a pending segment whose payload the caller cannot use to the
// rejected directory, counts it there and takes it off the queue, the same
// treatment Load gives to a damaged segment. reason is logged. It does not fail.
func (q *SpoolQueue) Reject(seg Segment, reason error) {
	if reason == nil {
		reason = errors.New("the caller refused the payload")
	}
	q.quarantine(seg.Name, reason)
}

// AnyPending reports whether a segment of kind waits in any instance directory
// under the spool root: this instance's, or one left by an engine that is
// stopped or still running. A caller that must not decide before every parked
// record of that kind has reached the storage asks it.
//
// Only files of the queue directory itself count: the rejected directory is a
// subdirectory and is never looked at, so set-aside segments do not keep the
// answer true. With skipSent a segment whose header says an attempt to deliver
// it may have reached the storage is not counted either: while the storage
// cannot deduplicate, the buffer holds such a segment on purpose and never
// writes it, so waiting for it would wait forever. A file whose header cannot be
// read as a segment is not counted, the queue sets it aside when it is loaded;
// a file that cannot be read for a disk reason is counted. A directory that
// cannot be read counts as empty. The scan is not cached, so a caller that asks
// often keeps its own timestamp.
func (s *Spool) AnyPending(kind string, skipSent bool) bool {
	entries, err := os.ReadDir(s.cfg.Root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.cfg.Root, e.Name(), kind))
		if err != nil {
			continue
		}
		dir := filepath.Join(s.cfg.Root, e.Name(), kind)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), segmentExt) {
				continue
			}
			if !skipSent {
				return true
			}
			meta, herr := readHeaderFile(filepath.Join(dir, f.Name()))
			if herr == nil && !meta.Sent || herr != nil && !errors.Is(herr, errBadHeader) {
				return true
			}
		}
	}
	return false
}
