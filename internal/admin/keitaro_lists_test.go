package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/kuztds/bridge/ktimport"
)

const ipListBody = "1.2.3.4\n10.0.0.0/8\n"

func TestBridgeGroupFiltersConvert(t *testing.T) {
	g := ktimport.Group{
		ID: "promo", Name: "kt: Promo", Status: true,
		Streams: []ktimport.Stream{{
			Name: "kt-1", Status: true,
			Rules: ktimport.Rules{
				IPList: ktimport.IPListFilter{Flag: 2, File: "kt-ip-abcdef012345.dat"},
				Org:    ktimport.ListFilter{Flag: 2, Raw: "Vodacom,MTN", Values: []string{"Vodacom", "MTN"}},
			},
			Out: ktimport.Output{Redirect: "show_html_404", Out: "<h1>no</h1>", Chance: 100},
		}},
	}
	got, err := convert[config.Group](g)
	if err != nil {
		t.Fatal(err)
	}
	st := got.Streams[0]
	if st.Rules.IPList.File != "kt-ip-abcdef012345.dat" || st.Rules.IPList.Flag != 2 {
		t.Fatalf("ip %+v", st.Rules.IPList)
	}
	if st.Rules.Org.Flag != 2 || len(st.Rules.Org.Values) != 2 || st.Rules.Org.Values[0] != "Vodacom" || st.Rules.Org.Values[1] != "MTN" {
		t.Fatalf("org %+v", st.Rules.Org)
	}
	if st.Out.Redirect != "show_html_404" {
		t.Fatal(st.Out.Redirect)
	}
}

func TestOfferQueryConverts(t *testing.T) {
	got, err := convert[config.AffiliateNetwork](ktimport.CatNetwork{
		ID: "kt-net-9", Name: "Net", Status: true, OfferQuery: "sub1={subid}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.OfferQuery != "sub1={subid}" {
		t.Fatalf("offer_query %q", got.OfferQuery)
	}
	if msg := validNetwork(got); msg != "" {
		t.Fatal(msg)
	}
}

func TestImportGroupsDropsInvalidOnly(t *testing.T) {
	local := config.Group{
		ID: "local", Name: "Local", Folder: strings.Repeat("я", 80),
		Streams: []config.Stream{{
			Name: "s",
			Out:  config.Output{Offers: []config.ShareRef{{ID: "o1", Share: -3}}},
		}},
	}
	groups := &memGroups{list: []config.Group{local}}
	s, _ := newKeitaroServers(t, "", "", groups, nil, nil)
	n, losses, err := s.importGroups(context.Background(), []ktimport.Group{
		{ID: "pad", Name: "kt: Pad", Folder: "  eu  ", Streams: []ktimport.Stream{{Name: "kt-1", Status: true}}},
		{ID: "bad", Name: "kt: Bad", Streams: []ktimport.Stream{{
			Name: "kt-2",
			Out:  ktimport.Output{Offers: []ktimport.ShareRef{{ID: "kt-off-1", Share: -1}}},
		}}},
		{ID: "bad", Name: "kt: Bad copy", Streams: []ktimport.Stream{{Name: "kt-9"}}},
		{ID: "long", Name: "kt: Long", Folder: strings.Repeat("я", 65)},
		{ID: "", Name: "kt: empty"},
		{ID: "local", Name: "kt: Hijack"},
	}, ktimport.Manifest{}, "")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(groups.list) != 2 {
		t.Fatalf("saved %+v", groups.list)
	}
	gotLocal, ok := savedGroup(groups.list, "local")
	if !ok || gotLocal.Name != "Local" || utf8.RuneCountInString(gotLocal.Folder) != 80 || gotLocal.Streams[0].Out.Offers[0].Share != -3 {
		t.Fatalf("local %+v", gotLocal)
	}
	pad, ok := savedGroup(groups.list, "pad")
	if !ok || pad.Folder != "eu" {
		t.Fatalf("pad %+v", pad)
	}
	if _, ok := savedGroup(groups.list, "bad"); ok {
		t.Fatal("bad group was saved")
	}
	if _, ok := savedGroup(groups.list, "long"); ok {
		t.Fatal("long group was saved")
	}
	for _, want := range []ktimport.Loss{
		{Kind: "skipped", Ref: "bad", Detail: "duplicate group id: bad"},
		{Kind: "conflict", Ref: "local", Detail: "id занят локальной записью"},
		{Kind: "skipped", Ref: "bad", Detail: "share must not be negative"},
		{Kind: "skipped", Ref: "long", Detail: "folder is longer than 64 characters: long"},
		{Kind: "skipped", Ref: "", Detail: "group id required"},
	} {
		if !lossHas(losses, want) {
			t.Fatalf("missing %+v in %+v", want, losses)
		}
	}
}

func TestKeitaroImportListsAndPurge(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFilterFake(t)
	dir := t.TempDir()
	lists := NewFileLists(dir)
	if err := lists.Write("ip_test.dat", "9.9.9.9\n"); err != nil {
		t.Fatal(err)
	}
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local", Status: true}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, nil, &keitaroFiles{dir: dir})
	s.cfg.Lists = lists
	raw := postKeitaroImport(t, admin, map[string]any{
		"networks": true, "offers": true, "campaigns": true,
	})
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || job.Error != "" || strings.Contains(job.Error, key) {
		t.Fatalf("job %+v", job)
	}
	if job.Counts["lists"] != 1 || job.Counts["groups"] != 2 || job.Counts["offers"] != 3 || job.Counts["networks"] != 1 {
		t.Fatalf("counts %+v", job.Counts)
	}
	promo, ok := savedGroup(groups.list, "promo")
	if !ok || len(promo.Streams) != 1 {
		t.Fatalf("promo %+v", promo)
	}
	st := promo.Streams[0]
	file := st.Rules.IPList.File
	body, err := lists.Read(file)
	if err != nil || body != ipListBody || file != ktimport.IPListName(ipListBody) {
		t.Fatalf("list %s %q err %v", file, body, err)
	}
	if st.Rules.IPList.Flag != 2 || len(st.Rules.Org.Values) != 2 || st.Rules.Org.Values[0] != "Vodacom" || st.Rules.Org.Values[1] != "MTN" {
		t.Fatalf("rules %+v", st.Rules)
	}
	same, ok := savedGroup(groups.list, "same")
	if !ok || len(same.Streams) != 1 || len(same.Streams[0].Out.Offers) != 1 || same.Streams[0].Out.Offers[0].Share != 100 {
		t.Fatalf("same %+v", same)
	}
	if _, ok := savedGroup(groups.list, "bad"); ok {
		t.Fatal("bad group was saved")
	}
	if !jobLoss(job, "skipped", "bad", "share must not be negative") {
		t.Fatalf("losses %+v", job.Losses)
	}
	if _, ok := savedGroup(groups.list, "local"); !ok {
		t.Fatal("local group dropped")
	}
	nets := readNets(t, filepath.Join(dir, "networks.json"))
	if len(nets) != 1 || nets[0].OfferQuery != "sub1={subid}" || nets[0].OfferParam != "" {
		t.Fatalf("networks %+v", nets)
	}

	raw, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK || strings.Contains(string(raw), key) {
		t.Fatalf("purge %d %s", code, raw)
	}
	var purged struct {
		Removed struct {
			Groups int `json:"groups"`
			Lists  int `json:"lists"`
		} `json:"removed"`
	}
	if err := json.Unmarshal(raw, &purged); err != nil {
		t.Fatal(err)
	}
	if purged.Removed.Lists != 1 || purged.Removed.Groups != 2 {
		t.Fatalf("removed %+v", purged.Removed)
	}
	if _, err := lists.Read(file); err == nil {
		t.Fatal("imported list remains")
	}
	kept, err := lists.Read("ip_test.dat")
	if err != nil || kept != "9.9.9.9\n" {
		t.Fatalf("ip_test %q err %v", kept, err)
	}
	names, err := lists.Names()
	if err != nil || len(names) != 1 || names[0] != "ip_test.dat" {
		t.Fatalf("names %v err %v", names, err)
	}
	if _, ok := savedGroup(groups.list, "local"); !ok || len(groups.list) != 1 {
		t.Fatalf("after purge %+v", groups.list)
	}
}

func TestKeitaroListsStoreMissing(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFilterFake(t)
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local"}}}
	_, admin := newKeitaroServers(t, fake.URL, key, groups, nil, nil)
	postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || job.Error != "" || job.Counts["lists"] != 0 || job.Counts["groups"] != 2 {
		t.Fatalf("job %+v", job)
	}
	promo, ok := savedGroup(groups.list, "promo")
	if !ok || promo.Streams[0].Rules.IPList.File != "" || promo.Streams[0].Rules.IPList.Flag != 0 {
		t.Fatalf("promo %+v", promo)
	}
	if len(promo.Streams[0].Rules.Org.Values) != 2 {
		t.Fatalf("org %+v", promo.Streams[0].Rules.Org)
	}
	file := ktimport.IPListName(ipListBody)
	if !jobLoss(job, "skipped", "lists", "lists store unavailable") || !jobLoss(job, "skipped", "main", "ip list "+file+" missing") {
		t.Fatalf("losses %+v", job.Losses)
	}
	if _, ok := savedGroup(groups.list, "bad"); ok {
		t.Fatal("bad group was saved")
	}
	if _, ok := savedGroup(groups.list, "same"); !ok {
		t.Fatal("same group was dropped")
	}
}

func TestKeitaroListWriteError(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFilterFake(t)
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local"}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, nil, nil)
	s.cfg.Lists = failLists{}
	postKeitaroImport(t, admin, map[string]any{"campaigns": true})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "failed" || job.Error != "save error" || strings.Contains(job.Error, key) {
		t.Fatalf("job %+v", job)
	}
	if _, ok := job.Counts["groups"]; ok || len(groups.list) != 1 || groups.list[0].ID != "local" {
		t.Fatalf("groups %+v counts %+v", groups.list, job.Counts)
	}
}

func TestFileListsDelete(t *testing.T) {
	dir := t.TempDir()
	fl := NewFileLists(dir)
	if err := fl.Delete("kt-ip-000000000000.dat"); err != nil {
		t.Fatal(err)
	}
	if err := fl.Write("kt-ip-000000000000.dat", "1.1.1.1\n"); err != nil {
		t.Fatal(err)
	}
	if err := fl.Write("ip_test.dat", "9.9.9.9\n"); err != nil {
		t.Fatal(err)
	}
	if err := fl.Delete("kt-ip-000000000000.dat"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kt-ip-000000000000.dat")); !os.IsNotExist(err) {
		t.Fatal("deleted file remains")
	}
	if _, err := fl.Read("ip_test.dat"); err != nil {
		t.Fatal(err)
	}
	if err := fl.Delete("../ip_test.dat"); err == nil {
		t.Fatal("bad name accepted")
	}
}

type failLists struct{}

func (failLists) Names() ([]string, error)    { return nil, nil }
func (failLists) Read(string) (string, error) { return "", os.ErrNotExist }
func (failLists) Write(string, string) error  { return os.ErrPermission }
func (failLists) Delete(string) error         { return nil }

func savedGroup(groups []config.Group, id string) (config.Group, bool) {
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return config.Group{}, false
}

func lossHas(losses []ktimport.Loss, want ktimport.Loss) bool {
	for _, l := range losses {
		if l.Kind == want.Kind && l.Ref == want.Ref && l.Detail == want.Detail {
			return true
		}
	}
	return false
}

func jobLoss(job keitaroJobBody, kind, ref, detail string) bool {
	for _, l := range job.Losses {
		if l.Kind == kind && l.Ref == ref && l.Detail == detail {
			return true
		}
	}
	return false
}

func keitaroFilterFake(t *testing.T) *httptest.Server {
	t.Helper()
	bodies := map[string]string{
		"/admin_api/v1/campaigns": `[
			{"id":7,"alias":"promo","name":"Spring","state":"active"},
			{"id":8,"alias":"same","name":"Same","state":"active"},
			{"id":9,"alias":"bad","name":"Bad","state":"active"}
		]`,
		"/admin_api/v1/campaigns/7/streams": `[{
			"id":3,"type":"regular","name":"main","state":"active","action_type":"http",
			"action_payload":"https://offer.example/a",
			"filters":[
				{"name":"ip","mode":"accept","payload":["10.0.0.0/8","1.2.3.4"]},
				{"name":"isp","mode":"accept","payload":["Vodacom","MTN"]}
			],
			"offers":[{"offer_id":4,"share":100,"state":"active"}]
		}]`,
		"/admin_api/v1/campaigns/8/streams": `[{
			"id":4,"type":"regular","name":"split","state":"active","action_type":"http",
			"offers":[
				{"offer_id":5,"share":40,"state":"active"},
				{"offer_id":6,"share":60,"state":"active"}
			]
		}]`,
		"/admin_api/v1/campaigns/9/streams": `[{
			"id":5,"type":"regular","name":"neg","state":"active","action_type":"http",
			"offers":[{"offer_id":4,"share":-1,"state":"active"}]
		}]`,
		"/admin_api/v1/offers": `[
			{"id":4,"name":"Offer","state":"active","action_type":"http","action_payload":"https://offer.example/a","affiliate_network_id":9},
			{"id":5,"name":"A","state":"active","action_type":"http","action_payload":"https://same.example/x","affiliate_network_id":9},
			{"id":6,"name":"B","state":"active","action_type":"http","action_payload":"https://same.example/x","affiliate_network_id":9}
		]`,
		"/admin_api/v1/affiliate_networks": `[{"id":9,"name":"Net","state":"active","offer_param":"sub1={subid}"}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := bodies[r.URL.Path]
		if body == "" {
			body = "[]"
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// globalImportGroups builds three campaigns, two of them sharing one postback.
// The OutboundPostback is the shared template, including its original status case.
// The third campaign carries a different URL. There is no error.
func globalImportGroups() ([]ktimport.Group, config.OutboundPostback) {
	shared := config.OutboundPostback{
		URL: "https://pb.example/hook?cid={subid}", Method: "post", Statuses: []string{"Sale", "Lead"},
	}
	pb := ktimport.Postback{URL: shared.URL, Method: shared.Method, Statuses: []string{"Sale", "Lead"}}
	other := ktimport.Postback{URL: "https://other.example/pb", Method: "GET", Statuses: []string{"rejected"}}
	groups := []ktimport.Group{
		{ID: "a", Name: "A", Postbacks: []ktimport.Postback{pb}},
		{ID: "b", Name: "B", Postbacks: []ktimport.Postback{pb}},
		{ID: "c", Name: "C", Postbacks: []ktimport.Postback{other}},
	}
	return groups, shared
}

// importS2SServer builds an admin server whose groups and optional s2s catalog are files in a temp directory.
// t reports setup failures. withS2S adds s2s.json when true.
// It returns the server and the directory. It does not return an error.
func importS2SServer(t *testing.T, withS2S bool) (*Server, string) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups:       NewFileGroups(filepath.Join(dir, "groups.json")),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
	}
	if withS2S {
		cfg.S2S = NewJSONStore[config.OutboundPostback](filepath.Join(dir, "s2s.json"))
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// readSavedGroups reads groups.json written by importGroups.
// t reports a missing or invalid file. path is the groups file.
// It returns the decoded campaigns. It does not return an error.
func readSavedGroups(t *testing.T, path string) []config.Group {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var groups []config.Group
	if err := json.Unmarshal(b, &groups); err != nil {
		t.Fatal(err)
	}
	return groups
}

func TestImportGroupsExtractsGlobalS2S(t *testing.T) {
	incoming, shared := globalImportGroups()
	id := config.PostbackID(shared.Signature())
	s, dir := importS2SServer(t, true)
	n, _, err := s.importGroups(context.Background(), incoming, ktimport.Manifest{}, "")
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	catRaw, err := os.ReadFile(filepath.Join(dir, "s2s.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat []config.OutboundPostback
	if err := json.Unmarshal(catRaw, &cat); err != nil {
		t.Fatal(err)
	}
	if len(cat) != 1 || cat[0].ID != id {
		t.Fatalf("catalog %+v, want %s", cat, id)
	}
	groups := readSavedGroups(t, filepath.Join(dir, "groups.json"))
	for _, gid := range []string{"a", "b"} {
		g, ok := savedGroup(groups, gid)
		if !ok || len(g.Postbacks) != 0 || len(g.PostbackIDs) != 1 || g.PostbackIDs[0] != id {
			t.Fatalf("%s %+v", gid, g)
		}
	}
	c, ok := savedGroup(groups, "c")
	if !ok || len(c.PostbackIDs) != 0 || len(c.Postbacks) != 1 || c.Postbacks[0].URL != "https://other.example/pb" {
		t.Fatalf("c %+v", c)
	}

	incoming, _ = globalImportGroups()
	s, dir = importS2SServer(t, false)
	n, _, err = s.importGroups(context.Background(), incoming, ktimport.Manifest{}, "")
	if err != nil || n != 3 {
		t.Fatalf("nil store n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s2s.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("s2s.json err=%v", err)
	}
	groups = readSavedGroups(t, filepath.Join(dir, "groups.json"))
	for _, gid := range []string{"a", "b", "c"} {
		g, ok := savedGroup(groups, gid)
		if !ok || len(g.Postbacks) != 1 || len(g.PostbackIDs) != 0 {
			t.Fatalf("inline %s %+v", gid, g)
		}
	}
}
