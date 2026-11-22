package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/geo"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

// geoInfoStub is a GeoInfo with fixed databases and no resolution.
type geoInfoStub struct {
	geo.Nop
	dbs []geo.Info
}

func (g geoInfoStub) Databases() []geo.Info { return g.dbs }

// toolsUser returns a regular user that opens the Tools section only.
func toolsUser(t *testing.T, id string) security.User {
	t.Helper()
	return security.User{
		ID: id, Login: id, Hash: mustHash(t, "p@ss"), Enabled: true, Type: security.UserRegular,
		Access: security.Access{Sections: []string{string(secTools)}},
	}
}

// geoToolsServer starts a panel with a cron config file, the three geo targets
// and user u1 with the Tools section. It returns the base URL and the cron file.
func geoToolsServer(t *testing.T) (string, string) {
	t.Helper()
	cronFile := filepath.Join(t.TempDir(), "cron.json")
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", func(s *Server) {
		s.cfg.CronFile = cronFile
		s.cfg.GeoCityPath, s.cfg.GeoASNPath, s.cfg.GeoDBIPPath = "/geo/city.mmdb", "/geo/asn.mmdb", "/geo/dbip.mmdb"
		s.cfg.Geo = geoInfoStub{dbs: []geo.Info{
			{Kind: geo.KindCity, Path: "/geo/city.mmdb"},
			{Kind: geo.KindDBIPASN, Path: "/geo/dbip.mmdb", Loaded: true},
		}}
		_ = s.users.Add(toolsUser(t, "u1"))
	})
	return hs.URL, cronFile
}

func TestToolsGeoStateOffersDBIPCity(t *testing.T) {
	base, _ := geoToolsServer(t)
	admin, _ := boardLogin(t, base, "admin", "p@ss")
	resp, raw := boardGet(t, admin, base+"/api/tools/geo", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("geo state %d %s", resp.StatusCode, raw)
	}
	var st geoToolsState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !st.CanDownload || !st.Cron || len(st.Databases) != 2 {
		t.Fatalf("state %+v", st)
	}
	city := st.Databases[0]
	if city.Kind != geo.KindCity || city.Source != cron.GeoSourceDBIP || !city.Ready || city.NeedsKey {
		t.Fatalf("city row %+v, want DB-IP City Lite filled in automatically", city)
	}
	if dbip := st.Databases[1]; !dbip.Ready {
		t.Fatalf("dbip row %+v", dbip)
	}

	user, _ := boardLogin(t, base, "u1", "p@ss")
	_, raw = boardGet(t, user, base+"/api/tools/geo", "")
	st = geoToolsState{}
	if err := json.Unmarshal(raw, &st); err != nil || st.CanDownload {
		t.Fatalf("a user without settings may not download: %+v %v", st, err)
	}
}

// TestToolsGeoStateShowsCityGuard checks that the last check of the city database
// by the cron service reaches the panel, and that the field is absent before any.
func TestToolsGeoStateShowsCityGuard(t *testing.T) {
	base, cronFile := geoToolsServer(t)
	admin, _ := boardLogin(t, base, "admin", "p@ss")
	read := func() geoToolsState {
		resp, raw := boardGet(t, admin, base+"/api/tools/geo", "")
		var st geoToolsState
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &st) != nil {
			t.Fatalf("geo state %d %s", resp.StatusCode, raw)
		}
		return st
	}
	if st := read(); st.CityGuard != nil {
		t.Fatalf("no cron status yet, guard %+v", st.CityGuard)
	}
	next := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	status := cron.Status{Heartbeat: time.Now(), GeoCity: &cron.GeoCityState{Running: false, Error: "download.db-ip.com: HTTP 503", NextTry: next}}
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cron.StatusPath(cronFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	st := read()
	if st.CityGuard == nil || st.CityGuard.Error != "download.db-ip.com: HTTP 503" || !st.CityGuard.NextTry.Equal(next) || !st.Alive {
		t.Fatalf("guard %+v alive=%v", st.CityGuard, st.Alive)
	}
}

func TestToolsGeoDownload(t *testing.T) {
	base, cronFile := geoToolsServer(t)
	admin, csrf := boardLogin(t, base, "admin", "p@ss")
	post := func(body any) (int, string) {
		resp, raw := boardJSON(t, admin, http.MethodPost, base+"/api/tools/geo/download", "", csrf, body)
		return resp.StatusCode, string(raw)
	}

	if code, raw := post(map[string]string{"kind": "asn"}); code != http.StatusConflict || !strings.Contains(raw, geoReasonNeedsKey) {
		t.Fatalf("keyless MaxMind asn: %d %s", code, raw)
	}
	if code, raw := post(map[string]string{"kind": "city"}); code != http.StatusAccepted {
		t.Fatalf("a keyless city source is downloaded from DB-IP, got %d %s", code, raw)
	}
	if code, _ := post(map[string]string{"kind": "zip"}); code != http.StatusBadRequest {
		t.Fatalf("bad kind: %d", code)
	}
	if code, _ := post(map[string]string{"kind": "asn", "source": "dbip"}); code != http.StatusBadRequest {
		t.Fatalf("dbip for asn: %d", code)
	}
	if code, raw := post(map[string]string{"kind": "city", "source": "dbip"}); code != http.StatusAccepted {
		t.Fatalf("dbip city: %d %s", code, raw)
	}
	cfg, err := cron.Load(cronFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range cfg.GeoSlots() {
		if slot.Kind == cron.KindCity && (slot.Source != cron.GeoSourceDBIP || !slot.Ready) {
			t.Fatalf("city slot after the switch %+v", slot)
		}
	}
	trigger, err := os.ReadFile(cron.TriggerPath(cronFile))
	if jobs := strings.Fields(string(trigger)); err != nil || len(jobs) == 0 || jobs[len(jobs)-1] != cron.JobGeoDB {
		t.Fatalf("trigger %q %v", trigger, err)
	}
	if code, _ := post(map[string]string{"kind": "dbip_asn"}); code != http.StatusAccepted {
		t.Fatalf("dbip asn: %d", code)
	}

	user, ucsrf := boardLogin(t, base, "u1", "p@ss")
	resp, _ := boardJSON(t, user, http.MethodPost, base+"/api/tools/geo/download", "", ucsrf, map[string]string{"kind": "dbip_asn"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a user without settings downloads: %d", resp.StatusCode)
	}
}

func TestToolsGeoDownloadWithoutCron(t *testing.T) {
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", nil)
	admin, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	resp, _ := boardJSON(t, admin, http.MethodPost, hs.URL+"/api/tools/geo/download", "", csrf, map[string]string{"kind": "city"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no cron: %d", resp.StatusCode)
	}
}

// historyPage is the answer of GET /api/tools/lookups.
type historyPage struct {
	Rows  []lookupEntry `json:"rows"`
	Total int           `json:"total"`
	Owner bool          `json:"owner"`
	Users []lookupUser  `json:"users"`
}

func readHistory(t *testing.T, c *http.Client, url string) (int, historyPage) {
	t.Helper()
	resp, raw := boardGet(t, c, url, "")
	var p historyPage
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, p
}

func TestLookupHistoryRights(t *testing.T) {
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", func(s *Server) {
		_ = s.users.Add(toolsUser(t, "u1"))
		_ = s.users.Add(toolsUser(t, "u2"))
	})
	base := hs.URL
	u1, u1csrf := boardLogin(t, base, "u1", "p@ss")
	u2, _ := boardLogin(t, base, "u2", "p@ss")
	admin, csrf := boardLogin(t, base, "admin", "p@ss")
	for _, c := range []struct {
		cl *http.Client
		ip string
	}{{u1, "192.0.2.1"}, {u1, "192.0.2.2"}, {u2, "198.51.100.7"}, {admin, "203.0.113.5"}} {
		if resp, _ := boardGet(t, c.cl, base+"/api/lookup?ip="+c.ip, ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("lookup %s: %d", c.ip, resp.StatusCode)
		}
	}

	code, p := readHistory(t, u1, base+"/api/tools/lookups")
	if code != http.StatusOK || p.Owner || p.Total != 2 || p.Rows[0].IP != "192.0.2.2" || p.Users != nil {
		t.Fatalf("own history %d %+v", code, p)
	}
	if code, _ := readHistory(t, u1, base+"/api/tools/lookups?user=u2"); code != http.StatusForbidden {
		t.Fatalf("another user's history: %d", code)
	}
	if code, p := readHistory(t, u1, base+"/api/tools/lookups?user=u1&limit=1&offset=1"); code != http.StatusOK || p.Total != 2 || len(p.Rows) != 1 || p.Rows[0].IP != "192.0.2.1" {
		t.Fatalf("second page %d %+v", code, p)
	}
	if resp, _ := boardJSON(t, u1, http.MethodDelete, base+"/api/tools/lookups", "", u1csrf, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("clear by a user: %d", resp.StatusCode)
	}

	code, p = readHistory(t, admin, base+"/api/tools/lookups")
	if code != http.StatusOK || !p.Owner || p.Total != 4 || len(p.Users) != 3 || p.Rows[0].UID != lookupOwnerUID {
		t.Fatalf("owner history %d %+v", code, p)
	}
	if _, p := readHistory(t, admin, base+"/api/tools/lookups?user=u2"); p.Total != 1 || p.Rows[0].Login != "u2" {
		t.Fatalf("owner filter %+v", p)
	}
	resp, raw := boardJSON(t, admin, http.MethodDelete, base+"/api/tools/lookups?user=u1", "", csrf, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"deleted":2`) {
		t.Fatalf("owner clear %d %s", resp.StatusCode, raw)
	}
	if _, p := readHistory(t, admin, base+"/api/tools/lookups"); p.Total != 2 {
		t.Fatalf("after clear %+v", p)
	}
}

func TestLookupHistoryPruneAndFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	h := newLookupHistory(dir)
	h.now = func() time.Time { return now }
	h.rows = []lookupEntry{{TS: now.Add(-lookupHistoryKeep - time.Hour), UID: "old", IP: "192.0.2.9"}}
	for range lookupHistoryMax {
		h.rows = append(h.rows, lookupEntry{TS: now.Add(-time.Hour), UID: "u1", IP: "192.0.2.3"})
	}
	h.loaded = true
	for range 5 {
		if err := h.add(lookupEntry{UID: "u1", IP: "192.0.2.1"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, total := h.page("", 0, 10)
	if total != lookupHistoryMax || len(rows) != 10 {
		t.Fatalf("total %d rows %d", total, len(rows))
	}
	if _, n := h.page("old", 0, 10); n != 0 {
		t.Fatalf("an expired check is kept: %d", n)
	}
	again := newLookupHistory(dir)
	again.now = h.now
	if _, n := again.page("u1", 0, 1); n != lookupHistoryMax {
		t.Fatalf("history file holds %d", n)
	}
	if st, err := os.Stat(filepath.Join(dir, lookupHistoryFileName)); err != nil || st.Size() == 0 {
		t.Fatalf("history file %v", err)
	}
}

func TestPostbackLogFilters(t *testing.T) {
	stats := &limitStats{}
	hs, _ := boardTestServer(t, &boardMemGroups{}, "", func(s *Server) { s.cfg.Stats = stats })
	admin, _ := boardLogin(t, hs.URL, "admin", "p@ss")
	url := hs.URL + "/api/postback-log?outcome=duplicate&network=net1&ip=203.0.113.20&cid=c1&direction=in&limit=50&offset=100"
	if resp, raw := boardGet(t, admin, url, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("postback log %d %s", resp.StatusCode, raw)
	}
	want := store.PostbackLogQuery{Outcome: "duplicate", Network: "net1", IP: "203.0.113.20", CID: "c1", Direction: "in", Limit: 50, Offset: 100}
	got := stats.query
	got.From, got.To = time.Time{}, time.Time{}
	if got.Scope != nil || got.Outcome != want.Outcome || got.Network != want.Network || got.IP != want.IP ||
		got.CID != want.CID || got.Direction != want.Direction || got.Limit != want.Limit || got.Offset != want.Offset {
		t.Fatalf("query %+v, want %+v", got, want)
	}
	for _, bad := range []string{"outcome=nope", "direction=sideways"} {
		if resp, _ := boardGet(t, admin, hs.URL+"/api/postback-log?"+bad, ""); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, resp.StatusCode)
		}
	}
}
