package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

func TestNewCIDFormat(t *testing.T) {
	s := &cidSigner{key: []byte("k")}
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c := s.newCID()
		if len(c) != cidLen {
			t.Fatalf("cid len = %d, want %d: %q", len(c), cidLen, c)
		}
		for _, r := range c {
			if !strings.ContainsRune(cidAlphabet, r) {
				t.Fatalf("cid has char outside base36: %q", c)
			}
		}
		if seen[c] {
			t.Fatalf("duplicate cid %q", c)
		}
		seen[c] = true
		if !s.reusedCID(c) {
			t.Fatalf("own cid %q does not verify", c)
		}
	}
}

func TestReusedCID(t *testing.T) {
	s := &cidSigner{key: []byte("k")}
	other := &cidSigner{key: []byte("another")}
	own := s.newCID()
	forged := own[:cidRandLen] + string(cidAlphabet[(strings.IndexByte(cidAlphabet, own[cidLen-1])+1)%len(cidAlphabet)])
	cases := []struct {
		name string
		cid  string
		want bool
	}{
		{"own", own, true},
		{"legacy hex", "0123456789", true},
		{"legacy upper", "0123456789A", false},
		{"signature changed", forged, false},
		{"other key", other.newCID(), false},
		{"empty", "", false},
		{"nine chars", "abcdefghi", false},
		{"thirteen chars", own + "a", false},
		{"upper case", strings.ToUpper(own), false},
		{"symbol", own[:5] + "-" + own[6:], false},
		{"imported ten non-hex", "ghijklmnop", false},
	}
	for _, c := range cases {
		if got := s.reusedCID(c.cid); got != c.want {
			t.Errorf("%s: reusedCID(%q) = %v, want %v", c.name, c.cid, got, c.want)
		}
	}
}

func TestNilSignerWorks(t *testing.T) {
	var s *cidSigner
	c := s.newCID()
	if len(c) != cidLen || !s.reusedCID(c) {
		t.Fatalf("nil signer cid %q", c)
	}
}

func TestCIDSignatureLength(t *testing.T) {
	if cidRandLen != 9 || cidSigLen != 3 || cidLen != 12 {
		t.Fatalf("lengths %d+%d=%d, want 9+3=12", cidRandLen, cidSigLen, cidLen)
	}
	s := &cidSigner{key: []byte("k")}
	if got := s.sign("abcdefghi"); len(got) != cidSigLen {
		t.Fatalf("signature %q", got)
	}
}

func TestCIDKeyPath(t *testing.T) {
	if got := cidKeyPath("", ""); got != "" {
		t.Errorf("no groups file: %q, want empty", got)
	}
	if got, want := cidKeyPath("", filepath.Join("state", "groups.json")), filepath.Join("state", cidKeyFile); got != want {
		t.Errorf("next to groups file: %q, want %q", got, want)
	}
	if got := cidKeyPath(" /x/own.key ", "state/groups.json"); got != "/x/own.key" {
		t.Errorf("override: %q", got)
	}
}

func TestNewCIDSignerKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", cidKeyFile)
	a := newCIDSigner("", path, discardLog())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) != cidKeyBytes*2 {
		t.Fatalf("key file content %q", raw)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Errorf("key file mode = %v, want 0600", st.Mode().Perm())
	}
	b := newCIDSigner("", path, discardLog())
	if !b.reusedCID(a.newCID()) {
		t.Error("a restarted engine must accept ids issued before the restart")
	}
}

// A key file that is empty, short or not hex is never rewritten: the engine
// signs with a process key and leaves the operator's file for inspection.
func TestNewCIDSignerKeepsBadFile(t *testing.T) {
	for name, content := range map[string]string{
		"empty":   "",
		"short":   "abcd\n",
		"not hex": strings.Repeat("zz", cidKeyBytes) + "\n",
	} {
		path := filepath.Join(t.TempDir(), cidKeyFile)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		s := newCIDSigner("", path, discardLog())
		if !s.reusedCID(s.newCID()) {
			t.Errorf("%s: signer does not verify its own ids", name)
		}
		if got, _ := os.ReadFile(path); string(got) != content {
			t.Errorf("%s: file rewritten to %q", name, got)
		}
		if string(s.key) != string(processKeySigner().key) {
			t.Errorf("%s: want the process key", name)
		}
	}
}

// A read error other than "not found" (a directory stands where the file
// should be) keeps the process key and creates nothing.
func TestNewCIDSignerUnreadablePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, cidKeyFile)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newCIDSigner("", path, discardLog())
	if string(s.key) != string(processKeySigner().key) {
		t.Error("want the process key")
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		t.Errorf("path was replaced: %v %v", st, err)
	}
}

func TestNewCIDSignerFallbackLogsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), cidKeyFile)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	newCIDSigner("", path, log)
	if got := out.String(); !strings.Contains(got, `"level":"ERROR"`) ||
		!strings.Contains(got, "using a process key") {
		t.Fatalf("fallback log = %q, want ERROR", got)
	}
}

func TestNewCIDSignerWithoutPathLogsError(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	newCIDSigner("", "", log)
	if got := out.String(); !strings.Contains(got, `"level":"ERROR"`) ||
		!strings.Contains(got, "using a process key") {
		t.Fatalf("fallback log = %q, want ERROR", got)
	}
}

// Engines starting together share one key: only one creates the file and the
// others read it.
func TestLoadCIDKeyConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), cidKeyFile)
	const n = 8
	keys := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keys[i], errs[i] = loadCIDKey(path)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("loader %d: %v", i, errs[i])
		}
		if string(keys[i]) != string(keys[0]) {
			t.Fatalf("loader %d got a different key", i)
		}
	}
}

func TestNewCIDSignerEnvWins(t *testing.T) {
	dir := t.TempDir()
	env := "  " + strings.Repeat("s", cidKeyBytes) + " "
	s := newCIDSigner(env, filepath.Join(dir, cidKeyFile), discardLog())
	if _, err := os.Stat(filepath.Join(dir, cidKeyFile)); err == nil {
		t.Error("env key must not create a key file")
	}
	if !newCIDSigner(strings.Repeat("s", cidKeyBytes), "", discardLog()).reusedCID(s.newCID()) {
		t.Error("same env key must verify")
	}
}

func TestNewCIDSignerShortEnvFallsBackToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), cidKeyFile)
	s := newCIDSigner(strings.Repeat("s", cidKeyBytes-1), path, discardLog())
	if string(s.key) == strings.Repeat("s", cidKeyBytes-1) {
		t.Fatal("a short env key was used")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file key not created: %v", err)
	}
	if !newCIDSigner("", path, discardLog()).reusedCID(s.newCID()) {
		t.Error("the file key must verify ids of the fallback signer")
	}
}

func TestNewCIDSignerWithoutPath(t *testing.T) {
	s := newCIDSigner("", "", nil)
	if !s.reusedCID(s.newCID()) {
		t.Fatal("signer without a file must still work")
	}
}

func TestSignVerifyOrigin(t *testing.T) {
	s := newCIDSigner(strings.Repeat("x", cidKeyBytes), "", discardLog())
	tok := s.signOrigin("camp-a")
	gid, ok := s.verifyOrigin(tok)
	if !ok || gid != "camp-a" {
		t.Fatalf("verify %q → %q %v", tok, gid, ok)
	}
	if _, ok := s.verifyOrigin("camp-a.bad"); ok {
		t.Fatal("bad sig accepted")
	}
}

func TestSignVerifyOriginDottedID(t *testing.T) {
	s := newCIDSigner(strings.Repeat("y", cidKeyBytes), "", discardLog())
	gid := "camp.v1"
	tok := s.signOrigin(gid)
	got, ok := s.verifyOrigin(tok)
	if !ok || got != gid {
		t.Fatalf("verify %q → %q %v", tok, got, ok)
	}
}

func TestOriginTokenNotCIDAndCIDNotOrigin(t *testing.T) {
	s := newCIDSigner(strings.Repeat("z", cidKeyBytes), "", discardLog())
	tok := s.signOrigin("camp.v1")
	if s.reusedCID(tok) {
		t.Fatal("origin token must not pass cid check")
	}
	cid := s.newCID()
	if _, ok := s.verifyOrigin(cid); ok {
		t.Fatal("cid must not verify as origin")
	}
}

func TestSignVerifyPath(t *testing.T) {
	s := newCIDSigner(strings.Repeat("p", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	tok := s.signPath("camp-a", "root-cid", now)
	gid, rootCID, issued, ok := s.verifyPath(tok, now)
	if !ok || gid != "camp-a" || rootCID != "root-cid" || !issued.Equal(now) {
		t.Fatalf("verify %q → %q %q %v %v", tok, gid, rootCID, issued, ok)
	}
}

func TestPathRejectsTamperedFields(t *testing.T) {
	s := newCIDSigner(strings.Repeat("q", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	parts := strings.Split(s.signPath("camp-a", "root-cid", now), ".")
	if len(parts) != 4 {
		t.Fatalf("token parts = %q", parts)
	}
	tamperedSig := "A" + parts[3][1:]
	if tamperedSig == parts[3] {
		tamperedSig = "B" + parts[3][1:]
	}
	cases := map[string][]string{
		"gid":  {"camp-b", parts[1], parts[2], parts[3]},
		"cid":  {parts[0], "other-cid", parts[2], parts[3]},
		"time": {parts[0], parts[1], strconv.FormatInt(now.Add(-time.Second).Unix(), 36), parts[3]},
		"sig":  {parts[0], parts[1], parts[2], tamperedSig},
	}
	for name, changed := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, ok := s.verifyPath(strings.Join(changed, "."), now); ok {
				t.Fatal("tampered token accepted")
			}
		})
	}
}

func TestPathValidityWindow(t *testing.T) {
	s := newCIDSigner(strings.Repeat("r", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name   string
		issued time.Time
		ok     bool
	}{
		{"expired", now.Add(-pathTokenTTL - time.Second), false},
		{"ttl boundary", now.Add(-pathTokenTTL), true},
		{"future within skew", now.Add(pathTokenSkew), true},
		{"future beyond skew", now.Add(pathTokenSkew + time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := s.signPath("camp-a", "root-cid", tc.issued)
			if _, _, _, ok := s.verifyPath(tok, now); ok != tc.ok {
				t.Fatalf("verify issued %v = %v, want %v", tc.issued, ok, tc.ok)
			}
		})
	}
}

func TestPathDottedGroupID(t *testing.T) {
	s := newCIDSigner(strings.Repeat("s", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	tok := s.signPath("camp.region.v1", "root-cid", now)
	gid, rootCID, _, ok := s.verifyPath(tok, now)
	if !ok || gid != "camp.region.v1" || rootCID != "root-cid" {
		t.Fatalf("verify dotted gid → %q %q %v", gid, rootCID, ok)
	}
}

func TestPathRejectsEmptyAndMalformedFields(t *testing.T) {
	s := newCIDSigner(strings.Repeat("t", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	if got := s.signPath("", "root-cid", now); got != "" {
		t.Fatalf("empty gid signed as %q", got)
	}
	if got := s.signPath("camp-a", "", now); got != "" {
		t.Fatalf("empty cid signed as %q", got)
	}
	if got := s.signPath("camp-a", "root.cid", now); got != "" {
		t.Fatalf("dotted cid signed as %q", got)
	}
	valid := s.signPath("camp-a", "root-cid", now)
	for _, tok := range []string{
		"",
		"camp-a",
		"camp-a.root-cid",
		"camp-a.root-cid.time",
		".root-cid.time.sig",
		"camp-a..time.sig",
		"camp-a.root-cid..sig",
		"camp-a.root-cid.time.",
		valid + ".extra",
	} {
		if _, _, _, ok := s.verifyPath(tok, now); ok {
			t.Errorf("malformed token accepted: %q", tok)
		}
	}
}

func TestPathRejectsOversizedToken(t *testing.T) {
	s := newCIDSigner(strings.Repeat("x", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	gid := strings.Repeat("g", pathTokenMaxLen)
	tok := s.signPath(gid, "root-cid", now)
	if len(tok) <= pathTokenMaxLen {
		t.Fatalf("token length = %d, want over %d", len(tok), pathTokenMaxLen)
	}
	if _, _, _, ok := s.verifyPath(tok, now); ok {
		t.Fatal("oversized token accepted")
	}
}

func TestPathRejectsNonCanonicalTimestamp(t *testing.T) {
	s := newCIDSigner(strings.Repeat("y", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	tokenWithTS := func(ts36 string) string {
		body := "camp-a.root-cid." + ts36
		m := hmac.New(sha256.New, s.signKey())
		m.Write([]byte(pathMACPrefix))
		m.Write([]byte(body))
		sig := base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:originSigBytes])
		return body + "." + sig
	}
	validTS := strconv.FormatInt(now.Unix(), 36)
	for name, ts36 := range map[string]string{
		"negative":     "-1",
		"plus":         "+1",
		"uppercase":    strings.ToUpper(validTS),
		"leading zero": "0" + validTS,
		"overflow":     "1y2p0ij32e8e8",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, ok := s.verifyPath(tokenWithTS(ts36), now); ok {
				t.Fatalf("timestamp %q accepted", ts36)
			}
		})
	}
}

func TestOriginAndPathFormatsAreDistinct(t *testing.T) {
	s := newCIDSigner(strings.Repeat("u", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	origin := s.signOrigin("camp.v1")
	path := s.signPath("camp.v1", "root-cid", now)
	if gid, ok := s.verifyOrigin(origin); !ok || gid != "camp.v1" {
		t.Fatalf("legacy origin rejected: %q %v", gid, ok)
	}
	if _, ok := s.verifyOrigin(path); ok {
		t.Fatal("path token accepted as legacy origin")
	}
	if _, _, _, ok := s.verifyPath(origin, now); ok {
		t.Fatal("legacy origin accepted as path token")
	}
}

func TestNilSignerPathToken(t *testing.T) {
	var s *cidSigner
	now := time.Unix(1_800_000_000, 0)
	tok := s.signPath("camp-a", "root-cid", now)
	gid, rootCID, issued, ok := s.verifyPath(tok, now)
	if !ok || gid != "camp-a" || rootCID != "root-cid" || !issued.Equal(now) {
		t.Fatalf("nil signer verify → %q %q %v %v", gid, rootCID, issued, ok)
	}
}

func TestPathTokenRejectsDifferentKey(t *testing.T) {
	a := newCIDSigner(strings.Repeat("v", cidKeyBytes), "", discardLog())
	b := newCIDSigner(strings.Repeat("w", cidKeyBytes), "", discardLog())
	now := time.Unix(1_800_000_000, 0)
	if _, _, _, ok := b.verifyPath(a.signPath("camp-a", "root-cid", now), now); ok {
		t.Fatal("token from another key accepted")
	}
}

func TestOfferStepRejectsImportedCID(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop",
		Streams: []config.Stream{{
			Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "ok"},
		}},
	})
	h, ins, stop := curlEnv(t, g, "0")
	do(t, h, "/g1/offer?cid=a1b2c3d4e5f6", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	stop()
	if len(ins.events) != 1 {
		t.Fatalf("events %+v", ins.events)
	}
	if got := ins.events[0].CID; got == "a1b2c3d4e5f6" || len(got) != cidLen {
		t.Fatalf("foreign cid was kept or new one malformed: %q", got)
	}
}

func TestOfferStepReusesSignedCID(t *testing.T) {
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop",
		Streams: []config.Stream{{
			Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "ok"},
		}},
	})
	h, ins, stop := curlEnv(t, g, "0")
	signed := (*cidSigner)(nil).newCID()
	do(t, h, "/g1/offer?cid="+signed, "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	stop()
	if len(ins.events) != 1 || ins.events[0].CID != signed {
		t.Fatalf("events %+v", ins.events)
	}
}
