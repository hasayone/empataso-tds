package detect

import (
	_ "embed"
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

// ProxyByOrg reports whether org names a hosting, VPN, or proxy network.
// org is the organization string; empty and "-" are not proxies.
// patterns are substrings compared case-insensitively; empty ones are skipped.
// It returns true on the first match and does not report an error.
func ProxyByOrg(org string, patterns []string) bool {
	if org == "" || org == "-" {
		return false
	}
	org = strings.ToLower(org)
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.Contains(org, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
