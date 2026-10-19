package admin

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseCondsRoundTripCases(t *testing.T) {
	cases := []struct {
		raw  string
		op   string
		want []string
	}{
		{"country:in:ru,us", "in", []string{"ru", "us"}},
		{`site:eq:C:\tmp`, "eq", []string{`C:\tmp`}},
		{"site:eq:a:b:c", "eq", []string{"a:b:c"}},
		{"country:in:a\\,b,c", "in", []string{"a,b", "c"}},
		{`country:in:x\\,y`, "in", []string{`x\`, "y"}},
		{`country:in:C:\tmp`, "in", []string{`C:\tmp`}},
	}
	for _, c := range cases {
		q := url.Values{}
		q.Add("f", c.raw)
		conds, err := parseConds(q)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if len(conds) != 1 || conds[0].Op != c.op {
			t.Fatalf("%s: %+v", c.raw, conds)
		}
		if strings.Join(conds[0].Values, "|") != strings.Join(c.want, "|") {
			t.Fatalf("%s: got %v want %v", c.raw, conds[0].Values, c.want)
		}
	}
}

func TestParseCondsEqMergePreservesCommas(t *testing.T) {
	q := url.Values{}
	q.Add("f", "site:eq:a,b")
	q.Add("f", "site:eq:c")
	conds, err := parseConds(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(conds) != 1 || conds[0].Field != "site" || conds[0].Op != "in" {
		t.Fatalf("shape %+v", conds)
	}
	want := []string{"a,b", "c"}
	if len(conds[0].Values) != len(want) {
		t.Fatalf("got %v want %v", conds[0].Values, want)
	}
	for i := range want {
		if conds[0].Values[i] != want[i] {
			t.Fatalf("got %v want %v", conds[0].Values, want)
		}
	}
}
