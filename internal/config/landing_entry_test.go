package config

import (
	"strings"
	"testing"
)

func TestLandingEntryTable(t *testing.T) {
	cases := []struct {
		entry string
		have  map[string]bool
		want  string
		ok    bool
	}{
		{"sub/x.txt", map[string]bool{"index.php": true}, "sub/x.txt", true},
		{" page.html ", nil, "page.html", true},
		{"missing.html", map[string]bool{"index.html": true}, "missing.html", true},
		{"./missing.html", map[string]bool{"index.html": true}, "index.html", true},
		{"./missing.html", map[string]bool{"index.php": true, "index.html": true}, "index.php", true},
		{"../x", map[string]bool{"index.html": true}, "index.html", true},
		{".hidden", map[string]bool{"index.php": true}, "index.php", true},
		{"a\\b", map[string]bool{"index.html": true}, "index.html", true},
		{"/abs", map[string]bool{"index.html": true}, "index.html", true},
		{"a//b.html", nil, "a/b.html", true},
		{"", map[string]bool{"index.html": true}, "index.html", true},
		{"", map[string]bool{"index.php": true, "index.html": true}, "index.php", true},
		{"", map[string]bool{"other.html": true}, "", false},
		{"", nil, "", false},
	}
	for _, c := range cases {
		got, ok := LandingEntry(c.entry, func(rel string) bool { return c.have[rel] })
		if got != c.want || ok != c.ok {
			t.Errorf("LandingEntry(%q, %v) = %q, %v; want %q, %v", c.entry, c.have, got, ok, c.want, c.ok)
		}
	}
}

func TestSafeLandingID(t *testing.T) {
	long := strings.Repeat("a", 129)
	cases := map[string]bool{
		"701": true, "kt-ip.1_a": true, "A9": true, strings.Repeat("a", 128): true,
		"": false, ".": false, "..": false, ".hidden": false, ".unzip-1-2": false,
		"a/b": false, "a b": false, "a\\b": false, "я": false, long: false,
	}
	for id, want := range cases {
		if got := SafeLandingID(id); got != want {
			t.Errorf("SafeLandingID(%q) = %v, want %v", id, got, want)
		}
	}
}
