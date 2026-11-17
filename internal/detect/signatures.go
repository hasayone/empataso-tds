package detect

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Signatures keeps signature lists (signature_ua/ref, ua_blacklist, proxy_org)
// in memory with atomic swap on hot-reload — analogous to ipindex.Set, but for
// strings. proxy_org starts as the built-in hosting/VPN list and is replaced
// when proxy_org.dat is present in the data directory. The UA matcher always
// holds the built-in signatures; signature_ua.dat extends or trims them.
type Signatures struct {
	dir string
	log *slog.Logger

	uaMatcher atomic.Pointer[Matcher]
	ref       atomic.Pointer[[]string]
	uaBlack   atomic.Pointer[[]string]
	proxyOrg  atomic.Pointer[[]string]
	botDB     atomic.Pointer[BotDB]

	stamps map[string]fileStamp
	smu    chan struct{} // lightweight mutex for stamps (buffer 1)
}

type fileStamp struct {
	mod  time.Time
	size int64
}

const (
	fileUA       = "signature_ua"
	fileRef      = "signature_ref"
	fileUABlack  = "ua_blacklist"
	fileProxyOrg = "proxy_org"
)

const (
	// maxUALines caps the number of pattern lines read from signature_ua.dat.
	maxUALines = 20000
	// maxUALineBytes is the longest line of signature_ua.dat that is kept.
	maxUALineBytes = 256
	// maxUABytes is the total size of the pattern lines of signature_ua.dat that is kept (256 KiB).
	maxUABytes = 256 << 10
	// maxBotDBFileBytes caps the size of FileBotSignatures that is read (16 MiB).
	maxBotDBFileBytes = 16 << 20
)

// NewSignatures creates the holder and performs the initial load.
func NewSignatures(dir string, log *slog.Logger) *Signatures {
	if log == nil {
		log = slog.Default()
	}
	s := &Signatures{dir: dir, log: log, stamps: map[string]fileStamp{}, smu: make(chan struct{}, 1)}
	s.smu <- struct{}{}
	empty := []string{}
	s.uaMatcher.Store(DefaultUAMatcher())
	s.ref.Store(&empty)
	s.uaBlack.Store(&empty)
	org := DefaultProxyOrg()
	s.proxyOrg.Store(&org)
	s.botDB.Store(&BotDB{})
	s.Reload()
	return s
}

// BotDB returns the current downloaded bot signature database. It is never nil
// for a holder made by NewSignatures; a nil receiver gives nil, which matches
// nothing. The database is immutable and is replaced as a whole on reload.
func (s *Signatures) BotDB() *BotDB {
	if s == nil {
		return nil
	}
	return s.botDB.Load()
}

// UAMatcher returns the current UA matcher: the built-in signatures and search
// engines plus the lines of signature_ua.dat. It is never nil, and a nil
// receiver gives DefaultUAMatcher. The matcher is immutable and is replaced as
// a whole on reload.
func (s *Signatures) UAMatcher() *Matcher {
	if s == nil {
		return DefaultUAMatcher()
	}
	if m := s.uaMatcher.Load(); m != nil {
		return m
	}
	return DefaultUAMatcher()
}

// Ref returns the current list of referer signatures, lowercased at load.
// The caller must not mutate the slice. A nil receiver gives nil.
func (s *Signatures) Ref() []string {
	if s == nil {
		return nil
	}
	return *s.ref.Load()
}

// UABlacklist returns the current UA blacklist in the file's case: it is matched exactly.
// A nil receiver gives nil.
func (s *Signatures) UABlacklist() []string {
	if s == nil {
		return nil
	}
	return *s.uaBlack.Load()
}

// ProxyOrg returns the current hosting and VPN organization substrings, lowercased.
// The built-in list is used until proxy_org.dat is loaded from the data directory,
// and for a nil receiver. The caller must not mutate the slice. It does not report an error.
func (s *Signatures) ProxyOrg() []string {
	if s == nil {
		return DefaultProxyOrg()
	}
	return *s.proxyOrg.Load()
}

// Reload re-reads changed files and atomically swaps the lists.
// Substring lists are lowercased once here; the exact-match blacklist is kept as is.
// A removed signature_ua.dat returns the UA matcher to the built-in set alone and a
// removed proxy_org.dat returns the built-in organization list; every other removed
// file keeps its previous list.
func (s *Signatures) Reload() {
	s.resetIfRemoved(fileUA, s.resetUA)
	s.resetIfRemoved(fileProxyOrg, s.resetProxyOrg)
	s.reloadOne(fileUA, true, s.readUA, s.applyUA)
	s.reloadOne(fileRef, true, readLines, storeList(&s.ref))
	s.reloadOne(fileUABlack, false, readLines, storeList(&s.uaBlack))
	s.reloadOne(fileProxyOrg, false, readProxyOrgFile, storeList(&s.proxyOrg))
	s.reloadBotDB()
}

// reloadBotDB swaps in FileBotSignatures when its modification time or size
// changed. A file that cannot be read or parsed is logged once and the previous
// database stays; a removed file leaves an empty database, so only the built-in
// signatures count. Clicks keep reading the old database until the swap.
func (s *Signatures) reloadBotDB() {
	path := filepath.Join(s.dir, FileBotSignatures)
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && s.forget(FileBotSignatures) {
			s.botDB.Store(&BotDB{})
			s.log.Info("detect: bot signature database removed, built-in signatures only")
		}
		return
	}
	cur := fileStamp{mod: fi.ModTime(), size: fi.Size()}
	<-s.smu
	prev, known := s.stamps[FileBotSignatures]
	s.stamps[FileBotSignatures] = cur
	s.smu <- struct{}{}
	if known && prev.mod.Equal(cur.mod) && prev.size == cur.size {
		return
	}
	if cur.size > maxBotDBFileBytes {
		s.log.Warn("detect: bot signature database too large, previous kept", "bytes", cur.size, "limit_bytes", maxBotDBFileBytes)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		s.log.Warn("detect: bot signature database unreadable, previous kept", "err", err)
		return
	}
	db, skipped, err := LoadBotDB(b)
	if err != nil {
		s.log.Warn("detect: bot signature database invalid, previous kept", "err", err)
		return
	}
	s.botDB.Store(db)
	s.log.Info("detect: bot signature database loaded", "rules", db.Len(), "weak", db.Weak(), "skipped", skipped)
}

// forget drops the stamp of name and reports whether it was known.
func (s *Signatures) forget(name string) bool {
	<-s.smu
	_, known := s.stamps[name]
	delete(s.stamps, name)
	s.smu <- struct{}{}
	return known
}

// storeList returns an apply function that swaps the list into dst.
func storeList(dst *atomic.Pointer[[]string]) func([]string) {
	return func(lines []string) { dst.Store(&lines) }
}

// resetIfRemoved calls reset and forgets the file stamp when name.dat was loaded
// before and no longer exists, so a removed file returns its list to the built-in
// state. Any other stat error leaves the list as it is.
func (s *Signatures) resetIfRemoved(name string, reset func()) {
	if _, err := os.Stat(filepath.Join(s.dir, name+".dat")); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if s.forget(name) {
		reset()
		s.log.Info("detect: signature file removed, built-in list restored", "name", name)
	}
}

// resetUA returns the UA matcher to the built-in signatures alone.
func (s *Signatures) resetUA() { s.uaMatcher.Store(DefaultUAMatcher()) }

// resetProxyOrg returns the organization list to the built-in one.
func (s *Signatures) resetProxyOrg() {
	org := DefaultProxyOrg()
	s.proxyOrg.Store(&org)
}

// readUA reads the pattern lines of signature_ua.dat without section headers.
// Lines longer than maxUALineBytes are dropped and reported in one warning per
// load. Once the kept lines reach maxUABytes in total, or maxUALines in number,
// the rest is dropped with one warning for each limit.
func (s *Signatures) readUA(path string) ([]string, error) {
	tooLong, overBytes, total := 0, 0, 0
	lines, truncated, err := scanLines(path, maxUALines, func(line string) bool {
		if _, header := sigSection(line); header {
			return true
		}
		if len(line) > maxUALineBytes {
			tooLong++
			return true
		}
		if total+len(line) > maxUABytes {
			overBytes++
			return true
		}
		total += len(line)
		return false
	})
	if err != nil {
		return nil, err
	}
	if tooLong > 0 {
		s.log.Warn("detect: signature_ua lines over the length limit are ignored", "count", tooLong, "limit_bytes", maxUALineBytes)
	}
	if overBytes > 0 {
		s.log.Warn("detect: signature_ua lines over the total size limit are ignored", "count", overBytes, "limit_bytes", maxUABytes)
	}
	if truncated {
		s.log.Warn("detect: signature_ua lines over the limit are ignored", "limit", maxUALines)
	}
	return lines, nil
}

// applyUA splits the lowercased lines of signature_ua.dat into patterns that extend
// the built-in set and "-pattern" lines that disable a built-in pattern with the same
// text, then swaps in the rebuilt matcher.
func (s *Signatures) applyUA(lines []string) {
	extra := make([]string, 0, len(lines))
	var disabled map[string]struct{}
	for _, line := range lines {
		rest, off := strings.CutPrefix(line, "-")
		if !off {
			extra = append(extra, line)
			continue
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			if disabled == nil {
				disabled = map[string]struct{}{}
			}
			disabled[rest] = struct{}{}
		}
	}
	s.uaMatcher.Store(buildUAMatcher(extra, disabled))
}

// reloadOne re-reads name.dat when its modification time or size changed, and
// passes the lines, lowercased when lower is set, to apply. read loads the lines
// of the file. A missing file keeps the previous list; a read error is logged and
// also keeps it.
func (s *Signatures) reloadOne(name string, lower bool, read func(path string) ([]string, error), apply func([]string)) {
	path := filepath.Join(s.dir, name+".dat")
	fi, err := os.Stat(path)
	if err != nil {
		return // no file — keep the previous list
	}
	cur := fileStamp{mod: fi.ModTime(), size: fi.Size()}

	<-s.smu
	prev, known := s.stamps[name]
	s.smu <- struct{}{}
	if known && prev.mod.Equal(cur.mod) && prev.size == cur.size {
		return
	}

	lines, err := read(path)
	if err != nil {
		s.log.Warn("detect: signatures reload failed", "name", name, "err", err)
		return
	}
	if lower {
		for i, line := range lines {
			lines[i] = strings.ToLower(line)
		}
	}
	apply(lines)

	<-s.smu
	s.stamps[name] = cur
	s.smu <- struct{}{}
	s.log.Info("detect: signatures loaded", "name", name, "lines", len(lines))
}

// Watch runs periodic Reload until ctx is cancelled.
func (s *Signatures) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reload()
		}
	}
}

// readLines reads non-empty lines of the file, skipping comments (#).
func readLines(path string) ([]string, error) {
	lines, _, err := scanLines(path, 0, nil)
	return lines, err
}

// scanLines reads the trimmed lines of the file, skipping blank lines, comments
// (#) and lines for which skip returns true; skip may be nil. When limit is
// positive, at most limit lines are returned and truncated reports that more
// were present. The error is a file open or read failure.
func scanLines(path string, limit int, skip func(line string) bool) (lines []string, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || (skip != nil && skip(line)) {
			continue
		}
		if limit > 0 && len(lines) >= limit {
			return lines, true, nil
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}
	return lines, false, nil
}
