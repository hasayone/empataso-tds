package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// expireBotSnapshot moves the cached bot profile stat past pageStatInterval.
func expireBotSnapshot() {
	if s := botSnap.Load(); s != nil {
		old := *s
		old.checked = time.Now().Add(-pageStatInterval - time.Second)
		botSnap.Store(&old)
	}
}

// writeBotFile writes body to path with modification time at.
func writeBotFile(t *testing.T, path, body string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestBotProfileThrottledReload checks the stat interval, re-reading on change and the default on removal.
func TestBotProfileThrottledReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot_protection.json")
	base := time.Now().Add(-time.Hour)
	writeBotFile(t, path, `{"ch_ua":true}`, base)
	d := &engineDeps{botPath: path}
	if got := d.botProfile(); got != (config.Bots{CheckUA: true}) {
		t.Fatalf("first read = %+v", got)
	}
	writeBotFile(t, path, `{"ch_ipv6":true}`, base.Add(time.Minute))
	if got := d.botProfile(); got != (config.Bots{CheckUA: true}) {
		t.Fatalf("inside interval = %+v, want cached", got)
	}
	expireBotSnapshot()
	if got := d.botProfile(); got != (config.Bots{IPv6: true}) {
		t.Fatalf("after change = %+v", got)
	}
	writeBotFile(t, path, `not json`, base.Add(2*time.Minute))
	expireBotSnapshot()
	if got := d.botProfile(); got != withProxy(config.DefaultBotProfile(), false) {
		t.Fatalf("invalid file = %+v, want the default without the default proxy check", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	expireBotSnapshot()
	if got := d.botProfile(); got != withProxy(config.DefaultBotProfile(), false) {
		t.Fatalf("missing file = %+v, want the default without the default proxy check", got)
	}
}

// TestBotProfileOtherPath checks that a different bot file is read at once, not served from another path's cache.
func TestBotProfileOtherPath(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	writeBotFile(t, a, `{"ch_ua":true}`, time.Now())
	writeBotFile(t, b, `{"ch_ptr":true}`, time.Now())
	if got := (&engineDeps{botPath: a}).botProfile(); !got.CheckUA {
		t.Fatalf("a = %+v", got)
	}
	if got := (&engineDeps{botPath: b}).botProfile(); got != (config.Bots{PTR: true}) {
		t.Fatalf("b = %+v", got)
	}
}

// TestBotProfileConcurrent reads the profile from many goroutines while the snapshot expires.
func TestBotProfileConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot_protection.json")
	writeBotFile(t, path, `{"ch_ua":true}`, time.Now())
	d := &engineDeps{botPath: path}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if !d.botProfile().CheckUA {
					t.Error("profile lost")
					return
				}
				if i%100 == 0 {
					expireBotSnapshot()
				}
			}
		}()
	}
	wg.Wait()
}

// TestSameBotChecks checks that only detection toggles decide equality.
func TestSameBotChecks(t *testing.T) {
	a := config.Bots{CheckUA: true, Redirect: "http_redirect", Out: "https://x", Header: "text/html"}
	if !sameBotChecks(a, config.Bots{CheckUA: true}) {
		t.Fatal("serving fields must be ignored")
	}
	if sameBotChecks(a, config.Bots{CheckUA: true, PTR: true}) {
		t.Fatal("different toggles must differ")
	}
}
