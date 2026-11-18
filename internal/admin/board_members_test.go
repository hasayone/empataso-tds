package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
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

// boardIDsSeen returns the normalized section ids of GET /api/boards for client c.
func boardIDsSeen(t *testing.T, c *http.Client, base string) []string {
	t.Helper()
	resp, raw := boardGet(t, c, base+"/api/boards", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("boards %d", resp.StatusCode)
	}
	var out boardsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(out.Items))
	for _, b := range out.Items {
		ids = append(ids, config.NormalizeBoardID(b.ID))
	}
	return ids
}

// putBoardMembers replaces the members of section id as the signed-in owner.
func putBoardMembers(t *testing.T, c *http.Client, base, csrf, id string, members []config.BoardMember) {
	t.Helper()
	resp, raw := boardJSON(t, c, http.MethodPut, base+"/api/boards/"+id+"/members", "", csrf, boardMembersBody{Members: members})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("members %s: %d %s", id, resp.StatusCode, raw)
	}
}

func TestBoardMembershipNeverNarrowsFullAccess(t *testing.T) {
	full := security.Access{Sections: sectionStrings(userSections)}
	hs, srv := boardTestServer(t, &boardMemGroups{}, "", func(s *Server) {
		_ = s.users.Add(security.User{ID: "adm", Login: "tofan", Hash: mustHash(t, "p@ss"), Enabled: true, Type: security.UserAdmin})
		_ = s.users.Add(security.User{ID: "stale", Login: "stale", Hash: mustHash(t, "p@ss"), Enabled: true, Type: security.UserAdmin, BoardIDs: []string{"seo"}})
		_ = s.users.Add(security.User{ID: "wide", Login: "wide", Hash: mustHash(t, "p@ss"), Enabled: true, Type: security.UserRegular, AllBoards: true, Access: full})
		_ = s.users.Add(security.User{ID: "lim", Login: "lim", Hash: mustHash(t, "p@ss"), Enabled: true, Type: security.UserRegular, Access: full})
	})
	owner, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	tofan, _ := boardLogin(t, hs.URL, "tofan", "p@ss")
	stale, _ := boardLogin(t, hs.URL, "stale", "p@ss")
	wide, _ := boardLogin(t, hs.URL, "wide", "p@ss")
	lim, _ := boardLogin(t, hs.URL, "lim", "p@ss")
	everyAll := func(step string) {
		t.Helper()
		for name, c := range map[string]*http.Client{"tofan": tofan, "stale": stale, "wide": wide} {
			if got := boardIDsSeen(t, c, hs.URL); len(got) != 3 {
				t.Fatalf("%s: %s sees %v, want every section", step, name, got)
			}
			if resp, _ := boardGet(t, c, hs.URL+"/api/groups", "aso"); resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: %s opens aso: %d", step, name, resp.StatusCode)
			}
		}
	}

	everyAll("before")
	putBoardMembers(t, owner, hs.URL, csrf, "seo", []config.BoardMember{
		{UserID: "adm", Role: config.BoardRoleRead},
		{UserID: "wide", Role: config.BoardRoleRead},
		{UserID: "lim", Role: config.BoardRoleEdit},
	})
	everyAll("added")
	if got := boardIDsSeen(t, lim, hs.URL); !slices.Equal(got, []string{config.MainBoardID, "seo"}) {
		t.Fatalf("limited user sees %v, want main and seo", got)
	}
	if resp, _ := boardGet(t, lim, hs.URL+"/api/groups", "aso"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("limited user opens aso: %d", resp.StatusCode)
	}
	u, _ := srv.userByID("wide")
	if !u.AllBoards {
		t.Fatal("membership cleared all_boards")
	}

	putBoardMembers(t, owner, hs.URL, csrf, "seo", []config.BoardMember{{UserID: "lim", Role: config.BoardRoleEdit}})
	everyAll("removed")
	if got := boardIDsSeen(t, lim, hs.URL); !slices.Equal(got, []string{config.MainBoardID, "seo"}) {
		t.Fatalf("limited user after removal of others sees %v", got)
	}
}

// putUser patches user id through PUT /api/users/{id} as the signed-in owner and returns the stored record.
func putUser(t *testing.T, c *http.Client, base, csrf, id string, body map[string]any) security.User {
	t.Helper()
	resp, raw := boardJSON(t, c, http.MethodPut, base+"/api/users/"+id, "", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put user %s: %d %s", id, resp.StatusCode, raw)
	}
	var u security.User
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestBoardMembershipKeepsMainSection(t *testing.T) {
	full := security.Access{Sections: sectionStrings(userSections)}
	usersPath := filepath.Join(t.TempDir(), "users.json")
	hs, srv := boardTestServer(t, &boardMemGroups{}, usersPath, func(s *Server) {
		_ = s.users.Add(security.User{ID: "old", Login: "old", Hash: mustHash(t, "p@ss"), Enabled: true, Type: security.UserRegular, Access: full})
	})
	owner, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	old, _ := boardLogin(t, hs.URL, "old", "p@ss")
	sees := func(step string, want ...string) {
		t.Helper()
		if got := boardIDsSeen(t, old, hs.URL); !slices.Equal(got, want) {
			t.Fatalf("%s: sees %v, want %v", step, got, want)
		}
	}
	status := func(board string) int {
		t.Helper()
		resp, _ := boardGet(t, old, hs.URL+"/api/groups", board)
		return resp.StatusCode
	}

	sees("record without the flag", config.MainBoardID)
	putBoardMembers(t, owner, hs.URL, csrf, "seo", []config.BoardMember{{UserID: "old", Role: config.BoardRoleEdit}})
	sees("added to seo", config.MainBoardID, "seo")
	if u, _ := srv.userByID("old"); u.NoMainBoard || !slices.Equal(u.BoardIDs, []string{"seo"}) {
		t.Fatalf("after add: no_main_board=%v board_ids=%v", u.NoMainBoard, u.BoardIDs)
	}
	if code := status("aso"); code != http.StatusForbidden {
		t.Fatalf("foreign section aso: %d", code)
	}

	putBoardMembers(t, owner, hs.URL, csrf, "main", []config.BoardMember{{UserID: "old", Role: config.BoardRoleRead}})
	if u, _ := srv.userByID("old"); !slices.Equal(u.BoardIDs, []string{"seo"}) {
		t.Fatalf("main membership leaked into board_ids: %v", u.BoardIDs)
	}
	putBoardMembers(t, owner, hs.URL, csrf, "main", nil)

	putBoardMembers(t, owner, hs.URL, csrf, "seo", nil)
	sees("removed from seo", config.MainBoardID)

	putBoardMembers(t, owner, hs.URL, csrf, "seo", []config.BoardMember{{UserID: "old", Role: config.BoardRoleEdit}})
	if u := putUser(t, owner, hs.URL, csrf, "old", map[string]any{"no_main_board": true}); !u.NoMainBoard {
		t.Fatal("no_main_board not stored")
	}
	sees("main unticked", "seo")
	if code := status("main"); code != http.StatusForbidden {
		t.Fatalf("main after untick: %d", code)
	}
	if code := status(""); code != http.StatusOK {
		t.Fatalf("empty header falls back to seo: %d", code)
	}

	putBoardMembers(t, owner, hs.URL, csrf, "aso", []config.BoardMember{{UserID: "old", Role: config.BoardRoleRead}})
	if u, _ := srv.userByID("old"); !u.NoMainBoard {
		t.Fatal("membership sync cleared no_main_board")
	}

	putBoardMembers(t, owner, hs.URL, csrf, "seo", nil)
	putBoardMembers(t, owner, hs.URL, csrf, "aso", nil)
	sees("no section")
	for _, board := range []string{"", "main", "seo"} {
		resp, raw := boardGet(t, old, hs.URL+"/api/groups", board)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("no section, groups %q: %d", board, resp.StatusCode)
		}
		if board == "" && !strings.Contains(string(raw), errNoBoards) {
			t.Fatalf("no section answer: %s", raw)
		}
	}
	resp, raw := boardGet(t, old, hs.URL+"/api/me", "seo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me without sections: %d %s", resp.StatusCode, raw)
	}
	var me meResponse
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Boards) != 0 || me.Current != "" {
		t.Fatalf("me without sections: boards=%v current=%q", me.Boards, me.Current)
	}

	putUser(t, owner, hs.URL, csrf, "old", map[string]any{"no_main_board": false})
	sees("main ticked again", config.MainBoardID)

	reloaded, err := security.LoadUserSet(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := reloaded.Get("old"); u.NoMainBoard || len(u.BoardIDs) != 0 {
		t.Fatalf("stored record: %+v", u)
	}
}

func TestUserMainBoardFlagAdminDropped(t *testing.T) {
	hs, srv := boardTestServer(t, &boardMemGroups{}, "", func(s *Server) {
		_ = s.users.Add(security.User{ID: "adm", Login: "tofan", Hash: mustHash(t, "p@ss"), Enabled: true, Type: security.UserAdmin})
	})
	owner, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	if u := putUser(t, owner, hs.URL, csrf, "adm", map[string]any{"no_main_board": true}); u.NoMainBoard {
		t.Fatal("administrator kept no_main_board")
	}
	resp, raw := boardJSON(t, owner, http.MethodPost, hs.URL+"/api/users", "", csrf, map[string]any{
		"login": "nomain", "password": "secret1", "type": "USER", "enabled": true, "no_main_board": true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, raw)
	}
	var created security.User
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if u, _ := srv.userByID(created.ID); !u.NoMainBoard {
		t.Fatal("create dropped no_main_board for a USER")
	}
	tofan, _ := boardLogin(t, hs.URL, "tofan", "p@ss")
	if got := boardIDsSeen(t, tofan, hs.URL); len(got) != 3 {
		t.Fatalf("administrator sees %v, want every section", got)
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
