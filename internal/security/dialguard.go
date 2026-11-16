package security

import (
	"errors"
	"net"
	"syscall"
)

// ErrPrivateAddr is returned by GuardDial when the dialed address is not a public IP.
var ErrPrivateAddr = errors.New("private address")

// reservedNets lists IP ranges that net.IP.IsPrivate does not cover.
// 100.64.0.0/10 is CGNAT. 198.18.0.0/15 is benchmarking.
// 0.0.0.0/8 is "this" network. 192.0.0.0/24 is IETF protocol assignments.
// 240.0.0.0/4 is reserved for future use.
var reservedNets = []net.IPNet{
	{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)},
	{IP: net.IPv4(198, 18, 0, 0).To4(), Mask: net.CIDRMask(15, 32)},
	{IP: net.IPv4(0, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(192, 0, 0, 0).To4(), Mask: net.CIDRMask(24, 32)},
	{IP: net.IPv4(240, 0, 0, 0).To4(), Mask: net.CIDRMask(4, 32)},
}

// nat64Prefix is the well-known NAT64 prefix 64:ff9b::/96.
var nat64Prefix *net.IPNet

func init() {
	_, nat64Prefix, _ = net.ParseCIDR("64:ff9b::/96")
}

// PublicIP reports whether ip is a global unicast address that is not private.
// A nil, loopback, link-local, unspecified, multicast or private address returns false.
// CGNAT 100.64.0.0/10, benchmarking 198.18.0.0/15, "this" network 0.0.0.0/8,
// IETF protocol assignments 192.0.0.0/24, and reserved 240.0.0.0/4 are also false.
// Documentation ranges such as 203.0.113.0/24 remain true.
// For an address in the NAT64 well-known prefix 64:ff9b::/96 the embedded IPv4
// (the last 4 bytes) is checked with the same rules.
func PublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// 64:ff9b::/96: check the embedded IPv4 in bytes 12–15.
	if len(ip) == net.IPv6len && nat64Prefix.Contains(ip) {
		embedded := make(net.IP, net.IPv4len)
		copy(embedded, ip[12:])
		return PublicIP(embedded)
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	// Normalise IPv4-mapped IPv6 to 4 bytes before checking extended ranges.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// GuardDial rejects a connection to a non-public address.
// network is the dial network. address is host:port with an IP as the host.
// c is the raw connection; it is not used.
// It returns ErrPrivateAddr when the address is missing, not an IP, or not a public IP.
func GuardDial(network, address string, c syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !PublicIP(net.ParseIP(host)) {
		return ErrPrivateAddr
	}
	return nil
}
