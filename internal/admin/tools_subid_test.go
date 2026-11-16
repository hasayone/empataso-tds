package admin

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/geo"
	"github.com/egerkuzma/emtds/internal/security"
)

// sourceServer is an admin with the MaxMind test databases and a traffic-source store.
func sourceServer(t *testing.T, sources ...config.TrafficSource) (base string, c *http.Client, csrf string) {
	t.Helper()
	dir := t.TempDir()
	db, err := geo.OpenWith(geo.Paths{City: "../geo/testdata/GeoLite2-City-Test.mmdb", ASN: "../geo/testdata/GeoLite2-ASN-Test.mmdb"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json"))
	if err := store.Save(context.Background(), sources); err != nil {
		t.Fatal(err)
	}
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: allowAll{}, SessionTTL: time.Hour,
		DataDir: dir, Geo: db, GeoCityPath: "x", CronFile: filepath.Join(dir, "cron.json"),
		Sources: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c = &http.Client{Jar: jar}
	_, csrf = login(t, c, srv.URL, "admin", "p@ss")
	return srv.URL, c, csrf
}

func TestSimulateSubIDUsesGroupSource(t *testing.T) {
	base, c, csrf := sourceServer(t,
		config.TrafficSource{ID: "sa", Parameters: map[string]string{"sub_id_1": "sub1"}},
		config.TrafficSource{ID: "sb", Parameters: map[string]string{"sub_id_1": "x"}},
	)
	vip := config.Rules{SubIDs: map[string]config.SubIDFilter{"sub_id_1": {Flag: config.FlagB, Values: []string{"vip"}}}}
	groups := []config.Group{
		{ID: "a", Status: true, SourceID: "sa", Redirect: "show_text", Out: "A DEFAULT", Streams: []config.Stream{
			{Name: "vip", Status: true, Rules: vip, Out: config.Output{Redirect: "show_text", Out: "A VIP"}},
			{Name: "to_b", Status: true, Out: config.Output{Redirect: "group", Out: "b"}},
		}},
		{ID: "b", Status: true, SourceID: "sb", Redirect: "show_text", Out: "B DEFAULT", Streams: []config.Stream{
			{Name: "vip", Status: true, Rules: vip, Out: config.Output{Redirect: "show_text", Out: "B VIP"}},
		}},
	}
	run := func(query string) []simStep {
		t.Helper()
		var req simRequest
		req.Groups, req.Group = groups, "a"
		req.Visitor.IP, req.Visitor.UserAgent, req.Visitor.Query = "81.2.69.142", uaDesktop, query
		var out struct{ Path []simStep }
		if code := call(t, c, "POST", base+"/api/simulate", csrf, req, &out); code != 200 {
			t.Fatalf("status %d", code)
		}
		return out.Path
	}

	path := run("?sub1=vip")
	if len(path) != 1 || path[0].Selected != "vip" || path[0].Out != "A VIP" {
		t.Errorf("entry group reads its source's parameter: %+v", path)
	}
	path = run("?sub_id_1=vip")
	if len(path) != 2 || path[0].Selected != "to_b" || path[1].Selected != "-" || path[1].Out != "B DEFAULT" {
		t.Errorf("raw name under renaming sources never matches: %+v", path)
	}
	path = run("?x=vip")
	if len(path) != 2 || path[1].Selected != "vip" || path[1].Out != "B VIP" {
		t.Errorf("the next group reads its own source's parameter: %+v", path)
	}
}
