package store

import (
	"errors"
	"testing"
)

// TestBuildWhereSubstring pins the SQL and arguments of contains and
// not_contains: values stay placeholders, LowCardinality columns are used as is.
func TestBuildWhereSubstring(t *testing.T) {
	c := readyClient()
	sql, args, skip, err := buildJournalWhere(c, "events", []Cond{{Field: "cid", Op: OpContains, Values: []string{"ab"}}})
	if err != nil || skip || sql != " AND (positionCaseInsensitiveUTF8(cid, ?) > 0)" || len(args) != 1 || args[0] != "ab" {
		t.Fatalf("partial subid: %q %#v skip=%v err=%v", sql, args, skip, err)
	}
	sql, args, skip, err = buildWhere(c, "events", []Cond{{Field: "sub_id_1", Op: OpContains, Values: []string{"ab%_'\\"}}})
	if err != nil || skip || sql != " AND (positionCaseInsensitiveUTF8(sub_id_1, ?) > 0)" || len(args) != 1 || args[0] != "ab%_'\\" {
		t.Fatalf("contains: %q %#v skip=%v err=%v", sql, args, skip, err)
	}
	sql, args, _, err = buildWhere(c, "events", []Cond{{Field: "referer", Op: OpContains, Values: []string{"a", "b"}}})
	want := " AND (positionCaseInsensitiveUTF8(referer, ?) > 0 OR positionCaseInsensitiveUTF8(referer, ?) > 0)"
	if err != nil || sql != want || len(args) != 2 || args[0] != "a" || args[1] != "b" {
		t.Fatalf("contains any: %q %#v err=%v", sql, args, err)
	}
	sql, args, _, err = buildWhere(c, "conversions", []Cond{{Field: "os", Op: OpNotContains, Values: []string{"win", "mac"}}})
	want = " AND positionCaseInsensitiveUTF8(os, ?) = 0 AND positionCaseInsensitiveUTF8(os, ?) = 0 AND os != ''"
	if err != nil || sql != want || len(args) != 2 || args[1] != "mac" {
		t.Fatalf("not_contains: %q %#v err=%v", sql, args, err)
	}
	sql, args, _, err = buildWhere(c, "events", []Cond{
		{Field: "country", Op: "eq", Values: []string{"PL"}},
		{Field: "useragent", Op: OpNotContains, Values: []string{"bot"}},
	})
	want = " AND country IN (?) AND positionCaseInsensitiveUTF8(useragent, ?) = 0 AND useragent != ''"
	if err != nil || sql != want || len(args) != 2 || args[0] != "PL" || args[1] != "bot" {
		t.Fatalf("mixed: %q %#v err=%v", sql, args, err)
	}
}

// TestValidateCondsSubstring rejects substring operators on fields that are not
// text and blank substrings, and accepts them on text fields.
func TestValidateCondsSubstring(t *testing.T) {
	for _, name := range []string{"country", "campaign", "campaign_id", "offer_id", "status", "profit", "ts", "asn", "uniq", "click_ts", "bot", "step"} {
		table := "events"
		if f, _ := fieldByName(name); f.Events == "" {
			table = "conversions"
		}
		for _, op := range []string{OpContains, OpNotContains} {
			err := ValidateConds(table, []Cond{{Field: name, Op: op, Values: []string{"x"}}})
			if !errors.Is(err, ErrBadCond) {
				t.Fatalf("%s %s: want ErrBadCond, got %v", name, op, err)
			}
		}
	}
	for _, vals := range [][]string{{""}, {"  "}, {"a", ""}} {
		if err := ValidateConds("events", []Cond{{Field: "sub_id_1", Op: OpContains, Values: vals}}); !errors.Is(err, ErrBadCond) {
			t.Fatalf("blank %q: want ErrBadCond, got %v", vals, err)
		}
	}
	for _, name := range []string{"sub_id_1", "sub_id_30", "cid", "referer", "useragent", "keyword", "site", "external_id", "ad_campaign_id", "creative_id", "stream", "city", "ip"} {
		if err := ValidateConds("events", []Cond{{Field: name, Op: OpContains, Values: []string{"x"}}}); err != nil {
			t.Fatalf("%s contains: %v", name, err)
		}
	}
	if err := ValidateConds("conversions", []Cond{{Field: "tid", Op: OpNotContains, Values: []string{"x"}}}); err != nil {
		t.Fatalf("tid not_contains: %v", err)
	}
}

// TestStaticFieldsText checks that the field list tells the interface which
// fields accept substring operators.
func TestStaticFieldsText(t *testing.T) {
	text := map[string]bool{}
	for _, f := range StaticFields() {
		text[f.Name] = f.Text
	}
	if !text["sub_id_1"] || !text["referer"] || !text["keyword"] {
		t.Fatalf("text fields missing: %v", text)
	}
	if text["country"] || text["profit"] || text["ts"] || text["offer_id"] {
		t.Fatalf("non-text field marked text: %v", text)
	}
}

func TestValidOp(t *testing.T) {
	for _, op := range CondOps {
		if !ValidOp(op) {
			t.Fatalf("%s rejected", op)
		}
	}
	if len(CondOps) != 6 || !ValidOp("contains") || !ValidOp("not_contains") {
		t.Fatalf("ops %v", CondOps)
	}
	for _, op := range []string{"", "like", "CONTAINS", "gt"} {
		if ValidOp(op) {
			t.Fatalf("%q accepted", op)
		}
	}
}
