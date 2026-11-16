package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// newTestNoticeLog returns a logger that feeds a fresh ring and discards the output.
func newTestNoticeLog(max int) (*slog.Logger, *noticeRing) {
	ring := newNoticeRing(max)
	return slog.New(&noticeHandler{Handler: slog.NewJSONHandler(io.Discard, nil), ring: ring}), ring
}

// stripStamp removes the leading RFC 3339 time of a ring line.
func stripStamp(line string) string {
	_, rest, _ := strings.Cut(line, " ")
	return rest
}

// TestNoticesKeepAttributes checks that warnings with one message but different attributes
// (six lists that failed to load) stay six distinct lines, each naming its list and error.
func TestNoticesKeepAttributes(t *testing.T) {
	log, ring := newTestNoticeLog(20)
	for _, name := range []string{"google", "yandex", "bing"} {
		log.Warn("ipindex: list not loaded", "name", name, "err", "open "+name+".txt: no such file")
	}
	log.Info("ipindex: list loaded", "name", "baidu")
	got := ring.snapshot()
	if len(got) != 3 {
		t.Fatalf("lines %d, want 3 (info is not a notice): %v", len(got), got)
	}
	seen := map[string]bool{}
	for i, name := range []string{"google", "yandex", "bing"} {
		line := stripStamp(got[i])
		want := `ipindex: list not loaded name=` + name + ` err="open ` + name + `.txt: no such file"`
		if line != want {
			t.Errorf("line %d = %q, want %q", i, line, want)
		}
		seen[line] = true
	}
	if len(seen) != 3 {
		t.Errorf("duplicate lines: %v", got)
	}
}

// TestNoticesBoundAttributes checks attributes bound with With and WithGroup, and a group-valued attribute.
func TestNoticesBoundAttributes(t *testing.T) {
	log, ring := newTestNoticeLog(20)
	log.With("cid", "c1").Warn("postback failed", "err", "boom")
	log.WithGroup("pb").With("id", 7).Warn("grouped", "k", "v")
	log.Warn("nested", slog.Group("req", slog.String("path", "/x"), slog.Int("n", 2)))
	log.Warn("plain")
	got := ring.snapshot()
	want := []string{
		`postback failed cid=c1 err=boom`,
		`grouped pb.id=7 pb.k=v`,
		`nested req.path=/x req.n=2`,
		`plain`,
	}
	if len(got) != len(want) {
		t.Fatalf("lines %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if line := stripStamp(got[i]); line != want[i] {
			t.Errorf("line %d = %q, want %q", i, line, want[i])
		}
	}
}
