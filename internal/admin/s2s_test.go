package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

func s2sAPI(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &memGroups{},
		S2S:          NewJSONStore[config.OutboundPostback](filepath.Join(dir, "s2s.json")),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
		UsersFile:    filepath.Join(dir, "users.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func TestS2SGetEmpty(t *testing.T) {
	_, srv := s2sAPI(t)
	c := newClient(t)
	login(t, c, srv.URL, "admin", "p@ss")
	resp, err := c.Get(srv.URL + "/api/s2s")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("GET %d %s", resp.StatusCode, b)
	}
}

func TestS2SPutAssignsID(t *testing.T) {
	_, srv := s2sAPI(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	body := []config.OutboundPostback{{
		Name: "hook", URL: "https://pb.example/hook", Method: "", Statuses: []string{"sale", "lead"},
	}}
	want := config.OutboundPostback{URL: body[0].URL, Method: http.MethodGet, Statuses: body[0].Statuses}
	want.ID = config.PostbackID(want.Signature())
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/s2s", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT %d %s", resp.StatusCode, b)
	}
	var got []config.OutboundPostback
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != want.ID || got[0].Method != http.MethodGet || got[0].Name != "hook" {
		t.Fatalf("saved %+v, want id %s", got, want.ID)
	}
}

func TestS2SPutRejectsUsedID(t *testing.T) {
	_, srv := s2sAPI(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	put := csrfDo(t, c, http.MethodPut, srv.URL+"/api/s2s", csrf, []config.OutboundPostback{{
		URL: "https://pb.example/hook", Method: "GET", Statuses: []string{"sale"},
	}})
	var saved []config.OutboundPostback
	if err := json.NewDecoder(put.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	put.Body.Close()
	if put.StatusCode != http.StatusOK || len(saved) != 1 || saved[0].ID == "" {
		t.Fatalf("seed %d %+v", put.StatusCode, saved)
	}
	id := saved[0].ID
	grp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/camp", csrf, config.Group{
		ID: "camp", Name: "Camp", PostbackIDs: []string{id},
	})
	grp.Body.Close()
	if grp.StatusCode != http.StatusOK {
		t.Fatalf("group put %d", grp.StatusCode)
	}
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/s2s", csrf, []config.OutboundPostback{})
	defer resp.Body.Close()
	var er struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatal(err)
	}
	want := "template " + id + " is used by 1 campaigns"
	if resp.StatusCode != http.StatusBadRequest || er.Error != want {
		t.Fatalf("remove %d %q, want %q", resp.StatusCode, er.Error, want)
	}
}

func TestS2SPutForbidden(t *testing.T) {
	s, srv := s2sAPI(t)
	if _, err := s.createUser(userDraft{
		Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: []string{"flows"}},
	}); err != nil {
		t.Fatal(err)
	}
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "bob", "secret1")
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/s2s", csrf, []config.OutboundPostback{})
	defer resp.Body.Close()
	var er struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || er.Error != "forbidden" {
		t.Fatalf("user put %d %q", resp.StatusCode, er.Error)
	}
}

func TestGroupPutKeepsPostbackIDs(t *testing.T) {
	_, srv := s2sAPI(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	resp := csrfDo(t, c, http.MethodPut, srv.URL+"/api/groups/camp", csrf, config.Group{
		ID: "camp", Name: "Camp", PostbackIDs: []string{"pb-abc"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put %d", resp.StatusCode)
	}
	got := fetchGroups(t, c, srv.URL)
	g, ok := groupByID(got, "camp")
	if !ok || len(g.PostbackIDs) != 1 || g.PostbackIDs[0] != "pb-abc" {
		t.Fatalf("group %+v ok=%v", g, ok)
	}
}
