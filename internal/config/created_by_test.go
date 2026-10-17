package config

import (
	"encoding/json"
	"testing"
)

func TestCreatedByJSONRoundTrip(t *testing.T) {
	g := Group{ID: "g1", CreatedBy: "u42", Status: true, Redirect: "stop", Streams: []Stream{}}
	gb, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContainsField(t, gb, "created_by", "u42") {
		t.Fatalf("group json missing created_by: %s", gb)
	}
	var g2 Group
	if err := json.Unmarshal(gb, &g2); err != nil {
		t.Fatal(err)
	}
	if g2.CreatedBy != "u42" {
		t.Fatalf("group created_by = %q", g2.CreatedBy)
	}
	gEmpty := Group{ID: "g2", Status: true, Redirect: "stop"}
	gb2, err := json.Marshal(gEmpty)
	if err != nil {
		t.Fatal(err)
	}
	if jsonHasKey(t, gb2, "created_by") {
		t.Fatalf("empty created_by must be omitted: %s", gb2)
	}

	o := Offer{ID: "o1", Name: "x", URL: "https://x/", Status: true, CreatedBy: "u7"}
	ob, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContainsField(t, ob, "created_by", "u7") {
		t.Fatalf("offer json missing created_by: %s", ob)
	}
	var o2 Offer
	if err := json.Unmarshal(ob, &o2); err != nil {
		t.Fatal(err)
	}
	if o2.CreatedBy != "u7" {
		t.Fatalf("offer created_by = %q", o2.CreatedBy)
	}
	oEmpty := Offer{ID: "o2", Name: "y", URL: "https://y/", Status: true}
	ob2, err := json.Marshal(oEmpty)
	if err != nil {
		t.Fatal(err)
	}
	if jsonHasKey(t, ob2, "created_by") {
		t.Fatalf("empty offer created_by must be omitted: %s", ob2)
	}
}

func jsonContainsField(t *testing.T, b []byte, key, want string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	s, _ := v.(string)
	return s == want
}

func jsonHasKey(t *testing.T, b []byte, key string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	_, ok := m[key]
	return ok
}
