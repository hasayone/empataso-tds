package admin

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/emtds/bridge/ktimport"
)

// keitaroStreamsMaxAge is how long the streams read by an unfinished import stay usable.
// An older file is dropped and the next attempt reads the streams of every campaign again.
const keitaroStreamsMaxAge = 48 * time.Hour

// keitaroStreamsHead is the first line of the streams file.
// Scope is a hash of the tracker host, so streams of another tracker are never reused;
// At is when the file was started and is not renewed by later attempts.
type keitaroStreamsHead struct {
	Scope string `json:"scope"`
	At    string `json:"at"`
}

// keitaroStreamsLine is one campaign line of the streams file: the body the tracker returned.
type keitaroStreamsLine struct {
	ID      string          `json:"id"`
	Streams json.RawMessage `json:"streams"`
}

// keitaroStreamsStore keeps the campaign streams of the running import snapshot on the state volume,
// one line per campaign, so a stop, a lost connection or an admin restart does not read them again.
// It holds streams only, never the tracker address or the key. add is safe for parallel callers.
type keitaroStreamsStore struct {
	path  string
	scope string
	warn  func(string, ...any)
	// known are the streams read by earlier attempts, keyed by campaign id.
	known map[string][]ktimport.KStream

	mu     sync.Mutex
	file   *os.File
	failed bool
}

// keitaroStreamsScope hashes a tracker host for the streams file head.
// host is the tracker host. It returns a hex digest and does not return an error.
func keitaroStreamsScope(host string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(host))))
	return hex.EncodeToString(sum[:8])
}

// keitaroStreamsPath is the streams file that goes with a journal file.
// journal is the path of keitaro-job.json. It returns "" for an empty journal path.
func keitaroStreamsPath(journal string) string {
	if journal == "" {
		return ""
	}
	return strings.TrimSuffix(journal, ".json") + "-streams.jsonl"
}

// openKeitaroStreams reads what an earlier attempt of the import left in the streams file.
// path is the file; an empty path keeps nothing. host is the tracker host; now is the clock.
// A file of another host, older than keitaroStreamsMaxAge, or with an unreadable head is removed.
// A line cut by a crash and everything after it is cut off the file, so later lines stay whole.
// warn receives a read or write failure and may be nil. The store is returned even when the
// file is unusable: then known is empty and the streams are read again. It does not return an error.
func openKeitaroStreams(path, host string, now time.Time, warn func(string, ...any)) *keitaroStreamsStore {
	st := &keitaroStreamsStore{path: path, scope: keitaroStreamsScope(host), warn: warn, known: map[string][]ktimport.KStream{}}
	if path == "" {
		return st
	}
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			st.warnf("keitaro streams file unreadable", "err", err)
		}
		return st
	}
	good, ok := st.load(f, now)
	_ = f.Close()
	if !ok {
		st.known = map[string][]ktimport.KStream{}
		removeKeitaroStreams(path)
		return st
	}
	if err := os.Truncate(path, good); err != nil {
		st.warnf("keitaro streams file not trimmed", "err", err)
		st.known = map[string][]ktimport.KStream{}
		removeKeitaroStreams(path)
	}
	return st
}

// load reads the head and the campaign lines of an open streams file into known.
// f is the file; now is the clock. It returns the size of the whole-line prefix and true when
// the head is valid, matches the scope and is not older than keitaroStreamsMaxAge.
// It returns false for any other head. A broken or unfinished line ends the read.
func (st *keitaroStreamsStore) load(f *os.File, now time.Time) (int64, bool) {
	rd := bufio.NewReaderSize(f, 1<<20)
	var good int64
	first := true
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			break
		}
		if first {
			var head keitaroStreamsHead
			if json.Unmarshal(line, &head) != nil {
				return 0, false
			}
			at, perr := time.Parse(time.RFC3339, head.At)
			if perr != nil || head.Scope != st.scope || now.Sub(at) > keitaroStreamsMaxAge {
				return 0, false
			}
			first = false
			good += int64(len(line))
			continue
		}
		var rec keitaroStreamsLine
		var streams []ktimport.KStream
		if json.Unmarshal(bytes.TrimSpace(line), &rec) != nil || rec.ID == "" || json.Unmarshal(rec.Streams, &streams) != nil {
			break
		}
		st.known[rec.ID] = streams
		good += int64(len(line))
	}
	return good, !first
}

// add appends the streams of one campaign to the file.
// id is the Keitaro campaign id; raw is the JSON array the tracker returned.
// The head is written first when the file is new. A write failure goes to warn once and stops
// further writes; the import itself does not stop for it. It does not return an error.
func (st *keitaroStreamsStore) add(id string, raw []byte) {
	if st == nil || st.path == "" {
		return
	}
	line, err := json.Marshal(keitaroStreamsLine{ID: id, Streams: json.RawMessage(raw)})
	if err != nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.failed {
		return
	}
	if st.file == nil {
		f, err := os.OpenFile(st.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			st.failLocked(err)
			return
		}
		st.file = f
		if info, err := f.Stat(); err != nil || info.Size() == 0 {
			head, _ := json.Marshal(keitaroStreamsHead{Scope: st.scope, At: time.Now().UTC().Format(time.RFC3339)})
			if _, err := f.Write(append(head, '\n')); err != nil {
				st.failLocked(err)
				return
			}
		}
	}
	if _, err := st.file.Write(append(line, '\n')); err != nil {
		st.failLocked(err)
	}
}

// failLocked stops further writes after a failure and reports it. st.mu is held.
// err is the failure. It does not return an error.
func (st *keitaroStreamsStore) failLocked(err error) {
	st.failed = true
	st.warnf("keitaro streams file not written", "err", err)
}

// close releases the file handle. A store that never wrote does nothing. It does not return an error.
func (st *keitaroStreamsStore) close() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.file != nil {
		_ = st.file.Close()
		st.file = nil
	}
}

// warnf passes a message to the warn hook when one is set.
func (st *keitaroStreamsStore) warnf(msg string, args ...any) {
	if st.warn != nil {
		st.warn(msg, args...)
	}
}

// removeKeitaroStreams deletes the streams file. A missing file is not a failure.
// path may be empty. It does not return an error.
func removeKeitaroStreams(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// dropStreams removes the streams file of the job, once the import no longer needs it:
// it finished, its catalogs passed, a new import starts or its data was purged.
// The file is not on disk for a job without a journal file. It does not return an error.
func (j *keitaroJob) dropStreams() {
	removeKeitaroStreams(keitaroStreamsPath(j.file))
}
