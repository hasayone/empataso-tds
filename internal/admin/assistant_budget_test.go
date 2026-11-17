package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
)

// aiBudgetSettings returns settings without the four daily limits and with the given budgets.
func aiBudgetSettings(user, group map[string]float64) aiSettings {
	zero := func() *int64 { return aiTestOff[int64]() }
	st := aiUsageSettings(zero(), zero(), aiTestOff[float64](), aiTestOff[float64]())
	st.UserBudgetUSD, st.GroupBudgetUSD = user, group
	return st
}

// aiBudgetUsers creates one account per login and gives each the role of roles[i]; it returns their ids.
func aiBudgetUsers(t *testing.T, e *aiTestEnv, logins []string, roles []string) []string {
	t.Helper()
	ids := make([]string, len(logins))
	for i, login := range logins {
		u, err := e.s.createUser(userDraft{Login: login, Password: "secret1", Type: security.UserRegular, Enabled: true,
			Access: security.Access{Grants: map[string]security.Grant{}}})
		if err != nil {
			t.Fatal(err)
		}
		role := roles[i]
		if err := e.s.users.Update(u.ID, func(rec *security.User) error { rec.RoleID = role; return nil }); err != nil {
			t.Fatal(err)
		}
		ids[i] = u.ID
	}
	return ids
}

// aiWantScope fails the test unless err is the ai_quota error of the budget scope.
func aiWantScope(t *testing.T, err error, scope string) {
	t.Helper()
	aiWantQuota(t, err)
	if got := aiErrOf(t, err).Params["scope"]; got != scope {
		t.Fatalf("scope %v, want %s", got, scope)
	}
}

func TestAIBudgetEmptyPasses(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiBudgetSettings(nil, nil)
	for i := 0; i < 5; i++ {
		if _, err := e.s.aiReserve("a", "claude-sonnet-4", st); err != nil {
			t.Fatalf("no budget, call %d: %v", i, err)
		}
	}
	st = aiBudgetSettings(map[string]float64{"other": 0.01}, map[string]float64{"role-x": 0.01})
	if _, err := e.s.aiReserve("a", "claude-sonnet-4", st); err != nil {
		t.Fatalf("a budget of another account or group must not apply: %v", err)
	}
}

func TestAIBudgetUserLimit(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiBudgetSettings(map[string]float64{"a": 0.3}, nil)
	r, err := e.s.aiReserve("a", "claude-sonnet-4", st)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.s.aiReserve("a", "claude-sonnet-4", st)
	aiWantScope(t, err, aiScopeUserBudget)
	if _, err = e.s.aiReserve("b", "claude-sonnet-4", st); err != nil {
		t.Fatalf("the budget is per account: %v", err)
	}
	e.s.aiCommit(r, 1000, 1000)
	if _, err = e.s.aiReserve("a", "claude-sonnet-4", st); err != nil {
		t.Fatalf("the spent money 0.018 and a new call fit 0.3: %v", err)
	}
}

func TestAIBudgetEnvAdministrator(t *testing.T) {
	e := newAITestEnv(t, nil)
	uid := e.s.aiUsageUID(security.Session{})
	st := aiBudgetSettings(map[string]float64{uid: 0.1}, nil)
	_, err := e.s.aiReserve(uid, "claude-sonnet-4", st)
	aiWantScope(t, err, aiScopeUserBudget)
}

func TestAIBudgetGroupLimit(t *testing.T) {
	e := newAITestEnv(t, nil)
	ids := aiBudgetUsers(t, e, []string{"u1", "u2", "u3"}, []string{"g1", "g1", ""})
	st := aiBudgetSettings(nil, map[string]float64{"g1": 0.3})
	if _, err := e.s.aiReserve(ids[0], "claude-sonnet-4", st); err != nil {
		t.Fatal(err)
	}
	_, err := e.s.aiReserve(ids[1], "claude-sonnet-4", st)
	aiWantScope(t, err, aiScopeGroupBudget)
	if _, err = e.s.aiReserve(ids[2], "claude-sonnet-4", st); err != nil {
		t.Fatalf("an account outside the group is not limited by it: %v", err)
	}
}

func TestAIBudgetStricterWins(t *testing.T) {
	e := newAITestEnv(t, nil)
	ids := aiBudgetUsers(t, e, []string{"u1", "u2"}, []string{"g1", "g1"})
	st := aiBudgetSettings(map[string]float64{ids[0]: 0.3}, map[string]float64{"g1": 1})
	if _, err := e.s.aiReserve(ids[0], "claude-sonnet-4", st); err != nil {
		t.Fatal(err)
	}
	_, err := e.s.aiReserve(ids[0], "claude-sonnet-4", st)
	aiWantScope(t, err, aiScopeUserBudget)
	if _, err = e.s.aiReserve(ids[1], "claude-sonnet-4", st); err != nil {
		t.Fatalf("a roomy group budget lets the other member in: %v", err)
	}
	st = aiBudgetSettings(map[string]float64{ids[0]: 5}, map[string]float64{"g1": 0.3})
	_, err = e.s.aiReserve(ids[0], "claude-sonnet-4", st)
	aiWantScope(t, err, aiScopeGroupBudget)
}

func TestAIBudgetIgnoresTokensAndChatLength(t *testing.T) {
	e := newAITestEnv(t, nil)
	st := aiBudgetSettings(map[string]float64{"a": 0.01}, nil)
	st.Prices = map[string]aiPrice{"free-model": {}}
	st.TurnInputTokens = aiTurnInputCap
	for i := 0; i < 3; i++ {
		r, err := e.s.aiReserve("a", "free-model", st)
		if err != nil {
			t.Fatalf("a call that costs nothing passes whatever its tokens: %v", err)
		}
		e.s.aiCommit(r, 1_000_000, 1_000_000)
	}
	if _, total, _ := e.s.aiUsageSnapshot(); total.Tokens != 6_000_000 || total.USD != 0 {
		t.Fatalf("usage %+v", total)
	}
}

func TestAIBudgetPut(t *testing.T) {
	e := newAITestEnv(t, nil)
	if code, body := e.put(`{"user_budget_usd":{"u1":1.5,"u2":0},"group_budget_usd":{"g1":3}}`); code != http.StatusOK {
		t.Fatalf("put %d %s", code, body)
	}
	st, err := e.s.aiLoad()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.UserBudgetUSD) != 1 || st.UserBudgetUSD["u1"] != 1.5 || st.GroupBudgetUSD["g1"] != 3 {
		t.Fatalf("stored %+v %+v", st.UserBudgetUSD, st.GroupBudgetUSD)
	}
	if code, body := e.put(`{"max_steps":4}`); code != http.StatusOK {
		t.Fatalf("put %d %s", code, body)
	}
	if st, _ = e.s.aiLoad(); st.UserBudgetUSD["u1"] != 1.5 {
		t.Fatalf("a field left out keeps the budgets: %+v", st.UserBudgetUSD)
	}
	code, body := e.call(e.s.handleAISettingsGet, http.MethodGet, "")
	var v aiSettingsView
	if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil || v.UserBudgetUSD["u1"] != 1.5 || v.BudgetGroups == nil || len(v.BudgetAccounts) == 0 {
		t.Fatalf("view %d %s", code, body)
	}
	for _, bad := range []string{
		`{"user_budget_usd":{"u1":-1}}`, `{"group_budget_usd":{"g1":-0.01}}`, `{"user_budget_usd":{"u1":1e12}}`,
		`{"user_budget_usd":{"":1}}`, `{"user_budget_usd":{"a b":1}}`,
	} {
		if code, body := e.put(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	if code, _ := e.put(`{"user_budget_usd":{}}`); code != http.StatusOK {
		t.Fatal("an empty map clears the budgets")
	}
	if st, _ = e.s.aiLoad(); len(st.UserBudgetUSD) != 0 || len(st.GroupBudgetUSD) != 1 {
		t.Fatalf("after clear %+v %+v", st.UserBudgetUSD, st.GroupBudgetUSD)
	}
}

func TestAIBudgetViewGroups(t *testing.T) {
	rolesFile := filepath.Join(t.TempDir(), "roles.json")
	e := newAITestEnv(t, func(c *Config) { c.RolesFile = rolesFile })
	if _, groups := e.s.aiBudgetView(nil); len(groups) != 0 {
		t.Fatalf("no roles, no groups: %+v", groups)
	}
	role, err := e.s.roles.Add(security.Role{Name: "Buyers"})
	if err != nil {
		t.Fatal(err)
	}
	ids := aiBudgetUsers(t, e, []string{"u1", "u2"}, []string{role.ID, ""})
	accounts, groups := e.s.aiBudgetView(map[string]aiTally{ids[0]: {USD: 0.25}, ids[1]: {USD: 9}})
	if len(accounts) != 3 || !strings.HasPrefix(accounts[0].UID, "env-") || accounts[1].Login != "u1" {
		t.Fatalf("accounts %+v", accounts)
	}
	if len(groups) != 1 || groups[0].ID != role.ID || groups[0].Users != 1 || groups[0].USD != 0.25 {
		t.Fatalf("groups %+v", groups)
	}
}
