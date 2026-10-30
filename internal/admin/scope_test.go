package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

func TestViewerMode(t *testing.T) {
	admin := viewer{admin: true}
	if admin.mode(resOffers) != security.ModeFull {
		t.Fatal("admin mode")
	}
	noGrant := viewer{grants: map[string]security.Grant{}}
	if noGrant.mode(resOffers) != security.ModeRead {
		t.Fatal("missing grant -> read")
	}
	sel := viewer{grants: map[string]security.Grant{
		resOffers: {Mode: security.ModeSelected, IDs: []string{"a"}},
	}}
	if sel.mode(resOffers) != security.ModeSelected {
		t.Fatal("selected mode")
	}
}

func TestCanSeeBranches(t *testing.T) {
	r := ref{id: "1", label: "L", owner: "u1"}
	admin := viewer{admin: true}
	if !admin.canSee(resOffers, r) || !admin.canWrite(resOffers, r) || !admin.canCreate(resOffers) {
		t.Fatal("admin see/write/create")
	}
	read := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeRead}}}
	if !read.canSee(resOffers, r) || read.canWrite(resOffers, r) || read.canCreate(resOffers) {
		t.Fatal("read only")
	}
	full := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeFull}}}
	if !full.canSee(resOffers, r) || !full.canWrite(resOffers, r) || !full.canCreate(resOffers) {
		t.Fatal("full")
	}
	sel := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected, IDs: []string{"1"}}}}
	if !sel.canSee(resOffers, r) || !sel.canWrite(resOffers, r) {
		t.Fatal("selected by id")
	}
	sel2 := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected, Labels: []string{"L"}}}}
	if !sel2.canSee(resOffers, r) {
		t.Fatal("selected by label")
	}
	selEmpty := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected}}}
	if selEmpty.canSee(resOffers, r) {
		t.Fatal("selected empty lists see nothing")
	}
	created := viewer{uid: "u1", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated}}}
	if !created.canSee(resOffers, r) || !created.canWrite(resOffers, r) || !created.canCreate(resOffers) {
		t.Fatal("created own")
	}
	other := viewer{uid: "u2", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated}}}
	if other.canSee(resOffers, r) || other.canWrite(resOffers, r) {
		t.Fatal("created other uid")
	}
	createdExtra := viewer{uid: "u2", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated, IDs: []string{"1"}}}}
	if createdExtra.canSee(resOffers, r) || createdExtra.canWrite(resOffers, r) {
		t.Fatal("created: an id list opens nothing, only own records")
	}
	createdLabel := viewer{uid: "u2", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated, Labels: []string{"L"}}}}
	if createdLabel.canSee(resOffers, r) {
		t.Fatal("created: a label list opens nothing")
	}
	emptyLabel := viewer{grants: map[string]security.Grant{resOffers: {Mode: security.ModeSelected, Labels: []string{""}}}}
	if emptyLabel.canSee(resOffers, ref{id: "x", label: ""}) {
		t.Fatal("empty label must not match empty labels entry")
	}
}

func TestCanWriteBoardRoleCreated(t *testing.T) {
	foreign := ref{id: "c1", owner: "u2", board: "seo"}
	own := ref{id: "c2", owner: "u1", board: "seo"}
	createdBoard := viewer{
		uid:          "u1",
		boardCurrent: "seo",
		boardRole:    config.BoardRoleCreated,
		grants:       map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
	}
	if createdBoard.canWrite(resCampaigns, foreign) {
		t.Fatal("board created: foreign owner must not write")
	}
	if !createdBoard.canWrite(resCampaigns, own) {
		t.Fatal("board created: own owner must write")
	}
	fullNoBoardRole := viewer{
		uid:    "u1",
		grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}},
	}
	if !fullNoBoardRole.canWrite(resCampaigns, foreign) {
		t.Fatal("ModeFull without board role must write foreign record")
	}
}

func TestScopedCampaignsHideOrigin(t *testing.T) {
	v := viewer{grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeSelected}}}
	if !scopedCampaigns(v) {
		t.Fatal("selected campaign grant is scoped")
	}
	v.hidden = map[string]bool{}
	if scopedCampaigns(v) {
		v.hidden["origin_campaign"] = true
		v.hidden["origin_campaign_id"] = true
	}
	if !v.hidden["origin_campaign"] || !v.hidden["origin_campaign_id"] {
		t.Fatalf("origin fields hidden for scoped viewer: %v", v.hidden)
	}
	full := viewer{grants: map[string]security.Grant{resCampaigns: {Mode: security.ModeFull}}}
	if scopedCampaigns(full) {
		t.Fatal("full campaign grant is not scoped")
	}
}

func scopeOriginServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		UsersFile:  filepath.Join(dir, "users.json"),
		Sessions:   security.NewMemoryStore(),
		Stats:      richStats{},
		Groups:     &memGroups{list: scopeCampaigns()},
		Limiter:    allowAll{},
		SessionTTL: time.Hour, CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustUser(t, s, "sel", "secret1", security.Access{
		Sections: userSectionsAll(),
		Grants: map[string]security.Grant{
			resCampaigns: {Mode: security.ModeSelected, IDs: []string{"c1"}, Labels: []string{"promo"}},
		},
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "sel", "secret1"); code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	return srv, c
}

func TestScopedCampaignOriginHiddenEndpoints(t *testing.T) {
	srv, c := scopeOriginServer(t)
	base := srv.URL

	for _, ep := range []string{"/api/logs", "/api/postbacks"} {
		resp, err := c.Get(base + ep)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := ioReadAllString(resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %d %s", ep, resp.StatusCode, body)
		}
		for _, key := range []string{"origin_campaign", "origin_campaign_id", "S_origin_campaign"} {
			if strings.Contains(body, key) {
				t.Fatalf("%s leaks %q: %s", ep, key, body)
			}
		}
	}

	for _, p := range []string{
		"/api/logs?f=origin_campaign:eq:x",
		"/api/postbacks?f=origin_campaign:eq:x",
		"/api/logs?sort=origin_campaign",
		"/api/postbacks?sort=origin_campaign",
		"/api/log-values?table=events&field=origin_campaign",
		"/api/log-values?table=conversions&field=origin_campaign",
	} {
		resp, err := c.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := ioReadAllString(resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", p, resp.StatusCode, body)
		}
	}

	resp, err := c.Get(base + "/api/log-fields")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := ioReadAllString(resp)
	resp.Body.Close()
	for _, key := range []string{`"origin_campaign"`, `"origin_campaign_id"`} {
		if strings.Contains(body, key) {
			t.Fatalf("log-fields lists origin: %s", body)
		}
	}

	for _, p := range []string{"/api/logs/export", "/api/postbacks/export"} {
		resp, err = c.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = ioReadAllString(resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %d", p, resp.StatusCode)
		}
		head := strings.Split(strings.SplitN(body, "\n", 2)[0], ",")
		for _, col := range head {
			if col == "origin_campaign" || col == "origin_campaign_id" {
				t.Fatalf("%s header has %s", p, col)
			}
		}
	}
}

func ioReadAllString(resp *http.Response) (string, error) {
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}
