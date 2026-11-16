package main

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egerkuzma/emtds/internal/config"
)

const (
	// maxKeyBytes is the longest keyword line written, in bytes, without the line feed.
	maxKeyBytes = 255

	// defaultKeysMaxBytes caps one daily keyword file. At 30 to 60 bytes a keyword it
	// holds 150 to 280 thousand lines, more than a day of one campaign needs, and 30
	// days of both lists stay under 0.5 GiB per campaign.
	defaultKeysMaxBytes = 8 << 20

	// keyDropLogEvery is the shortest pause between two "limit reached" log lines.
	keyDropLogEvery = 10 * time.Minute
)

// keyFileID names one daily keyword file of a campaign.
type keyFileID struct {
	group string
	se    bool
}

// keyFile is the write state of one daily keyword file.
// mu guards every field. size is the file length as last seen by this process,
// valid when known is true. buf is reused to build the line.
type keyFile struct {
	mu    sync.Mutex
	path  string
	size  int64
	known bool
	buf   []byte
}

// keyWriter appends collected keywords to dir/<group>/<date>[-se].dat.
// One file stops growing at limit bytes; limit 0 or below means no cap.
// The size is read once per file and day and then counted in memory.
type keyWriter struct {
	dir   string
	limit int64
	log   *slog.Logger

	mu      sync.Mutex
	day     int
	dayName string
	files   map[keyFileID]*keyFile

	dropped  atomic.Int64
	lastWarn atomic.Int64
}

// newKeyWriter returns a writer into dir with a daily cap of limit bytes per file.
// An empty dir disables writing. log receives the "limit reached" notices.
func newKeyWriter(dir string, limit int64, log *slog.Logger) *keyWriter {
	return &keyWriter{dir: dir, limit: limit, log: log, files: map[keyFileID]*keyFile{}}
}

// keysMaxBytes returns the daily file cap from EMTDS_KEYS_MAX_BYTES in bytes.
// Unset gives defaultKeysMaxBytes, 0 disables the cap. A value that is not a
// whole number or is negative is logged to log and gives the default.
func keysMaxBytes(log *slog.Logger) int64 {
	v := config.Env("EMTDS_KEYS_MAX_BYTES")
	if v == "" {
		return defaultKeysMaxBytes
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		log.Warn("EMTDS_KEYS_MAX_BYTES is not a byte count, using the default", "value", v, "default", defaultKeysMaxBytes)
		return defaultKeysMaxBytes
	}
	return n
}

// add writes value as one line of today's file of group; se selects the search-engine list.
// The value is normalized by normalizeKey; an empty result, an empty group, a nil
// writer and a full file write nothing. Disk errors are dropped: a lost keyword
// must not fail the click.
func (w *keyWriter) add(group, value string, se bool) {
	if w == nil || w.dir == "" || group == "" {
		return
	}
	line := normalizeKey(value)
	if line == "" {
		return
	}
	f := w.file(group, se, time.Now())
	f.mu.Lock()
	defer f.mu.Unlock()
	need := int64(len(line)) + 1
	if f.known && w.full(f, need) {
		return
	}
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if errors.Is(err, fs.ErrNotExist) {
		if os.MkdirAll(filepath.Dir(f.path), 0o755) != nil {
			return
		}
		f.known = false
		fh, err = os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	}
	if err != nil {
		return
	}
	defer fh.Close()
	if !f.known {
		st, err := fh.Stat()
		if err != nil {
			return
		}
		f.size, f.known = st.Size(), true
		if w.full(f, need) {
			return
		}
	}
	f.buf = append(append(f.buf[:0], line...), '\n')
	n, _ := fh.Write(f.buf)
	f.size += int64(n)
}

// file returns the state of today's file of group, creating it on first use.
// now picks the day; on a new day the states of the previous day are dropped.
func (w *keyWriter) file(group string, se bool, now time.Time) *keyFile {
	y, m, d := now.Date()
	day := y*10000 + int(m)*100 + d
	w.mu.Lock()
	defer w.mu.Unlock()
	if day != w.day {
		w.day, w.dayName = day, now.Format("2006-01-02")
		w.files = map[keyFileID]*keyFile{}
	}
	id := keyFileID{group: group, se: se}
	f := w.files[id]
	if f == nil {
		name := w.dayName
		if se {
			name += "-se"
		}
		f = &keyFile{path: filepath.Join(w.dir, group, name+".dat")}
		w.files[id] = f
	}
	return f
}

// full reports whether need more bytes would take f past the cap.
// A refused line is counted; at most once per keyDropLogEvery the count is logged and reset.
// The caller holds f.mu.
func (w *keyWriter) full(f *keyFile, need int64) bool {
	if w.limit <= 0 || f.size+need <= w.limit {
		return false
	}
	w.dropped.Add(1)
	now := time.Now().UnixNano()
	last := w.lastWarn.Load()
	if now-last >= int64(keyDropLogEvery) && w.lastWarn.CompareAndSwap(last, now) && w.log != nil {
		w.log.Warn("keys: daily file limit reached, keywords dropped",
			"file", f.path, "limit", w.limit, "dropped", w.dropped.Swap(0))
	}
	return true
}

// normalizeKey turns value into one keyword line.
// Runs of control characters, line and paragraph separators become one space,
// invalid UTF-8 bytes are removed, the ends are trimmed and the result is cut to
// maxKeyBytes on a rune boundary. A clean value is returned without copying.
func normalizeKey(value string) string {
	if keyNeedsCleaning(value) {
		value = cleanKey(value)
	}
	value = strings.TrimSpace(value)
	if len(value) <= maxKeyBytes {
		return value
	}
	cut := maxKeyBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut])
}

// keyNeedsCleaning reports whether value holds a rune cleanKey changes.
func keyNeedsCleaning(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; c < 0x20 || c >= 0x7f {
			return slowKeyCheck(value[i:])
		}
	}
	return false
}

// slowKeyCheck is keyNeedsCleaning for the part of a value that is not plain ASCII.
func slowKeyCheck(value string) bool {
	for i, r := range value {
		if isKeyBreak(r) {
			return true
		}
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(value[i:]); size == 1 {
				return true
			}
		}
	}
	return false
}

// isKeyBreak reports whether r would split or garble a line: a control character
// or a Unicode line or paragraph separator.
func isKeyBreak(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}

// cleanKey returns value with every run of break runes as one space and invalid bytes removed.
func cleanKey(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	space := false
	for i, r := range value {
		switch {
		case isKeyBreak(r):
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		case r == utf8.RuneError:
			if _, size := utf8.DecodeRuneInString(value[i:]); size == 1 {
				continue
			}
		}
		b.WriteRune(r)
		space = false
	}
	return b.String()
}

// parseSEKeyword extracts the search query from the referer.
func parseSEKeyword(referer string) string {
	if referer == "" || referer == "-" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	q := u.Query()
	switch {
	case strings.Contains(host, "yandex"):
		return strings.TrimSpace(q.Get("text"))
	case strings.Contains(host, "rambler"), strings.Contains(host, "tut.by"):
		return strings.TrimSpace(q.Get("query"))
	case strings.Contains(host, "yahoo"):
		return strings.TrimSpace(q.Get("p"))
	case strings.Contains(host, "google"), strings.Contains(host, "mail.ru"), strings.Contains(host, "bing"):
		return strings.TrimSpace(q.Get("q"))
	}
	return ""
}
