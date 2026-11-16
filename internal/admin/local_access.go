package admin

import (
	"net"
	"net/http"
	"strings"
)

// localAccess reports whether the admin runs on a local machine.
// It is true when Config.PublicIP is empty and the request Host names this machine or a
// private network. A server with a public address configured is never local, and neither is
// a server reached by a public name or address: a missing public IP there is a setup error.
// r is the incoming request; a nil request is not local.
func (s *Server) localAccess(r *http.Request) bool {
	if r == nil || s.cfg.PublicIP != "" {
		return false
	}
	return localHost(r.Host)
}

// localHost reports whether a Host header value points to a loopback, private or link-local
// address, to localhost, or to a name that cannot be resolved from the internet.
// hostport is the header as sent, with an optional port and optional IPv6 brackets.
// An empty value is not local.
func localHost(hostport string) bool {
	h := strings.TrimSpace(hostport)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return !publicIP(ip)
	}
	if !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}
