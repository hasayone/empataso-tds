package detect

import (
	"slices"
	"strings"
)

// Match priorities: a larger value wins, zero means "no label". Search engines
// are the top of the scale and keep the order baidu, bing, google, mail, yahoo,
// yandex, apple; below them come sign_ua, headless, scanner, http_client, crawler.
const (
	prioNone uint8 = iota
	prioCrawler
	prioHTTPClient
	prioScanner
	prioHeadless
	prioSignUA
	prioApple
	prioYandex
	prioYahoo
	prioMail
	prioGoogle
	prioBing
	prioBaidu
	prioCount
)

// labelByPrio maps a priority to the bot label it reports.
var labelByPrio = [prioCount]string{
	prioNone:       BotNone,
	prioCrawler:    BotCrawler,
	prioHTTPClient: BotHTTPClient,
	prioScanner:    BotScanner,
	prioHeadless:   BotHeadless,
	prioSignUA:     BotSignUA,
	prioApple:      BotApple,
	prioYandex:     BotYandex,
	prioYahoo:      BotYahoo,
	prioMail:       BotMail,
	prioGoogle:     BotGoogle,
	prioBing:       BotBing,
	prioBaidu:      BotBaidu,
}

// Service flags a pattern can raise while the User-Agent is scanned.
const (
	flagMozilla uint8 = 1 << iota // "mozilla/" seen: bare patterns no longer count
	flagHuman                     // a token of a human browser or app seen: search engine labels are dropped
)

// pattern is one substring the matcher looks for.
// text is the substring (lowercased when the matcher is built), prio the priority
// of the label it reports (prioNone for a pure flag pattern), bare marks a pattern
// that counts only when "mozilla/" is absent, flag the service flag it raises.
type pattern struct {
	text string
	prio uint8
	bare bool
	flag uint8
}

// summary is what one scan step knows: the best search engine, the best other
// label, the best bare label and the raised flags. Merging keeps the maximum of each field.
type summary struct {
	se, other, bare, flags uint8
}

// summaryOf converts a pattern into the summary it contributes.
func summaryOf(p pattern) summary {
	s := summary{flags: p.flag}
	switch {
	case p.prio == prioNone:
	case p.prio >= prioApple:
		s.se = p.prio
	case p.bare:
		s.bare = p.prio
	default:
		s.other = p.prio
	}
	return s
}

// merge raises every field of s to at least the value in o.
func (s *summary) merge(o summary) {
	s.se = max(s.se, o.se)
	s.other = max(s.other, o.other)
	s.bare = max(s.bare, o.bare)
	s.flags |= o.flags
}

// edge is a transition from a node by byte b to node to; to is never the root.
type edge struct {
	b  byte
	to uint32
}

// node is one automaton state: the summary of every pattern that ends at the
// state or at a suffix of it, the slice of its sorted edges in Matcher.edges,
// and the failure link.
type node struct {
	sum     summary
	edgeOff uint32
	fail    uint32
	edgeN   uint16
}

// Matcher is an immutable Aho-Corasick automaton over User-Agent patterns.
// Edges are sparse: every node keeps a sorted list of its outgoing bytes and
// only the root has a dense 256-entry table. A built Matcher is safe for
// concurrent use and Match does not allocate.
type Matcher struct {
	nodes []node
	edges []edge
	root  [256]uint32
}

// buildNode is a node of the trie while the automaton is being built.
type buildNode struct {
	edges []edge
	fail  uint32
	sum   summary
}

// findEdge returns the target of the edge labelled c in the sorted edges, or 0 when there is none.
func findEdge(edges []edge, c byte) uint32 {
	if len(edges) <= 8 {
		for i := range edges {
			if edges[i].b == c {
				return edges[i].to
			}
		}
		return 0
	}
	lo, hi := 0, len(edges)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if edges[mid].b < c {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(edges) && edges[lo].b == c {
		return edges[lo].to
	}
	return 0
}

// addChild returns the child of trie node u by byte c, creating it when absent.
func addChild(t []buildNode, u uint32, c byte) ([]buildNode, uint32) {
	if v := findEdge(t[u].edges, c); v != 0 {
		return t, v
	}
	v := uint32(len(t))
	t = append(t, buildNode{})
	pos, _ := slices.BinarySearchFunc(t[u].edges, c, func(e edge, c byte) int { return int(e.b) - int(c) })
	t[u].edges = slices.Insert(t[u].edges, pos, edge{b: c, to: v})
	return t, v
}

// newMatcher builds the automaton for pats. Pattern texts are lowercased with
// strings.ToLower and empty ones are skipped; the scan later folds only ASCII
// letters of the input, so a pattern with non-ASCII capitals never matches them.
// The result is never nil.
func newMatcher(pats []pattern) *Matcher {
	t := []buildNode{{}}
	for _, p := range pats {
		text := strings.ToLower(p.text)
		if text == "" {
			continue
		}
		u := uint32(0)
		for i := 0; i < len(text); i++ {
			t, u = addChild(t, u, text[i])
		}
		t[u].sum.merge(summaryOf(p))
	}

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
			t[v].sum.merge(t[t[v].fail].sum)
			queue = append(queue, v)
		}
	}

	total := 0
	for i := 1; i < len(t); i++ {
		total += len(t[i].edges)
	}
	m := &Matcher{nodes: make([]node, len(t)), edges: make([]edge, 0, total)}
	for i := range t {
		m.nodes[i] = node{sum: t[i].sum, fail: t[i].fail}
		if i == 0 {
			for _, e := range t[0].edges {
				m.root[e.b] = e.to
			}
			continue
		}
		m.nodes[i].edgeOff = uint32(len(m.edges))
		m.nodes[i].edgeN = uint16(len(t[i].edges))
		m.edges = append(m.edges, t[i].edges...)
	}
	return m
}

// Match scans ua once, folding ASCII capitals on the fly, and returns the bot
// label or BotNone. A search engine label wins unless a human token was seen;
// otherwise the best remaining label wins, and bare labels count only when
// "mozilla/" was not seen. It does not allocate and does not report an error.
func (m *Matcher) Match(ua string) string {
	label, _ := m.MatchHuman(ua)
	return label
}

// MatchHuman is Match that also reports whether ua carries a token of a human
// browser or app (flagHuman). It does not allocate and does not report an error.
func (m *Matcher) MatchHuman(ua string) (label string, human bool) {
	var sum summary
	var s uint32
	for i := 0; i < len(ua); i++ {
		c := ua[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		var next uint32
		for s != 0 {
			n := &m.nodes[s]
			if next = findEdge(m.edges[n.edgeOff:n.edgeOff+uint32(n.edgeN)], c); next != 0 {
				break
			}
			s = n.fail
		}
		if s == 0 {
			next = m.root[c]
		}
		s = next
		if s != 0 {
			sum.merge(m.nodes[s].sum)
		}
	}
	human = sum.flags&flagHuman != 0
	if !human && sum.se != prioNone {
		return labelByPrio[sum.se], false
	}
	best := sum.other
	if sum.flags&flagMozilla == 0 {
		best = max(best, sum.bare)
	}
	return labelByPrio[best], human
}
