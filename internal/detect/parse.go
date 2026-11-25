package detect

import (
	"strings"
	"unicode"

	ua "github.com/mileusna/useragent"
)

// Info holds the parsed client characteristics for filters and logs.
type Info struct {
	OS         string // Android, iOS, Windows, macOS, Linux, ...
	OSVersion  string // 13, 16.0, 10, ...
	Browser    string // Chrome, Safari, Firefox, YandexBrowser, ...
	BrowserVer string
	Brand      string // Apple, Samsung, Xiaomi, ... ("" for desktop/unknown)
	Model      string // SM-S918B, Pixel 8, ... ("" when the User-Agent does not name one)
	Device     string // computer/phone/tablet (category)
}

// Parse parses the User-Agent: OS/browser/versions — via mileusna/useragent,
// device category — via our Device(),
// brand — via the brandOf() heuristic.
// Results for recently seen User-Agents come from a bounded in-memory cache;
// Info holds only strings, so the returned value is the caller's own copy.
func Parse(uaStr string) Info {
	if info, ok := parsed.get(uaStr); ok {
		return info
	}
	info := parseUA(uaStr)
	parsed.put(uaStr, info)
	return info
}

// parseUA is the uncached parse behind Parse.
func parseUA(uaStr string) Info {
	p := ua.Parse(uaStr)
	return Info{
		OS:         p.OS,
		OSVersion:  p.OSVersion,
		Browser:    p.Name,
		BrowserVer: p.Version,
		Brand:      brandOf(uaStr, p.OS),
		Model:      modelOf(uaStr),
		Device:     Device(uaStr),
	}
}

// maxModelLen is the longest device model kept, in characters. A longer value
// is cut to this length.
const maxModelLen = 64

// nonModels are the values an Android User-Agent puts where a model belongs
// when the real one is hidden. Compared in lower case.
var nonModels = map[string]bool{
	"k": true, "android": true, "mobile": true, "tablet": true,
	"wv": true, "linux": true, "u": true, "unknown": true,
}

// modelOf extracts the device model from an Android User-Agent: the token
// after the "Android <version>" token of the first parenthesized group, as in
// "(Linux; Android 13; SM-S918B Build/TP1A)". Locale tokens ("ru-ru") are
// skipped. When that group is the app tuple
// "Android (sdk/release; dpi; resolution; maker; model; ...)", the model field
// of the tuple is used. A User-Agent without either shape, or whose model is
// hidden (the single letter K of reduced Chrome strings), returns "".
// A known token is replaced by one trade name (see tradeModel).
func modelOf(uaStr string) string {
	open := strings.IndexByte(uaStr, '(')
	if open < 0 {
		return ""
	}
	rest := uaStr[open+1:]
	if end := strings.IndexByte(rest, ')'); end >= 0 {
		rest = rest[:end]
	}
	parts := strings.Split(rest, ";")
	for i, p := range parts {
		if !strings.HasPrefix(strings.TrimSpace(p), "Android") {
			continue
		}
		for _, cand := range parts[i+1:] {
			if isLocaleToken(strings.TrimSpace(cand)) {
				continue
			}
			return cleanModel(cand)
		}
		return ""
	}
	return appDeviceModel(uaStr[:open], parts)
}

// appDeviceModel reads the model field of an Instagram or Facebook app
// User-Agent, "Android (sdk/release; dpi; resolution; maker; model; ...)".
//
// before is the text before the parenthesis and parts are the semicolon fields
// inside it. It returns "" when before does not mention Android or the fields
// are not that tuple. The model is passed through cleanModel.
func appDeviceModel(before string, parts []string) string {
	if len(parts) < 5 || !strings.Contains(strings.ToLower(before), "android") {
		return ""
	}
	head := strings.TrimSpace(parts[0])
	if head == "" || head[0] < '0' || head[0] > '9' || !strings.Contains(head, "/") {
		return ""
	}
	if !strings.Contains(strings.ToLower(parts[1]), "dpi") {
		return ""
	}
	return cleanModel(parts[4])
}

// isLocaleToken reports whether s looks like a locale such as "ru", "ru-ru"
// or "en-US", which old Android User-Agents place before the model.
func isLocaleToken(s string) bool {
	if len(s) != 2 && len(s) != 5 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case len(s) == 5 && i == 2:
			if c != '-' && c != '_' {
				return false
			}
		case c|0x20 < 'a' || c|0x20 > 'z':
			return false
		}
	}
	return true
}

// cleanModel normalizes a model string: trims spaces and quotes, cuts a
// " Build/..." suffix, drops control characters and invalid UTF-8, and cuts the
// result to maxModelLen characters. A known token is then replaced by its trade
// name. It returns "" for an empty result and for the placeholders in nonModels.
func cleanModel(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	for _, marker := range []string{" Build/", " build/"} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxModelLen {
		s = strings.TrimSpace(string(r[:maxModelLen]))
	}
	if s == "" || nonModels[strings.ToLower(s)] {
		return ""
	}
	if name := tradeModel(s); name != "" {
		return name
	}
	return s
}

// HintModel normalizes the Sec-CH-UA-Model request header. hint is the raw
// header value, usually quoted ("Pixel 8"). It returns "" for a missing or
// empty header and for a value that is not a plausible model.
func HintModel(hint string) string {
	return cleanModel(hint)
}

// brandPatterns maps UA tokens → brand (order matters: specific before general).
var brandPatterns = []struct{ token, brand string }{
	{"honor", "Honor"},
	{"huawei", "Huawei"},
	{"redmi", "Xiaomi"}, {"poco", "Xiaomi"}, {"xiaomi", "Xiaomi"},
	{"oneplus", "OnePlus"},
	{"realme", "Realme"}, {"rmx", "Realme"},
	{"oppo", "Oppo"}, {"cph", "Oppo"},
	{"vivo", "Vivo"},
	{"pixel", "Google"},
	{"nokia", "Nokia"},
	{"xperia", "Sony"}, {"sony", "Sony"},
	{"motorola", "Motorola"}, {"moto ", "Motorola"},
	{"samsung", "Samsung"}, {"sm-", "Samsung"}, {"gt-", "Samsung"}, {"galaxy", "Samsung"},
	{"lg-", "LG"}, {"lm-", "LG"}, {"nexus", "Google"},
}

// brandOf determines the device brand. Apple — by OS (iOS/macOS) and UA.
func brandOf(uaStr, os string) string {
	l := strings.ToLower(uaStr)
	if os == "iOS" || os == "macOS" || strings.Contains(l, "iphone") ||
		strings.Contains(l, "ipad") || strings.Contains(l, "ipod") || strings.Contains(l, "macintosh") {
		return "Apple"
	}
	for _, p := range brandPatterns {
		if strings.Contains(l, p.token) {
			return p.brand
		}
	}
	return ""
}
