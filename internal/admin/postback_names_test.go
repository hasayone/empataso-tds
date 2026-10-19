package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

// nameStats answers the conversion journal with rows that have a campaign id and no name,
// the way an old row looks when the click that would name it is gone.
type nameStats struct{ fakeStats }

func (nameStats) QueryPostbacks(context.Context, store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error) {
	return []store.PostbackRow{
		{CID: "a", CampaignID: "c1"},
		{CID: "b", CampaignID: "c2"},
		{CID: "c", CampaignID: "c9"},
		{CID: "d"},
		{CID: "e", CampaignID: "c1", Group: "kept"},
	}, store.PostbackTotals{Count: 5}, nil
}

// postbackGroups reads /api/postbacks as c and returns the group of each row by cid.
func postbackGroups(t *testing.T, rig *rolesRig, c *http.Client) (map[string]string, string) {
	t.Helper()
	code, body := rig.get(t, c, "/api/postbacks")
	if code != http.StatusOK {
		t.Fatalf("postbacks %d %s", code, body)
	}
	var out struct {
		Rows []struct {
			CID   string  `json:"cid"`
			Group *string `json:"group"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range out.Rows {
		if r.Group != nil {
			got[r.CID] = *r.Group
		}
	}
	return got, body
}

func TestPostbackCampaignNameFromConfig(t *testing.T) {
	rig := newRolesRigStats(t, "", nameStats{})
	got, _ := postbackGroups(t, rig, rig.admin)
	want := map[string]string{"a": "one", "b": "two", "c": "", "d": "", "e": "kept"}
	for cid, name := range want {
		if got[cid] != name {
			t.Errorf("admin: row %s group = %q, want %q", cid, got[cid], name)
		}
	}
}

// A role that sees one campaign gets no name of another campaign, even when the
// source returns the row, and a hidden campaign field drops the name from the body.
func TestPostbackCampaignNameRespectsAccess(t *testing.T) {
	rig := newRolesRigStats(t, "", nameStats{})
	rig.setRole(t, security.Access{
		Sections: userSectionsAll(),
		Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeSelected, IDs: []string{"c1"}}},
	})
	got, _ := postbackGroups(t, rig, rig.user)
	if got["a"] != "one" || got["b"] != "" {
		t.Errorf("scoped role: groups %v, want c1 named and c2 unnamed", got)
	}

	rig.setRole(t, security.Access{Sections: userSectionsAll(), Hidden: []string{"campaign"}})
	hidden, body := postbackGroups(t, rig, rig.user)
	if len(hidden) != 0 || strings.Contains(body, "one") || strings.Contains(body, "two") {
		t.Errorf("hidden campaign still names rows: %s", body)
	}
}
