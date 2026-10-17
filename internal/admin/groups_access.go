package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// campaignLinkKinds lists grant resources validated on campaign save links.
var campaignLinkKinds = []string{resOffers, resLandings, resDomains, resSources, resCampaigns}

// unknownLinkNoun maps a grant resource to the singular word in 400 link errors.
var unknownLinkNoun = map[string]string{
	resOffers:    "offer",
	resLandings:  "landing",
	resDomains:   "domain",
	resSources:   "source",
	resCampaigns: "campaign",
}

// mergeGroups combines stored campaigns with the request body under viewer rules.
// It returns the merged slice, or a non-zero HTTP status and message on denial.
func mergeGroups(v viewer, stored, body []config.Group) ([]config.Group, int, string) {
	if v.admin {
		byID := map[string]config.Group{}
		for _, g := range stored {
			byID[g.ID] = g
		}
		out := append([]config.Group(nil), body...)
		for i := range out {
			if old, ok := byID[out[i].ID]; ok {
				if out[i].CreatedBy == "" {
					out[i].CreatedBy = old.CreatedBy
				}
			} else {
				out[i].CreatedBy = v.uid
			}
		}
		return out, 0, ""
	}
	bodyByID := map[string]config.Group{}
	for _, g := range body {
		bodyByID[g.ID] = g
	}
	storedByID := map[string]config.Group{}
	for _, g := range stored {
		storedByID[g.ID] = g
	}
	var out []config.Group
	for _, old := range stored {
		ref := campaignRef(old)
		if !v.canSee(resCampaigns, ref) {
			out = append(out, old)
			if g, ok := bodyByID[old.ID]; ok && !groupsEqual(old, g) {
				return nil, http.StatusForbidden, "foreign campaign: " + old.ID
			}
			continue
		}
		g, inBody := bodyByID[old.ID]
		if !inBody {
			if v.canWrite(resCampaigns, ref) {
				continue
			}
			return nil, http.StatusForbidden, "read-only campaign"
		}
		if !groupsEqual(old, g) {
			if !v.canWrite(resCampaigns, ref) {
				return nil, http.StatusForbidden, "read-only campaign: " + old.ID
			}
			g.CreatedBy = old.CreatedBy
			out = append(out, g)
		} else {
			out = append(out, old)
		}
	}
	for _, g := range body {
		if _, was := storedByID[g.ID]; was {
			continue
		}
		if !v.canCreate(resCampaigns) {
			return nil, http.StatusForbidden, "cannot create campaigns"
		}
		g.CreatedBy = v.uid
		out = append(out, g)
	}
	return out, 0, ""
}

// campaignRef builds an access ref for one campaign row.
func campaignRef(g config.Group) ref {
	return ref{id: g.ID, label: g.Folder, owner: g.CreatedBy}
}

// groupsEqual reports whether a and b serialize to the same JSON.
func groupsEqual(a, b config.Group) bool {
	ba, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(ba, bb)
}

// linkCatalog maps grant resource names to catalog id→ref rows for link checks.
type linkCatalog map[string]map[string]ref

// loadLinkCatalog reads catalog rows from disk and indexes campaigns from groups.
// groups supplies campaign ids and aliases. Missing stores leave empty maps.
func (s *Server) loadLinkCatalog(ctx context.Context, groups []config.Group) linkCatalog {
	cat := linkCatalog{}
	for _, res := range campaignLinkKinds {
		cat[res] = map[string]ref{}
	}
	if s.cfg.Offers != nil {
		if list, err := s.cfg.Offers.List(ctx); err == nil {
			for _, o := range list {
				cat[resOffers][o.ID] = ref{id: o.ID, label: o.Group, owner: o.CreatedBy}
			}
		}
	}
	if s.cfg.Landings != nil {
		if list, err := s.cfg.Landings.List(ctx); err == nil {
			for _, l := range list {
				cat[resLandings][l.ID] = ref{id: l.ID, label: l.Group, owner: l.CreatedBy}
			}
		}
	}
	if s.cfg.Domains != nil {
		if list, err := s.cfg.Domains.List(ctx); err == nil {
			for _, d := range list {
				cat[resDomains][d.ID] = ref{id: d.ID, label: d.Group, owner: d.CreatedBy}
			}
		}
	}
	if s.cfg.Sources != nil {
		if list, err := s.cfg.Sources.List(ctx); err == nil {
			for _, src := range list {
				cat[resSources][src.ID] = ref{id: src.ID, label: src.Group, owner: src.CreatedBy}
			}
		}
	}
	for _, g := range groups {
		r := campaignRef(g)
		cat[resCampaigns][g.ID] = r
		for _, a := range g.Aliases {
			if a != "" {
				cat[resCampaigns][a] = r
			}
		}
	}
	return cat
}

// groupLinks maps grant resource names to linked entity ids on one campaign.
type groupLinks map[string]map[string]struct{}

// newGroupLinks returns empty link sets for every campaign link resource.
func newGroupLinks() groupLinks {
	m := groupLinks{}
	for _, res := range campaignLinkKinds {
		m[res] = map[string]struct{}{}
	}
	return m
}

// collectGroupLinks gathers offer, landing, domain, source and campaign ids referenced by g.
func collectGroupLinks(g config.Group) groupLinks {
	links := newGroupLinks()
	add := func(res, id string) {
		if id == "" {
			return
		}
		links[res][id] = struct{}{}
	}
	add(resDomains, g.DomainID)
	add(resSources, g.SourceID)
	if g.Redirect == "group" {
		add(resCampaigns, strings.TrimSpace(g.Out))
	}
	for _, st := range g.Streams {
		for _, o := range st.Out.Offers {
			add(resOffers, o.ID)
		}
		for _, l := range st.Out.Landings {
			add(resLandings, l.ID)
		}
		if st.Out.Redirect == "group" {
			add(resCampaigns, strings.TrimSpace(st.Out.Out))
		}
	}
	return links
}

// linkAdded reports whether id is linked in links but not in old for res.
func linkAdded(links, old groupLinks, res, id string) bool {
	if id == "" {
		return false
	}
	if _, inNew := links[res][id]; !inNew {
		return false
	}
	if _, was := old[res][id]; was {
		return false
	}
	return true
}

// checkLinks validates new references in g that were not present in old.
// It returns an empty string when every link is allowed, otherwise a 400 message.
func checkLinks(v viewer, g config.Group, old *config.Group, cat linkCatalog) string {
	if v.admin {
		return ""
	}
	oldLinks := newGroupLinks()
	if old != nil {
		oldLinks = collectGroupLinks(*old)
	}
	newLinks := collectGroupLinks(g)
	for _, res := range campaignLinkKinds {
		for id := range newLinks[res] {
			if !linkAdded(newLinks, oldLinks, res, id) {
				continue
			}
			r, ok := cat[res][id]
			if !ok || !v.canSee(res, r) {
				return "unknown " + unknownLinkNoun[res] + ": " + id
			}
		}
	}
	return ""
}

// checkCampaignAliases rejects alias/id clashes introduced by new or changed groups.
func checkCampaignAliases(v viewer, stored, merged []config.Group) string {
	if v.admin {
		return ""
	}
	storedByID := map[string]config.Group{}
	for _, g := range stored {
		storedByID[g.ID] = g
	}
	taken := map[string]string{}
	for _, g := range merged {
		taken[g.ID] = g.ID
		for _, a := range g.Aliases {
			if a != "" {
				taken[a] = g.ID
			}
		}
	}
	for _, g := range merged {
		old, had := storedByID[g.ID]
		if had && groupsEqual(old, g) {
			continue
		}
		oldAlias := map[string]struct{}{}
		if had {
			for _, a := range old.Aliases {
				if a != "" {
					oldAlias[a] = struct{}{}
				}
			}
		}
		for _, a := range g.Aliases {
			if a == "" {
				continue
			}
			if _, was := oldAlias[a]; was {
				continue
			}
			if owner, ok := taken[a]; ok && owner != g.ID {
				return "alias taken: " + a
			}
		}
	}
	return ""
}

// validateUserGroupLinks runs checkLinks and alias checks on changed merged campaigns.
// stored is the file before merge; merged is the candidate save payload.
// It returns the first 400/validation message, or an empty string.
func validateUserGroupLinks(v viewer, stored, merged []config.Group, cat linkCatalog) string {
	storedByID := map[string]config.Group{}
	for _, g := range stored {
		storedByID[g.ID] = g
	}
	for _, g := range merged {
		old, had := storedByID[g.ID]
		if had && groupsEqual(old, g) {
			continue
		}
		var oldPtr *config.Group
		if had {
			o := old
			oldPtr = &o
		}
		if msg := checkLinks(v, g, oldPtr, cat); msg != "" {
			return msg
		}
	}
	return checkCampaignAliases(v, stored, merged)
}
