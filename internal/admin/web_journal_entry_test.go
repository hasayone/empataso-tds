package admin

import "testing"

// TestJournalEntryFromCampaign runs the campaign button helpers under node: the address of a campaign
// becomes one visible campaign_id Equals filter, it applies only on a fresh entry, and a redraw of the
// same page and address keeps the filters the user has now.
func TestJournalEntryFromCampaign(t *testing.T) {
	prelude := `
const CAMPS = [{id:'zzt_entry', name:'ZZT Entry'}, {id:'other', name:'Other'}];
function groupById(id){ return CAMPS.find(g=>g.id===id) || null; }
let PAGE_PREV = '';
`
	assistantNode(t, []string{"entryCampaignConds", "campaignOfConds", "journalEntryFresh"}, prelude, `
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
