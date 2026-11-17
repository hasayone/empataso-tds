package detect

import (
	"strings"
	"unicode/utf8"
)

// Bot labels.
const (
	BotNone        = ""
	BotApple       = "apple"
	BotBaidu       = "baidu"
	BotBing        = "bing"
	BotGoogle      = "google"
	BotMail        = "mail"
	BotYahoo       = "yahoo"
	BotYandex      = "yandex"
	BotHeadless    = "headless"
	BotHTTPClient  = "http_client"
	BotCrawler     = "crawler"
	BotScanner     = "scanner"
	BotSignUA      = "sign_ua"
	BotSignRef     = "sign_ref"
	BotUABlacklist = "ua_blacklist"
	// BotSignDB labels a User-Agent that matched a rule of the downloaded bot signature database.
	BotSignDB = "sign_db"
)

// FakeLabel returns the label of a bot that claims to be label but failed
// verification, for example "fake_google".
func FakeLabel(label string) string { return "fake_" + label }

// Verifiable reports whether the identity of a bot with this label can be
// confirmed by reverse DNS: google, bing, yandex, baidu, yahoo, mail and apple.
func Verifiable(label string) bool {
	switch label {
	case BotGoogle, BotBing, BotYandex, BotBaidu, BotYahoo, BotMail, BotApple:
		return true
	}
	return false
}

// crawlerDomains lists the reverse DNS domains of the verifiable search engines.
var crawlerDomains = []struct {
	label   string
	domains []string
}{
	{BotGoogle, []string{"googlebot.com", "google.com"}},
	{BotBing, []string{"search.msn.com"}},
	{BotYandex, []string{"yandex.ru", "yandex.net", "yandex.com"}},
	{BotBaidu, []string{"baidu.com", "baidu.jp"}},
	{BotYahoo, []string{"crawl.yahoo.net"}},
	{BotMail, []string{"go.mail.ru"}},
	{BotApple, []string{"applebot.apple.com"}},
}

// CrawlerByHost classifies a crawler by its reverse DNS (PTR) name.
// host may carry a trailing dot and any letter case. It returns the label when
// host equals a crawler domain or is a subdomain of it on a dot boundary, and
// BotNone otherwise, including for an empty host. It does not report an error.
func CrawlerByHost(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return BotNone
	}
	for _, c := range crawlerDomains {
		for _, d := range c.domains {
			if host == d || strings.HasSuffix(host, "."+d) {
				return c.label
			}
		}
	}
	return BotNone
}

// containsFolded reports whether l contains pattern s, compared case-insensitively.
// l must already be lowercased with strings.ToLower. A pattern of lowercase
// ASCII is matched without a copy; any other pattern is lowercased first, so
// callers may pass lists in any case and get the same answer.
func containsFolded(l, s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= utf8.RuneSelf || ('A' <= c && c <= 'Z') {
			return strings.Contains(l, strings.ToLower(s))
		}
	}
	return strings.Contains(l, s)
}

// BotByUA classifies a bot by its User-Agent in one pass of the matcher m.
// A nil m means DefaultUAMatcher. An empty ua is not a bot. It returns a search
// engine label, sign_ua, headless, scanner, http_client or crawler, or BotNone,
// and does not allocate.
func BotByUA(ua string, m *Matcher) string {
	if ua == "" {
		return BotNone
	}
	if m == nil {
		m = DefaultUAMatcher()
	}
	return m.Match(ua)
}

// ClassifyUA classifies a bot by its User-Agent: the matcher m first (nil means
// DefaultUAMatcher), then the downloaded database db (nil means none). db is
// asked only when m found no label and ua has no token of a human browser or
// app, so a search engine label and its IP verification stay as they are and a
// database rule never makes a search engine User-Agent count more than before.
// It returns BotNone for an empty ua or no match, BotSignDB with the rule in hit
// for a database match, and the matcher label with an empty hit otherwise. It
// does not allocate and does not use the network.
func ClassifyUA(ua string, m *Matcher, db *BotDB) (label string, hit BotHit) {
	if ua == "" {
		return BotNone, BotHit{}
	}
	if m == nil {
		m = DefaultUAMatcher()
	}
	label, human := m.MatchHuman(ua)
	if label != BotNone || human {
		return label, BotHit{}
	}
	if h, ok := db.Match(ua); ok {
		return BotSignDB, h
	}
	return BotNone, BotHit{}
}

// BotByReferer: a substring from signature_ref.dat in the referer → sign_ref.
// sigs is the contents of signature_ref.dat in any case; Signatures keeps it
// lowercased. An empty referer is not checked by the caller.
func BotByReferer(referer string, sigs []string) string {
	if referer == "" {
		return BotNone
	}
	l := strings.ToLower(referer)
	for _, s := range sigs {
		if s != "" && containsFolded(l, s) {
			return BotSignRef
		}
	}
	return BotNone
}

// InUABlacklist: exact match of the UA against a line from ua_blacklist.dat.
func InUABlacklist(ua string, list []string) bool {
	for _, s := range list {
		if ua == s {
			return true
		}
	}
	return false
}
