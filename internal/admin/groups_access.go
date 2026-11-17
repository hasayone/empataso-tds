package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
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

// mergeGroups combines stored campaigns with the request body under viewer and section rules.
// Rows of the current section that the body omits are deleted. Rows of other sections that the
// body omits stay. A row the session can see is edited in place and keeps its stored board id.
// A changed row the session cannot see is forbidden. An unchanged foreign row in the body is kept.
// A new id gets the current section even when the body names another section.
// It returns the merged slice, or a non-zero HTTP status and message on denial.
func mergeGroups(v viewer, stored, body []config.Group) ([]config.Group, int, string) {
	board := v.boardCurrent
	bodyByID := map[string]config.Group{}
	for _, g := range body {
		if g.ID == "" {
			return nil, http.StatusBadRequest, "group id required"
		}
		bodyByID[g.ID] = g
	}
	storedByID := map[string]config.Group{}
	for _, g := range stored {
		storedByID[g.ID] = g
	}
	var hidden []config.Group
	var visibleOther []config.Group
	var ownStored []config.Group
	for _, old := range stored {
		if groupInBoard(old, board) {
			ownStored = append(ownStored, old)
			continue
		}
		if v.seesBoard(old.BoardID) {
			visibleOther = append(visibleOther, old)
			continue
		}
		hidden = append(hidden, old)
		if g, ok := bodyByID[old.ID]; ok && !groupsEqual(old, g) {
			return nil, http.StatusForbidden, "foreign campaign: " + old.ID
		}
	}
	ownBody := make([]config.Group, 0, len(body))
	for _, g := range body {
		old, exists := storedByID[g.ID]
		if !exists {
			g.BoardID = board
			ownBody = append(ownBody, g)
			continue
		}
		if groupInBoard(old, board) {
			g.BoardID = old.BoardID
			ownBody = append(ownBody, g)
		}
	}
	otherBody := append([]config.Group(nil), visibleOther...)
	for i, old := range otherBody {
		g, ok := bodyByID[old.ID]
		if !ok {
			continue
		}
		g.BoardID = old.BoardID
		otherBody[i] = g
	}
	mergedOther, code, msg := mergeBoardGroups(v, visibleOther, otherBody)
	if code != 0 {
		return nil, code, msg
	}
	mergedOwn, code, msg := mergeBoardGroups(v, ownStored, ownBody)
	if code != 0 {
		return nil, code, msg
	}
	out := make([]config.Group, 0, len(hidden)+len(mergedOther)+len(mergedOwn))
	out = append(out, hidden...)
	out = append(out, mergedOther...)
	out = append(out, mergedOwn...)
	return out, 0, ""
}

// mergeBoardGroups merges campaigns within one section.
func mergeBoardGroups(v viewer, stored, body []config.Group) ([]config.Group, int, string) {
	if v.admin {
		byID := map[string]config.Group{}
		for _, g := range stored {
			byID[g.ID] = g
		}
		out := append([]config.Group(nil), body...)
		for i := range out {
			if old, ok := byID[out[i].ID]; ok {
				out[i].BoardID = old.BoardID
				if out[i].CreatedBy == "" {
					out[i].CreatedBy = old.CreatedBy
				}
			} else {
				out[i].BoardID = v.boardCurrent
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
			g.BoardID = old.BoardID
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
		g.BoardID = v.boardCurrent
		out = append(out, g)
	}
	return out, 0, ""
}

// campaignRef builds an access ref for one campaign row.
func campaignRef(g config.Group) ref {
	return ref{id: g.ID, label: g.Folder, owner: g.CreatedBy, board: g.BoardID}
}

// groupsEqual reports whether a and b are the same campaign.
// a and b are compared as saved JSON, after empty slices and maps are treated as absent,
// so a nil value, [] and {} are one value and a zero-valued optional object equals a missing one.
// Any other difference, including a changed number, owner or rule flag, makes them unequal.
// It returns false when either side cannot be encoded. Neither argument is modified.
func groupsEqual(a, b config.Group) bool {
	ba, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	if bytes.Equal(ba, bb) {
		return true
	}
	ca, ok1 := canonicalGroupJSON(ba)
	cb, ok2 := canonicalGroupJSON(bb)
	return ok1 && ok2 && bytes.Equal(ca, cb)
}

// canonicalGroupJSON decodes one marshaled campaign into a fresh Group, clears every empty
// slice and map, and marshals it again, so the omitempty and omitzero rules drop what is empty.
// raw is the JSON of a Group. It returns the canonical JSON and true,
// or nil and false when raw does not decode or encode.
func canonicalGroupJSON(raw []byte) ([]byte, bool) {
	var g config.Group
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, false
	}
	clearEmpty(reflect.ValueOf(&g).Elem())
	out, err := json.Marshal(g)
	return out, err == nil
}

// clearEmpty sets every empty slice and map under v to its nil value, recursively.
// v must be addressable and owned by the caller, because it is modified in place.
// Slice elements are kept even when empty, so the length of a list is never changed.
func clearEmpty(v reflect.Value) {
	switch v.Kind() {
	case reflect.Slice:
		if v.Len() == 0 {
			v.SetZero()
			return
		}
		for i := 0; i < v.Len(); i++ {
			clearEmpty(v.Index(i))
		}
	case reflect.Map:
		if v.Len() == 0 {
			v.SetZero()
			return
		}
		for _, k := range v.MapKeys() {
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(v.MapIndex(k))
			clearEmpty(elem)
			v.SetMapIndex(k, elem)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				clearEmpty(v.Field(i))
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			clearEmpty(v.Index(i))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			clearEmpty(v.Elem())
		}
	}
}

// linkCatalog maps grant resource names to catalog id→ref rows for link checks.
type linkCatalog map[string]map[string]ref

// loadLinkCatalog reads rows of every campaignLinkKinds resource from disk and indexes campaigns from groups.
// groups supplies campaign ids and aliases. Missing stores leave empty maps.
// Affiliate networks are not campaign links and are not loaded.
func (s *Server) loadLinkCatalog(ctx context.Context, groups []config.Group) linkCatalog {
	cat := linkCatalog{}
	for _, res := range campaignLinkKinds {
		cat[res] = map[string]ref{}
	}
	if s.cfg.Offers != nil {
		if list, err := s.cfg.Offers.List(ctx); err == nil {
			for _, o := range list {
				cat[resOffers][o.ID] = ref{id: o.ID, label: o.Group, owner: o.CreatedBy, board: o.BoardID}
			}
		}
	}
	if s.cfg.Landings != nil {
		if list, err := s.cfg.Landings.List(ctx); err == nil {
			for _, l := range list {
				cat[resLandings][l.ID] = ref{id: l.ID, label: l.Group, owner: l.CreatedBy, board: l.BoardID}
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
				cat[resSources][src.ID] = ref{id: src.ID, label: src.Group, owner: src.CreatedBy, board: src.BoardID}
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
// A new link must belong to the section of g, not the request section. Domains are not section-scoped.
// It returns an empty string when every link is allowed, otherwise a 400 message.
func checkLinks(v viewer, g config.Group, old *config.Group, cat linkCatalog) string {
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
			if res != resDomains && !config.SameBoard(r.board, g.BoardID) {
				return "unknown " + unknownLinkNoun[res] + ": " + id
			}
		}
	}
	return ""
}

// checkCampaignAliases rejects alias/id clashes introduced by new or changed groups.
func checkCampaignAliases(_ viewer, stored, merged []config.Group) string {
	storedByID := map[string]config.Group{}
	for _, g := range stored {
		storedByID[g.ID] = g
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
			if aliasTakenAmong(merged, g.ID, a) {
				return "alias taken"
			}
		}
	}
	return ""
}

// aliasTakenAmong reports whether alias is already used as an id or alias on another campaign in merged.
func aliasTakenAmong(merged []config.Group, selfID, alias string) bool {
	for _, g := range merged {
		if g.ID == selfID {
			continue
		}
		if g.ID == alias {
			return true
		}
		for _, a := range g.Aliases {
			if a == alias {
				return true
			}
		}
	}
	return false
}

// validateGroupLinks runs checkLinks and alias checks on changed campaigns the session can see.
// stored is the file before merge; merged is the candidate save payload.
// It returns the first 400/validation message, or an empty string.
func validateGroupLinks(v viewer, stored, merged []config.Group, cat linkCatalog) string {
	storedByID := map[string]config.Group{}
	for _, g := range stored {
		storedByID[g.ID] = g
	}
	for _, g := range merged {
		if !v.seesBoard(g.BoardID) {
			continue
		}
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
