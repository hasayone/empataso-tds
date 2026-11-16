package main

import (
	"bytes"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/admin"
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

func TestViewsFileDefaultPath(t *testing.T) {
	dir := t.TempDir()
	groups := filepath.Join(dir, "groups.json")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var cfg admin.Config
	applyDataFiles(&cfg, groups, log)
	want := filepath.Join(dir, "views.json")
	if cfg.ViewsPath != want {
		t.Fatalf("default views path: got %q want %q", cfg.ViewsPath, want)
	}
	t.Setenv("EMTDS_VIEWS_FILE", filepath.Join(dir, "custom-views.json"))
	applyDataFiles(&cfg, groups, log)
	if cfg.ViewsPath != filepath.Join(dir, "custom-views.json") {
		t.Fatalf("env override: %q", cfg.ViewsPath)
	}
}

func TestKeitaroSyncFileDefaultPath(t *testing.T) {
	dir := t.TempDir()
	groups := filepath.Join(dir, "groups.json")
	want := filepath.Join(dir, "keitaro-sync.json")
	if got := keitaroSyncFileForGroups(groups); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	t.Setenv("EMTDS_KEITARO_SYNC_FILE", filepath.Join(dir, "custom-sync.json"))
	if got := keitaroSyncFileForGroups(groups); got != filepath.Join(dir, "custom-sync.json") {
		t.Fatalf("env override: %q", got)
	}
}

func TestRunRecoveryRejectsArguments(t *testing.T) {
	var buf bytes.Buffer
	if runRecovery(&buf, []string{"not-a-password"}) != 1 {
		t.Fatal("expected rejection")
	}
	if buf.Len() != 0 {
		t.Fatal("a rejected recovery must not print a code")
	}
}

func TestRunRecoveryNeedsPasswordFile(t *testing.T) {
	t.Setenv("EMTDS_ADMIN_PASSWORD_FILE", "")
	t.Setenv("EMTDS_RECOVERY_LOGIN", "admin")
	var buf bytes.Buffer
	if runRecovery(&buf, nil) != 1 {
		t.Fatal("expected failure")
	}
	if buf.Len() != 0 {
		t.Fatal("failure must not print a code")
	}
}

func TestConfigSecretValues(t *testing.T) {
	for _, name := range secretEnvNames {
		t.Setenv(name, "")
	}
	if got := secretValues(); len(got) != 0 {
		t.Fatalf("nothing is set: %v", got)
	}
	t.Setenv("EMTDS_REDIS_PASSWORD", "redis-secret-1")
	t.Setenv("KEITARO_API_KEY", "keitaro-secret-1")
	got := secretValues()
	if len(got) != 2 || got[0] != "redis-secret-1" || got[1] != "keitaro-secret-1" {
		t.Fatalf("got %v", got)
	}
}

func TestConfigCIDKeyFile(t *testing.T) {
	groups := filepath.Join("data", "groups.json")
	cases := []struct{ override, groups, want string }{
		{"", "", ""},
		{"", groups, filepath.Join("data", "cid.key")},
		{" /vol/own.key ", groups, "/vol/own.key"},
		{"/vol/own.key", "", "/vol/own.key"},
	}
	for _, c := range cases {
		if got := cidKeyFile(c.override, c.groups); got != c.want {
			t.Errorf("cidKeyFile(%q, %q) = %q, want %q", c.override, c.groups, got, c.want)
		}
	}
}
