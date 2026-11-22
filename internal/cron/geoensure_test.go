package cron

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cityGuardEnv is a runner whose city database is a temp path, whose DB-IP City
// Lite URL points at the env's test server, clock is fixed at now and guard is on.
func cityGuardEnv(t *testing.T, now time.Time) *env {
	t.Helper()
	e := newEnv(t)
	e.r.now = func() time.Time { return now }
	e.r.dbipCityURL = e.srv.URL + "/free/dbip-city-lite-" + MonthPlaceholder + ".mmdb.gz"
	e.r.WatchCityDB()
	return e
}

// cityState is what the guard stored for the panel.
func cityState(e *env) (st GeoCityState) {
	e.r.state(func(s *Status) {
		if s.GeoCity != nil {
			st = *s.GeoCity
		}
	})
	return st
}

// serveCity answers the DB-IP City Lite paths in files and counts the requests.
func serveCity(e *env, files map[string][]byte) *dbipServer {
	var d dbipServer
	e.mux.HandleFunc("/free/", d.handler(files))
	return &d
}

func TestCityGuardDownloadsAMissingDatabase(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	e := cityGuardEnv(t, now)
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	d := serveCity(e, map[string][]byte{"/free/dbip-city-lite-2026-11.mmdb.gz": gz(city)})

	e.r.guardCityDB(context.Background(), Config{})
	if got, _ := os.ReadFile(e.r.paths.CityDB); !bytes.Equal(got, city) {
		t.Fatal("the missing city database was not installed")
	}
	st := cityState(e)
	if !st.Present || st.Running || st.Error != "" || st.DownloadedAt.IsZero() || st.Built.IsZero() {
		t.Fatalf("state after the download: %+v", st)
	}

	// With the database in place a later check asks for nothing. The fixture is
	// years old, so the schedule is left to geo_db here.
	managed := Default()
	managed.GeoDB.Enabled = true
	before := len(d.seen())
	e.r.guardCityDB(context.Background(), managed)
	if after := len(d.seen()); after != before {
		t.Fatalf("a working database must not be downloaded again: %d requests more", after-before)
	}
}

func TestCityGuardFallsBackToThePreviousMonth(t *testing.T) {
	e := cityGuardEnv(t, time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC))
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	d := serveCity(e, map[string][]byte{"/free/dbip-city-lite-2026-12.mmdb.gz": gz(city)})

	e.r.guardCityDB(context.Background(), Config{})
	if got, _ := os.ReadFile(e.r.paths.CityDB); !bytes.Equal(got, city) {
		t.Fatal("the December file must be used until the January one is published")
	}
	hits := d.seen()
	if len(hits) != 2 || !strings.HasPrefix(hits[0], "/free/dbip-city-lite-2027-01") || !strings.HasPrefix(hits[1], "/free/dbip-city-lite-2026-12") {
		t.Fatalf("requests %q, want the current month first and the previous one after a 404", hits)
	}
}

func TestCityGuardRefusesAnInvalidFile(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	e := cityGuardEnv(t, now)
	serveCity(e, map[string][]byte{"/free/dbip-city-lite-2026-11.mmdb.gz": gz([]byte("<html>error</html>"))})

	// No database yet: the junk is not installed, the failure is kept for the panel.
	e.r.guardCityDB(context.Background(), Config{})
	if _, err := os.Stat(e.r.paths.CityDB); err == nil {
		t.Fatal("a download that is not a database was installed")
	}
	st := cityState(e)
	if st.Present || st.Error == "" || !st.NextTry.Equal(now.Add(cityRetryMin)) {
		t.Fatalf("state after a failure: %+v", st)
	}

	// A working but old database stays when the new file is junk.
	good := testDB(t, "GeoLite2-City-Test.mmdb")
	if err := os.WriteFile(e.r.paths.CityDB, good, 0o644); err != nil {
		t.Fatal(err)
	}
	e.r.guardCityDB(context.Background(), Config{})
	if got, _ := os.ReadFile(e.r.paths.CityDB); !bytes.Equal(got, good) {
		t.Fatal("junk replaced the working database")
	}
	if st = cityState(e); !st.Present || st.Error == "" {
		t.Fatalf("a failed refresh must be reported while the old database works: %+v", st)
	}
}

func TestCityGuardReplacesABrokenFile(t *testing.T) {
	e := cityGuardEnv(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC))
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	serveCity(e, map[string][]byte{"/free/dbip-city-lite-2026-11.mmdb.gz": gz(city)})
	if err := os.WriteFile(e.r.paths.CityDB, city[:len(city)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	e.r.guardCityDB(context.Background(), Config{})
	if got, _ := os.ReadFile(e.r.paths.CityDB); !bytes.Equal(got, city) {
		t.Fatal("a file that does not open must be replaced")
	}
}

func TestCityGuardRetryPauseGrowsAndIsCapped(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	e := cityGuardEnv(t, now)
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	var down atomic.Bool
	down.Store(true)
	e.mux.HandleFunc("/free/", func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(gz(city))
	})

	var pauses []time.Duration
	for i := 0; i < 8; i++ {
		e.r.guardCityDB(context.Background(), Config{})
		e.r.mu.Lock()
		pauses = append(pauses, e.r.cityNext.Sub(now))
		e.r.mu.Unlock()
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, cityRetryMax, cityRetryMax, cityRetryMax}
	for i := range want {
		if pauses[i] != want[i] {
			t.Fatalf("pauses %v, want %v", pauses, want)
		}
	}
	if st := cityState(e); !strings.Contains(st.Error, "503") {
		t.Fatalf("the error must say what the server answered: %q", st.Error)
	}

	// The server comes back: the database is installed and the count starts over.
	down.Store(false)
	e.r.guardCityDB(context.Background(), Config{})
	e.r.mu.Lock()
	fails := e.r.cityFails
	e.r.mu.Unlock()
	if st := cityState(e); !st.Present || st.Error != "" || !st.NextTry.IsZero() || fails != 0 {
		t.Fatalf("after the recovery: %+v, %d failures counted", st, fails)
	}
}

func TestCityGuardLooksForANewerFileOnlyWhenNoJobKeepsIt(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	e := cityGuardEnv(t, now)
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	d := serveCity(e, map[string][]byte{"/free/dbip-city-lite-2026-11.mmdb.gz": gz(city)})
	if err := os.WriteFile(e.r.paths.CityDB, city, 0o644); err != nil {
		t.Fatal(err)
	}

	// The fixture is years old and geo_db is on with a city source: the job's business.
	managed := Default()
	managed.GeoDB.Enabled = true
	e.r.guardCityDB(context.Background(), managed)
	if n := len(d.seen()); n != 0 {
		t.Fatalf("the guard touched a database the geo_db job keeps: %d requests", n)
	}

	// With geo_db off the guard refreshes it, once a day at most.
	e.r.guardCityDB(context.Background(), Config{})
	first := len(d.seen())
	if first == 0 {
		t.Fatal("an old database nobody keeps was not refreshed")
	}
	e.r.guardCityDB(context.Background(), Config{})
	if n := len(d.seen()); n != first {
		t.Fatalf("the refresh repeated within a day: %d requests more", n-first)
	}
	e.r.now = func() time.Time { return now.Add(cityRefreshEvery + time.Minute) }
	e.r.guardCityDB(context.Background(), Config{})
	if n := len(d.seen()); n == first {
		t.Fatal("the refresh did not come back after a day")
	}
}

func TestCityGuardRunsFromTickWhateverGeoDBIsSetTo(t *testing.T) {
	e := cityGuardEnv(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC))
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	serveCity(e, map[string][]byte{"/free/dbip-city-lite-2026-11.mmdb.gz": gz(city)})

	// geo_db is off in the default config, and a broken config stops every job.
	if err := os.WriteFile(e.r.paths.Config, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.r.Tick(context.Background())
	e.r.Wait()
	if got, _ := os.ReadFile(e.r.paths.CityDB); !bytes.Equal(got, city) {
		t.Fatal("the first Tick did not install the missing city database")
	}
}

func TestCityGuardIsOffUntilWatched(t *testing.T) {
	e := newEnv(t)
	e.r.dbipCityURL = e.srv.URL + "/free/dbip-city-lite-" + MonthPlaceholder + ".mmdb.gz"
	d := serveCity(e, nil)
	e.r.Tick(context.Background())
	e.r.Wait()
	if n := len(d.seen()); n != 0 {
		t.Fatalf("a runner that does not watch the city database made %d requests", n)
	}
}

func TestGeoDBWithoutAKeyDownloadsDBIPCity(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	e := cityGuardEnv(t, now)
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	serveCity(e, map[string][]byte{"/free/dbip-city-lite-2026-11.mmdb.gz": gz(city)})

	cfg := Default().GeoDB
	cfg.Sources = cfg.Sources[:2] // the keyless MaxMind city and asn sources
	msg, err := e.r.runGeoDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"city: GeoLite2-City updated", "asn: skipped, no MaxMind license key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if got, _ := os.ReadFile(e.r.paths.CityDB); !bytes.Equal(got, city) {
		t.Fatal("the city database came from nowhere")
	}
}

func TestWithCityFallback(t *testing.T) {
	e := newEnv(t)
	keyless := GeoSource{Kind: KindCity, URL: "https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz"}
	keyed := keyless
	keyed.User, keyed.Password = "1", "key"
	asn := GeoSource{Kind: KindASN, URL: "https://download.maxmind.com/geoip/databases/GeoLite2-ASN/download?suffix=tar.gz"}

	got := e.r.withCityFallback([]GeoSource{asn, keyless, keyless})
	if len(got) != 2 || got[0] != asn || got[1].Kind != KindCity || got[1].URL != DBIPCityURL {
		t.Fatalf("keyless city sources must become one DB-IP source in place: %+v", got)
	}
	if got := e.r.withCityFallback([]GeoSource{keyless, keyed}); len(got) != 2 || got[0] != keyless {
		t.Fatalf("a city source with a key keeps the list as it is: %+v", got)
	}
	if got := e.r.withCityFallback([]GeoSource{asn}); len(got) != 1 {
		t.Fatalf("no city source, nothing added: %+v", got)
	}
	e.r.paths.CityDB = ""
	if got := e.r.withCityFallback([]GeoSource{keyless}); len(got) != 1 || got[0] != keyless {
		t.Fatalf("no city path, nowhere to put DB-IP City Lite: %+v", got)
	}
}

func TestIsDBIPAndItsCredentials(t *testing.T) {
	city := GeoSource{Kind: KindCity, URL: " " + DBIPCityURL + " ", User: "1", Password: "key"}
	if !isDBIP(city) || !isDBIP(GeoSource{Kind: KindDBIPASN, URL: "https://mirror.example/x"}) {
		t.Fatal("DB-IP City Lite and every dbip_asn source are DB-IP sources")
	}
	if isDBIP(GeoSource{Kind: KindCity, URL: "https://download.maxmind.com/x"}) {
		t.Fatal("a MaxMind source is not a DB-IP one")
	}
	if u, p := city.Credentials(); u != "" || p != "" {
		t.Fatal("a license key must never be sent to DB-IP")
	}
}
