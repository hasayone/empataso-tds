package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/render"
)

// pageStatInterval is the minimum gap between stats of one page name and of
// the bot profile file. A request inside the gap reuses the cached contents.
// The file is read again only when a later stat shows a different
// modification time or size. It equals the render data-file interval.
const pageStatInterval = render.FileStatInterval

// pageNameRE matches a page file name. The name ends in .html and otherwise
// contains only letters, digits, dot, underscore and hyphen.
var pageNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.html$`)

// pageCache reads HTML page files and keeps their bodies in memory.
// dir is the directory files are read from; an empty dir serves nothing.
// log receives one warning per missing name and may be nil.
// Get is safe for concurrent callers.
type pageCache struct {
	dir    string
	log    *slog.Logger
	mu     sync.RWMutex
	items  map[string]pageEntry
	warned map[string]struct{}
}

// pageEntry is the last stat of one page name.
// body is the file contents when present is true. mtime and size are the
// file identity from that stat. checked is when the stat ran.
type pageEntry struct {
	body    string
	present bool
	mtime   time.Time
	size    int64
	checked time.Time
}

// newPageCache returns a cache that reads pages from dir.
// log is warned once per missing name; a nil log drops the warning.
// There is no error: a missing directory is reported later, per file.
func newPageCache(dir string, log *slog.Logger) *pageCache {
	return &pageCache{
		dir:    dir,
		log:    log,
		items:  map[string]pageEntry{},
		warned: map[string]struct{}{},
	}
}

// pagesDir returns the directory stream page files are read from.
// explicit is EMTDS_PAGES_DIR. groupsFile is EMTDS_GROUPS_FILE.
// An explicit path wins. Otherwise, when groupsFile is set, the directory
// is "pages" next to that file. Both empty returns "" and no file is read.
func pagesDir(explicit, groupsFile string) string {
	if explicit != "" {
		return explicit
	}
	if groupsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(groupsFile), "pages")
}

// pageRef reports the file name stored in a stream output.
// raw is the output text, with optional surrounding spaces.
// name is the part after @page:. ok is false when raw is not a page reference.
func pageRef(raw string) (name string, ok bool) {
	s := strings.TrimSpace(raw)
	const prefix = "@page:"
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	return strings.TrimSpace(s[len(prefix):]), true
}

// validPageName reports whether name may be opened inside the pages directory.
// name is the file name only. A name containing ".." or any other character
// outside pageNameRE is rejected so the path cannot leave the directory.
func validPageName(name string) bool {
	if name == "" || strings.Contains(name, "..") {
		return false
	}
	return pageNameRE.MatchString(name)
}

// Get returns the body of the page file name.
// name is a file name, not a path. The bool is false for an invalid name,
// an empty cache directory, a missing file, or a read error. There is no
// error return: the caller serves an empty body. A missing file is logged
// once per name.
func (c *pageCache) Get(name string) (string, bool) {
	if c == nil || c.dir == "" || !validPageName(name) {
		return "", false
	}
	if ent, fresh := c.fresh(name); fresh {
		return ent.body, ent.present
	}
	path := filepath.Join(c.dir, name)
	info, err := os.Stat(path)
	if err != nil {
		c.noteMissing(name, err)
		return "", false
	}
	if !info.Mode().IsRegular() {
		c.noteMissing(name, errors.New("not a regular file"))
		return "", false
	}
	if body, ok := c.reuse(name, info); ok {
		return body, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		c.noteMissing(name, err)
		return "", false
	}
	c.store(name, string(b), info)
	return string(b), true
}

// fresh returns the cached entry when its stat is still inside pageStatInterval.
// name is the cache key. fresh is false when the name was never stat'ed or
// the interval has elapsed. There is no error.
func (c *pageCache) fresh(name string) (pageEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ent, ok := c.items[name]
	if !ok || time.Since(ent.checked) >= pageStatInterval {
		return pageEntry{}, false
	}
	return ent, true
}

// reuse returns the cached body when info still matches the stored mtime and size.
// name is the cache key. info is a new stat of that file. The bool is false
// when the file changed or was not cached, and the caller must read it.
// A match refreshes the stat time so the next requests skip the stat.
func (c *pageCache) reuse(name string, info os.FileInfo) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.items[name]
	if !ok || !ent.present || !ent.mtime.Equal(info.ModTime()) || ent.size != info.Size() {
		return "", false
	}
	ent.checked = time.Now()
	c.items[name] = ent
	return ent.body, true
}

// store records a successful read of name.
// body is the file contents. info supplies the mtime and size that later
// stats are compared with. There is no error.
func (c *pageCache) store(name, body string, info os.FileInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]pageEntry{}
	}
	c.items[name] = pageEntry{
		body:    body,
		present: true,
		mtime:   info.ModTime(),
		size:    info.Size(),
		checked: time.Now(),
	}
}

// noteMissing remembers a failed stat or read and warns once per name.
// name is the page file. err is the stat or read error included in the log.
// There is no return: the caller treats the page as absent.
func (c *pageCache) noteMissing(name string, err error) {
	c.mu.Lock()
	if c.items == nil {
		c.items = map[string]pageEntry{}
	}
	c.items[name] = pageEntry{checked: time.Now()}
	_, already := c.warned[name]
	if !already {
		if c.warned == nil {
			c.warned = map[string]struct{}{}
		}
		c.warned[name] = struct{}{}
	}
	c.mu.Unlock()
	if already || c.log == nil {
		return
	}
	c.log.Warn("engine: page file missing", "name", name, "err", err)
}

// applyPage replaces a @page: reference with the file body.
// raw is the stream output after distribution, with optional surrounding space.
// The result is the template to expand. A reference that cannot be read
// becomes an empty body; any other string is returned unchanged.
// There is no error and no fallback to another stream.
func (d *engineDeps) applyPage(raw string) string {
	name, ok := pageRef(raw)
	if !ok {
		return raw
	}
	if d == nil || d.pages == nil {
		return ""
	}
	body, found := d.pages.Get(name)
	if !found {
		return ""
	}
	return body
}
