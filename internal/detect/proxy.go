package detect

import (
	_ "embed"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed proxy_org.dat
var proxyOrgEmbed string

var (
	proxyOrgOnce     sync.Once
	proxyOrgEmbedded []string
)

// DefaultProxyOrg returns the built-in hosting and VPN organization substrings.
// Blank lines and lines starting with # in the embedded file are dropped, and
// the rest are lowercased. The slice is computed once and must not be mutated.
// It does not report an error.
func DefaultProxyOrg() []string {
	proxyOrgOnce.Do(func() {
		for _, line := range strings.Split(proxyOrgEmbed, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			proxyOrgEmbedded = append(proxyOrgEmbedded, strings.ToLower(line))
		}
	})
	return proxyOrgEmbedded
}

// ListLookup is the IP-list lookup IsProxy needs; *ipindex.Set satisfies it.
type ListLookup interface {
	Lookup(name string, ip netip.Addr) (string, bool)
}

// Names of the IP lists IsProxy reads.
const (
	ListProxy        = "ip_proxy"
	ListPrivateRelay = "ip_private_relay"
)

// IsProxy reports whether ip is a proxy, VPN or hosting address.
// The address counts when it is in the ListProxy list, or org or orgAlt matches
// a substring in patterns (see ProxyByOrg); an address that is also in the
// ListPrivateRelay list never counts, because iCloud Private Relay carries real
// phones. The relay list is read only after a hit.
// lists may be nil, then only the organization names decide. It does not allocate.
func IsProxy(lists ListLookup, ip netip.Addr, patterns []string, org, orgAlt string) bool {
	hit := false
	if lists != nil {
		_, hit = lists.Lookup(ListProxy, ip)
	}
	if !hit {
		hit = ProxyByOrg(org, patterns) || ProxyByOrg(orgAlt, patterns)
	}
	if !hit {
		return false
	}
	if lists != nil {
		if _, relay := lists.Lookup(ListPrivateRelay, ip); relay {
			return false
		}
	}
	return true
}

// readProxyOrgFile reads a proxy_org.dat file: lines are trimmed and lowercased,
// blank lines and # comments are dropped. It is the one reader shared by
// LoadProxyOrg and Signatures. The error is a file open or read failure.
func readProxyOrgFile(path string) ([]string, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	for i, line := range lines {
		lines[i] = strings.ToLower(line)
	}
	if lines == nil {
		lines = []string{}
	}
	return lines, nil
}

// LoadProxyOrg reads dir/proxy_org.dat the way Signatures does. A missing or
// unreadable file gives DefaultProxyOrg; an existing file with no lines gives an
// empty list. It does not report an error.
func LoadProxyOrg(dir string) []string {
	lines, err := readProxyOrgFile(filepath.Join(dir, fileProxyOrg+".dat"))
	if err != nil {
		return DefaultProxyOrg()
	}
	return lines
}

// ProxyByOrg reports whether org names a hosting, VPN, or proxy network.
// org is the organization string; empty and "-" are not proxies.
// patterns are substrings compared case-insensitively over ASCII letters; empty
// ones are skipped. Non-ASCII bytes are compared as they are. It returns true on
// the first match, does not allocate and does not report an error.
func ProxyByOrg(org string, patterns []string) bool {
	if org == "" || org == "-" {
		return false
	}
	for _, p := range patterns {
		if p != "" && containsASCIIFold(org, p) {
			return true
		}
	}
	return false
}

// foldASCII lowercases an ASCII capital and leaves every other byte as it is.
func foldASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// containsASCIIFold reports whether s contains the non-empty substr, ignoring the
// case of ASCII letters on both sides. It does not allocate.
func containsASCIIFold(s, substr string) bool {
	first := foldASCII(substr[0])
	for i := 0; i+len(substr) <= len(s); i++ {
		if foldASCII(s[i]) != first {
			continue
		}
		j := 1
		for j < len(substr) && foldASCII(s[i+j]) == foldASCII(substr[j]) {
			j++
		}
		if j == len(substr) {
			return true
		}
	}
	return false
}
