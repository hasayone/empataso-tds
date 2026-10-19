package store

import (
	"errors"
	"testing"
)

func allWavesReady() func(w wave) bool {
	return func(w wave) bool { return true }
}

func TestJournalOrderByDefault(t *testing.T) {
	order, slow, err := journalOrderBy("events", "", true, allWavesReady())
	if err != nil || slow {
		t.Fatalf("order=%q slow=%v err=%v", order, slow, err)
	}
	if order != "ts DESC, cid" {
		t.Fatalf("order = %q", order)
	}
}

func TestJournalOrderByTS(t *testing.T) {
	order, slow, err := journalOrderBy("events", "ts", false, allWavesReady())
	if err != nil || !slow {
		t.Fatalf("order=%q slow=%v err=%v", order, slow, err)
	}
	if order != "ts ASC, cid" {
		t.Fatalf("order = %q", order)
	}
	order, _, _ = journalOrderBy("events", "ts", true, allWavesReady())
	if order != "ts DESC, cid" {
		t.Fatalf("desc order = %q", order)
	}
}

func TestJournalOrderByField(t *testing.T) {
	order, slow, err := journalOrderBy("events", "country", true, allWavesReady())
	if err != nil || !slow {
		t.Fatalf("order=%q slow=%v err=%v", order, slow, err)
	}
	if order != "country DESC, ts DESC, cid" {
		t.Fatalf("order = %q", order)
	}
	order, _, err = journalOrderBy("conversions", "profit", false, allWavesReady())
	if err != nil {
		t.Fatal(err)
	}
	if order != "toString(profit) ASC, ts DESC, cid" {
		t.Fatalf("profit order = %q", order)
	}
}

func TestSortColumnRejects(t *testing.T) {
	ready := allWavesReady()
	cases := []string{"step", "useragent", "x;drop", "campaign desc", "nope", "status"}
	for _, name := range cases {
		if _, err := SortColumn("events", name, ready); !errors.Is(err, ErrBadSort) {
			t.Errorf("SortColumn(events, %q) = %v, want ErrBadSort", name, err)
		}
	}
	if _, err := SortColumn("events", "status", ready); !errors.Is(err, ErrBadSort) {
		t.Errorf("status on events: %v", err)
	}
}

func TestSortColumnCampaignAlias(t *testing.T) {
	expr, err := SortColumn("events", "group", allWavesReady())
	if err != nil || expr != "group_name" {
		t.Fatalf("group alias: expr=%q err=%v", expr, err)
	}
}
