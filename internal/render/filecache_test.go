package render

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// expireEntry moves the last stat of path in c past FileStatInterval.
func expireEntry(c *fileCache, path string) {
	c.mu.Lock()
	ent := c.items[path]
	ent.checked = time.Now().Add(-FileStatInterval - time.Second)
	c.items[path] = ent
	c.mu.Unlock()
}

// writeWithTime writes body to path and sets its modification time to at.
func writeWithTime(t *testing.T, path, body string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestFileCacheMtimeInvalidation checks the stat interval and re-reading on mtime or size change.
func TestFileCacheMtimeInvalidation(t *testing.T) {
	c := &fileCache{load: loadLines}
	path := filepath.Join(t.TempDir(), "list.dat")
	base := time.Now().Add(-time.Hour)
	writeWithTime(t, path, "one\n", base)
	if got := c.get(path); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("first read = %q", got)
	}
	writeWithTime(t, path, "two\n", base.Add(time.Minute))
	if got := c.get(path); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("inside interval = %q, want cached", got)
	}
	expireEntry(c, path)
	if got := c.get(path); !reflect.DeepEqual(got, []string{"two"}) {
		t.Fatalf("after mtime change = %q", got)
	}
	writeWithTime(t, path, "two\nthree\n", base.Add(time.Minute))
	expireEntry(c, path)
	if got := c.get(path); !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("after size change = %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	expireEntry(c, path)
	if got := c.get(path); got != nil {
		t.Fatalf("removed file = %q, want nil", got)
	}
}

// TestFileCacheUnchangedSkipsRead checks that an unchanged stat reuses the cached slice.
func TestFileCacheUnchangedSkipsRead(t *testing.T) {
	reads := 0
	c := &fileCache{load: func(p string) ([]string, error) { reads++; return loadLines(p) }}
	path := filepath.Join(t.TempDir(), "list.dat")
	writeWithTime(t, path, "a\nb\n", time.Now().Add(-time.Hour))
	c.get(path)
	expireEntry(c, path)
	c.get(path)
	if reads != 1 {
		t.Fatalf("reads = %d, want 1", reads)
	}
}

// TestFileCacheBounded checks that the cache never exceeds fileCacheMax paths.
func TestFileCacheBounded(t *testing.T) {
	c := &fileCache{load: func(string) ([]string, error) { return []string{"x"}, nil }}
	dir := t.TempDir()
	path := filepath.Join(dir, "f.dat")
	writeWithTime(t, path, "x\n", time.Now())
	for i := 0; i < fileCacheMax+50; i++ {
		c.get(fmt.Sprintf("%s?%d", path, i))
	}
	c.get(path)
	if n := len(c.items); n > fileCacheMax {
		t.Fatalf("cache holds %d paths; bound is %d", n, fileCacheMax)
	}
}

// TestFileCacheSkipsLargeFile checks that a file over fileCacheMaxBytes is read on each call and not stored.
func TestFileCacheSkipsLargeFile(t *testing.T) {
	reads := 0
	c := &fileCache{load: func(p string) ([]string, error) { reads++; return loadLines(p) }}
	path := filepath.Join(t.TempDir(), "big.dat")
	lines := fileCacheMaxBytes/1024 + 1
	line := strings.Repeat("x", 1023) + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got := c.get(path); len(got) != lines {
			t.Fatalf("get = %d lines, want %d", len(got), lines)
		}
	}
	if reads != 2 {
		t.Fatalf("reads = %d, want 2 (no caching)", reads)
	}
	if _, ok := c.items[path]; ok {
		t.Fatal("large file must not be stored")
	}
}

// TestFileCacheDropsEntryWhenFileGrows checks that a cached file that later exceeds the limit is evicted.
func TestFileCacheDropsEntryWhenFileGrows(t *testing.T) {
	c := &fileCache{load: loadLines}
	path := filepath.Join(t.TempDir(), "list.dat")
	base := time.Now().Add(-time.Hour)
	writeWithTime(t, path, "a\n", base)
	c.get(path)
	writeWithTime(t, path, strings.Repeat("y", fileCacheMaxBytes+1), base.Add(time.Minute))
	expireEntry(c, path)
	if got := c.get(path); len(got) != 1 {
		t.Fatalf("get = %d lines, want 1", len(got))
	}
	if _, ok := c.items[path]; ok {
		t.Fatal("grown file must be evicted")
	}
}

// TestPoolDirNewFileVisible checks that [RANDDFL] sees a file added to its directory after the interval.
func TestPoolDirNewFileVisible(t *testing.T) {
	dir := t.TempDir()
	pool := filepath.Join(dir, "pool")
	if err := os.Mkdir(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Expand("[RANDDFL-(pool)-1]", MacroDeps{DataDir: dir}); got != "" {
		t.Fatalf("empty pool = %q", got)
	}
	writeWithTime(t, filepath.Join(pool, "a.dat"), "only\n", time.Now())
	if err := os.Chtimes(pool, time.Now().Add(time.Minute), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	expireEntry(poolDirs, pool)
	if got := Expand("[RANDDFL-(pool)-1]", MacroDeps{DataDir: dir}); got != "only" {
		t.Fatalf("pool after add = %q, want only", got)
	}
}

// TestFileCacheConcurrent reads one file and expires its stat from many goroutines for the race detector.
func TestFileCacheConcurrent(t *testing.T) {
	c := &fileCache{load: loadLines}
	path := filepath.Join(t.TempDir(), "list.dat")
	writeWithTime(t, path, "a\n", time.Now())
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if got := c.get(path); len(got) != 1 {
					t.Errorf("get = %q", got)
					return
				}
				if i%50 == 0 {
					expireEntry(c, path)
				}
			}
		}()
	}
	wg.Wait()
}
