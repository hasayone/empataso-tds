package security

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeKeyFile writes a key file with one key whose secret is raw and whose
// extra JSON members are extra (for example `,"type":"USER"`).
func writeKeyFile(t *testing.T, raw, extra string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(raw))
	path := filepath.Join(t.TempDir(), "api-keys.json")
	body := `[{"id":"aaaaaaaaaaaa","name":"k","prefix":"12345678","hash":"` + hex.EncodeToString(sum[:]) +
		`","scopes":["admin","engine"],"created_at":"2026-01-01T00:00:00Z"` + extra + `}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGenerateHasNoAccessFields(t *testing.T) {
	s := NewKeySet()
	raw, key, err := s.Generate("plain", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if key.Type != "" || key.RoleID != "" || key.Access != nil || len(key.Actions) != 0 {
		t.Fatalf("access fields on a plain key: %+v", key)
	}
	if key.EffectiveType() != KeyTypeAdmin || key.HasAction(ActionCron) {
		t.Fatalf("empty type must be ADMIN without actions: %+v", key)
	}
	got, ok := s.Verify(raw, ScopeAdmin)
	if !ok || got.EffectiveType() != KeyTypeAdmin || len(got.Actions) != 0 {
		t.Fatalf("verify: %+v %v", got, ok)
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, field := range []string{`"type"`, `"role_id"`, `"access"`, `"actions"`} {
		if strings.Contains(string(data), field) {
			t.Fatalf("plain key file has %s: %s", field, data)
		}
	}
}

func TestOldKeyFileReads(t *testing.T) {
	path := writeKeyFile(t, "kz_old", "")
	s, err := LoadKeySet(path)
	if err != nil {
		t.Fatal(err)
	}
	k, ok := s.Verify("kz_old", ScopeEngine)
	if !ok || k.EffectiveType() != KeyTypeAdmin || len(k.Actions) != 0 || k.HasAction(ActionLogsDelete) {
		t.Fatalf("old key: %+v %v", k, ok)
	}
}

// TestKeyPrefixes requires new keys to start with em_, a stored kz_ or em_ key to
// verify, and a stored key under any other prefix to be refused.
func TestKeyPrefixes(t *testing.T) {
	set := NewKeySet()
	raw, key, err := set.Generate("fresh", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, APIKeyPrefix) || key.Lead != APIKeyPrefix {
		t.Fatalf("new key must start with %s, lead %q", APIKeyPrefix, key.Lead)
	}
	next, rotated, err := set.Rotate(key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(next, APIKeyPrefix) || rotated.Lead != APIKeyPrefix {
		t.Fatalf("rotated key must start with %s, lead %q", APIKeyPrefix, rotated.Lead)
	}
	for raw, want := range map[string]bool{
		"em_new": true, "kz_old": true, "xx_other": false, "plainsecret": false, "EM_upper": false,
	} {
		s, err := LoadKeySet(writeKeyFile(t, raw, ""))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Verify(raw, ScopeAdmin); ok != want {
			t.Errorf("%s: verify %v, want %v", raw, ok, want)
		}
		if HasAPIKeyPrefix(raw) != want {
			t.Errorf("%s: HasAPIKeyPrefix %v, want %v", raw, !want, want)
		}
	}
}

func TestVerifyRefusesInvalidStoredAccess(t *testing.T) {
	for name, extra := range map[string]string{
		"typo type":        `,"type":"admin"`,
		"unknown type":     `,"type":"ROOT"`,
		"typo action":      `,"actions":["logs-delete"]`,
		"user with cron":   `,"type":"USER","actions":["cron"]`,
		"role and access":  `,"type":"USER","role_id":"r","access":{"sections":["offers"],"grants":{}}`,
		"valid legacy":     "",
		"valid admin full": `,"type":"ADMIN","actions":["cron","boards"]`,
	} {
		path := writeKeyFile(t, "kz_x", extra)
		s, err := LoadKeySet(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_, ok := s.Verify("kz_x", ScopeAdmin)
		wantOK := strings.HasPrefix(name, "valid")
		if ok != wantOK {
			t.Fatalf("%s: verify %v, want %v", name, ok, wantOK)
		}
	}
}

func TestInvalidStoredKeyStaysRefusedAfterSaveAndLoad(t *testing.T) {
	path := writeKeyFile(t, "kz_x", `,"type":"admim"`)
	s, err := LoadKeySet(path)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "again.json")
	if err := s.Save(out); err != nil {
		t.Fatal(err)
	}
	again, err := LoadKeySet(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Verify("kz_x", ScopeAdmin); ok {
		t.Fatal("a typo in type must not be repaired by a save")
	}
}

func TestGenerateWithValidation(t *testing.T) {
	cases := []struct {
		name string
		ka   KeyAccess
		want error
	}{
		{"bad type", KeyAccess{Type: "user"}, ErrAPIKeyType},
		{"bad action", KeyAccess{Actions: []string{"nope"}}, ErrAPIKeyAction},
		{"role and access", KeyAccess{Type: KeyTypeUser, RoleID: "r", Access: &Access{Sections: []string{"offers"}}}, ErrAPIKeyAccess},
		{"user cron", KeyAccess{Type: KeyTypeUser, Actions: []string{ActionCron}}, ErrAPIKeyUserAction},
		{"user boards", KeyAccess{Type: KeyTypeUser, Actions: []string{ActionBoards}}, ErrAPIKeyUserAction},
	}
	for _, tc := range cases {
		s := NewKeySet()
		raw, key, err := s.GenerateWith("k", []string{ScopeAdmin}, tc.ka)
		if !errors.Is(err, tc.want) || raw != "" || key.ID != "" {
			t.Fatalf("%s: raw=%q key=%+v err=%v", tc.name, raw, key, err)
		}
		if len(s.List()) != 0 {
			t.Fatalf("%s: set changed", tc.name)
		}
	}

	s := NewKeySet()
	acc := &Access{Sections: []string{"offers"}, Grants: map[string]Grant{"offers": {Mode: ModeRead}}}
	raw, key, err := s.GenerateWith("k", []string{ScopeAdmin}, KeyAccess{
		Type: KeyTypeUser, Access: acc, Actions: []string{ActionLogsDelete, ActionLogsDelete, " " + ActionLandingFiles},
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.Type != KeyTypeUser || !slices.Equal(key.Actions, []string{ActionLogsDelete, ActionLandingFiles}) {
		t.Fatalf("stored: %+v", key)
	}
	acc.Sections[0] = "changed"
	got, ok := s.Verify(raw, ScopeAdmin)
	if !ok || got.Access == nil || got.Access.Sections[0] != "offers" {
		t.Fatalf("access must not share memory with the caller: %+v", got)
	}

	_, admin, err := s.GenerateWith("a", []string{ScopeAdmin}, KeyAccess{Type: KeyTypeAdmin, RoleID: "r", Access: acc, Actions: AllActions()})
	if err != nil {
		t.Fatal(err)
	}
	if admin.RoleID != "" || admin.Access != nil || len(admin.Actions) != len(AllActions()) {
		t.Fatalf("admin key keeps no role or access: %+v", admin)
	}

	_, withRole, err := s.GenerateWith("r", []string{ScopeAdmin}, KeyAccess{Type: KeyTypeUser, RoleID: " r1 ", Access: &Access{}})
	if err != nil {
		t.Fatal(err)
	}
	if withRole.RoleID != "r1" || withRole.Access != nil {
		t.Fatalf("role with blank access: %+v", withRole)
	}

	_, userLists, err := s.GenerateWith("lists", []string{ScopeAdmin}, KeyAccess{Type: KeyTypeUser, Actions: []string{ActionListsRead}})
	if err != nil || !slices.Equal(userLists.Actions, []string{ActionListsRead}) {
		t.Fatalf("USER lists_read: %+v %v", userLists, err)
	}
}

func TestSetAccess(t *testing.T) {
	s := NewKeySet()
	raw, key, err := s.Generate("k", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.SetAccess(key.ID, KeyAccess{Type: KeyTypeAdmin, Actions: []string{ActionCron}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Hash != key.Hash || updated.Name != "k" || !updated.HasAction(ActionCron) {
		t.Fatalf("updated: %+v", updated)
	}
	if k, ok := s.Verify(raw, ScopeAdmin); !ok || !k.HasAction(ActionCron) {
		t.Fatalf("verify after SetAccess: %+v %v", k, ok)
	}
	if _, err := s.SetAccess(key.ID, KeyAccess{Type: "x"}); !errors.Is(err, ErrAPIKeyType) {
		t.Fatalf("bad type: %v", err)
	}
	if k, _ := s.Get(key.ID); !k.HasAction(ActionCron) {
		t.Fatal("refused SetAccess changed the key")
	}
	if _, err := s.SetAccess("nope", KeyAccess{}); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	s.Revoke(key.ID)
	if _, err := s.SetAccess(key.ID, KeyAccess{}); !errors.Is(err, ErrAPIKeyRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestKeyAccessSaveLoadAndCopies(t *testing.T) {
	s := NewKeySet()
	raw, key, err := s.GenerateWith("k", []string{ScopeAdmin}, KeyAccess{
		Type:    KeyTypeUser,
		Access:  &Access{Sections: []string{"offers"}, Grants: map[string]Grant{"offers": {Mode: ModeCreated}}, Hidden: []string{"profit"}},
		Actions: []string{ActionLogsDelete},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadKeySet(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Verify(raw, ScopeAdmin)
	if !ok || got.Type != KeyTypeUser || got.Access == nil || got.Access.Grants["offers"].Mode != ModeCreated ||
		!slices.Equal(got.Access.Hidden, []string{"profit"}) || !slices.Equal(got.Actions, []string{ActionLogsDelete}) {
		t.Fatalf("round trip: %+v %v", got, ok)
	}

	pub := key.Public()
	pub.Access.Sections[0] = "changed"
	pub.Actions[0] = "changed"
	if pub.Hash != "" {
		t.Fatal("public key keeps the hash")
	}
	again, _ := s.Get(key.ID)
	if again.Access.Sections[0] != "offers" || again.Actions[0] != ActionLogsDelete {
		t.Fatalf("Public shares memory: %+v", again)
	}
	if list := s.List(); len(list) != 1 || list[0].Hash != "" || list[0].Access == nil {
		t.Fatalf("list: %+v", list)
	}
}

func TestValidActions(t *testing.T) {
	out, err := ValidActions(nil)
	if err != nil || out != nil {
		t.Fatalf("empty: %v %v", out, err)
	}
	out, err = ValidActions([]string{" cron ", "cron", "boards"})
	if err != nil || !slices.Equal(out, []string{ActionCron, ActionBoards}) {
		t.Fatalf("trim and dedupe: %v %v", out, err)
	}
	if _, err := ValidActions([]string{"cron", ""}); !errors.Is(err, ErrAPIKeyAction) {
		t.Fatalf("empty name: %v", err)
	}
	all := AllActions()
	all[0] = "changed"
	if AllActions()[0] == "changed" {
		t.Fatal("AllActions shares its list")
	}
}
