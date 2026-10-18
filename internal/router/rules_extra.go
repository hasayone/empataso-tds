package router

import (
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// ruleCheck is one condition of a stream for the "any condition" mode: its name,
// whether it is configured, and the rules that hold only that condition.
type ruleCheck struct {
	name string
	on   bool
	rule config.Rules
}

// Visitor.Subs holds the sub ids as the group's traffic source maps them
// (config.ApplySource); Subs[i] is sub_id_<i+1>.

// subIDKeys are the Rules.SubIDs keys sub_id_1..sub_id_12, built once, so the
// click path does not format a key per rule.
var subIDKeys = func() (keys [config.MaxRuleSubID]string) {
	for i := range keys {
		keys[i] = config.SubIDRuleKey(i + 1)
	}
	return keys
}()

// whyExtra checks the query-variable, model and Sub ID conditions of a stream in
// "all conditions" mode.
//
// r is the stream's rules and v the visitor. The query variable is "name"
// (present and non-empty) or "name=value". It returns the name of the first
// failing condition ("get", "model", then "sub_id_1".."sub_id_12" in numeric
// order) or "" when all pass. A stream without Sub ID rules does no per-key work.
// An empty model never matches, so a whitelist drops visitors whose device is unknown.
func whyExtra(r *config.Rules, v Visitor) string {
	if rejectList(r.Query.Flag, len(r.Query.Values) > 0, queryAny(r.Query.Values, v.Query)) {
		return "get"
	}
	if rejectList(r.Model.Flag, len(r.Model.Values) > 0, exactAny(r.Model.Values, v.Model)) {
		return "model"
	}
	if len(r.SubIDs) == 0 {
		return ""
	}
	for i, key := range subIDKeys {
		if f, ok := r.SubIDs[key]; ok && f.Active() && rejectList(f.Flag, true, subIDMatch(f, v.Subs[i])) {
			return key
		}
	}
	return ""
}

// extraChecks lists the model and Sub ID conditions that are configured, for the
// "any condition" mode. r is the stream's rules. Unconfigured conditions are
// not listed.
func extraChecks(r config.Rules) []ruleCheck {
	var out []ruleCheck
	if listOn(r.Model) {
		out = append(out, ruleCheck{name: "model", on: true, rule: config.Rules{Model: r.Model}})
	}
	if len(r.SubIDs) == 0 {
		return out
	}
	for _, key := range subIDKeys {
		if f, ok := r.SubIDs[key]; ok && f.Active() {
			out = append(out, ruleCheck{name: key, on: true, rule: config.Rules{SubIDs: map[string]config.SubIDFilter{key: f}}})
		}
	}
	return out
}

// subIDMatch reports whether a sub id value matches a rule.
//
// f is the rule and value the sub id read from the query. A blank value matches
// when the rule has Empty; any other value matches when it equals one of the
// rule's values, ignoring case and surrounding spaces.
func subIDMatch(f config.SubIDFilter, value string) bool {
	if value = strings.TrimSpace(value); value == "" {
		return f.Empty
	}
	for _, e := range f.Values {
		if strings.EqualFold(strings.TrimSpace(e), value) {
			return true
		}
	}
	return false
}
