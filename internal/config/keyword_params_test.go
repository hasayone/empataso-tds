package config

import (
	"slices"
	"testing"
)

// TestKeywordParamsOrder pins the names and the priority the engine reads a keyword under.
func TestKeywordParamsOrder(t *testing.T) {
	if want := []string{"keyword", "q", "utm_term"}; !slices.Equal(KeywordParams[:], want) {
		t.Errorf("KeywordParams = %v, want %v", KeywordParams, want)
	}
}

// TestKeywordNameTaken pins which names another field of the campaign owns.
func TestKeywordNameTaken(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		params    map[string]string
		cost, cur string
		want      bool
	}{
		{"no source, no price", "utm_term", nil, "", "", false},
		{"sub id on the name", "utm_term", map[string]string{"sub_id_1": "utm_term"}, "", "", true},
		{"extra field on the name", "keyword", map[string]string{"site": "keyword"}, "", "", true},
		{"mapping is trimmed", "utm_term", map[string]string{"external_id": "  utm_term "}, "", "", true},
		{"q is never taken by a sub id", "q", map[string]string{"sub_id_1": "q"}, "", "", false},
		{"q is never taken by an extra field", "q", map[string]string{"site": " q "}, "", "", false},
		{"q is never taken by the price", "q", nil, "q", "q", false},
		{"keyword mapped onto its alias is not another field", "utm_term", map[string]string{"keyword": "utm_term"}, "", "", false},
		{"other name", "keyword", map[string]string{"sub_id_1": "utm_term"}, "", "", false},
		{"empty mapping", "utm_term", map[string]string{"sub_id_1": ""}, "", "", false},
		{"cost name", "utm_term", nil, "utm_term", "", true},
		{"currency name, trimmed", "keyword", nil, "", " keyword ", true},
		{"other price names", "utm_term", nil, "price", "cur", false},
	}
	for _, c := range cases {
		if got := KeywordNameTaken(c.query, c.params, c.cost, c.cur); got != c.want {
			t.Errorf("%s: KeywordNameTaken = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestKeywordNameTakenDoesNotAllocate keeps the rule free of allocations on the hot path.
func TestKeywordNameTakenDoesNotAllocate(t *testing.T) {
	params := map[string]string{"keyword": "k", "sub_id_1": "p1", "sub_id_2": " utm_term ", "site": "s"}
	var sink bool
	if n := testing.AllocsPerRun(200, func() {
		for _, name := range KeywordParams {
			sink = KeywordNameTaken(name, params, "price", "cur") || sink
		}
	}); n != 0 {
		t.Errorf("KeywordNameTaken allocates %v times per call", n)
	}
	_ = sink
}
