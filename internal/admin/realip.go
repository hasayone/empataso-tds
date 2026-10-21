package admin

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// parseAdminTrustedProxies parses CIDR lines from KUZTDS_ADMIN_TRUSTED_PROXIES.
// raw is the list from the environment; empty entries are skipped. A bare address becomes /32 or /128.
// When raw yields no prefixes, localhost defaults apply. An invalid entry is an error.
func parseAdminTrustedProxies(raw []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(raw))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		p, err := parseTrustedPrefix(c)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", c, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		for _, d := range []string{"127.0.0.1/32", "::1/128"} {
			p, err := netip.ParsePrefix(d)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// parseTrustedPrefix parses one trusted-proxy entry as a CIDR or a single address.
func parseTrustedPrefix(c string) (netip.Prefix, error) {
	if strings.Contains(c, "/") {
		return netip.ParsePrefix(c)
	}
	a, err := netip.ParseAddr(c)
	if err != nil {
		return netip.Prefix{}, err
	}
	bits := 128
	if a.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(a, bits), nil
}

// peerAddr parses the host part of remoteAddr into an IP address.
// remoteAddr is a host:port pair or a bare address. An invalid value is an invalid Addr.
func peerAddr(remoteAddr string) netip.Addr {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// trustedAddr reports whether a is contained in the server trusted-proxy prefixes.
func (s *Server) trustedAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range s.trustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP returns the visitor address for rate limits and logs.
// When the immediate peer is trusted, the rightmost valid X-Forwarded-For address that is not
// itself trusted is used; CF-Connecting-IP is never read. When the rightmost XFF entry is invalid,
// RemoteAddr is used. Otherwise RemoteAddr is used.
func (s *Server) clientIP(r *http.Request) string {
	peer := peerAddr(r.RemoteAddr)
	if s.trustedAddr(peer) {
		parts := splitXFF(r.Header.Values("X-Forwarded-For"))
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(parts[i])
			if err != nil {
				if i != len(parts)-1 {
					continue
				}
				break
			}
			a = a.Unmap()
			if !s.trustedAddr(a) {
				return a.String()
			}
		}
	}
	if peer.IsValid() {
		return peer.String()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// cookieSecure reports whether the session cookie should carry Secure.
// It is true when CookieSecure is set, the request is TLS, or a trusted peer
// reports https through X-Forwarded-Proto.
func (s *Server) cookieSecure(r *http.Request) bool {
	if s.cfg.CookieSecure || r.TLS != nil {
		return true
	}
	if s.trustedAddr(peerAddr(r.RemoteAddr)) && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	return false
}

// splitXFF flattens X-Forwarded-For header values into trimmed non-empty parts.
func splitXFF(values []string) []string {
	var out []string
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
