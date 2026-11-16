package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/detect"
	"github.com/egerkuzma/emtds/internal/ipindex"
)

// TestDetectBotDNSErrorIsNotFake checks that a timeout or a temporary DNS error keeps the label of the user
// agent, and that the failed answer is reused instead of starting a new check.
func TestDetectBotDNSErrorIsNotFake(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	servfail := func(context.Context, string) ([]string, error) { return nil, errors.New("servfail") }
	cases := []struct {
		name    string
		resolve func(context.Context, string) ([]string, error)
		forward func(context.Context, string) ([]netip.Addr, error)
	}{
		{"PTR timeout", func(ctx context.Context, _ string) ([]string, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil},
		{"PTR SERVFAIL", servfail, nil},
		{"forward SERVFAIL", ptrNamed(googleHost + "."), func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("servfail")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			c := stubPTR(t, func(ctx context.Context, addr string) ([]string, error) {
				calls.Add(1)
				return tc.resolve(ctx, addr)
			}, tc.forward)
			c.timeout = 30 * time.Millisecond
			lists, sigs, dir := ptrBotEnv(t)
			cfg := config.Bots{CheckUA: true}
			if got := detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), cfg); got != "google" {
				t.Fatalf("first click = %q, want google", got)
			}
			if ent := waitPTR(t, c, ip); !ent.failed {
				t.Fatalf("entry = %+v, want failed", ent)
			}
			for i := 0; i < 5; i++ {
				if got := detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), cfg); got != "google" {
					t.Fatalf("after a DNS error = %q, want google", got)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("reverse lookups = %d, want 1 while the failed answer lives", calls.Load())
			}
		})
	}
}

// TestDetectBotChPTRIgnoresFailedAnswer checks that ch_ptr does not label an address whose check failed.
func TestDetectBotChPTRIgnoresFailedAnswer(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	c := stubPTR(t, func(context.Context, string) ([]string, error) { return nil, errors.New("servfail") }, nil)
	lists, sigs, dir := ptrBotEnv(t)
	cfg := config.Bots{PTR: true}
	detectBot(lists, sigs, dir, botVisit(ip, uaWinChrome), cfg)
	waitPTR(t, c, ip)
	if got := detectBot(lists, sigs, dir, botVisit(ip, uaWinChrome), cfg); got != detect.BotNone {
		t.Fatalf("failed check = %q, want none", got)
	}
}

// TestChPTRNeverSavesIP checks that ch_ptr with an ordinary user agent labels a confirmed crawler but never writes its address.
func TestChPTRNeverSavesIP(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	c := stubPTR(t, ptrNamed(googleHost+"."), fwdTo(ip))
	lists, sigs, dir := ptrBotEnv(t)
	cfg := config.Bots{PTR: true, SaveIP: true}
	savedBotIPs.Clear()
	detectBot(lists, sigs, dir, botVisit(ip, uaWinChrome), cfg)
	waitPTR(t, c, ip)
	if got := detectBot(lists, sigs, dir, botVisit(ip, uaWinChrome), cfg); got != "google" {
		t.Fatalf("confirmed crawler = %q, want google", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "ip_google.dat")); err == nil {
		t.Fatal("ch_ptr must not write ip_google.dat")
	}
}

// TestSaveBotIPLabels checks that saveBotIP writes a search engine and ignores apple, fake and other labels.
func TestSaveBotIPLabels(t *testing.T) {
	dir := t.TempDir()
	ip := netip.MustParseAddr("66.249.66.1")
	savedBotIPs.Clear()
	for _, label := range []string{detect.BotApple, detect.FakeLabel("google"), "proxy", "others", detect.BotNone} {
		saveBotIP(dir, label, ip)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("unexpected files written: %v", entries)
	}
	saveBotIP(dir, detect.BotGoogle, ip)
	b, err := os.ReadFile(filepath.Join(dir, "ip_google.dat"))
	if err != nil || strings.TrimSpace(string(b)) != "66.249.66.1" {
		t.Fatalf("ip_google.dat = %q, %v", b, err)
	}
}

// relayDeps returns engine deps whose list index has the Private Relay list loaded from body ("" for an empty
// list). botPath is the bot profile file or "" for none, log receives the profile messages.
func relayDeps(t *testing.T, botPath, body string, log *slog.Logger) (*engineDeps, *ipindex.Set, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, detect.ListPrivateRelay+".dat"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lists := ipindex.NewSet(dir, discardLog())
	lists.Load(detect.ListPrivateRelay)
	return &engineDeps{botPath: botPath, lists: lists, log: log}, lists, dir
}

// TestBotProfileDefaultProxyWaitsForRelayList checks the default proxy check without a file and with a file
// that has no ch_proxy: off while the Private Relay list is empty, on once it is loaded.
func TestBotProfileDefaultProxyWaitsForRelayList(t *testing.T) {
	want := config.DefaultBotProfile()
	t.Run("no file", func(t *testing.T) {
		d, _, _ := relayDeps(t, "", "", nil)
		if got := d.botProfile(); got.Proxy || got != withProxy(want, false) {
			t.Fatalf("empty list = %+v, want the default with Proxy off", got)
		}
		d, _, _ = relayDeps(t, "", "203.0.113.5\n", nil)
		if got := d.botProfile(); got != want {
			t.Fatalf("loaded list = %+v, want %+v", got, want)
		}
	})
	t.Run("file without ch_proxy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bot_protection.json")
		writeBotFile(t, path, `{"ch_ua":true}`, time.Now())
		d, lists, dir := relayDeps(t, path, "", nil)
		if got := d.botProfile(); got != (config.Bots{CheckUA: true}) {
			t.Fatalf("empty list = %+v, want Proxy off", got)
		}
		if err := os.WriteFile(filepath.Join(dir, detect.ListPrivateRelay+".dat"), []byte("203.0.113.5\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		lists.Reload()
		if got := d.botProfile(); got.Proxy {
			t.Fatalf("inside the stat interval = %+v, want the cached profile", got)
		}
		expireBotSnapshot()
		if got := d.botProfile(); got != (config.Bots{CheckUA: true, Proxy: true}) {
			t.Fatalf("loaded list = %+v, want Proxy on", got)
		}
	})
	t.Run("invalid file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bot_protection.json")
		writeBotFile(t, path, `not json`, time.Now())
		d, _, _ := relayDeps(t, path, "", nil)
		if got := d.botProfile(); got != withProxy(want, false) {
			t.Fatalf("invalid file, empty list = %+v", got)
		}
	})
}

// withProxy returns p with the proxy toggle set to on.
func withProxy(p config.Bots, on bool) config.Bots {
	p.Proxy = on
	return p
}

// TestBotProfileExplicitProxy checks that an explicit ch_proxy does not depend on the Private Relay list.
func TestBotProfileExplicitProxy(t *testing.T) {
	cases := []struct {
		body, relay string
		want        bool
	}{
		{`{"ch_ua":true,"ch_proxy":true}`, "", true},
		{`{"ch_ua":true,"ch_proxy":true}`, "203.0.113.5\n", true},
		{`{"ch_ua":true,"ch_proxy":false}`, "", false},
		{`{"ch_ua":true,"ch_proxy":false}`, "203.0.113.5\n", false},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "bot_protection.json")
		writeBotFile(t, path, tc.body, time.Now())
		d, _, _ := relayDeps(t, path, tc.relay, nil)
		if got := d.botProfile(); got.Proxy != tc.want || !got.CheckUA {
			t.Errorf("%s with relay %q = %+v, want Proxy %v", tc.body, tc.relay, got, tc.want)
		}
	}
}

// TestBotProfileProxyStateLoggedOnChange checks that the default proxy state is logged once per change, not per request.
func TestBotProfileProxyStateLoggedOnChange(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	path := filepath.Join(t.TempDir(), "bot_protection.json")
	writeBotFile(t, path, `{"ch_ua":true}`, time.Now())
	d, lists, dir := relayDeps(t, path, "", log)
	count := func() int { return strings.Count(out.String(), "default proxy check") }

	for i := 0; i < 5; i++ {
		d.botProfile()
	}
	expireBotSnapshot()
	d.botProfile()
	if count() != 1 {
		t.Fatalf("messages = %d, want 1:\n%s", count(), out.String())
	}
	if err := os.WriteFile(filepath.Join(dir, detect.ListPrivateRelay+".dat"), []byte("203.0.113.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lists.Reload()
	expireBotSnapshot()
	d.botProfile()
	if count() != 2 || !strings.Contains(out.String(), "enabled=true") {
		t.Fatalf("messages = %d after the list loaded, want 2 with enabled=true:\n%s", count(), out.String())
	}
}

// TestDetectBotRepeatedFailureBecomesFake checks that the first and second failed checks keep the user agent
// label, the third consecutive one is final (fake, ptrNegativeTTL) and an answer resets the count.
func TestDetectBotRepeatedFailureBecomesFake(t *testing.T) {
	ip := netip.MustParseAddr("66.249.66.1")
	var healthy atomic.Bool
	c := stubPTR(t, func(context.Context, string) ([]string, error) {
		if healthy.Load() {
			return []string{googleHost + "."}, nil
		}
		return nil, errors.New("servfail")
	}, fwdTo(ip))
	lists, sigs, dir := ptrBotEnv(t)
	cfg := config.Bots{CheckUA: true}
	visit := func() string { return detectBot(lists, sigs, dir, botVisit(ip, uaGoogleBot), cfg) }
	// check starts a new check of the address and waits for its entry.
	check := func() ptrEntry {
		expirePTR(c, ip, time.Now().Add(-time.Second))
		visit()
		return waitPTR(t, c, ip)
	}

	for n := 1; n <= 2; n++ {
		if ent := check(); !ent.failed || ent.failures != n {
			t.Fatalf("failure %d: entry = %+v", n, ent)
		}
		if got := visit(); got != "google" {
			t.Fatalf("failure %d: label = %q, want google", n, got)
		}
	}
	before := time.Now()
	ent := check()
	if ent.failed || ent.failures != ptrFailedLimit {
		t.Fatalf("third failure: entry = %+v, want final", ent)
	}
	if d := ent.expires.Sub(before); d < ptrNegativeTTL || d > ptrNegativeTTL+5*time.Second {
		t.Fatalf("final entry lives %v, want about %v", d, ptrNegativeTTL)
	}
	if got := visit(); got != "fake_google" {
		t.Fatalf("third failure: label = %q, want fake_google", got)
	}

	healthy.Store(true)
	if ent := check(); ent.failed || !ent.confirmed || ent.failures != 0 {
		t.Fatalf("answer: entry = %+v, want confirmed with the count reset", ent)
	}
	healthy.Store(false)
	if ent := check(); !ent.failed || ent.failures != 1 {
		t.Fatalf("failure after an answer: entry = %+v, want the first failure", ent)
	}
	if got := visit(); got != "google" {
		t.Fatalf("failure after an answer: label = %q, want google", got)
	}
}
