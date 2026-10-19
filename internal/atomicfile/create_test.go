package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCreateIfAbsentCreatesAndKeeps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	created, err := CreateIfAbsent(path, []byte("one"), 0o600)
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	created, err = CreateIfAbsent(path, []byte("two"), 0o600)
	if err != nil || created {
		t.Fatalf("second call: created=%v err=%v", created, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "one" {
		t.Fatalf("content %q, want the first writer's", got)
	}
}

func TestCreateIfAbsentLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k")
	for i := 0; i < 2; i++ {
		if _, err := CreateIfAbsent(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "k" {
		t.Fatalf("directory holds %v, want only the key", entries)
	}
}

func TestCreateIfAbsentMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	if _, err := CreateIfAbsent(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("mode %v is readable by others", perm)
	}
}

func TestCreateIfAbsentMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "k")
	if created, err := CreateIfAbsent(path, []byte("x"), 0o600); err == nil || created {
		t.Fatalf("created=%v err=%v, want an error", created, err)
	}
}

// Many writers race: one wins, the others see its complete content at once.
func TestCreateIfAbsentRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		data := bytes.Repeat([]byte{byte('a' + i%26)}, 64)
		go func() {
			defer wg.Done()
			created, err := CreateIfAbsent(path, data, 0o600)
			if err != nil {
				t.Errorf("err %v", err)
				return
			}
			if created {
				wins.Add(1)
			}
			if got, err := os.ReadFile(path); err != nil || len(got) != 64 {
				t.Errorf("reader saw %d bytes err=%v", len(got), err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d writers won, want 1", wins.Load())
	}
}
