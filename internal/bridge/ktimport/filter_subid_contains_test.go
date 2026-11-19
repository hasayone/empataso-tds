package ktimport

import (
	"strings"
	"testing"
)

// TestSubIDSubstringCarried переносит режим подстроки как «содержит».
func TestSubIDSubstringCarried(t *testing.T) {
	rules, ok, approx, why := oneFilter(subFilter("sub_id_2", "contains", `["Fonbet"]`), nil)
	got := rules.SubIDs["sub_id_2"]
	if !ok || approx || why != "" || !got.Contains || got.Flag != 2 || strings.Join(got.Values, "|") != "Fonbet" {
		t.Fatalf("ok=%v approx=%v why=%q got=%+v", ok, approx, why, got)
	}
	rules, ok, _, _ = oneFilter(subFilter("sub_id_2", "not_contains", `["Fonbet"]`), nil)
	if got = rules.SubIDs["sub_id_2"]; !ok || !got.Contains || got.Flag != 1 {
		t.Fatalf("not contains = %+v", got)
	}
	rules, ok, _, _ = oneFilter(subFilter("sub_id_2", "reject", `["Fonbet"]`), nil)
	if got = rules.SubIDs["sub_id_2"]; !ok || got.Contains || got.Flag != 1 {
		t.Fatalf("plain reject must stay exact: %+v", got)
	}
}

// TestSubIDRegexLiterals переносит регулярку из литералов как «содержит».
func TestSubIDRegexLiterals(t *testing.T) {
	cases := []struct {
		name, mode, payload, want string
		approx                    bool
	}{
		{"one literal", "accept", `["/Fonbet/"]`, "Fonbet", true},
		{"alternation", "accept", `["/Fonbet|Bovada|Another One/"]`, "Fonbet|Bovada|Another One", true},
		{"case flag is exact", "accept", `["/Fonbet|Bovada/i"]`, "Fonbet|Bovada", false},
		{"escaped marks", "accept", `["/a\\.b|c\\-d|e\\/f/"]`, "a.b|c-d|e/f", true},
		{"regex mode, bare pattern", "regex", `["Fonbet|Bovada"]`, "Fonbet|Bovada", true},
		{"several values", "accept", `["/Fonbet/","/Bovada/"]`, "Fonbet|Bovada", true},
	}
	for _, c := range cases {
		rules, ok, approx, why := oneFilter(subFilter("sub_id_2", c.mode, c.payload), nil)
		got := rules.SubIDs["sub_id_2"]
		if !ok || !got.Contains || got.Flag != 2 || strings.Join(got.Values, "|") != c.want || approx != c.approx {
			t.Errorf("%s: ok=%v approx=%v why=%q got=%+v", c.name, ok, approx, why, got)
		}
	}
	rules, ok, _, _ := oneFilter(subFilter("sub_id_2", "reject", `["/Fonbet/"]`), nil)
	if got := rules.SubIDs["sub_id_2"]; !ok || got.Flag != 1 || !got.Contains {
		t.Errorf("reject regex = %+v", got)
	}
}

// TestSubIDRealRegexStaysSkipped не теряет настоящую регулярку молча: фильтр пропущен с пояснением.
func TestSubIDRealRegexStaysSkipped(t *testing.T) {
	for _, payload := range []string{
		`["/^Fonbet/"]`, `["/Fonbet$/"]`, `["/Fon.et/"]`, `["/Fon+bet/"]`, `["/[Ff]onbet/"]`,
		`["/(Fonbet|Bovada)/"]`, `["/\\d+/"]`, `["/Fonbet|/"]`, `["/Fonbet/g"]`, `["/Fonbet/","/^x/"]`,
	} {
		rules, ok, _, why := oneFilter(subFilter("sub_id_2", "accept", payload), nil)
		if ok || len(rules.SubIDs) != 0 || !strings.Contains(why, "слишком сложное для «содержит»") {
			t.Errorf("%s: ok=%v why=%q rules=%+v", payload, ok, why, rules.SubIDs)
		}
	}
	_, _, _, losses, _ := mapFilters([]KFilter{subFilter("sub_id_2", "accept", `["/^F/"]`), subFilter("country", "accept", `["US"]`)}, nil, false)
	if len(losses) != 1 || losses[0].Kind != "skipped" || !strings.Contains(losses[0].Detail, "sub_id_2") {
		t.Errorf("losses = %+v", losses)
	}
}

// TestSubIDContainsMergeKeepsKind не склеивает «содержит» с точным правилом одного ключа.
func TestSubIDContainsMergeKeepsKind(t *testing.T) {
	rules, _, _, losses, _ := mapFilters([]KFilter{
		subFilter("sub_id_1", "contains", `["a"]`),
		subFilter("sub_id_1", "accept", `["b"]`),
	}, nil, true)
	got := rules.SubIDs["sub_id_1"]
	if !got.Contains || strings.Join(got.Values, "|") != "a" || len(losses) != 1 || losses[0].Kind != "skipped" {
		t.Fatalf("got=%+v losses=%+v", got, losses)
	}
	rules, _, _, losses, _ = mapFilters([]KFilter{
		subFilter("sub_id_1", "contains", `["a"]`),
		subFilter("sub_id_1", "contains", `["b"]`),
	}, nil, true)
	if got = rules.SubIDs["sub_id_1"]; !got.Contains || strings.Join(got.Values, "|") != "a|b" || len(losses) != 0 {
		t.Fatalf("got=%+v losses=%+v", got, losses)
	}
}
