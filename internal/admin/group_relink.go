package admin

import (
	"net/url"
	"slices"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// relinkKinds are the redirect types whose out is one destination address.
var relinkKinds = map[string]bool{
	"http_redirect":   true,
	"meta_refresh":    true,
	"js_redirect":     true,
	"iframe_redirect": true,
}

// trackerCampaignKey returns the campaign key of raw when raw is a plain campaign link on the old tracker.
// raw is a destination address, host the lower-case host of the tracker (see keitaroHost).
// A plain link is http or https on that host with exactly one path segment: https://host/<key>?query.
// It returns an empty string for any other address, for a list of addresses split by "|||",
// and for an address with a macro in the host that does not parse.
func trackerCampaignKey(raw, host string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || host == "" || strings.Contains(raw, "|||") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.ToLower(u.Hostname()) != host {
		return ""
	}
	key := strings.Trim(u.Path, "/")
	if key == "" || strings.Contains(key, "/") {
		return ""
	}
	return key
}

// relinkTrackerOuts sends the destinations of g that point at a campaign link of the old tracker
// to the same campaign in this panel, as a link of type "group".
// Only a destination that really leads the click is changed: the default of the campaign and the
// output of a stream without offers and landings, because a stream with them serves the catalog
// address and keeps its out as a spare.
// all is the candidate list the key is looked up in, by id or alias. A target counts when it is
// another campaign of the same section that v may see, so the saved link passes checkLinks.
// host is the tracker host; an empty host changes nothing.
// The streams slice of g is copied before the first change, so a slice shared with the stored
// list stays as it was. It reports whether anything changed.
func relinkTrackerOuts(v viewer, g *config.Group, all []config.Group, host string) bool {
	target := func(raw string) (string, bool) {
		key := trackerCampaignKey(raw, host)
		if key == "" {
			return "", false
		}
		for _, t := range all {
			if t.ID == g.ID || !config.SameBoard(t.BoardID, g.BoardID) || !v.canSee(resCampaigns, campaignRef(t)) {
				continue
			}
			if t.ID == key || slices.Contains(t.Aliases, key) {
				return t.ID, true
			}
		}
		return "", false
	}
	changed, copied := false, false
	if relinkKinds[g.Redirect] {
		if id, ok := target(g.Out); ok {
			g.Redirect, g.Out = "group", id
			changed = true
		}
	}
	for i := range g.Streams {
		out := g.Streams[i].Out
		if !relinkKinds[out.Redirect] || len(out.Offers) > 0 || len(out.Landings) > 0 {
			continue
		}
		id, ok := target(out.Out)
		if !ok {
			continue
		}
		if !copied {
			g.Streams = append([]config.Stream(nil), g.Streams...)
			copied = true
		}
		g.Streams[i].Out.Redirect, g.Streams[i].Out.Out = "group", id
		g.Streams[i].Out.OutShares = nil
		changed = true
	}
	return changed
}

// relinkNewCampaigns runs relinkTrackerOuts on every campaign of groups whose id is not in stored,
// the rows a create or a copy adds. A campaign that already exists is never touched, so other
// campaigns and their destinations stay as they were saved.
// v is the caller, stored the list before the write, groups the candidate list, updated in place.
// host is the tracker host. It returns the changed rows.
func relinkNewCampaigns(v viewer, stored, groups []config.Group, host string) []config.Group {
	if host == "" {
		return nil
	}
	known := make(map[string]struct{}, len(stored))
	for _, g := range stored {
		known[g.ID] = struct{}{}
	}
	var changed []config.Group
	for i := range groups {
		if _, ok := known[groups[i].ID]; ok {
			continue
		}
		if relinkTrackerOuts(v, &groups[i], groups, host) {
			changed = append(changed, groups[i])
		}
	}
	return changed
}
