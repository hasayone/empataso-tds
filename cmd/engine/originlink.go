package main

import (
	"net/url"
	"strconv"
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

// carryClickParams adds the collected click values to a redirect that stays on
// this tracker, under the link names of the campaign that received them.
//
// location is the Location value after the path token is applied. grp is the
// campaign the visitor entered; its parameter map names the query keys, and
// the placeholders stored for its link are not written. keyword, subs and
// extras are the values collected for this click. A relative address, a foreign
// host and an address whose first path segment is not an active campaign are
// returned unchanged. An empty value is not written. A query key that is
// already present is left as it is, including when its value is empty, and
// origin is never replaced. The fragment stays where it is. It does not fail.
func (d *engineDeps) carryClickParams(location string, grp *config.Group, keyword string, subs [config.MaxSubID]string, extras config.Extras) string {
	if d == nil || grp == nil || location == "" {
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
		return location
	}
	have, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return location
	}
	src, _ := d.sourceOf(grp)
	src = config.EffectiveSource(grp, src)
	seen := make(map[string]struct{}, len(have))
	for name := range have {
		seen[name] = struct{}{}
	}
	add := make([]string, 0, 4)
	put := func(field, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		name := clickLinkName(src, field)
		if name == "" || name == "origin" {
			return
		}
		if _, taken := seen[name]; taken {
			return
		}
		seen[name] = struct{}{}
		add = append(add, url.QueryEscape(name)+"="+url.QueryEscape(value))
	}
	put("keyword", keyword)
	for i := 0; i < config.MaxSubID; i++ {
		put("sub_id_"+strconv.Itoa(i+1), subs[i])
	}
	put("site", extras.Site)
	put("ad_campaign_id", extras.AdCampaignID)
	put("creative_id", extras.CreativeID)
	put("external_id", extras.ExternalID)
	if len(add) == 0 {
		return location
	}
	joined := strings.Join(add, "&")
	switch {
	case !strings.Contains(body, "?"):
		body += "?" + joined
	case strings.HasSuffix(body, "?") || strings.HasSuffix(body, "&"):
		body += joined
	default:
		body += "&" + joined
	}
	return body + frag
}

// clickLinkName is the query key a campaign writes for one collected field.
//
// src is the campaign's effective traffic source and may be nil. field is
// keyword, sub_id_N or an extra click field. A non-empty mapped name is the
// key; otherwise field itself is the key. Link placeholders are not read.
// There is no error.
func clickLinkName(src *config.TrafficSource, field string) string {
	if src != nil {
		if mapped := strings.TrimSpace(src.Parameters[field]); mapped != "" {
			return mapped
		}
	}
	return field
}
