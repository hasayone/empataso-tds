package detect

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"time"
	"unicode"
)

// FileBotSignatures is the name of the bot signature database in the data
// directory. The cron job writes it and the engine reads it.
const FileBotSignatures = "bot_signatures.json"

const (
	// botDBVersion is the format version of FileBotSignatures.
	botDBVersion = 1
	// MaxBotRules caps the number of rules in one database.
	MaxBotRules = 20000
	// MaxBotPatternBytes is the longest source pattern accepted.
	MaxBotPatternBytes = 512
	// minAnchorBytes is the shortest literal a rule must contain: a rule without
	// one would have to run its expression on every click.
	minAnchorBytes = 3
	// strongAnchorBytes is the shortest literal that makes a rule strong.
	strongAnchorBytes = 4
	// maxVerify caps the expressions checked for one User-Agent.
	maxVerify = 32
	// maxReasonBytes caps the reason shown in the click diagnostics.
	maxReasonBytes = 120
)

// matomoPrefix is the token boundary DeviceDetector puts before every bot
// expression, so a short name does not match inside a longer word.
const matomoPrefix = `(?:^|[^a-z0-9_-]|[^a-z0-9-]_|sprd-|mz-)`

// weakWords are anchors too common in real visitor User-Agents to label a
// visitor alone; a rule that can match through one of them is weak.
var weakWords = map[string]bool{
	"bot": true, "bots": true, "spider": true, "crawl": true, "crawler": true, "crawling": true,
	"http": true, "https": true, "fetch": true, "fetcher": true, "scan": true, "scanner": true,
	"check": true, "checker": true, "monitor": true, "client": true, "agent": true, "java": true,
	"python": true, "perl": true, "ruby": true, "php": true, "proxy": true, "preview": true,
	"search": true, "index": true, "feed": true, "reader": true, "archive": true, "download": true,
	"validator": true, "test": true, "probe": true, "link": true, "news": true, "google": true,
	"mobile": true, "safari": true, "android": true, "iphone": true, "windows": true, "linux": true,
	"mozilla": true, "compatible": true, "chrome": true, "firefox": true, "webkit": true, "app": true,
	"telegram": true, "instagram": true, "sogou": true, ".com": true, "www.": true,
}

// BotCandidate is one rule as a source states it, before validation.
// Pattern is a Go regular expression ready to compile; Name is what the
// diagnostics show for it.
type BotCandidate struct {
	Pattern string
	Name    string
}

// BotRuleRecord is one stored rule of FileBotSignatures: the source name, the
// Go regular expression and the display name.
type BotRuleRecord struct {
	Source  string `json:"s"`
	Pattern string `json:"p"`
	Name    string `json:"n,omitempty"`
}

// BotDBSource is the state of one source at the last update.
// Rules is the number of accepted rules it contributed before deduplication,
// Rejected the number of rules that failed validation, Error the failure of the
// last download (its previous rules are kept then), OK the time of its last
// successful download.
type BotDBSource struct {
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Rules    int       `json:"rules"`
	Rejected int       `json:"rejected"`
	Error    string    `json:"error,omitempty"`
	OK       time.Time `json:"ok,omitzero"`
}

// BotDBFile is the content of FileBotSignatures.
type BotDBFile struct {
	Version int             `json:"version"`
	Updated time.Time       `json:"updated"`
	Sources []BotDBSource   `json:"sources"`
	Rules   []BotRuleRecord `json:"rules"`
}

// BotHit is the rule that labelled a visitor. Source is the source name, Name
// the rule name and Reason "source: name", ready for a response header.
type BotHit struct {
	Source string
	Name   string
	Reason string
}

// botRule is a compiled strong rule.
type botRule struct {
	re  *regexp.Regexp
	hit BotHit
}

// BotDB is an immutable compiled bot signature database: an Aho-Corasick
// automaton over the literal anchors of every strong rule and the rules
// themselves. A rule's expression runs only when one of its anchors occurs in
// the User-Agent. Weak rules are counted but not compiled in: they never label
// a visitor alone, and several of them do not add up to a strong one. The zero
// value and nil are an empty database. It is safe for concurrent use.
type BotDB struct {
	weak  int
	rules []botRule
	nodes []dbNode
	edges []edge
	outs  []uint32
	root  [256]uint32
}

// dbNode is one automaton state: its sorted edges, the failure link, the next
// state on the failure chain that has outputs, and the rules ending here.
type dbNode struct {
	edgeOff uint32
	edgeN   uint16
	fail    uint32
	dict    uint32
	outOff  uint32
	outN    uint32
}

// compiledRule is a validated rule with its anchors.
type compiledRule struct {
	re      *regexp.Regexp
	anchors []string
	weak    bool
}

// compileBotRule validates a Go regular expression for the database.
// It returns the compiled rule, or an error when the pattern is empty or too
// long, does not compile, or has no literal of at least minAnchorBytes that
// every match must contain.
func compileBotRule(pattern string) (compiledRule, error) {
	if pattern == "" {
		return compiledRule{}, errors.New("empty pattern")
	}
	if len(pattern) > 2*MaxBotPatternBytes {
		return compiledRule{}, errors.New("pattern too long")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return compiledRule{}, err
	}
	tree, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return compiledRule{}, err
	}
	anchors := anchorsOf(tree.Simplify())
	if anchorScore(anchors) < minAnchorBytes {
		return compiledRule{}, errors.New("no literal anchor")
	}
	weak := false
	for _, a := range anchors {
		if len(a) < strongAnchorBytes || weakWords[strings.TrimFunc(a, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' })] {
			weak = true
		}
	}
	return compiledRule{re: re, anchors: anchors, weak: weak}, nil
}

// anchorScore is the length of the shortest anchor, or 0 for no anchors.
func anchorScore(anchors []string) int {
	if len(anchors) == 0 {
		return 0
	}
	n := len(anchors[0])
	for _, a := range anchors[1:] {
		n = min(n, len(a))
	}
	return n
}

// literalOf returns the lowercased text of a node that matches exactly one
// string: a literal or a class of one letter in both cases.
func literalOf(re *syntax.Regexp) (string, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		return strings.ToLower(string(re.Rune)), true
	case syntax.OpCharClass:
		r := re.Rune
		if len(r) == 2 && r[0] == r[1] {
			return strings.ToLower(string(r[0])), true
		}
		if len(r) == 4 && r[0] == r[1] && r[2] == r[3] && unicode.ToLower(r[0]) == unicode.ToLower(r[2]) {
			return strings.ToLower(string(r[0])), true
		}
	}
	return "", false
}

// anchorsOf returns lowercased literals of which every match of re contains at
// least one, choosing the set whose shortest literal is longest. It returns nil
// when no such set exists.
func anchorsOf(re *syntax.Regexp) []string {
	switch re.Op {
	case syntax.OpLiteral, syntax.OpCharClass:
		if s, ok := literalOf(re); ok {
			return []string{s}
		}
	case syntax.OpCapture, syntax.OpPlus:
		return anchorsOf(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return anchorsOf(re.Sub[0])
		}
	case syntax.OpConcat:
		var best []string
		var run strings.Builder
		consider := func(set []string) {
			if anchorScore(set) > anchorScore(best) {
				best = set
			}
		}
		for _, sub := range re.Sub {
			if s, ok := literalOf(sub); ok {
				run.WriteString(s)
				continue
			}
			if run.Len() > 0 {
				consider([]string{run.String()})
				run.Reset()
			}
			consider(anchorsOf(sub))
		}
		if run.Len() > 0 {
			consider([]string{run.String()})
		}
		return best
	case syntax.OpAlternate:
		var out []string
		for _, sub := range re.Sub {
			a := anchorsOf(sub)
			if a == nil {
				return nil
			}
			out = append(out, a...)
		}
		return out
	}
	return nil
}

// ParseBotSource reads a downloaded signature file and returns its rules as Go
// expressions. The format is detected from the content: a JSON array of
// strings (isbot) or of objects with "pattern" or "regex" (Crawler User
// Agents), a YAML list with "regex:" keys (Matomo DeviceDetector, whose token
// boundary is kept), or plain text with one expression per line. Every rule is
// case-insensitive. rejected counts rules that failed validation; err is set
// when the content cannot be read in any of these formats.
func ParseBotSource(body []byte) (rules []BotCandidate, rejected int, err error) {
	body = bytes.TrimPrefix(bytes.TrimSpace(body), []byte("\xef\xbb\xbf"))
	var raw []BotCandidate
	matomo := false
	switch {
	case len(body) == 0:
		return nil, 0, errors.New("empty response")
	case body[0] == '[':
		raw, err = parseBotJSON(body)
	case body[0] == '{' || body[0] == '<':
		return nil, 0, errors.New("not a signature list")
	case bytes.Contains(body, []byte("regex:")):
		raw, matomo = parseMatomoYAML(body), true
	default:
		raw = parseBotLines(body)
	}
	if err != nil {
		return nil, 0, err
	}
	for _, c := range raw {
		p := strings.TrimSpace(c.Pattern)
		if p == "" || len(p) > MaxBotPatternBytes {
			rejected++
			continue
		}
		if matomo {
			p = "(?i)" + matomoPrefix + "(?:" + p + ")"
		} else {
			p = "(?i)" + p
		}
		if _, cerr := compileBotRule(p); cerr != nil {
			rejected++
			continue
		}
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = strings.TrimSpace(c.Pattern)
		}
		rules = append(rules, BotCandidate{Pattern: p, Name: name})
	}
	return rules, rejected, nil
}

// parseBotJSON reads a JSON array of strings or of objects with a "pattern" or
// "regex" field and an optional "name". It returns an error for any other JSON.
func parseBotJSON(body []byte) ([]BotCandidate, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, errors.New("not a JSON array")
	}
	out := make([]BotCandidate, 0, len(items))
	for _, it := range items {
		var s string
		if json.Unmarshal(it, &s) == nil {
			out = append(out, BotCandidate{Pattern: s})
			continue
		}
		var o struct {
			Pattern string `json:"pattern"`
			Regex   string `json:"regex"`
			Name    string `json:"name"`
		}
		if json.Unmarshal(it, &o) != nil {
			return nil, errors.New("JSON array items must be strings or objects")
		}
		p := o.Pattern
		if p == "" {
			p = o.Regex
		}
		out = append(out, BotCandidate{Pattern: p, Name: o.Name})
	}
	return out, nil
}

// parseMatomoYAML reads the bots.yml list of DeviceDetector: every item starts
// with "- regex:" and may have a "name:" key at the item level. Nested keys
// such as the producer name are ignored. Single-quoted, double-quoted and plain
// scalars are understood.
func parseMatomoYAML(body []byte) []BotCandidate {
	var out []BotCandidate
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := yamlKey(line, "- regex:"); ok {
			out = append(out, BotCandidate{Pattern: v})
			continue
		}
		if v, ok := yamlKey(line, "  regex:"); ok && len(out) > 0 && out[len(out)-1].Pattern == "" {
			out[len(out)-1].Pattern = v
			continue
		}
		if v, ok := yamlKey(line, "  name:"); ok && len(out) > 0 && out[len(out)-1].Name == "" {
			out[len(out)-1].Name = v
		}
	}
	return out
}

// yamlKey returns the scalar after prefix when line starts with it exactly.
func yamlKey(line, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return "", false
	}
	return yamlScalar(strings.TrimSpace(rest)), true
}

// yamlScalar unquotes a YAML scalar: a doubled single quote inside single
// quotes is one quote; a doubled backslash and an escaped double quote inside
// double quotes are a backslash and a quote; a plain scalar loses a trailing
// " #" comment.
func yamlScalar(v string) string {
	switch {
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		return strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(v[1 : len(v)-1])
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// parseBotLines reads one expression per line, skipping blank lines and # comments.
func parseBotLines(body []byte) []BotCandidate {
	var out []BotCandidate
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, BotCandidate{Pattern: line})
		}
	}
	return out
}

// MergeBotRules joins the rules of several sources in order and drops
// duplicates: a pattern seen before, compared case-insensitively, keeps its
// first source. At most MaxBotRules rules are returned.
func MergeBotRules(lists ...[]BotRuleRecord) []BotRuleRecord {
	seen := map[string]bool{}
	var out []BotRuleRecord
	for _, list := range lists {
		for _, r := range list {
			key := strings.ToLower(r.Pattern)
			if r.Pattern == "" || seen[key] {
				continue
			}
			if len(out) >= MaxBotRules {
				return out
			}
			seen[key] = true
			out = append(out, r)
		}
	}
	return out
}

// DecodeBotDBFile parses the content of FileBotSignatures. It returns an error
// for invalid JSON or an unknown version.
func DecodeBotDBFile(b []byte) (BotDBFile, error) {
	var f BotDBFile
	if err := json.Unmarshal(b, &f); err != nil {
		return BotDBFile{}, fmt.Errorf("bot signatures: %w", err)
	}
	if f.Version != botDBVersion {
		return BotDBFile{}, fmt.Errorf("bot signatures: unknown version %d", f.Version)
	}
	return f, nil
}

// EncodeBotDBFile returns the content of FileBotSignatures for f with the
// current format version.
func EncodeBotDBFile(f BotDBFile) ([]byte, error) {
	f.Version = botDBVersion
	return json.Marshal(f)
}

// LoadBotDB parses and compiles the content of FileBotSignatures. Rules that
// fail validation are skipped and counted in skipped. It returns an error when
// the content is not a valid database file; the caller keeps its previous
// database then.
func LoadBotDB(b []byte) (db *BotDB, skipped int, err error) {
	f, err := DecodeBotDBFile(b)
	if err != nil {
		return nil, 0, err
	}
	db, skipped = BuildBotDB(f.Rules)
	return db, skipped, nil
}

// BuildBotDB compiles records into a database. Duplicates are dropped as in
// MergeBotRules; records that fail validation are skipped and counted in
// skipped. The result is never nil.
func BuildBotDB(records []BotRuleRecord) (*BotDB, int) {
	records = MergeBotRules(records)
	skipped := 0
	db := &BotDB{}
	t := []buildNode{{}}
	termOuts := map[uint32][]uint32{}
	for _, r := range records {
		c, err := compileBotRule(r.Pattern)
		if err != nil {
			skipped++
			continue
		}
		if c.weak {
			db.weak++
			continue
		}
		id := uint32(len(db.rules))
		reason := r.Source + ": " + r.Name
		if len(reason) > maxReasonBytes {
			reason = reason[:maxReasonBytes]
		}
		db.rules = append(db.rules, botRule{re: c.re, hit: BotHit{Source: r.Source, Name: r.Name, Reason: headerSafe(reason)}})
		for _, a := range c.anchors {
			u := uint32(0)
			for i := 0; i < len(a); i++ {
				t, u = addChild(t, u, a[i])
			}
			if outs := termOuts[u]; !slices.Contains(outs, id) {
				termOuts[u] = append(outs, id)
			}
		}
	}
	db.build(t, termOuts)
	return db, skipped
}

// headerSafe replaces bytes that may not appear in an HTTP header value with '?'.
func headerSafe(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c >= 0x7f {
			b[i] = '?'
		}
	}
	return string(b)
}

// build computes the failure and output links of trie t and flattens it into db.
// outs maps a trie node to the rules whose anchor ends there.
func (db *BotDB) build(t []buildNode, outs map[uint32][]uint32) {
	dict := make([]uint32, len(t))
	queue := make([]uint32, 0, len(t))
	for _, e := range t[0].edges {
		queue = append(queue, e.to)
	}
	for head := 0; head < len(queue); head++ {
		u := queue[head]
		for _, e := range t[u].edges {
			v := e.to
			f := t[u].fail
			for {
				if w := findEdge(t[f].edges, e.b); w != 0 {
					t[v].fail = w
					break
				}
				if f == 0 {
					break
				}
				f = t[f].fail
			}
			if fv := t[v].fail; len(outs[fv]) > 0 {
				dict[v] = fv
			} else {
				dict[v] = dict[fv]
			}
			queue = append(queue, v)
		}
	}
	db.nodes = make([]dbNode, len(t))
	for i := range t {
		n := dbNode{fail: t[i].fail, dict: dict[i], outOff: uint32(len(db.outs)), outN: uint32(len(outs[uint32(i)]))}
		db.outs = append(db.outs, outs[uint32(i)]...)
		if i == 0 {
			for _, e := range t[0].edges {
				db.root[e.b] = e.to
			}
		} else {
			n.edgeOff, n.edgeN = uint32(len(db.edges)), uint16(len(t[i].edges))
			db.edges = append(db.edges, t[i].edges...)
		}
		db.nodes[i] = n
	}
}

// Len returns the number of compiled strong rules. A nil database has none.
func (db *BotDB) Len() int {
	if db == nil {
		return 0
	}
	return len(db.rules)
}

// Weak returns the number of weak rules left out of matching. A nil database has none.
func (db *BotDB) Weak() int {
	if db == nil {
		return 0
	}
	return db.weak
}

// Match scans ua once and checks the expressions of the rules whose anchors
// occur in it, at most maxVerify of them. It returns the first rule that
// matches with ok true. A nil database, an empty database or an empty ua give
// ok false. It does not allocate a database and does not use the network.
func (db *BotDB) Match(ua string) (hit BotHit, ok bool) {
	if db == nil || len(db.rules) == 0 || ua == "" {
		return BotHit{}, false
	}
	var tried [maxVerify]uint32
	n := 0
	var s uint32
	for i := 0; i < len(ua); i++ {
		c := ua[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		var next uint32
		for s != 0 {
			nd := &db.nodes[s]
			if next = findEdge(db.edges[nd.edgeOff:nd.edgeOff+uint32(nd.edgeN)], c); next != 0 {
				break
			}
			s = nd.fail
		}
		if s == 0 {
			next = db.root[c]
		}
		s = next
		t := s
		if t != 0 && db.nodes[t].outN == 0 {
			t = db.nodes[t].dict
		}
		for ; t != 0; t = db.nodes[t].dict {
			nd := &db.nodes[t]
			for _, id := range db.outs[nd.outOff : nd.outOff+nd.outN] {
				r := &db.rules[id]
				if slices.Contains(tried[:n], id) {
					continue
				}
				if n == maxVerify {
					return BotHit{}, false
				}
				tried[n] = id
				n++
				if r.re.MatchString(ua) {
					return r.hit, true
				}
			}
		}
	}
	return BotHit{}, false
}
