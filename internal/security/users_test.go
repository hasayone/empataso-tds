package security

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadUserSetMissing(t *testing.T) {
	s, err := LoadUserSet(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Fatalf("expected empty set, got %d", s.Len())
	}
	list := s.List()
	if list == nil || len(list) != 0 {
		t.Fatalf("List should be empty non-nil slice, got %v", list)
	}
}

func TestLoadUserSetBadJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserSet(p); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestLoadUserSetBadHash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "users.json")
	body := `[{"id":"1","login":"a","type":"USER","enabled":true,"hash":"not-a-hash","access":{"sections":[],"grants":{}},"created_at":"2026-01-01T00:00:00Z"}]`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserSet(p); err == nil {
		t.Fatal("expected hash validation error")
	}
}

func TestUserSetAddUpdateRemove(t *testing.T) {
	s := NewUserSet()
	hash, err := HashPassword("secret1")
	if err != nil {
		t.Fatal(err)
	}
	u := User{
		Login: "bob", Type: UserRegular, Enabled: true, Hash: hash,
		Access: Access{Sections: []string{"stats"}, Grants: map[string]Grant{"stats": {Mode: ModeRead}}},
	}
	if err := s.Add(u); err != nil {
		t.Fatal(err)
	}
	got, ok := s.ByLogin("BOB")
	if !ok || got.Login != "bob" {
		t.Fatalf("ByLogin: %+v ok=%v", got, ok)
	}
	pub := s.List()[0]
	if pub.Hash != "" {
		t.Error("List must not expose hash")
	}
	if err := s.Add(u); err != ErrUserExists {
		t.Fatalf("duplicate login: %v", err)
	}
	if err := s.Update(got.ID, func(u *User) error {
		u.Login = "bobby"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("missing"); err != ErrUserNotFound {
		t.Fatalf("Remove missing: %v", err)
	}
	if err := s.Remove(got.ID); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Fatalf("expected empty after remove, got %d", s.Len())
	}
}

func TestUserSetSaveMode(t *testing.T) {
	s := NewUserSet()
	hash, _ := HashPassword("x")
	_ = s.Add(User{
		Login: "a", Type: UserRegular, Enabled: true, Hash: hash,
		Access: Access{Grants: map[string]Grant{}},
	})
	p := filepath.Join(t.TempDir(), "users.json")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"hash"`) {
		t.Error("file must store password hashes")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("file mode want 0600, got %o", st.Mode().Perm())
		}
	}
}

func TestUserFingerprintDisabled(t *testing.T) {
	hash, _ := HashPassword("p")
	u := User{Enabled: false, Hash: hash}
	if u.Fingerprint() != "" {
		t.Error("disabled user must have empty fingerprint")
	}
}

func TestValidateLogin(t *testing.T) {
	cases := []struct {
		login string
		ok    bool
	}{
		{"", false},
		{" ab", false},
		{"ab ", false},
		{"a", true},
		{strings.Repeat("x", 65), false},
		{"apikey:abc", false},
		{"user name", false},
		{"ok_login", true},
	}
	for _, c := range cases {
		err := ValidateLogin(c.login)
		if c.ok && err != nil {
			t.Errorf("%q: want ok, got %v", c.login, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: want error", c.login)
		}
	}
}

func TestDummyHashValid(t *testing.T) {
	if err := ValidateHash(DummyHash); err != nil {
		t.Fatalf("DummyHash invalid: %v", err)
	}
	if VerifyPassword("", DummyHash) {
		t.Error("empty password must not verify against DummyHash")
	}
}

func TestReloadKeepsSetOnError(t *testing.T) {
	s := NewUserSet()
	_ = s.Add(User{Login: "x", Type: UserRegular, Enabled: true, Access: Access{Grants: map[string]Grant{}}})
	path := filepath.Join(t.TempDir(), "users.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(path); err == nil {
		t.Fatal("expected reload error")
	}
	if s.Len() != 1 {
		t.Fatalf("set must be unchanged, len=%d", s.Len())
	}
}

func TestErrLastAdminUpdate(t *testing.T) {
	s := NewUserSet()
	hash, _ := HashPassword("p")
	_ = s.Add(User{Login: "a", Type: UserAdmin, Enabled: true, Hash: hash, Access: Access{Grants: map[string]Grant{}}})
	u, _ := s.ByLogin("a")
	if err := s.Update(u.ID, func(u *User) error {
		u.Type = UserRegular
		return nil
	}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
	if err := s.Update(u.ID, func(u *User) error {
		u.Enabled = false
		return nil
	}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disable last admin: %v", err)
	}
	_ = s.Add(User{Login: "b", Type: UserAdmin, Enabled: true, Hash: hash, Access: Access{Grants: map[string]Grant{}}})
	if err := s.Update(u.ID, func(u *User) error {
		u.Type = UserRegular
		return nil
	}); err != nil {
		t.Fatalf("demote with backup: %v", err)
	}
}

func TestErrLastAdminRemove(t *testing.T) {
	s := NewUserSet()
	hash, _ := HashPassword("p")
	_ = s.Add(User{Login: "a", Type: UserAdmin, Enabled: true, Hash: hash, Access: Access{Grants: map[string]Grant{}}})
	u, _ := s.ByLogin("a")
	if err := s.Remove(u.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("remove last admin: %v", err)
	}
	_ = s.Add(User{Login: "b", Type: UserAdmin, Enabled: true, Hash: hash, Access: Access{Grants: map[string]Grant{}}})
	if err := s.Remove(u.ID); err != nil {
		t.Fatalf("remove with backup: %v", err)
	}
}

func TestOutsideAdminLetsRemoveLastFileAdmin(t *testing.T) {
	s := NewUserSet()
	hash, _ := HashPassword("p")
	if err := s.Add(User{Login: "a", Type: UserAdmin, Enabled: true, Hash: hash, Access: Access{Grants: map[string]Grant{}}}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.ByLogin("a")
	if err := s.Remove(u.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("remove last file admin: %v", err)
	}
	s.SetOutsideAdmins(1)
	if err := s.Remove(u.ID); err != nil {
		t.Fatalf("remove with outside admin: %v", err)
	}
}

func TestUserCreatedAtDefault(t *testing.T) {
	s := NewUserSet()
	before := time.Now().UTC()
	if err := s.Add(User{Login: "t", Type: UserRegular, Enabled: true, Access: Access{Grants: map[string]Grant{}}}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.ByLogin("t")
	if u.CreatedAt.Before(before) {
		t.Error("CreatedAt should be set on Add")
	}
}
