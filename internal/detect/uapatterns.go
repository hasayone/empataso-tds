package detect

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed signature_ua_default.dat
var uaDefaultEmbed string

// searchEnginePatterns are the fixed search engine substrings. They are not
// part of the data file and cannot be disabled by signature_ua.dat.
var searchEnginePatterns = []pattern{
	{text: "baidu", prio: prioBaidu},
	{text: "bing", prio: prioBing},
	{text: "msnbot", prio: prioBing},
	{text: "google", prio: prioGoogle},
	{text: "mail.ru", prio: prioMail},
	{text: "yahoo", prio: prioYahoo},
	{text: "yandex.com/bots", prio: prioYandex},
	{text: "applebot", prio: prioApple},
}

// servicePatterns are the fixed flag patterns: "mozilla/" turns off bare
// signatures; the human tokens turn off search engine labels, because real
// people use the Bing, Baidu, Yahoo Japan and TikTok in-app browsers and games
// whose UA names the engine, the Google Play channel or "bingo".
var servicePatterns = []pattern{
	{text: "mozilla/", flag: flagMozilla},
	{text: "bingsapphire", flag: flagHuman},
	{text: "bingweb", flag: flagHuman},
	{text: "baiduboxapp", flag: flagHuman},
	{text: "baidubrowser", flag: flagHuman},
	{text: "bdbrowser", flag: flagHuman},
	{text: "googleplay", flag: flagHuman},
	{text: "yjapp", flag: flagHuman},
	{text: "jp.co.yahoo", flag: flagHuman},
	{text: "bingo", flag: flagHuman},
}

// sectionRule is the label priority and the bare flag of a section of the data file.
type sectionRule struct {
	prio uint8
	bare bool
}

// sectionRules maps the section names of signature_ua_default.dat to their rules.
var sectionRules = map[string]sectionRule{
	"headless":         {prio: prioHeadless},
	"http_client":      {prio: prioHTTPClient},
	"http_client_bare": {prio: prioHTTPClient, bare: true},
	"crawler":          {prio: prioCrawler},
	"scanner":          {prio: prioScanner},
}

// sigSection reports whether a trimmed line is a "[section]" header and returns the section name.
func sigSection(line string) (string, bool) {
	if len(line) >= 2 && line[0] == '[' && line[len(line)-1] == ']' {
		return strings.TrimSpace(line[1 : len(line)-1]), true
	}
	return "", false
}

// sigSkip reports whether a trimmed line carries no pattern: blank, a # comment or a [section] header.
func sigSkip(line string) bool {
	if line == "" || line[0] == '#' {
		return true
	}
	_, header := sigSection(line)
	return header
}

// parseUADefault parses the text of signature_ua_default.dat into patterns.
// Lines outside a known section are ignored. Texts are lowercased.
func parseUADefault(text string) []pattern {
	var out []pattern
	var rule sectionRule
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := sigSection(line); ok {
			rule = sectionRules[strings.ToLower(name)]
			continue
		}
		if sigSkip(line) || rule.prio == prioNone {
			continue
		}
		out = append(out, pattern{text: strings.ToLower(line), prio: rule.prio, bare: rule.bare})
	}
	return out
}

var (
	uaDefaultOnce     sync.Once
	uaDefaultPatterns []pattern
)

// defaultUAPatterns returns the built-in data file patterns, parsed once.
// The slice must not be mutated.
func defaultUAPatterns() []pattern {
	uaDefaultOnce.Do(func() { uaDefaultPatterns = parseUADefault(uaDefaultEmbed) })
	return uaDefaultPatterns
}

// buildUAMatcher builds the matcher for the fixed patterns, the built-in set
// without the patterns listed in disabled, and extra as sign_ua patterns.
// disabled keys and extra items must be lowercase; disabled may be nil.
// The result is never nil.
func buildUAMatcher(extra []string, disabled map[string]struct{}) *Matcher {
	defaults := defaultUAPatterns()
	pats := make([]pattern, 0, len(searchEnginePatterns)+len(servicePatterns)+len(defaults)+len(extra))
	pats = append(pats, searchEnginePatterns...)
	pats = append(pats, servicePatterns...)
	for _, p := range defaults {
		if _, off := disabled[p.text]; !off {
			pats = append(pats, p)
		}
	}
	for _, s := range extra {
		pats = append(pats, pattern{text: s, prio: prioSignUA})
	}
	return newMatcher(pats)
}

var (
	uaMatcherOnce sync.Once
	uaMatcher     *Matcher
)

// DefaultUAMatcher returns the matcher of the built-in signatures and the
// search engines, without any signature_ua.dat additions. It is built once,
// is never nil and is safe for concurrent use.
func DefaultUAMatcher() *Matcher {
	uaMatcherOnce.Do(func() { uaMatcher = buildUAMatcher(nil, nil) })
	return uaMatcher
}
