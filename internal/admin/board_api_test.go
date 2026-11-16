package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

func TestBoardPostCreateIcon(t *testing.T) {
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards", "", csrf, map[string]string{
		"name": "Demo",
		"icon": "flows",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d %s", resp.StatusCode, raw)
	}
	var created config.Board
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Icon != "flows" {
		t.Fatalf("created = %+v", created)
	}
	resp2, _ := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards", "", csrf, map[string]string{
		"name": "Bad",
		"icon": "nope",
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid icon: %d", resp2.StatusCode)
	}
}

func TestBoardPatchMainIcon(t *testing.T) {
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPatch, hs.URL+"/api/boards/main", "", csrf, map[string]string{
		"icon": "dashboard",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch icon %d %s", resp.StatusCode, raw)
	}
	var updated config.Board
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Icon != "dashboard" {
		t.Fatalf("updated = %+v", updated)
	}
	resp2, _ := boardJSON(t, c, http.MethodPatch, hs.URL+"/api/boards/main", "", csrf, map[string]string{
		"name": "Other",
	})
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("rename main: %d", resp2.StatusCode)
	}
}

func TestBoardDeleteMain(t *testing.T) {
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, _ := boardJSON(t, c, http.MethodDelete, hs.URL+"/api/boards/main", "", csrf, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete main: %d", resp.StatusCode)
	}
}

func TestBoardDeleteEmptySection(t *testing.T) {
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards", "", csrf, map[string]string{"name": "Temp"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d %s", resp.StatusCode, raw)
	}
	var created config.Board
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	resp2, _ := boardJSON(t, c, http.MethodDelete, hs.URL+"/api/boards/"+created.ID, "", csrf, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("delete %d", resp2.StatusCode)
	}
	resp3, raw3 := boardGet(t, c, hs.URL+"/api/boards", "")
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("list %d", resp3.StatusCode)
	}
	var out boardsResponse
	if err := json.Unmarshal(raw3, &out); err != nil {
		t.Fatal(err)
	}
	for _, b := range out.Items {
		if b.ID == created.ID {
			t.Fatalf("still listed: %+v", out.Items)
		}
	}
}

func TestBoardDeleteInUseConflict(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{{ID: "g1", Name: "c", BoardID: "seo"}}}
	hs, _ := boardTestServer(t, groups, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, _ := boardJSON(t, c, http.MethodDelete, hs.URL+"/api/boards/seo", "", csrf, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete in use: %d", resp.StatusCode)
	}
}

func TestBoardManageForbidden(t *testing.T) {
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", func(s *Server) {
		_ = s.users.Add(security.User{
			ID: "u1", Login: "u1", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"seo"},
			Access: security.Access{Sections: sectionStrings(userSections)},
		})
	})
	c, csrf := boardLogin(t, hs.URL, "u1", "p@ss")
	for _, call := range []struct {
		method string
		url    string
	}{
		{http.MethodPost, hs.URL + "/api/boards"},
		{http.MethodPatch, hs.URL + "/api/boards/seo"},
		{http.MethodDelete, hs.URL + "/api/boards/seo"},
	} {
		resp, _ := boardJSON(t, c, call.method, call.url, "", csrf, map[string]string{"name": "X"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s: %d", call.method, call.url, resp.StatusCode)
		}
	}
}
