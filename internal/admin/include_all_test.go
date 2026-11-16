package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
	"github.com/egerkuzma/emtds/internal/store"
)

func TestIncludeAllPatch(t *testing.T) {
	hs, srv := boardTestServer(t, &boardMemGroups{}, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	before, err := os.ReadFile(srv.cfg.Boards.path)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := boardGet(t, c, hs.URL+"/api/boards", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("boards %d %s", resp.StatusCode, raw)
	}
	after, err := os.ReadFile(srv.cfg.Boards.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("GET /api/boards rewrote boards.json")
	}
	items := decodeBoardItems(t, raw)
	main := boardByID(t, items, config.MainBoardID)
	if main.IncludeAll == nil || !*main.IncludeAll {
		t.Fatalf("missing field must read as enabled: %+v", main)
	}
	if boardByID(t, items, "seo").IncludeAll != nil {
		t.Fatal("seo must omit include_all")
	}
	meResp, meRaw := boardGet(t, c, hs.URL+"/api/me", "")
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("me %d", meResp.StatusCode)
	}
	var me struct {
		Boards []config.Board `json:"boards"`
	}
	if err := json.Unmarshal(meRaw, &me); err != nil {
		t.Fatal(err)
	}
	meMain := boardByID(t, me.Boards, config.MainBoardID)
	if meMain.IncludeAll == nil || !*meMain.IncludeAll {
		t.Fatalf("me include_all: %+v", meMain)
	}

	empty, emptyRaw := boardJSON(t, c, http.MethodPatch, hs.URL+"/api/boards/main", "", csrf, map[string]any{})
	if empty.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty patch %d %s", empty.StatusCode, emptyRaw)
	}

	off, offRaw := boardJSON(t, c, http.MethodPatch, hs.URL+"/api/boards/main", "", csrf, map[string]any{"include_all": false})
	if off.StatusCode != http.StatusOK {
		t.Fatalf("patch false %d %s", off.StatusCode, offRaw)
	}
	var patched config.Board
	if err := json.Unmarshal(offRaw, &patched); err != nil {
		t.Fatal(err)
	}
	if patched.IncludeAll == nil || *patched.IncludeAll {
		t.Fatalf("patch response: %+v", patched)
	}
	stored := mustBoards(t, srv)
	storedMain := boardByID(t, stored, config.MainBoardID)
	if storedMain.IncludeAll == nil || *storedMain.IncludeAll {
		t.Fatalf("stored false: %+v", storedMain)
	}
	again, againRaw := boardGet(t, c, hs.URL+"/api/boards", "")
	if again.StatusCode != http.StatusOK {
		t.Fatalf("reread %d", again.StatusCode)
	}
	if got := boardByID(t, decodeBoardItems(t, againRaw), config.MainBoardID); got.IncludeAll == nil || *got.IncludeAll {
		t.Fatalf("reread include_all: %+v", got)
	}

	fileBeforeForeign, err := os.ReadFile(srv.cfg.Boards.path)
	if err != nil {
		t.Fatal(err)
	}
	foreign, foreignRaw := boardJSON(t, c, http.MethodPatch, hs.URL+"/api/boards/seo", "", csrf, map[string]any{"include_all": false})
	if foreign.StatusCode != http.StatusBadRequest {
		t.Fatalf("seo include_all %d %s", foreign.StatusCode, foreignRaw)
	}
	fileAfterForeign, err := os.ReadFile(srv.cfg.Boards.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileBeforeForeign, fileAfterForeign) {
		t.Fatal("rejected include_all was written")
	}

	on, onRaw := boardJSON(t, c, http.MethodPatch, hs.URL+"/api/boards/main", "", csrf, map[string]any{"include_all": true})
	if on.StatusCode != http.StatusOK {
		t.Fatalf("patch true %d %s", on.StatusCode, onRaw)
	}
	var turned config.Board
	if err := json.Unmarshal(onRaw, &turned); err != nil {
		t.Fatal(err)
	}
	if turned.IncludeAll == nil || !*turned.IncludeAll {
		t.Fatalf("patch true response: %+v", turned)
	}
	disk, err := os.ReadFile(srv.cfg.Boards.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(disk, []byte("include_all")) {
		t.Fatalf("true must not be stored: %s", disk)
	}
	if got := boardByID(t, mustBoards(t, srv), config.MainBoardID); got.IncludeAll != nil {
		t.Fatalf("stored true pointer: %+v", got)
	}
}

func TestSeesBoardWideView(t *testing.T) {
	open := viewer{
		boardCurrent: config.MainBoardID,
		includeAll:   true,
		boardOpen:    map[string]bool{config.MainBoardID: true, "seo": true},
		boardRoles: map[string]string{
			config.MainBoardID: config.BoardRoleEdit,
			"seo":              config.BoardRoleCreated,
		},
		uid:    "u1",
		grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
	}
	if !open.seesBoard("seo") || !open.seesBoard("") {
		t.Fatal("open section must be visible from wide main")
	}
	if open.canSee(resCampaigns, ref{id: "other", owner: "u2", board: "seo"}) {
		t.Fatal("created role sees another owner's row")
	}
	if !open.canSee(resCampaigns, ref{id: "mine", owner: "u1", board: "seo"}) {
		t.Fatal("created role must see its own row")
	}
	closed := open
	closed.boardOpen = map[string]bool{config.MainBoardID: true}
	if closed.seesBoard("seo") {
		t.Fatal("closed section must stay hidden")
	}
	narrow := open
	narrow.includeAll = false
	if narrow.seesBoard("seo") || !narrow.seesBoard("main") {
		t.Fatal("flag off shows only the current section")
	}
	read := open
	read.boardRoles = map[string]string{"seo": config.BoardRoleRead, config.MainBoardID: config.BoardRoleEdit}
	if read.canWrite(resCampaigns, ref{id: "mine", owner: "u1", board: "seo"}) {
		t.Fatal("read role on seo must not write from main")
	}
	if !read.canWrite(resCampaigns, ref{id: "m", owner: "u1", board: "main"}) {
		t.Fatal("edit role on main must still write main")
	}
}

func TestWideViewListsAndScope(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "main1", Name: "Main", BoardID: "main"},
		{ID: "seo1", Name: "SEO", BoardID: "seo"},
	}}
	hs, srv := boardTestServer(t, groups, "", func(s *Server) {
		_ = s.users.Add(security.User{
			ID: "u-main", Login: "onlymain", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"main"},
			Access: security.Access{Sections: sectionStrings(userSections)},
		})
	})
	dir := t.TempDir()
	spendPath := filepath.Join(dir, "spend.json")
	entries := []config.SpendEntry{
		{ID: "sp-main", GroupID: "main1", Cents: 100, Currency: "USD"},
		{ID: "sp-seo", GroupID: "seo1", Cents: 200, Currency: "USD"},
	}
	b, _ := json.Marshal(entries)
	if err := os.WriteFile(spendPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	srv.cfg.SpendPath = spendPath

	admin, _ := boardLogin(t, hs.URL, "admin", "p@ss")
	assertGroupIDs(t, admin, hs.URL, "main", map[string]bool{"main1": true, "seo1": true})
	assertScopeIDs(t, srv, admin, hs.URL, "main", map[string]bool{"main1": true, "seo1": true})
	assertSpendGroups(t, admin, hs.URL, "main", map[string]bool{"main1": true, "seo1": true})

	setIncludeAll(t, srv, false)
	assertGroupIDs(t, admin, hs.URL, "main", map[string]bool{"main1": true})
	assertScopeIDs(t, srv, admin, hs.URL, "main", map[string]bool{"main1": true})
	assertSpendGroups(t, admin, hs.URL, "main", map[string]bool{"main1": true})

	setIncludeAll(t, srv, true)
	limited, _ := boardLogin(t, hs.URL, "onlymain", "p@ss")
	assertGroupIDs(t, limited, hs.URL, "main", map[string]bool{"main1": true})
	assertScopeIDs(t, srv, limited, hs.URL, "main", map[string]bool{"main1": true})
	assertSpendGroups(t, limited, hs.URL, "main", map[string]bool{"main1": true})
}

func TestWidePutKeepsOtherCampaigns(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "main1", Name: "Main", BoardID: "main"},
		{ID: "seo1", Name: "SEO", BoardID: "seo"},
	}}
	hs, _ := boardTestServer(t, groups, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPut, hs.URL+"/api/groups", "main", csrf, []config.Group{
		{ID: "main1", Name: "Main renamed", BoardID: "main"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put %d %s", resp.StatusCode, raw)
	}
	byID := groupsByID(groups.list)
	if byID["seo1"].Name != "SEO" || byID["seo1"].BoardID != "seo" {
		t.Fatalf("seo removed or moved: %+v", groups.list)
	}
	if byID["main1"].Name != "Main renamed" {
		t.Fatalf("main not saved: %+v", byID["main1"])
	}
}

func TestPointPutKeepsOtherCampaign(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "main1", Name: "Main", BoardID: "main"},
		{ID: "seo1", Name: "SEO", BoardID: "seo"},
	}}
	hs, srv := boardTestServer(t, groups, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPut, hs.URL+"/api/groups/main1", "main", csrf, config.Group{
		ID: "main1", Name: "Main edited", BoardID: "main",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("point put %d %s", resp.StatusCode, raw)
	}
	byID := groupsByID(groups.list)
	if byID["seo1"].Name != "SEO" || byID["seo1"].BoardID != "seo" {
		t.Fatalf("seo touched: %+v", groups.list)
	}
	if byID["main1"].Name != "Main edited" {
		t.Fatalf("main not edited: %+v", byID["main1"])
	}

	setIncludeAll(t, srv, false)
	resp, raw = boardJSON(t, c, http.MethodPut, hs.URL+"/api/groups/main1", "main", csrf, config.Group{
		ID: "main1", Name: "Main again", BoardID: "main",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("narrow point put %d %s", resp.StatusCode, raw)
	}
	byID = groupsByID(groups.list)
	if byID["seo1"].Name != "SEO" {
		t.Fatalf("narrow put touched seo: %+v", groups.list)
	}
}

func TestPutOfferKeepsBoard(t *testing.T) {
	dir := t.TempDir()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	offers := NewJSONStore[config.Offer](filepath.Join(dir, "offers.json"))
	if err := offers.Save(context.Background(), []config.Offer{{
		ID: "seoOff", Name: "Offer", URL: "https://example.com/o", BoardID: "seo",
	}}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        &scopeStats{},
		Groups:       &boardMemGroups{},
		Boards:       NewJSONStore[config.Board](filepath.Join(dir, "boards.json")),
		Offers:       offers,
		UsersFile:    filepath.Join(dir, "users.json"),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.cfg.Boards.Save(context.Background(), []config.Board{
		{ID: config.MainBoardID, Name: mainBoardName},
		{ID: "seo", Name: "SEO"},
		{ID: "aso", Name: "ASO"},
	}); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPut, hs.URL+"/api/offers/seoOff", "main", csrf, config.Offer{
		ID: "seoOff", Name: "Offer renamed", URL: "https://example.com/o", BoardID: "main",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put offer %d %s", resp.StatusCode, raw)
	}
	list, err := srv.cfg.Offers.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].BoardID != "seo" || list[0].Name != "Offer renamed" {
		t.Fatalf("offer %+v", list)
	}
}

func TestCrossBoardLinkRejected(t *testing.T) {
	v := viewer{admin: true, boardCurrent: config.MainBoardID, includeAll: true, boardsAll: true}
	aso := linkCatalog{resOffers: {"asoOff": ref{id: "asoOff", board: "aso"}}}
	seoOffer := linkCatalog{resOffers: {"seoOff": ref{id: "seoOff", board: "seo"}}}
	base := config.Group{ID: "seo1", Name: "S", BoardID: "seo"}
	bad := base
	bad.Streams = []config.Stream{{Name: "s", Out: config.Output{Offers: []config.ShareRef{{ID: "asoOff", Share: 100}}}}}
	if msg := checkLinks(v, bad, &base, aso); msg == "" {
		t.Fatal("seo campaign must not link an aso offer")
	}
	if msg := validateGroupLinks(v, []config.Group{base}, []config.Group{bad}, aso); msg == "" {
		t.Fatal("visible seo edit must be checked from main")
	}
	ok := base
	ok.Streams = []config.Stream{{Name: "s", Out: config.Output{Offers: []config.ShareRef{{ID: "seoOff", Share: 100}}}}}
	if msg := checkLinks(v, ok, &base, seoOffer); msg != "" {
		t.Fatalf("same-section link: %s", msg)
	}

	groups := &boardMemGroups{list: []config.Group{base}}
	hs, srv := boardTestServer(t, groups, "", nil)
	srv.cfg.Offers = NewJSONStore[config.Offer](filepath.Join(t.TempDir(), "offers.json"))
	if err := srv.cfg.Offers.Save(context.Background(), []config.Offer{{
		ID: "asoOff", Name: "A", URL: "https://example.com/a", BoardID: "aso",
	}}); err != nil {
		t.Fatal(err)
	}
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardJSON(t, c, http.MethodPut, hs.URL+"/api/groups/seo1", "main", csrf, bad)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("link put %d %s", resp.StatusCode, raw)
	}
	if groups.list[0].BoardID != "seo" || len(groups.list[0].Streams) != 0 {
		t.Fatalf("campaign changed: %+v", groups.list[0])
	}
}

func TestS2SPutKeepsBoard(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "seoCamp", Name: "SEO", BoardID: "seo", PostbackIDs: []string{"tpl-main"}},
	}}
	hs, srv := boardTestServer(t, groups, "", nil)
	srv.cfg.S2S = NewJSONStore[config.OutboundPostback](filepath.Join(t.TempDir(), "s2s.json"))
	seed := []config.OutboundPostback{
		{ID: "tpl-main", Name: "Main", URL: "https://pb.example/main", Method: "GET", Statuses: []string{"sale"}, BoardID: "main"},
		{ID: "tpl-seo", Name: "SEO", URL: "https://pb.example/seo", Method: "GET", Statuses: []string{"sale"}, BoardID: "seo"},
	}
	if err := srv.cfg.S2S.Save(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	body := []config.OutboundPostback{
		{ID: "tpl-main", Name: "Main", URL: "https://pb.example/main", Method: "GET", Statuses: []string{"sale"}, BoardID: "main"},
		{ID: "tpl-seo", Name: "SEO renamed", URL: "https://pb.example/seo", Method: "GET", Statuses: []string{"sale"}, BoardID: "main"},
	}
	resp, raw := boardJSON(t, c, http.MethodPut, hs.URL+"/api/s2s", "main", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put s2s %d %s", resp.StatusCode, raw)
	}
	got, err := srv.cfg.S2S.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("duplicated templates: %+v", got)
	}
	byID := map[string]config.OutboundPostback{}
	for _, p := range got {
		if _, ok := byID[p.ID]; ok {
			t.Fatalf("duplicate id %s", p.ID)
		}
		byID[p.ID] = p
	}
	if byID["tpl-seo"].BoardID != "seo" || byID["tpl-seo"].Name != "SEO renamed" {
		t.Fatalf("seo template: %+v", byID["tpl-seo"])
	}
	if byID["tpl-main"].BoardID != "main" {
		t.Fatalf("main template: %+v", byID["tpl-main"])
	}

	onlyMain := []config.OutboundPostback{body[0]}
	resp, raw = boardJSON(t, c, http.MethodPut, hs.URL+"/api/s2s", "main", csrf, onlyMain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("omit seo %d %s", resp.StatusCode, raw)
	}
	got, err = srv.cfg.S2S.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || postbackIDs(got)["tpl-seo"] || !postbackIDs(got)["tpl-main"] {
		t.Fatalf("visible unused seo template must be dropped: %+v", got)
	}

	resp, raw = boardJSON(t, c, http.MethodPut, hs.URL+"/api/s2s", "main", csrf, []config.OutboundPostback{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("used template %d %s", resp.StatusCode, raw)
	}
	got, err = srv.cfg.S2S.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !postbackIDs(got)["tpl-main"] {
		t.Fatalf("used main template removed: %+v", got)
	}
}

func TestS2SWidePutDropsVisibleTemplate(t *testing.T) {
	hs, srv := boardTestServer(t, &boardMemGroups{}, "", nil)
	srv.cfg.S2S = NewJSONStore[config.OutboundPostback](filepath.Join(t.TempDir(), "s2s.json"))
	seed := []config.OutboundPostback{
		{ID: "tpl-main", Name: "Main", URL: "https://pb.example/main", Method: "GET", Statuses: []string{"sale"}, BoardID: "main"},
		{ID: "tpl-seo", Name: "SEO", URL: "https://pb.example/seo", Method: "GET", Statuses: []string{"sale"}, BoardID: "seo"},
	}
	if err := srv.cfg.S2S.Save(context.Background(), append([]config.OutboundPostback(nil), seed...)); err != nil {
		t.Fatal(err)
	}
	admin, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	onlyMain := []config.OutboundPostback{seed[0]}
	resp, raw := boardJSON(t, admin, http.MethodPut, hs.URL+"/api/s2s", "main", csrf, onlyMain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wide put %d %s", resp.StatusCode, raw)
	}
	got, err := srv.cfg.S2S.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := postbackIDs(got)
	if len(got) != 1 || !ids["tpl-main"] || ids["tpl-seo"] {
		t.Fatalf("unused visible seo template: %+v", got)
	}
	if got[0].ID != "tpl-main" || got[0].BoardID != "main" {
		t.Fatalf("main template: %+v", got[0])
	}

	if err := srv.cfg.S2S.Save(context.Background(), append([]config.OutboundPostback(nil), seed...)); err != nil {
		t.Fatal(err)
	}
	setIncludeAll(t, srv, false)
	resp, raw = boardJSON(t, admin, http.MethodPut, hs.URL+"/api/s2s", "main", csrf, onlyMain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("narrow put %d %s", resp.StatusCode, raw)
	}
	got, err = srv.cfg.S2S.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]config.OutboundPostback{}
	for _, p := range got {
		if _, ok := byID[p.ID]; ok {
			t.Fatalf("duplicate id %s", p.ID)
		}
		byID[p.ID] = p
	}
	if len(got) != 2 || byID["tpl-seo"].BoardID != "seo" || byID["tpl-seo"].Name != "SEO" {
		t.Fatalf("hidden seo template changed: %+v", got)
	}
	if byID["tpl-main"].BoardID != "main" {
		t.Fatalf("main template: %+v", byID["tpl-main"])
	}
}

func TestWideMainRowRole(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "main1", Name: "Main", BoardID: "main"},
		{ID: "mine", Name: "Mine", BoardID: "seo", CreatedBy: "u1"},
		{ID: "other", Name: "Other", BoardID: "seo", CreatedBy: "u2"},
	}}
	hs, srv := boardTestServer(t, groups, "", func(s *Server) {
		full := security.Access{
			Sections: sectionStrings(userSections),
			Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
		}
		_ = s.users.Add(security.User{
			ID: "u1", Login: "created", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"main", "seo"}, Access: full,
		})
		_ = s.users.Add(security.User{
			ID: "u2", Login: "reader", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"main", "seo"}, Access: full,
		})
	})
	ctx := context.Background()
	rawBoards, err := srv.cfg.Boards.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rawBoards {
		if rawBoards[i].ID == "seo" {
			rawBoards[i].Members = []config.BoardMember{
				{UserID: "u1", Role: config.BoardRoleCreated},
				{UserID: "u2", Role: config.BoardRoleRead},
			}
		}
	}
	if err := srv.cfg.Boards.Save(ctx, rawBoards); err != nil {
		t.Fatal(err)
	}

	created, _ := boardLogin(t, hs.URL, "created", "p@ss")
	resp, raw := boardGet(t, created, hs.URL+"/api/groups", "main")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("created list %d %s", resp.StatusCode, raw)
	}
	seen := decodeGroupIDs(t, raw)
	if !seen["main1"] || !seen["mine"] || seen["other"] {
		t.Fatalf("created sees %v", seen)
	}

	reader, csrf := boardLogin(t, hs.URL, "reader", "p@ss")
	resp, raw = boardJSON(t, reader, http.MethodPut, hs.URL+"/api/groups/mine", "main", csrf, config.Group{
		ID: "mine", Name: "hacked", BoardID: "seo", CreatedBy: "u1",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read write %d %s", resp.StatusCode, raw)
	}
	if groupsByID(groups.list)["mine"].Name != "Mine" {
		t.Fatalf("read role changed seo: %+v", groups.list)
	}

	dir := t.TempDir()
	srv.cfg.SpendPath = filepath.Join(dir, "spend.json")
	if err := os.WriteFile(srv.cfg.SpendPath, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-48 * time.Hour)
	resp, raw = boardJSON(t, reader, http.MethodPost, hs.URL+"/api/spend", "main", csrf, map[string]any{
		"group_id": "mine",
		"zone":     "UTC",
		"from":     now.Format("2006-01-02T15:04"),
		"to":       now.Add(time.Hour).Format("2006-01-02T15:04"),
		"amount":   "1.00",
		"currency": "USD",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read spend %d %s", resp.StatusCode, raw)
	}
}

func TestMergeNewGroupUsesCurrentBoard(t *testing.T) {
	v := viewer{admin: true, boardCurrent: config.MainBoardID, includeAll: true, boardsAll: true}
	stored := []config.Group{{ID: "seo1", Name: "SEO", BoardID: "seo"}}
	body := []config.Group{{ID: "new1", Name: "New", BoardID: "seo"}}
	got, code, msg := mergeGroups(v, stored, body)
	if code != 0 {
		t.Fatalf("merge %d %s", code, msg)
	}
	byID := groupsByID(got)
	if byID["seo1"].BoardID != "seo" {
		t.Fatalf("seo moved: %+v", byID["seo1"])
	}
	if !config.SameBoard(byID["new1"].BoardID, config.MainBoardID) {
		t.Fatalf("new board: %+v", byID["new1"])
	}
}

func setIncludeAll(t *testing.T, srv *Server, on bool) {
	t.Helper()
	ctx := context.Background()
	raw, err := srv.cfg.Boards.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw = boardsWithMain(raw)
	for i := range raw {
		if !config.SameBoard(raw[i].ID, config.MainBoardID) {
			continue
		}
		if on {
			raw[i].IncludeAll = nil
			continue
		}
		off := false
		raw[i].IncludeAll = &off
	}
	if err := srv.cfg.Boards.Save(ctx, raw); err != nil {
		t.Fatal(err)
	}
}

func mustBoards(t *testing.T, srv *Server) []config.Board {
	t.Helper()
	list, err := srv.cfg.Boards.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func decodeBoardItems(t *testing.T, raw []byte) []config.Board {
	t.Helper()
	var out boardsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func boardByID(t *testing.T, list []config.Board, id string) config.Board {
	t.Helper()
	for _, b := range list {
		if config.SameBoard(b.ID, id) {
			return b
		}
	}
	t.Fatalf("board %s missing", id)
	return config.Board{}
}

func decodeGroupIDs(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	var list []config.Group
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("groups json: %v %s", err, raw)
	}
	return groupIDSet(list)
}

func assertGroupIDs(t *testing.T, c *http.Client, base, board string, want map[string]bool) {
	t.Helper()
	resp, raw := boardGet(t, c, base+"/api/groups", board)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("groups %d %s", resp.StatusCode, raw)
	}
	got := decodeGroupIDs(t, raw)
	if len(got) != len(want) {
		t.Fatalf("groups %v want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("groups %v missing %s", got, id)
		}
	}
}

func assertScopeIDs(t *testing.T, srv *Server, c *http.Client, base, board string, want map[string]bool) {
	t.Helper()
	st := srv.cfg.Stats.(*scopeStats)
	resp, raw := boardGet(t, c, base+"/api/stats/summary", board)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary %d %s", resp.StatusCode, raw)
	}
	var values []string
	for _, cond := range st.last {
		if cond.Field == "campaign_id" && cond.Scope {
			values = cond.Values
		}
	}
	if len(values) == 0 {
		t.Fatalf("empty campaign scope: %+v", st.last)
	}
	got := map[string]bool{}
	for _, id := range values {
		got[id] = true
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("scope %v missing %s (conds %+v)", got, id, st.last)
		}
	}
	for id := range got {
		if id == store.ScopeNone {
			t.Fatalf("scope none: %+v", st.last)
		}
		if !want[id] {
			t.Fatalf("scope has %s: %+v", id, st.last)
		}
	}
}

func assertSpendGroups(t *testing.T, c *http.Client, base, board string, want map[string]bool) {
	t.Helper()
	resp, raw := boardGet(t, c, base+"/api/spend", board)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spend %d %s", resp.StatusCode, raw)
	}
	var list []config.SpendEntry
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range list {
		got[e.GroupID] = true
	}
	if len(got) != len(want) {
		t.Fatalf("spend %v want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("spend %v missing %s", got, id)
		}
	}
}

func postbackIDs(list []config.OutboundPostback) map[string]bool {
	out := map[string]bool{}
	for _, p := range list {
		out[p.ID] = true
	}
	return out
}
