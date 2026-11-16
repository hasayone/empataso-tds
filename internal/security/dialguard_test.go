package security

import (
	"net"
	"testing"
)

func TestPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",         // loopback
		"10.0.0.1",          // private RFC 1918
		"172.21.0.5",        // private RFC 1918
		"192.168.1.1",       // private RFC 1918
		"100.64.1.1",        // CGNAT
		"198.18.0.5",        // benchmarking
		"0.0.0.1",           // 0.0.0.0/8
		"192.0.0.1",         // 192.0.0.0/24 IETF
		"240.0.0.1",         // 240.0.0.0/4 reserved
		"::1",               // IPv6 loopback
		"::ffff:127.0.0.1",  // IPv4-mapped loopback
		"0.0.0.0",           // unspecified
		"64:ff9b::a00:1",    // NAT64 with embedded 10.0.0.1
	}
	public := []string{
		"203.0.113.10",    // documentation range — stays true per spec
		"8.8.8.8",         // Google DNS
		"64:ff9b::808:808", // NAT64 with embedded 8.8.8.8
		"2001:db8::1",     // documentation IPv6 — global unicast
	}

	for _, addr := range blocked {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Errorf("cannot parse test address %q", addr)
			continue
		}
		if PublicIP(ip) {
			t.Errorf("PublicIP(%q) = true, want false", addr)
		}
	}
	for _, addr := range public {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Errorf("cannot parse test address %q", addr)
			continue
		}
		if !PublicIP(ip) {
			t.Errorf("PublicIP(%q) = false, want true", addr)
		}
	}
}

func TestPublicIPNil(t *testing.T) {
	if PublicIP(nil) {
		t.Error("PublicIP(nil) = true, want false")
	}
}

func TestGuardDial(t *testing.T) {
	blocked := []struct{ addr string }{
		{"127.0.0.1:443"},
		{"10.0.0.1:443"},
		{"172.21.0.5:443"},
		{"100.64.1.1:443"},
		{"[::1]:443"},
		{"[::ffff:127.0.0.1]:443"},
		{"0.0.0.0:443"},
		{"[64:ff9b::a00:1]:443"},
	}
	public := []struct{ addr string }{
		{"203.0.113.10:443"},
		{"[64:ff9b::808:808]:443"},
	}

	for _, c := range blocked {
		if err := GuardDial("tcp", c.addr, nil); err == nil {
			t.Errorf("GuardDial(%q) = nil, want error", c.addr)
		}
	}
	for _, c := range public {
		if err := GuardDial("tcp", c.addr, nil); err != nil {
			t.Errorf("GuardDial(%q) = %v, want nil", c.addr, err)
		}
	}
}
