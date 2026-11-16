package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDurableWritesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg")
	if err := WriteDurable(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteDurable(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "two" {
		t.Fatalf("content %q, want the second write", got)
	}
}

func TestWriteDurableLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := WriteDurable(filepath.Join(dir, "seg"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "seg" {
		t.Fatalf("directory holds %v, want only the segment", entries)
	}
}

func TestWriteDurableMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg")
	if err := WriteDurable(path, []byte("x"), 0o600); err != nil {
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

func TestWriteDurableMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "seg")
	if err := WriteDurable(path, []byte("x"), 0o600); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}

func TestWriteDurableEmptyData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg")
	if err := WriteDurable(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("stat %v err=%v, want an empty file", info, err)
	}
}

func TestWriteDurableDirSyncFailureKeepsTheFile(t *testing.T) {
	syncDirFunc = func(string) error { return errors.New("disk said no") }
	defer func() { syncDirFunc = syncDir }()
	path := filepath.Join(t.TempDir(), "seg")
	err := WriteDurable(path, []byte("whole"), 0o600)
	if !errors.Is(err, ErrSyncDir) {
		t.Fatalf("err = %v, want ErrSyncDir", err)
	}
	if got, rerr := os.ReadFile(path); rerr != nil || string(got) != "whole" {
		t.Fatalf("the published file is %q (%v), want it whole", got, rerr)
	}
}
