package cron

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeitaroManifestPath(t *testing.T) {
	if keitaroManifestPath("") != "" {
		t.Fatal("empty groups must yield empty path")
	}
	got := keitaroManifestPath(`/data/groups.json`)
	want := filepath.Join(`/data`, "keitaro-import.json")
	if got != want {
		t.Fatalf("path = %q want %q", got, want)
	}
}

func TestKeitaroExcludeHost(t *testing.T) {
	dir := t.TempDir()
	groups := filepath.Join(dir, "groups.json")
	if err := os.WriteFile(groups, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Paths{Config: filepath.Join(dir, "cron.json"), Groups: groups}, nil, nil)
	if r.keitaroExcludeHost() != "" {
		t.Fatal("missing manifest must be silent")
	}
	manifest := filepath.Join(dir, "keitaro-import.json")
	if err := os.WriteFile(manifest, []byte(`{"host":" tracker.example "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.keitaroExcludeHost(); got != "tracker.example" {
		t.Fatalf("host = %q", got)
	}
}
