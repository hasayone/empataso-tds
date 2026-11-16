package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readKeys returns today's keyword file of group in dir, se selecting the search-engine list.
func readKeys(t *testing.T, dir, group string, se bool) string {
	t.Helper()
	name := time.Now().Format("2006-01-02")
	if se {
		name += "-se"
	}
	b, err := os.ReadFile(filepath.Join(dir, group, name+".dat"))
	if err != nil {
		t.Fatalf("read keys: %v", err)
	}
	return string(b)
}

func TestKeyWriterAdd(t *testing.T) {
	dir := t.TempDir()
	w := newKeyWriter(dir, 0, nil)
	w.add("g1", "hello", false)
	w.add("g1", "world", false)
	w.add("g1", "fromse", true)
	w.add("g1", "", false)
	w.add("g1", " \r\n\t ", false)
	w.add("", "x", false)
	newKeyWriter("", 0, nil).add("g1", "x", false)
	var nilWriter *keyWriter
	nilWriter.add("g1", "x", false)

	if got := readKeys(t, dir, "g1", false); got != "hello\nworld\n" {
		t.Errorf("keys file = %q", got)
	}
	if got := readKeys(t, dir, "g1", true); got != "fromse\n" {
		t.Errorf("se keys file = %q", got)
	}
}

func TestNormalizeKey(t *testing.T) {
	long := strings.Repeat("a", 254) + "ж" + "tail"
	cases := []struct{ in, want string }{
		{"plain key", "plain key"},
		{"  padded  ", "padded"},
		{"one\ntwo", "one two"},
		{"one\r\ntwo", "one two"},
		{"a\x00b\x1fc\x7fd", "a b c d"},
		{"tab\there", "tab here"},
		{"line\u2028sep\u2029par\u0085nel", "line sep par nel"},
		{"bad\xffutf8", "badutf8"},
		{"real \uFFFD stays", "real \uFFFD stays"},
		{"куртка\nзимняя", "куртка зимняя"},
		{"\r\n", ""},
		{strings.Repeat("x", 300), strings.Repeat("x", 255)},
		{long, strings.Repeat("a", 254)},
		{strings.Repeat("я", 200), strings.Repeat("я", 127)},
	}
	for _, c := range cases {
		got := normalizeKey(c.in)
		if got != c.want {
			t.Errorf("normalizeKey(%q) = %q, want %q", c.in, got, c.want)
		}
		if len(got) > maxKeyBytes || strings.ContainsAny(got, "\r\n") {
			t.Errorf("normalizeKey(%q) = %q breaks the line rules", c.in, got)
		}
	}
}

func TestKeyWriterOneLinePerKey(t *testing.T) {
	dir := t.TempDir()
	w := newKeyWriter(dir, 0, nil)
	w.add("g1", "first\nsecond\rthird", false)
	w.add("g1", "next", false)
	if got := readKeys(t, dir, "g1", false); got != "first second third\nnext\n" {
		t.Errorf("keys file = %q", got)
	}
}

func TestKeyWriterStopsAtLimit(t *testing.T) {
	dir := t.TempDir()
	w := newKeyWriter(dir, 20, discardLog())
	for i := 0; i < 10; i++ {
		w.add("g1", "abcd", false)
	}
	if got := readKeys(t, dir, "g1", false); got != "abcd\nabcd\nabcd\nabcd\n" {
		t.Errorf("capped file = %q", got)
	}
	if w.dropped.Load() != 5 {
		t.Errorf("the first drop is logged and resets the count, the next five wait: dropped = %d", w.dropped.Load())
	}
	if w.lastWarn.Load() == 0 {
		t.Error("reaching the limit must log once")
	}
	w.add("g1", "abcd", true)
	if got := readKeys(t, dir, "g1", true); got != "abcd\n" {
		t.Errorf("the se list has its own cap, file = %q", got)
	}
}

func TestKeyWriterCountsExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "g1"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "g1", time.Now().Format("2006-01-02")+".dat")
	if err := os.WriteFile(path, []byte("0123456789012345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newKeyWriter(dir, 20, nil)
	w.add("g1", "toolong", false)
	w.add("g1", "ab", false)
	if got := readKeys(t, dir, "g1", false); got != "0123456789012345\nab\n" {
		t.Errorf("file written by an earlier process must count, got %q", got)
	}
}

func TestKeyWriterRecreatesRemovedDir(t *testing.T) {
	dir := t.TempDir()
	w := newKeyWriter(dir, 13, nil)
	w.add("g1", "abcdef", false)
	if err := os.RemoveAll(filepath.Join(dir, "g1")); err != nil {
		t.Fatal(err)
	}
	w.add("g1", "ghijk", false)
	w.add("g1", "lmnopq", false)
	w.add("g1", "r", false)
	if got := readKeys(t, dir, "g1", false); got != "ghijk\nlmnopq\n" {
		t.Errorf("a removed campaign directory starts a new count, got %q", got)
	}
}

func TestKeysMaxBytes(t *testing.T) {
	for _, c := range []struct {
		env  string
		want int64
	}{{"", defaultKeysMaxBytes}, {"1024", 1024}, {"0", 0}, {"-1", defaultKeysMaxBytes}, {"8MiB", defaultKeysMaxBytes}} {
		t.Setenv("EMTDS_KEYS_MAX_BYTES", c.env)
		if got := keysMaxBytes(discardLog()); got != c.want {
			t.Errorf("EMTDS_KEYS_MAX_BYTES=%q: %d, want %d", c.env, got, c.want)
		}
	}
}

// BenchmarkAppendKey measures one keyword write on the click path.
func BenchmarkAppendKey(b *testing.B) {
	w := newKeyWriter(b.TempDir(), 0, nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w.add("g1", "buy cheap shoes online", false)
	}
}

// BenchmarkAppendKeyAtLimit measures a click whose keyword file is already full.
func BenchmarkAppendKeyAtLimit(b *testing.B) {
	w := newKeyWriter(b.TempDir(), 1, discardLog())
	w.add("g1", "fill", false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w.add("g1", "buy cheap shoes online", false)
	}
}
