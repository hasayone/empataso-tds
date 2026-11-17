package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

func boardAssignServer(t *testing.T) (*http.Client, string, string, *boardMemGroups) {
	t.Helper()
	groups := &boardMemGroups{list: []config.Group{
		{ID: "m1", Name: "Main camp", Folder: "Leads", BoardID: "main"},
		{ID: "m2", Name: "Other main", BoardID: "main"},
		{ID: "s1", Name: "SEO old", BoardID: "seo"},
		{ID: "a1", Name: "ASO", BoardID: "aso"},
	}}
	hs, _ := boardTestServer(t, groups, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	return c, csrf, hs.URL, groups
}

func TestBoardAssignFolderAndReturnMain(t *testing.T) {
	c, csrf, base, groups := boardAssignServer(t)
	body := boardAssignBody{}
	body.Folders = []string{"Leads"}
	body.IDs.Campaigns = []string{"s1"}
	resp, _ := boardJSON(t, c, http.MethodPost, base+"/api/boards/seo/assign", "", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assign status %d", resp.StatusCode)
	}
	byID := map[string]config.Group{}
	for _, g := range groups.list {
		byID[g.ID] = g
	}
	if byID["m1"].BoardID != "seo" {
		t.Fatalf("folder campaign board=%q", byID["m1"].BoardID)
	}
	if byID["s1"].BoardID != "seo" {
		t.Fatalf("listed seo campaign board=%q", byID["s1"].BoardID)
	}
	if byID["a1"].BoardID != "aso" {
		t.Fatalf("aso moved: %+v", byID["a1"])
	}
	body2 := boardAssignBody{}
	resp2, _ := boardJSON(t, c, http.MethodPost, base+"/api/boards/seo/assign", "", csrf, body2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("clear assign %d", resp2.StatusCode)
	}
	for _, g := range groups.list {
		switch g.ID {
		case "m1", "s1":
			if g.BoardID != "" && !config.SameBoard(g.BoardID, config.MainBoardID) {
				t.Fatalf("%s not main: %q", g.ID, g.BoardID)
			}
		}
	}
}

func TestBoardMemberRoleCreatedAndRead(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "mine", Name: "Mine", BoardID: "seo", CreatedBy: "u1"},
		{ID: "other", Name: "Other", BoardID: "seo", CreatedBy: "u2"},
	}}
	hs, srv := boardTestServer(t, groups, filepath.Join(t.TempDir(), "users.json"), func(s *Server) {
		_ = s.users.Add(security.User{
			ID: "u1", Login: "created", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"seo"},
			Access: security.Access{
				Sections: sectionStrings(userSections),
				Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
			},
		})
		_ = s.users.Add(security.User{
			ID: "u2", Login: "edit", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"seo"},
			Access: security.Access{
				Sections: sectionStrings(userSections),
				Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
			},
		})
		_ = s.users.Add(security.User{
			ID: "u3", Login: "read", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"seo"},
			Access: security.Access{
				Sections: sectionStrings(userSections),
				Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
			},
		})
	})
	ctx := context.Background()
	raw, _ := srv.cfg.Boards.List(ctx)
	for i := range raw {
		if raw[i].ID == "seo" {
			raw[i].Members = []config.BoardMember{
				{UserID: "u1", Role: config.BoardRoleCreated},
				{UserID: "u2", Role: config.BoardRoleEdit},
				{UserID: "u3", Role: config.BoardRoleRead},
			}
		}
	}
	_ = srv.cfg.Boards.Save(ctx, raw)

	cCreated, _ := boardLogin(t, hs.URL, "created", "p@ss")
	resp, rawBody := boardGet(t, cCreated, hs.URL+"/api/groups", "seo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("created list %d", resp.StatusCode)
	}
	var listed []config.Group
	if err := json.Unmarshal(rawBody, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "mine" {
		t.Fatalf("created sees %+v", listed)
	}

	cEdit, _ := boardLogin(t, hs.URL, "edit", "p@ss")
	resp2, raw2 := boardGet(t, cEdit, hs.URL+"/api/groups", "seo")
	if resp2.StatusCode != http.StatusOK {
		t.Fatal(resp2.StatusCode)
	}
	var listed2 []config.Group
	_ = json.Unmarshal(raw2, &listed2)
	if len(listed2) != 2 {
		t.Fatalf("edit sees %d", len(listed2))
	}

	cRead, csrf := boardLogin(t, hs.URL, "read", "p@ss")
	resp3, _ := boardJSON(t, cRead, http.MethodPut, hs.URL+"/api/groups", "seo", csrf, []config.Group{
		{ID: "mine", Name: "Mine changed", BoardID: "seo", CreatedBy: "u1"},
		{ID: "other", Name: "Other", BoardID: "seo", CreatedBy: "u2"},
	})
	if resp3.StatusCode != http.StatusForbidden {
		t.Fatalf("read write %d", resp3.StatusCode)
	}
}

func TestBoardAssignHoldsGroupsMu(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{{ID: "c1", BoardID: "main"}}}
	hs, s := boardTestServer(t, groups, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	s.groupsMu.Lock()
	done := make(chan struct{})
	go func() {
		body := boardAssignBody{}
		body.IDs.Campaigns = []string{"c1"}
		_, _ = boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/seo/assign", "", csrf, body)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("assign finished while groupsMu held")
	case <-time.After(200 * time.Millisecond):
	}
	s.groupsMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("assign did not finish after unlock")
	}
}
