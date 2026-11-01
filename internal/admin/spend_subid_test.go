package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

func groupWithSubIDs(m map[string]config.SubIDFilter) []config.Group {
	return []config.Group{{ID: "g", Streams: []config.Stream{{Name: "s", Rules: config.Rules{SubIDs: m}}}}}
}

func TestSubIDFault(t *testing.T) {
	ok := config.SubIDFilter{Flag: config.FlagB, Values: []string{"a"}}
	cases := []struct {
		name string
		m    map[string]config.SubIDFilter
		want string
	}{
		{"none", nil, ""},
		{"valid first and last", map[string]config.SubIDFilter{"sub_id_1": ok, "sub_id_12": {Flag: config.FlagA, Empty: true}}, ""},
		{"key above 30", map[string]config.SubIDFilter{"sub_id_31": ok}, "sub_id_31"},
		{"key zero", map[string]config.SubIDFilter{"sub_id_0": ok}, "sub_id_0"},
		{"foreign key", map[string]config.SubIDFilter{"get": ok}, "get"},
		{"flag off", map[string]config.SubIDFilter{"sub_id_2": {Flag: config.FlagOff, Values: []string{"a"}}}, "flag"},
		{"flag three", map[string]config.SubIDFilter{"sub_id_2": {Flag: 3, Values: []string{"a"}}}, "flag"},
		{"long value", map[string]config.SubIDFilter{"sub_id_2": {Flag: config.FlagB, Values: []string{strings.Repeat("x", 256)}}}, "too long"},
	}
	for _, c := range cases {
		got := subIDFault(groupWithSubIDs(c.m)[0])
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: subIDFault = %q, want %q", c.name, got, c.want)
		}
		if c.want != "" && !strings.Contains(got, "group g, stream s") {
			t.Errorf("%s: message %q does not name the group and the stream", c.name, got)
		}
	}
}

// badSubIDGroup is a campaign whose only stream has a Sub ID rule with a bad flag.
func badSubIDGroup(id string) config.Group {
	g := shareGroup(id)
	g.Streams[0].Rules.SubIDs = map[string]config.SubIDFilter{"sub_id_1": {Flag: 3, Values: []string{"a"}}}
	return g
}

func TestValidateChangedSubIDs(t *testing.T) {
	legacy := badSubIDGroup("legacy")
	if msg := validateChangedSubIDs([]config.Group{legacy}, []config.Group{legacy}); msg != "" {
		t.Errorf("unchanged stored campaign blocks the save: %q", msg)
	}
	touched := legacy
	touched.Name = "touched"
	if msg := validateChangedSubIDs([]config.Group{legacy}, []config.Group{touched}); !strings.Contains(msg, "group legacy, stream s") {
		t.Errorf("changed campaign: %q", msg)
	}
	if msg := validateChangedSubIDs(nil, []config.Group{badSubIDGroup("fresh")}); msg == "" {
		t.Error("new campaign with a bad rule must be refused")
	}
}

func TestGroupPutSubIDsOnlyForChanged(t *testing.T) {
	legacy := badSubIDGroup("legacy")
	srv, _ := sharesServer(t, []config.Group{legacy, shareGroup("other", 50)})
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	put := func(g config.Group) (int, string) {
		t.Helper()
		resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/"+g.ID, csrf, g)
		defer resp.Body.Close()
		var er struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&er)
		return resp.StatusCode, er.Error
	}
	other := shareGroup("other", 50)
	other.Name = "renamed"
	if code, msg := put(other); code != http.StatusOK {
		t.Fatalf("other beside a stored campaign with a bad rule: %d %q", code, msg)
	}
	bad := badSubIDGroup("other")
	if code, msg := put(bad); code != http.StatusBadRequest || !strings.Contains(msg, "group other, stream s") {
		t.Fatalf("changed campaign with a bad rule: %d %q", code, msg)
	}
}
