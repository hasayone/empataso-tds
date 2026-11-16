package detect

import (
	"math/rand"
	"strings"
	"testing"
)

// naiveMatch is the reference for Matcher.Match: it checks every pattern with
// strings.Contains on the lowercased input and applies the same label rules.
func naiveMatch(pats []pattern, ua string) string {
	l := strings.ToLower(ua)
	var bestSE, bestOther, bestBare uint8
	mozilla, human := false, false
	for _, p := range pats {
		text := strings.ToLower(p.text)
		if text == "" || !strings.Contains(l, text) {
			continue
		}
		mozilla = mozilla || p.flag&flagMozilla != 0
		human = human || p.flag&flagHuman != 0
		switch {
		case p.prio == prioNone:
		case p.prio >= prioApple:
			bestSE = max(bestSE, p.prio)
		case p.bare:
			bestBare = max(bestBare, p.prio)
		default:
			bestOther = max(bestOther, p.prio)
		}
	}
	if !human && bestSE != 0 {
		return labelByPrio[bestSE]
	}
	if !mozilla {
		bestOther = max(bestOther, bestBare)
	}
	return labelByPrio[bestOther]
}

// TestMatcherClassic checks overlapping patterns, suffixes and failure links on the textbook set.
func TestMatcherClassic(t *testing.T) {
	m := newMatcher([]pattern{
		{text: "he", prio: prioCrawler},
		{text: "she", prio: prioScanner},
		{text: "his", prio: prioHeadless},
		{text: "hers", prio: prioSignUA},
	})
	cases := []struct{ in, want string }{
		{"ushers", BotSignUA},
		{"ushe", BotScanner},
		{"xhe", BotCrawler},
		{"shis", BotHeadless},
		{"sh", BotNone},
		{"hhhh", BotNone},
		{"shhers", BotSignUA},
		{"", BotNone},
	}
	for _, c := range cases {
		if got := m.Match(c.in); got != c.want {
			t.Errorf("Match(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestMatcherFailLinks checks that a match is found through a chain of failure links.
func TestMatcherFailLinks(t *testing.T) {
	m := newMatcher([]pattern{
		{text: "abcde", prio: prioHeadless},
		{text: "bcdf", prio: prioCrawler},
		{text: "cdg", prio: prioScanner},
	})
	cases := []struct{ in, want string }{
		{"abcdf", BotCrawler},
		{"abcdg", BotScanner},
		{"abcde", BotHeadless},
		{"abcdx", BotNone},
		{"xabcdabcdf", BotCrawler},
	}
	for _, c := range cases {
		if got := m.Match(c.in); got != c.want {
			t.Errorf("Match(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestMatcherInnerSuffixLabel checks that a pattern which is a suffix of a longer
// pattern is still reported when the longer one fails.
func TestMatcherInnerSuffixLabel(t *testing.T) {
	m := newMatcher([]pattern{
		{text: "xxbot", prio: prioScanner},
		{text: "bot", prio: prioCrawler},
	})
	if got := m.Match("xxbot"); got != BotScanner {
		t.Errorf("long pattern = %q", got)
	}
	if got := m.Match("xbot"); got != BotCrawler {
		t.Errorf("suffix pattern = %q", got)
	}
}

// TestMatcherCase checks that patterns and input are compared without regard to ASCII case.
func TestMatcherCase(t *testing.T) {
	m := newMatcher([]pattern{{text: "AhrefsBot", prio: prioCrawler}})
	for _, in := range []string{"ahrefsbot", "AHREFSBOT", "x AhRefsBoT/7", "AhrefsBot"} {
		if got := m.Match(in); got != BotCrawler {
			t.Errorf("Match(%q) = %q", in, got)
		}
	}
	u := newMatcher([]pattern{{text: "Яндекс", prio: prioCrawler}})
	if got := u.Match("бот яндекс"); got != BotCrawler {
		t.Errorf("lowercase UTF-8 input = %q", got)
	}
}

// TestMatcherRank checks the priority order of labels in one UA.
func TestMatcherRank(t *testing.T) {
	order := []struct {
		text string
		prio uint8
		want string
	}{
		{"zcrawler", prioCrawler, BotCrawler},
		{"zclient", prioHTTPClient, BotHTTPClient},
		{"zscanner", prioScanner, BotScanner},
		{"zheadless", prioHeadless, BotHeadless},
		{"zsign", prioSignUA, BotSignUA},
		{"applebot", prioApple, BotApple},
		{"yandex.com/bots", prioYandex, BotYandex},
		{"yahoo", prioYahoo, BotYahoo},
		{"mail.ru", prioMail, BotMail},
		{"google", prioGoogle, BotGoogle},
		{"bing", prioBing, BotBing},
		{"baidu", prioBaidu, BotBaidu},
	}
	var pats []pattern
	for _, o := range order {
		pats = append(pats, pattern{text: o.text, prio: o.prio})
	}
	m := newMatcher(pats)
	ua := ""
	for i := range order {
		ua += " " + order[i].text
		if got := m.Match(ua); got != order[i].want {
			t.Errorf("after %q: Match = %q; want %q", order[i].text, got, order[i].want)
		}
	}
}

// TestMatcherBareAndHuman checks the bare, "mozilla/" and human token rules.
func TestMatcherBareAndHuman(t *testing.T) {
	m := newMatcher([]pattern{
		{text: "mozilla/", flag: flagMozilla},
		{text: "bingsapphire", flag: flagHuman},
		{text: "bing", prio: prioBing},
		{text: "okhttp", prio: prioHTTPClient, bare: true},
		{text: "curl/", prio: prioHTTPClient},
		{text: "zzscan", prio: prioScanner},
	})
	cases := []struct{ in, want string }{
		{"okhttp/4.9", BotHTTPClient},
		{"Mozilla/5.0 okhttp/4.9", BotNone},
		{"okhttp/4.9 Mozilla/5.0", BotNone},
		{"Mozilla/5.0 curl/7", BotHTTPClient},
		{"Mozilla/5.0 okhttp zzscan", BotScanner},
		{"bingbot", BotBing},
		{"Mozilla/5.0 BingSapphire/1.0", BotNone},
		{"BingSapphire bingbot curl/7", BotHTTPClient},
		{"BingSapphire bingbot", BotNone},
	}
	for _, c := range cases {
		if got := m.Match(c.in); got != c.want {
			t.Errorf("Match(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestMatcherEmpty checks empty input, an empty pattern set and empty patterns.
func TestMatcherEmpty(t *testing.T) {
	if got := newMatcher(nil).Match("anything at all"); got != BotNone {
		t.Errorf("empty matcher = %q", got)
	}
	m := newMatcher([]pattern{{text: "", prio: prioCrawler}, {text: "x", prio: prioCrawler}})
	if m.Match("") != BotNone || m.Match("abc") != BotNone || m.Match("axb") != BotCrawler {
		t.Error("empty pattern must be ignored")
	}
}

// TestMatcherWideNodes checks nodes with more edges than the linear scan limit.
func TestMatcherWideNodes(t *testing.T) {
	var pats []pattern
	for c := byte('a'); c <= 'z'; c++ {
		pats = append(pats, pattern{text: "p" + string(c), prio: prioScanner})
		pats = append(pats, pattern{text: "pq" + string(c), prio: prioSignUA})
	}
	m := newMatcher(pats)
	for c := byte('a'); c <= 'z'; c++ {
		if got := m.Match("..p" + string(c)); got != BotScanner {
			t.Errorf("p%c = %q", c, got)
		}
	}
	if got := m.Match("p!"); got != BotNone {
		t.Errorf("p! = %q", got)
	}
	if got := m.Match("pqr"); got != BotSignUA {
		t.Errorf("pqr = %q", got)
	}
}

// TestMatcherVersusNaive compares the automaton with strings.Contains on random
// patterns and inputs over a tiny alphabet, where overlaps and failure links are dense.
func TestMatcherVersusNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const alphabet = "abAB/"
	word := func(minLen, maxLen int) string {
		b := make([]byte, minLen+rng.Intn(maxLen-minLen+1))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	for round := 0; round < 300; round++ {
		pats := make([]pattern, 1+rng.Intn(14))
		for i := range pats {
			p := pattern{text: word(1, 5), prio: uint8(rng.Intn(int(prioCount)))}
			if p.prio == prioNone {
				p.flag = flagHuman
				if rng.Intn(2) == 0 {
					p.flag = flagMozilla
				}
			} else if p.prio < prioApple {
				p.bare = rng.Intn(3) == 0
			}
			pats[i] = p
		}
		m := newMatcher(pats)
		for k := 0; k < 40; k++ {
			ua := word(0, 40)
			if got, want := m.Match(ua), naiveMatch(pats, ua); got != want {
				t.Fatalf("round %d: Match(%q) = %q; naive %q; patterns %+v", round, ua, got, want, pats)
			}
		}
	}
}

// TestMatchDoesNotAllocate checks that scanning allocates nothing for a hit and for a miss.
func TestMatchDoesNotAllocate(t *testing.T) {
	m := DefaultUAMatcher()
	for _, ua := range []string{benchUAs[0], benchBotUA, "Googlebot/2.1", strings.Repeat("A", 500)} {
		if n := testing.AllocsPerRun(100, func() { BotByUA(ua, m) }); n != 0 {
			t.Errorf("BotByUA(%.30q) allocates %v times", ua, n)
		}
	}
}
