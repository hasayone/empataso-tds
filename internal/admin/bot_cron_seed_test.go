package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/cron"
	"github.com/egerkuzma/emtds/internal/security"
)

// botServer is an admin whose bot profile and cron config files are in a temp
// directory. It returns the server address, a logged-in client with its CSRF
// token, and both file paths (neither exists yet).
func botServer(t *testing.T) (base string, c *http.Client, csrf, botFile, cronFile string) {
	t.Helper()
	return botServerWith(t, nil)
}

// botServerWith is botServer with a hook that may change the admin Config before the server starts.
func botServerWith(t *testing.T, edit func(*Config)) (base string, c *http.Client, csrf, botFile, cronFile string) {
	t.Helper()
	dir := t.TempDir()
	botFile, cronFile = filepath.Join(dir, "bot.json"), filepath.Join(dir, "cron.json")
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: allowAll{}, SessionTTL: time.Hour,
		BotPath: botFile, CronFile: cronFile,
	}
	if edit != nil {
		edit(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c = &http.Client{Jar: jar}
	if _, csrf = login(t, c, srv.URL, "admin", "p@ss"); csrf == "" {
		t.Fatal("login failed")
	}
	return srv.URL, c, csrf, botFile, cronFile
}

// botView is the answer of /api/bot-protection: the profile and the two panel flags.
type botView struct {
	config.Bots
	ProxyDefault bool `json:"proxy_default"`
	PrivateRelay bool `json:"private_relay"`
}

// getBot reads GET /api/bot-protection and returns the decoded answer and the raw body.
func getBot(t *testing.T, c *http.Client, base string) (botView, string) {
	t.Helper()
	resp, err := c.Get(base + "/api/bot-protection")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/bot-protection → %d %s %v", resp.StatusCode, b, err)
	}
	var v botView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v, string(b)
}

func TestBotProfileDefaultHasProxyOn(t *testing.T) {
	base, c, _, _, _ := botServer(t)
	v, raw := getBot(t, c, base)
	if !v.Proxy || !v.ProxyDefault || v.PrivateRelay || !strings.Contains(raw, `"ch_proxy": true`) {
		t.Fatalf("default profile = %+v, body %s", v, raw)
	}
}

func TestBotProfileFileWithoutProxyKeyReadsAsDefault(t *testing.T) {
	base, c, _, botFile, _ := botServer(t)
	if err := os.WriteFile(botFile, []byte("{\n  \"ch_ua\": true\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, _ := getBot(t, c, base)
	if !v.Proxy || !v.CheckUA || !v.ProxyDefault {
		t.Fatalf("profile = %+v", v)
	}
}

func TestBotProfileGetKeepsTheShapeAndAddsTwoFlags(t *testing.T) {
	base, c, _, botFile, _ := botServer(t)
	if err := os.WriteFile(botFile, []byte(`{"ch_proxy":false,"ch_ua":true,"save_ip":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, raw := getBot(t, c, base)
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"ch_proxy": false, "ch_ua": true, "save_ip": true, "proxy_default": false, "private_relay": false}
	if len(doc) != len(want) {
		t.Fatalf("keys = %v, want %v", doc, want)
	}
	for k, v := range want {
		if doc[k] != v {
			t.Errorf("%s = %v, want %v", k, doc[k], v)
		}
	}
}

func TestBotProfilePrivateRelayFlagFollowsTheList(t *testing.T) {
	dir := t.TempDir()
	base, c, _, _, _ := botServerWith(t, func(cfg *Config) { cfg.DataDir = dir })
	list := filepath.Join(dir, "ip_private_relay.dat")
	if v, _ := getBot(t, c, base); v.PrivateRelay {
		t.Fatal("no list yet")
	}
	if err := os.WriteFile(list, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _ := getBot(t, c, base); v.PrivateRelay {
		t.Fatal("an empty list is not a list")
	}
	if err := os.WriteFile(list, []byte("172.224.226.0/27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _ := getBot(t, c, base); !v.PrivateRelay {
		t.Fatal("a filled list must set private_relay")
	}
}

func TestBotProfilePutKeepsProxyOff(t *testing.T) {
	base, c, csrf, botFile, _ := botServer(t)
	var echoed botView
	if code := call(t, c, "PUT", base+"/api/bot-protection", csrf, `{"ch_proxy":false,"ch_ua":true}`, &echoed); code != 200 {
		t.Fatalf("PUT → %d", code)
	}
	if echoed.Proxy || !echoed.CheckUA || echoed.ProxyDefault {
		t.Fatalf("PUT answer = %+v", echoed)
	}
	stored, err := os.ReadFile(botFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"ch_proxy": false`) || !strings.HasSuffix(string(stored), "\n") {
		t.Fatalf("stored file = %q", stored)
	}
	v, raw := getBot(t, c, base)
	if v.Proxy || !v.CheckUA || v.ProxyDefault || !strings.Contains(raw, `"ch_proxy": false`) {
		t.Fatalf("GET after PUT = %+v, body %s", v, raw)
	}
}

// A save that does not touch the proxy switch must not write it: the engine
// treats a missing key as "on once the Private Relay list is loaded".
func TestBotProfilePutWithoutProxyKeyWritesNoKey(t *testing.T) {
	base, c, csrf, botFile, _ := botServer(t)
	var echoed botView
	if code := call(t, c, "PUT", base+"/api/bot-protection", csrf, `{"ch_ua":true}`, &echoed); code != 200 {
		t.Fatalf("PUT → %d", code)
	}
	if stored, _ := os.ReadFile(botFile); strings.Contains(string(stored), "ch_proxy") {
		t.Fatalf("stored file = %q", stored)
	}
	if !echoed.Proxy || !echoed.ProxyDefault {
		t.Fatalf("answer = %+v, want the effective proxy on and proxy_default", echoed)
	}
}

func TestBotProfilePutKeepsWhatTheBodyDoesNotMention(t *testing.T) {
	base, c, csrf, botFile, _ := botServer(t)
	if err := os.WriteFile(botFile, []byte(`{"ch_proxy":false,"save_ip":true,"ch_ua":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := call(t, c, "PUT", base+"/api/bot-protection", csrf, `{"ch_empty_ua":true}`, nil); code != 200 {
		t.Fatalf("PUT → %d", code)
	}
	stored, _ := os.ReadFile(botFile)
	for _, want := range []string{`"save_ip": true`, `"ch_ua": true`, `"ch_empty_ua": true`, `"ch_proxy": false`} {
		if !strings.Contains(string(stored), want) {
			t.Errorf("%s lost: %s", want, stored)
		}
	}
}

func TestBotProfilePutNullProxyReturnsToDefault(t *testing.T) {
	base, c, csrf, botFile, _ := botServer(t)
	if err := os.WriteFile(botFile, []byte(`{"ch_proxy":false,"save_ip":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var echoed botView
	if code := call(t, c, "PUT", base+"/api/bot-protection", csrf, `{"ch_proxy":null}`, &echoed); code != 200 {
		t.Fatalf("PUT → %d", code)
	}
	stored, _ := os.ReadFile(botFile)
	if strings.Contains(string(stored), "ch_proxy") || !strings.Contains(string(stored), `"save_ip": true`) {
		t.Fatalf("stored file = %q", stored)
	}
	if !echoed.Proxy || !echoed.ProxyDefault {
		t.Fatalf("answer = %+v", echoed)
	}
}

func TestBotProfilePutDropsStreamAndUnknownKeysAndRejectsBadBodies(t *testing.T) {
	base, c, csrf, botFile, _ := botServer(t)
	body := `{"ch_ua":true,"redirect":"https://x.example","out":"o","header":"h","proxy_default":true,"private_relay":true}`
	if code := call(t, c, "PUT", base+"/api/bot-protection", csrf, body, nil); code != 200 {
		t.Fatalf("PUT → %d", code)
	}
	stored, _ := os.ReadFile(botFile)
	for _, key := range []string{"redirect", `"out"`, "header", "proxy_default", "private_relay"} {
		if strings.Contains(string(stored), key) {
			t.Errorf("%s was stored: %s", key, stored)
		}
	}
	before, _ := os.ReadFile(botFile)
	for _, bad := range []string{`{`, `[]`, `"x"`, `null`, ``, `{"ch_ua":"yes"}`} {
		if code := call(t, c, "PUT", base+"/api/bot-protection", csrf, bad, nil); code != http.StatusBadRequest {
			t.Errorf("PUT %q → %d, want 400", bad, code)
		}
	}
	if after, _ := os.ReadFile(botFile); string(after) != string(before) {
		t.Error("a refused PUT changed the file")
	}
}
func TestCronGetSeedsAnOldFileAndSavePersistsARemoval(t *testing.T) {
	base, c, csrf, _, cronFile := botServer(t)
	old := `{"ip_lists":{"enabled":false,"every_minutes":1440,"sources":[{"url":"https://a.example/x.dat","target":"ip_x","mode":"replace"}]},` +
		`"geo_db":{"enabled":false,"every_minutes":4320,"sources":[{"kind":"city","url":"https://c.example/c.mmdb"}]}}`
	if err := os.WriteFile(cronFile, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	st, _ := getCron(t, c, base)
	var relay, dbip int
	for _, s := range st.Config.IPLists.Sources {
		if s.Target == "ip_private_relay" {
			relay++
		}
	}
	for _, s := range st.Config.GeoDB.Sources {
		if s.Kind == "dbip_asn" {
			dbip++
		}
	}
	if relay != 1 || dbip != 1 || len(st.Config.Seeded) != 2 {
		t.Fatalf("panel config: relay=%d dbip=%d seeded=%v", relay, dbip, st.Config.Seeded)
	}
	if got, _ := os.ReadFile(cronFile); string(got) != old {
		t.Fatal("GET must not rewrite the cron file")
	}

	// The client drops the private relay source and does not send "seeded" at all.
	cfg := st.Config
	cfg.Seeded = nil
	cfg.IPLists.Sources = cfg.IPLists.Sources[:1]
	if code := call(t, c, "PUT", base+"/api/cron", csrf, cfg, nil); code != 200 {
		t.Fatalf("save → %d", code)
	}
	raw, _ := os.ReadFile(cronFile)
	var onDisk struct {
		Seeded []string `json:"seeded"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil || len(onDisk.Seeded) != 2 {
		t.Fatalf("saved flags = %v (%v): %s", onDisk.Seeded, err, raw)
	}
	again, err := cron.Load(cronFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range again.IPLists.Sources {
		if s.Target == "ip_private_relay" {
			t.Fatal("the removed private relay source came back")
		}
	}
	if len(again.IPLists.Sources) != 1 || len(again.GeoDB.Sources) != 2 {
		t.Fatalf("sources after save: lists=%+v geo=%+v", again.IPLists.Sources, again.GeoDB.Sources)
	}
}

func TestCronSaveRejectsDBIPOnAnotherHost(t *testing.T) {
	base, c, csrf, _, cronFile := botServer(t)
	st, _ := getCron(t, c, base)
	cfg := st.Config
	cfg.GeoDB.Sources = []cron.GeoSource{{Kind: "dbip_asn", URL: "https://evil.example/dbip-asn-lite.mmdb.gz"}}
	var e struct{ Error string }
	if code := call(t, c, "PUT", base+"/api/cron", csrf, cfg, &e); code != http.StatusBadRequest || !strings.Contains(e.Error, "dbip_asn") {
		t.Fatalf("save → %d %q", code, e.Error)
	}
	if _, err := os.Stat(cronFile); err == nil {
		t.Fatal("a refused save wrote the file")
	}
}

func TestCronGeoPathsReportDBIP(t *testing.T) {
	dbip := filepath.Join(t.TempDir(), "dbip-asn.mmdb")
	panel := func(path string) (cronState, string) {
		base, c, _, _, _ := botServerWith(t, func(cfg *Config) { cfg.GeoDBIPPath = path })
		return getCron(t, c, base)
	}

	st, raw := panel("")
	if st.GeoPaths.DBIP || st.GeoPaths.DBIPFile {
		t.Fatalf("no path configured: %+v", st.GeoPaths)
	}
	if !strings.Contains(raw, `"dbip":false`) || !strings.Contains(raw, `"dbip_file":false`) {
		t.Fatalf("JSON names: %s", raw)
	}
	if st.DBIPURL != cron.DBIPASNURL || !strings.Contains(raw, `"dbip_url":"https://download.db-ip.com/free/dbip-asn-lite-{YYYY-MM}.mmdb.gz"`) {
		t.Fatalf("dbip_url = %q in %s", st.DBIPURL, raw)
	}
	if st, _ = panel(dbip); !st.GeoPaths.DBIP || st.GeoPaths.DBIPFile {
		t.Fatalf("path set, file missing: %+v", st.GeoPaths)
	}
	if err := os.WriteFile(dbip, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ = panel(dbip); st.GeoPaths.DBIPFile {
		t.Fatalf("an empty file is not a database: %+v", st.GeoPaths)
	}
	if err := os.WriteFile(dbip, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ = panel(dbip); !st.GeoPaths.DBIP || !st.GeoPaths.DBIPFile {
		t.Fatalf("path and file present: %+v", st.GeoPaths)
	}
}

func TestLookupProxyListsAndPrivateRelay(t *testing.T) {
	base, c, csrf, dir := toolsServer(t)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name+".dat"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ip_proxy", "203.0.113.0/24\n198.51.100.0/24\n")
	write("ip_private_relay", "198.51.100.0/24\n")
	lookup := func(ip string) visitorInfo {
		var out struct{ Visitor visitorInfo }
		if code := call(t, c, "GET", base+"/api/lookup?ip="+ip, csrf, nil, &out); code != 200 {
			t.Fatalf("lookup %s → %d", ip, code)
		}
		return out.Visitor
	}
	if !lookup("203.0.113.9").Proxy {
		t.Error("an address in ip_proxy must be a proxy")
	}
	if lookup("198.51.100.9").Proxy {
		t.Error("an address in ip_proxy and ip_private_relay must not be a proxy")
	}
	if lookup("192.0.2.9").Proxy {
		t.Error("an address in neither list must not be a proxy")
	}
}

func TestLookupProxyOrgPatternsComeFromTheDataDir(t *testing.T) {
	base, c, csrf, dir := toolsServer(t)
	// 1.128.0.1 belongs to Telstra in the ASN test database.
	var out struct{ Visitor visitorInfo }
	call(t, c, "GET", base+"/api/lookup?ip=1.128.0.1", csrf, nil, &out)
	if out.Visitor.Proxy {
		t.Fatal("Telstra is not a hosting network by default")
	}
	if err := os.WriteFile(filepath.Join(dir, "proxy_org.dat"), []byte("# own list\ntelstra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call(t, c, "GET", base+"/api/lookup?ip=1.128.0.1", csrf, nil, &out)
	if !out.Visitor.Proxy {
		t.Fatal("proxy_org.dat in the data directory must decide")
	}
}
