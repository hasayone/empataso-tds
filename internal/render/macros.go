// Package render expands output macros and builds the HTTP response based on the
// redirect type and rand macros.
package render

import (
	"math/rand"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// MacroDeps — data for expanding output macros.
type MacroDeps struct {
	Key       string   // [KEY] (url-encoded)
	Path      string   // [PATH]
	IP        string   // [IP]
	Country   string   // [COUNTRY], [()COUNTRY()]
	City      string   // [CITY], [()CITY()]
	Region    string   // [REGION]
	ASN       string   // [ASN] — the number, "" when unknown
	Org       string   // [ORG] (url-encoded: free text from the database)
	Timezone  string   // [TIMEZONE] — IANA name
	TZOffset  string   // [UTC] — current offset, "+3"
	Lang      string   // [LANG]
	Device    string   // [DEVICE]
	Operator  string   // [OPERATOR]
	Domain    string   // [DOMAIN]
	UserAgent string   // [USERAGENT]
	CID       string   // [CID] and {subid}
	RootCID   string   // {root_subid}; empty when the current click is the chain root
	Origin    string   // {origin} — signed first-entry campaign token, url-encoded when expanded
	Pars      []string // [PAR-1]..[PAR-5]
	SubIDs    [config.MaxSubID]string
	Status    string // {status}
	Payout    string // {payout}
	Offer     string // {offer} — offer-step path for this click
	DataDir   string // .dat directory for [RANDLINE]/[RANDDFL]
	Rng       *rand.Rand
	// Query reads one click URL parameter. name is the parameter.
	// The string is its raw value and the bool is false when it is absent
	// or empty. A nil Query leaves unknown {name} placeholders untouched.
	Query func(name string) (string, bool)
}

// The RAND* macros are anchored: Expand matches them at the current position
// rather than scanning the whole string, so a value substituted for a scalar
// macro is never re-examined for macros of its own.
var (
	reRandNum  = regexp.MustCompile(`^\[RANDNUM-([0-9]+)-([0-9]+)\]`)
	reRandStr  = regexp.MustCompile(`^\[RANDSTR-\((.+?)\)-([0-9]+)\]`)
	reRandLine = regexp.MustCompile(`^\[RANDLINE-\((.+?)\)-([0-9]+)(/u)?\]`)
	reRandDfl  = regexp.MustCompile(`^\[RANDDFL-\((.+?)\)-([0-9]+)(/u)?\]`)
)

// Expand expands all supported macros in the out string.
//
// Expansion is a single left-to-right pass and substituted values are written
// out verbatim, never rescanned. That matters because several macros carry
// visitor-controlled data ([USERAGENT], [DOMAIN], [PAR-n], [PATH], [LANG]): if
// the result were scanned again, a visitor could put macros of their own into a
// header and have the engine execute them — reading files through
// [RANDLINE-(../...)] or forcing a huge allocation through [RANDSTR].
//
// Macros nested inside a RAND* argument still work, because the argument comes
// from the template itself; only its scalar macros are expanded, never RAND*
// ones (see expandScalars), so a visitor cannot introduce a macro.
//
// A visitor can still influence such an argument, though: a template that says
// [RANDLINE-([PATH].dat)-1] puts the Host header into a file name. underDir is
// what keeps that inside DataDir, and it is load-bearing, not belt-and-braces.
func Expand(s string, d MacroDeps) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '{' {
			val, n := d.curlyAt(s[i:])
			if n == 0 {
				b.WriteByte('{')
				i++
				continue
			}
			b.WriteString(val)
			i += n
			continue
		}
		if s[i] != '[' {
			b.WriteByte(s[i])
			i++
			continue
		}
		val, n := d.macroAt(s[i:])
		if n == 0 {
			b.WriteByte('[')
			i++
			continue
		}
		b.WriteString(val)
		i += n
	}
	return b.String()
}

// macroAt matches a single macro at the start of s. It returns the replacement
// and the number of bytes consumed, or n == 0 when s does not start with a
// macro.
func (d MacroDeps) macroAt(s string) (val string, n int) {
	for _, m := range d.scalars() {
		if strings.HasPrefix(s, m.name) {
			return m.val, len(m.name)
		}
	}
	// Only the (…) arguments can hold a nested macro — the counts and the
	// RANDNUM bounds are [0-9]+ in the patterns, so expanding scalars there
	// would be dead code.
	if g, n := matchRand(reRandNum, s); n > 0 {
		return randNum(g[1], g[2], d.Rng), n
	}
	if g, n := matchRand(reRandStr, s); n > 0 {
		return randStr(d.expandScalars(g[1]), g[2], d.Rng), n
	}
	if g, n := matchRand(reRandLine, s); n > 0 {
		return d.randLine(d.expandScalars(g[1]), g[2], g[3] == "/u"), n
	}
	if g, n := matchRand(reRandDfl, s); n > 0 {
		return d.randDfl(d.expandScalars(g[1]), g[2], g[3] == "/u"), n
	}
	return "", 0
}

type scalarMacro struct{ name, val string }

// scalars lists the plain name → value macros. [()COUNTRY()] and [()CITY()]
// come first: they are longer literals, and prefix matching takes the first hit.
func (d MacroDeps) scalars() []scalarMacro {
	out := []scalarMacro{
		{"[()COUNTRY()]", d.Country},
		{"[()CITY()]", d.City},
		{"[KEY]", url.QueryEscape(d.Key)},
		{"[PATH]", d.Path},
		{"[IP]", d.IP},
		{"[COUNTRY]", d.Country},
		{"[CITY]", d.City},
		{"[REGION]", d.Region},
		{"[ASN]", d.ASN},
		{"[ORG]", url.QueryEscape(d.Org)},
		{"[TIMEZONE]", d.Timezone},
		{"[UTC]", d.TZOffset},
		{"[LANG]", d.Lang},
		{"[DEVICE]", d.Device},
		{"[OPERATOR]", d.Operator},
		{"[DOMAIN]", d.Domain},
		{"[USERAGENT]", d.UserAgent},
		{"[CID]", d.CID},
	}
	for i := 0; i < 5; i++ {
		v := ""
		if i < len(d.Pars) {
			v = d.Pars[i]
		}
		out = append(out, scalarMacro{"[PAR-" + strconv.Itoa(i+1) + "]", v})
	}
	return out
}

// curlyAt expands a Keitaro placeholder at the start of s.
//
// s is the template suffix starting at '{'. val is the replacement and n is
// the number of bytes consumed, including both braces. n is 0 when the braces
// are not a macro, and there is no error return. The closing brace must sit
// at index 24 or earlier, the same window as before.
//
// subid, root_subid, country, keyword, status, payout, offer and
// sub_id_1..sub_id_30 are filled from MacroDeps and are never read from the click
// query. Any other name of [A-Za-z0-9_-] is looked up in Query. A nil Query, a
// missing parameter, or a name outside that set (a dot, sub_id_31, an HTML brace
// like {color}) is left untouched.
func (d MacroDeps) curlyAt(s string) (val string, n int) {
	end := strings.IndexByte(s, '}')
	if end <= 1 || end > 24 {
		return "", 0
	}
	name := s[1:end]
	switch name {
	case "subid":
		return url.QueryEscape(d.CID), end + 1
	case "root_subid":
		rootCID := d.RootCID
		if rootCID == "" {
			rootCID = d.CID
		}
		return url.QueryEscape(rootCID), end + 1
	case "country":
		return d.Country, end + 1
	case "keyword":
		return url.QueryEscape(d.Key), end + 1
	case "status":
		return url.QueryEscape(d.Status), end + 1
	case "payout":
		return url.QueryEscape(d.Payout), end + 1
	case "offer":
		return d.Offer, end + 1
	case "origin":
		return url.QueryEscape(d.Origin), end + 1
	default:
		if idx, ok := subIDIndex(name); ok {
			return url.QueryEscape(d.subID(idx)), end + 1
		}
		if d.Query == nil || !queryMacroName(name) {
			return "", 0
		}
		v, ok := d.Query(name)
		if !ok {
			return "", 0
		}
		return url.QueryEscape(v), end + 1
	}
}

// queryMacroName reports whether name is a click-query macro.
//
// name is the text between curly braces. The result is true only when name
// has 1..24 characters and each one is an ASCII letter, digit, '_' or '-'.
// Anything else is ordinary text. There is no error return.
func queryMacroName(name string) bool {
	if len(name) < 1 || len(name) > 24 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// subIDIndex parses sub_id_1 .. sub_id_30 into a zero-based index.
func subIDIndex(name string) (int, bool) {
	n, ok := strings.CutPrefix(name, "sub_id_")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(n)
	if err != nil || i < 1 || i > config.MaxSubID {
		return 0, false
	}
	return i - 1, true
}

// subID returns the named parameter. The first five fall back to Pars only
// when the named slot is empty, so an explicit sub_id_6 is never stored there.
func (d MacroDeps) subID(i int) string {
	if i >= 0 && i < len(d.SubIDs) && d.SubIDs[i] != "" {
		return d.SubIDs[i]
	}
	if i >= 0 && i < 5 && i < len(d.Pars) {
		return d.Pars[i]
	}
	return ""
}

// expandScalars replaces only the scalar macros, used for RAND* arguments taken
// from the template (e.g. [RANDLINE-([COUNTRY].dat)-1]). It does not recurse
// into RAND* macros, so it cannot be driven by a substituted value.
func (d MacroDeps) expandScalars(s string) string {
	if !strings.Contains(s, "[") {
		return s
	}
	for _, m := range d.scalars() {
		if strings.Contains(s, m.name) {
			s = strings.ReplaceAll(s, m.name, m.val)
		}
	}
	return s
}

// matchRand matches an anchored RAND* pattern at the start of s.
func matchRand(re *regexp.Regexp, s string) (groups []string, n int) {
	g := re.FindStringSubmatch(s)
	if g == nil {
		return nil, 0
	}
	return g, len(g[0])
}

func intn(rng *rand.Rand, n int) int {
	if n <= 0 {
		return 0
	}
	if rng != nil {
		return rng.Intn(n)
	}
	return rand.Intn(n)
}

// [RANDNUM-min-max] → random integer in [min, max].
func randNum(minRaw, maxRaw string, rng *rand.Rand) string {
	min, _ := strconv.Atoi(minRaw)
	max, _ := strconv.Atoi(maxRaw)
	if max < min {
		min, max = max, min
	}
	return strconv.Itoa(min + intn(rng, max-min+1))
}

// [RANDSTR-(charset)-count] → random string of length count from charset.
func randStr(set, countRaw string, rng *rand.Rand) string {
	count, _ := strconv.Atoi(countRaw)
	if set == "" || count <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < count; i++ {
		b.WriteByte(set[intn(rng, len(set))])
	}
	return b.String()
}

// [RANDLINE-(file)-count(/u)] → count random lines from DataDir/file joined by ';'.
// The /u suffix means no repeats. A nonexistent file → empty substitution.
func (d MacroDeps) randLine(file, countRaw string, unique bool) string {
	count, _ := strconv.Atoi(countRaw)
	path, ok := underDir(d.DataDir, file)
	if !ok {
		return ""
	}
	return pickLines(lineFiles.get(path), count, unique, d.Rng)
}

// [RANDDFL-(dir)-count(/u)] → picks a random file in DataDir/dir and takes
// count random lines from it. /u means no repeats.
func (d MacroDeps) randDfl(sub, countRaw string, unique bool) string {
	count, _ := strconv.Atoi(countRaw)
	dir, ok := underDir(d.DataDir, sub)
	if !ok || count <= 0 {
		return ""
	}
	files := poolDirs.get(dir)
	if len(files) == 0 {
		return ""
	}
	return pickLines(lineFiles.get(filepath.Join(dir, files[intn(d.Rng, len(files))])), count, unique, d.Rng)
}

// pickLines takes count random lines joined by ';' (no repeats when unique).
func pickLines(lines []string, count int, unique bool, rng *rand.Rand) string {
	if len(lines) == 0 || count <= 0 {
		return ""
	}
	if unique && count > len(lines) {
		count = len(lines)
	}
	var picked []string
	used := map[int]bool{}
	for len(picked) < count {
		i := intn(rng, len(lines))
		if unique {
			if used[i] {
				continue
			}
			used[i] = true
		}
		picked = append(picked, lines[i])
	}
	return strings.Join(picked, ";")
}

// underDir resolves rel against base and refuses anything that escapes base.
// Without it "[RANDLINE-(../../etc/passwd)-1]" would read outside the data
// directory and put the result straight into the visitor's redirect.
//
// The check is lexical: it works on the cleaned path, not on what the
// filesystem resolves to. A symlink inside DataDir pointing outside it still
// passes. That is deliberate — DataDir is operator-owned, and resolving every
// path on the hot path costs a syscall per macro — but it means underDir bounds
// the name, not the destination.
func underDir(base, rel string) (string, bool) {
	if base == "" || rel == "" {
		return "", false
	}
	if filepath.IsAbs(rel) {
		return "", false
	}
	clean := filepath.Clean(base)
	p := filepath.Join(clean, rel)
	if p != clean && !strings.HasPrefix(p, clean+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}
