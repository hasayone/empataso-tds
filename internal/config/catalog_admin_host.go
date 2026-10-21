package config

import (
	"net"
	"strings"
)

// NormalizeHTTPHost lowercases host, removes any port, then a trailing dot.
// host is the Host header value or a bare name. There is no error.
func NormalizeHTTPHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

// IsAdminDomain reports whether d may serve the admin panel on admin.<host>.
// The host must not already be an admin subdomain and the row must be enabled with
// primary set or role admin or both.
func IsAdminDomain(d Domain) bool {
	host := NormalizeHTTPHost(d.Host)
	if host == "" || strings.HasPrefix(host, "admin.") || d.Disabled {
		return false
	}
	if d.Primary {
		return true
	}
	switch d.Role {
	case "admin", "both":
		return true
	default:
		return false
	}
}

// AdminHostOf returns admin.<host> when d is an admin domain, otherwise an empty string.
func AdminHostOf(d Domain) string {
	if !IsAdminDomain(d) {
		return ""
	}
	return "admin." + NormalizeHTTPHost(d.Host)
}

// AdminHostAllowed reports whether host is an allowed admin.<base> address.
// list is domains.json. host is normalized the same way as NormalizeHTTPHost.
// When list is nil or host is not admin.*, the result is false.
func AdminHostAllowed(list []Domain, host string) bool {
	host = NormalizeHTTPHost(host)
	if host == "" || !strings.HasPrefix(host, "admin.") {
		return false
	}
	base := strings.TrimPrefix(host, "admin.")
	if base == "" {
		return false
	}
	for _, d := range list {
		if NormalizeHTTPHost(d.Host) == base && IsAdminDomain(d) {
			return true
		}
	}
	return false
}
