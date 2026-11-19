package store

import (
	"strings"
	"testing"
)

// originReadyClient is readyClient with the origin wave on both tables.
func originReadyClient() *CH {
	c := readyClient()
	c.originEvents.Store(true)
	c.originConv.Store(true)
	return c
}

// TestJournalWhereEntryCampaign pins the click journal filter of a campaign: the
// serving campaign and the entry campaign both match, and ne/nin exclude both.
func TestJournalWhereEntryCampaign(t *testing.T) {
	c := originReadyClient()
	cases := []struct {
		name  string
		cond  Cond
		sql   string
		nargs int
	}{
		{"id eq", Cond{Field: "campaign_id", Op: "eq", Values: []string{"a"}},
			" AND (group_id IN (?) OR origin_group_id IN (?))", 2},
		{"id in", Cond{Field: "campaign_id", Op: "in", Values: []string{"a", "b"}},
			" AND (group_id IN (?,?) OR origin_group_id IN (?,?))", 4},
		{"name eq", Cond{Field: "campaign", Op: "eq", Values: []string{"Name"}},
			" AND (group_name IN (?) OR origin_group_name IN (?))", 2},
		{"id ne", Cond{Field: "campaign_id", Op: "ne", Values: []string{"a"}},
			" AND group_id NOT IN (?) AND origin_group_id NOT IN (?) AND group_id != ''", 2},
	}
	for _, tc := range cases {
		sql, args, skip, err := buildJournalWhere(c, "events", []Cond{tc.cond})
		if err != nil || skip || sql != tc.sql || len(args) != tc.nargs {
			t.Fatalf("%s: %q %#v skip=%v err=%v", tc.name, sql, args, skip, err)
		}
		for i, a := range args {
			if a != tc.cond.Values[i%len(tc.cond.Values)] {
				t.Fatalf("%s: arg %d is %v", tc.name, i, a)
			}
		}
	}
}

// TestJournalWhereEntryKeepsScope proves a Scope condition and other fields stay exact.
func TestJournalWhereEntryKeepsScope(t *testing.T) {
	c := originReadyClient()
	sql, args, _, err := buildJournalWhere(c, "events", []Cond{
		{Field: "campaign_id", Op: "in", Values: []string{"own"}, Scope: true},
		{Field: "country", Op: "eq", Values: []string{"PL"}},
	})
	if err != nil || sql != " AND group_id IN (?) AND country IN (?)" || len(args) != 2 {
		t.Fatalf("scope: %q %#v err=%v", sql, args, err)
	}
}

// TestJournalWhereEntryWithoutOriginWave proves the ordinary fragment is used
// while the origin columns are missing, and that buildWhere itself never widens.
func TestJournalWhereEntryWithoutOriginWave(t *testing.T) {
	cond := []Cond{{Field: "campaign_id", Op: "eq", Values: []string{"a"}}}
	sql, args, _, err := buildJournalWhere(readyClient(), "events", cond)
	if err != nil || sql != " AND group_id IN (?)" || len(args) != 1 {
		t.Fatalf("no origin: %q %#v err=%v", sql, args, err)
	}
	sql, args, _, err = buildWhere(originReadyClient(), "events", cond)
	if err != nil || sql != " AND group_id IN (?)" || len(args) != 1 {
		t.Fatalf("plain buildWhere: %q %#v err=%v", sql, args, err)
	}
}

// TestJournalWhereEntryConversions pins the conversion journal: a row matches by its
// own campaign, its entry campaign, or, with no campaign, by the click that either owns.
func TestJournalWhereEntryConversions(t *testing.T) {
	c := originReadyClient()
	sql, args, skip, err := buildJournalWhere(c, "conversions", []Cond{{Field: "campaign_id", Op: "eq", Values: []string{"g1"}}})
	want := " AND (group_id IN (?) OR origin_group_id IN (?) OR (group_id = '' AND cid IN " +
		"(SELECT cid FROM events WHERE (group_id IN (?) OR origin_group_id IN (?)) AND cid != '')))"
	if err != nil || skip || sql != want || len(args) != 4 {
		t.Fatalf("eq: %q %#v skip=%v err=%v", sql, args, skip, err)
	}
	if strings.Count(sql, "?") != len(args) {
		t.Fatalf("placeholders %d, args %d", strings.Count(sql, "?"), len(args))
	}
	sql, args, _, err = buildJournalWhere(c, "conversions", []Cond{{Field: "campaign", Op: "in", Values: []string{"A", "B"}}})
	if err != nil || sql != " AND (group_name IN (?,?) OR origin_group_name IN (?,?))" || len(args) != 4 {
		t.Fatalf("name: %q %#v err=%v", sql, args, err)
	}
}

// TestJournalWhereEntryConversionsEventsOrigin covers a client whose conversions have
// the origin wave while the events table does not: the click subquery stays exact.
func TestJournalWhereEntryConversionsEventsOrigin(t *testing.T) {
	c := readyClient()
	c.originConv.Store(true)
	sql, args, _, err := buildJournalWhere(c, "conversions", []Cond{{Field: "campaign_id", Op: "eq", Values: []string{"g1"}}})
	want := " AND (group_id IN (?) OR origin_group_id IN (?) OR (group_id = '' AND cid IN " +
		"(SELECT cid FROM events WHERE group_id IN (?) AND cid != '')))"
	if err != nil || sql != want || len(args) != 3 || strings.Count(sql, "?") != len(args) {
		t.Fatalf("%q %#v err=%v", sql, args, err)
	}
}
