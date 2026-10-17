package store

import (
	"errors"
	"strings"
	"testing"
)

func readyClient() *CH {
	return &CH{readyFlags: map[string]map[wave]bool{
		"events": {
			waveBase: true, waveNet: true, waveClick: true, waveCost: true, waveExt: true,
		},
		"conversions": {
			waveBase: true, waveClick: true, waveExt: true,
		},
	}}
}

func TestBuildWhereOps(t *testing.T) {
	c := readyClient()
	sql, args, skip, err := buildWhere(c, "events", []Cond{{Field: "country", Op: "eq", Values: []string{"PL"}}})
	if err != nil || skip || sql != " AND country IN (?)" || len(args) != 1 || args[0] != "PL" {
		t.Fatalf("eq: %q %#v skip=%v err=%v", sql, args, skip, err)
	}
	sql, args, skip, err = buildWhere(c, "events", []Cond{{Field: "country", Op: "in", Values: []string{"PL", "DE"}}})
	if err != nil || skip || sql != " AND country IN (?,?)" || len(args) != 2 || args[0] != "PL" || args[1] != "DE" {
		t.Fatalf("in: %q %#v err=%v", sql, args, err)
	}
	sql, args, _, err = buildWhere(c, "events", []Cond{{Field: "country", Op: "ne", Values: []string{"PL"}}})
	if err != nil || sql != " AND country NOT IN (?) AND country != ''" || args[0] != "PL" {
		t.Fatalf("ne: %q %#v err=%v", sql, args, err)
	}
	if !strings.Contains(sql, "!= ''") {
		t.Fatalf("ne missing empty guard: %s", sql)
	}
	sql, args, _, err = buildWhere(c, "events", []Cond{{Field: "device", Op: "nin", Values: []string{"phone", "tv"}}})
	if err != nil || sql != " AND device NOT IN (?,?) AND device != ''" || len(args) != 2 {
		t.Fatalf("nin: %q %#v err=%v", sql, args, err)
	}
}

func TestBuildWhereLowerAndASN(t *testing.T) {
	c := readyClient()
	_, args, _, err := buildWhere(c, "conversions", []Cond{{Field: "status", Op: "eq", Values: []string{" Sale "}}})
	if err != nil || len(args) != 1 || args[0] != "sale" {
		t.Fatalf("lower: %#v err=%v", args, err)
	}
	sql, args, _, err := buildWhere(c, "events", []Cond{{Field: "asn", Op: "eq", Values: []string{"AS3320"}}})
	want := " AND if(asn = 0, '', concat('AS', toString(asn))) IN (?)"
	if err != nil || sql != want || args[0] != "AS3320" {
		t.Fatalf("asn: %q %#v err=%v", sql, args, err)
	}
	sql, _, _, err = buildWhere(c, "events", []Cond{{Field: "asn", Op: "ne", Values: []string{"AS1"}}})
	if err != nil || !strings.Contains(sql, "!= ''") || !strings.Contains(sql, "if(asn = 0, '', concat('AS', toString(asn)))") {
		t.Fatalf("asn ne: %q err=%v", sql, err)
	}
}

func TestBuildWhereErrorsAndSkip(t *testing.T) {
	c := readyClient()
	_, _, _, err := buildWhere(c, "events", []Cond{{Field: "nope", Op: "eq", Values: []string{"x"}}})
	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("unknown: %v", err)
	}
	_, _, _, err = buildWhere(c, "conversions", []Cond{{Field: "bot", Op: "eq", Values: []string{"x"}}})
	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("bot on conversions: %v", err)
	}
	_, _, _, err = buildWhere(c, "events", []Cond{{Field: "country", Op: "eq", Values: nil}})
	if !errors.Is(err, ErrBadCond) {
		t.Fatalf("empty values: %v", err)
	}
	_, _, _, err = buildWhere(c, "events", []Cond{{Field: "country", Op: "like", Values: []string{"x"}}})
	if !errors.Is(err, ErrBadCond) {
		t.Fatalf("op: %v", err)
	}
	many := make([]Cond, 21)
	for i := range many {
		many[i] = Cond{Field: "country", Op: "eq", Values: []string{"x"}}
	}
	if _, _, _, err = buildWhere(c, "events", many); !errors.Is(err, ErrBadCond) {
		t.Fatalf("too many conds: %v", err)
	}
	vals := make([]string, 201)
	if _, _, _, err = buildWhere(c, "events", []Cond{{Field: "country", Op: "in", Values: vals}}); !errors.Is(err, ErrBadCond) {
		t.Fatalf("too many values: %v", err)
	}
	c.readyFlags["events"][waveExt] = false
	sql, args, skip, err := buildWhere(c, "events", []Cond{{Field: "xrw", Op: "eq", Values: []string{"XMLHttpRequest"}}})
	if err != nil || !skip || sql != "" || args != nil {
		t.Fatalf("skip: %q %#v skip=%v err=%v", sql, args, skip, err)
	}
	sql, _, skip, err = buildWhere(&CH{}, "events", []Cond{{Field: "country", Op: "eq", Values: []string{"PL"}}})
	if err != nil || skip || sql != " AND country IN (?)" {
		t.Fatalf("base without probe: %q skip=%v err=%v", sql, skip, err)
	}
}

func TestValidateCondsScope(t *testing.T) {
	many := make([]string, 250)
	for i := range many {
		many[i] = "x"
	}
	scope := []Cond{{Field: "campaign_id", Op: "in", Values: many, Scope: true}}
	if err := ValidateConds("events", scope); err != nil {
		t.Fatalf("scope 250 values: %v", err)
	}
	regular := []Cond{{Field: "country", Op: "in", Values: many}}
	if err := ValidateConds("events", regular); !errors.Is(err, ErrBadCond) {
		t.Fatalf("regular 250 values want ErrBadCond, got %v", err)
	}
	twenty := make([]Cond, 20)
	for i := range twenty {
		twenty[i] = Cond{Field: "country", Op: "eq", Values: []string{"PL"}}
	}
	withScope := append(append([]Cond(nil), twenty...), scope[0])
	if err := ValidateConds("events", withScope); err != nil {
		t.Fatalf("20 regular + scope: %v", err)
	}
	badScope := []Cond{{Field: "campaign_id", Op: "like", Values: []string{"x"}, Scope: true}}
	if err := ValidateConds("events", badScope); !errors.Is(err, ErrBadCond) {
		t.Fatalf("scope bad op want ErrBadCond, got %v", err)
	}
}

func TestEventsCidInSQL(t *testing.T) {
	c := &CH{readyFlags: map[string]map[wave]bool{"events": {waveBase: true}}}
	where, args, skip, err := buildWhere(c, "events", []Cond{{Field: "device", Op: "eq", Values: []string{"mobile"}}})
	if err != nil || skip {
		t.Fatalf("where err=%v skip=%v", err, skip)
	}
	got := eventsCidInSQL(where)
	want := ` AND cid IN (SELECT cid FROM events WHERE ts >= ? AND ts < ? AND cid != ''` + where + `)`
	if got != want {
		t.Fatalf("cid in\n%s\nwant\n%s", got, want)
	}
	if len(args) != 1 || args[0] != "mobile" {
		t.Fatalf("args %#v", args)
	}
	if eventsCidInSQL("") != ` AND cid IN (SELECT cid FROM events WHERE ts >= ? AND ts < ? AND cid != ''`+`)` {
		t.Fatal("empty where")
	}
}
