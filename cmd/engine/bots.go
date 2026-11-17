package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/ipindex"
)

// botSnapshot is the last read of the bot profile file.
// path is the file it came from. file is the parsed profile, or the default
// when the file is missing, unreadable or not valid JSON. proxyDefault is true
// when file.Proxy is on only by default: the file has no ch_proxy key or there
// is no file. relay is true when the ip_private_relay list was loaded and not
// empty at the time of the snapshot. profile is file with the proxy check
// resolved: a default proxy check stays off until relay is true.
// read is true when the file contents were read, so an unchanged mtime and size
// may reuse file. checked is when the file was last stat'ed.
type botSnapshot struct {
	path         string
	file         config.Bots
	profile      config.Bots
	proxyDefault bool
	relay        bool
	read         bool
	mtime        time.Time
	size         int64
	checked      time.Time
}

var (
	// botSnap is read lock-free by every request.
	botSnap atomic.Pointer[botSnapshot]
	// botReload lets one request at a time re-stat the file after the interval.
	botReload sync.Mutex
)

// fresh reports whether s is a stat of path taken less than pageStatInterval ago.
func (s *botSnapshot) fresh(path string) bool {
	return s != nil && s.path == path && time.Since(s.checked) < pageStatInterval
}

// relayLoaded reports whether the ip_private_relay list is loaded and not empty.
// A nil d or a d without lists gives false; a nil index counts as empty.
func (d *engineDeps) relayLoaded() bool {
	return d != nil && d.lists.Len(detect.ListPrivateRelay) > 0
}

// resolveProxy returns p with a default proxy check switched off until the
// Private Relay list is loaded. proxyDefault is true when p.Proxy is on only by
// default; an explicit ch_proxy is never changed. relay is true when the list
// is loaded and not empty. Without the list every iCloud Private Relay visitor
// would be treated as a proxy.
func resolveProxy(p config.Bots, proxyDefault, relay bool) config.Bots {
	if proxyDefault && !relay {
		p.Proxy = false
	}
	return p
}

// botProfile returns the installation bot profile. A missing or invalid file uses the default.
// The file is stat'ed at most once per pageStatInterval and read again only when
// its modification time or size changed, so an edit is seen within that interval.
// A proxy check that is on only by default stays off until the Private Relay
// list is loaded; the list is looked at when the file is stat'ed, and a change
// of that state is logged once.
func (d *engineDeps) botProfile() config.Bots {
	if d == nil || d.botPath == "" {
		return resolveProxy(config.DefaultBotProfile(), true, d.relayLoaded())
	}
	if s := botSnap.Load(); s.fresh(d.botPath) {
		return s.profile
	}
	botReload.Lock()
	defer botReload.Unlock()
	prev := botSnap.Load()
	if prev.fresh(d.botPath) {
		return prev.profile
	}
	next := readBotProfile(d.botPath, prev, d.relayLoaded())
	if next.proxyDefault && d.log != nil && (prev == nil || prev.path != next.path || !prev.proxyDefault || prev.relay != next.relay) {
		d.log.Info("bot profile: default proxy check", "enabled", next.profile.Proxy, "private_relay_list", next.relay)
	}
	botSnap.Store(next)
	return next.profile
}

// readBotProfile stats path and returns a new snapshot of it.
// prev is the previous snapshot or nil; its parsed file is reused when it read
// the same path with the same mtime and size. relay says whether the Private
// Relay list is loaded and not empty. The file is parsed by
// config.ParseBotProfileExplicit. Errors are not returned: a missing, unreadable or
// invalid file yields the default profile.
func readBotProfile(path string, prev *botSnapshot, relay bool) *botSnapshot {
	next := &botSnapshot{path: path, file: config.DefaultBotProfile(), proxyDefault: true, relay: relay, checked: time.Now()}
	defer func() { next.profile = resolveProxy(next.file, next.proxyDefault, relay) }()
	st, err := os.Stat(path)
	if err != nil {
		return next
	}
	next.mtime, next.size = st.ModTime(), st.Size()
	if prev != nil && prev.read && prev.path == path && prev.mtime.Equal(next.mtime) && prev.size == next.size {
		next.file, next.proxyDefault, next.read = prev.file, prev.proxyDefault, true
		return next
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return next
	}
	next.read = true
	if profile, proxySet, err := config.ParseBotProfileExplicit(b); err == nil {
		next.file = profile
		next.proxyDefault = profile.Proxy && !proxySet
	}
	return next
}

// sameBotChecks reports whether a and b enable the same detection toggles.
// The serving fields Redirect, Out and Header are ignored: detectBot does not
// read them, so equal toggles give detectBot the same answer for one visitor.
func sameBotChecks(a, b config.Bots) bool {
	a.Redirect, a.Out, a.Header = "", "", ""
	b.Redirect, b.Out, b.Header = "", "", ""
	return a == b
}

// botInput is the visitor facts detectBot classifies.
// IP, UA, Ref and Lang are the address, user agent, referer and language.
// Proxy is true when the IP is in the ip_proxy list or matches an organization template.
// Device is detect.Phone, detect.Tablet or detect.Computer; an empty or unknown value is not a bot.
type botInput struct {
	IP     netip.Addr
	UA     string
	Ref    string
	Lang   string
	Proxy  bool
	Device string
}

// detectBot classifies a visitor from the toggles in b.
// lists is the IP index, sigs holds UA and referer signatures, and dataDir
// is the directory saveBotIP appends crawler addresses to. in carries the
// visitor address, user agent, referer, language, proxy flag and device.
// It returns the bot label, or detect.BotNone when no enabled check matches.
// It does not return an error and never waits for DNS.
//
// A user agent of a verifiable search engine is checked against the address:
// a listed address (ip_<label>) confirms it at once; otherwise the cached
// forward-confirmed reverse DNS answer decides, and a mismatch gives the
// detect.FakeLabel of the engine. The first request from an unknown address
// starts the check in the background and is labelled by its user agent alone.
// A timeout or a temporary DNS error is not a forgery: while such an answer is
// cached the visitor is labelled as if there were no answer.
// ch_ptr labels an address whose cached PTR name is in a crawler domain; it
// never saves an address.
// save_ip appends only addresses of a search-engine user agent that DNS
// confirmed.
func detectBot(lists *ipindex.Set, sigs *detect.Signatures, dataDir string, in botInput, b config.Bots) string {
	bot, _ := detectBotWhy(lists, sigs, dataDir, in, b)
	return bot
}

// detectBotWhy is detectBot that also returns why: "source: rule" of the
// downloaded signature database when its rule gave the label, empty otherwise.
// ch_ua covers the built-in signatures, signature_ua.dat and that database, so
// every stream and profile with ch_ua gets it without a switch of its own.
func detectBotWhy(lists *ipindex.Set, sigs *detect.Signatures, dataDir string, in botInput, b config.Bots) (bot, why string) {
	bot = detect.BotNone
	confirmed := false
	if b.CheckUA {
		var hit detect.BotHit
		bot, hit = detect.ClassifyUA(in.UA, sigs.UAMatcher(), sigs.BotDB())
		why = hit.Reason
		if detect.Verifiable(bot) {
			bot, confirmed = verifyCrawler(lists, in.IP, bot)
		}
		if bot == detect.BotNone {
			bot = detect.BotByReferer(in.Ref, sigs.Ref())
		}
	}
	if bot == detect.BotNone && b.Proxy && in.Proxy {
		bot = "proxy"
	}
	if bot == detect.BotNone && b.NonMobile && in.Device == detect.Computer {
		bot = "device"
	}
	if bot == detect.BotNone && b.IPv6 && in.IP.IsValid() && !in.IP.Is4() {
		bot = "ipv6"
	}
	if bot == detect.BotNone && b.EmptyUA && (in.UA == "" || in.UA == " ") {
		bot = "empty_ua"
	}
	if bot == detect.BotNone && b.EmptyRef && (in.Ref == "" || in.Ref == "-") {
		bot = "empty_ref"
	}
	if bot == detect.BotNone && b.EmptyLang && (in.Lang == "" || in.Lang == "-") {
		bot = "empty_lang"
	}
	if bot == detect.BotNone && b.PTR && in.IP.IsValid() {
		if ent, ok := ptrNames.peek(in.IP, ptrClassPTR); ok && !ent.failed && ent.crawler != "" {
			bot = ent.crawler
			if !ent.confirmed {
				bot = detect.FakeLabel(ent.crawler)
			}
		}
	}
	if bot == detect.BotNone && b.ListUA && detect.InUABlacklist(in.UA, sigs.UABlacklist()) {
		bot = detect.BotUABlacklist
	}
	if confirmed && b.SaveIP {
		if _, known := lists.Lookup("ip_"+bot, in.IP); !known {
			saveBotIP(dataDir, bot, in.IP)
		}
	}
	if bot == detect.BotNone {
		for _, c := range []struct {
			on   bool
			name string
		}{
			{b.IPBaidu, "baidu"}, {b.IPBing, "bing"}, {b.IPGoogle, "google"},
			{b.IPMail, "mail"}, {b.IPYahoo, "yahoo"}, {b.IPYandex, "yandex"}, {b.IPOthers, "others"},
		} {
			if c.on {
				if _, ok := lists.Lookup("ip_"+c.name, in.IP); ok {
					bot = c.name
					break
				}
			}
		}
	}
	return bot, why
}

// verifyCrawler checks that ip really belongs to the search engine label that
// the user agent claims. lists is the IP index, ip the visitor address and
// label a verifiable engine label.
// It returns label with confirmed true when ip is in ip_<label> (apple has no
// such list) or the cached DNS check confirmed it for that engine; label with
// confirmed false while the check has not finished or ended with a timeout or
// a temporary error; detect.FakeLabel(label) when the check gave a final
// answer without confirmation. It does not wait for DNS.
func verifyCrawler(lists *ipindex.Set, ip netip.Addr, label string) (string, bool) {
	if !ip.IsValid() {
		return label, false
	}
	if label != detect.BotApple {
		if _, listed := lists.Lookup("ip_"+label, ip); listed {
			return label, true
		}
	}
	ent, ok := ptrNames.peek(ip, ptrClassCrawler)
	switch {
	case !ok || ent.failed:
		return label, false
	case ent.crawler == label && ent.confirmed:
		return label, true
	}
	return detect.FakeLabel(label), false
}

// savedBotIPs holds the addresses this process already appended to ip_<se>.dat,
// keyed by "<bot>|<ip>", so one address is written once between list reloads.
// It grows with the number of distinct confirmed crawler addresses. After a
// restart it is empty, which costs at most one repeated line per address.
var savedBotIPs sync.Map

// saveBotIP appends an IP to ip_<se>.dat, at most once per IP per process.
// bot must be a verifiable search engine with an IP list; any other label,
// including apple and the fake_ labels, is ignored.
func saveBotIP(dataDir, bot string, ip netip.Addr) {
	if !detect.Verifiable(bot) || bot == detect.BotApple {
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
