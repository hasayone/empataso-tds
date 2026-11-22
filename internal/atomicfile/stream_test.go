package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// writeString is a fill function that writes s.
func writeString(s string) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

// entries lists the names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFromReplacesAfterAccept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen string
	replaced, err := WriteFrom(path, 0o644, writeString("new"), func(tmp string) error {
		b, rerr := os.ReadFile(tmp)
		seen = string(b)
		return rerr
	})
	if err != nil || !replaced {
		t.Fatalf("replaced=%v err=%v", replaced, err)
	}
	if seen != "new" {
		t.Fatalf("accept saw %q, want the finished temporary file", seen)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("content %q", got)
	}
	if names := entries(t, dir); len(names) != 1 {
		t.Fatalf("directory holds %v, want only the target", names)
	}
}

func TestWriteFromRefusedKeepsTheOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := errors.New("not a database")
	replaced, err := WriteFrom(path, 0o644, writeString("junk"), func(string) error { return bad })
	if !errors.Is(err, bad) || replaced {
		t.Fatalf("replaced=%v err=%v, want the accept error", replaced, err)
	}
	failing := func(io.Writer) error { return bad }
	if replaced, err = WriteFrom(path, 0o644, failing, nil); !errors.Is(err, bad) || replaced {
		t.Fatalf("a failed fill: replaced=%v err=%v", replaced, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("content %q, the old file must stay", got)
	}
	if names := entries(t, dir); len(names) != 1 {
		t.Fatalf("directory holds %v, want no temporary file", names)
	}
}

func TestWriteFromKeep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	replaced, err := WriteFrom(path, 0o644, writeString("same"), func(string) error { return ErrKeep })
	if err != nil || replaced {
		t.Fatalf("replaced=%v err=%v, ErrKeep is not an error", replaced, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("content %q", got)
	}
	if names := entries(t, dir); len(names) != 1 {
		t.Fatalf("directory holds %v", names)
	}
}

func TestWriteFromCreatesAndNeedsADirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if replaced, err := WriteFrom(path, 0o644, writeString("x"), nil); err != nil || !replaced {
		t.Fatalf("replaced=%v err=%v", replaced, err)
	}
	if _, err := WriteFrom(filepath.Join(t.TempDir(), "absent", "db"), 0o644, writeString("x"), nil); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}
