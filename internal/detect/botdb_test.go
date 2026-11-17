package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// botFixtureMatomo is a short bots.yml in the DeviceDetector format.
const botFixtureMatomo = `###############
# Device Detector
###############

- regex: 'WireReaderBot'
  name: 'WireReaderBot'
  category: 'Feed Fetcher'
  producer:
    name: 'Wire Reader Inc'

- regex: "Let's Encrypt validation server"
  name: 'Let''s Encrypt Validation'

- regex: '(?<!HTC)[ _]Butterfly/'
  name: 'Butterfly'

- regex: 'Yeti'
  name: 'Naver'
`

// botFixtureCUA is a short crawler-user-agents.json.
const botFixtureCUA = `[
 {"pattern": "Zoombot\\/", "instances": ["Zoombot/1.0"]},
 {"pattern": "wireReaderbot"},
 {"pattern": "[wW]get"},
 {"pattern": "bot"}
]`

// botFixtureIsbot is a short isbot patterns.json.
const botFixtureIsbot = `[" daum[ /]", "(?<! cu)bots?(?:\\b|_)", "^[^ ]{50,}$", "24x7", "google"]`

// mustBotDB parses the given sources in order and builds the database.
func mustBotDB(t *testing.T, sources map[string]string, order ...string) *BotDB {
	t.Helper()
	var lists [][]BotRuleRecord
	for _, name := range order {
		cands, _, err := ParseBotSource([]byte(sources[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var recs []BotRuleRecord
		for _, c := range cands {
			recs = append(recs, BotRuleRecord{Source: name, Pattern: c.Pattern, Name: c.Name})
		}
		lists = append(lists, recs)
	}
	db, _ := BuildBotDB(MergeBotRules(lists...))
	return db
}

func TestParseBotSourceFormats(t *testing.T) {
	m, rej, err := ParseBotSource([]byte(botFixtureMatomo))
	if err != nil || len(m) != 3 || rej != 1 {
		t.Fatalf("matomo: %d rules, %d rejected, %v", len(m), rej, err)
	}
	if m[1].Name != "Let's Encrypt Validation" || !strings.Contains(m[1].Pattern, "Let's Encrypt") || !strings.HasPrefix(m[0].Pattern, "(?i)"+matomoPrefix) {
		t.Fatalf("matomo rule %+v / %+v", m[0], m[1])
	}
	c, rej, err := ParseBotSource([]byte(botFixtureCUA))
	if err != nil || len(c) != 4 || rej != 0 {
		t.Fatalf("cua: %d rules, %d rejected, %v", len(c), rej, err)
	}
	i, rej, err := ParseBotSource([]byte(botFixtureIsbot))
	if err != nil || len(i) != 3 || rej != 2 {
		t.Fatalf("isbot: %d rules, %d rejected, %v", len(i), rej, err)
	}
	l, _, err := ParseBotSource([]byte("# comment\nFooCrawler/\n\nbar-agent-x\n"))
	if err != nil || len(l) != 2 {
		t.Fatalf("lines: %d, %v", len(l), err)
	}
	for _, bad := range []string{"", "  ", "<html>error</html>", `{"error":"rate limited"}`, "[1,2"} {
		if _, _, err := ParseBotSource([]byte(bad)); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

// TestBotDBDuplicatesAndBrokenRules: one pattern from two sources is kept once
// with the first source; a broken or anchorless expression is not accepted.
func TestBotDBDuplicatesAndBrokenRules(t *testing.T) {
	a := []BotRuleRecord{{Source: "a", Pattern: "(?i)zoombot/", Name: "Zoom"}, {Source: "a", Pattern: "(?i)zoombot/"}}
	b := []BotRuleRecord{{Source: "b", Pattern: "(?I)ZOOMBOT/"}, {Source: "b", Pattern: "(?i)otherbot-x"}}
	merged := MergeBotRules(a, b)
	if len(merged) != 2 || merged[0].Source != "a" || merged[1].Pattern != "(?i)otherbot-x" {
		t.Fatalf("merged %+v", merged)
	}
	db, skipped := BuildBotDB([]BotRuleRecord{
		{Source: "a", Pattern: "(?i)zoombot/"},
		{Source: "a", Pattern: "(?i)zoombot/"},
		{Source: "a", Pattern: "(?i)(?<!x)brokenbot"},
		{Source: "a", Pattern: "(?i)^[^ ]{50,}$"},
		{Source: "a", Pattern: "(?i)[a-z"},
	})
	if db.Len() != 1 || skipped != 3 {
		t.Fatalf("len %d skipped %d", db.Len(), skipped)
	}
}

// TestBotDBSignatureNotSearchEngine: a crawler from a source labels the visitor
// with the source and the rule, through ClassifyUA.
func TestBotDBSignatureNotSearchEngine(t *testing.T) {
	db := mustBotDB(t, map[string]string{"matomo": botFixtureMatomo, "cua": botFixtureCUA, "isbot": botFixtureIsbot}, "matomo", "cua", "isbot")
	cases := []struct{ ua, label, reason string }{
		{"Mozilla/5.0 (compatible; WireReaderBot/1.2)", BotSignDB, "matomo: WireReaderBot"},
		{"Zoombot/1.0 (+http://zoom.example)", BotSignDB, "cua: Zoombot\\/"},
		{"Mozilla/5.0 (compatible; Let's Encrypt validation server)", BotSignDB, "matomo: Let's Encrypt Validation"},
		{"Mozilla/5.0 x Daum/4.1", BotSignDB, "isbot: daum[ /]"},
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", BotGoogle, ""},
		{"curl/8.0", BotHTTPClient, ""},
	}
	for _, c := range cases {
		label, hit := ClassifyUA(c.ua, nil, db)
		if label != c.label || hit.Reason != c.reason {
			t.Errorf("ClassifyUA(%q) = %q %q; want %q %q", c.ua, label, hit.Reason, c.label, c.reason)
		}
	}
}

// TestBotDBTokenBoundary: a Matomo name does not match inside a longer word.
func TestBotDBTokenBoundary(t *testing.T) {
	db := mustBotDB(t, map[string]string{"matomo": botFixtureMatomo}, "matomo")
	if _, ok := db.Match("Mozilla/5.0 Spaghettiyeti/1.0"); ok {
		t.Fatal("Yeti inside a word must not match")
	}
	if _, ok := db.Match("Mozilla/5.0 (compatible; Yeti/1.1)"); !ok {
		t.Fatal("Yeti as a token must match")
	}
}

// TestBotDBBrowsersAreHumans: ordinary browsers and in-app browsers are not
// bots, and weak words such as bot, google or wget never label a visitor alone.
func TestBotDBBrowsersAreHumans(t *testing.T) {
	db := mustBotDB(t, map[string]string{"matomo": botFixtureMatomo, "cua": botFixtureCUA, "isbot": botFixtureIsbot}, "matomo", "cua", "isbot")
	if db.Weak() == 0 {
		t.Fatal("the fixtures carry weak rules")
	}
	for _, ua := range []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
		"Mozilla/5.0 (Linux; Android 13; Pixel 7; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/130.0 Mobile Safari/537.36 Instagram 350.0",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148 [FBAN/FBIOS;FBAV/450.0] cubot bot",
		"Mozilla/5.0 (Linux; Android 12) Mobile BingSapphire/28.0 WireReaderBot",
		"",
	} {
		if label, hit := ClassifyUA(ua, nil, db); label != BotNone {
			t.Errorf("ClassifyUA(%.60q) = %q (%s); want human", ua, label, hit.Reason)
		}
	}
}

// TestBotDBNilAndEmpty: no database answers like the built-in matcher alone.
func TestBotDBNilAndEmpty(t *testing.T) {
	for _, db := range []*BotDB{nil, {}} {
		if _, ok := db.Match("WireReaderBot"); ok || db.Len() != 0 {
			t.Fatal("an empty database matches nothing")
		}
		if label, _ := ClassifyUA("curl/7", nil, db); label != BotHTTPClient {
			t.Fatalf("built-in label lost: %q", label)
		}
	}
}

// TestBotDBMatchAllocations: a click check scans the loaded database without
// building anything and without allocating.
func TestBotDBMatchAllocations(t *testing.T) {
	db := mustBotDB(t, map[string]string{"matomo": botFixtureMatomo, "cua": botFixtureCUA, "isbot": botFixtureIsbot}, "matomo", "cua", "isbot")
	human := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	bot := "Mozilla/5.0 (compatible; WireReaderBot/1.2)"
	db.Match(bot)
	for _, ua := range []string{human, bot} {
		if n := testing.AllocsPerRun(200, func() { ClassifyUA(ua, nil, db) }); n != 0 {
			t.Errorf("ClassifyUA(%.30q) allocates %v times", ua, n)
		}
	}
}

// writeBotDB writes a database file with recs into dir and moves its time
// forward, so the next Reload sees a change.
func writeBotDB(t *testing.T, dir string, content []byte, step int) {
	t.Helper()
	path := filepath.Join(dir, FileBotSignatures)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(time.Duration(step) * time.Minute)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

// TestSignaturesBotDBReload: a valid file is swapped in, a broken file keeps
// the working database, a removed file leaves only the built-in signatures.
func TestSignaturesBotDBReload(t *testing.T) {
	dir := t.TempDir()
	s := NewSignatures(dir, nil)
	if s.BotDB() == nil || s.BotDB().Len() != 0 {
		t.Fatal("a fresh holder has an empty database")
	}
	good, err := EncodeBotDBFile(BotDBFile{Rules: []BotRuleRecord{{Source: "a", Pattern: "(?i)zoombot/", Name: "Zoom"}}})
	if err != nil {
		t.Fatal(err)
	}
	writeBotDB(t, dir, good, 1)
	s.Reload()
	first := s.BotDB()
	if first.Len() != 1 {
		t.Fatalf("loaded %d rules", first.Len())
	}
	writeBotDB(t, dir, []byte(`{"version":1,"rules":[`), 2)
	s.Reload()
	if s.BotDB() != first {
		t.Fatal("a broken file replaced the working database")
	}
	writeBotDB(t, dir, []byte(`{"version":99,"rules":[]}`), 3)
	s.Reload()
	if s.BotDB() != first {
		t.Fatal("an unknown version replaced the working database")
	}
	if label, hit := ClassifyUA("Zoombot/2", s.UAMatcher(), s.BotDB()); label != BotSignDB || hit.Reason != "a: Zoom" {
		t.Fatalf("got %q %q", label, hit.Reason)
	}
	if err := os.Remove(filepath.Join(dir, FileBotSignatures)); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if s.BotDB().Len() != 0 {
		t.Fatal("a removed file must leave the built-in signatures only")
	}
}
