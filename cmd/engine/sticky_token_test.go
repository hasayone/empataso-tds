package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
	"github.com/redis/go-redis/v9"
)

const stickyNameA, stickyNameB = "premium-offer", "second-offer"

// twoSuitable is a sticky group of two streams that both fit the test visitor.
func twoSuitable(method string) *config.Group {
	return stickyGroup(method, weighted(stickyNameA, 1), weighted(stickyNameB, 1))
}

// forgeries lists cookie or Redis values an outsider could build to pick stream
// name without the signing key.
func forgeries(name string) map[string]string {
	plain := sha256.Sum256([]byte("g1\x00" + name))
	other := stickyMAC([]byte("another key of 32 bytes.........."), "g1", name)
	return map[string]string{
		"base64 of the name":   base64.RawURLEncoding.EncodeToString([]byte(name)),
		"plain name":           name,
		"unkeyed sha256":       base64.RawURLEncoding.EncodeToString(plain[:16]),
		"token of another key": other,
		"token of group g2":    stickyMAC((*cidSigner)(nil).derive(stickyKeyLabel), "g2", name),
		"token, one char off":  stickyToken(name)[1:],
		"token, one char over": stickyToken(name) + "A",
		"empty":                "",
		"not base64":           strings.Repeat("%", stickyTokenLen),
		"over-long":            strings.Repeat("x", 500),
	}
}

func TestStickyTokenProperties(t *testing.T) {
	key := (*cidSigner)(nil).derive(stickyKeyLabel)
	tok := stickyMAC(key, "g1", stickyNameA)
	if len(tok) != stickyTokenLen {
		t.Fatalf("token length %d, want %d", len(tok), stickyTokenLen)
	}
	if tok != stickyMAC(key, "g1", stickyNameA) {
		t.Error("the token must be stable for a stream")
	}
	if strings.Contains(tok, stickyNameA) || strings.Contains(tok, "offer") {
		t.Errorf("token %q shows the stream name", tok)
	}
	if tok == stickyMAC(key, "g1", stickyNameB) || tok == stickyMAC(key, "g2", stickyNameA) {
		t.Error("the token must differ by stream and by group")
	}
	if tok == stickyMAC([]byte("another key of 32 bytes.........."), "g1", stickyNameA) {
		t.Error("the token must depend on the key")
	}
	if k := (*cidSigner)(nil).derive(stickyKeyLabel); string(k) == string((*cidSigner)(nil).signKey()) {
		t.Error("the sticky key must be derived, not the signing key itself")
	}
}

// The sticky key is derived once per signer and matches the derivation.
func TestStickyKeyIsDerivedOncePerSigner(t *testing.T) {
	s := keyedSigner([]byte("k"))
	if string(s.stickyKey()) != string(s.derive(stickyKeyLabel)) || len(s.sticky) == 0 {
		t.Fatal("a keyed signer must hold its derived sticky key")
	}
	if &s.stickyKey()[0] != &s.stickyKey()[0] {
		t.Error("the sticky key must not be rebuilt per call")
	}
	if got := (&cidSigner{key: []byte("k")}).stickyKey(); string(got) != string(s.stickyKey()) {
		t.Error("a signer without the cached key must derive the same key")
	}
	if got := (*cidSigner)(nil).stickyKey(); string(got) != string((*cidSigner)(nil).derive(stickyKeyLabel)) {
		t.Error("a nil signer must use the process key")
	}
	if string(s.stickyKey()) == string(keyedSigner([]byte("other")).stickyKey()) {
		t.Error("the sticky key must depend on the signing key")
	}
}

func TestStickyCookieHidesStreamName(t *testing.T) {
	h := testEnv(t, config.NewGroups(twoSuitable("cookie")))
	j := jar{}
	rec := j.visit(t, h, "/g1")
	served := streamOf(rec)
	v := j["zts_g1"].Value
	if v != stickyToken(served) || strings.Contains(v, "offer") || strings.Contains(v, served) {
		t.Errorf("cookie %q must be the opaque token of %q", v, served)
	}
	if got := rec.Header().Get("Set-Cookie"); strings.Contains(got, served) {
		t.Errorf("Set-Cookie shows the stream name: %q", got)
	}
}

// A forged cookie must never pick a suitable stream: the engine draws again and
// replaces the cookie with its own token.
func TestStickyForgedCookieIsRedrawn(t *testing.T) {
	h := testEnv(t, config.NewGroups(twoSuitable("cookie")))
	for _, name := range []string{stickyNameA, stickyNameB} {
		for what, value := range forgeries(name) {
			j := jar{"zts_g1": {Name: "zts_g1", Value: value}}
			served := streamOf(j.visit(t, h, "/g1"))
			if served != stickyNameA && served != stickyNameB {
				t.Fatalf("%s %q: served %q", what, name, served)
			}
			if c := j["zts_g1"]; c == nil || c.Value != stickyToken(served) {
				t.Errorf("%s %q: the forged value was kept: %+v", what, name, c)
			}
		}
	}
}

// A real token of a stream that no longer fits is ignored, and the visitor is
// moved to a stream that fits.
func TestStickyUnsuitableStreamIsDropped(t *testing.T) {
	phonesOnly := weighted("phones", 1)
	phonesOnly.Rules = config.Rules{Computer: config.FlagA} // the test visitor is a computer
	off := weighted("off", 1)
	off.Status = false
	zero := weighted("zero", 0)
	h := testEnv(t, config.NewGroups(stickyGroup("cookie", weighted("main", 1), phonesOnly, off, zero)))
	for _, name := range []string{"ghost", "off", "phones", "zero"} {
		j := jar{"zts_g1": {Name: "zts_g1", Value: stickyToken(name)}}
		if got := streamOf(j.visit(t, h, "/g1")); got != "main" {
			t.Errorf("%s: served %q, want main", name, got)
		}
		if c := j["zts_g1"]; c == nil || c.Value != stickyToken("main") {
			t.Errorf("%s: cookie after the visit = %+v, want the token of main", name, c)
		}
	}
}

// A token issued for another group is not valid here.
func TestStickyTokenOfAnotherGroup(t *testing.T) {
	g := twoSuitable("cookie")
	h := testEnv(t, config.NewGroups(g))
	foreign := stickyMAC((*cidSigner)(nil).derive(stickyKeyLabel), "g2", stickyNameA)
	for i := 0; i < 20; i++ {
		j := jar{"zts_g1": {Name: "zts_g1", Value: foreign}}
		served := streamOf(j.visit(t, h, "/g1"))
		if j["zts_g1"].Value != stickyToken(served) {
			t.Fatalf("the foreign token was accepted: %+v", j["zts_g1"])
		}
	}
}

func TestStickyRedisHoldsToken(t *testing.T) {
	mr := miniredis.RunT(t)
	h := testEnv(t, config.NewGroups(twoSuitable("ip")), func(d *engineDeps) {
		d.counters = store.NewCounters(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	})
	served := streamOf((jar{}).visit(t, h, "/g1"))
	got, err := mr.Get("st:g1:8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if got != stickyToken(served) || strings.Contains(got, "offer") {
		t.Errorf("redis value %q must be the opaque token of %q", got, served)
	}
	for _, name := range []string{stickyNameA, stickyNameB} {
		for what, value := range forgeries(name) {
			mr.Set("st:g1:8.8.8.8", value)
			served := streamOf((jar{}).visit(t, h, "/g1"))
			if now, _ := mr.Get("st:g1:8.8.8.8"); now != stickyToken(served) {
				t.Errorf("%s %q: the forged value %q was kept", what, name, now)
			}
		}
	}
}

func TestStickyOverLongCookieIsIgnored(t *testing.T) {
	h := testEnv(t, config.NewGroups(twoSuitable("cookie")))
	j := jar{"zts_g1": {Name: "zts_g1", Value: strings.Repeat("A", 4000)}}
	if rec := j.visit(t, h, "/g1"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(j["zts_g1"].Value) != stickyTokenLen {
		t.Errorf("cookie %q was kept", j["zts_g1"].Value)
	}
}
