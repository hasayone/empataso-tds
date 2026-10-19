package config

// maxSubIDValueLen is the longest value of a Sub ID rule, in bytes. The engine
// reads at most this much of a sub id from the query (ApplySource), so a rule
// value and the sub id it is compared with share one limit.
const maxSubIDValueLen = MaxValueBytes

// SubIDRulesError checks the Sub ID rules of r.
//
// Every key must be sub_id_1..sub_id_12, every flag 1 or 2 and every value at
// most 255 bytes. It returns a message for the first fault, or "" when the
// rules are valid or absent. The message is safe to show to the panel user.
func (r Rules) SubIDRulesError() string {
	if len(r.SubIDs) == 0 {
		return ""
	}
	known := make(map[string]bool, MaxRuleSubID)
	for n := 1; n <= MaxRuleSubID; n++ {
		known[SubIDRuleKey(n)] = true
	}
	for key, f := range r.SubIDs {
		if !known[key] {
			return "unknown sub id rule " + key
		}
		if f.Flag != FlagA && f.Flag != FlagB {
			return "sub id rule " + key + ": flag must be 1 or 2"
		}
		for _, v := range f.Values {
			if len(v) > maxSubIDValueLen {
				return "sub id rule " + key + ": value is too long"
			}
		}
	}
	return ""
}
