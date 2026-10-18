package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIPvFilter(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		payload string
		version int
		ok      bool
	}{
		{name: "ipv4", mode: "accept", payload: `["1"]`, version: 1, ok: true},
		{name: "ipv4", mode: "reject", payload: `["1"]`, version: 2, ok: true},
		{name: "ipv6", mode: "accept", payload: `["1"]`, version: 2, ok: true},
		{name: "ipv6", mode: "reject", payload: `["1"]`, version: 1, ok: true},
		{name: "ipv_4", mode: "accept", payload: `["1"]`, version: 1, ok: true},
		{name: "ipv_6", mode: "accept", payload: `["1"]`, version: 2, ok: true},
		{name: "ip_version", mode: "accept", payload: `"6"`, version: 2, ok: true},
		{name: "ip_version", mode: "accept", payload: `"IPv4"`, version: 1, ok: true},
		{name: "ip_version", mode: "accept", payload: `"x"`, version: 0, ok: false},
		{name: "ip_version", mode: "accept", payload: `6`, version: 2, ok: true},
		{name: "ip_version", mode: "accept", payload: `["ipv6"]`, version: 2, ok: true},
	}
	for _, tc := range cases {
		rules, ok := ipvFilter(KFilter{Name: tc.name, Mode: tc.mode, Payload: json.RawMessage(tc.payload)})
		if ok != tc.ok || rules.IPVersion != tc.version {
			t.Errorf("%s %s %s → %d %v, want %d %v", tc.name, tc.mode, tc.payload, rules.IPVersion, ok, tc.version, tc.ok)
		}
	}
}

func TestProxyReject(t *testing.T) {
	rules, ok, _, why := oneFilter(KFilter{Name: "proxy", Mode: "reject", Payload: json.RawMessage(`["1"]`)}, nil)
	if !ok || rules.Proxy != 2 || why != "" {
		t.Fatalf("proxy %+v ok=%v %s", rules, ok, why)
	}
}

func TestBotProtectionProxyStream(t *testing.T) {
	snap := Snapshot{
		Campaigns: []Campaign{{ID: 1, Alias: "guard", Name: "Guard", State: "active"}},
		Streams: map[string][]KStream{
			"1": {{
				ID: 1, Type: "regular", Name: "bots", State: "active", FilterOr: true,
				Schema: "redirect", ActionType: "http", ActionPayload: "https://a.example/",
				Filters: []KFilter{
					{Name: "proxy", Mode: "accept", Payload: json.RawMessage(`["1"]`)},
					{Name: "ipv_6", Mode: "accept", Payload: json.RawMessage(`["1"]`)},
					{Name: "device_type", Mode: "accept", Payload: json.RawMessage(`["mobile","tablet"]`)},
					{Name: "isp", Mode: "accept", Payload: json.RawMessage(`["Google LLC"]`)},
					{Name: "user_agent", Mode: "accept", Payload: json.RawMessage(`["sdk_gphone","emulator"]`)},
					{Name: "sub_id_3", Mode: "accept", Payload: json.RawMessage(`["V2439"]`)},
					{Name: "ip", Mode: "accept", Payload: json.RawMessage(`["1.2.3.4"]`)},
				},
			}},
		},
	}
	res := MapAll(snap)
	g, ok := findGroup(res.Groups, "guard")
	if !ok || len(g.Streams) != 1 || !g.Streams[0].Status || !g.Streams[0].FilterOr {
		t.Fatalf("stream = %+v", g.Streams)
	}
	rules := g.Streams[0].Rules
	if rules.Proxy != 1 || rules.IPVersion != 2 || rules.Computer != 1 || rules.Phone != 0 || rules.Tablet != 0 {
		t.Fatalf("flags = %+v", rules)
	}
	if rules.Org.Flag != 2 || strings.Join(rules.Org.Values, "|") != "Google LLC" {
		t.Fatalf("org = %+v", rules.Org)
	}
	if rules.UAText.Flag != 2 || strings.Join(rules.UAText.Values, "|") != "sdk_gphone|emulator" {
		t.Fatalf("ua = %+v", rules.UAText)
	}
	if sub := rules.SubIDs["sub_id_3"]; sub.Flag != 2 || strings.Join(sub.Values, "|") != "V2439" || rules.Query.Flag != 0 {
		t.Fatalf("sub_ids = %+v get = %+v", rules.SubIDs, rules.Query)
	}
	body := res.Lists[rules.IPList.File]
	if rules.IPList.Flag != 2 || body != "1.2.3.4\n" || rules.IPList.File != IPListName(body) {
		t.Fatalf("ip_list = %+v %q", rules.IPList, body)
	}
	text := lossesText(res.Losses)
	for _, gone := range []string{"фильтр proxy", "фильтр ipv", "нет фильтра версии IP"} {
		if strings.Contains(text, gone) {
			t.Fatalf("skipped %s\n%s", gone, text)
		}
	}
}
