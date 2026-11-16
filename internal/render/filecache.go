package render

import (
	"os"
	"strings"
	"sync"
	"time"
)

// FileStatInterval is the minimum gap between stats of one cached file or directory.
// A request inside the gap reuses the cached contents; the file is read again
// only when a later stat shows a different modification time or size, so an
// edit becomes visible within this interval. The engine's page and bot-profile
// caches use the same interval.
const FileStatInterval = 5 * time.Second

// fileCacheMax is the most paths one cache holds. Adding a new path to a full
// cache drops an arbitrary entry.
const fileCacheMax = 1024

// fileCacheMaxBytes is the largest file the cache keeps in memory. A bigger
// file is read on every call and never stored.
const fileCacheMaxBytes = 4 << 20

// fileCache keeps the parsed contents of data files by path.
// load turns a path into the cached values and reports a read error.
// Safe for concurrent use; cached slices are shared and must not be mutated.
type fileCache struct {
	load  func(path string) ([]string, error)
	mu    sync.RWMutex
	items map[string]fileEntry
}

// fileEntry is the last stat of one path.
// vals is what load returned when present is true. mtime and size identify
// the file at that stat. checked is when the stat ran.
type fileEntry struct {
	vals    []string
	present bool
	mtime   time.Time
	size    int64
	checked time.Time
}

// lineFiles caches the non-empty trimmed lines of [RANDLINE] and [RANDDFL] files.
var lineFiles = &fileCache{load: loadLines}

// poolDirs caches the file names [RANDDFL] chooses from in a directory.
var poolDirs = &fileCache{load: loadPoolNames}

// get returns the values for path, re-reading it when the stat interval has
// passed and the file changed. A missing or unreadable path returns nil and
// is retried after the interval. A file larger than fileCacheMaxBytes is read
// on every call without being cached. There is no error return.
func (c *fileCache) get(path string) []string {
	c.mu.RLock()
	ent, ok := c.items[path]
	c.mu.RUnlock()
	now := time.Now()
	if ok && now.Sub(ent.checked) < FileStatInterval {
		return ent.vals
	}
	info, err := os.Stat(path)
	if err != nil {
		c.store(path, fileEntry{checked: now})
		return nil
	}
	if info.Size() > fileCacheMaxBytes {
		c.forget(path)
		vals, err := c.load(path)
		if err != nil {
			return nil
		}
		return vals
	}
	if ok && ent.present && ent.mtime.Equal(info.ModTime()) && ent.size == info.Size() {
		ent.checked = now
		c.store(path, ent)
		return ent.vals
	}
	vals, err := c.load(path)
	if err != nil {
		c.store(path, fileEntry{checked: now})
		return nil
	}
	c.store(path, fileEntry{vals: vals, present: true, mtime: info.ModTime(), size: info.Size(), checked: now})
	return vals
}

// forget drops the cached entry of path, if any.
func (c *fileCache) forget(path string) {
	c.mu.Lock()
	delete(c.items, path)
	c.mu.Unlock()
}

// store records ent for path, evicting an arbitrary entry when the cache is full.
func (c *fileCache) store(path string, ent fileEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]fileEntry{}
	}
	if _, ok := c.items[path]; !ok && len(c.items) >= fileCacheMax {
		for k := range c.items {
			delete(c.items, k)
			break
		}
	}
	c.items[path] = ent
}

// loadLines reads path and returns its non-empty lines with surrounding space trimmed.
// It returns the read error unchanged.
func loadLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out, nil
}

// loadPoolNames lists the entries of dir that [RANDDFL] may pick, in os.ReadDir name order.
// Subdirectories, randdfl.dat and .htaccess are skipped. It returns the read error unchanged.
func loadPoolNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && n != "randdfl.dat" && n != ".htaccess" {
			files = append(files, n)
		}
	}
	return files, nil
}
