package cron

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeStatusMissingFile(t *testing.T) {
	_, err := ProbeStatus(filepath.Join(t.TempDir(), "nope.json"), time.Now())
	if !os.IsNotExist(err) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestProbeStatusHeartbeat(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cron.json")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	st := Status{Heartbeat: now.Add(-90 * time.Second), Jobs: map[string]JobStatus{}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(StatusPath(configPath), b, 0o644); err != nil {
		t.Fatal(err)
	}

	probe, err := ProbeStatus(StatusPath(configPath), now)
	if err != nil {
		t.Fatal(err)
	}
	if probe.HeartbeatAge != 90 {
		t.Errorf("HeartbeatAge = %d, want 90", probe.HeartbeatAge)
	}
	if probe.Busy {
		t.Error("Busy must be false when no jobs are running")
	}
}

func TestProbeStatusZeroHeartbeat(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cron.json")

	st := Status{Jobs: map[string]JobStatus{}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(StatusPath(configPath), b, 0o644); err != nil {
		t.Fatal(err)
	}

	probe, err := ProbeStatus(StatusPath(configPath), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if probe.HeartbeatAge != -1 {
		t.Errorf("HeartbeatAge = %d; want -1 for zero heartbeat", probe.HeartbeatAge)
	}
}

func TestProbeStatusBusy(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cron.json")
	now := time.Now()

	for _, job := range []string{JobGeoDB, JobVirusTotal} {
		st := Status{
			Heartbeat: now,
			Jobs:      map[string]JobStatus{job: {Running: true}},
		}
		b, _ := json.Marshal(st)
		if err := os.WriteFile(StatusPath(configPath), b, 0o644); err != nil {
			t.Fatal(err)
		}

		probe, err := ProbeStatus(StatusPath(configPath), now)
		if err != nil {
			t.Fatal(err)
		}
		if !probe.Busy {
			t.Errorf("job %s running: Busy must be true", job)
		}
	}
}

func TestProbeStatusVersionCommit(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cron.json")
	now := time.Now()

	st := Status{
		Heartbeat: now,
		Jobs:      map[string]JobStatus{},
		Version:   "v1.2.3",
		Commit:    "abc123def456",
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(StatusPath(configPath), b, 0o644); err != nil {
		t.Fatal(err)
	}

	probe, err := ProbeStatus(StatusPath(configPath), now)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", probe.Version)
	}
	if probe.Commit != "abc123def456" {
		t.Errorf("Commit = %q, want abc123def456", probe.Commit)
	}
}

func TestProbeFormat(t *testing.T) {
	p := Probe{HeartbeatAge: 42, Busy: true, Version: "v1.0.0", Commit: "abc1234"}
	got := p.Format()
	want := "heartbeat_age=42\nbusy=1\nversion=v1.0.0\ncommit=abc1234\n"
	if got != want {
		t.Errorf("Format() = %q; want %q", got, want)
	}

	p2 := Probe{HeartbeatAge: -1, Busy: false}
	got2 := p2.Format()
	if got2 != "heartbeat_age=-1\nbusy=0\nversion=\ncommit=\n" {
		t.Errorf("empty fields: %q", got2)
	}
}

func TestSafeProbeField(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"v1.0.0", "v1.0.0"},
		{"abc123+test-1.0", "abc123+test-1.0"},
		{"<script>alert(1)</script>", ""},
		{strings.Repeat("a", 65), ""},
		{"", ""},
		{"abc def", ""},
		{"a/b", ""},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
	}
	for _, c := range cases {
		if got := safeProbeField(c.in); got != c.want {
			t.Errorf("safeProbeField(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
