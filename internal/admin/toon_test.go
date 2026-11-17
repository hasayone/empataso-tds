package admin

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/toon-format/toon-go"

	"github.com/hasayone/empataso-tds/internal/security"
)

// TestToonFromJSONSpecExamples checks the encoder against the examples of the TOON specification
// (Appendix A) and the encoding rules of its sections 2, 7 and 9.
func TestToonFromJSONSpecExamples(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"object", `{"id":123,"name":"Ada","active":true}`, "id: 123\nname: Ada\nactive: true"},
		{"nested object", `{"user":{"id":123,"name":"Ada"}}`, "user:\n  id: 123\n  name: Ada"},
		{"primitive array", `{"tags":["admin","ops","dev"]}`, "tags[3]: admin,ops,dev"},
		{"array of arrays", `{"pairs":[[1,2],[3,4]]}`, "pairs[2]:\n  - [2]: 1,2\n  - [2]: 3,4"},
		{"tabular", `{"items":[{"sku":"A1","qty":2,"price":9.99},{"sku":"B2","qty":1,"price":14.5}]}`,
			"items[2]{sku,qty,price}:\n  A1,2,9.99\n  B2,1,14.5"},
		{"nested field group", `{"orders":[{"id":1,"customer":{"name":"Ada","country":"DK"},"total":99},{"id":2,"customer":{"name":"Bob","country":"UK"},"total":149}]}`,
			"orders[2]{id,customer{name,country},total}:\n  1,Ada,DK,99\n  2,Bob,UK,149"},
		{"keyed tabular", `{"users":{"alice":{"age":30,"city":"Berlin"},"bob":{"age":25,"city":"Oslo"}}}`,
			"users[2:]{age,city}:\n  alice: 30,Berlin\n  bob: 25,Oslo"},
		{"mixed array", `{"items":[1,{"a":1},"text"]}`, "items[3]:\n  - 1\n  - a: 1\n  - text"},
		{"missing field becomes null", `{"items":[{"id":1,"name":"First"},{"id":2,"name":"Second","extra":true}]}`,
			"items[2]{id,name,extra}:\n  1,First,null\n  2,Second,true"},
		{"list of primitives becomes a JSON cell", `{"items":[{"id":1,"geo":["ru","kz"]},{"id":2,"geo":[]}]}`,
			"items[2]{id,geo}:\n  1,\"[\\\"ru\\\",\\\"kz\\\"]\"\n  2,\"[]\""},
		{"objects as list items", `{"items":[{"id":1,"subs":[{"n":1}]},{"id":2,"name":"Second"}]}`,
			"items[2]:\n  - id: 1\n    subs[1]{n}:\n      1\n  - id: 2\n    name: Second"},
		{"quoted colons", `{"links":[{"id":1,"url":"http://a:b"},{"id":2,"url":"https://example.com?q=a:b"}]}`,
			"links[2]{id,url}:\n  1,\"http://a:b\"\n  2,\"https://example.com?q=a:b\""},
		{"edge values", `{"name":"","tags":[],"version":"123","enabled":"true","none":null,"off":false}`,
			"name: \"\"\ntags: []\nversion: \"123\"\nenabled: \"true\"\nnone: null\noff: false"},
		{"canonical numbers", `{"a":1e6,"b":1.5000,"c":-0,"d":0.000001,"e":1.0,"f":-12}`,
			"a: 1000000\nb: 1.5\nc: 0\nd: 0.000001\ne: 1\nf: -12"},
		{"big integer stays exact", `{"id":9007199254740993,"huge":123456789012345678901234567890}`,
			"id: \"9007199254740993\"\nhuge: \"123456789012345678901234567890\""},
		{"key quoting", `{"my-key":[1,2,3],"x y":1,"ключ":"значение","a.b":1}`,
			"\"my-key\"[3]: 1,2,3\n\"x y\": 1\n\"ключ\": значение\na.b: 1"},
		{"string escaping", `{"s":"a\"b\\c\nd","t":" lead","u":"-x","v":"#tag","w":"a,b","x":"[1]"}`,
			"s: \"a\\\"b\\\\c\\nd\"\nt: \" lead\"\nu: \"-x\"\nv: \"#tag\"\nw: \"a,b\"\nx: \"[1]\""},
		{"unicode", `{"message":"Hello 世界 👋","html":"<b>&</b>"}`, "message: Hello 世界 👋\nhtml: <b>&</b>"},
		{"root tabular", `[{"id":1},{"id":2}]`, "[2]{id}:\n  1\n  2"},
		{"root primitive array", `["a","b"]`, "[2]: a,b"},
		{"root empty object", `{}`, ""},
		{"root empty array", `[]`, "[]"},
		{"root primitive", `"ok"`, "ok"},
		{"error answer", `{"error":"not found","code":"not_found"}`, "error: not found\ncode: not_found"},
	}
	for _, c := range cases {
		got, err := toonFromJSON([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestToonFromJSONRefusesBadInput(t *testing.T) {
	for _, in := range []string{``, `{`, `{"a":1}{"b":2}`, `[1,]`, `nope`} {
		if _, err := toonFromJSON([]byte(in)); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestWantsTOON(t *testing.T) {
	cases := []struct {
		query, accept string
		want          bool
	}{
		{"", "", false},
		{"format=toon", "", true},
		{"format=TOON", "", true},
		{"format=json", "text/toon", false},
		{"format=xml", "", false},
		{"format=xml", "text/toon", true},
		{"", "text/toon", true},
		{"", "application/json, text/toon;q=0.9", true},
		{"", "Text/TOON; charset=utf-8", true},
		{"", "text/toon;q=0", false},
		{"", "application/json", false},
		{"", "*/*", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/groups?"+c.query, nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		if got := wantsTOON(r); got != c.want {
			t.Errorf("query %q accept %q: got %v", c.query, c.accept, got)
		}
	}
}

func TestWriteJSONFollowsTheMark(t *testing.T) {
	v := map[string]any{"items": []any{map[string]any{"id": "a", "n": 1}, map[string]any{"id": "b", "n": 2}}}
	plain := httptest.NewRecorder()
	writeJSON(plain, http.StatusOK, v)
	if ct := plain.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("plain content type %q", ct)
	}
	marked := httptest.NewRecorder()
	writeJSON(&statusWriter{ResponseWriter: &toonWriter{ResponseWriter: marked}}, http.StatusCreated, v)
	if marked.Code != http.StatusCreated || marked.Header().Get("Content-Type") != toonContentType {
		t.Fatalf("marked %d %q", marked.Code, marked.Header().Get("Content-Type"))
	}
	if got := marked.Body.String(); got != "items[2]{id,n}:\n  a,1\n  b,2" {
		t.Errorf("marked body %q", got)
	}
}

// toonTestServer is newTestServer with a Keitaro key, so a secret exists that no answer may show.
func toonTestServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Stats: fakeStats{}, Groups: &fakeGroups{}, Limiter: allowAll{}, SessionTTL: time.Hour,
		KeitaroBase: "https://tracker.example", KeitaroKey: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// toonGet sends GET path with the cookie client c and the given Accept header (none when empty).
// It returns the status, the Content-Type and the body.
func toonGet(t *testing.T, c *http.Client, url, accept string) (int, string, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), body
}

// TestToonAnswersRoundTrip asks real routes for JSON and for TOON, decodes the TOON with the
// reference decoder and requires the same value, status and no leaked secret.
func TestToonAnswersRoundTrip(t *testing.T) {
	const secret = "kt-secret-value-0123456789"
	srv := toonTestServer(t, secret)
	c := newClient(t)
	login(t, c, srv.URL, "admin", "p@ss")
	paths := []string{
		"/api/groups", "/api/logs?from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z", "/api/stats/performance",
		"/api/stats/summary", "/api/report?from=2026-11-01&to=2026-11-02", "/api/offers", "/api/keitaro/settings",
		"/api/me", "/api/no-such-route", "/api/openapi.json",
	}
	for _, p := range paths {
		sep := "?"
		if strings.Contains(p, "?") {
			sep = "&"
		}
		jsonCode, jsonType, jsonBody := toonGet(t, c, srv.URL+p, "")
		for _, ask := range []struct{ url, accept string }{{srv.URL + p + sep + "format=toon", ""}, {srv.URL + p, "text/toon"}} {
			code, ctype, body := toonGet(t, c, ask.url, ask.accept)
			if code != jsonCode {
				t.Errorf("%s: status %d, JSON %d", ask.url, code, jsonCode)
			}
			if !strings.HasPrefix(jsonType, "application/json") {
				continue
			}
			if ctype != toonContentType {
				t.Errorf("%s: content type %q", ask.url, ctype)
				continue
			}
			if bytes.Contains(body, []byte(secret)) {
				t.Errorf("%s: the secret leaked into TOON", ask.url)
			}
			got, err := toon.Decode(body)
			if err != nil {
				t.Errorf("%s: reference decoder: %v\n%s", ask.url, err, body)
				continue
			}
			var want any
			if err := json.Unmarshal(jsonBody, &want); err != nil {
				t.Fatalf("%s: JSON: %v", p, err)
			}
			if obj, ok := want.(map[string]any); ok && len(obj) == 0 {
				want = map[string]any{}
			}
			if !reflect.DeepEqual(toonUnpad(got, true), toonUnpad(want, false)) {
				t.Errorf("%s: TOON decodes to\n%v\nJSON is\n%v", ask.url, got, want)
			}
		}
	}
}

// toonUnpad undoes the table padding of toonTable in a decoded value v: it drops null object
// fields and, when cells is true, turns a string that holds a JSON list back into the list.
func toonUnpad(v any, cells bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, f := range t {
			if f != nil {
				out[k] = toonUnpad(f, cells)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			out[i] = toonUnpad(el, cells)
		}
		return out
	case string:
		var list []any
		if cells && strings.HasPrefix(t, "[") && json.Unmarshal([]byte(t), &list) == nil {
			return list
		}
	}
	return v
}

// TestToonTableRoundTrip pads records with optional fields and lists of primitives into one table:
// the reference decoder, with nulls dropped and JSON cells parsed, gives back the source JSON.
func TestToonTableRoundTrip(t *testing.T) {
	in := `{"items":[{"id":"o1","name":"A","countries":["ru","kz"],"payout":12.5},` +
		`{"id":"o2","enabled":true,"countries":[]},{"name":"C","subs":[1,"x",true],"id":"o3"}],"total":3,` +
		`"groups":[{"id":"g1","flows":[{"id":"f1"},{"id":"f2","weight":5}]},{"id":"g2","geo":{"c":"RU"}}]}`
	out, err := toonFromJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "items[3]{id,name,countries,payout,enabled,subs}:\n") {
		t.Errorf("records are not one table:\n%s", out)
	}
	if !strings.Contains(string(out), "flows[2]{id,weight}:") || !strings.Contains(string(out), "groups[2]:\n  - ") {
		t.Errorf("nested records changed shape:\n%s", out)
	}
	got, err := toon.Decode(out)
	if err != nil {
		t.Fatalf("reference decoder: %v\n%s", err, out)
	}
	var want any
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(toonUnpad(got, true), want) {
		t.Errorf("TOON decodes to\n%v\nJSON is\n%v", toonUnpad(got, true), want)
	}
}

func TestToonErrorsAndPassThrough(t *testing.T) {
	srv := toonTestServer(t, "kt-secret-value-0123456789")
	anon := newClient(t)
	code, ctype, body := toonGet(t, anon, srv.URL+"/api/me?format=toon", "")
	if code != http.StatusUnauthorized || ctype != toonContentType || string(body) != "code: unauthorized\nerror: unauthorized" {
		t.Errorf("unauthorized: %d %q %q", code, ctype, body)
	}
	c := newClient(t)
	login(t, c, srv.URL, "admin", "p@ss")
	code, ctype, body = toonGet(t, c, srv.URL+"/api/no-such-route", "text/toon")
	if code != http.StatusNotFound || ctype != toonContentType || string(body) != "code: not_found\nerror: not found" {
		t.Errorf("not found: %d %q %q", code, ctype, body)
	}
	if _, ctype, _ = toonGet(t, c, srv.URL+"/api/groups?format=json", "text/toon"); !strings.HasPrefix(ctype, "application/json") {
		t.Errorf("format=json did not win over Accept: %q", ctype)
	}
	if _, ctype, _ = toonGet(t, c, srv.URL+"/api/groups?format=yaml", ""); !strings.HasPrefix(ctype, "application/json") {
		t.Errorf("unknown format: %q", ctype)
	}
	if _, ctype, _ = toonGet(t, c, srv.URL+"/api/logs/export?format=toon", ""); strings.HasPrefix(ctype, "text/toon") {
		t.Errorf("CSV export converted: %q", ctype)
	}
	if _, ctype, _ = toonGet(t, c, srv.URL+"/?format=toon", "text/toon"); !strings.HasPrefix(ctype, "text/html") {
		t.Errorf("SPA page: %q", ctype)
	}
}

func TestToonIsGzipped(t *testing.T) {
	srv := toonTestServer(t, "kt-secret-value-0123456789")
	c := newClient(t)
	login(t, c, srv.URL, "admin", "p@ss")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/groups?format=toon", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Content-Type") != toonContentType {
		t.Fatalf("headers %v", resp.Header)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !strings.HasPrefix(string(body), "[1]") {
		t.Errorf("body %q", body)
	}
}

func TestAIToolTextPicksTheShorterForm(t *testing.T) {
	table := `{"items":[{"id":"o1","name":"A","clicks":10},{"id":"o2","name":"B","clicks":20}],"total":2}`
	if got := aiToolText([]byte(table)); got != "items[2]{id,name,clicks}:\n  o1,A,10\n  o2,B,20\ntotal: 2" {
		t.Errorf("table: %q", got)
	}
	deep := `{"a":{"b":{"c":{"d":{"e":1}}}}}`
	if got := aiToolText([]byte(deep)); got != deep {
		t.Errorf("deep object should stay JSON: %q", got)
	}
	if got := aiToolText([]byte("not json")); got != "not json" {
		t.Errorf("bad input: %q", got)
	}
}

// toonSavingsFixtures are typical answers: campaigns, offers, a report and a click journal.
func toonSavingsFixtures() map[string]any {
	rows := func(n int, row func(i int) map[string]any) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = row(i)
		}
		return out
	}
	return map[string]any{
		"groups (20)": rows(20, func(i int) map[string]any {
			return map[string]any{"id": fmt.Sprintf("g%d", i), "name": fmt.Sprintf("Campaign %d", i), "alias": fmt.Sprintf("promo-%d", i),
				"enabled": i%3 != 0, "board_id": "main", "streams": i % 5, "created_at": "2026-11-01T10:00:00Z"}
		}),
		"offers (50)": map[string]any{"items": rows(50, func(i int) map[string]any {
			return map[string]any{"id": fmt.Sprintf("o%d", i), "name": fmt.Sprintf("Offer %d", i), "url": fmt.Sprintf("https://offer.example/%d?sub={subid}", i),
				"country": "RU", "payout": 12.5, "network": "net1", "enabled": true}
		}), "total": 50, "offset": 0, "limit": 50},
		"report (100)": map[string]any{"rows": rows(100, func(i int) map[string]any {
			return map[string]any{"campaign": fmt.Sprintf("Campaign %d", i%20), "country": "PL", "clicks": 1000 + i, "unique": 800 + i,
				"bots": i, "conversions": i % 7, "revenue": float64(i) * 1.25}
		}), "total": 100},
		"logs (100)": map[string]any{"rows": rows(100, func(i int) map[string]any {
			return map[string]any{"time": "2026-11-16T12:00:00Z", "ip": fmt.Sprintf("203.0.113.%d", i), "country": "DE", "group": "g1",
				"stream": "s1", "bot": false, "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", "click_id": fmt.Sprintf("c%06d", i)}
		}), "total": 100},
		"offers with optional fields (50)": map[string]any{"items": rows(50, func(i int) map[string]any {
			row := map[string]any{"id": fmt.Sprintf("o%d", i), "name": fmt.Sprintf("Offer %d", i), "url": fmt.Sprintf("https://offer.example/%d", i), "enabled": true}
			if i%2 == 0 {
				row["payout"] = 12.5
			}
			if i%3 == 0 {
				row["countries"] = []any{"ru", "kz"}
			}
			return row
		}), "total": 50},
		"one campaign": map[string]any{"id": "g1", "name": "Campaign 1", "enabled": true,
			"streams": rows(2, func(i int) map[string]any {
				return map[string]any{"id": fmt.Sprintf("s%d", i), "filters": map[string]any{"country": []any{"RU", "KZ"}}, "weight": 50}
			})},
	}
}

// TestToonSavings measures how much shorter TOON is than compact JSON on typical answers and
// logs the table: characters and an estimate of tokens (characters / 4). Uniform tables must
// save at least 30%, records with optional or list fields included, since toonTable pads them into a table.
func TestToonSavings(t *testing.T) {
	var report strings.Builder
	report.WriteString("\n| answer | JSON chars | TOON chars | ~JSON tokens | ~TOON tokens | saved |\n|---|---|---|---|---|---|\n")
	for name, v := range toonSavingsFixtures() {
		raw, err := aiMarshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out, err := toonFromJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		saved := 100 - 100*len(out)/len(raw)
		fmt.Fprintf(&report, "| %s | %d | %d | %d | %d | %d%% |\n", name, len(raw), len(out), len(raw)/4, len(out)/4, saved)
		if strings.Contains(name, "(") && saved < 30 {
			t.Errorf("%s: TOON saves only %d%%", name, saved)
		}
	}
	t.Log(report.String())
}

func TestDocsMarkdownCarriesTheTOONSection(t *testing.T) {
	for _, lang := range docsLangs {
		md := docsMarkdown(nil, lang)
		if !strings.Contains(md, "## ") || !strings.Contains(md, "?format=toon") || !strings.Contains(md, "text/toon") {
			t.Errorf("%s: no TOON section in the Markdown reference", lang)
		}
	}
}

func TestDocsTOONSampleMatchesTheEncoder(t *testing.T) {
	js := `{"items":[{"id":"o1","name":"Casino RU","country":"RU","payout":12.5,"enabled":true},` +
		`{"id":"o2","name":"Betting UA","country":"UA","payout":8,"enabled":false},` +
		`{"id":"o3","name":"Slots KZ","country":"KZ","payout":10.25,"enabled":true}],"total":3}`
	out, err := toonFromJSON([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	literal := strings.ReplaceAll(string(out), "\n", `\n`)
	if !bytes.Contains(indexHTML, []byte("const TOON_DOC_TOON='"+literal+"';")) {
		t.Errorf("TOON_DOC_TOON in index.html is not %q", literal)
	}
}
