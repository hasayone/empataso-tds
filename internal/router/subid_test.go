package router

import (
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

func subRules(m map[string]config.SubIDFilter) config.Rules {
	return config.Rules{SubIDs: m}
}

func subVisitor(subs map[int]string) Visitor {
	v := baseVisitor()
	for n, val := range subs {
		v.Subs[n-1] = val
	}
	return v
}

func TestSubIDWhitelistAndBlacklist(t *testing.T) {
	v := subVisitor(map[int]string{3: "V2439"})
	cases := []struct {
		name string
		f    config.SubIDFilter
		want bool
	}{
		{"whitelist hit, case-insensitive", config.SubIDFilter{Flag: config.FlagB, Values: []string{"v2439"}}, true},
		{"whitelist miss", config.SubIDFilter{Flag: config.FlagB, Values: []string{"other"}}, false},
		{"blacklist hit", config.SubIDFilter{Flag: config.FlagA, Values: []string{"V2439"}}, false},
		{"blacklist miss", config.SubIDFilter{Flag: config.FlagA, Values: []string{"other"}}, true},
		{"prefix is not a match", config.SubIDFilter{Flag: config.FlagB, Values: []string{"V24"}}, false},
	}
	for _, c := range cases {
		s := stream("s", subRules(map[string]config.SubIDFilter{"sub_id_3": c.f}))
		got := Why(&s, v, Deps{}) == ""
		if got != c.want {
			t.Errorf("%s: pass = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSubIDEmptyValue(t *testing.T) {
	blank := subVisitor(nil)
	filled := subVisitor(map[int]string{1: "x"})
	onlyEmpty := config.SubIDFilter{Flag: config.FlagB, Empty: true}
	if s := stream("s", subRules(map[string]config.SubIDFilter{"sub_id_1": onlyEmpty})); Why(&s, blank, Deps{}) != "" || Why(&s, filled, Deps{}) != "sub_id_1" {
		t.Error("a whitelist of the empty value lets a blank sub id and cuts a filled one")
	}
	noEmpty := config.SubIDFilter{Flag: config.FlagA, Empty: true}
	if s := stream("s", subRules(map[string]config.SubIDFilter{"sub_id_1": noEmpty})); Why(&s, blank, Deps{}) != "sub_id_1" || Why(&s, filled, Deps{}) != "" {
		t.Error("a blacklist of the empty value cuts a blank sub id and lets a filled one")
	}
	if s := stream("s", subRules(map[string]config.SubIDFilter{"sub_id_1": {Flag: config.FlagB, Values: []string{"x"}}})); Why(&s, blank, Deps{}) != "sub_id_1" {
		t.Error("a blank sub id never matches a whitelist of values")
	}
}

func TestSubIDInactiveRuleDoesNothing(t *testing.T) {
	v := subVisitor(map[int]string{2: "x"})
	for _, f := range []config.SubIDFilter{
		{},
		{Flag: config.FlagB},
		{Flag: config.FlagOff, Values: []string{"y"}},
	} {
		s := stream("s", subRules(map[string]config.SubIDFilter{"sub_id_2": f}))
		if got := Why(&s, v, Deps{}); got != "" {
			t.Errorf("rule %+v must be inactive, Why = %q", f, got)
		}
	}
}

func TestSubIDAndOr(t *testing.T) {
	v := subVisitor(map[int]string{1: "a", 2: "zzz"})
	rules := subRules(map[string]config.SubIDFilter{
		"sub_id_1": {Flag: config.FlagB, Values: []string{"a"}},
		"sub_id_2": {Flag: config.FlagB, Values: []string{"b"}},
	})
	s := stream("s", rules)
	if got := Why(&s, v, Deps{}); got != "sub_id_2" {
		t.Errorf("AND: Why = %q, want sub_id_2", got)
	}
	s.FilterOr = true
	if got := Why(&s, v, Deps{}); got != "" {
		t.Errorf("OR: one passing sub id must let through, Why = %q", got)
	}
	v.Subs[0] = "nope"
	if got := Why(&s, v, Deps{}); got != "sub_id_1" {
		t.Errorf("OR: no rule passes, Why = %q, want sub_id_1", got)
	}
}

func TestSubIDSelectUsesSourceMappedValue(t *testing.T) {
	g := &config.Group{Streams: []config.Stream{
		stream("vip", subRules(map[string]config.SubIDFilter{"sub_id_1": {Flag: config.FlagB, Values: []string{"vip"}}})),
		stream("rest", config.Rules{}),
	}}
	// The source renamed sub_id_1 to "sub1": ApplySource has put its value into Subs.
	src := &config.TrafficSource{Parameters: map[string]string{"sub_id_1": "sub1"}}
	_, subs := config.ApplySource(map[string][]string{"sub1": {"vip"}, "sub_id_1": {"plain"}}, src)
	v := baseVisitor()
	v.Subs = subs
	picked, ok := Select(g, v, Deps{})
	if !ok || picked.Name != "vip" {
		t.Fatalf("picked = %q ok=%v, want vip", picked.Name, ok)
	}
	_, v.Subs = config.ApplySource(map[string][]string{"sub_id_1": {"vip"}}, src)
	if picked, ok = Select(g, v, Deps{}); !ok || picked.Name != "rest" {
		t.Fatalf("the raw name must not feed the rule when the source renames it, picked = %q", picked.Name)
	}
}
