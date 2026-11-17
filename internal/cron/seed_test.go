package cron

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/detect"
)

func countGeo(c Config, kind string) (n int) {
	for _, s := range c.GeoDB.Sources {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

func countRelay(c Config) (n int) {
	for _, s := range c.IPLists.Sources {
		if s.Target == "ip_private_relay" && s.URL == seedIPSource.URL {
			n++
		}
	}
	return n
}

func writeCron(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cron.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultCarriesTheSeededSources(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if countGeo(c, "dbip_asn") != 1 || countRelay(c) != 1 {
		t.Fatalf("default sources: dbip_asn=%d relay=%d", countGeo(c, "dbip_asn"), countRelay(c))
	}
	if strings.Join(c.Seeded, ",") != SeedDBIPASN+","+SeedPrivateRelay {
		t.Fatalf("Seeded = %v", c.Seeded)
	}
	for _, s := range c.GeoDB.Sources {
		if s.Kind == "dbip_asn" && s.URL != "https://download.db-ip.com/free/dbip-asn-lite-{YYYY-MM}.mmdb.gz" {
			t.Fatalf("dbip_asn URL = %q", s.URL)
		}
	}
	for _, s := range c.IPLists.Sources {
		if s.Target == "ip_private_relay" && (s.Mode != "replace" || s.URL != "https://mask-api.icloud.com/egress-ip-ranges.csv") {
			t.Fatalf("private relay source = %+v", s)
		}
	}
}

func TestLoadSeedsAnOldFileInMemoryOnly(t *testing.T) {
	body := `{"ip_lists":{"enabled":true,"every_minutes":60,"sources":[{"url":"https://a.example/x.dat","target":"ip_x","mode":"replace"}]},` +
		`"geo_db":{"enabled":true,"every_minutes":4320,"sources":[{"kind":"city","url":"https://c.example/city.mmdb"}]}}`
	p := writeCron(t, body)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if countGeo(c, "dbip_asn") != 1 || countRelay(c) != 1 || len(c.GeoDB.Sources) != 2 || len(c.IPLists.Sources) != 2 {
		t.Fatalf("after Load: geo=%+v lists=%+v", c.GeoDB.Sources, c.IPLists.Sources)
	}
	if len(c.Seeded) != 2 {
		t.Fatalf("Seeded = %v", c.Seeded)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != body {
		t.Fatal("Load must not write the file")
	}
	// A second Load of the same file gives the same answer.
	again, _ := Load(p)
	if len(again.GeoDB.Sources) != 2 || len(again.IPLists.Sources) != 2 {
		t.Fatal("seeding must not depend on earlier loads")
	}
}

func TestLoadDoesNotBringBackARemovedSource(t *testing.T) {
	p := writeCron(t, `{"geo_db":{"sources":[{"kind":"city","url":"https://c.example/city.mmdb"}]},`+
		`"ip_lists":{"sources":[]},"seeded":["dbip_asn","ip_private_relay"]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if countGeo(c, "dbip_asn") != 0 || countRelay(c) != 0 {
		t.Fatalf("removed sources came back: geo=%+v lists=%+v", c.GeoDB.Sources, c.IPLists.Sources)
	}
}

func TestLoadSeedsOnlyTheMissingKey(t *testing.T) {
	p := writeCron(t, `{"geo_db":{"sources":[]},"ip_lists":{"sources":[]},"seeded":["dbip_asn"]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if countGeo(c, "dbip_asn") != 0 || countRelay(c) != 1 {
		t.Fatalf("geo=%+v lists=%+v", c.GeoDB.Sources, c.IPLists.Sources)
	}
}

func TestLoadDoesNotDuplicateASourceTheUserAlreadyHas(t *testing.T) {
	p := writeCron(t, `{"geo_db":{"sources":[{"kind":"dbip_asn","url":"`+seedGeoSource.URL+`"}]},`+
		`"ip_lists":{"sources":[{"url":"`+seedIPSource.URL+`","target":"ip_private_relay","mode":"merge"}]}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if countGeo(c, "dbip_asn") != 1 || countRelay(c) != 1 {
		t.Fatalf("geo=%+v lists=%+v", c.GeoDB.Sources, c.IPLists.Sources)
	}
	if len(c.Seeded) != 2 {
		t.Fatalf("Seeded = %v", c.Seeded)
	}
}

func TestLoadMissingFileKeepsDefaultSeeded(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(c.Seeded) != 2 || countGeo(c, "dbip_asn") != 1 {
		t.Fatalf("%+v, %v", c.Seeded, err)
	}
}

func TestKeepSeededUnitesRequestAndStored(t *testing.T) {
	stored := Config{Seeded: []string{SeedDBIPASN, SeedPrivateRelay}}
	if got := (Config{}).KeepSeeded(stored).Seeded; strings.Join(got, ",") != SeedDBIPASN+","+SeedPrivateRelay {
		t.Fatalf("a request without seeded lost the flags: %v", got)
	}
	got := Config{Seeded: []string{"future_key", SeedDBIPASN}}.KeepSeeded(Config{Seeded: []string{SeedPrivateRelay, SeedDBIPASN}}).Seeded
	if strings.Join(got, ",") != SeedPrivateRelay+","+SeedDBIPASN+",future_key" {
		t.Fatalf("union = %v", got)
	}
	if got := (Config{}).KeepSeeded(Config{}).Seeded; len(got) != 0 {
		t.Fatalf("nothing in, nothing out: %v", got)
	}
}

func TestSaveThenLoadKeepsARemovedSourceRemoved(t *testing.T) {
	p := writeCron(t, `{}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.IPLists.Sources = nil
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	again, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if countRelay(again) != 0 {
		t.Fatal("a source the user removed and saved must stay removed")
	}
}

func TestCleanLinesAcceptsPrivateRelayCSV(t *testing.T) {
	in := strings.Join([]string{
		"# Apple egress ranges",
		"172.224.226.0/27,GB,GB-EN,London,",
		"  2a02:26f7:b3c0:4000::/64,DE,DE-BE,Berlin,  ",
		"104.28.0.1,US,US-CA,San Francisco,",
		"not-an-address,US,US-CA,Nowhere,",
		",GB,GB-EN,London,",
		"999.1.1.1/24,XX,XX-XX,Void,",
		"",
	}, "\n")
	got := cleanLines(in, true)
	want := []string{"# Apple egress ranges", "172.224.226.0/27", "2a02:26f7:b3c0:4000::/64", "104.28.0.1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("cleanLines = %q, want %q", got, want)
	}
	if countEntries(got) != 3 {
		t.Fatalf("countEntries = %d", countEntries(got))
	}
}

func TestSplitSectionsReadsCSVRows(t *testing.T) {
	got := splitSections("# relay\n172.224.226.0/27,GB,GB-EN,London,\n", true)
	if len(got["ip_relay"]) != 1 || got["ip_relay"][0] != "172.224.226.0/27" {
		t.Fatalf("sections = %v", got)
	}
}

func TestPrivateRelayCSVSourceWritesTheList(t *testing.T) {
	e := newEnv(t)
	u := e.serve("/egress-ip-ranges.csv", "172.224.226.0/27,GB,GB-EN,London,\n2a02:26f7:b3c0:4000::/64,DE,DE-BE,Berlin,\n")
	msg, err := e.r.runIPLists(t.Context(), IPLists{Sources: []IPSource{{URL: u, Target: "ip_private_relay", Mode: "replace"}}})
	if err != nil || msg != "ip_private_relay: 2" {
		t.Fatalf("%q, %v", msg, err)
	}
	if got := e.list("ip_private_relay"); got != "172.224.226.0/27\n2a02:26f7:b3c0:4000::/64\n" {
		t.Fatalf("list = %q", got)
	}
}
func TestPlainListsDoNotUseCSVParsing(t *testing.T) {
	in := "1.2.3.4, comment\n5.6.7.8\n9.9.9.0/24,GB\n"
	got := cleanLines(in, false)
	if strings.Join(got, "|") != "5.6.7.8" {
		t.Fatalf("plain list kept %q, want only the clean line", got)
	}
	if got := splitSections("# x\n1.2.3.4, comment\n5.6.7.8\n", false); len(got["ip_x"]) != 1 {
		t.Fatalf("plain sectioned list = %v", got)
	}

	e := newEnv(t)
	u := e.serve("/list.txt", "1.2.3.4, comment\n5.6.7.8\n")
	if _, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{{URL: u, Target: "ip_plain"}}}); err != nil {
		t.Fatal(err)
	}
	if got := e.list("ip_plain"); got != "5.6.7.8\n" {
		t.Fatalf("plain list file = %q", got)
	}
}

func TestCSVSourceIsRecognisedByPathOrTarget(t *testing.T) {
	cases := []struct {
		src  IPSource
		want bool
	}{
		{IPSource{URL: "https://x.example/egress.csv", Target: "ip_x"}, true},
		{IPSource{URL: "https://x.example/Egress.CSV?v=1", Target: "ip_x"}, true},
		{IPSource{URL: " https://x.example/a.csv ", Target: ""}, true},
		{IPSource{URL: "https://x.example/list.txt", Target: detect.ListPrivateRelay}, true},
		{IPSource{URL: "https://x.example/list.txt", Target: "ip_x"}, false},
		{IPSource{URL: "https://x.example/csv", Target: "ip_x"}, false},
	}
	for _, c := range cases {
		if got := isCSVSource(c.src); got != c.want {
			t.Errorf("isCSVSource(%+v) = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestCSVPathSourceWritesTheFirstField(t *testing.T) {
	e := newEnv(t)
	u := e.serve("/ranges.csv", "10.1.0.0/16,AA,AA-1,Town,\n")
	if _, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{{URL: u, Target: "ip_csv"}}}); err != nil {
		t.Fatal(err)
	}
	if got := e.list("ip_csv"); got != "10.1.0.0/16\n" {
		t.Fatalf("list = %q", got)
	}
}

func TestListLimitIsLargerForPrivateRelay(t *testing.T) {
	if got := listLimit(IPSource{Target: detect.ListPrivateRelay}); got != 64<<20 {
		t.Fatalf("private relay limit = %d, want 64 MiB", got)
	}
	if got := listLimit(IPSource{Target: "ip_google"}); got != maxListBytes {
		t.Fatalf("other lists keep %d, got %d", maxListBytes, got)
	}
}
