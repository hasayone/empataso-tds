package router

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// traceGroup has three streams: one that rejects the base visitor on two rules,
// a disabled one, and one that takes everybody.
func traceGroup() *config.Group {
	return &config.Group{ID: "g", Streams: []config.Stream{
		stream("strict", config.Rules{Lang: list(config.FlagB, "", "en"), Country: list(config.FlagB, "", "us"), Phone: config.FlagA}),
		{Name: "off", Status: false},
		stream("all", config.Rules{}),
	}}
}

func TestSeenReportsEveryActiveStreamInOrder(t *testing.T) {
	rec := &HopRecorder{}
	g := traceGroup()
	s, ok := Select(g, baseVisitor(), Deps{Seen: rec.Note})
	if !ok || s.Name != "all" {
		t.Fatalf("selected %v %v", s, ok)
	}
	hop := HopDraft{Group: "g", Rec: rec, V: baseVisitor(), Selected: s.Name}.Hop(Deps{})
	if len(hop.Streams) != 2 {
		t.Fatalf("streams = %+v, want the two active ones", hop.Streams)
	}
	strict := hop.Streams[0]
	if strict.Name != "strict" || strict.Match || !reflect.DeepEqual(strict.Why, []string{"lang", "country"}) {
		t.Errorf("strict = %+v, want lang and country", strict)
	}
	if !hop.Streams[1].Match || hop.Selected != "all" {
		t.Errorf("hop = %+v", hop)
	}
}

func TestSeenDoesNotChangeTheChoice(t *testing.T) {
	g := traceGroup()
	a, _ := Select(g, baseVisitor(), Deps{})
	b, _ := Select(g, baseVisitor(), Deps{Seen: (&HopRecorder{}).Note})
	if a != b {
		t.Errorf("a hook changed the selected stream: %v vs %v", a, b)
	}
}

func TestSeenInWeightedSelection(t *testing.T) {
	g := traceGroup()
	g.Weight = true
	g.Streams[2].Weight = 1
	rec := &HopRecorder{}
	s, ok := Select(g, baseVisitor(), Deps{Seen: rec.Note})
	if !ok || s.Name != "all" {
		t.Fatalf("selected %v %v", s, ok)
	}
	if got := len(rec.seen); got != 2 {
		t.Errorf("recorded %d streams, want 2", got)
	}
}

func TestSeenNotesALimitTakenAtTheDraw(t *testing.T) {
	g := &config.Group{Weight: true, Streams: []config.Stream{
		{Name: "capped", Status: true, Weight: 1, Rules: config.Rules{Limit: config.LimitRule{Enabled: true, Count: 1}}},
	}}
	rec := &HopRecorder{}
	if _, ok := Select(g, baseVisitor(), Deps{Seen: rec.Note, Limiter: denyAll{}}); ok {
		t.Fatal("the exhausted stream must not serve")
	}
	hop := HopDraft{Group: "g", Rec: rec, V: baseVisitor()}.Hop(Deps{})
	if len(hop.Streams) != 1 || hop.Streams[0].Match || !reflect.DeepEqual(hop.Streams[0].Why, []string{"limit"}) {
		t.Errorf("hop = %+v, want the limit as the reason", hop)
	}
}

type denyAll struct{}

func (denyAll) Allowed(string, config.LimitRule) bool { return false }

func TestWhyAllAgreesWithWhy(t *testing.T) {
	v := baseVisitor()
	for _, s := range traceGroup().Streams {
		s := s
		first := Why(&s, v, Deps{})
		all := WhyAll(&s, v, Deps{})
		if (first == "") != (len(all) == 0) {
			t.Errorf("%s: Why %q, WhyAll %v", s.Name, first, all)
		}
		if first != "" && !containsKey(all, first) {
			t.Errorf("%s: the first reason %q is missing from %v", s.Name, first, all)
		}
	}
}

func containsKey(list []string, key string) bool {
	for _, k := range list {
		if k == key {
			return true
		}
	}
	return false
}

func TestWhyAllInAnyConditionMode(t *testing.T) {
	s := stream("or", config.Rules{Lang: list(config.FlagB, "", "en"), Country: list(config.FlagB, "", "us")})
	s.FilterOr = true
	if got := WhyAll(&s, baseVisitor(), Deps{}); !reflect.DeepEqual(got, []string{"lang", "country"}) {
		t.Errorf("both rules turn the visitor away, got %v", got)
	}
	v := baseVisitor()
	v.Country = "us"
	if Why(&s, v, Deps{}) != "" {
		t.Fatal("any one passing rule is enough")
	}
}

func TestWhyAllNeverSpendsALimit(t *testing.T) {
	s := stream("capped", config.Rules{Limit: config.LimitRule{Enabled: true, Count: 1}})
	spy := &spyLimiter{}
	if got := WhyAll(&s, baseVisitor(), Deps{Limiter: spy}); len(got) != 0 || spy.calls != 0 {
		t.Errorf("WhyAll %v asked the limiter %d times", got, spy.calls)
	}
}

type spyLimiter struct{ calls int }

func (s *spyLimiter) Allowed(string, config.LimitRule) bool { s.calls++; return true }

func TestWhyAllNilStream(t *testing.T) {
	if WhyAll(nil, baseVisitor(), Deps{}) != nil {
		t.Error("a nil stream has no reasons")
	}
}

func TestMaskURLHidesSecretValues(t *testing.T) {
	cases := map[string]string{
		"https://h/p?a=1&api=SECRET&b=2":      "https://h/p?a=1&api=[hidden]&b=2",
		"/p?Token=abc&x=y":                    "/p?Token=[hidden]&x=y",
		"/p?password=1&Authorization=Bearer":  "/p?password=[hidden]&Authorization=[hidden]",
		"/p?origin=tok&sub_id_1=keep":         "/p?origin=[hidden]&sub_id_1=keep",
		"/p?flag&api":                         "/p?flag&api",
		"/p?%61pi=SECRET":                     "/p?%61pi=[hidden]",
		"/plain":                              "/plain",
		"/p?":                                 "/p?",
		"/p?q=buy shoes":                      "/p?q=buy shoes",
	}
	for in, want := range cases {
		if got := MaskURL(in); got != want {
			t.Errorf("MaskURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSealCapsTextAndLayout(t *testing.T) {
	long := strings.Repeat("я", 5000)
	tr := Trace{URL: long, Out: long, Headers: []HeaderLine{{Name: "x-forwarded-for", Value: long}},
		Visitor: VisitorInfo{UserAgent: long, Referer: long, Keyword: long, Subs: map[string]string{"sub_id_1": long}}}
	for i := 0; i < 10; i++ {
		tr.Hops = append(tr.Hops, Hop{Group: "g", Streams: make([]StreamCheck, 150)})
	}
	tr.Seal()
	if tr.V != TraceVersion {
		t.Errorf("version = %d", tr.V)
	}
	if len(tr.URL) > maxTraceURL || len(tr.Out) > maxTraceOut || len(tr.Headers[0].Value) > maxTraceText ||
		len(tr.Visitor.UserAgent) > maxTraceAgent || len(tr.Visitor.Subs["sub_id_1"]) > maxTraceText {
		t.Errorf("a text was not cut: url %d out %d", len(tr.URL), len(tr.Out))
	}
	if len(tr.Hops) != maxTraceHops || len(tr.Hops[0].Streams) != maxTraceStreams {
		t.Errorf("hops %d streams %d", len(tr.Hops), len(tr.Hops[0].Streams))
	}
	if b, err := json.Marshal(tr); err != nil || len(b) > 64<<10 {
		t.Errorf("a sealed trace is %d bytes, err %v", len(b), err)
	}
}

func TestSearchTextCoversTheClick(t *testing.T) {
	tr := Trace{CID: "AbC123", Campaign: "Spring", IP: "203.0.113.7", URL: "https://h/Spring?x=1", Stream: "Mobile",
		Headers: []HeaderLine{{Name: "x-real-ip", Value: "198.51.100.9"}},
		Visitor: VisitorInfo{Country: "DE", ASN: 64500, Subs: map[string]string{"sub_id_2": "Fonbet"}}}
	text := tr.SearchText()
	for _, want := range []string{"abc123", "spring", "203.0.113.7", "mobile", "198.51.100.9", "de", "as64500", "fonbet"} {
		if !strings.Contains(text, want) {
			t.Errorf("search text lacks %q: %s", want, text)
		}
	}
	if text != strings.ToLower(text) {
		t.Error("search text must be lower case")
	}
}

func TestHopWithoutRecorder(t *testing.T) {
	h := HopDraft{Group: "g", Selected: "-"}.Hop(Deps{})
	if h.Streams == nil || len(h.Streams) != 0 || h.Group != "g" {
		t.Errorf("hop = %+v", h)
	}
}
