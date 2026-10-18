package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
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
