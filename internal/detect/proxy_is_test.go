package detect

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// fakeLists is a ListLookup over fixed addresses per list name; it counts relay reads.
type fakeLists struct {
	lists      map[string]map[netip.Addr]bool
	relayReads int
}

// Lookup reports whether ip is in the named fake list.
func (f *fakeLists) Lookup(name string, ip netip.Addr) (string, bool) {
	if name == ListPrivateRelay {
		f.relayReads++
	}
	return name, f.lists[name][ip]
}

func TestIsProxy(t *testing.T) {
	patterns := DefaultProxyOrg()
	proxyIP := netip.MustParseAddr("203.0.113.5")
	relayIP := netip.MustParseAddr("203.0.113.6")
	plainIP := netip.MustParseAddr("203.0.113.7")
	lists := func() *fakeLists {
		return &fakeLists{lists: map[string]map[netip.Addr]bool{
			ListProxy:        {proxyIP: true, relayIP: true},
			ListPrivateRelay: {relayIP: true},
		}}
	}
	cases := []struct {
		name   string
		lists  ListLookup
		ip     netip.Addr
		org    string
		orgAlt string
		want   bool
	}{
		{"ip in proxy list", lists(), proxyIP, "", "", true},
		{"private relay inside proxy list", lists(), relayIP, "", "", false},
		{"private relay with hosting org", lists(), relayIP, "Hetzner Online GmbH", "", false},
		{"org matches", lists(), plainIP, "Hetzner Online GmbH", "", true},
		{"alt org matches", lists(), plainIP, "T-Mobile USA, Inc.", "DigitalOcean, LLC", true},
		{"alt org only", lists(), plainIP, "", "OVH SAS", true},
		{"nothing matches", lists(), plainIP, "Comcast Cable Communications, LLC", "-", false},
		{"nil lists, org matches", nil, plainIP, "Hetzner Online GmbH", "", true},
		{"nil lists, alt matches", nil, plainIP, "", "DigitalOcean, LLC", true},
		{"nil lists, no match", nil, plainIP, "Vodafone GmbH", "", false},
		{"invalid ip", lists(), netip.Addr{}, "", "", false},
		{"T-Mobile", nil, plainIP, "T-Mobile USA, Inc.", "", false},
		{"Comcast", nil, plainIP, "Comcast Cable Communications, LLC", "", false},
		{"Deutsche Telekom", nil, plainIP, "Deutsche Telekom AG", "", false},
		{"MTS", nil, plainIP, "MTS PJSC", "", false},
		{"Jio", nil, plainIP, "Reliance Jio Infocomm Limited", "", false},
		{"Verizon", nil, plainIP, "Verizon Business", "", false},
		{"Vodafone", nil, plainIP, "Vodafone GmbH", "", false},
		{"Hetzner", nil, plainIP, "Hetzner Online GmbH", "", true},
		{"DigitalOcean", nil, plainIP, "DigitalOcean, LLC", "", true},
		{"OVH", nil, plainIP, "OVH SAS", "", true},
	}
	for _, c := range cases {
		if got := IsProxy(c.lists, c.ip, patterns, c.org, c.orgAlt); got != c.want {
			t.Errorf("%s: IsProxy = %v; want %v", c.name, got, c.want)
		}
	}
}

// TestIsProxyReadsRelayAfterHit checks that the relay list is consulted only after a proxy hit.
func TestIsProxyReadsRelayAfterHit(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.7")
	f := &fakeLists{lists: map[string]map[netip.Addr]bool{}}
	if IsProxy(f, ip, DefaultProxyOrg(), "Comcast Cable Communications, LLC", "") || f.relayReads != 0 {
		t.Errorf("a miss must not read the relay list; reads = %d", f.relayReads)
	}
	if !IsProxy(f, ip, DefaultProxyOrg(), "Hetzner Online GmbH", "") || f.relayReads != 1 {
		t.Errorf("a hit reads the relay list once; reads = %d", f.relayReads)
	}
}

func TestLoadProxyOrg(t *testing.T) {
	dir := t.TempDir()
	if got := LoadProxyOrg(dir); len(got) != len(DefaultProxyOrg()) || len(got) == 0 {
		t.Fatalf("a missing file must give the built-in list, got %d", len(got))
	}
	body := "# custom\n\nCustomHost\n  Other Net  \n"
	if err := os.WriteFile(filepath.Join(dir, fileProxyOrg+".dat"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadProxyOrg(dir)
	if len(got) != 2 || got[0] != "customhost" || got[1] != "other net" {
		t.Fatalf("LoadProxyOrg = %q", got)
	}
	if got := NewSignatures(dir, nil).ProxyOrg(); len(got) != 2 || got[0] != "customhost" {
		t.Errorf("Signatures must read the file the same way: %q", got)
	}
	if got := LoadProxyOrg(filepath.Join(dir, "no-such-dir")); len(got) != len(DefaultProxyOrg()) {
		t.Errorf("an unreadable dir must give the built-in list, got %d", len(got))
	}
}
