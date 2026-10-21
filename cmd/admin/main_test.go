package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestDomainWatchInterval(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := map[string]time.Duration{
		"":        5 * time.Minute,
		"  ":      5 * time.Minute,
		"2m":      2 * time.Minute,
		"90s":     90 * time.Second,
		"10s":     time.Minute,
		"-1m":     time.Minute,
		"banana":  5 * time.Minute,
		"1h30m":   90 * time.Minute,
		"1m":      time.Minute,
		"5minute": 5 * time.Minute,
	}
	for raw, want := range cases {
		if got := domainWatchInterval(raw, log); got != want {
			t.Errorf("%q: got %v, want %v", raw, got, want)
		}
	}
}

func TestKeitaroSyncFileDefaultPath(t *testing.T) {
	dir := t.TempDir()
	groups := filepath.Join(dir, "groups.json")
	want := filepath.Join(dir, "keitaro-sync.json")
	if got := keitaroSyncFileForGroups(groups); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	t.Setenv("KUZTDS_KEITARO_SYNC_FILE", filepath.Join(dir, "custom-sync.json"))
	if got := keitaroSyncFileForGroups(groups); got != filepath.Join(dir, "custom-sync.json") {
		t.Fatalf("env override: %q", got)
	}
}
