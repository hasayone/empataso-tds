package main

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/ipindex"
)

// save_ip dedupes against the in-memory index, which only catches up on the
// next hot-reload (a minute by default). Without the process-wide memory every
// hit from the same confirmed crawler IP appended another line.
func TestSaveBotIPWritesEachIPOnce(t *testing.T) {
	dir := t.TempDir()
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, log)
	bots := config.Bots{CheckUA: true, SaveIP: true}
	ip1, ip2 := netip.MustParseAddr("66.249.66.1"), netip.MustParseAddr("66.249.66.2")
	c := stubPTR(t, ptrNamed(googleHost+"."), func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{ip1, ip2}, nil
	})

	savedBotIPs.Clear()
	for _, ip := range []netip.Addr{ip1, ip2} {
		detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), bots)
		waitPTR(t, c, ip)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "ip_google.dat")); err != nil || strings.TrimSpace(string(b)) != "" {
		t.Fatalf("an address must not be saved before the DNS check finished: %q, %v", b, err)
	}
	for i := 0; i < 50; i++ {
		detectBot(lists, sigs, dir, botVisit(ip1, uaGoogleBot), bots)
	}
	detectBot(lists, sigs, dir, botVisit(ip2, uaGoogleBot), bots)

	b, err := os.ReadFile(filepath.Join(dir, "ip_google.dat"))
	if err != nil {
		t.Fatalf("ip_google.dat not written: %v", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(b)))
	if len(lines) != 2 {
		t.Errorf("50 hits from 2 IPs → 2 lines, got %d: %q", len(lines), string(b))
	}
	if !strings.Contains(string(b), "66.249.66.1") || !strings.Contains(string(b), "66.249.66.2") {
		t.Errorf("both crawler IPs must be recorded, got %q", string(b))
	}
}

// Pending and fake crawlers are never written; only a DNS-confirmed address is.
func TestSaveBotIPSkipsFakeAndPending(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.9")
	c := stubPTR(t, ptrNamed("host.example.net."), fwdTo())
	lists, sigs, dir := ptrBotEnv(t)
	bots := config.Bots{CheckUA: true, SaveIP: true}

	savedBotIPs.Clear()
	if got := detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), bots); got != "google" {
		t.Fatalf("pending click = %q, want google", got)
	}
	waitPTR(t, c, ip)
	if got := detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), bots); got != "fake_google" {
		t.Fatalf("checked click = %q, want fake_google", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "ip_google.dat")); err != nil || strings.TrimSpace(string(b)) != "" {
		t.Fatalf("a pending or fake crawler must not be saved: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ip_fake_google.dat")); err == nil {
		t.Fatal("a fake label must not get a list file")
	}
}

// An IP already present in the list is never appended again and needs no DNS.
func TestSaveBotIPSkipsKnownIP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ip_google.dat"), []byte("66.249.66.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, log)
	var asked atomic.Int64
	stubPTR(t, func(context.Context, string) ([]string, error) {
		asked.Add(1)
		return nil, nil
	}, nil)

	savedBotIPs.Clear()
	for i := 0; i < 10; i++ {
		detectBot(lists, sigs, dir, botVisit(netip.MustParseAddr("66.249.66.1"), uaGoogleBot),
			config.Bots{CheckUA: true, SaveIP: true})
	}
	b, _ := os.ReadFile(filepath.Join(dir, "ip_google.dat"))
	if got := strings.TrimSpace(string(b)); got != "66.249.66.1" {
		t.Errorf("a known IP must not be appended again, file = %q", got)
	}
	if asked.Load() != 0 {
		t.Errorf("reverse DNS was asked %d times for a listed address", asked.Load())
	}
}
