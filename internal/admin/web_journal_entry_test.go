package admin

import (
	"strings"
	"testing"
)

// TestJournalEntryFromCampaign runs the campaign button helpers under node: the address of a campaign
// becomes one visible campaign_id Equals filter, it applies only on a fresh entry, and a redraw of the
// same page and address keeps the filters the user has now.
func TestJournalEntryFromCampaign(t *testing.T) {
	prelude := `
const CAMPS = [{id:'zzt_entry', name:'ZZT Entry'}, {id:'other', name:'Other'}];
function groupById(id){ return CAMPS.find(g=>g.id===id) || null; }
let PAGE_PREV = '';
`
	assistantNode(t, []string{"entryCampaignConds", "entryConversionConds", "campaignOfConds", "journalEntryFresh"}, prelude, `
eq(entryConversionConds('zzt_entry'), [{f:'campaign_id', op:'eq', v:['zzt_entry']}, {f:'status', op:'in', v:['lead','sale']}], 'the conversions of a campaign open on the statuses it counts: lead and sale, not rejected');
eq(entryConversionConds('zzt_entry', new Set(['cost'])), [{f:'campaign_id', op:'eq', v:['zzt_entry']}, {f:'status', op:'in', v:['lead','sale']}], 'another hidden field leaves the statuses');
eq(entryConversionConds('zzt_entry', new Set(['status'])), [{f:'campaign_id', op:'eq', v:['zzt_entry']}], 'a viewer without the status field gets the campaign only');
eq(entryConversionConds('missing'), [], 'an unknown campaign sets no filter, also no statuses');
eq(entryConversionConds(''), [], 'no address sets no filter');
eq(campaignOfConds(entryConversionConds('zzt_entry')), 'zzt_entry', 'the campaign is still found among the conversion filters');
eq(entryCampaignConds('zzt_entry'), [{f:'campaign_id', op:'eq', v:['zzt_entry']}], 'the address becomes campaign_id equals the campaign');
eq(entryCampaignConds('missing'), [], 'an unknown campaign sets no filter');
eq(entryCampaignConds(''), [], 'no address sets no filter');
eq(campaignOfConds([{f:'country',op:'eq',v:['pl']},{f:'campaign_id',op:'eq',v:['a']}]), 'a', 'the campaign of the filters');
eq(campaignOfConds([{f:'campaign_id',op:'ne',v:['a']},null]), '', 'not equals is no campaign');
eq(campaignOfConds(null), '', 'no filters, no campaign');

PAGE_PREV = 'flows';
eq(journalEntryFresh('logs', '', 'a', false), true, 'coming from another page applies the filter');
PAGE_PREV = 'logs';
eq(journalEntryFresh('logs', 'a', 'a', false), false, 'a redraw of the same address keeps the filters');
eq(journalEntryFresh('logs', 'a', 'b', false), true, 'another campaign applies its filter');
eq(journalEntryFresh('logs', '', 'a', false), true, 'a campaign after the bare journal applies its filter');
eq(journalEntryFresh('logs', 'a', 'a', true), false, 'a redraw after an edit never applies');
eq(journalEntryFresh('logs', 'a', '', false), false, 'the bare journal sets nothing');
`)
}

// TestFilterChipText runs condChipText under node: a chip reads as one phrase, the operator in lower case
// (Campaign ID equals: Promo; Status in list: Lead, Sale), in every language and for every operator,
// while the field name and the value labels stay as they are.
func TestFilterChipText(t *testing.T) {
	prelude := `
const DICT = {
  'flt.c.eq':'равно','flt.c.ne':'не равно','flt.c.in':'из списка','flt.c.nin':'не из списка','flt.c.contains':'содержит','flt.c.not_contains':'не содержит',
  'flt.eq':'Равно','flt.in':'Из списка','flt.gt':'Больше'
};
const tr = k => DICT[k] || k;
const NAMES = {campaign_id:'ID кампании', status:'Статус'};
const fltName = (table, f) => NAMES[f] || f;
const LABELS = {lead:'Лид', sale:'Продажа', 'All Offers - Dating':'All Offers - Dating'};
const idLabel = (f, v) => LABELS[v] || v;
`
	assistantNode(t, []string{"condChipText"}, prelude, `
eq(condChipText('conversions', {f:'campaign_id', op:'eq', v:['All Offers - Dating']}), 'ID кампании равно: All Offers - Dating', 'equals reads in lower case');
eq(condChipText('conversions', {f:'status', op:'in', v:['lead','sale']}), 'Статус из списка: Лид, Продажа', 'in list reads in lower case and keeps the value labels');
eq(condChipText('conversions', {f:'status', op:'nin', v:['lead']}), 'Статус не из списка: Лид', 'not in list');
eq(condChipText('conversions', {f:'status', op:'ne', v:['sale']}), 'Статус не равно: Продажа', 'not equal');
eq(condChipText('events', {f:'status', v:['sale']}), 'Статус равно: Продажа', 'a condition without an operator is equals');
eq(condChipText('events', {f:'status', op:'gt', v:['1']}), 'Статус больше: 1', 'an operator without a chip word falls back to its lower-cased label');
`)
	html := spaSource()
	for _, op := range []string{"eq", "ne", "in", "nin", "contains", "not_contains"} {
		if n := strings.Count(html, "'flt.c."+op+"':'"); n != 3 {
			t.Errorf("flt.c.%s is translated %d times, want 3", op, n)
		}
	}
	if !strings.Contains(spaFunctionLine(html, "paintFilterBar"), "condChipText(table, c)") {
		t.Error("the filter bar does not use condChipText")
	}
}
