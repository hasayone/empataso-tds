package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

func TestResolveEntryFollowsConfigLandingEntry(t *testing.T) {
	cases := []struct {
		entry string
		have  []string
		want  string
		ok    bool
	}{
		{"sub/x.txt", []string{"index.php", "sub/x.txt"}, "sub/x.txt", true},
		{"missing.html", []string{"index.html"}, "missing.html", true},
		{"./missing.html", []string{"index.html"}, "index.html", true},
		{"./missing.html", []string{"index.php", "index.html"}, "index.php", true},
		{"a\\b", []string{"index.html"}, "index.html", true},
		{"", []string{"other.html"}, "", false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		for _, rel := range c.have {
			full := filepath.Join(dir, "7", filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		entry := c.entry
		s := newLandingServer(dir, "", func(string) (config.Landing, bool) {
			return config.Landing{ID: "7", Status: true, Local: true, Entry: entry}, true
		}, nil)
		got, ok := s.resolveEntry("7")
		if got != c.want || ok != c.ok {
			t.Errorf("entry %q over %v: %q, %v; want %q, %v", c.entry, c.have, got, ok, c.want, c.ok)
		}
		want, wantOK := config.LandingEntry(c.entry, func(rel string) bool { return s.fileExists("7", rel) })
		if got != want || ok != wantOK {
			t.Errorf("entry %q: engine %q, %v differs from config.LandingEntry %q, %v", c.entry, got, ok, want, wantOK)
		}
	}
}
