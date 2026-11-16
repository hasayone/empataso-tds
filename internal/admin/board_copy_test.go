package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

func boardCopyServer(t *testing.T, groups *memGroups) (*httptest.Server, *memGroups, string) {
	t.Helper()
	dir := t.TempDir()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        &scopeStats{},
		Groups:       groups,
		Boards:       NewJSONStore[config.Board](filepath.Join(dir, "boards.json")),
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:     NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:      NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:     NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		S2S:          NewJSONStore[config.OutboundPostback](filepath.Join(dir, "s2s.json")),
		SeqOffers:    NewIDSeq(filepath.Join(dir, "offer_seq.json")),
		SeqLandings:  NewIDSeq(filepath.Join(dir, "landing_seq.json")),
		LandingFiles: NewFileLandings(filepath.Join(dir, "landing-files")),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Boards.Save(context.Background(), []config.Board{
		{ID: config.MainBoardID, Name: mainBoardName},
		{ID: "seo", Name: "SEO"},
	}); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return hs, groups, dir
}

func TestBoardImportCopiesWithNewIDs(t *testing.T) {
	groups := &memGroups{list: []config.Group{
		{ID: "mainCamp", Name: "Main", BoardID: "main", Num: 3, Redirect: "stop"},
	}}
	hs, store, _ := boardCopyServer(t, groups)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/import", "seo", csrf, boardPickBody{
		From: "main", Campaigns: []string{"mainCamp"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import %d %s", resp.StatusCode, raw)
	}
	var out boardImportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Copied.Campaigns != 1 || out.Map.Campaigns["mainCamp"] == "" || out.Map.Campaigns["mainCamp"] == "mainCamp" {
		t.Fatalf("map: %+v", out.Map.Campaigns)
	}
	var foundMain, foundSEO bool
	for _, g := range store.list {
		if g.ID == "mainCamp" && config.SameBoard(g.BoardID, "main") {
			foundMain = true
		}
		if g.ID == out.Map.Campaigns["mainCamp"] && config.SameBoard(g.BoardID, "seo") {
			foundSEO = true
			if g.Num <= 0 {
				t.Fatalf("num not assigned: %+v", g)
			}
		}
	}
	if !foundMain || !foundSEO {
		t.Fatalf("groups: %+v", store.list)
	}
}

func TestBoardImportLocalLandingFiles(t *testing.T) {
	groups := &memGroups{}
	hs, _, dir := boardCopyServer(t, groups)
	files := NewFileLandings(filepath.Join(dir, "landing-files"))
	if err := files.Write("7", "index.html", []byte("<html>ok</html>")); err != nil {
		t.Fatal(err)
	}
	landStore := NewJSONStore[config.Landing](filepath.Join(dir, "landings.json"))
	if err := landStore.Save(context.Background(), []config.Landing{{ID: "7", Name: "L", Local: true, BoardID: "main"}}); err != nil {
		t.Fatal(err)
	}
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/import", "seo", csrf, boardPickBody{
		From: "main", Landings: []string{"7"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import %d %s", resp.StatusCode, raw)
	}
	var out boardImportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	newID := out.Map.Landings["7"]
	if newID == "" {
		t.Fatal("no landing map")
	}
	data, err := files.Read(newID, "index.html")
	if err != nil || string(data) != "<html>ok</html>" {
		t.Fatalf("files: %v %q", err, data)
	}
}

func TestBoardImportDropsMissingCampaignLink(t *testing.T) {
	groups := &memGroups{list: []config.Group{
		{ID: "peer", BoardID: "main", Redirect: "stop"},
		{ID: "copyMe", BoardID: "main", Num: 1, Redirect: "group", Out: "peer"},
	}}
	hs, store, _ := boardCopyServer(t, groups)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/import", "seo", csrf, boardPickBody{
		From: "main", Campaigns: []string{"copyMe"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import %d %s", resp.StatusCode, raw)
	}
	var out boardImportResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Dropped) == 0 {
		t.Fatalf("expected dropped, got %+v", out)
	}
	newID := out.Map.Campaigns["copyMe"]
	var copied config.Group
	for _, g := range store.list {
		if g.ID == newID {
			copied = g
		}
	}
	if copied.Redirect != "" || copied.Out != "" {
		t.Fatalf("redirect not cleared: %+v", copied)
	}
}

func TestBoardMoveChangesBoardID(t *testing.T) {
	groups := &memGroups{list: []config.Group{
		{ID: "seoCamp", Name: "SEO", BoardID: "seo", Num: 2, Redirect: "stop"},
	}}
	hs, store, _ := boardCopyServer(t, groups)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/move", "seo", csrf, boardPickBody{
		To: "main", Campaigns: []string{"seoCamp"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move %d %s", resp.StatusCode, raw)
	}
	var out boardMoveResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Moved.Campaigns != 1 {
		t.Fatalf("moved: %+v", out)
	}
	for _, g := range store.list {
		if g.ID == "seoCamp" && !config.SameBoard(g.BoardID, "main") {
			t.Fatalf("board not moved: %+v", g)
		}
	}
}

func TestBoardMoveStripsCrossBoardLink(t *testing.T) {
	groups := &memGroups{list: []config.Group{
		{ID: "stay", BoardID: "seo", Redirect: "group", Out: "go"},
		{ID: "go", BoardID: "seo", Redirect: "stop"},
	}}
	hs, store, _ := boardCopyServer(t, groups)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/move", "seo", csrf, boardPickBody{
		To: "main", Campaigns: []string{"go"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move %d %s", resp.StatusCode, raw)
	}
	var out boardMoveResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Dropped) == 0 {
		t.Fatalf("expected dropped: %+v", out)
	}
	for _, g := range store.list {
		if g.ID == "stay" && (g.Redirect != "" || g.Out != "") {
			t.Fatalf("link not stripped: %+v", g)
		}
	}
}
