package security

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestValidateRoleName(t *testing.T) {
	for _, ok := range []string{"a", "Media buyers", "Закупка", strings.Repeat("я", 64)} {
		if err := ValidateRoleName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", " x", "x ", "a\nb", "a\x00b", strings.Repeat("я", 65)} {
		if err := ValidateRoleName(bad); !errors.Is(err, ErrRoleName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestRoleSetAddUpdateRemove(t *testing.T) {
	set := NewRoleSet()
	a, err := set.Add(Role{Name: "Beta", Access: Access{Sections: []string{"logs"}}})
	if err != nil || a.ID == "" {
		t.Fatalf("add %+v %v", a, err)
	}
	if _, err := set.Add(Role{Name: "beta"}); !errors.Is(err, ErrRoleExists) {
		t.Fatalf("same name in another case: %v", err)
	}
	if _, err := set.Add(Role{Name: " "}); !errors.Is(err, ErrRoleName) {
		t.Fatalf("bad name: %v", err)
	}
	b, err := set.Add(Role{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.List(); len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID {
		t.Fatalf("list is sorted by name: %+v", got)
	}

	if _, err := set.Update(b.ID, func(r *Role) error { r.Name = "Beta"; return nil }); !errors.Is(err, ErrRoleExists) {
		t.Fatalf("rename onto a taken name: %v", err)
	}
	if got, _ := set.Get(b.ID); got.Name != "alpha" {
		t.Fatalf("failed update changed the role: %+v", got)
	}
	if _, err := set.Update(b.ID, func(r *Role) error { r.ID = "other"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Get(b.ID); !ok {
		t.Fatal("update must not change the id")
	}
	wantErr := errors.New("stop")
	if _, err := set.Update(b.ID, func(*Role) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("callback error: %v", err)
	}
	if _, err := set.Update("nope", func(*Role) error { return nil }); !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	if err := set.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := set.Remove(a.ID); !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	if _, ok := set.Get(a.ID); ok {
		t.Fatal("removed role is still found")
	}
}

func TestRoleSetReturnsCopies(t *testing.T) {
	set := NewRoleSet()
	r, _ := set.Add(Role{Name: "x", Access: Access{
		Sections: []string{"logs"},
		Grants:   map[string]Grant{"offers": {Mode: ModeFull}},
		Hidden:   []string{"ip"},
	}})
	got, _ := set.Get(r.ID)
	got.Access.Sections[0] = "changed"
	got.Access.Grants["offers"] = Grant{Mode: ModeRead}
	got.Access.Hidden[0] = "changed"
	again, _ := set.Get(r.ID)
	if again.Access.Sections[0] != "logs" || again.Access.Grants["offers"].Mode != ModeFull || again.Access.Hidden[0] != "ip" {
		t.Fatalf("stored role changed through a copy: %+v", again)
	}
}

func TestRoleSetSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.json")
	set := NewRoleSet()
	a, _ := set.Add(Role{Name: "one", Access: Access{Sections: []string{"logs"}, Hidden: []string{"ip"}}})
	if err := set.Save(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("mode: %v %v", info, err)
		}
	}
	loaded, err := LoadRoleSet(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(a.ID)
	if !ok || !reflect.DeepEqual(got.Access.Sections, []string{"logs"}) || !reflect.DeepEqual(got.Access.Hidden, []string{"ip"}) {
		t.Fatalf("loaded %+v", got)
	}
	if got.Access.Grants == nil {
		t.Error("grants must not be nil after loading")
	}
}

func TestLoadRoleSetMissingFile(t *testing.T) {
	set, err := LoadRoleSet(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || set == nil || len(set.List()) != 0 {
		t.Fatalf("missing file: %v %v", set, err)
	}
}

func TestLoadRoleSetRejectsDamagedFiles(t *testing.T) {
	cases := map[string]string{
		"bad json":      `{`,
		"empty":         ``,
		"no id":         `[{"id":"","name":"a"}]`,
		"repeated id":   `[{"id":"1","name":"a"},{"id":"1","name":"b"}]`,
		"repeated name": `[{"id":"1","name":"a"},{"id":"2","name":"A"}]`,
		"bad name":      `[{"id":"1","name":" a"}]`,
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "roles.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if set, err := LoadRoleSet(path); err == nil || set != nil {
			t.Errorf("%s: loaded %v", name, set)
		}
	}
}

func TestRoleSetReloadKeepsPreviousOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.json")
	set := NewRoleSet()
	r, _ := set.Add(Role{Name: "keep"})
	if err := set.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := set.Reload(path); err == nil {
		t.Fatal("reload of a damaged file must fail")
	}
	if _, ok := set.Get(r.ID); !ok {
		t.Fatal("failed reload dropped the roles")
	}
}

func TestRoleSetConcurrentUse(t *testing.T) {
	set := NewRoleSet()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := set.Add(Role{Name: string(rune('a' + i))})
			if err != nil {
				t.Error(err)
				return
			}
			set.List()
			set.Get(r.ID)
			_, _ = set.Update(r.ID, func(role *Role) error { role.Access.Hidden = []string{"ip"}; return nil })
		}(i)
	}
	wg.Wait()
	if n := len(set.List()); n != 8 {
		t.Fatalf("roles: %d", n)
	}
}

func TestUserCloneCopiesAccess(t *testing.T) {
	u := User{Access: Access{Sections: []string{"logs"}, Grants: map[string]Grant{"offers": {Mode: ModeFull}}, Hidden: []string{"ip"}}}
	c := u.clone()
	c.Access.Sections[0] = "changed"
	c.Access.Grants["offers"] = Grant{Mode: ModeRead}
	c.Access.Hidden[0] = "changed"
	if u.Access.Sections[0] != "logs" || u.Access.Grants["offers"].Mode != ModeFull || u.Access.Hidden[0] != "ip" {
		t.Fatalf("clone shares access: %+v", u.Access)
	}
}
