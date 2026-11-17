package admin

import (
	"math"
	"net/http"
	"sort"
	"strings"
)

// Scopes of an ai_quota error caused by a USD budget, the "scope" param of the error.
const (
	aiScopeUserBudget  = "user_budget"
	aiScopeGroupBudget = "group_budget"
)

// aiBudgetIDMax bounds the length of a user or role id in a budget map.
const aiBudgetIDMax = 128

// aiBudgetUser is one account the panel can set a USD budget for: the usage id and the login.
type aiBudgetUser struct {
	UID   string `json:"uid"`
	Login string `json:"login"`
}

// aiBudgetGroup is one group of users (a role of roles.json) in the settings view: the role
// id, its name, the number of accounts that hold it and the USD they spent on the current day.
type aiBudgetGroup struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Users int     `json:"users"`
	USD   float64 `json:"usd"`
}

// aiBudgetOK reports whether v is a usable budget: finite and above zero.
func aiBudgetOK(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0
}

// aiSanitizeBudgets returns a copy of in without the entries that cannot be a budget (an empty
// id, a non-positive or non-finite amount) and with amounts above aiUSDCap cut to it, so a
// hand-edited ai.json cannot widen or break a limit. It returns nil when nothing is left.
func aiSanitizeBudgets(in map[string]float64) map[string]float64 {
	var out map[string]float64
	for id, v := range in {
		if id == "" || !aiBudgetOK(v) {
			continue
		}
		if out == nil {
			out = make(map[string]float64, len(in))
		}
		out[id] = min(v, aiUSDCap)
	}
	return out
}

// aiBudgetsOrEmpty returns m, or an empty map for nil, so the settings view encodes {} and not null.
func aiBudgetsOrEmpty(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}

// aiCleanBudgets checks the budget map of a PUT. field names the request field in the errors.
// An entry with 0 means no limit and is dropped; an id must be 1..aiBudgetIDMax printable ASCII
// characters. It returns ai_bad_limit for a negative, non-finite or too large amount, ai_bad_request
// (reason invalid) for a bad id and ai_bad_request (reason too_many) for more than aiMaxModels
// entries. An empty result is nil.
func aiCleanBudgets(field string, in map[string]float64) (map[string]float64, error) {
	if len(in) > aiMaxModels {
		return nil, aiBadRequest(field, aiReasonTooMany, "too many entries in "+field)
	}
	out := make(map[string]float64, len(in))
	for id, v := range in {
		id = strings.TrimSpace(id)
		if !aiPlainASCII(id, aiBudgetIDMax) {
			return nil, aiBadRequest(field, aiReasonInvalid, field+" holds a bad id")
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > aiUSDCap {
			return nil, aiBadLimit(field, 0, aiUSDCap)
		}
		if v > 0 {
			out[id] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// aiBudgetErr builds the 429 ai_quota error of a reached USD budget; scope is aiScopeUserBudget
// or aiScopeGroupBudget and travels in the params so the panel words the refusal itself.
func aiBudgetErr(scope string) *aiError {
	what := "the USD budget of the user"
	if scope == aiScopeGroupBudget {
		what = "the USD budget of the group"
	}
	return newAIErrorP(http.StatusTooManyRequests, aiErrQuota, what+" limit is reached", map[string]any{"scope": scope})
}

// aiGroupOf returns the group of the account uid: the role id of its users.json record, or ""
// for the environment administrator, a key user and an unknown id.
func (s *Server) aiGroupOf(uid string) string {
	if u, ok := s.userByID(uid); ok {
		return u.RoleID
	}
	return ""
}

// aiGroupMembers returns the ids of the accounts of users.json that hold the role id.
func (s *Server) aiGroupMembers(id string) map[string]bool {
	out := map[string]bool{}
	if s.users == nil || id == "" {
		return out
	}
	for _, u := range s.users.List() {
		if u.RoleID == id {
			out[u.ID] = true
		}
	}
	return out
}

// aiBudgetScope names the budget a reservation of uid has to be checked against.
// It returns the group id and its members when st sets a budget for the group of uid, otherwise "" and nil.
func (s *Server) aiBudgetScope(uid string, st aiSettings) (string, map[string]bool) {
	if len(st.GroupBudgetUSD) == 0 {
		return "", nil
	}
	gid := s.aiGroupOf(uid)
	if gid == "" || st.GroupBudgetUSD[gid] <= 0 {
		return "", nil
	}
	return gid, s.aiGroupMembers(gid)
}

// aiBudgetOver reports whether spent plus the estimate of the call passes the budget of uid or of
// its group, and which of the two. users and held are the counters of the day and of the calls in
// flight; members are the accounts of the group gid (nil for no group). A missing budget never
// refuses. The caller holds the usage lock.
func aiBudgetOver(st aiSettings, uid, gid string, members map[string]bool, users, held map[string]aiTally, est float64) string {
	if b := st.UserBudgetUSD[uid]; aiBudgetOK(b) && users[uid].USD+held[uid].USD+est > b {
		return aiScopeUserBudget
	}
	if b := st.GroupBudgetUSD[gid]; gid != "" && aiBudgetOK(b) {
		spent := est
		for m := range members {
			spent += users[m].USD + held[m].USD
		}
		if spent > b {
			return aiScopeGroupBudget
		}
	}
	return ""
}

// aiBudgetView lists the accounts and the groups the panel can set a budget for.
// Accounts are the environment administrator and the users of users.json sorted by login;
// groups are the roles of roles.json with the USD their holders spent today, taken from users.
// The groups list is empty when roles.json holds no role, and the panel then shows no group block.
func (s *Server) aiBudgetView(users map[string]aiTally) ([]aiBudgetUser, []aiBudgetGroup) {
	accounts := []aiBudgetUser{{UID: "env-" + s.cfg.AdminUser, Login: s.cfg.AdminUser}}
	byRole := map[string][]string{}
	if s.users != nil {
		list := s.users.List()
		sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Login) < strings.ToLower(list[j].Login) })
		for _, u := range list {
			accounts = append(accounts, aiBudgetUser{UID: u.ID, Login: u.Login})
			if u.RoleID != "" {
				byRole[u.RoleID] = append(byRole[u.RoleID], u.ID)
			}
		}
	}
	groups := []aiBudgetGroup{}
	if s.roles != nil {
		for _, r := range s.roles.List() {
			g := aiBudgetGroup{ID: r.ID, Name: r.Name, Users: len(byRole[r.ID])}
			for _, id := range byRole[r.ID] {
				g.USD += users[id].USD
			}
			g.USD = aiRoundUSD(g.USD)
			groups = append(groups, g)
		}
	}
	return accounts, groups
}
