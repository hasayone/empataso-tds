package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func subFilter(name, mode, payload string) KFilter {
	return KFilter{Name: name, Mode: mode, Payload: json.RawMessage(payload)}
}

func TestSubIDFilterExact(t *testing.T) {
	rules, ok, _, why := oneFilter(subFilter("sub_id_3", "reject", `["a","B"]`), nil)
	if !ok || why != "" {
		t.Fatalf("ok=%v why=%q", ok, why)
	}
	got := rules.SubIDs["sub_id_3"]
	if got.Flag != 1 || strings.Join(got.Values, "|") != "a|B" || rules.Query.Flag != 0 {
		t.Fatalf("sub_ids = %+v get = %+v", rules.SubIDs, rules.Query)
	}
	rules, ok, _, _ = oneFilter(subFilter("sub_id_03", "accept", `["x"]`), nil)
	if !ok || rules.SubIDs["sub_id_3"].Flag != 2 {
		t.Fatalf("leading zero key: %+v", rules.SubIDs)
	}
}

func TestSubIDFilterNotCarried(t *testing.T) {
	cases := map[string]KFilter{
		"substring": subFilter("sub_id_1", "contains", `["a"]`),
		"regex":     subFilter("sub_id_1", "accept", `["/^a/"]`),
		"too high":  subFilter("sub_id_13", "accept", `["a"]`),
		"zero":      subFilter("sub_id_0", "accept", `["a"]`),
	}
	for name, f := range cases {
		if _, ok, _, why := oneFilter(f, nil); ok || why == "" {
			t.Errorf("%s: ok=%v why=%q, want a skipped filter", name, ok, why)
		}
	}
}

func TestSubIDFiltersStayApart(t *testing.T) {
	rules, ok, _, losses := mapFilters([]KFilter{
		subFilter("sub_id_1", "accept", `["a"]`),
		subFilter("sub_id_2", "reject", `["b"]`),
	}, nil, false)
	if !ok || len(losses) != 0 {
		t.Fatalf("ok=%v losses=%+v", ok, losses)
	}
	if rules.SubIDs["sub_id_1"].Flag != 2 || rules.SubIDs["sub_id_2"].Flag != 1 || len(rules.SubIDs) != 2 {
		t.Fatalf("flags must stay separate per key: %+v", rules.SubIDs)
	}
}

func TestSubIDDuplicateKey(t *testing.T) {
	cases := []struct {
		name    string
		or      bool
		a, b    KFilter
		flag    int
		values  string
		skipped bool
	}{
		{"reject twice in AND joins", false, subFilter("sub_id_1", "reject", `["a"]`), subFilter("sub_id_1", "reject", `["b"]`), 1, "a|b", false},
		{"accept twice in OR joins", true, subFilter("sub_id_1", "accept", `["a"]`), subFilter("sub_id_1", "accept", `["b"]`), 2, "a|b", false},
		{"accept twice in AND is skipped", false, subFilter("sub_id_1", "accept", `["a"]`), subFilter("sub_id_1", "accept", `["b"]`), 2, "a", true},
		{"reject twice in OR is skipped", true, subFilter("sub_id_1", "reject", `["a"]`), subFilter("sub_id_1", "reject", `["b"]`), 1, "a", true},
		{"accept and reject is skipped", false, subFilter("sub_id_1", "accept", `["a"]`), subFilter("sub_id_1", "reject", `["b"]`), 2, "a", true},
	}
	for _, c := range cases {
		rules, _, _, losses := mapFilters([]KFilter{c.a, c.b}, nil, c.or)
		got := rules.SubIDs["sub_id_1"]
		if got.Flag != c.flag || strings.Join(got.Values, "|") != c.values {
			t.Errorf("%s: sub_id_1 = %+v", c.name, got)
		}
		if (len(losses) == 1 && losses[0].Kind == "skipped") != c.skipped {
			t.Errorf("%s: losses = %+v, skipped want %v", c.name, losses, c.skipped)
		}
	}
}

func TestQueryFilterKeepsFlagOnMerge(t *testing.T) {
	rules, _, _, losses := mapFilters([]KFilter{
		subFilter("parameter", "accept", `{"name":"utm","value":"a","mode":"accept"}`),
		subFilter("parameter", "reject", `{"name":"utm","value":"b","mode":"reject"}`),
	}, nil, false)
	if rules.Query.Flag != 2 || strings.Join(rules.Query.Values, "|") != "utm=a" {
		t.Fatalf("get = %+v", rules.Query)
	}
	if len(losses) != 1 || losses[0].Kind != "skipped" || !strings.Contains(losses[0].Detail, "get") {
		t.Fatalf("losses = %+v", losses)
	}
}
