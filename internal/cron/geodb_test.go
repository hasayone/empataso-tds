package cron

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// asnProbeIP returns an address the ASN test database knows an organization for.
func asnProbeIP(t *testing.T) net.IP {
	t.Helper()
	db, err := maxminddb.FromBytes(testDB(t, "GeoLite2-ASN-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	nets := db.Networks(maxminddb.SkipAliasedNetworks)
	for nets.Next() {
		var rec struct {
			Organization string `maxminddb:"autonomous_system_organization"`
		}
		n, err := nets.Network(&rec)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Organization != "" {
			return n.IP
		}
	}
	t.Fatal("no network with an organization in the ASN fixture")
	return nil
}

// dbipFixture is the ASN test database with its type renamed to one a DB-IP
// file would carry. The new name has the same length as the old one, so every
// offset in the file stays valid.
func dbipFixture(t *testing.T) []byte {
	t.Helper()
	b := append([]byte(nil), testDB(t, "GeoLite2-ASN-Test.mmdb")...)
	meta := bytes.LastIndex(b, []byte("\xab\xcd\xefMaxMind.com"))
	if meta < 0 {
		t.Fatal("no metadata marker in the fixture")
	}
	i := bytes.Index(b[meta:], []byte("GeoLite2-ASN"))
	if i < 0 {
		t.Fatal("no database type in the fixture metadata")
	}
	copy(b[meta+i:], "DBIP-ASN-Lit")
	return b
}

const dbipTypeName = "DBIP-ASN-Lit"

// allowTestServers lets dbip_asn sources use the given test servers, and only them.
func (e *env) allowTestServers(servers ...*httptest.Server) {
	hosts := map[string]bool{}
	for _, s := range servers {
		u, _ := url.Parse(s.URL)
		hosts[u.Host] = true
	}
	e.r.dbipAllowed = func(u *url.URL) bool { return hosts[u.Host] }
}

// dbipServer answers GET requests for the paths in files with Last-Modified
// set, 304 for a matching If-Modified-Since and 404 for any other path.
type dbipServer struct {
	mu   sync.Mutex
	hits []string
}

const dbipLastModified = "Mon, 02 Nov 2026 00:00:00 GMT"

func (d *dbipServer) handler(files map[string][]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.hits = append(d.hits, r.URL.Path+"|"+r.Header.Get("If-Modified-Since"))
		d.mu.Unlock()
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-Modified-Since") == dbipLastModified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", dbipLastModified)
		_, _ = w.Write(body)
	}
}

func (d *dbipServer) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.hits...)
}

// dbipEnv is a runner whose DB-IP target is a temp file, clock is fixed at
// now and whose dbip_asn sources may use the env's test server.
func dbipEnv(t *testing.T, now time.Time) *env {
	t.Helper()
	e := newEnv(t)
	e.r.paths.DBIPASN = filepath.Join(e.dir, "dbip-asn.mmdb")
	e.r.now = func() time.Time { return now }
	e.allowTestServers(e.srv)
	return e
}

// geoSeen is what the runner remembers about the last dbip_asn download.
func geoSeen(e *env) (seen GeoSeen) {
	e.r.state(func(st *Status) { seen = st.GeoSeen["dbip_asn"] })
	return seen
}

func dbipSource(e *env) GeoSource {
	return GeoSource{Kind: "dbip_asn", URL: e.srv.URL + "/free/dbip-asn-lite-" + MonthPlaceholder + ".mmdb.gz"}
}

func runDBIP(e *env, src GeoSource) (string, error) {
	return e.r.runGeoDB(context.Background(), GeoDB{Sources: []GeoSource{src}})
}

func TestDBIPDownloadAndMonthChange(t *testing.T) {
	nov := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	e := dbipEnv(t, nov)
	fixture := dbipFixture(t)
	var d dbipServer
	e.mux.HandleFunc("/free/", d.handler(map[string][]byte{
		"/free/dbip-asn-lite-2026-11.mmdb.gz": gz(fixture),
		"/free/dbip-asn-lite-2026-12.mmdb.gz": gz(fixture),
	}))

	msg, err := runDBIP(e, dbipSource(e))
	if err != nil || msg != "dbip_asn: "+dbipTypeName+" updated" {
		t.Fatalf("first run: %q, %v", msg, err)
	}
	if got, _ := os.ReadFile(e.r.paths.DBIPASN); !bytes.Equal(got, fixture) {
		t.Fatal("installed file differs from the download")
	}
	if seen := geoSeen(e); seen.URL != e.srv.URL+"/free/dbip-asn-lite-2026-11.mmdb.gz" {
		t.Fatalf("the expanded URL must be remembered, got %q", seen.URL)
	}

	// Same month: the server is asked only for what changed.
	if msg, err = runDBIP(e, dbipSource(e)); err != nil || !strings.HasPrefix(msg, "dbip_asn: not modified") {
		t.Fatalf("second run: %q, %v", msg, err)
	}

	// New month: another URL, so no condition is sent and the file is fetched again.
	e.r.now = func() time.Time { return time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC) }
	if msg, err = runDBIP(e, dbipSource(e)); err != nil || msg != "dbip_asn: "+dbipTypeName+" is up to date" {
		t.Fatalf("december run: %q, %v", msg, err)
	}
	want := []string{
		"/free/dbip-asn-lite-2026-11.mmdb.gz|",
		"/free/dbip-asn-lite-2026-11.mmdb.gz|" + dbipLastModified,
		"/free/dbip-asn-lite-2026-12.mmdb.gz|",
	}
	if got := d.seen(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests = %q, want %q", got, want)
	}
	if seen := geoSeen(e); !strings.HasSuffix(seen.URL, "2026-12.mmdb.gz") {
		t.Fatalf("seen URL after the month change = %q", seen.URL)
	}
}

func TestDBIPFallsBackToPreviousMonth(t *testing.T) {
	e := dbipEnv(t, time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC))
	var d dbipServer
	e.mux.HandleFunc("/free/", d.handler(map[string][]byte{"/free/dbip-asn-lite-2026-12.mmdb.gz": gz(dbipFixture(t))}))

	if msg, err := runDBIP(e, dbipSource(e)); err != nil || msg != "dbip_asn: "+dbipTypeName+" updated" {
		t.Fatalf("%q, %v", msg, err)
	}
	if seen := geoSeen(e); !strings.HasSuffix(seen.URL, "dbip-asn-lite-2026-12.mmdb.gz") {
		t.Fatalf("the month that answered must be remembered, got %q", seen.URL)
	}
	// The previous month's URL is the one the condition belongs to.
	if _, err := runDBIP(e, dbipSource(e)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/free/dbip-asn-lite-2027-01.mmdb.gz|",
		"/free/dbip-asn-lite-2026-12.mmdb.gz|",
		"/free/dbip-asn-lite-2027-01.mmdb.gz|",
		"/free/dbip-asn-lite-2026-12.mmdb.gz|" + dbipLastModified,
	}
	if got := d.seen(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests = %q, want %q", got, want)
	}
}

func TestDBIPBothMonthsMissingIsAnError(t *testing.T) {
	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	var d dbipServer
	e.mux.HandleFunc("/free/", d.handler(nil))
	_, err := runDBIP(e, dbipSource(e))
	if err == nil || statusOf(err) != http.StatusNotFound {
		t.Fatalf("err = %v, want HTTP 404", err)
	}
	if len(d.seen()) != 2 {
		t.Fatalf("requests = %v, want the current and the previous month", d.seen())
	}
}

func TestGeoURLsMonthBoundaries(t *testing.T) {
	got := geoURLs("https://x.example/a-"+MonthPlaceholder+".gz", time.Date(2026, 1, 31, 23, 59, 0, 0, time.UTC))
	want := []string{"https://x.example/a-2026-01.gz", "https://x.example/a-2025-12.gz"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("geoURLs = %v, want %v", got, want)
	}
	// The clock's zone does not matter: the month is the UTC one.
	zone := time.FixedZone("late", -10*3600)
	if got := ExpandMonth("m-"+MonthPlaceholder, time.Date(2026, 3, 31, 20, 0, 0, 0, zone)); got != "m-2026-04" {
		t.Fatalf("ExpandMonth = %q, want the UTC month", got)
	}
	if got := geoURLs("https://x.example/plain.mmdb", time.Now()); len(got) != 1 || got[0] != "https://x.example/plain.mmdb" {
		t.Fatalf("a URL without the placeholder must stay alone: %v", got)
	}
}

func TestDBIPRedirectToAnotherHostIsRefused(t *testing.T) {
	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	var reached bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = w.Write(gz(dbipFixture(t)))
	}))
	defer other.Close()
	e.mux.HandleFunc("/free/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/evil.mmdb.gz", http.StatusFound)
	})
	if _, err := runDBIP(e, dbipSource(e)); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v, want the redirect refused", err)
	}
	if reached {
		t.Fatal("the foreign host was contacted")
	}
	if _, err := os.Stat(e.r.paths.DBIPASN); err == nil {
		t.Fatal("a database was installed")
	}
}

func TestDBIPRedirectWithinTheAllowedHostIsFollowed(t *testing.T) {
	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	fixture := dbipFixture(t)
	e.mux.HandleFunc("/free/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/moved.mmdb.gz", http.StatusFound)
	})
	e.mux.HandleFunc("/moved.mmdb.gz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(gz(fixture)) })
	if msg, err := runDBIP(e, dbipSource(e)); err != nil || !strings.HasSuffix(msg, "updated") {
		t.Fatalf("%q, %v", msg, err)
	}
}

func TestDBIPDefaultRunnerAcceptsOnlyTheDBIPHost(t *testing.T) {
	e := newEnv(t)
	e.r.paths.DBIPASN = filepath.Join(e.dir, "dbip-asn.mmdb")
	var hits int
	e.mux.HandleFunc("/free/", func(w http.ResponseWriter, r *http.Request) { hits++ })
	if _, err := runDBIP(e, dbipSource(e)); err == nil || hits != 0 {
		t.Fatalf("a foreign host must be refused before any request: err=%v hits=%d", err, hits)
	}
}

func TestDBIPSizeLimitsFailAndKeepTheOldFile(t *testing.T) {
	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	e.r.dbipLimit = 1 << 20
	old := []byte("the database in use")
	if err := os.WriteFile(e.r.paths.DBIPASN, old, 0o644); err != nil {
		t.Fatal(err)
	}
	bomb := gz(make([]byte, 2<<20)) // a few kilobytes on the wire, 2 MiB unpacked
	raw := make([]byte, 2<<20)
	var d dbipServer
	e.mux.HandleFunc("/free/", d.handler(map[string][]byte{
		"/free/dbip-asn-lite-2026-11.mmdb.gz": bomb,
	}))
	e.mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) })

	for name, u := range map[string]string{
		"unpacked size": e.srv.URL + "/free/dbip-asn-lite-" + MonthPlaceholder + ".mmdb.gz",
		"download size": e.srv.URL + "/raw/db.mmdb",
	} {
		_, err := runDBIP(e, GeoSource{Kind: "dbip_asn", URL: u})
		if err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("%s: err = %v, want a size error", name, err)
		}
		if got, _ := os.ReadFile(e.r.paths.DBIPASN); !bytes.Equal(got, old) {
			t.Fatalf("%s: the working file was replaced", name)
		}
	}
	left, _ := filepath.Glob(filepath.Join(e.dir, ".dbip-asn.mmdb.dl*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestUnpackMMDBLimitIsAnErrorForEveryFormat(t *testing.T) {
	data := make([]byte, 4096)
	for name, payload := range map[string][]byte{
		"raw":    data,
		"gzip":   gz(data),
		"tar.gz": tarGz(map[string][]byte{"db/x.mmdb": data}),
	} {
		var out bytes.Buffer
		if err := unpackMMDB(bytes.NewReader(payload), &out, 1024); err == nil {
			t.Errorf("%s: a database over the limit must fail", name)
		}
		out.Reset()
		if err := unpackMMDB(bytes.NewReader(payload), &out, 4096); err != nil || out.Len() != 4096 {
			t.Errorf("%s: a database exactly at the limit must pass: %v, %d bytes", name, err, out.Len())
		}
	}
}

func TestDBIPRejectsWhatIsNotADBIPDatabase(t *testing.T) {
	old := []byte("the database in use")
	cases := map[string][]byte{
		"a MaxMind ASN database": testDB(t, "GeoLite2-ASN-Test.mmdb"),
		"a city database":        testDB(t, "GeoLite2-City-Test.mmdb"),
		"an HTML page":           []byte("<html>blocked</html>"),
	}
	for name, payload := range cases {
		e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
		if err := os.WriteFile(e.r.paths.DBIPASN, old, 0o644); err != nil {
			t.Fatal(err)
		}
		var d dbipServer
		e.mux.HandleFunc("/free/", d.handler(map[string][]byte{"/free/dbip-asn-lite-2026-11.mmdb.gz": gz(payload)}))
		if _, err := runDBIP(e, dbipSource(e)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
		if got, _ := os.ReadFile(e.r.paths.DBIPASN); !bytes.Equal(got, old) {
			t.Errorf("%s replaced the working database", name)
		}
	}
}

func TestDBIPNeedsAnOrganizationForTheProbeAddress(t *testing.T) {
	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	var d dbipServer
	e.mux.HandleFunc("/free/", d.handler(map[string][]byte{"/free/dbip-asn-lite-2026-11.mmdb.gz": gz(dbipFixture(t))}))
	e.r.probeIP = net.ParseIP("192.0.2.1") // documentation address, absent from the fixture
	if _, err := runDBIP(e, dbipSource(e)); err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("err = %v, want the verification failure", err)
	}
	if _, err := os.Stat(e.r.paths.DBIPASN); err == nil {
		t.Fatal("a database that fails the check was installed")
	}
}

// The probe address is a DB-IP check only: a MaxMind ASN database is accepted
// on its type and structure, as before.
func TestGeoDBASNKindIgnoresTheProbeAddress(t *testing.T) {
	e := newEnv(t)
	u := e.serve("/asn", string(testDB(t, "GeoLite2-ASN-Test.mmdb")))
	e.r.probeIP = net.ParseIP("192.0.2.1")
	if msg, err := e.r.runGeoDB(context.Background(), GeoDB{Sources: []GeoSource{{Kind: KindASN, URL: u}}}); err != nil || !strings.HasSuffix(msg, "updated") {
		t.Fatalf("%q, %v", msg, err)
	}
}

func TestDBIPNeverSendsCredentials(t *testing.T) {
	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	fixture := gz(dbipFixture(t))
	var mu sync.Mutex
	var auth []string
	e.mux.HandleFunc("/free/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write(fixture)
	})
	src := dbipSource(e)
	src.User, src.Password = "12345", "license"
	if _, err := runDBIP(e, src); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auth) != 1 || auth[0] != "" {
		t.Fatalf("Authorization headers = %q, want one empty", auth)
	}
}

func TestOnlyDBIPLosesItsCredentials(t *testing.T) {
	for _, kind := range []string{KindCity, KindASN} {
		if u, p := (GeoSource{Kind: kind, User: "u", Password: "p"}).Credentials(); u != "u" || p != "p" {
			t.Errorf("%s must keep its credentials, got %q %q", kind, u, p)
		}
	}
	if u, p := (GeoSource{Kind: KindDBIPASN, User: "u", Password: "p"}).Credentials(); u != "" || p != "" {
		t.Errorf("dbip_asn must send nothing, got %q %q", u, p)
	}
}
func TestSourceURLsAreTrimmed(t *testing.T) {
	c := Default()
	c.GeoDB.Sources = []GeoSource{{Kind: KindDBIPASN, URL: "  " + seedGeoSource.URL + "\n"}}
	c.IPLists.Sources = []IPSource{{URL: " https://a.example/x.dat ", Target: "ip_x"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("padded URLs must validate: %v", err)
	}

	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	var d dbipServer
	e.mux.HandleFunc("/free/", d.handler(map[string][]byte{"/free/dbip-asn-lite-2026-11.mmdb.gz": gz(dbipFixture(t))}))
	src := dbipSource(e)
	src.URL = " " + src.URL + " "
	if msg, err := runDBIP(e, src); err != nil || !strings.HasSuffix(msg, "updated") {
		t.Fatalf("padded geo URL: %q, %v", msg, err)
	}
	if seen := geoSeen(e); strings.ContainsAny(seen.URL, " \n") {
		t.Fatalf("the remembered URL keeps the padding: %q", seen.URL)
	}

	list := e.serve("/plain.dat", "1.2.3.4\n")
	if _, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{{URL: "\t" + list + " ", Target: "ip_x"}}}); err != nil {
		t.Fatalf("padded list URL: %v", err)
	}
}

func TestDBIPNeedsATarget(t *testing.T) {
	e := dbipEnv(t, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	e.r.paths.DBIPASN = ""
	_, err := runDBIP(e, dbipSource(e))
	if err == nil || !strings.Contains(err.Error(), "EMTDS_DBIP_ASN_DB") {
		t.Fatalf("err = %v, want it to name the variable", err)
	}
}

func TestMaxMindWithoutALicenseIsSkippedNotFailed(t *testing.T) {
	e := newEnv(t)
	e.r.paths.CityDB, e.r.paths.ASNDB = "", ""
	cfg := Default().GeoDB
	cfg.Sources = cfg.Sources[:2] // the two MaxMind sources
	msg, err := e.r.runGeoDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("err = %v, a missing key is not an error", err)
	}
	for _, want := range []string{"city: skipped, no MaxMind license key", "asn: skipped, no MaxMind license key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

func TestValidateDBIPSource(t *testing.T) {
	ok := func(raw string) Config {
		c := Default()
		c.GeoDB.Sources = []GeoSource{{Kind: "dbip_asn", URL: raw}}
		return c
	}
	if err := ok("https://download.db-ip.com/free/dbip-asn-lite-" + MonthPlaceholder + ".mmdb.gz").Validate(); err != nil {
		t.Fatalf("the default source must validate: %v", err)
	}
	for _, bad := range []string{
		"http://download.db-ip.com/free/dbip-asn-lite-" + MonthPlaceholder + ".mmdb.gz",
		"https://evil.example/free/dbip-asn-lite-" + MonthPlaceholder + ".mmdb.gz",
		"https://download.db-ip.com.evil.example/a.mmdb.gz",
		"https://download.db-ip.com:8443/a.mmdb.gz",
		"https://db-ip.com/a.mmdb.gz",
		"ftp://download.db-ip.com/a.mmdb.gz",
	} {
		if err := ok(bad).Validate(); err == nil {
			t.Errorf("%s must be rejected for dbip_asn", bad)
		}
	}
	// The host rule is for dbip_asn only: other kinds keep their freedom.
	c := Default()
	c.GeoDB.Sources = []GeoSource{{Kind: "asn", URL: "https://mirror.example/asn.mmdb"}}
	if err := c.Validate(); err != nil {
		t.Errorf("an asn source on another host must stay valid: %v", err)
	}
}
