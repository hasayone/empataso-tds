package admin

import (
	"strings"
	"testing"
)

// TestSPAAIBudgetFields checks the budget fields of the Usage block: the table of users gets a budget column right after the USD
// amount, the group block is drawn only when the server lists a group, the typed budgets travel with the one Save of the page,
// and the texts exist in en, ru and uk.
func TestSPAAIBudgetFields(t *testing.T) {
	html := spaSource()
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	must(t, panel, "${aiBudgetField('user',u.uid,u.login||u.uid,cur)}", "the field sits in the row of the user")
	must(t, panel, "${aiBudgetGroupsHTML(cur)}", "the group block is drawn below the users")
	must(t, panel, "const bud=aiBudgetBody(cur,budgets);", "the budgets go with the settings Save")
	must(t, panel, "Object.assign(body,bud.body);", "no second Save button")
	for _, k := range []string{
		"ai.set.usage.budget", "ai.set.budget.none", "ai.set.budget.groups", "ai.set.budget.group", "ai.set.budget.members",
		"ai.set.budget.hint", "ai.set.err.budget", "ai.chat.err.ai_quota.user_budget", "ai.chat.err.ai_quota.group_budget",
	} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("%q missing for lang %q", k, lang)
			}
		}
	}
	assistantNode(t, []string{"aiBudgetRows", "aiBudgetField", "aiBudgetGroupsHTML", "aiBudgetBody", "aiSettingsTab", "aiErrKey"},
		aiLimitPrelude(t, html)+`
const AI_ERR_KEYS = {ai_quota:'q'};
const AI_ERR_VARIANTS = {ai_quota:{user_budget:'qu', group_budget:'qg'}};
const AI_ERR_PICK = {ai_quota:p=>p.scope};
`, `
const cur = {limit_max:{usd:1000000}, user_budget_usd:{u1:1.5, gone:2}, group_budget_usd:{},
  usage_today:{users:[{uid:'u1', login:'ann', tokens:10, usd:0.003}]},
  budget_accounts:[{uid:'env-admin', login:'admin'}, {uid:'u1', login:'ann'}], budget_groups:[]};
eq(aiBudgetRows(cur).map(r => [r.uid, r.usd]), [['u1', 0.003], ['env-admin', 0]], 'users who spent come first, then the other accounts');
eq(aiBudgetGroupsHTML(cur), '', 'no group, no block');
const html = aiBudgetGroupsHTML(Object.assign({}, cur, {budget_groups:[{id:'g1', name:'Buy<ers', users:2, usd:0.5}]}));
eq([html.includes('data-aibud="group"'), html.includes('data-id="g1"'), html.includes('Buy&lt;ers')], [true, true, true], 'a group row with an escaped name');
const f = aiBudgetField('user', 'u1', 'ann', cur);
eq([f.includes('value="1.5"'), f.includes('min="0"'), f.includes('max="1000000"'), aiBudgetField('user', 'env-admin', 'admin', cur).includes('value=""')], [true, true, true, true], 'the field: saved value, no negatives, empty means no limit');
eq(aiBudgetBody(cur, {user:{u1:'1.5'}, group:{}}), {body:{}, bad:null}, 'nothing changed, nothing sent');
eq(aiBudgetBody(cur, {user:{u1:'3', 'env-admin':'0.5'}, group:{}}).body, {user_budget_usd:{u1:3, gone:2, 'env-admin':0.5}}, 'a new budget joins the saved ones, one of a missing account stays');
eq(aiBudgetBody(cur, {user:{u1:''}, group:{}}).body, {user_budget_usd:{gone:2}}, 'an empty field removes the budget');
eq(aiBudgetBody(cur, {user:{u1:'0'}, group:{g1:'4'}}).body, {user_budget_usd:{gone:2}, group_budget_usd:{g1:4}}, '0 is no limit; a group budget is its own map');
for (const bad of ['-1', 'abc', '1e12']) eq(aiBudgetBody(cur, {user:{u1:bad}, group:{}}).bad, {scope:'user', id:'u1'}, 'refused: ' + bad);
eq(aiBudgetBody(cur, {user:{}, group:{g1:'-0.5'}}).bad, {scope:'group', id:'g1'}, 'a negative group budget');
eq(aiSettingsTab('ai_bad_limit', 'user_budget_usd'), 'usage', 'a refused budget opens the Usage block');
eq([aiErrKey('ai_quota', {scope:'user_budget'}), aiErrKey('ai_quota', {scope:'group_budget'}), aiErrKey('ai_quota', {})], ['qu', 'qg', 'q'], 'the chat words which budget stopped the turn');
`)
	if !strings.Contains(html, "input.aibud{") {
		t.Error("the budget field has no style")
	}
}
