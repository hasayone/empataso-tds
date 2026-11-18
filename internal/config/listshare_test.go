package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// longModels builds a model whitelist of n entries with the given suffix, the shape a Keitaro import repeats in every stream.
func longModels(n int, suffix string) ListFilter {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = "Phone Model " + strings.Repeat("x", i%7) + suffix
	}
	return ListFilter{Flag: FlagB, Raw: strings.Join(vals, ","), Values: vals}
}

// TestListFilterFieldsCoverRules checks that ListFilterFields names every ListFilter field of Rules by its JSON key.
func TestListFilterFieldsCoverRules(t *testing.T) {
	var r Rules
	got := map[string]bool{}
	for _, nf := range r.ListFilterFields() {
		got[nf.Key] = true
	}
	rt := reflect.TypeOf(r)
	want := 0
	for i := range rt.NumField() {
		f := rt.Field(i)
		if f.Type != reflect.TypeOf(ListFilter{}) {
			continue
		}
		want++
		key := strings.Split(f.Tag.Get("json"), ",")[0]
		if !got[key] {
			t.Errorf("field %s (%s) is missing from ListFilterFields", f.Name, key)
		}
	}
	if len(got) != want {
		t.Errorf("ListFilterFields has %d keys, Rules has %d list filters", len(got), want)
	}
}

// TestShareListFilters checks that equal long filters end up on one backing copy, flags stay per stream,
// and short or different filters are left as they were.
func TestShareListFilters(t *testing.T) {
	a, b := longModels(300, ""), longModels(300, "")
	b.Flag = FlagA
	other := longModels(300, "-other")
	short := ListFilter{Flag: FlagB, Raw: "US,DE", Values: []string{"US", "DE"}}
	groups := []Group{
		{ID: "g1", Streams: []Stream{{Rules: Rules{Model: a, Country: short}}}},
		{ID: "g2", Streams: []Stream{{Rules: Rules{Model: b, Country: ListFilter{Flag: FlagB, Raw: "US,DE", Values: []string{"US", "DE"}}}}, {Rules: Rules{Model: other}}}},
	}
	ShareListFilters(groups)
	m1, m2, m3 := groups[0].Streams[0].Rules.Model, groups[1].Streams[0].Rules.Model, groups[1].Streams[1].Rules.Model
	if &m1.Values[0] != &m2.Values[0] || unsafe.StringData(m1.Raw) != unsafe.StringData(m2.Raw) {
		t.Fatal("equal long filters do not share one copy")
	}
	if m1.Flag != FlagB || m2.Flag != FlagA {
		t.Fatalf("flags changed: %d %d", m1.Flag, m2.Flag)
	}
	if &m3.Values[0] == &m1.Values[0] || m3.Raw == m1.Raw {
		t.Fatal("a different list was merged")
	}
	c1, c2 := groups[0].Streams[0].Rules.Country, groups[1].Streams[0].Rules.Country
	if &c1.Values[0] == &c2.Values[0] {
		t.Fatal("a short filter was shared")
	}
}

// writeGroupsJSON stores groups as a groups file in a temporary directory and returns its path.
func writeGroupsJSON(t *testing.T, groups []Group) string {
	t.Helper()
	b, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "groups.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadGroupsSharesLists checks that LoadGroups applies ShareListFilters to the file it reads.
func TestLoadGroupsSharesLists(t *testing.T) {
	m := longModels(200, "")
	path := writeGroupsJSON(t, []Group{
		{ID: "a", Streams: []Stream{{Rules: Rules{Model: m}}}},
		{ID: "b", Streams: []Stream{{Rules: Rules{Model: m}}}},
	})
	gs, err := LoadGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := gs.Get("a")
	b, _ := gs.Get("b")
	if &a.Streams[0].Rules.Model.Values[0] != &b.Streams[0].Rules.Model.Values[0] {
		t.Fatal("loaded groups keep separate copies of one list")
	}
}
