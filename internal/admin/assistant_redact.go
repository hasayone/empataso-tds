package admin

import (
	"bytes"
	"encoding/json"
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// aiRedactMark replaces a secret value in everything the assistant shows to the model
	// or keeps in a chat.
	aiRedactMark = "***"

	// aiTokenSegmentMin is the shortest URL path segment that can be taken for a token.
	aiTokenSegmentMin = 24

	// aiTokenEntropyMin is the Shannon entropy in bits per character from which a long path
	// segment counts as a token.
	aiTokenEntropyMin = 3.5

	// aiIPv6MaskBits is the prefix kept of an IPv6 address; an IPv4 address keeps /24.
	aiIPv6MaskBits = 48
)

// aiSecretKeys lists, in lower case, the exact JSON keys whose value is never shown. The match is
// exact on purpose: a substring rule would hide key_text, save_keys and cost_token.
var aiSecretKeys = map[string]bool{
	"token": true, "api_key": true, "apikey": true, "password": true, "passwd": true,
	"secret": true, "client_secret": true, "authorization": true, "cookie": true, "csrf": true,
	"hash": true, "pw_fp": true, "access_token": true, "refresh_token": true, "private_key": true,
	"key": true, "pass": true, "pwd": true, "x-api-key": true, "api-key": true, "postback_key": true,
}

// aiCountKey is the field that marks a {key, count} breakdown row: there "key" is the value of a
// dimension, not a credential, and stays visible.
const aiCountKey = "count"

// aiTruncMark is the ellipsis that aiCompress puts at the end of a shortened string. A proposal
// that carries it would write the cut text over the real one.
const aiTruncMark = "…"

// aiShortenFloor is the shortest cut aiCompress makes of a long string, in bytes. A string it
// shortened holds at least aiShortenFloor-3 bytes of text (a cut moves back over at most three
// bytes of a rune) and the mark, so it is at least aiShortenFloor bytes long. A shorter string that
// ends in the mark is the user's own text.
const aiShortenFloor = 64

// aiIsShortened reports whether s looks like a string aiCompress shortened: it ends in aiTruncMark
// and is at least aiShortenFloor bytes long.
func aiIsShortened(s string) bool {
	return len(s) >= aiShortenFloor && strings.HasSuffix(s, aiTruncMark)
}

// aiSecretQueryKeys lists, in lower case, the URL query parameters whose value is never shown:
// every secret key and the usual signature names.
var aiSecretQueryKeys = buildAISecretQueryKeys()

// buildAISecretQueryKeys returns aiSecretKeys plus sig, signature, auth and key.
func buildAISecretQueryKeys() map[string]bool {
	out := map[string]bool{"sig": true, "signature": true, "auth": true, "key": true}
	for k := range aiSecretKeys {
		out[k] = true
	}
	return out
}

// aiURLKeys lists, in lower case, the journal keys that hold a link of a visitor: a referrer, a
// landing, a redirect. When the journal option is on, the query of these links is cut because it
// carries click ids and sub tags.
var aiURLKeys = map[string]bool{
	"referrer": true, "referer": true, "url": true, "landing_url": true,
	"redirect": true, "out": true, "page": true,
}

var (
	// aiURLPattern finds a link inside a text.
	aiURLPattern = regexp.MustCompile("https?://[^\\s\"'<>`]+")

	// aiKeyFormats are the provider key formats: sk-, sk-ant-, sk-or-, AIza, GitHub tokens.
	aiKeyFormats = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]{10,}`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
	}

	// aiBearerPattern finds a Bearer credential.
	aiBearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)

	// aiIPCandidate finds a run that can be an IP address; the candidate is checked by netip.
	aiIPCandidate = regexp.MustCompile(`[0-9A-Fa-f:.]+`)

	// aiIPv4Mask and aiIPv6Mask find a masked address made by aiMaskIP.
	aiIPv4Mask = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.x\b`)
	aiIPv6Mask = regexp.MustCompile(`::/48\b`)
)

// aiRedactor applies the redaction rules with one set of literal secrets.
//
// secrets are the values to cut out of any text, longest first. logURLs turns on the journal
// rules: the query of every link is cut, and a value under a link key loses its query.
type aiRedactor struct {
	secrets []string
	logURLs bool
}

// newAIRedactor returns a redactor for secrets. Duplicates and values shorter than
// aiSecretMinLen are dropped. logURLs selects the journal rules.
func newAIRedactor(secrets []string, logURLs bool) *aiRedactor {
	seen := map[string]bool{}
	var list []string
	for _, s := range secrets {
		if len(s) < aiSecretMinLen || seen[s] {
			continue
		}
		seen[s] = true
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	return &aiRedactor{secrets: list, logURLs: logURLs}
}

// aiRedactWith returns a copy of v without secrets. v is a decoded JSON value or any value that
// encodes to JSON. secrets are the literal values to cut out of every string.
func aiRedactWith(v any, secrets []string) any {
	return newAIRedactor(secrets, false).value(v)
}

// aiRedact returns a copy of v without secrets: the pattern rules plus every literal value of
// processSecrets. Use it before anything goes to the model or into a chat.
func (s *Server) aiRedact(v any) any {
	return aiRedactWith(v, s.processSecrets())
}

// aiRedactText cuts secrets out of one text: the literal values in secrets, provider key
// formats, Bearer credentials, link userinfo, secret query parameters and token-like path
// segments. It never fails and returns s unchanged when nothing matches.
func aiRedactText(s string, secrets []string) string {
	return newAIRedactor(secrets, false).text(s)
}

// value returns a redacted copy of v. A value that is not a decoded JSON tree is encoded and
// decoded first; one that cannot be encoded is replaced by the mark.
func (r *aiRedactor) value(v any) any {
	switch x := v.(type) {
	case nil, bool, json.Number, float64, float32, int, int64, int32, uint, uint64, uint32:
		return x
	case string:
		return r.str(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.value(e)
		}
		return out
	case map[string]any:
		return r.object(x)
	case json.RawMessage:
		return r.value(aiDecode(x))
	case []byte:
		return r.value(aiDecode(x))
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return aiRedactMark
	}
	return r.value(aiDecode(raw))
}

// aiDecode decodes JSON keeping numbers exact. Invalid JSON is returned as a string.
func aiDecode(raw []byte) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return string(raw)
	}
	return out
}

// object returns a redacted copy of m. The key decides first: a secret key hides its value, an IP
// key masks the addresses in it, a link key under the journal rules loses its query.
func (r *aiRedactor) object(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	_, isRow := m[aiCountKey]
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		lk := strings.ToLower(k)
		name := aiMaskIPs(r.text(k))
		var shown any
		switch {
		case aiSecretKeys[lk] && aiSecretValue(v) && !(isRow && lk == "key"):
			shown = aiRedactMark
		case aiIPKey(lk):
			shown = r.ipValue(v)
		case r.logURLs && aiURLKeys[lk]:
			shown = r.linkValue(v)
		default:
			shown = r.value(v)
		}
		aiPutKey(out, name, shown)
	}
	return out
}

// aiPutKey stores v under name in out. Masking can give two keys one name, for example two
// addresses of one /24: numbers are added, anything else goes under "name #2", "name #3".
func aiPutKey(out map[string]any, name string, v any) {
	prev, taken := out[name]
	if !taken {
		out[name] = v
		return
	}
	if sum, ok := aiAddNumbers(prev, v); ok {
		out[name] = sum
		return
	}
	for i := 2; ; i++ {
		alt := name + " #" + strconv.Itoa(i)
		if _, used := out[alt]; !used {
			out[alt] = v
			return
		}
	}
}

// aiAddNumbers returns a+b when both are integers of one kind (json.Number or float64).
func aiAddNumbers(a, b any) (any, bool) {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return nil, false
		}
		xi, errX := x.Int64()
		yi, errY := y.Int64()
		if errX != nil || errY != nil {
			return nil, false
		}
		return json.Number(strconv.FormatInt(xi+yi, 10)), true
	case float64:
		if y, ok := b.(float64); ok {
			return x + y, true
		}
	}
	return nil, false
}

// aiSecretValue reports whether v is a value worth hiding: not nil, not empty, not a flag.
func aiSecretValue(v any) bool {
	switch x := v.(type) {
	case nil, bool:
		return false
	case string:
		return x != ""
	}
	return true
}

// aiIPKey reports whether the lower-case key names a visitor address: ip, or ending in _ip.
func aiIPKey(lk string) bool {
	return lk == "ip" || strings.HasSuffix(lk, "_ip")
}

// ipValue masks every address inside the strings of v, which is the value of an IP key.
func (r *aiRedactor) ipValue(v any) any {
	switch x := v.(type) {
	case string:
		return aiMaskIPs(r.text(x))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.ipValue(e)
		}
		return out
	}
	return r.value(v)
}

// linkValue redacts the strings of v, which is the value of a journal link key, and cuts the
// query and fragment of each.
func (r *aiRedactor) linkValue(v any) any {
	switch x := v.(type) {
	case string:
		return aiMaskIPs(aiCutQuery(r.text(x)))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.linkValue(e)
		}
		return out
	}
	return r.value(v)
}

// aiCutQuery drops everything from the first ? or # of s.
func aiCutQuery(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// str redacts one string: the text rules, then every IP address inside it is masked.
func (r *aiRedactor) str(s string) string {
	return aiMaskIPs(r.text(s))
}

// text applies the literal secrets, the links, the key formats and the Bearer rule to s.
func (r *aiRedactor) text(s string) string {
	if s == "" {
		return s
	}
	for _, sec := range r.secrets {
		if strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, aiRedactMark)
		}
	}
	if strings.Contains(s, "://") {
		s = aiURLPattern.ReplaceAllStringFunc(s, r.link)
	}
	for _, re := range aiKeyFormats {
		s = re.ReplaceAllString(s, aiRedactMark)
	}
	return aiBearerPattern.ReplaceAllString(s, "Bearer "+aiRedactMark)
}

// link redacts one http or https link: userinfo, token-like path segments, secret query
// parameters and a secret fragment. Under the journal rules the query and the fragment go.
// Macros such as {subid} outside secret parameters stay as they are.
func (r *aiRedactor) link(u string) string {
	trail := ""
	for len(u) > 0 && strings.ContainsRune(".,;", rune(u[len(u)-1])) {
		trail = u[len(u)-1:] + trail
		u = u[:len(u)-1]
	}
	schemeEnd := strings.Index(u, "://") + len("://")
	scheme, rest := u[:schemeEnd], u[schemeEnd:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = aiRedactMark + "@" + authority[at+1:]
	}
	fragment := ""
	if i := strings.IndexByte(tail, '#'); i >= 0 {
		fragment, tail = tail[i+1:], tail[:i]
	}
	query, hasQuery := "", false
	if i := strings.IndexByte(tail, '?'); i >= 0 {
		query, hasQuery, tail = tail[i+1:], true, tail[:i]
	}
	var b strings.Builder
	b.WriteString(scheme + authority + aiRedactPath(tail))
	if hasQuery && !r.logURLs {
		b.WriteString("?" + aiRedactQuery(query))
	}
	if fragment != "" && !r.logURLs {
		b.WriteString("#" + aiRedactQuery(fragment))
	}
	return b.String() + trail
}

// aiRedactQuery hides the value of every secret parameter of a raw query. A part without = and
// every other parameter stay verbatim, so macros keep working.
func aiRedactQuery(q string) string {
	parts := strings.Split(q, "&")
	for i, p := range parts {
		name, _, ok := strings.Cut(p, "=")
		if ok && aiSecretQueryKeys[strings.ToLower(name)] {
			parts[i] = name + "=" + aiRedactMark
		}
	}
	return strings.Join(parts, "&")
}

// aiRedactPath replaces every token-like segment of a URL path with the mark.
func aiRedactPath(p string) string {
	if p == "" {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if aiLooksLikeToken(s) {
			segs[i] = aiRedactMark
		}
	}
	return strings.Join(segs, "/")
}

// aiLooksLikeToken reports whether a path segment is a credential: at least aiTokenSegmentMin
// characters from the token alphabet, mixing letters with digits or upper with lower case, and
// an entropy of at least aiTokenEntropyMin bits per character. A plain word slug fails the mix
// or the entropy test.
func aiLooksLikeToken(seg string) bool {
	if len(seg) < aiTokenSegmentMin {
		return false
	}
	var digit, upper, lower bool
	counts := map[byte]int{}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c == '-' || c == '_' || c == '.' || c == '=':
		default:
			return false
		}
		counts[c]++
	}
	if !(digit && (upper || lower)) && !(upper && lower) {
		return false
	}
	var h float64
	n := float64(len(seg))
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h >= aiTokenEntropyMin
}

// aiParseIP parses a whole string as an IP address without a zone.
func aiParseIP(s string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.WithZone(""), true
}

// aiMaskIP returns the visible form of a visitor address: an IPv4 address becomes a.b.c.x and an
// IPv6 address becomes its /48 prefix, for example 2001:db8:abcd::/48. An IPv4-mapped IPv6
// address is masked as IPv4.
func aiMaskIP(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is4() {
		b := ip.As4()
		return strconv.Itoa(int(b[0])) + "." + strconv.Itoa(int(b[1])) + "." + strconv.Itoa(int(b[2])) + ".x"
	}
	return netip.PrefixFrom(ip, aiIPv6MaskBits).Masked().String()
}

// aiMaskIPs masks every IP address found in text, whatever the surrounding words are. Text that
// only looks like an address is left alone: a version with fewer than four parts, a date, a time,
// a version of a product token such as Chrome/120.0.0.0, the unspecified address, and a candidate
// glued to letters as in std::string. An address followed by a port keeps its port, and an
// address that is already masked stays as it is.
func aiMaskIPs(text string) string {
	locs := aiIPCandidate.FindAllStringIndex(text, -1)
	if locs == nil {
		return text
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		b.WriteString(text[last:l[0]])
		b.WriteString(aiMaskCandidate(text, l[0], l[1]))
		last = l[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// aiMaskCandidate returns the replacement of text[start:end], a run of address characters. The
// whole run is tried first, then the run without its trailing dots and colons, so "see 1.2.3.4."
// finds the address while "2001:db8::" is still one. A run that is no address comes back unchanged.
func aiMaskCandidate(text string, start, end int) string {
	c := text[start:end]
	if !strings.ContainsAny(c, ":.") {
		return c
	}
	if out, ok := aiMaskRun(text, start, c, ""); ok {
		return out
	}
	if core := strings.TrimRight(c, ".:"); core != c {
		if out, ok := aiMaskRun(text, start, core, c[len(core):]); ok {
			return out
		}
	}
	return c
}

// aiMaskRun masks the address in core, which starts at text[start]. Leading dots and colons are
// tried off one by one, so "ip:1.2.3.4" finds the address. tail is appended unchanged. ok is false
// when core holds no address to mask.
func aiMaskRun(text string, start int, core, tail string) (string, bool) {
	lead := len(core) - len(strings.TrimLeft(core, ".:"))
	for i := 0; i <= lead && i < len(core); i++ {
		sub := core[i:]
		ip, err := netip.ParseAddr(sub)
		port := ""
		if err != nil {
			ap, perr := netip.ParseAddrPort(sub)
			if perr != nil {
				continue
			}
			ip, port = ap.Addr(), sub[strings.LastIndexByte(sub, ':'):]
		}
		ip = ip.WithZone("")
		coreEnd := start + len(core)
		if ip.IsUnspecified() || aiIPGlued(text, start+i, coreEnd) {
			return "", false
		}
		mask := aiMaskIP(ip)
		if port == "" && strings.HasPrefix(mask, sub) && strings.HasPrefix(text[coreEnd:], mask[len(sub):]) {
			return "", false
		}
		return core[:i] + mask + port + tail, true
	}
	return "", false
}

// aiIPGlued reports whether the candidate text[start:end] is a part of a longer word or a product
// version rather than an address: a letter right before or after it, or a product token and a
// slash before it, as in "Chrome/120.0.0.0".
func aiIPGlued(text string, start, end int) bool {
	if start > 0 && aiIsASCIILetter(text[start-1]) {
		return true
	}
	if end < len(text) && aiIsASCIILetter(text[end]) {
		return true
	}
	if start < 2 || text[start-1] != '/' || !aiIsASCIILetter(text[start-2]) {
		return false
	}
	i := start - 2
	for i >= 0 && aiIsASCIILetter(text[i]) {
		i--
	}
	return i < 0 || text[i] == ' ' || text[i] == '('
}

// aiIsASCIILetter reports whether c is an ASCII letter.
func aiIsASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// aiTruncatedMap reports whether m carries the marks of aiCompress: truncated=true next to a
// "total" or "<key>_total" count of the rows that were cut off.
func aiTruncatedMap(m map[string]any) bool {
	if cut, _ := m["truncated"].(bool); !cut {
		return false
	}
	for k := range m {
		if k == "total" || strings.HasSuffix(k, "_total") {
			return true
		}
	}
	return false
}

// aiHasRedactMark reports whether v holds data the model got in a reduced form: a string with the
// redaction mark, a masked IP address (a.b.c.x or an IPv6 /48 prefix), the ellipsis aiCompress
// puts at the end of a shortened string (see aiIsShortened), or an object marked truncated with
// its total. v is a decoded JSON value, raw JSON or any value that encodes to JSON. A proposal that carries such a
// value would write the reduced data over the real one. Three plain dots are not a mark.
func aiHasRedactMark(v any) bool {
	switch x := v.(type) {
	case nil, bool, json.Number, float64, int, int64:
		return false
	case string:
		return strings.Contains(x, aiRedactMark) || aiIsShortened(x) ||
			aiIPv4Mask.MatchString(x) || aiIPv6Mask.MatchString(x)
	case []string:
		for _, e := range x {
			if aiHasRedactMark(e) {
				return true
			}
		}
		return false
	case []any:
		for _, e := range x {
			if aiHasRedactMark(e) {
				return true
			}
		}
		return false
	case map[string]any:
		if aiTruncatedMap(x) {
			return true
		}
		for k, e := range x {
			if aiHasRedactMark(k) || aiHasRedactMark(e) {
				return true
			}
		}
		return false
	case json.RawMessage:
		return aiHasRedactMark(aiDecode(x))
	case []byte:
		return aiHasRedactMark(aiDecode(x))
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return aiHasRedactMark(aiDecode(raw))
}

// aiRedactContext returns c with secrets cut out and visitor addresses masked, for the page
// context the panel sends. The filters ("field:op:value") keep their field and operator; the value
// of a secret field is hidden and any address in a value is masked.
func aiRedactContext(c aiPageContext) aiPageContext {
	return aiRedactContextWith(c, nil)
}

// aiRedactContextWith is aiRedactContext that also cuts the literal secrets out of every field.
func aiRedactContextWith(c aiPageContext, secrets []string) aiPageContext {
	r := newAIRedactor(secrets, false)
	plain := func(s string) string { return aiMaskIPs(r.text(s)) }
	out := c
	out.Page, out.Arg, out.Board = plain(c.Page), plain(c.Arg), plain(c.Board)
	out.Tab, out.Campaign = plain(c.Tab), plain(c.Campaign)
	out.Filters = nil
	for _, f := range c.Filters {
		out.Filters = append(out.Filters, r.filter(f))
	}
	return out
}

// filter redacts one "field:op:value" condition. A condition on a secret field keeps field and
// operator and hides the value; any other value goes through the text rules and IP masking.
func (r *aiRedactor) filter(f string) string {
	parts := strings.SplitN(f, ":", 3)
	if len(parts) == 3 {
		if aiSecretKeys[strings.ToLower(strings.TrimSpace(parts[0]))] {
			return parts[0] + ":" + parts[1] + ":" + aiRedactMark
		}
		return parts[0] + ":" + parts[1] + ":" + aiMaskIPs(r.text(parts[2]))
	}
	return aiMaskIPs(r.text(f))
}
