package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/cron"
)

func TestRunProbeMissingFile(t *testing.T) {
	var out, errOut strings.Builder
	code := runProbe(filepath.Join(t.TempDir(), "nope.json"), time.Now(), &out, &errOut)
	if code != 1 {
		t.Errorf("missing file: got exit %d, want 1", code)
	}
	if errOut.String() == "" {
		t.Error("missing file: want error on stderr")
	}
	if out.String() != "" {
		t.Errorf("missing file: want empty stdout, got %q", out.String())
	}
}

func TestRunProbeValidFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cron.json")
	statusPath := cron.StatusPath(configPath)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	st := cron.Status{
		Heartbeat: now.Add(-10 * time.Second),
		Jobs:      map[string]cron.JobStatus{},
		Version:   "v1.0.0",
		Commit:    "abc1234",
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(statusPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	code := runProbe(statusPath, now, &out, &errOut)
	if code != 0 {
		t.Errorf("valid file: got exit %d, want 0; stderr: %s", code, errOut.String())
	}
	output := out.String()
	if !strings.Contains(output, "heartbeat_age=10") {
		t.Errorf("want heartbeat_age=10 in output: %q", output)
	}
	if !strings.Contains(output, "busy=0") {
		t.Errorf("want busy=0 in output: %q", output)
	}
	if !strings.Contains(output, "version=v1.0.0") {
		t.Errorf("want version=v1.0.0 in output: %q", output)
	}
	if !strings.Contains(output, "commit=abc1234") {
		t.Errorf("want commit=abc1234 in output: %q", output)
	}
}
