package main

import (
	"net/url"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

// withOrigin appends a path token to a redirect that stays on this tracker.
// location is the Location value after macros. token is the path token of this
// click. A relative address, a protocol-relative address and an address whose
// first path segment is not an active campaign are returned unchanged.
// A host that is not in the domain catalog (a disabled row still counts) is
// returned unchanged and counted as unknown. A query that already carries a
// valid path token is returned unchanged. An invalid origin pair is replaced.
// The address is edited as text: other pairs and the fragment stay where they
// are. It does not fail.
func (d *engineDeps) withOrigin(location, token string) string {
	if d == nil || token == "" {
		return location
	}
	body, frag := location, ""
	if i := strings.IndexByte(location, '#'); i >= 0 {
		body, frag = location[:i], location[i:]
	}
	if !strings.HasPrefix(body, "http://") && !strings.HasPrefix(body, "https://") {
		return location
	}
	u, err := url.Parse(body)
	if err != nil || u.Host == "" {
		return location
	}
	seg := firstCampaignSegment(u.Path)
	if seg == "" || d.groups == nil {
		return location
	}
	g, ok := d.groups.Get(seg)
	if !ok || g == nil || !g.Status {
		return location
	}
	if !d.ownHost(config.NormalizeHTTPHost(u.Host)) {
		d.stats.originLink.unknownHost.Add(1)
		return location
	}
	if d.hasLivePathToken(u.Query(), time.Now()) {
		return location
	}
	d.stats.originLink.added.Add(1)
	return setOriginQuery(body, token) + frag
}

// ownHost reports whether host is a row of the domain catalog.
// host is already normalized. A disabled row still counts. A nil book does not.
func (d *engineDeps) ownHost(host string) bool {
	if d == nil || d.domains == nil || host == "" {
		return false
	}
	for _, row := range d.domains.ByID() {
		if config.NormalizeHTTPHost(row.Host) == host {
			return true
		}
	}
	return false
}

// hasLivePathToken reports whether any origin value is a path token that
// verifies at now. q is the parsed query of the redirect. It does not fail.
func (d *engineDeps) hasLivePathToken(q url.Values, now time.Time) bool {
	if d == nil || d.cids == nil {
		return false
	}
	for _, v := range q["origin"] {
		if _, _, _, ok := d.cids.verifyPath(strings.TrimSpace(v), now); ok {
			return true
		}
	}
	return false
}

// firstCampaignSegment returns the first path segment, or "" for an empty path.
// p is the URL path. It does not fail.
func firstCampaignSegment(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// setOriginQuery writes origin=token into the query of body.
// body has no fragment. An existing origin pair is replaced in place; otherwise
// the pair is appended. Other pairs are not re-encoded. It does not fail.
func setOriginQuery(body, token string) string {
	pair := "origin=" + url.QueryEscape(token)
	q := strings.IndexByte(body, '?')
	if q < 0 {
		return body + "?" + pair
	}
	rest := body[q+1:]
	if rest == "" {
		return body + pair
	}
	pos := q + 1
	for rest != "" {
		end := strings.IndexByte(rest, '&')
		one := rest
		next := ""
		if end >= 0 {
			one = rest[:end]
			next = rest[end+1:]
		}
		name, _, _ := strings.Cut(one, "=")
		if name == "origin" {
			return body[:pos] + pair + body[pos+len(one):]
		}
		pos += len(one)
		if end >= 0 {
			pos++
		}
		rest = next
	}
	return body + "&" + pair
}
