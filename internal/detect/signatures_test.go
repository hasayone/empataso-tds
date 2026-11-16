package detect

import (
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeSigFile writes name.dat into dir and returns its path.
func writeSigFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name+".dat")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDefaultUAPatterns checks the embedded file: every pattern is in a known
// section, lowercase, unique, and none is a generic word that real visitors carry.
func TestDefaultUAPatterns(t *testing.T) {
	pats := defaultUAPatterns()
	if len(pats) < 100 {
		t.Fatalf("only %d built-in patterns", len(pats))
	}
	want := 0
	for _, line := range strings.Split(uaDefaultEmbed, "\n") {
		if !sigSkip(strings.TrimSpace(line)) {
			want++
		}
	}
	if len(pats) != want {
		t.Errorf("parsed %d patterns of %d lines: a section name is unknown", len(pats), want)
	}
	generic := map[string]bool{"bot": true, "spider": true, "crawler": true, "safari": true, "wv": true, "instagram": true, "fban": true, "telegram": true, "sogou": true}
	seen := map[string]bool{}
	for _, p := range pats {
		if p.text != strings.ToLower(p.text) || p.text != strings.TrimSpace(p.text) {
			t.Errorf("pattern %q is not trimmed lowercase", p.text)
		}
		if seen[p.text] {
			t.Errorf("duplicate pattern %q", p.text)
		}
		seen[p.text] = true
		if generic[p.text] {
			t.Errorf("generic pattern %q", p.text)
		}
	}
	for _, p := range pats {
		if p.bare != (p.text == "okhttp/" || p.text == "java/") {
			t.Errorf("pattern %q has bare=%v", p.text, p.bare)
		}
	}
}

// TestSignaturesFileExtendsBuiltin checks that signature_ua.dat adds sign_ua
// patterns and the built-in set keeps working next to them.
func TestSignaturesFileExtendsBuiltin(t *testing.T) {
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, "Foo-Bot\n# comment\n\nbar/1\n")
	s := NewSignatures(dir, nil)
	m := s.UAMatcher()
	cases := []struct{ ua, want string }{
		{"Mozilla/5.0 (X11) FOO-BOT/1", BotSignUA},
		{"something bar/1", BotSignUA},
		{"curl/7.68.0", BotHTTPClient},
		{"Mozilla/5.0 (compatible; Googlebot/2.1)", BotGoogle},
		{"Mozilla/5.0 (X11; Linux) Firefox/120.0", BotNone},
	}
	for _, c := range cases {
		if got := BotByUA(c.ua, m); got != c.want {
			t.Errorf("BotByUA(%q) = %q; want %q", c.ua, got, c.want)
		}
	}
}

// TestSignaturesFileDisablesBuiltin checks that "-pattern" turns off a built-in
// pattern without regard to case, while search engines and human tokens stay.
func TestSignaturesFileDisablesBuiltin(t *testing.T) {
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, "-curl/\n-WGET/\n-googlebot\n-bingsapphire\n-\n- \nextra-bot\n")
	s := NewSignatures(dir, nil)
	m := s.UAMatcher()
	cases := []struct{ ua, want string }{
		{"curl/7.68.0", BotNone},
		{"Wget/1.21", BotNone},
		{"python-requests/2.28", BotHTTPClient},
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", BotGoogle},
		{"Mozilla/5.0 (iPhone) Mobile/15E148 Safari/604.1 BingSapphire/1.0", BotNone},
		{"extra-bot/1", BotSignUA},
	}
	for _, c := range cases {
		if got := BotByUA(c.ua, m); got != c.want {
			t.Errorf("BotByUA(%q) = %q; want %q", c.ua, got, c.want)
		}
	}
}

// TestSignaturesFileSkipsSections checks that [section] headers of the file are not patterns.
func TestSignaturesFileSkipsSections(t *testing.T) {
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, "[crawler]\nfoo-bot\n[ scanner ]\n# note\nbar-bot\n")
	s := NewSignatures(dir, nil)
	if BotByUA("bar-bot", s.UAMatcher()) != BotSignUA {
		t.Error("lines after a header must load")
	}
	for _, ua := range []string{"x [crawler] y", "x [ scanner ] y"} {
		if got := BotByUA(ua, s.UAMatcher()); got != BotNone {
			t.Errorf("header leaked into patterns: %q = %q", ua, got)
		}
	}
	if got := BotByUA("foo-bot", s.UAMatcher()); got != BotSignUA {
		t.Errorf("file lines are sign_ua, got %q", got)
	}
}

// TestSignaturesFileLineLimit checks that lines over the limit are dropped.
func TestSignaturesFileLineLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxUALines+5; i++ {
		fmt.Fprintf(&b, "zsig%05d\n", i)
	}
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, b.String())
	s := NewSignatures(dir, nil)
	m := s.UAMatcher()
	if got := BotByUA(fmt.Sprintf("x zsig%05d", maxUALines-1), m); got != BotSignUA {
		t.Errorf("last kept line = %q", got)
	}
	if got := BotByUA(fmt.Sprintf("x zsig%05d", maxUALines), m); got != BotNone {
		t.Errorf("line over the limit = %q", got)
	}
}

// TestSignaturesBigFileBuild checks build time and heap of the matcher for a full-size file.
func TestSignaturesBigFileBuild(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var b strings.Builder
	for i := 0; i < maxUALines; i++ {
		fmt.Fprintf(&b, "%05d-%06x\n", i, rng.Uint32()&0xffffff)
	}
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, b.String())

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	s := NewSignatures(dir, nil)
	took := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)

	if took > 10*time.Second {
		t.Errorf("building %d patterns took %v", maxUALines, took)
	}
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 64<<20 {
		t.Errorf("matcher keeps %d MiB of heap", grown>>20)
	}
	if got := BotByUA("x "+strings.Split(b.String(), "\n")[42], s.UAMatcher()); got != BotSignUA {
		t.Errorf("pattern of the big file not found: %q", got)
	}
	runtime.KeepAlive(s)
}

// TestSignaturesReloadSwapsMatcher checks that a changed file rebuilds the
// matcher as a new value, the old one stays valid, and a removed file keeps the list.
func TestSignaturesReloadSwapsMatcher(t *testing.T) {
	dir := t.TempDir()
	path := writeSigFile(t, dir, fileUA, "first-bot\n")
	s := NewSignatures(dir, nil)
	old := s.UAMatcher()
	if BotByUA("first-bot", old) != BotSignUA {
		t.Fatal("first pattern missing")
	}

	writeSigFile(t, dir, fileUA, "second-bot-longer\n")
	s.Reload()
	cur := s.UAMatcher()
	if cur == old {
		t.Fatal("matcher must be rebuilt")
	}
	if BotByUA("second-bot-longer", cur) != BotSignUA || BotByUA("first-bot", cur) != BotNone {
		t.Error("reloaded matcher has the wrong patterns")
	}
	if BotByUA("first-bot", old) != BotSignUA {
		t.Error("the replaced matcher must stay valid for running readers")
	}

	s.Reload()
	if s.UAMatcher() != cur {
		t.Error("an unchanged file must not rebuild the matcher")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if s.UAMatcher() == cur || s.UAMatcher() != DefaultUAMatcher() {
		t.Error("a removed signature_ua.dat must return to the built-in set")
	}
	if BotByUA("second-bot-longer", s.UAMatcher()) != BotNone {
		t.Error("file patterns must be dropped with the file")
	}
	if _, known := s.stamps[fileUA]; known {
		t.Error("the stamp of a removed file must be forgotten")
	}

	writeSigFile(t, dir, fileUA, "second-bot-longer\n")
	s.Reload()
	if BotByUA("second-bot-longer", s.UAMatcher()) != BotSignUA {
		t.Error("a file that comes back must load again")
	}
}

// TestSignaturesRemovedUAFileRestoresDisabled checks that "-pattern" lines stop
// working when the file is removed.
func TestSignaturesRemovedUAFileRestoresDisabled(t *testing.T) {
	dir := t.TempDir()
	path := writeSigFile(t, dir, fileUA, "-curl/\n")
	s := NewSignatures(dir, nil)
	if BotByUA("curl/7", s.UAMatcher()) != BotNone {
		t.Fatal("curl/ must be disabled")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if BotByUA("curl/7", s.UAMatcher()) != BotHTTPClient {
		t.Error("removing the file must restore the built-in curl/")
	}
}

// TestSignaturesOtherListsKeepOnRemoval checks that the referer and blacklist
// lists keep their previous content when their file disappears.
func TestSignaturesOtherListsKeepOnRemoval(t *testing.T) {
	dir := t.TempDir()
	ref := writeSigFile(t, dir, fileRef, "spam.example\n")
	black := writeSigFile(t, dir, fileUABlack, "Bad/1\n")
	s := NewSignatures(dir, nil)
	for _, p := range []string{ref, black} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	s.Reload()
	if len(s.Ref()) != 1 || len(s.UABlacklist()) != 1 {
		t.Errorf("lists lost on removal: ref=%q black=%q", s.Ref(), s.UABlacklist())
	}
}

// TestSignaturesProxyOrgRemovedRestoresBuiltin checks that removing proxy_org.dat
// returns the built-in list, as LoadProxyOrg gives for a missing file.
func TestSignaturesProxyOrgRemovedRestoresBuiltin(t *testing.T) {
	dir := t.TempDir()
	path := writeSigFile(t, dir, fileProxyOrg, "customhost\n")
	s := NewSignatures(dir, nil)
	if got := s.ProxyOrg(); len(got) != 1 || got[0] != "customhost" {
		t.Fatalf("file list = %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if got, want := s.ProxyOrg(), LoadProxyOrg(dir); len(got) != len(want) || len(got) != len(DefaultProxyOrg()) {
		t.Errorf("after removal Signatures has %d patterns, LoadProxyOrg %d, built-in %d", len(got), len(want), len(DefaultProxyOrg()))
	}
	writeSigFile(t, dir, fileProxyOrg, "otherhost\n")
	s.Reload()
	if got := s.ProxyOrg(); len(got) != 1 || got[0] != "otherhost" {
		t.Errorf("a file that comes back must load again: %q", got)
	}
}

// TestSignaturesTotalSizeLimit checks that pattern lines over 256 KiB in total
// are dropped with one warning.
func TestSignaturesTotalSizeLimit(t *testing.T) {
	const lineLen = 200
	count := maxUABytes/lineLen + 500
	var b strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&b, "%05d%s\n", i, strings.Repeat("x", lineLen-5))
	}
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, b.String())
	var logBuf strings.Builder
	s := NewSignatures(dir, slog.New(slog.NewTextHandler(&logBuf, nil)))
	m := s.UAMatcher()
	kept := maxUABytes / lineLen
	line := func(i int) string { return fmt.Sprintf("%05d%s", i, strings.Repeat("x", lineLen-5)) }
	if BotByUA("a "+line(0), m) != BotSignUA || BotByUA("a "+line(kept-1), m) != BotSignUA {
		t.Error("lines within the budget must load")
	}
	if BotByUA("a "+line(kept), m) != BotNone || BotByUA("a "+line(count-1), m) != BotNone {
		t.Error("lines over the budget must be dropped")
	}
	if got := strings.Count(logBuf.String(), "total size limit"); got != 1 {
		t.Errorf("want one size warning, got %d: %s", got, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), fmt.Sprintf("count=%d", count-kept)) {
		t.Errorf("warning must carry the dropped count %d: %s", count-kept, logBuf.String())
	}
}

// TestSignaturesLongLinesDropped checks that lines over 256 bytes are dropped
// and reported in a single warning with their count.
func TestSignaturesLongLinesDropped(t *testing.T) {
	long := strings.Repeat("a", maxUALineBytes+1)
	exact := strings.Repeat("b", maxUALineBytes)
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, long+"\n"+exact+"\nkept-bot\n"+long+"c\n-"+long+"\n")
	var logBuf strings.Builder
	s := NewSignatures(dir, slog.New(slog.NewTextHandler(&logBuf, nil)))
	m := s.UAMatcher()
	if BotByUA("x "+long+" y", m) != BotNone {
		t.Error("a long line must not become a pattern")
	}
	if BotByUA("x "+exact, m) != BotSignUA || BotByUA("kept-bot", m) != BotSignUA {
		t.Error("lines within the limit must load")
	}
	if got := strings.Count(logBuf.String(), "over the length limit"); got != 1 {
		t.Errorf("want one warning, got %d: %s", got, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "count=3") {
		t.Errorf("warning must carry the count 3: %s", logBuf.String())
	}
}

// TestSignaturesNilReceivers checks that a nil *Signatures is safe for every reader.
func TestSignaturesNilReceivers(t *testing.T) {
	var s *Signatures
	if s.Ref() != nil || s.UABlacklist() != nil {
		t.Error("nil receiver must give nil Ref and UABlacklist")
	}
	if got := s.ProxyOrg(); len(got) != len(DefaultProxyOrg()) || len(got) == 0 {
		t.Errorf("nil ProxyOrg() = %d patterns", len(got))
	}
}

// TestSignaturesMatcherNeverNil checks the fallbacks of UAMatcher.
func TestSignaturesMatcherNeverNil(t *testing.T) {
	var nilSigs *Signatures
	if nilSigs.UAMatcher() != DefaultUAMatcher() {
		t.Error("nil receiver must give the default matcher")
	}
	s := NewSignatures(t.TempDir(), nil)
	if s.UAMatcher() == nil || BotByUA("curl/7", s.UAMatcher()) != BotHTTPClient {
		t.Error("without a file the built-in set must work")
	}
	if BotByUA("curl/7", nil) != BotHTTPClient || BotByUA("", nil) != BotNone {
		t.Error("nil matcher must mean the default one")
	}
}

// TestSignaturesConcurrentReload runs readers while the file is rewritten and reloaded.
func TestSignaturesConcurrentReload(t *testing.T) {
	dir := t.TempDir()
	writeSigFile(t, dir, fileUA, "stable-bot\n")
	s := NewSignatures(dir, nil)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for g := 0; g < 6; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m := s.UAMatcher()
				if BotByUA("Mozilla/5.0 (compatible; Googlebot/2.1)", m) != BotGoogle {
					t.Error("search engine lost during reload")
					return
				}
				if BotByUA("stable-bot", m) != BotSignUA {
					t.Error("kept pattern lost during reload")
					return
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		writeSigFile(t, dir, fileUA, "stable-bot\n"+strings.Repeat("x", i)+"-extra\n")
		s.Reload()
	}
	close(stop)
	readers.Wait()
}
