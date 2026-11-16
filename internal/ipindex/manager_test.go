package ipindex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDat(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name+".dat")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetLoadAndLookup(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, dir, "ip_google", "66.249.64.0/19\n")
	writeDat(t, dir, "ip_blacklist", "10.0.0.0/8\n")

	s := NewSet(dir, nil)
	s.Load("ip_google", "ip_blacklist", "ip_missing") // ip_missing absent -> empty

	if _, ok := s.Lookup("ip_google", mustAddr("66.249.70.1")); !ok {
		t.Error("ip_google: expected a hit")
	}
	if _, ok := s.Lookup("ip_blacklist", mustAddr("10.1.2.3")); !ok {
		t.Error("ip_blacklist: expected a hit")
	}
	if _, ok := s.Lookup("ip_missing", mustAddr("1.2.3.4")); ok {
		t.Error("ip_missing: there should be no hits")
	}
	if _, ok := s.Lookup("nonexistent", mustAddr("1.2.3.4")); ok {
		t.Error("an unknown list must be empty")
	}
}

func TestSetReloadOnChange(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, dir, "ip_others", "1.1.1.0/24\n")

	s := NewSet(dir, nil)
	s.Load("ip_others")

	if _, ok := s.Lookup("ip_others", mustAddr("2.2.2.2")); ok {
		t.Fatal("2.2.2.2 must not match the original list")
	}

	// Change the file and force a newer mtime (deterministically).
	p := writeDat(t, dir, "ip_others", "1.1.1.0/24\n2.2.2.0/24\n")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}

	changed := s.Reload()
	if len(changed) != 1 || changed[0] != "ip_others" {
		t.Fatalf("expected reload of ip_others, got %v", changed)
	}
	if _, ok := s.Lookup("ip_others", mustAddr("2.2.2.2")); !ok {
		t.Error("after reload 2.2.2.2 must match")
	}

	// A repeated reload without changes — nothing is re-read.
	if changed := s.Reload(); len(changed) != 0 {
		t.Errorf("an unchanged file must not be re-read, got %v", changed)
	}
}

// TestSetLen checks the range count of a loaded, an empty, a missing and an unknown list, and that Len does not allocate.
func TestSetLen(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, dir, "ip_two", "10.0.0.0/8\n192.168.0.0/16\n")
	writeDat(t, dir, "ip_empty", "")

	s := NewSet(dir, nil)
	s.Load("ip_two", "ip_empty", "ip_missing")

	for name, want := range map[string]int{"ip_two": 2, "ip_empty": 0, "ip_missing": 0, "nonexistent": 0} {
		if got := s.Len(name); got != want {
			t.Errorf("Len(%q) = %d, want %d", name, got, want)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() { s.Len("ip_two"); s.Len("nonexistent") }); allocs != 0 {
		t.Errorf("Len allocates %v times per call", allocs)
	}
	writeDat(t, dir, "ip_empty", "172.16.0.0/12\n")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "ip_empty.dat"), future, future); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if got := s.Len("ip_empty"); got != 1 {
		t.Errorf("Len after reload = %d, want 1", got)
	}
}

// TestNilSetReadsAreEmpty checks that the reading methods of a nil Set answer as an empty set.
func TestNilSetReadsAreEmpty(t *testing.T) {
	var s *Set
	if label, ok := s.Lookup("ip_google", mustAddr("66.249.66.1")); ok || label != "" {
		t.Errorf("Lookup = %q, %v, want empty", label, ok)
	}
	if got := s.Len("ip_google"); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
	if got := s.Get("ip_google"); got == nil || got.Len() != 0 {
		t.Errorf("Get = %v, want an empty index", got)
	}
	if changed := s.Reload(); len(changed) != 0 {
		t.Errorf("Reload = %v, want none", changed)
	}
}
