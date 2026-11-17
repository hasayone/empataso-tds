package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// legacyOverShares is a stored campaign whose only stream has offer shares above 100%.
const legacyOverShares = `{"id":"legacy","name":"legacy","streams":[{"name":"s","status":true,` +
	`"out":{"offers":[{"id":"o1","share":60},{"id":"o2","share":50}]}}]}`

// spaLegacyOverShares is the same campaign as the editor sends it back untouched:
// empty containers, zero-valued filters and a spaced layout that the stored file does not have.
const spaLegacyOverShares = `{ "id" : "legacy", "name":"legacy", "status":false, "aliases":[], "postbacks":[],
 "postback_ids":[], "folder":"", "firewall":{"enabled":false,"queries":0,"seconds":0},
 "streams":[ {"name":"s","status":true,"comment":"",
  "rules":{"sub_ids":{},"operators":{},"lang":{"flag":0,"values":[],"raw":""},
           "ip_list":{"flag":0,"file":""},"computer":0,"limit":{"enabled":false,"type":0,"count":0,"seconds":0}},
  "out":{"offers":[{"id":"o1","share":60},{"id":"o2","share":50}],"landings":[],"out_shares":[]},
  "bots":{"redirect":"","header":""},"separation":{},"remote":{}} ] }`

// decodeGroup parses one campaign JSON the way the PUT handlers do.
func decodeGroup(t *testing.T, raw string) config.Group {
	t.Helper()
	var g config.Group
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGroupsEqualIgnoresEmptyShapes(t *testing.T) {
	stored := decodeGroup(t, legacyOverShares)
	sent := decodeGroup(t, spaLegacyOverShares)
	if !groupsEqual(stored, sent) {
		t.Fatal("an untouched campaign with empty containers counts as changed")
	}
	nullStreams := decodeGroup(t, `{"id":"a","name":"a","streams":null}`)
	emptyStreams := decodeGroup(t, `{"id":"a","name":"a","streams":[]}`)
	if !groupsEqual(nullStreams, emptyStreams) {
		t.Fatal("streams null and [] must be equal")
	}
}

func TestGroupsEqualSeesRealChanges(t *testing.T) {
	base := decodeGroup(t, spaLegacyOverShares)
	edits := map[string]func(g *config.Group){
		"name":       func(g *config.Group) { g.Name = "x" },
		"status":     func(g *config.Group) { g.Status = true },
		"alias":      func(g *config.Group) { g.Aliases = []string{"a"} },
		"num":        func(g *config.Group) { g.Num = 7 },
		"created by": func(g *config.Group) { g.CreatedBy = "u1" },
		"share":      func(g *config.Group) { g.Streams[0].Out.Offers[0].Share = 61 },
		"offer added": func(g *config.Group) {
			g.Streams[0].Out.Offers = append(g.Streams[0].Out.Offers, config.ShareRef{ID: "o3"})
		},
		"stream added": func(g *config.Group) {
			g.Streams = append(g.Streams, config.Stream{Name: "t"})
		},
		"empty stream added": func(g *config.Group) { g.Streams = append(g.Streams, config.Stream{}) },
		"flag zero kept as rule": func(g *config.Group) {
			g.Streams[0].Rules.Operators = map[string]config.Flag{"mts": config.FlagA}
		},
		"list filter": func(g *config.Group) {
			g.Streams[0].Rules.Country = config.ListFilter{Flag: config.FlagB, Values: []string{"US"}}
		},
		"sub id rule": func(g *config.Group) {
			g.Streams[0].Rules.SubIDs = map[string]config.SubIDFilter{"sub_id_1": {Flag: config.FlagB, Empty: true}}
		},
	}
	for name, edit := range edits {
		other := decodeGroup(t, spaLegacyOverShares)
		edit(&other)
		if groupsEqual(base, other) {
			t.Errorf("%s: a real change is reported as equal", name)
		}
	}
}

func TestGroupsEqualDoesNotMutateInputs(t *testing.T) {
	a := decodeGroup(t, spaLegacyOverShares)
	b := decodeGroup(t, legacyOverShares)
	_ = groupsEqual(a, b)
	if a.Aliases == nil || a.Streams[0].Rules.SubIDs == nil || a.Streams[0].Out.Landings == nil {
		t.Fatal("groupsEqual cleared an empty container of its argument")
	}
}

func TestChangedFaultUntouchedCampaignInSPAShape(t *testing.T) {
	stored := []config.Group{decodeGroup(t, legacyOverShares)}
	sent := []config.Group{decodeGroup(t, spaLegacyOverShares)}
	if msg := validateChangedShares(stored, sent); msg != "" {
		t.Errorf("untouched stored campaign blocks the save: %q", msg)
	}
	touched := decodeGroup(t, spaLegacyOverShares)
	touched.Name = "renamed"
	if msg := validateChangedShares(stored, []config.Group{touched}); msg != errSharesExceed {
		t.Errorf("touched campaign: %q", msg)
	}
}

// TestChangedFaultNumAssignedByEditor covers an unnumbered stored campaign that the editor
// numbers before saving: the number the server would assign is not a change, any other
// number is, and so is a number change of a campaign that already had one.
func TestChangedFaultNumAssignedByEditor(t *testing.T) {
	numbered := config.Group{ID: "other", Name: "other", Num: 1}
	stored := []config.Group{numbered, decodeGroup(t, legacyOverShares)}
	withNum := func(n int) []config.Group {
		sent := decodeGroup(t, spaLegacyOverShares)
		sent.Num = n
		return []config.Group{numbered, sent}
	}
	if msg := validateChangedShares(stored, withNum(2)); msg != "" {
		t.Errorf("the number the server assigns blocks the save: %q", msg)
	}
	for _, n := range []int{1, 5, 3} {
		if msg := validateChangedShares(stored, withNum(n)); msg != errSharesExceed {
			t.Errorf("num %d (duplicate or arbitrary) must count as a change: %q", n, msg)
		}
	}
	stored[1].Num = 3
	if msg := validateChangedShares(stored, withNum(2)); msg != errSharesExceed {
		t.Errorf("a changed number of a numbered campaign must count as a change: %q", msg)
	}
}

func TestGroupsSaveKeepsLegacyCampaignInSPAShape(t *testing.T) {
	legacy := decodeGroup(t, legacyOverShares)
	srv, store := sharesServer(t, []config.Group{legacy})
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	list := `[` + spaLegacyOverShares + `,{"id":"fresh","name":"fresh","streams":[]}]`
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, json.RawMessage(list))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var er struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&er)
		t.Fatalf("save of the list: %d %q", resp.StatusCode, er.Error)
	}
	if len(store.list) != 2 || !strings.Contains(store.list[0].ID, "legacy") {
		t.Fatalf("saved list: %+v", store.list)
	}
	renamed := strings.Replace(list, `"name":"legacy"`, `"name":"renamed"`, 1)
	resp2 := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups", csrf, json.RawMessage(renamed))
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("edited campaign with shares above 100%%: %d", resp2.StatusCode)
	}
}
