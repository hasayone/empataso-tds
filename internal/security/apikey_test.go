package security

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestGenerateAPIKey checks the raw secret's shape, uniqueness, and the
// stored prefix and scopes.
func TestGenerateAPIKey(t *testing.T) {
	s := NewKeySet()
	raw1, key1, err := s.Generate("  night shift  ", []string{" engine ", ScopeEngine, ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	raw2, key2, err := s.Generate("other", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{raw1, raw2} {
		if !strings.HasPrefix(raw, "kz_") || len(raw) != 43 {
			t.Fatalf("raw key length %d, prefix ok %v", len(raw), strings.HasPrefix(raw, "kz_"))
		}
		if _, err := hex.DecodeString(raw[3:]); err != nil {
			t.Fatal(err)
		}
	}
	if raw1 == raw2 || key1.ID == key2.ID {
		t.Fatal("two Generate calls returned the same secret or id")
	}
	if key1.Name != "night shift" {
		t.Fatalf("name = %q", key1.Name)
	}
	if key1.Prefix != raw1[3:11] || len(key1.Prefix) != 8 {
		t.Fatalf("prefix = %q", key1.Prefix)
	}
	if !slices.Equal(key1.Scopes, []string{ScopeEngine, ScopeAdmin}) {
		t.Fatalf("scopes = %v", key1.Scopes)
	}
	sum := sha256.Sum256([]byte(raw1))
	if key1.Hash != hex.EncodeToString(sum[:]) {
		t.Fatal("stored hash is not sha256 of the raw key")
	}
	if !key1.Active() || !key1.HasScope(ScopeEngine) || !key1.HasScope(ScopeAdmin) {
		t.Fatal("new key should be active and carry both scopes")
	}
	if _, ok := s.Verify(raw1, ScopeEngine); !ok {
		t.Fatal("engine scope should verify")
	}
	if _, ok := s.Verify(raw1, ScopeAdmin); !ok {
		t.Fatal("admin scope should verify")
	}
}

// TestVerifyAPIKey checks the scope, a damaged secret, and an empty secret.
func TestVerifyAPIKey(t *testing.T) {
	s := NewKeySet()
	raw, _, err := s.Generate("n", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(raw, ScopeAdmin); !ok {
		t.Fatal("matching scope should verify")
	}
	if _, ok := s.Verify(raw, ScopeEngine); ok {
		t.Fatal("foreign scope should not verify")
	}
	if _, ok := s.Verify(corruptKey(raw), ScopeAdmin); ok {
		t.Fatal("damaged key should not verify")
	}
	if _, ok := s.Verify("", ScopeAdmin); ok {
		t.Fatal("empty key should not verify")
	}
}

// TestRevokeAPIKey checks that revoke sticks, that a second revoke is a
// no-op, and that the revoked time survives a save and load.
func TestRevokeAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	s := NewKeySet()
	raw, key, err := s.Generate("n", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Revoke(key.ID) {
		t.Fatal("revoke should succeed")
	}
	if _, ok := s.Verify(raw, ScopeAdmin); ok {
		t.Fatal("revoked key should not verify")
	}
	if s.Revoke(key.ID) {
		t.Fatal("second revoke should return false")
	}
	if s.Revoke("missing") || s.Revoke("") {
		t.Fatal("unknown id should return false")
	}
	list := s.List()
	if len(list) != 1 || list[0].Active() || list[0].RevokedAt == nil || list[0].Hash != "" {
		t.Fatalf("list after revoke: %+v", list)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("revoked_at")) {
		t.Fatal("revoked key file should contain revoked_at")
	}
	loaded, err := LoadKeySet(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Verify(raw, ScopeAdmin); ok {
		t.Fatal("revoked key should stay revoked after load")
	}
}

// TestAPIKeySaveLoad checks the round trip, the file mode, and that the raw
// secret and revoked_at of a live key stay out of the file.
func TestAPIKeySaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	s := NewKeySet()
	raw, key, err := s.Generate("campaign", []string{ScopeAdmin, ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Generate("later", []string{ScopeEngine}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(raw)) {
		t.Fatal("key file contains the raw secret")
	}
	if !bytes.Contains(body, []byte(key.Hash)) {
		t.Fatal("key file is missing the hash")
	}
	if bytes.Contains(body, []byte("revoked_at")) {
		t.Fatal("active key file should omit revoked_at")
	}
	var recs []APIKey
	if err := json.Unmarshal(body, &recs); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].CreatedAt.Before(recs[i-1].CreatedAt) {
			t.Fatal("key file is not sorted by CreatedAt")
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
		}
	}
	loaded, err := LoadKeySet(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Verify(raw, ScopeAdmin); !ok {
		t.Fatal("loaded key should verify with its scope")
	}
	if _, ok := loaded.Verify(raw, ScopeEngine); ok {
		t.Fatal("loaded key should not gain a scope")
	}
}

// TestAPIKeyReloadKeepsSet checks that a broken file does not replace the
// set, and that a valid file does.
func TestAPIKeyReloadKeepsSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	s := NewKeySet()
	raw, _, err := s.Generate("kept", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(path); err == nil {
		t.Fatal("broken JSON should return an error")
	}
	if _, ok := s.Verify(raw, ScopeAdmin); !ok {
		t.Fatal("failed reload dropped the set")
	}

	good := filepath.Join(dir, "good.json")
	if err := s.Save(good); err != nil {
		t.Fatal(err)
	}
	extra, _, err := s.Generate("extra", []string{ScopeEngine})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(good); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(raw, ScopeAdmin); !ok {
		t.Fatal("reload lost the saved key")
	}
	if _, ok := s.Verify(extra, ScopeEngine); ok {
		t.Fatal("reload kept a key that was not in the file")
	}
}

// TestAPIKeyListRedacts checks that List drops the hash and never carries
// the raw secret.
func TestAPIKeyListRedacts(t *testing.T) {
	s := NewKeySet()
	raw, key, err := s.Generate("n", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("list len = %d", len(list))
	}
	if list[0].Hash != "" {
		t.Fatal("list item still has a hash")
	}
	if list[0].ID != key.ID || list[0].Prefix != key.Prefix {
		t.Fatal("list dropped the public fields")
	}
	blob, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte(raw)) || bytes.Contains(blob, []byte(key.Hash)) {
		t.Fatal("list JSON contains the raw secret or the hash")
	}
}

// TestAPIKeyGenerateRejects checks empty names, empty scopes, and unknown scopes.
func TestAPIKeyGenerateRejects(t *testing.T) {
	s := NewKeySet()
	cases := []struct {
		name   string
		scopes []string
		want   error
	}{
		{name: "  ", scopes: []string{ScopeAdmin}, want: ErrAPIKeyName},
		{name: "", scopes: []string{ScopeAdmin}, want: ErrAPIKeyName},
		{name: strings.Repeat("я", 65), scopes: []string{ScopeAdmin}, want: ErrAPIKeyNameLen},
		{name: "ok", scopes: nil, want: ErrAPIKeyScope},
		{name: "ok", scopes: []string{}, want: ErrAPIKeyScope},
		{name: "ok", scopes: []string{"billing"}, want: ErrAPIKeyScope},
		{name: "ok", scopes: []string{ScopeAdmin, "nope"}, want: ErrAPIKeyScope},
	}
	for _, tc := range cases {
		if _, _, err := s.Generate(tc.name, tc.scopes); !errors.Is(err, tc.want) {
			t.Errorf("Generate(%q, %v) err = %v, want %v", tc.name, tc.scopes, err, tc.want)
		}
	}
	if len(s.List()) != 0 {
		t.Fatal("rejected Generate changed the set")
	}
	long := strings.Repeat("я", 64)
	if _, key, err := s.Generate(long, []string{ScopeEngine}); err != nil {
		t.Fatal(err)
	} else if key.Name != long {
		t.Fatalf("name = %q", key.Name)
	}
}

// TestLoadKeySetMissingFile checks that a new install gets an empty set.
func TestLoadKeySetMissingFile(t *testing.T) {
	s, err := LoadKeySet(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || len(s.List()) != 0 {
		t.Fatal("missing file should load as an empty set")
	}
}

// TestLoadKeySetBrokenJSON checks that a corrupt file is an error.
func TestLoadKeySetBrokenJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeySet(path); err == nil {
		t.Fatal("broken JSON should return an error")
	}
}

// TestValidScopes checks trim, dedupe, and rejection of unknown scopes.
func TestValidScopes(t *testing.T) {
	got, err := ValidScopes([]string{" engine ", ScopeEngine, ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{ScopeEngine, ScopeAdmin}) {
		t.Fatalf("scopes = %v", got)
	}
	if _, err := ValidScopes(nil); !errors.Is(err, ErrAPIKeyScope) {
		t.Fatalf("nil scopes err = %v", err)
	}
	if _, err := ValidScopes([]string{"nope"}); !errors.Is(err, ErrAPIKeyScope) {
		t.Fatalf("unknown scope err = %v", err)
	}
}

// corruptKey flips the last character so the secret no longer matches.
func corruptKey(raw string) string {
	b := []byte(raw)
	if b[len(b)-1] == '0' {
		b[len(b)-1] = '1'
	} else {
		b[len(b)-1] = '0'
	}
	return string(b)
}
