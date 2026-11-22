package store

import (
	"reflect"
	"strings"
	"testing"
)

// TestStatusInListIsOr checks that a status filter of two values is one IN list that matches either value
// and that the values are compared in lower case, so a lead and a sale are both found and neither is
// replaced by the other. It does not touch ClickHouse.
func TestStatusInListIsOr(t *testing.T) {
	for _, c := range []*CH{{}, readyClient()} {
		for _, vals := range [][]string{{"lead", "sale"}, {"Lead", " SALE "}} {
			sql, args, skip, err := buildJournalWhere(c, "conversions", []Cond{{Field: "status", Op: "in", Values: vals}})
			if err != nil || skip {
				t.Fatalf("where: err=%v skip=%v", err, skip)
			}
			if !strings.Contains(sql, "status IN (?,?)") && !strings.Contains(sql, "status IN (?, ?)") {
				t.Fatalf("two statuses must be one IN list: %s", sql)
			}
			if strings.Count(sql, "status") != 1 || strings.Contains(sql, "NOT IN") {
				t.Fatalf("the status condition must appear once and not be negated: %s", sql)
			}
			if !reflect.DeepEqual(args, []any{"lead", "sale"}) {
				t.Fatalf("args = %v, want [lead sale]", args)
			}
		}
	}
}

// TestStatusWithCampaignIsAnd checks the entry of a campaign's conversions: the campaign condition and the
// status list are two conditions joined with AND, and the status list stays a single IN.
func TestStatusWithCampaignIsAnd(t *testing.T) {
	c := readyClient()
	sql, args, skip, err := buildJournalWhere(c, "conversions", []Cond{
		{Field: "campaign_id", Op: "eq", Values: []string{"c1"}},
		{Field: "status", Op: "in", Values: []string{"lead", "sale"}},
	})
	if err != nil || skip {
		t.Fatalf("where: err=%v skip=%v", err, skip)
	}
	if !strings.Contains(sql, " AND status IN (") {
		t.Fatalf("the status list is its own AND condition: %s", sql)
	}
	if len(args) < 3 || args[len(args)-2] != "lead" || args[len(args)-1] != "sale" {
		t.Fatalf("the statuses are the last arguments: %v", args)
	}
}
