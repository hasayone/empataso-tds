package admin

import (
	"regexp"
	"strings"
)

var (
	// aiAuthorityRe finds the authority (user, host and port) after "//" in a text: behind a
	// scheme of any kind (the slashes may be backslashes, as a browser reads them), or alone
	// at the start of the text or after a separator, as in //host/path.
	aiAuthorityRe = regexp.MustCompile(`(?i)(?:^|[\s"'=(|,;?&:>])(?:[a-z][a-z0-9+.\-]*:)?[/\\]{2}([^\s/\\?#|"'<>]*)`)
	// aiBareHostRe finds a host with no scheme at the start of a value: a name (labels and a
	// top-level label of letters), an IPv4 address or an IPv6 literal in brackets, then an optional
	// port and a path, a query or the end.
	aiBareHostRe = regexp.MustCompile(`(?i)^((?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}|\d{1,3}(?:\.\d{1,3}){3}|\[[0-9a-f:.]+\])(?::\d+)?(?:[/?#]|$)`)
)

// aiChgURLKeys lists, in lower case, the keys whose values are a URL or a list of URLs split by
// "|||": a bare host in such a value is a host too.
var aiChgURLKeys = map[string]bool{"url": true, "out": true, "postback_url": true, "reserved": true}

// aiChgHostOf returns the lower-case host of an authority: user info, port and a trailing dot
// are cut off, and an IPv6 literal keeps its brackets.
func aiChgHostOf(authority string) string {
	host := authority
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end > 0 {
			host = host[:end+1]
		}
	} else if colon := strings.IndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// aiChgHosts adds every host named by v to into, compared as a whole host. v is a decoded JSON
// value and key the name of the field it is the value of (empty at the top). A string gives the
// authority of every URL in it, with a scheme or protocol-relative (//host/x); under a key of
// aiChgURLKeys it also gives the host of each "|||" part that starts with a bare host.
func aiChgHosts(key string, v any, into map[string]bool) {
	switch x := v.(type) {
	case string:
		for _, m := range aiAuthorityRe.FindAllStringSubmatch(x, -1) {
			if host := aiChgHostOf(m[1]); host != "" {
				into[host] = true
			}
		}
		if aiChgURLKeys[strings.ToLower(key)] {
			for _, part := range strings.Split(x, "|||") {
				if m := aiBareHostRe.FindStringSubmatch(strings.TrimSpace(part)); m != nil {
					into[strings.ToLower(m[1])] = true
				}
			}
		}
	case []any:
		for _, e := range x {
			aiChgHosts(key, e, into)
		}
	case map[string]any:
		for k, e := range x {
			aiChgHosts(k, e, into)
		}
	}
}

// aiChgHostChanged reports whether after, the value of the field key, names a host that before
// does not. Both are decoded JSON values. A host that only disappears is not a change.
func aiChgHostChanged(key string, before, after any) bool {
	had, now := map[string]bool{}, map[string]bool{}
	aiChgHosts(key, before, had)
	aiChgHosts(key, after, now)
	for h := range now {
		if !had[h] {
			return true
		}
	}
	return false
}
