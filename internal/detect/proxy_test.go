package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProxyByOrg(t *testing.T) {
	patterns := DefaultProxyOrg()
	cases := []struct {
		org  string
		want bool
	}{
		{"Hetzner Online GmbH", true},
		{"Cloudflare, Inc.", false},
		{"T-Mobile USA", false},
		{"", false},
		{"-", false},
		{"Google LLC", false},
		{"DigitalOcean, LLC", true},
	}
	for _, c := range cases {
		if got := ProxyByOrg(c.org, patterns); got != c.want {
			t.Errorf("ProxyByOrg(%q) = %v; want %v", c.org, got, c.want)
		}
	}
}

func TestDefaultProxyOrg(t *testing.T) {
	var hetzner, cloud bool
	for _, p := range DefaultProxyOrg() {
		if p == "" {
			t.Fatal("empty pattern")
		}
		if p == "hetzner" {
			hetzner = true
		}
		if p == "cloud" || p == "host" {
			t.Errorf("bare pattern %q", p)
		}
		if p == "cloud" {
			cloud = true
		}
	}
	if !hetzner {
		t.Fatal("built-in list must contain hetzner")
	}
	if cloud {
		t.Fatal("built-in list must not contain cloud")
	}
}

func TestSignaturesProxyOrgFileReplacesBuiltin(t *testing.T) {
	dir := t.TempDir()
	s := NewSignatures(dir, nil)
	if !ProxyByOrg("Hetzner Online GmbH", s.ProxyOrg()) {
		t.Fatal("builtin list should match hetzner")
	}
	body := "# custom\n\ncustomhost\n"
	if err := os.WriteFile(filepath.Join(dir, fileProxyOrg+".dat"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if ProxyByOrg("Hetzner Online GmbH", s.ProxyOrg()) {
		t.Fatal("proxy_org.dat should replace the builtin list")
	}
	if !ProxyByOrg("CustomHost GmbH", s.ProxyOrg()) {
		t.Fatal("file pattern should match")
	}
}
