package main

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// keywordSink keeps the benchmark result alive.
var keywordSink string

// aliasKeyword serves one click on a group whose traffic source maps keyword to
// "k" and returns the Keyword the click log received.
//
// t is the test, rawQuery the query of the request without the question mark.
func aliasKeyword(t *testing.T, rawQuery string) string {
	t.Helper()
	gg := linked(&config.Group{ID: "c", Status: true, SourceID: "sc", Redirect: "show_text", Out: "x"})
	h, ins, stop := withClickLog(t, gg, withSources(t,
		config.TrafficSource{ID: "sc", Parameters: map[string]string{"keyword": "k"}}))
	do(t, h, "/c?"+rawQuery, "8.8.8.8", chrome)
	stop()
	if len(ins.events) != 1 {
		t.Fatalf("%q: %d events, want 1", rawQuery, len(ins.events))
	}
	return ins.events[0].Keyword
}

func TestBaseKeywordPriority(t *testing.T) {
	cases := []struct {
		name string
		q    url.Values
		want string
	}{
		{"nothing", url.Values{}, ""},
		{"nil query", nil, ""},
		{"keyword only", url.Values{"keyword": {"kw"}}, "kw"},
		{"q only", url.Values{"q": {"qq"}}, "qq"},
		{"utm_term only", url.Values{"utm_term": {"ut"}}, "ut"},
		{"keyword over q", url.Values{"keyword": {"kw"}, "q": {"qq"}}, "kw"},
		{"q over utm_term", url.Values{"q": {"qq"}, "utm_term": {"ut"}}, "qq"},
		{"keyword over all", url.Values{"keyword": {"kw"}, "q": {"qq"}, "utm_term": {"ut"}}, "kw"},
		{"empty keyword falls to q", url.Values{"keyword": {""}, "q": {"qq"}, "utm_term": {"ut"}}, "qq"},
		{"empty keyword and q fall to utm_term", url.Values{"keyword": {""}, "q": {""}, "utm_term": {"ut"}}, "ut"},
		{"all empty", url.Values{"keyword": {""}, "q": {""}, "utm_term": {""}}, ""},
		{"first value of a repeated keyword", url.Values{"keyword": {"first", "second"}}, "first"},
		{"empty first value is absent", url.Values{"keyword": {"", "second"}, "q": {"qq"}}, "qq"},
		{"other names are ignored", url.Values{"utm_source": {"s"}, "kw": {"x"}, "Q": {"upper"}}, ""},
	}
	for _, c := range cases {
		if got := baseKeyword(c.q, nil, "", ""); got != c.want {
			t.Errorf("%s: baseKeyword = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBaseKeywordLimitsAtRuneBoundary(t *testing.T) {
	cases := []struct {
		name, in string
		want     int
	}{
		{"ascii at the limit", strings.Repeat("a", 255), 255},
		{"ascii over the limit", strings.Repeat("a", 300), 255},
		{"2-byte rune across the limit", strings.Repeat("a", 254) + "\u00e9", 254},
		{"2-byte runes only", strings.Repeat("\u00e9", 300), 254},
		{"3-byte rune across the limit", strings.Repeat("a", 253) + "\u20ac", 253},
		{"4-byte rune across the limit", strings.Repeat("a", 252) + "\U0001F600", 252},
	}
	for _, c := range cases {
		for _, name := range []string{"keyword", "q", "utm_term"} {
			got := baseKeyword(url.Values{name: {c.in}}, nil, "", "")
			if len(got) != c.want || !utf8.ValidString(got) || !strings.HasPrefix(c.in, got) {
				t.Errorf("%s via %s: %d bytes valid=%v, want %d", c.name, name, len(got), utf8.ValidString(got), c.want)
			}
		}
	}
}

// A name another field of the group owns is not read as the keyword: the source
// maps it onto any field but keyword, or it is the price or currency name.
func TestBaseKeywordSkipsNamesOfOtherFields(t *testing.T) {
	all := url.Values{"keyword": {"kw"}, "q": {"qq"}, "utm_term": {"ut"}}
	cases := []struct {
		name       string
		q          url.Values
		params     map[string]string
		cost, cur  string
		want       string
		withoutSrc bool
	}{
		{name: "sub_id_1 on utm_term, only utm_term sent", q: url.Values{"utm_term": {"SECRET"}}, params: map[string]string{"sub_id_1": "utm_term"}, want: ""},
		{name: "sub_id_1 on utm_term, q sent too", q: url.Values{"utm_term": {"SECRET"}, "q": {"x"}}, params: map[string]string{"sub_id_1": "utm_term"}, want: "x"},
		{name: "site on keyword", q: url.Values{"keyword": {"SECRET"}, "q": {"qq"}}, params: map[string]string{"site": "keyword"}, want: "qq"},
		{name: "site on keyword, nothing else", q: url.Values{"keyword": {"SECRET"}}, params: map[string]string{"site": "keyword"}, want: ""},
		{name: "q stays the keyword though a sub id is bound to it", q: url.Values{"q": {"x"}, "utm_term": {"ut"}}, params: map[string]string{"sub_id_3": " q "}, want: "x"},
		{name: "mapping is trimmed", q: url.Values{"utm_term": {"SECRET"}, "q": {""}}, params: map[string]string{"sub_id_3": " utm_term "}, want: ""},
		{name: "extra fields count", q: url.Values{"keyword": {"a"}, "q": {"b"}, "utm_term": {"c"}},
			params: map[string]string{"creative_id": "keyword", "external_id": "utm_term"}, want: "b"},
		{name: "keyword mapped onto its own alias is not another field", q: url.Values{"utm_term": {"ut"}}, params: map[string]string{"keyword": "utm_term"}, want: "ut"},
		{name: "unrelated mapping", q: all, params: map[string]string{"sub_id_1": "p1", "keyword": "kw"}, want: "kw"},
		{name: "cost param", q: url.Values{"utm_term": {"12.5"}}, cost: "utm_term", cur: "cur", want: ""},
		{name: "cost param falls to next", q: url.Values{"utm_term": {"12.5"}, "q": {"qq"}}, cost: "utm_term", cur: "cur", want: "qq"},
		{name: "currency param", q: url.Values{"keyword": {"USD"}, "utm_term": {"ut"}}, cost: "cost", cur: " keyword ", want: "ut"},
		{name: "q stays the keyword though it is the price name", q: url.Values{"q": {"12.5"}}, cost: "q", cur: "cur", want: "12.5"},
		{name: "cost without a source", q: all, cost: "keyword", want: "qq", withoutSrc: true},
		{name: "mapping ignored without a source", q: all, want: "kw", withoutSrc: true},
	}
	for _, c := range cases {
		var src *config.TrafficSource
		if !c.withoutSrc {
			src = &config.TrafficSource{ID: "s", Parameters: c.params}
		}
		if got := baseKeyword(c.q, src, c.cost, c.cur); got != c.want {
			t.Errorf("%s: baseKeyword = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBaseKeywordDoesNotAllocate(t *testing.T) {
	src := &config.TrafficSource{ID: "s", Parameters: map[string]string{
		"keyword": "k", "sub_id_1": "p1", "sub_id_2": "utm_term", "site": "s", "creative_id": "c",
	}}
	wide := url.Values{"utm_term": {strings.Repeat("\u00e9", 300)}, "q": {"x"}, "sub_id_1": {"x"}}
	full := url.Values{"keyword": {"kw"}, "q": {"qq"}, "utm_term": {"ut"}}
	cases := []struct {
		name string
		q    url.Values
		src  *config.TrafficSource
		cost string
	}{
		{"no source, alias", wide, nil, ""},
		{"no source, keyword wins", full, nil, ""},
		{"source, alias after a skipped name", wide, src, "price"},
		{"source, keyword wins", full, src, "price"},
		{"source, every alias skipped", url.Values{"utm_term": {"x"}}, &config.TrafficSource{Parameters: map[string]string{"sub_id_1": "keyword", "sub_id_2": "q", "sub_id_3": "utm_term"}}, "price"},
	}
	for _, c := range cases {
		if n := testing.AllocsPerRun(200, func() { keywordSink = baseKeyword(c.q, c.src, c.cost, "cur") }); n != 0 {
			t.Errorf("%s: baseKeyword allocates %v times per call", c.name, n)
		}
	}
}

func BenchmarkBaseKeyword(b *testing.B) {
	src := &config.TrafficSource{ID: "s", Parameters: map[string]string{
		"keyword": "k", "sub_id_1": "p1", "sub_id_2": "p2", "sub_id_3": "utm_term", "site": "s", "creative_id": "c",
	}}
	cases := []struct {
		name string
		q    url.Values
		src  *config.TrafficSource
	}{
		{"keyword", url.Values{"keyword": {"red shoes"}, "q": {"x"}, "utm_term": {"y"}}, nil},
		{"utm_term", url.Values{"utm_term": {"red shoes"}, "sub_id_1": {"a"}, "cid": {"b"}}, nil},
		{"none", url.Values{"sub_id_1": {"a"}, "cid": {"b"}}, nil},
		{"wide", url.Values{"q": {strings.Repeat("\u00e9", 300)}}, nil},
		{"source_q", url.Values{"q": {"red shoes"}, "sub_id_1": {"a"}}, src},
		{"source_skipped", url.Values{"utm_term": {"a"}, "q": {"red shoes"}}, src},
		{"source_none", url.Values{"sub_id_1": {"a"}, "cid": {"b"}}, src},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				keywordSink = baseKeyword(c.q, c.src, "price", "cur")
			}
		})
	}
}

// The click log receives the keyword from the first non-empty name in the order
// keyword, q, utm_term.
func TestKeywordAliasInClickLog(t *testing.T) {
	cases := []struct{ query, want string }{
		{"keyword=kw", "kw"},
		{"utm_term=ut", "ut"},
		{"q=qq", "qq"},
		{"keyword=kw&q=qq&utm_term=ut", "kw"},
		{"q=qq&utm_term=ut", "qq"},
		{"keyword=&q=qq&utm_term=ut", "qq"},
		{"keyword=&q=&utm_term=ut", "ut"},
		{"keyword=&q=&utm_term=", ""},
		{"utm_term=" + url.QueryEscape(strings.Repeat("\u00e9", 300)), strings.Repeat("\u00e9", 127)},
	}
	for _, c := range cases {
		if got := aliasKeyword(t, c.query); got != c.want {
			t.Errorf("?%s: logged keyword %q, want %q", c.query, got, c.want)
		}
	}
}

// A keyword the traffic source maps under its own name beats every alias; when
// that name is absent or empty the aliases apply.
func TestKeywordAliasSourceMappingWins(t *testing.T) {
	cases := []struct{ query, want string }{
		{"k=src&keyword=kw&q=qq&utm_term=ut", "src"},
		{"k=src", "src"},
		{"keyword=kw&q=qq&utm_term=ut", "kw"},
		{"k=&keyword=kw", "kw"},
		{"k=&q=qq&utm_term=ut", "qq"},
		{"k=&utm_term=ut", "ut"},
	}
	for _, c := range cases {
		if got := aliasKeyword(t, c.query); got != c.want {
			t.Errorf("?%s: logged keyword %q, want %q", c.query, got, c.want)
		}
	}
}

// {keyword} and [KEY] carry the chosen keyword once; {utm_term} stays an
// ordinary click-query placeholder and does not touch the keyword.
func TestKeywordAliasMacros(t *testing.T) {
	gg := linked(&config.Group{
		ID: "c", Status: true, Redirect: "http_redirect",
		Out: "https://o.example/go?k={keyword}&key=[KEY]&t={utm_term}&s={sub_id_1}",
	})
	h, ins, stop := withClickLog(t, gg, nil)
	rec := do(t, h, "/c?utm_term=ut&sub_id_1=s1", "8.8.8.8", chrome)
	if loc := rec.Header().Get("Location"); loc != "https://o.example/go?k=ut&key=ut&t=ut&s=s1" {
		t.Errorf("Location = %q", loc)
	}
	rec = do(t, h, "/c?keyword=kw&q=qq&utm_term=ut&sub_id_1=s1", "8.8.4.4", chrome)
	if loc := rec.Header().Get("Location"); loc != "https://o.example/go?k=kw&key=kw&t=ut&s=s1" {
		t.Errorf("Location = %q", loc)
	}
	stop()
	for _, e := range ins.events {
		if e.Subs[0] != "s1" {
			t.Errorf("sub_id_1 = %q, an alias must not touch it", e.Subs[0])
		}
	}
}

// The legacy pixel returns before the keyword is read: the aliases produce no
// click and the block itself never names them.
func TestKeywordAliasLegacyPixelIgnoresThem(t *testing.T) {
	gg := linked(&config.Group{ID: "c", Status: true, Redirect: "show_text", Out: "x"})
	h, ins, stop := withClickLog(t, gg, func(d *engineDeps) { d.postbackKey = "secret" })
	for _, path := range []string{
		"/c?pb=secret&cid=abc&keyword=kw&utm_term=ut&q=qq",
		"/c?pb=wrong&keyword=kw",
		"/?pb=secret&utm_term=ut",
	} {
		if rec := do(t, h, path, "8.8.8.8", chrome); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Errorf("%s: status %d body %q", path, rec.Code, rec.Body.String())
		}
	}
	stop()
	if len(ins.events) != 0 {
		t.Errorf("the pixel logged %d clicks", len(ins.events))
	}

	raw, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	i := strings.Index(body, `if pb := query.Get("pb")`)
	j := strings.Index(body, "API-client mode")
	if i < 0 || j < i {
		t.Fatal("pixel block not found")
	}
	for _, name := range []string{"keyword", "utm_term", "baseKeyword", `"q"`} {
		if strings.Contains(body[i:j], name) {
			t.Errorf("the pixel block reads %s", name)
		}
	}
}

// extraParams skips only q: keyword and utm_term keep their place among the
// positional parameters, so existing [PAR-n] links read the same values.
func TestKeywordAliasKeepsPositionalParams(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "/g?keyword=a&q=b&utm_term=c&x=d", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := extraParams(req)
	want := []string{"a", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("extraParams = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extraParams = %v, want %v", got, want)
		}
	}
}

// clickAt serves one click on path and returns the logged event.
//
// t is the test, gg the groups, sources the traffic-source book entries and path
// the request path with its query.
func clickAt(t *testing.T, gg *config.Groups, sources []config.TrafficSource, path string) logbuf.Event {
	t.Helper()
	h, ins, stop := withClickLog(t, gg, withSources(t, sources...))
	do(t, h, path, "8.8.8.8", chrome)
	stop()
	if len(ins.events) != 1 {
		t.Fatalf("%s: %d events, want 1", path, len(ins.events))
	}
	return ins.events[0]
}

// A source that binds a hidden-able field to utm_term (or another keyword name)
// keeps that value out of the keyword; the next name in the order is used.
func TestKeywordAliasSkipsNamesBoundToOtherFields(t *testing.T) {
	cases := []struct {
		name      string
		params    map[string]string
		costParam string
		path      string
		wantKey   string
		wantSub1  string
		wantSite  string
	}{
		{name: "sub_id_1 on utm_term, keyword empty", params: map[string]string{"sub_id_1": "utm_term"}, path: "/c?utm_term=SECRET", wantKey: "", wantSub1: "SECRET"},
		{name: "sub_id_1 on utm_term, q sent", params: map[string]string{"sub_id_1": "utm_term"}, path: "/c?utm_term=SECRET&q=x", wantKey: "x", wantSub1: "SECRET"},
		{name: "sub_id_1 on utm_term, keyword sent", params: map[string]string{"sub_id_1": "utm_term"}, path: "/c?utm_term=SECRET&keyword=kw", wantKey: "kw", wantSub1: "SECRET"},
		{name: "sub_id_1 on q keeps q as the keyword", params: map[string]string{"sub_id_1": "q"}, path: "/c?q=x", wantKey: "x", wantSub1: "x"},
		{name: "sub_id_1 on q, utm_term only", params: map[string]string{"sub_id_1": "q"}, path: "/c?utm_term=S", wantKey: "S"},
		{name: "sub_id_1 on utm_term, utm_term only", params: map[string]string{"sub_id_1": "utm_term"}, path: "/c?utm_term=S", wantKey: "", wantSub1: "S"},
		{name: "site on keyword", params: map[string]string{"site": "keyword"}, path: "/c?keyword=SECRET&q=qq", wantKey: "qq", wantSite: "SECRET"},
		{name: "mapped keyword still wins", params: map[string]string{"keyword": "k", "sub_id_1": "utm_term"}, path: "/c?k=src&utm_term=SECRET", wantKey: "src", wantSub1: "SECRET"},
		{name: "mapped keyword empty, bound alias skipped", params: map[string]string{"keyword": "k", "sub_id_1": "utm_term"}, path: "/c?k=&utm_term=SECRET", wantKey: "", wantSub1: "SECRET"},
		{name: "q stays the keyword though it is the price name", costParam: "q", path: "/c?q=12.5", wantKey: "12.5"},
		{name: "cost param", costParam: "utm_term", path: "/c?utm_term=12.5&q=qq", wantKey: "qq"},
		{name: "cost param, nothing else", costParam: "utm_term", path: "/c?utm_term=12.5", wantKey: ""},
	}
	for _, c := range cases {
		gg := linked(&config.Group{ID: "c", Status: true, SourceID: "sc", Redirect: "show_text", Out: "x",
			CostParam: c.costParam, CurrencyParam: "cur"})
		e := clickAt(t, gg, []config.TrafficSource{{ID: "sc", Parameters: c.params}}, c.path)
		if e.Keyword != c.wantKey || e.Subs[0] != c.wantSub1 || e.Site != c.wantSite {
			t.Errorf("%s: keyword %q sub_id_1 %q site %q, want %q %q %q",
				c.name, e.Keyword, e.Subs[0], e.Site, c.wantKey, c.wantSub1, c.wantSite)
		}
	}
}

// redirect=group keeps a keyword or sub id that was already collected and
// fills only the field that is still empty. A name bound on one campaign of
// the chain is not dropped when the other campaign reads it differently.
func TestKeywordAliasKeptAcrossGroupHop(t *testing.T) {
	gg := linked(
		&config.Group{ID: "free2bound", Status: true, Redirect: "stop", Streams: []config.Stream{link("to", "bound", config.Rules{})}},
		&config.Group{ID: "bound", Status: true, SourceID: "sbound", Redirect: "show_text", Out: "x"},
		&config.Group{ID: "bound2free", Status: true, SourceID: "sbound", Redirect: "stop", Streams: []config.Stream{link("to", "free", config.Rules{})}},
		&config.Group{ID: "free", Status: true, Redirect: "show_text", Out: "x"},
	)
	sources := []config.TrafficSource{{ID: "sbound", Parameters: map[string]string{"sub_id_1": "utm_term"}}}

	e := clickAt(t, gg, sources, "/free2bound?utm_term=T")
	if e.Keyword != "T" || e.Subs[0] != "T" {
		t.Errorf("entry keyword, serving group binds utm_term: keyword %q sub_id_1 %q, want T and T", e.Keyword, e.Subs[0])
	}
	e = clickAt(t, gg, sources, "/bound2free?utm_term=T")
	if e.Keyword != "T" || e.Subs[0] != "T" {
		t.Errorf("entry sub id, serving group has no source: keyword %q sub_id_1 %q, want T and T", e.Keyword, e.Subs[0])
	}
}
