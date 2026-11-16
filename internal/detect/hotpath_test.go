package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestContainsFoldedMatchesToLower checks the copy-free path against the plain lowercase comparison.
func TestContainsFoldedMatchesToLower(t *testing.T) {
	texts := []string{"mozilla/5.0 ahrefsbot/7.0", "яндекс бот", "", "spam-site.example"}
	pats := []string{"ahrefsbot", "AhrefsBot", "ЯНДЕКС", "яндекс", "Spam-Site", "x", "BOT/7"}
	for _, l := range texts {
		for _, p := range pats {
			want := strings.Contains(l, strings.ToLower(p))
			if got := containsFolded(l, p); got != want {
				t.Errorf("containsFolded(%q, %q) = %v; want %v", l, p, got, want)
			}
		}
	}
}

// TestProxyByOrgFoldsASCII checks the copy-free search against strings.Contains on lowercased text.
func TestProxyByOrgFoldsASCII(t *testing.T) {
	orgs := []string{"Hetzner Online GmbH", "HETZNER", "ovh sas", "Some Residential ISP", "a"}
	pats := []string{"hetzner", "HETZNER", "Ovh", "sidential", "a", "zz", "hetzner online gmbh x", "isp"}
	for _, org := range orgs {
		for _, p := range pats {
			want := strings.Contains(strings.ToLower(org), strings.ToLower(p))
			if got := ProxyByOrg(org, []string{p}); got != want {
				t.Errorf("ProxyByOrg(%q, %q) = %v; want %v", org, p, got, want)
			}
		}
	}
	if !ProxyByOrg("ооо хостинг", []string{"хостинг"}) || !ProxyByOrg("Résidential Hosting", []string{"hosting", "résidential"}) {
		t.Error("non-ASCII bytes must match as they are")
	}
	for _, org := range []string{benchOrgMiss, "Hetzner Online GmbH"} {
		if n := testing.AllocsPerRun(100, func() { ProxyByOrg(org, DefaultProxyOrg()) }); n != 0 {
			t.Errorf("ProxyByOrg(%q) allocates %v times", org, n)
		}
	}
}

// TestBotByUAAnyCaseList checks that callers passing mixed-case lists keep case-insensitive matching.
func TestBotByUAAnyCaseList(t *testing.T) {
	if BotByUA("Mozilla AHREFSBOT", buildUAMatcher([]string{"AhrefsBot"}, nil)) != BotSignUA {
		t.Fatal("mixed-case UA signature must match")
	}
	if BotByReferer("https://SPAM.example/", []string{"Spam.Example"}) != BotSignRef {
		t.Fatal("mixed-case referer signature must match")
	}
	if !ProxyByOrg("hetzner online", []string{"HETZNER"}) {
		t.Fatal("mixed-case org pattern must match")
	}
}

// TestSignaturesLowercasedAtLoad checks that substring lists are lowercased and the blacklist is not.
func TestSignaturesLowercasedAtLoad(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"signature_ua.dat":  "AhrefsBot\n",
		"signature_ref.dat": "Spam.Example\n",
		"proxy_org.dat":     "HetznerX\n",
		"ua_blacklist.dat":  "Exact UA/1.0\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := NewSignatures(dir, nil)
	if got := BotByUA("x AHREFSBOT/7", s.UAMatcher()); got != BotSignUA {
		t.Errorf("UA signature = %q; want sign_ua", got)
	}
	if got := s.Ref(); len(got) != 1 || got[0] != "spam.example" {
		t.Errorf("Ref() = %q", got)
	}
	if got := s.ProxyOrg(); len(got) != 1 || got[0] != "hetznerx" {
		t.Errorf("ProxyOrg() = %q", got)
	}
	if got := s.UABlacklist(); len(got) != 1 || got[0] != "Exact UA/1.0" {
		t.Errorf("UABlacklist() = %q", got)
	}
	if !InUABlacklist("Exact UA/1.0", s.UABlacklist()) || InUABlacklist("exact ua/1.0", s.UABlacklist()) {
		t.Error("blacklist must stay an exact match")
	}
}

// TestParseCacheSameResult checks that a cached parse equals the uncached one.
func TestParseCacheSameResult(t *testing.T) {
	for _, u := range benchUAs {
		want := parseUA(u)
		if got := Parse(u); got != want {
			t.Errorf("first Parse(%q) = %+v; want %+v", u, got, want)
		}
		if got := Parse(u); got != want {
			t.Errorf("cached Parse(%q) = %+v; want %+v", u, got, want)
		}
	}
}

// TestParseCacheBounded checks the two-generation bound and that long keys are not stored.
func TestParseCacheBounded(t *testing.T) {
	c := &parseCache{}
	for i := 0; i < parseCacheGen*5; i++ {
		c.put(fmt.Sprintf("ua-%d", i), Info{OS: "x"})
	}
	if n := len(c.cur) + len(c.prev); n > 2*parseCacheGen {
		t.Fatalf("cache holds %d entries; bound is %d", n, 2*parseCacheGen)
	}
	if _, ok := c.get(fmt.Sprintf("ua-%d", parseCacheGen*5-1)); !ok {
		t.Fatal("most recent key must be cached")
	}
	if _, ok := c.get("ua-0"); ok {
		t.Fatal("oldest key must be evicted")
	}
	long := strings.Repeat("a", parseCacheMaxKey+1)
	c.put(long, Info{})
	if _, ok := c.get(long); ok {
		t.Fatal("over-long key must not be cached")
	}
}

// TestParseCachePromotesPrev checks that a hit in the old generation survives the next rotation.
func TestParseCachePromotesPrev(t *testing.T) {
	c := &parseCache{}
	c.put("keep", Info{OS: "k"})
	for i := 0; i < parseCacheGen; i++ {
		c.put(fmt.Sprintf("fill-%d", i), Info{})
	}
	if _, ok := c.get("keep"); !ok {
		t.Fatal("keep must be in prev")
	}
	for i := 0; i < parseCacheGen; i++ {
		c.put(fmt.Sprintf("more-%d", i), Info{})
	}
	if info, ok := c.get("keep"); !ok || info.OS != "k" {
		t.Fatal("promoted key must survive rotation")
	}
}

// TestParseCacheConcurrent runs Parse from many goroutines for the race detector.
func TestParseCacheConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				u := benchUAs[i%len(benchUAs)]
				if i%3 == 0 {
					u = fmt.Sprintf("%s g%d-%d", u, g, i)
				}
				if got, want := Parse(u), parseUA(u); got != want {
					t.Errorf("Parse(%q) = %+v; want %+v", u, got, want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
