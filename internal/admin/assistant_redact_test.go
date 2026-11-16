package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// redactJSON runs aiRedactWith on the JSON text in and returns the result as JSON text.
func redactJSON(t *testing.T, in string, secrets ...string) string {
	t.Helper()
	out := aiRedactWith(aiDecode([]byte(in)), secrets)
	raw, err := aiMarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAssistantRedactSecretKeysAreExact(t *testing.T) {
	in := `{"token":"T1","api_key":"K1","apikey":"K2","password":"P1","passwd":"P2","secret":"S1",
		"client_secret":"S2","Authorization":"A1","cookie":"C1","csrf":"X1","hash":"H1","pw_fp":"F1",
		"access_token":"AT","refresh_token":"RT","private_key":"PK",
		"key_text":"keep-key-text","save_keys":true,"cost_token":"{price}","name":"keep-name","token_note":"keep-note"}`
	got := redactJSON(t, in)
	for _, leak := range []string{`"T1"`, `"K1"`, `"K2"`, `"P1"`, `"P2"`, `"S1"`, `"S2"`, `"A1"`, `"C1"`, `"X1"`, `"H1"`, `"F1"`, `"AT"`, `"RT"`, `"PK"`} {
		if strings.Contains(got, leak) {
			t.Errorf("secret value %s survived: %s", leak, got)
		}
	}
	for _, keep := range []string{"keep-key-text", `"save_keys":true`, `"cost_token":"{price}"`, "keep-name", "keep-note"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was touched: %s", keep, got)
		}
	}
	if !aiHasRedactMark(aiDecode([]byte(got))) {
		t.Error("aiHasRedactMark does not see the mark")
	}
}

func TestAssistantRedactEmptySecretKeysStayEmpty(t *testing.T) {
	got := redactJSON(t, `{"token":"","password":null,"hash":false}`)
	if strings.Contains(got, aiRedactMark) {
		t.Errorf("an empty value got the mark: %s", got)
	}
}

func TestAssistantRedactLinks(t *testing.T) {
	long := "aB3dE9fG2hJ5kL8mN1pQ4rS7tU0vW6xY"
	cases := []struct{ in, want string }{
		{"https://user:pw@host.test/a?x=1", "https://***@host.test/a?x=1"},
		{"https://x.test/p/" + long + "?token=CANARY&sig=SIG&gclid=keep&cid={subid}", "https://x.test/p/***?token=***&sig=***&gclid=keep&cid={subid}"},
		{"https://x.test/cb?Signature=S&auth=A&key=K&Api_Key=Z&a=b", "https://x.test/cb?Signature=***&auth=***&key=***&Api_Key=***&a=b"},
		{"https://x.test/postback/" + long + "/?cid={subid}", "https://x.test/postback/***/?cid={subid}"},
		{"https://x.test/landing/my-super-long-landing-name-for-offer/index.html", "https://x.test/landing/my-super-long-landing-name-for-offer/index.html"},
		{"https://x.test/#access_token=ZZZ&state=1", "https://x.test/#access_token=***&state=1"},
		{"see https://x.test/a?token=T1, then", "see https://x.test/a?token=***, then"},
	}
	for _, c := range cases {
		if got := aiRedactText(c.in, nil); got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestAssistantRedactKeyFormats(t *testing.T) {
	for _, secret := range []string{
		"sk-abcdefghijklmnop12345", "sk-ant-api03-abcdefghijklmnop12345", "sk-or-v1-abcdefghijklmnop12345",
		"AIzaSyA1234567890abcdefghijklmnop", "ghp_abcdefghijklmnopqrstuvwx0123", "github_pat_abcdefghijklmnopqrst1234",
	} {
		got := aiRedactText("key is "+secret+" ok", nil)
		if got != "key is *** ok" {
			t.Errorf("%s: %q", secret, got)
		}
	}
	if got := aiRedactText("Authorization: Bearer abc.DEF-123_xyz=", nil); got != "Authorization: Bearer ***" {
		t.Errorf("bearer: %q", got)
	}
	if got := aiRedactText("task-run and sk-short", nil); got != "task-run and sk-short" {
		t.Errorf("plain words changed: %q", got)
	}
}

func TestAssistantRedactLiteralSecrets(t *testing.T) {
	got := aiRedactText("a LITERAL-ONE b LITERAL-TWO c ab", []string{"LITERAL-ONE", "LITERAL-TWO", "ab", ""})
	if got != "a *** b *** c ab" {
		t.Errorf("literals: %q", got)
	}
	got = redactJSON(t, `{"LITERAL-KEY":"x","list":["see LITERAL-ONE"]}`, "LITERAL-KEY", "LITERAL-ONE")
	if strings.Contains(got, "LITERAL") {
		t.Errorf("literal in key or array: %s", got)
	}
}

func TestAssistantRedactIPs(t *testing.T) {
	got := redactJSON(t, `{"ip":"203.0.113.77","client_ip":"198.51.100.4:8080","v6":"2001:db8:abcd:12::1",
		"list":["192.0.2.9"],"note":"ip 10.1.2.3 here","browser_ver":"120.0.6099.109","uip":"x"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"ip": "203.0.113.x", "client_ip": "198.51.100.x:8080", "v6": "2001:db8:abcd::/48",
		"list": []any{"192.0.2.x"}, "note": "ip 10.1.2.x here", "browser_ver": "120.0.6099.109",
	}
	for k, w := range want {
		if gotJSON, _ := json.Marshal(m[k]); string(gotJSON) != aiTestJSON(w) {
			t.Errorf("%s = %s, want %s", k, gotJSON, aiTestJSON(w))
		}
	}
	for _, in := range []string{"::ffff:203.0.113.5", "2001:db8::1", "fe80::1%eth0"} {
		got := aiRedactWith(in, nil).(string)
		if strings.Contains(got, "113.5") || strings.Contains(got, "::1") && !strings.HasSuffix(got, "/48") {
			t.Errorf("%s -> %s", in, got)
		}
	}
	if got := aiRedactWith("::ffff:203.0.113.5", nil); got != "203.0.113.x" {
		t.Errorf("mapped address: %v", got)
	}
}

func aiTestJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAssistantRedactJournalLinks(t *testing.T) {
	in := `{"rows":[{"referer":"https://ref.test/p?gclid=CANARYG&sub=1","page":"/lp/a?x=1#h","url":"https://u.test/z?fbclid=F",
		"note":"see https://n.test/x?utm=1","ip":"203.0.113.9"}]}`
	got := newAIRedactor(nil, true).value(aiDecode([]byte(in)))
	raw, _ := aiMarshal(got)
	text := string(raw)
	for _, leak := range []string{"CANARYG", "sub=1", "x=1", "fbclid", "utm=1", "113.9"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q survived: %s", leak, text)
		}
	}
	for _, keep := range []string{"https://ref.test/p", "/lp/a", "https://u.test/z", "https://n.test/x"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q was lost: %s", keep, text)
		}
	}
	cfg := redactJSON(t, `{"url":"https://x.test/o?cid={subid}&a=1"}`)
	if !strings.Contains(cfg, "cid={subid}&a=1") {
		t.Errorf("config link lost its query: %s", cfg)
	}
}

func TestAssistantRedactTypedValueAndCopy(t *testing.T) {
	type rec struct {
		Name  string `json:"name"`
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	in := []rec{{"a", "TOK", "https://x.test/?key=K"}}
	out := aiRedactWith(in, nil)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "TOK") || strings.Contains(string(raw), "key=K") {
		t.Errorf("typed value leaked: %s", raw)
	}
	if in[0].Token != "TOK" {
		t.Error("the input was changed")
	}
	src := map[string]any{"token": "T", "n": json.Number("12.50")}
	_ = aiRedactWith(src, nil)
	if src["token"] != "T" {
		t.Error("the input map was changed")
	}
	if got, _ := aiMarshal(aiRedactWith(src, nil)); !strings.Contains(string(got), `"n":12.50`) {
		t.Errorf("number changed: %s", got)
	}
}

func TestAssistantRedactMarkDetection(t *testing.T) {
	yes := []any{
		"***", "a *** b", "203.0.113.x", "2001:db8:abcd::/48",
		map[string]any{"a": []any{"x", "198.51.100.x"}}, json.RawMessage(`{"url":"https://x.test/***/"}`),
		[]string{"ok", "***"},
	}
	for _, v := range yes {
		if !aiHasRedactMark(v) {
			t.Errorf("mark not found in %v", v)
		}
	}
	no := []any{
		"plain", "1.2.3.4", "https://x.test/a?cid={subid}", map[string]any{"n": json.Number("5")},
		json.RawMessage(`{"name":"A B"}`), nil, true,
	}
	for _, v := range no {
		if aiHasRedactMark(v) {
			t.Errorf("false mark in %v", v)
		}
	}
}

func TestAssistantRedactContext(t *testing.T) {
	ctx := aiPageContext{
		Page: "logs", Arg: "203.0.113.5", Board: "main", Tab: "clicks", Campaign: "c1",
		Range:   aiRange{ID: "7d", From: "2026-11-01", To: "2026-11-08"},
		Filters: []string{"ip:eq:203.0.113.77", "country:eq:PL", "token:eq:SECRETV", "ip:eq:2001:db8:abcd:12::1", "referer:contains:https://x.test/p?token=T1"},
	}
	got := aiRedactContext(ctx)
	text := aiTestJSON(got)
	for _, leak := range []string{"113.77", "113.5", "SECRETV", "abcd:12", "token=T1"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q survived: %s", leak, text)
		}
	}
	want := []string{"ip:eq:203.0.113.x", "country:eq:PL", "token:eq:***", "ip:eq:2001:db8:abcd::/48", "referer:contains:https://x.test/p?token=***"}
	for i, w := range want {
		if got.Filters[i] != w {
			t.Errorf("filter %d = %q, want %q", i, got.Filters[i], w)
		}
	}
	if got.Range != ctx.Range || got.Page != "logs" || got.Arg != "203.0.113.x" {
		t.Errorf("context fields: %+v", got)
	}
	if len(ctx.Filters) != 5 || ctx.Filters[0] != "ip:eq:203.0.113.77" {
		t.Error("the input context was changed")
	}
	lit := aiRedactContextWith(aiPageContext{Filters: []string{"name:contains:LITSECRET"}}, []string{"LITSECRET"})
	if lit.Filters[0] != "name:contains:***" {
		t.Errorf("literal in a filter: %q", lit.Filters[0])
	}
}

func TestAssistantRedactTokenHeuristic(t *testing.T) {
	for seg, want := range map[string]bool{
		"aB3dE9fG2hJ5kL8mN1pQ4rS7tU0vW6xY":             true,
		"0123456789abcdef0123456789abcdef":             true,
		"short":                                        false,
		"my-super-long-landing-name-for-offer":         false,
		"ABCDEFGHIJKLMNOPQRSTUVWXYZABCD":               false,
		"has space in it 1234567890 abcdefghijklmnopq": false,
	} {
		if got := aiLooksLikeToken(seg); got != want {
			t.Errorf("%q = %v, want %v", seg, got, want)
		}
	}
}

func TestAssistantRedactMoreSecretKeys(t *testing.T) {
	in := `{"key":"K-SECRET","pass":"P-SECRET","pwd":"W-SECRET","x-api-key":"X-SECRET","api-key":"A-SECRET","postback_key":"B-SECRET",
		"Key":"UP-SECRET","keyword":"keep-keyword","key_text":"keep-text","save_keys":true,"cost_token":"{price}",
		"passport":"keep-passport","keys":["keep-keys"],"postback_keys":"keep-pbk","password_hint":"keep-hint"}`
	got := redactJSON(t, in)
	for _, leak := range []string{"K-SECRET", "P-SECRET", "W-SECRET", "X-SECRET", "A-SECRET", "B-SECRET", "UP-SECRET"} {
		if strings.Contains(got, leak) {
			t.Errorf("%s survived: %s", leak, got)
		}
	}
	for _, keep := range []string{"keep-keyword", "keep-text", `"save_keys":true`, `"cost_token":"{price}"`, "keep-passport", "keep-keys", "keep-pbk", "keep-hint"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was touched: %s", keep, got)
		}
	}
	row := redactJSON(t, `{"rows":[{"key":"organic","count":12},{"key":"PL","count":3}]}`)
	if !strings.Contains(row, `"key":"organic"`) || !strings.Contains(row, `"key":"PL"`) || strings.Contains(row, aiRedactMark) {
		t.Errorf("a breakdown row lost its key: %s", row)
	}
	if f := newAIRedactor(nil, false).filter("key:eq:K-SECRET"); f != "key:eq:***" {
		t.Errorf("filter on key: %q", f)
	}
}

func TestAssistantRedactIPsInsideText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"via 203.0.113.77 proxy", "via 203.0.113.x proxy"},
		{"203.0.113.77, 198.51.100.4", "203.0.113.x, 198.51.100.x"},
		{"from 203.0.113.77:8080 ok", "from 203.0.113.x:8080 ok"},
		{"ip:203.0.113.77.", "ip:203.0.113.x."},
		{"via 2001:db8:abcd:12::1 proxy", "via 2001:db8:abcd::/48 proxy"},
		{"v6 [2001:db8:abcd:12::1]:443", "v6 [2001:db8:abcd::/48]:443"},
		{"http://203.0.113.77/path?src=198.51.100.4&x=1", "http://203.0.113.x/path?src=198.51.100.x&x=1"},
		{"Mozilla/5.0 (X11) 203.0.113.9", "Mozilla/5.0 (X11) 203.0.113.x"},
		{"2001:db8:abcd::/48", "2001:db8:abcd::/48"},
		{"203.0.113.x", "203.0.113.x"},
	}
	for _, c := range cases {
		if got := aiRedactWith(c.in, nil); got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	for _, same := range []string{
		"1.2.3", "v1.2.3", "2026-11-16", "2026.11.16", "12:30:45", "10:20", "3.14", "Chrome/120.0.0.0 Safari/537.36",
		"(Chrome/120.0.6099.109)", "0.0.0.0", "std::string", "Foo::bar", "aa:bb:cc:dd:ee:ff", "price 1.5.2 usd", "1.2.3.4.5",
	} {
		if got := aiRedactWith(same, nil); got != same {
			t.Errorf("%q was changed to %q", same, got)
		}
	}
	if got := aiRedactWith(aiRedactWith("via 203.0.113.77 and 2001:db8:abcd:12::1", nil), nil); got != "via 203.0.113.x and 2001:db8:abcd::/48" {
		t.Errorf("masking twice: %q", got)
	}
}

func TestAssistantRedactIPsInEveryField(t *testing.T) {
	got := redactJSON(t, `{"via":"1.1.1.1, 203.0.113.77","keyword":"buy from 198.51.100.4","subs":["a 192.0.2.9","b"],
		"useragent":"Mozilla/5.0 Chrome/120.0.0.0 via 203.0.113.5","out":"https://x.test/go?ip=198.51.100.7&a=1",
		"referer":"https://203.0.113.20/p","n":12,"version":"1.2.3"}`)
	for _, leak := range []string{"1.1.1.1", "113.77", "100.4", "0.2.9", "113.5", "100.7", "113.20"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	for _, keep := range []string{"Chrome/120.0.0.0", `"n":12`, `"version":"1.2.3"`, "a=1", "buy from 198.51.100.x"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q was touched: %s", keep, got)
		}
	}
	journal, _ := aiMarshal(newAIRedactor(nil, true).value(aiDecode([]byte(`{"out":"http://203.0.113.77/a?x=1","referer":"https://198.51.100.4/p#f"}`))))
	if strings.Contains(string(journal), "113.77") || strings.Contains(string(journal), "100.4/") || !strings.Contains(string(journal), "203.0.113.x/a") {
		t.Errorf("journal link host: %s", journal)
	}
}

func TestAssistantRedactIPsAsMapKeys(t *testing.T) {
	got := redactJSON(t, `{"203.0.113.77":5,"203.0.113.78":7,"198.51.100.4":2,"2001:db8:abcd:12::1":"v6","note via 192.0.2.9":"x"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"203.0.113.x": 12.0, "198.51.100.x": 2.0, "2001:db8:abcd::/48": "v6", "note via 192.0.2.x": "x"}
	if len(m) != len(want) {
		t.Fatalf("keys %v, want %v", m, want)
	}
	for k, w := range want {
		if m[k] != w {
			t.Errorf("%q = %v, want %v", k, m[k], w)
		}
	}
	text := redactJSON(t, `{"203.0.113.77":"a","203.0.113.78":"b"}`)
	if !strings.Contains(text, `"203.0.113.x":"a"`) || !strings.Contains(text, `"203.0.113.x #2":"b"`) {
		t.Errorf("colliding keys lost data: %s", text)
	}
}

func TestAssistantRedactContextIPsEverywhere(t *testing.T) {
	got := aiRedactContext(aiPageContext{Page: "logs", Arg: "via 203.0.113.5 ok", Campaign: "2001:db8:abcd:12::1",
		Filters: []string{"keyword:contains:from 198.51.100.4", "plain 192.0.2.9", "useragent:eq:Chrome/120.0.0.0"}})
	text := aiTestJSON(got)
	for _, leak := range []string{"113.5", "abcd:12", "100.4", "0.2.9"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q survived: %s", leak, text)
		}
	}
	if got.Filters[0] != "keyword:contains:from 198.51.100.x" || got.Filters[2] != "useragent:eq:Chrome/120.0.0.0" {
		t.Errorf("filters: %v", got.Filters)
	}
}

func TestAssistantRedactMarkSeesTruncation(t *testing.T) {
	yes := []any{
		strings.Repeat("x", 70) + "…", map[string]any{"name": strings.Repeat("н", 40) + "…"},
		map[string]any{"items": []any{"a"}, "total": 9, "truncated": true},
		map[string]any{"rows": []any{}, "rows_total": json.Number("40"), "truncated": true},
		json.RawMessage(`{"a":{"truncated":true,"x_total":3}}`),
	}
	for _, v := range yes {
		if !aiHasRedactMark(v) {
			t.Errorf("truncation not seen in %v", v)
		}
	}
	no := []any{
		"wait... what", "...", "Привет...", "Акция…", "…", "cut text…", "Акция… лето", map[string]any{"name": "Акция…"}, map[string]any{"truncated": false, "total": 3},
		map[string]any{"truncated": true}, map[string]any{"total": 3}, map[string]any{"name": "a_total"},
	}
	for _, v := range no {
		if aiHasRedactMark(v) {
			t.Errorf("false truncation mark in %v", v)
		}
	}
	rows := make([]any, 400)
	for i := range rows {
		rows[i] = map[string]any{"id": i, "name": strings.Repeat("n", 40)}
	}
	out, err := aiCompress(map[string]any{"rows": rows, "title": strings.Repeat("t", 9000)}, aiResultMax)
	if err != nil {
		t.Fatal(err)
	}
	if !aiHasRedactMark(out) {
		t.Errorf("the output of aiCompress is not recognised as reduced: %s", aiTestJSON(out)[:200])
	}
	if !strings.Contains(aiTestJSON(out), aiTruncMark) {
		t.Error("aiCompress does not use aiTruncMark for shortened strings")
	}
}
