package main

import (
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/ipindex"
)

// uaZoomBot is a crawler that only the downloaded database knows.
const uaZoomBot = "Zoombot/1.0 (+http://zoom.example/bot)"

// botSigRules is a small downloaded database: a crawler, a search engine the
// built-in matcher already knows and a weak generic word.
var botSigRules = []detect.BotRuleRecord{
	{Source: "test", Pattern: "(?i)zoombot/", Name: "Zoom"},
	{Source: "test", Pattern: "(?i)googlebot", Name: "Googlebot"},
	{Source: "test", Pattern: "(?i)bot", Name: "generic"},
}

// writeBotSigs writes recs as the bot signature database of dir.
func writeBotSigs(t *testing.T, dir string, recs []detect.BotRuleRecord) {
	t.Helper()
	b, err := detect.EncodeBotDBFile(detect.BotDBFile{Rules: recs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, detect.FileBotSignatures), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// botSigEnv returns an IP index with ip_google holding 66.249.64.0/19 and
// signatures over the same directory, with the database written when recs is
// not nil.
func botSigEnv(t *testing.T, recs []detect.BotRuleRecord) (*ipindex.Set, *detect.Signatures, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ip_google.dat"), []byte("66.249.64.0/19\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if recs != nil {
		writeBotSigs(t, dir, recs)
	}
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	return lists, detect.NewSignatures(dir, log), dir
}

// TestBotSigsSearchEngineNeedsIP: a Googlebot user agent from a Google address
// is google; from a foreign address the answer is the same as without the
// database (pending, then fake_google) and the database is never the reason.
func TestBotSigsSearchEngineNeedsIP(t *testing.T) {
	c := stubPTR(t, ptrNamed("host.example.net."), fwdTo())
	cfg := config.Bots{CheckUA: true}
	listed, foreign := netip.MustParseAddr("66.249.66.1"), netip.MustParseAddr("203.0.113.7")

	withDB, sigsDB, dirDB := botSigEnv(t, botSigRules)
	if sigsDB.BotDB().Len() == 0 {
		t.Fatal("database not loaded")
	}
	if bot, why := detectBotWhy(withDB, sigsDB, dirDB, botVisit(listed, uaGoogleBot), cfg); bot != detect.BotGoogle || why != "" {
		t.Fatalf("listed googlebot = %q %q, want google without a database reason", bot, why)
	}

	plain, sigsPlain, dirPlain := botSigEnv(t, nil)
	before, _ := detectBotWhy(plain, sigsPlain, dirPlain, botVisit(foreign, uaGoogleBot), cfg)
	waitPTR(t, c, foreign)
	after, _ := detectBotWhy(plain, sigsPlain, dirPlain, botVisit(foreign, uaGoogleBot), cfg)

	gotAfter, why := detectBotWhy(withDB, sigsDB, dirDB, botVisit(foreign, uaGoogleBot), cfg)
	if before != detect.BotGoogle || after != detect.FakeLabel(detect.BotGoogle) {
		t.Fatalf("baseline changed: %q then %q", before, after)
	}
	if gotAfter != after || why != "" {
		t.Fatalf("foreign googlebot with the database = %q %q, want %q as before", gotAfter, why, after)
	}
}

// TestBotSigsWeakSignalsAreNotBots: a proxy or VPN address, an empty referer and
// a weak generic word do not make a visitor a bot under the signature check.
func TestBotSigsWeakSignalsAreNotBots(t *testing.T) {
	lists, sigs, dir := botSigEnv(t, botSigRules)
	ip := netip.MustParseAddr("203.0.113.8")
	cfg := config.Bots{CheckUA: true}
	for _, in := range []botInput{
		{IP: ip, UA: uaIPhone, Ref: "", Lang: "en", Proxy: true, Device: detect.Phone},
		{IP: ip, UA: uaWinChrome, Ref: "-", Lang: "en", Proxy: true, Device: detect.Computer},
		{IP: ip, UA: uaIPhone + " cubot", Ref: "", Lang: "en", Device: detect.Phone},
	} {
		if bot, why := detectBotWhy(lists, sigs, dir, in, cfg); bot != detect.BotNone {
			t.Errorf("%+v = %q %q, want human", in, bot, why)
		}
	}
}

// botFilterGroup has a stream that keeps bots (Bot = yes) and one that keeps humans (Bot = no).
func botFilterGroup() *config.Groups {
	return group(
		config.Stream{Name: "Bots", Status: true, Rules: config.Rules{Bot: config.FlagA},
			Out: config.Output{Redirect: "show_text", Out: "BOT"}},
		config.Stream{Name: "Humans", Status: true, Rules: config.Rules{Bot: config.FlagB},
			Out: config.Output{Redirect: "show_text", Out: "HUMAN"}},
	)
}

// TestBotSigsStreamFilters: the database works in every stream without a switch
// of its own. A crawler it knows goes to the Bot = yes stream with the source
// and rule in X-Emtds-Bot-Reason; a browser goes to Bot = no; without the
// database the same crawler stays where it went before.
func TestBotSigsStreamFilters(t *testing.T) {
	dbDir := t.TempDir()
	writeBotSigs(t, dbDir, botSigRules)
	withDB := testEnv(t, botFilterGroup(), func(d *engineDeps) { d.sigs = detect.NewSignatures(dbDir, discardLog()) })
	plain := testEnv(t, botFilterGroup())
	hdr := func(ua string) map[string]string { return map[string]string{"User-Agent": ua, "Accept-Language": "en"} }

	rec := do(t, withDB, "/g1", "8.8.8.8", hdr(uaZoomBot))
	wantStream(t, rec, "Bots")
	wantHdr(t, rec, "X-Emtds-Bot", detect.BotSignDB)
	wantHdr(t, rec, "X-Emtds-Bot-Reason", "test: Zoom")
	wantCode(t, rec, http.StatusOK)

	rec = do(t, withDB, "/g1", "8.8.8.8", hdr(uaIPhone))
	wantStream(t, rec, "Humans")
	wantHdr(t, rec, "X-Emtds-Bot", "-")
	wantHdr(t, rec, "X-Emtds-Bot-Reason", "")

	rec = do(t, plain, "/g1", "8.8.8.8", hdr(uaZoomBot))
	wantStream(t, rec, "Humans")
	wantHdr(t, rec, "X-Emtds-Bot", "-")

	rec = do(t, withDB, "/g1", "8.8.8.8", hdr("curl/8.4"))
	wantStream(t, rec, "Bots")
	wantHdr(t, rec, "X-Emtds-Bot", detect.BotHTTPClient)
	wantHdr(t, rec, "X-Emtds-Bot-Reason", "")
}

// TestBotSigsCheckDoesNotAllocate: the signature part of a click reads the
// loaded database and allocates nothing.
func TestBotSigsCheckDoesNotAllocate(t *testing.T) {
	lists, sigs, dir := botSigEnv(t, botSigRules)
	ip := netip.MustParseAddr("203.0.113.9")
	cfg := config.Bots{CheckUA: true}
	for _, ua := range []string{uaWinChrome, uaZoomBot} {
		in := botVisit(ip, ua)
		detectBotWhy(lists, sigs, dir, in, cfg)
		if n := testing.AllocsPerRun(200, func() { detectBotWhy(lists, sigs, dir, in, cfg) }); n != 0 {
			t.Errorf("detectBotWhy(%.30q) allocates %v times", ua, n)
		}
	}
}
