package config

import (
	"encoding/json"
	"testing"
)

func TestSubIDFilterActive(t *testing.T) {
	cases := []struct {
		name string
		f    SubIDFilter
		want bool
	}{
		{"zero value", SubIDFilter{}, false},
		{"flag without values", SubIDFilter{Flag: FlagB}, false},
		{"values without flag", SubIDFilter{Values: []string{"a"}}, false},
		{"whitelist of values", SubIDFilter{Flag: FlagB, Values: []string{"a"}}, true},
		{"blacklist of empty", SubIDFilter{Flag: FlagA, Empty: true}, true},
	}
	for _, c := range cases {
		if got := c.f.Active(); got != c.want {
			t.Errorf("%s: Active = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSubIDRuleKey(t *testing.T) {
	if SubIDRuleKey(1) != "sub_id_1" || SubIDRuleKey(MaxRuleSubID) != "sub_id_12" {
		t.Fatalf("keys = %q %q", SubIDRuleKey(1), SubIDRuleKey(MaxRuleSubID))
	}
	if MaxRuleSubID > MaxSubID {
		t.Fatalf("MaxRuleSubID %d exceeds MaxSubID %d", MaxRuleSubID, MaxSubID)
	}
}

func TestRulesSubIDsJSON(t *testing.T) {
	in := Rules{SubIDs: map[string]SubIDFilter{"sub_id_2": {Flag: FlagA, Values: []string{"x"}, Empty: true}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Rules
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := out.SubIDs["sub_id_2"]
	if got.Flag != FlagA || len(got.Values) != 1 || got.Values[0] != "x" || !got.Empty {
		t.Fatalf("round trip = %+v from %s", out.SubIDs, raw)
	}
	var bare Rules
	if raw, _ = json.Marshal(bare); string(raw) != "{}" && !jsonHasNoSubIDs(raw) {
		t.Fatalf("empty rules must not write sub_ids: %s", raw)
	}
}

func TestSubIDRulesError(t *testing.T) {
	good := SubIDFilter{Flag: FlagB, Values: []string{"a"}}
	if msg := (Rules{}).SubIDRulesError(); msg != "" {
		t.Errorf("no rules: %q", msg)
	}
	if msg := (Rules{SubIDs: map[string]SubIDFilter{"sub_id_12": good}}).SubIDRulesError(); msg != "" {
		t.Errorf("valid rule: %q", msg)
	}
	for _, key := range []string{"sub_id_13", "sub_id_0", "sub_id_", "SUB_ID_1", "get"} {
		if msg := (Rules{SubIDs: map[string]SubIDFilter{key: good}}).SubIDRulesError(); msg == "" {
			t.Errorf("key %q accepted", key)
		}
	}
	if msg := (Rules{SubIDs: map[string]SubIDFilter{"sub_id_1": {Flag: 0}}}).SubIDRulesError(); msg == "" {
		t.Error("flag 0 accepted")
	}
}

func jsonHasNoSubIDs(raw []byte) bool {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	_, has := m["sub_ids"]
	return !has
}
