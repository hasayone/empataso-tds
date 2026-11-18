package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// importedModels is a whitelist of n device models, the list a Keitaro import repeats in every stream.
func importedModels(n int) config.ListFilter {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = "Vendor Phone " + strings.Repeat("9", i%5) + string(rune('A'+i%26))
	}
	return config.ListFilter{Flag: config.FlagB, Raw: strings.Join(vals, ","), Values: vals}
}

// importedGroups builds n campaigns of three streams each; the first stream of every campaign carries models.
func importedGroups(n int, models config.ListFilter) []config.Group {
	out := make([]config.Group, n)
	for i := range out {
		out[i] = config.Group{ID: "c" + strconv.Itoa(i), Name: "campaign", Status: true, Redirect: "stop",
			Streams: []config.Stream{
				{Name: "models", Status: true, Rules: config.Rules{Model: models, Country: config.ListFilter{Flag: config.FlagB, Raw: "US,DE", Values: []string{"US", "DE"}}}},
				{Name: "geo", Status: true, Rules: config.Rules{Country: config.ListFilter{Flag: config.FlagA, Raw: "RU", Values: []string{"RU"}}}},
				{Name: "rest", Status: true},
			}}
	}
	return out
}

// TestShareGroupLists checks the shared answer: one list entry for the repeated filter, a ref per stream that
// carried it, flags and short filters kept in place, and the source groups untouched.
func TestShareGroupLists(t *testing.T) {
	models := importedModels(300)
	groups := importedGroups(4, models)
	groups[2].Streams[0].Rules.Model.Flag = config.FlagA
	out := shareGroupLists(groups)
	if len(out.Lists) != 1 || out.Lists[0].Raw != models.Raw || len(out.Lists[0].Values) != 300 {
		t.Fatalf("lists = %d entries", len(out.Lists))
	}
	if len(out.Refs) != 4 {
		t.Fatalf("refs = %v", out.Refs)
	}
	for i, ref := range out.Refs {
		if ref != [4]any{i, 0, "model", 0} {
			t.Fatalf("ref %d = %v", i, ref)
		}
	}
	m := out.Groups[2].Streams[0].Rules.Model
	if m.Flag != config.FlagA || m.Raw != "" || m.Values != nil {
		t.Fatalf("shared filter left in the group: %+v", m)
	}
	if out.Groups[0].Streams[0].Rules.Country.Raw != "US,DE" {
		t.Fatal("a short filter was cut")
	}
	if groups[0].Streams[0].Rules.Model.Raw != models.Raw {
		t.Fatal("the source groups were changed")
	}
	empty := shareGroupLists([]config.Group{{ID: "a"}})
	if empty.Lists == nil || empty.Refs == nil || len(empty.Groups) != 1 {
		t.Fatalf("empty answer = %+v", empty)
	}
}

// TestShareGroupListsSize reproduces the imported volume: 5500 campaigns whose first stream carries one list of 2569
// models. The plain answer is hundreds of megabytes; the shared one stays a few megabytes.
func TestShareGroupListsSize(t *testing.T) {
	const n = 5500
	groups := importedGroups(n, importedModels(2569))
	one, err := json.Marshal(groups[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(shareGroupLists(groups))
	if err != nil {
		t.Fatal(err)
	}
	plain := len(one) * n
	if plain < 300<<20 {
		t.Fatalf("synthetic plain answer %d bytes, the data is not like the import", plain)
	}
	if len(b) > 4<<20 {
		t.Fatalf("shared answer %d bytes for a plain one of about %d", len(b), plain)
	}
}

// TestGroupsGetShared checks GET /api/groups: the plain answer stays an array and ?shared=1 returns groups, lists and refs.
func TestGroupsGetShared(t *testing.T) {
	srv, groups, _ := catalogServer(t)
	groups.list[0].Streams[0].Rules.Model = importedModels(200)
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	resp, err := c.Get(srv.URL + "/api/groups")
	if err != nil {
		t.Fatal(err)
	}
	var plain []config.Group
	if err := json.NewDecoder(resp.Body).Decode(&plain); err != nil || len(plain) != 1 || len(plain[0].Streams[0].Rules.Model.Values) != 200 {
		t.Fatalf("plain answer: %v %d", err, len(plain))
	}
	resp.Body.Close()
	resp, err = c.Get(srv.URL + "/api/groups?shared=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var shared struct {
		Groups []config.Group `json:"groups"`
		Lists  []sharedList   `json:"lists"`
		Refs   [][]any        `json:"refs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&shared); err != nil {
		t.Fatal(err)
	}
	if len(shared.Groups) != 1 || len(shared.Lists) != 1 || len(shared.Refs) != 1 || shared.Groups[0].Streams[0].Rules.Model.Raw != "" {
		t.Fatalf("shared answer: %d groups, %d lists, refs %v", len(shared.Groups), len(shared.Lists), shared.Refs)
	}
}
