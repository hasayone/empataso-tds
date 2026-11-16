package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/store"
	"github.com/redis/go-redis/v9"
)

// TestUniqCookieSignature checks the MAC, the campaign binding, the cookie name,
// the expiry and that a plain legacy value does not pass.
func TestUniqCookieSignature(t *testing.T) {
	key := bytesKey(7)
	other := bytesKey(8)
	now := time.Unix(1_700_000_000, 0)
	exp := now.Add(time.Hour).Unix()
	value := uniqCookieValue(key, "camp", "ztu_camp", exp)
	if strings.Contains(value, "203.0.113.5") || strings.Contains(value, "SecretAgent/9") || strings.Count(value, ".") != 1 {
		t.Fatalf("cookie holds visitor text: %q", value)
	}
	if !uniqCookieValid(key, "camp", "ztu_camp", value, now) {
		t.Fatal("a fresh cookie must pass")
	}
	if uniqCookieValid(other, "camp", "ztu_camp", value, now) {
		t.Fatal("another key must not pass")
	}
	if uniqCookieValid(key, "other", "ztu_camp", value, now) {
		t.Fatal("another campaign must not pass")
	}
	if uniqCookieValid(key, "camp", "ztuo_camp", value, now) {
		t.Fatal("the offer cookie name must not pass")
	}
	past := uniqCookieValue(key, "camp", "ztu_camp", now.Add(-time.Minute).Unix())
	if uniqCookieValid(key, "camp", "ztu_camp", past, now) {
		t.Fatal("an expired cookie must not pass")
	}
	for _, bad := range []string{"1", "", "nope", value + "x", "9999999999.aaaa"} {
		if uniqCookieValid(key, "camp", "ztu_camp", bad, now) {
			t.Fatalf("forged value passed: %q", bad)
		}
	}
	dot := strings.IndexByte(value, '.')
	flipped := value[:dot+1] + "A" + value[dot+2:]
	if uniqCookieValid(key, "camp", "ztu_camp", flipped, now) {
		t.Fatal("a flipped MAC character must not pass")
	}
}

// bytesKey is a 32-byte uniqueness key filled with b.
func bytesKey(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// TestSignedCookieOutlivesRedis checks the cookie term, the Redis ceiling and
// that a live cookie is a repeat after Redis has dropped the key.
func TestSignedCookieOutlivesRedis(t *testing.T) {
	mr := startRedis(t)
	const window = 8760 * 3600
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop", UniqCookie: true, UniqSeconds: window,
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "OK"}}},
	})
	h := testEnv(t, g, func(d *engineDeps) {
		d.counters = store.NewCounters(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	})
	ua := "SecretAgent/9"
	rec := do(t, h, "/g1", "203.0.113.5", map[string]string{"User-Agent": ua})
	if rec.Header().Get("X-Emtds-Uniq") != "yes" {
		t.Fatalf("first visit %q", rec.Header().Get("X-Emtds-Uniq"))
	}
	ck := rec.Result().Cookies()
	if len(ck) != 1 || ck[0].Name != "ztu_g1" || ck[0].MaxAge != window || !ck[0].HttpOnly {
		t.Fatalf("cookie %+v", ck)
	}
	if strings.Contains(ck[0].Value, "203.0.113.5") || strings.Contains(ck[0].Value, ua) || ck[0].Value == "1" {
		t.Fatalf("cookie value %q", ck[0].Value)
	}
	if ttl := mr.TTL("uniq:g1:203.0.113.5"); ttl != 24*time.Hour {
		t.Errorf("redis ttl = %v, want 24h", ttl)
	}
	mr.FlushAll()
	again := doCookie(t, h, "/g1", "203.0.113.5", ua, ck[0])
	if again.Header().Get("X-Emtds-Uniq") != "no" {
		t.Fatalf("live cookie after redis flush: %q", again.Header().Get("X-Emtds-Uniq"))
	}
	if len(again.Result().Cookies()) != 0 {
		t.Fatal("a live cookie must not be renewed")
	}
	fresh := do(t, h, "/g1", "203.0.113.5", map[string]string{"User-Agent": ua})
	if fresh.Header().Get("X-Emtds-Uniq") != "yes" {
		t.Fatalf("without the cookie the visitor is new, got %q", fresh.Header().Get("X-Emtds-Uniq"))
	}
}

// TestForgedUniqCookieDoesNotCount checks that a plain or altered cookie is not a repeat.
func TestForgedUniqCookieDoesNotCount(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop", UniqCookie: true, UniqSeconds: 3600,
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "OK"}}},
	})
	h := testEnv(t, g)
	req := httptest.NewRequest(http.MethodGet, "/g1", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	req.Header.Set("User-Agent", "SecretAgent/9")
	req.AddCookie(&http.Cookie{Name: "ztu_g1", Value: "1"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Emtds-Uniq") != "yes" {
		t.Fatalf("forged cookie counted as a repeat: %q", rec.Header().Get("X-Emtds-Uniq"))
	}
	got := rec.Result().Cookies()
	if len(got) != 1 || got[0].Value == "1" || !strings.Contains(got[0].Value, ".") {
		t.Fatalf("replacement cookie %+v", got)
	}
}

// TestUniqByUserAgent splits Redis keys by agent and keeps an IP-only campaign shared.
func TestUniqByUserAgent(t *testing.T) {
	mr := startRedis(t)
	uaGroup := func(by string) *config.Groups {
		return config.NewGroups(&config.Group{
			ID: "g1", Status: true, Redirect: "stop", UniqBy: by, UniqSeconds: 3600,
			Streams: []config.Stream{{Name: "s", Status: true,
				Out: config.Output{Redirect: "show_text", Out: "OK"}}},
		})
	}
	h := testEnv(t, uaGroup("ip_ua"), func(d *engineDeps) {
		d.counters = store.NewCounters(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	})
	chrome := map[string]string{"User-Agent": "ChromeUA"}
	firefox := map[string]string{"User-Agent": "FirefoxUA"}
	if got := do(t, h, "/g1", "1.2.3.4", chrome).Header().Get("X-Emtds-Uniq"); got != "yes" {
		t.Fatalf("chrome first %q", got)
	}
	if got := do(t, h, "/g1", "1.2.3.4", firefox).Header().Get("X-Emtds-Uniq"); got != "yes" {
		t.Fatalf("firefox must be its own visitor, got %q", got)
	}
	if got := do(t, h, "/g1", "1.2.3.4", chrome).Header().Get("X-Emtds-Uniq"); got != "no" {
		t.Fatalf("chrome repeat %q", got)
	}
	for _, key := range mr.Keys() {
		if strings.Contains(key, "ChromeUA") || strings.Contains(key, "FirefoxUA") {
			t.Fatalf("redis key holds the agent: %s", key)
		}
	}

	mr2 := startRedis(t)
	old := testEnv(t, config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop", UniqMethod: "ip", UniqSeconds: 3600,
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "OK"}}},
	}), func(d *engineDeps) {
		d.counters = store.NewCounters(redis.NewClient(&redis.Options{Addr: mr2.Addr()}))
	})
	if got := do(t, old, "/g1", "1.2.3.4", chrome).Header().Get("X-Emtds-Uniq"); got != "yes" {
		t.Fatalf("legacy first %q", got)
	}
	if got := do(t, old, "/g1", "1.2.3.4", firefox).Header().Get("X-Emtds-Uniq"); got != "no" {
		t.Fatalf("legacy ip campaign must ignore the agent, got %q", got)
	}
	if _, err := mr2.Get("uniq:g1:1.2.3.4"); err != nil {
		t.Fatalf("legacy key: %v", err)
	}
}

// TestLegacyCookieUnchanged keeps the plain cookie of a campaign without the new fields.
func TestLegacyCookieUnchanged(t *testing.T) {
	h := testEnv(t, uniqGroup("cookie"))
	rec := do(t, h, "/g1", "1.2.3.4", map[string]string{"User-Agent": "ChromeUA"})
	ck := rec.Result().Cookies()
	if len(ck) != 1 || ck[0].Name != "ztu_g1" || ck[0].Value != "1" || ck[0].MaxAge != 3600 {
		t.Fatalf("legacy cookie %+v", ck)
	}
	again := doCookie(t, h, "/g1", "9.9.9.9", "OtherUA", ck[0])
	if again.Header().Get("X-Emtds-Uniq") != "no" {
		t.Fatalf("legacy repeat %q", again.Header().Get("X-Emtds-Uniq"))
	}
}

// startRedis runs a throwaway Redis and closes it with the test.
func startRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return mr
}

// doCookie repeats do with one cookie and a user-agent.
func doCookie(t *testing.T, h http.Handler, path, ip, ua string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", ip)
	req.Header.Set("User-Agent", ua)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
