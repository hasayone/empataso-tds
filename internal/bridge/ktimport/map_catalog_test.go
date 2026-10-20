package ktimport

import (
	"encoding/json"
	"testing"
)

func TestKDomainGroupJSON(t *testing.T) {
	cases := []struct {
		raw       string
		wantGroup string
		wantGID   int
	}{
		{`{"id":1,"name":"a.example.com","group_id":1,"group":"Main"}`, "Main", 1},
		{`{"id":2,"name":"b.example.com","group_id":5,"group":"APPS"}`, "APPS", 5},
		{`{"id":3,"name":"c.example.com","group_id":11,"group":"Dashbord"}`, "Dashbord", 11},
		{`{"id":4,"name":"d.example.com","group_id":0,"group":null}`, "", 0},
		{`{"id":5,"name":"e.example.com","group_id":2,"group":{"name":"FromObj"}}`, "FromObj", 2},
	}
	for _, tc := range cases {
		var row KDomain
		if err := json.Unmarshal([]byte(tc.raw), &row); err != nil {
			t.Fatalf("json %q: %v", tc.raw, err)
		}
		if int(row.GroupID) != tc.wantGID || string(row.Group) != tc.wantGroup {
			t.Fatalf("json %q: got gid=%d group=%q", tc.raw, row.GroupID, row.Group)
		}
	}
}

func TestMapDomainsGroups(t *testing.T) {
	groups := indexGroups([]KGroup{
		{ID: 1, Name: "CampMain", Type: "campaigns"},
		{ID: 1, Name: "Main", Type: "domains"},
		{ID: 5, Name: "APPS", Type: "domains"},
		{ID: 11, Name: "Dashbord", Type: "domains"},
	})
	rows := []KDomain{
		{ID: 10, Name: "a.example.com", State: "active", GroupID: 1},
		{ID: 11, Name: "b.example.com", GroupID: 5},
		{ID: 12, Name: "c.example.com", GroupID: 11},
		{ID: 13, Name: "d.example.com", GroupID: 0},
		{ID: 14, Name: "e.example.com", GroupID: 99, Group: "Inline"},
	}
	doms, losses := mapDomains(rows, groups)
	if len(losses) != 0 {
		t.Fatalf("losses %+v", losses)
	}
	byID := map[string]CatDomain{}
	for _, d := range doms {
		byID[d.ID] = d
	}
	if byID["10"].Group != "Main" {
		t.Fatalf("id 10 group %q want Main", byID["10"].Group)
	}
	if byID["11"].Group != "APPS" || byID["12"].Group != "Dashbord" {
		t.Fatalf("groups %+v", byID)
	}
	if byID["13"].Group != "" {
		t.Fatalf("id 13 group %q want empty", byID["13"].Group)
	}
	if byID["14"].Group != "Inline" {
		t.Fatalf("inline group %q", byID["14"].Group)
	}
}
