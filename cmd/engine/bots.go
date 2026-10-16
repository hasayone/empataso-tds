package main

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/ipindex"
)

var ptrResolver = &net.Resolver{}

type botFileCache struct {
	mu      sync.Mutex
	mod     time.Time
	known   bool
	profile config.Bots
}

var botFiles botFileCache

// botProfile reads the installation bot profile. A missing file uses the default.
// The result is cached until the file's modification time changes.
func (d *engineDeps) botProfile() config.Bots {
	if d == nil || d.botPath == "" {
		return config.DefaultBotProfile()
	}
	st, err := os.Stat(d.botPath)
	botFiles.mu.Lock()
	defer botFiles.mu.Unlock()
	if err != nil {
		return config.DefaultBotProfile()
	}
	if botFiles.known && botFiles.mod.Equal(st.ModTime()) {
		return botFiles.profile
	}
	b, err := os.ReadFile(d.botPath)
	if err != nil {
		return config.DefaultBotProfile()
	}
	var profile config.Bots
	if json.Unmarshal(b, &profile) != nil {
		return config.DefaultBotProfile()
	}
	botFiles.profile = profile
	botFiles.mod = st.ModTime()
	botFiles.known = true
	return profile
}

// detectBot — bot detection based on the toggles of the selected
// stream. Returns the bot label or "" (not a bot).
func detectBot(lists *ipindex.Set, sigs *detect.Signatures, dataDir string, ip netip.Addr, ua, ref, lang string, b config.Bots) string {
	bot := detect.BotNone
	if b.CheckUA {
		bot = detect.BotByUA(ua, sigs.UA())
		if bot == detect.BotNone {
			bot = detect.BotByReferer(ref, sigs.Ref())
		}
	}
	if bot == detect.BotNone && b.IPv6 && ip.IsValid() && !ip.Is4() {
		bot = "ipv6"
	}
	if bot == detect.BotNone && b.EmptyUA && (ua == "" || ua == " ") {
		bot = "empty_ua"
	}
	if bot == detect.BotNone && b.EmptyRef && (ref == "" || ref == "-") {
		bot = "empty_ref"
	}
	if bot == detect.BotNone && b.EmptyLang && (lang == "" || lang == "-") {
		bot = "empty_lang"
	}
	if bot == detect.BotNone && b.PTR && ip.IsValid() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if names, err := ptrResolver.LookupAddr(ctx, ip.String()); err == nil && len(names) > 0 {
			bot = detect.BotByPTR(names[0])
		}
		cancel()
	}
	if bot == detect.BotNone && b.ListUA && detect.InUABlacklist(ua, sigs.UABlacklist()) {
		bot = detect.BotUABlacklist
	}
	// save_ip: append the IP of a matched search engine to its list (if not already there).
	if bot != detect.BotNone && b.SaveIP {
		if _, known := lists.Lookup("ip_"+bot, ip); !known {
			saveBotIP(dataDir, bot, ip)
		}
	}
	// Detection via search-engine IP lists.
	if bot == detect.BotNone {
		for _, c := range []struct {
			on   bool
			name string
		}{
			{b.IPBaidu, "baidu"}, {b.IPBing, "bing"}, {b.IPGoogle, "google"},
			{b.IPMail, "mail"}, {b.IPYahoo, "yahoo"}, {b.IPYandex, "yandex"}, {b.IPOthers, "others"},
		} {
			if c.on {
				if _, ok := lists.Lookup("ip_"+c.name, ip); ok {
					bot = c.name
					break
				}
			}
		}
	}
	return bot
}

// savedBotIPs remembers what this process already appended, keyed by
// "<bot>|<ip>".
//
// The ipindex lookup in detectBot only sees IPs as of the last hot-reload (a
// minute by default), so without this every hit from the same crawler IP within
// that window appended another line: a busy site grew ip_<se>.dat by thousands
// of duplicate lines per minute, forever. The set is bounded by the number of
// distinct crawler IPs seen, which is small, and it is only an optimisation —
// losing it on restart costs at most one redundant line per IP.
var savedBotIPs sync.Map

// saveBotIP appends an IP to ip_<se>.dat (only for known search engines), at
// most once per IP per process.
func saveBotIP(dataDir, bot string, ip netip.Addr) {
	switch bot {
	case "baidu", "bing", "google", "mail", "yahoo", "yandex":
	default:
		return
	}
	if _, dup := savedBotIPs.LoadOrStore(bot+"|"+ip.String(), struct{}{}); dup {
		return
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "ip_"+bot+".dat"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		// Not recorded after all — let a later request retry the write.
		savedBotIPs.Delete(bot + "|" + ip.String())
		return
	}
	_, _ = f.WriteString(ip.String() + "\n")
	_ = f.Close()
}
