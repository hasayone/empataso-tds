package router

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

// TraceVersion is the layout version written into every stored Trace.
const TraceVersion = 1

// Trace sources.
const (
	// TraceSimulation marks the path of a simulated visitor; a real click leaves Source empty.
	TraceSimulation = "simulation"
)

// Size ceilings of one stored trace, so a hostile request cannot fill Redis.
const (
	maxTraceURL     = 2048
	maxTraceAgent   = 512
	maxTraceOut     = 1024
	maxTraceText    = 256
	maxTraceHops    = config.MaxGroupHops + 1
	maxTraceStreams = 100
	hiddenValue     = "[hidden]"
)

// secretQueryNames are the query parameters whose values are never stored: they
// carry the api request, a key or a token the visitor's link may hold.
var secretQueryNames = map[string]bool{
	"api": true, "pb": true, "token": true, "access_token": true, "secret": true,
	"password": true, "passwd": true, "pass": true, "auth": true, "authorization": true,
	"sign": true, "signature": true, "apikey": true, "api_key": true, "origin": true,
}

// HeaderLine is one request header that could name the visitor's address.
type HeaderLine struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"` // empty means the request did not carry the header
}

// StreamCheck is the verdict on one stream the router tried.
type StreamCheck struct {
	Name  string   `json:"name"`
	Match bool     `json:"match"`
	Why   []string `json:"why,omitempty"` // every rule that turned the visitor away, by rule key
}

// Hop is one pass over the streams of a campaign. A campaign that hands the
// visitor to another campaign adds one hop per campaign.
type Hop struct {
	Group    string        `json:"group"`
	Streams  []StreamCheck `json:"streams"`
	Selected string        `json:"selected"`       // the stream that served, or "-" for the campaign default
	Link     string        `json:"link,omitempty"` // the campaign this hop handed the visitor to
	Error    string        `json:"error,omitempty"`
}

// VisitorInfo is what the engine learned about the visitor on this request.
type VisitorInfo struct {
	Country    string            `json:"country,omitempty"`
	Region     string            `json:"region,omitempty"`
	City       string            `json:"city,omitempty"`
	Timezone   string            `json:"timezone,omitempty"`
	ASN        uint32            `json:"asn,omitempty"`
	Org        string            `json:"org,omitempty"`
	Operator   string            `json:"operator,omitempty"`
	Device     string            `json:"device,omitempty"`
	OS         string            `json:"os,omitempty"`
	OSVersion  string            `json:"os_version,omitempty"`
	Browser    string            `json:"browser,omitempty"`
	BrowserVer string            `json:"browser_ver,omitempty"`
	Brand      string            `json:"brand,omitempty"`
	Model      string            `json:"model,omitempty"`
	Lang       string            `json:"lang,omitempty"`
	Referer    string            `json:"referer,omitempty"`
	Keyword    string            `json:"keyword,omitempty"`
	UserAgent  string            `json:"useragent,omitempty"`
	Bot        string            `json:"bot,omitempty"`
	BotWhy     string            `json:"bot_why,omitempty"`
	Proxy      bool              `json:"proxy,omitempty"`
	Unique     bool              `json:"unique"`
	Subs       map[string]string `json:"subs,omitempty"` // sub_id_N → value, only the filled ones
}

// Trace is the path of one click through the engine: what arrived, which
// streams were tried and why each one did or did not take the visitor, what the
// visitor was given and what the engine knew about them. It holds no cookie, no
// Authorization header and no secret query value.
type Trace struct {
	V        int          `json:"v"`
	TS       time.Time    `json:"ts"`
	Source   string       `json:"source,omitempty"`
	CID      string       `json:"cid,omitempty"`
	Step     int          `json:"step,omitempty"` // 1 for the /offer step
	Campaign string       `json:"campaign"`       // id of the campaign the visitor entered
	Name     string       `json:"name,omitempty"`
	URL      string       `json:"url,omitempty"`
	Headers  []HeaderLine `json:"headers,omitempty"`
	Trusted  bool         `json:"trusted,omitempty"` // the address came through a trusted proxy
	IP       string       `json:"ip"`
	Hops     []Hop        `json:"hops,omitempty"`
	Stream   string       `json:"stream,omitempty"` // the stream that finally served, "-" for the default
	Via      string       `json:"via,omitempty"`    // "<campaign>/<stream>" of the first link followed
	Action   string       `json:"action,omitempty"` // what the visitor got: redirect type of the output
	Out      string       `json:"out,omitempty"`
	OfferID  string       `json:"offer_id,omitempty"`
	Landing  string       `json:"landing_id,omitempty"`
	Network  string       `json:"network_id,omitempty"`
	Notes    []string     `json:"notes,omitempty"` // keys of things that changed the course: "bot_served", "antiflood", …
	Saved    bool         `json:"saved"`           // the click was queued for the statistics
	DurUS    int64        `json:"dur_us"`          // time the engine spent before it answered, in microseconds
	Visitor  VisitorInfo  `json:"visitor"`
}

// HopDraft is a hop as the request goroutine leaves it: the streams the router
// looked at with the first rule that stopped each, and a copy of the visitor. The
// full list of failed rules is worked out later by Hop, off the request path.
type HopDraft struct {
	Group    string
	Rec      *HopRecorder
	V        Visitor
	Selected string
	Link     string
	Error    string
}

// HopRecorder collects the streams one selection looked at, in order.
// The zero value is ready. It is used by one request goroutine.
type HopRecorder struct {
	seen []seenStream
}

type seenStream struct {
	s   *config.Stream
	why string
}

// Note records that stream s was tried and why: why is the first rule that
// turned the visitor away, or "" when s matched. A stream noted twice keeps the
// later verdict in its first place. It matches the Deps.Seen signature.
func (h *HopRecorder) Note(s *config.Stream, why string) {
	for i := range h.seen {
		if h.seen[i].s == s {
			h.seen[i].why = why
			return
		}
	}
	h.seen = append(h.seen, seenStream{s: s, why: why})
}

// Hop turns the draft into a stored hop.
//
// deps carries the IP lists and the clock for the rules that read them; its
// limiter is ignored, so building the hop never spends a counter. A stream the
// router rejected lists every rule that rejects the visitor, not only the first.
// A limit that stopped the stream is added from the verdict recorded at selection.
func (d HopDraft) Hop(deps Deps) Hop {
	h := Hop{Group: d.Group, Selected: d.Selected, Link: d.Link, Error: d.Error, Streams: []StreamCheck{}}
	if d.Rec == nil {
		return h
	}
	deps.Limiter = nil
	deps.Seen = nil
	for i, e := range d.Rec.seen {
		if i >= maxTraceStreams {
			break
		}
		c := StreamCheck{Name: e.s.Name, Match: e.why == ""}
		if !c.Match {
			c.Why = WhyAll(e.s, d.V, deps)
			if e.why == "limit" {
				c.Why = append(c.Why, "limit")
			}
			if len(c.Why) == 0 {
				c.Why = []string{e.why}
			}
		}
		h.Streams = append(h.Streams, c)
	}
	return h
}

// WhyAll names every rule of the stream that rejects the visitor, in the order
// of the stream's rule list, or nil when none does. It uses the same checks as
// Why, one rule at a time, so the two never disagree about a rule.
//
// s is the stream, v the visitor, deps the IP lists and clock. The serve limit is
// left out: asking would spend its counter, and Why already reports it.
func WhyAll(s *config.Stream, v Visitor, deps Deps) []string {
	if s == nil {
		return nil
	}
	deps.Limiter = nil
	deps.Seen = nil
	cp := *s
	cp.FilterOr = false
	var out []string
	for _, c := range ruleChecks(s.Rules) {
		if !c.on {
			continue
		}
		cp.Rules = c.rule
		if whyAnd(&cp, v, deps) != "" {
			out = append(out, c.name)
		}
	}
	return out
}

// Seal makes the trace safe to store: it fixes the layout version, cuts every
// free text to its ceiling and keeps at most the hops and streams a stored trace
// may hold. It does not fail and may be called twice.
func (t *Trace) Seal() {
	t.V = TraceVersion
	t.URL = config.LimitBytes(t.URL, maxTraceURL)
	t.Out = config.LimitBytes(t.Out, maxTraceOut)
	for i := range t.Headers {
		t.Headers[i].Value = config.LimitBytes(t.Headers[i].Value, maxTraceText)
	}
	if len(t.Hops) > maxTraceHops {
		t.Hops = t.Hops[:maxTraceHops]
	}
	for i := range t.Hops {
		if len(t.Hops[i].Streams) > maxTraceStreams {
			t.Hops[i].Streams = t.Hops[i].Streams[:maxTraceStreams]
		}
	}
	v := &t.Visitor
	v.UserAgent = config.LimitBytes(v.UserAgent, maxTraceAgent)
	v.Referer = config.LimitBytes(v.Referer, maxTraceText*2)
	v.Keyword = config.LimitBytes(v.Keyword, maxTraceText)
	for k, val := range v.Subs {
		v.Subs[k] = config.LimitBytes(val, maxTraceText)
	}
}

// SearchText is the lower-case text a log search looks through: the address,
// the request, the campaign and stream, the visitor's place and device, the ids
// and every sub id.
func (t *Trace) SearchText() string {
	v := t.Visitor
	parts := []string{t.CID, t.Campaign, t.Name, t.URL, t.IP, t.Stream, t.Via, t.Action, t.Out, t.OfferID, t.Landing, t.Network,
		v.Country, v.Region, v.City, v.Org, v.Operator, v.Device, v.OS, v.Browser, v.Brand, v.Model, v.Lang, v.Referer, v.Keyword,
		v.UserAgent, v.Bot, v.BotWhy, v.Timezone}
	if v.ASN != 0 {
		parts = append(parts, fmt.Sprintf("as%d", v.ASN))
	}
	for _, h := range t.Headers {
		parts = append(parts, h.Value)
	}
	for _, h := range t.Hops {
		parts = append(parts, h.Group, h.Selected)
	}
	keys := make([]string, 0, len(v.Subs))
	for k := range v.Subs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, v.Subs[k])
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// MaskURL hides the value of every secret query parameter of an address.
//
// raw is the request address or its path with a query. Names are compared
// ignoring case; the rest of the address keeps its original spelling. An address
// without a query is returned unchanged.
func MaskURL(raw string) string {
	base, query, ok := strings.Cut(raw, "?")
	if !ok || query == "" {
		return raw
	}
	pairs := strings.Split(query, "&")
	for i, p := range pairs {
		name, _, hasValue := strings.Cut(p, "=")
		if !hasValue {
			continue
		}
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		if secretQueryNames[strings.ToLower(name)] {
			pairs[i] = p[:strings.IndexByte(p, '=')+1] + hiddenValue
		}
	}
	return base + "?" + strings.Join(pairs, "&")
}
