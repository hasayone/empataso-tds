package admin

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// checkerKeywordMaxRunes is the longest keyword the checker integration accepts.
	checkerKeywordMaxRunes = 100
	// checkerBundleMax is the longest bundle id the checker accepts.
	checkerBundleMax = 255
)

var (
	// checkerBundleLike matches a keyword candidate that is really a bundle id.
	checkerBundleLike = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9_]+)+$`)
	// checkerBundleToken matches one part of a campaign name that holds a bundle id.
	checkerBundleToken = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)
	// checkerBundleValid is the bundle id format of POST /api/apps.
	checkerBundleValid = regexp.MustCompile(`^[a-z0-9.]+$`)
)

// checkerKeywordFromName reads the campaign keyword out of a campaign name such as
// "❌ SpinDog - Play Cobra - com.playcobra.fasterthink".
// name is the campaign name. The leading characters that are neither a letter nor a digit
// (status emoji, spaces, "!") are dropped and the text up to the first " -" is the keyword,
// trimmed and with inner runs of spaces collapsed.
// It returns an empty string when the name has no " -" separator, or when the candidate looks like
// a bundle id or an address, holds ":" or a line break, or is longer than 100 runes.
func checkerKeywordFromName(name string) string {
	rest := strings.TrimLeftFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	end := strings.Index(rest, " -")
	if end < 0 {
		return ""
	}
	kw := strings.Join(strings.Fields(rest[:end]), " ")
	switch {
	case kw == "", checkerBundleLike.MatchString(kw):
		return ""
	case strings.HasPrefix(strings.ToLower(kw), "http"):
		return ""
	case strings.ContainsAny(kw, ":\r\n"):
		return ""
	case utf8.RuneCountInString(kw) > checkerKeywordMaxRunes:
		return ""
	}
	return kw
}

// checkerBundleFromName reads the bundle id out of a campaign name: the last part, split on " - ",
// that looks like a dotted package name such as "com.playcobra.fasterthink".
// name is the campaign name. A part that starts with "http" or holds "/" is no bundle id.
// It returns the id in lower case, or an empty string when no part qualifies.
func checkerBundleFromName(name string) string {
	parts := strings.Split(name, " - ")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if strings.HasPrefix(strings.ToLower(p), "http") || strings.Contains(p, "/") {
			continue
		}
		if checkerBundleToken.MatchString(p) {
			return strings.ToLower(p)
		}
	}
	return ""
}

// checkerKeywordProblem validates a keyword for the checker.
// kw is the trimmed keyword. It returns an empty string when the keyword is usable, otherwise
// the client message: the keyword may not hold ":" or a line break and has at most 100 runes.
// An empty keyword is not a problem here; the callers that require one check it themselves.
func checkerKeywordProblem(kw string) string {
	if strings.ContainsAny(kw, ":\r\n") {
		return "keyword must not contain a colon or a line break"
	}
	if !utf8.ValidString(kw) || utf8.RuneCountInString(kw) > checkerKeywordMaxRunes {
		return "keyword is too long"
	}
	return ""
}

// checkerBundleProblem validates a bundle id for the checker.
// bundle is the trimmed id. It returns an empty string when the id is usable, otherwise the
// client message: lower-case letters, digits and dots, at least one dot, at most 255 characters.
// An empty id is not a problem here; the callers that require one check it themselves.
func checkerBundleProblem(bundle string) string {
	if len(bundle) > checkerBundleMax || !checkerBundleValid.MatchString(bundle) || !strings.Contains(bundle, ".") {
		return "bundle_id must match ^[a-z0-9.]+$ and contain a dot"
	}
	return ""
}

// checkerAppName builds the application name "<number> - <keyword>".
// num is the campaign number; a campaign without a number gives the bare keyword.
func checkerAppName(num int, keyword string) string {
	if num <= 0 {
		return keyword
	}
	return fmt.Sprintf("%d - %s", num, keyword)
}

// checkerPlayURL returns the Google Play page of a bundle id, or an empty string for an empty id.
func checkerPlayURL(bundle string) string {
	if bundle == "" {
		return ""
	}
	return "https://play.google.com/store/apps/details?id=" + bundle
}
