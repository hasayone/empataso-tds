package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

type boardMemGroups struct {
	list []config.Group
}

func (m *boardMemGroups) List(context.Context) ([]config.Group, error) {
	return append([]config.Group(nil), m.list...), nil
}

func (m *boardMemGroups) Save(_ context.Context, g []config.Group) error {
	m.list = append([]config.Group(nil), g...)
	return nil
}

type scopeStats struct {
	fakeStats
	last []store.Cond
}

func (s *scopeStats) SummaryFiltered(_ context.Context, _, _ time.Time, conds []store.Cond) (store.StatsSummary, error) {
	s.last = append([]store.Cond(nil), conds...)
	return store.StatsSummary{}, nil
}

func boardTestServer(t *testing.T, groups *boardMemGroups, usersFile string, addUsers func(*Server)) (*httptest.Server, *Server) {
	t.Helper()
	dir := t.TempDir()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	st := scopeStats{}
	usersPath := usersFile
	if usersPath == "" {
		usersPath = filepath.Join(dir, "users.json")
	}
	srv, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        &st,
		Groups:       groups,
		Boards:       NewJSONStore[config.Board](filepath.Join(dir, "boards.json")),
		UsersFile:    usersPath,
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if addUsers != nil {
		addUsers(srv)
		if err := srv.persistUsers(); err != nil {
			t.Fatal(err)
		}
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
	return hs, srv
}

func boardLogin(t *testing.T, base, user, pass string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	body := `{"login":"` + user + `","password":"` + pass + `"}`
	resp, err := c.Post(base+"/api/login", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ CSRF string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return c, out.CSRF
}

func boardGet(t *testing.T, c *http.Client, url, board string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if board != "" {
		req.Header.Set(boardHeader, board)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

func boardJSON(t *testing.T, c *http.Client, method, url, board, csrf string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if board != "" {
		req.Header.Set(boardHeader, board)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

func TestBoardAdminScopeExcludesOtherSection(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "main1", Name: "Main camp", BoardID: "main"},
		{ID: "seo1", Name: "SEO camp", BoardID: "seo"},
		{ID: "aso1", Name: "ASO camp", BoardID: "aso"},
	}}
	hs, srv := boardTestServer(t, groups, "", nil)
	c, _ := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, _ := boardGet(t, c, hs.URL+"/api/stats/summary", "seo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary status %d", resp.StatusCode)
	}
	st := srv.cfg.Stats.(*scopeStats)
	foundIn := false
	for _, cnd := range st.last {
		if cnd.Field == "campaign_id" && cnd.Op == "in" {
			foundIn = true
			for _, id := range cnd.Values {
				if id == "aso1" {
					t.Fatal("aso campaign in seo scope")
				}
			}
		}
	}
	if !foundIn {
		t.Fatalf("expected in scope, got %+v", st.last)
	}
}

func TestBoardForeignHeaderForbidden(t *testing.T) {
	groups := &boardMemGroups{}
	hs, _ := boardTestServer(t, groups, "", func(s *Server) {
		_ = s.users.Add(security.User{
			ID: "u1", Login: "u1", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"seo"},
			Access: security.Access{Sections: sectionStrings(userSections)},
		})
	})
	c, _ := boardLogin(t, hs.URL, "u1", "p@ss")
	resp, _ := boardGet(t, c, hs.URL+"/api/groups", "aso")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign board: %d", resp.StatusCode)
	}
}

func TestBoardEnvAdminSwitchable(t *testing.T) {
	groups := &boardMemGroups{}
	hs, _ := boardTestServer(t, groups, "", nil)
	c, _ := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, raw := boardGet(t, c, hs.URL+"/api/boards", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("boards %d", resp.StatusCode)
	}
	var out boardsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Switchable || len(out.Items) < 3 {
		t.Fatalf("env admin switchable: %+v", out)
	}
}

func TestBoardSummaryMainAllCampaigns(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "c1", BoardID: "main"},
		{ID: "c2", BoardID: "main"},
		{ID: "c3", BoardID: "main"},
	}}
	hs, srv := boardTestServer(t, groups, "", nil)
	c, _ := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, _ := boardGet(t, c, hs.URL+"/api/stats/summary", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary status %d", resp.StatusCode)
	}
	st := srv.cfg.Stats.(*scopeStats)
	var inCond, ninCond *store.Cond
	for i := range st.last {
		switch st.last[i].Op {
		case "in":
			inCond = &st.last[i]
		case "nin":
			ninCond = &st.last[i]
		}
	}
	if ninCond != nil && len(ninCond.Values) == 0 {
		t.Fatalf("empty nin scope: %+v", st.last)
	}
	if inCond == nil || len(inCond.Values) == 0 {
		t.Fatalf("expected in scope with ids, got %+v", st.last)
	}
}

func TestBoardSingleSectionNotSwitchable(t *testing.T) {
	groups := &boardMemGroups{}
	hs, _ := boardTestServer(t, groups, "", func(s *Server) {
		_ = s.users.Add(security.User{
			ID: "u1", Login: "u1", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"seo"}, NoMainBoard: true,
			Access: security.Access{Sections: sectionStrings(userSections)},
		})
	})
	c, _ := boardLogin(t, hs.URL, "u1", "p@ss")
	resp, raw := boardGet(t, c, hs.URL+"/api/boards", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("boards %d", resp.StatusCode)
	}
	var out boardsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Switchable {
		t.Fatalf("switchable true: %+v", out)
	}
}

func TestBoardAliasTakenNoName(t *testing.T) {
	stored := []config.Group{
		{ID: "a", BoardID: "main", Aliases: []string{"x"}},
		{ID: "b", BoardID: "seo"},
	}
	merged := []config.Group{
		stored[0],
		{ID: "b", BoardID: "seo", Aliases: []string{"x"}},
	}
	v := viewer{admin: true, boardCurrent: "seo"}
	if !aliasTakenAmong(merged, "b", "x") {
		t.Fatal("aliasTakenAmong expected true")
	}
	msg := checkCampaignAliases(v, stored, merged)
	if msg != "alias taken" {
		t.Fatalf("msg %q", msg)
	}
}

func TestBoardPutGroupsKeepsOtherSection(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "m1", Name: "Main", BoardID: "main"},
		{ID: "s1", Name: "SEO", BoardID: "seo"},
	}}
	hs, _ := boardTestServer(t, groups, "", nil)
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, _ := boardJSON(t, c, http.MethodPut, hs.URL+"/api/groups", "seo", csrf, []config.Group{
		{ID: "s1", Name: "SEO updated", BoardID: "seo"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put groups %d", resp.StatusCode)
	}
	var mainOK bool
	for _, g := range groups.list {
		if g.ID == "m1" && g.Name == "Main" {
			mainOK = true
		}
	}
	if !mainOK {
		t.Fatalf("main campaign lost: %+v", groups.list)
	}
}

func TestBoardCrossSectionCampaignLink(t *testing.T) {
	cat := linkCatalog{
		resCampaigns: {"aso1": ref{id: "aso1", board: "aso"}},
	}
	g := config.Group{ID: "s1", BoardID: "seo", Redirect: "group", Out: "aso1"}
	v := viewer{admin: true, boardCurrent: "seo", grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}}}
	if msg := checkLinks(v, g, nil, cat); msg == "" {
		t.Fatal("expected link error")
	}
}

// TestBoardPostbackLogScopedForLimitedUser checks that a user with the conversions
// section reads the postback log limited to the campaigns of the sections they may open.
func TestBoardPostbackLogScopedForLimitedUser(t *testing.T) {
	groups := &boardMemGroups{list: []config.Group{
		{ID: "s1", Name: "SEO one", BoardID: "seo"},
		{ID: "a1", Name: "ASO one", BoardID: "aso"},
	}}
	stats := &limitStats{}
	hs, _ := boardTestServer(t, groups, "", func(s *Server) {
		s.cfg.Stats = stats
		_ = s.users.Add(security.User{
			ID: "u1", Login: "u1", Hash: mustHash(t, "p@ss"), Enabled: true,
			Type: security.UserRegular, BoardIDs: []string{"seo"},
			Access: security.Access{
				Sections: []string{string(secConversions)},
				Grants:   map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
			},
		})
	})
	c, _ := boardLogin(t, hs.URL, "u1", "p@ss")
	resp, _ := boardGet(t, c, hs.URL+"/api/postback-log", "seo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("postback-log %d", resp.StatusCode)
	}
	sc := stats.query.Scope
	if sc == nil {
		t.Fatal("a limited user must read a scoped log")
	}
	in := sc.Op == "in" && slices.Contains(sc.Values, "s1") && !slices.Contains(sc.Values, "a1")
	nin := sc.Op == "nin" && slices.Contains(sc.Values, "a1") && !slices.Contains(sc.Values, "s1")
	if !in && !nin {
		t.Fatalf("scope %+v, want only the SEO campaign", sc)
	}
	admin, _ := boardLogin(t, hs.URL, "admin", "p@ss")
	if resp, _ := boardGet(t, admin, hs.URL+"/api/postback-log", "seo"); resp.StatusCode != http.StatusOK || stats.query.Scope != nil {
		t.Fatalf("the administrator reads the whole log: %d %+v", resp.StatusCode, stats.query.Scope)
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := security.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
