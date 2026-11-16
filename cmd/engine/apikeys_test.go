package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
)

// mustAPIKey generates one key into set.
//
// name and scopes are passed to KeySet.Generate. The raw secret is returned
// and is not written into the test log. A generate error fails the test.
func mustAPIKey(t *testing.T, set *security.KeySet, name string, scopes []string) (string, security.APIKey) {
	t.Helper()
	raw, key, err := set.Generate(name, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return raw, key
}

// stampOf reads the current fingerprint of path.
//
// A stat error fails the test. The returned stamp is marked seen.
func stampOf(t *testing.T, path string) apiKeyStamp {
	t.Helper()
	st, err := statFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return apiKeyStamp{seen: true, stamp: *st}
}

func TestAPIKeysEnvAndEmptyRaw(t *testing.T) {
	env, err := security.RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	keys := apiKeys{env: env}
	if !keys.verify(env) {
		t.Fatal("env key must be accepted")
	}
	if keys.verify("") {
		t.Fatal("empty raw must be rejected when env is set")
	}
	blank := apiKeys{}
	if blank.verify("") {
		t.Fatal("empty raw must be rejected when env is empty")
	}
	if blank.verify(env) {
		t.Fatal("empty env must not accept a raw key")
	}
}

func TestAPIKeysFileScopes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_keys.json")
	stored := security.NewKeySet()
	rawEngine, _ := mustAPIKey(t, stored, "engine", []string{security.ScopeEngine})
	rawAdmin, _ := mustAPIKey(t, stored, "admin", []string{security.ScopeAdmin})
	rawRevoked, revoked := mustAPIKey(t, stored, "revoked", []string{security.ScopeEngine})
	if !stored.Revoke(revoked.ID) {
		t.Fatal("revoke")
	}
	if err := stored.Save(path); err != nil {
		t.Fatal(err)
	}
	env, err := security.RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	keys := apiKeys{env: env, set: loadAPIKeys(path, discardLog())}
	if keys.set == nil {
		t.Fatal("load")
	}
	if !keys.verify(env) {
		t.Fatal("env key must still be accepted beside the file")
	}
	if !keys.verify(rawEngine) {
		t.Fatal("engine scope must be accepted")
	}
	if keys.verify(rawAdmin) {
		t.Fatal("admin-only key must be rejected")
	}
	if keys.verify(rawRevoked) {
		t.Fatal("revoked key must be rejected")
	}
	if keys.verify("") {
		t.Fatal("empty raw must be rejected")
	}
}

func TestAPIKeysWatcherTick(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_keys.json")
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, nil))

	first := security.NewKeySet()
	rawOld, _ := mustAPIKey(t, first, "old", []string{security.ScopeEngine})
	if err := first.Save(path); err != nil {
		t.Fatal(err)
	}
	live := loadAPIKeys(path, log)
	if live == nil || !(apiKeys{set: live}).verify(rawOld) {
		t.Fatal("startup load")
	}
	prev := stampOf(t, path)
	if next := reloadAPIKeys(path, live, prev, log); next != prev {
		t.Fatal("unchanged file must not reload")
	}

	second := security.NewKeySet()
	rawNew, _ := mustAPIKey(t, second, "replacement-key", []string{security.ScopeEngine})
	if err := second.Save(path); err != nil {
		t.Fatal(err)
	}
	bumped := prev.stamp.mod.Add(2 * time.Second)
	if err := os.Chtimes(path, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() == prev.stamp.size && after.ModTime().Equal(prev.stamp.mod) {
		t.Fatal("rewrite did not change the file stamp")
	}
	prev = reloadAPIKeys(path, live, prev, log)
	if !prev.seen {
		t.Fatal("reload stamp")
	}
	if !(apiKeys{set: live}).verify(rawNew) {
		t.Fatal("new key must be accepted after one tick")
	}
	if (apiKeys{set: live}).verify(rawOld) {
		t.Fatal("replaced key must be rejected after reload")
	}

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, bumped.Add(2*time.Second), bumped.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	kept := reloadAPIKeys(path, live, prev, log)
	if kept != prev {
		t.Fatal("broken json must keep the previous stamp")
	}
	if !(apiKeys{set: live}).verify(rawNew) {
		t.Fatal("broken json must not clear the set")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cleared := reloadAPIKeys(path, live, kept, log)
	if cleared.seen {
		t.Fatal("missing file must clear the stamp")
	}
	if (apiKeys{set: live}).verify(rawNew) {
		t.Fatal("missing file must empty the set")
	}
	if strings.Contains(logBuf.String(), rawOld) || strings.Contains(logBuf.String(), rawNew) {
		t.Fatal("raw key was logged")
	}
}

func TestLoadAPIKeysEmptyAndBroken(t *testing.T) {
	if loadAPIKeys("", discardLog()) != nil {
		t.Fatal("empty path must return nil")
	}
	path := filepath.Join(t.TempDir(), "api_keys.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, nil))
	got := loadAPIKeys(path, log)
	if got == nil {
		t.Fatal("broken file must return an empty set")
	}
	if _, ok := got.Verify("x", security.ScopeEngine); ok {
		t.Fatal("empty set must reject")
	}
	if !strings.Contains(logBuf.String(), "api keys file not loaded") {
		t.Fatal("broken file must be logged")
	}
	if loadAPIKeys(path, nil) == nil {
		t.Fatal("nil logger must still return an empty set")
	}
}

func TestKeysWatcherStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "api_keys.json")
	done := make(chan struct{})
	go func() {
		keysWatcher(ctx, path, security.NewKeySet(), time.Hour, discardLog())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("keysWatcher did not return on a cancelled context")
	}
	keysWatcher(context.Background(), "", security.NewKeySet(), time.Hour, discardLog())
}
