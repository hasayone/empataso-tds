package admin

import (
	"context"
	"strconv"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

// recordAcceptedImportManifest appends accepted group ids and campaign map entries.
//
// Parameters: dst is this run's manifest fragment; incoming is the deduped mapper output;
// conflicts are ids the merge refused to overwrite; saved is the list about to be written;
// losses includes validation skips for incoming groups.
// Result: dst receives KindGroups ids and campaign_id → group_id pairs for accepted groups.
// Errors: none.
func recordAcceptedImportManifest(dst *ktimport.Manifest, incoming []ktimport.Group, conflicts map[string]bool, saved []config.Group, losses []ktimport.Loss) {
	if dst == nil {
		return
	}
	skipped := skippedGroupIDs(losses)
	have := groupIDSet(saved)
	for _, g := range incoming {
		if !groupImportAccepted(g, conflicts, skipped, have) {
			continue
		}
		dst.Add(ktimport.KindGroups, g.ID)
		if dst.Campaigns == nil {
			dst.Campaigns = map[string]string{}
		}
		cid := strconv.Itoa(g.Num)
		for k, v := range dst.Campaigns {
			if v == g.ID {
				delete(dst.Campaigns, k)
			}
		}
		dst.Campaigns[cid] = g.ID
	}
}

// groupImportAccepted reports whether importGroups writes one incoming group.
//
// Parameters: g is the mapper group; conflicts and skipped are rejected ids; have is saved ids.
// Result: true when the group is written to groups.json.
// Errors: none.
func groupImportAccepted(g ktimport.Group, conflicts map[string]bool, skipped map[string]bool, have map[string]bool) bool {
	if g.ID == "" || g.Num <= 0 || conflicts[g.ID] || skipped[g.ID] || !have[g.ID] {
		return false
	}
	return true
}

// skippedGroupIDs collects group ids skipped during validation.
//
// Parameters: losses is the import report accumulated so far.
// Result: set of group ids with kind skipped.
// Errors: none.
func skippedGroupIDs(losses []ktimport.Loss) map[string]bool {
	out := map[string]bool{}
	for _, l := range losses {
		if l.Kind == "skipped" && l.Ref != "" {
			out[l.Ref] = true
		}
	}
	return out
}

// saveImportGroupsManifest writes the merged manifest before groups.json.
//
// Parameters: base is the manifest loaded at run start; delta is this run's fragment; host is the tracker hostname.
// Result: base merged with delta is persisted, including host when host is non-empty.
// Errors: manifest store write failure.
func (s *Server) saveImportGroupsManifest(base ktimport.Manifest, delta *ktimport.Manifest, host string) error {
	if s.cfg.Manifest == nil {
		return nil
	}
	var frag ktimport.Manifest
	if delta != nil {
		frag = *delta
	}
	next := base.Merge(frag)
	if host != "" {
		next.Host = host
	}
	return s.cfg.Manifest.Save(next)
}

// resolveLogCampaignMap builds campaign id to group id for log import.
//
// Parameters: manifest is prev merged with this run; snapCampaigns is set when FetchSnapshot ran;
// groups is the current groups-store snapshot; creds and ctx reach the tracker when needed.
// Result: aliases for row mapping and optional backfill pairs to persist for legacy manifests.
// Errors: tracker campaign list unavailable when it must be fetched.
func (s *Server) resolveLogCampaignMap(ctx context.Context, creds ktCreds, manifest ktimport.Manifest, snapCampaigns []ktimport.Campaign, groups []config.Group) (map[int]string, map[string]string, error) {
	aliases := filterLogAliases(manifest.LogCampaignAliases(), groups)
	if len(manifest.Campaigns) > 0 {
		return aliases, nil, nil
	}
	camps := snapCampaigns
	if len(camps) == 0 {
		var err error
		camps, err = s.keitaroClient(creds).FetchCampaigns(ctx)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(manifest.Groups) == 0 {
		return aliases, nil, nil
	}
	add, backfill := inferLegacyBackfill(manifest, groups, camps, aliases)
	for num, gid := range add {
		aliases[num] = gid
	}
	if len(backfill) == 0 {
		return aliases, nil, nil
	}
	return aliases, backfill, nil
}

// inferLegacyBackfill proposes campaign links for manifests with groups but no campaign map.
//
// Parameters: manifest lists imported group ids; groups is the store snapshot; camps is the tracker list;
// existing holds pairs already resolved for this import.
// Result: new int aliases and string pairs suitable for AddCampaignsIfAbsent.
// Errors: none.
func inferLegacyBackfill(manifest ktimport.Manifest, groups []config.Group, camps []ktimport.Campaign, existing map[int]string) (map[int]string, map[string]string) {
	imported := manifest.Imported(ktimport.KindGroups)
	byID := groupsByID(groups)
	numOwner := manifestNumOwners(groups, imported)
	proposed := ktimport.GroupIDs(camps)
	add := map[int]string{}
	backfill := map[string]string{}
	for num, gid := range proposed {
		if _, ok := existing[num]; ok {
			continue
		}
		cid := strconv.Itoa(num)
		if !legacyBackfillLink(num, gid, proposed, byID, numOwner, imported) {
			continue
		}
		add[num] = gid
		backfill[cid] = gid
	}
	return add, backfill
}

// legacyBackfillLink reports whether one tracker campaign may link to a stored group.
//
// Parameters: num is the Keitaro campaign id; gid is the proposed group id; proposed is GroupIDs(camps);
// byID indexes the store; numOwner maps numbers to the sole imported owner; imported checks manifest groups.
// Result: true when every consistency rule passes.
// Errors: none.
func legacyBackfillLink(num int, gid string, proposed map[int]string, byID map[string]config.Group, numOwner map[int]string, imported func(string) bool) bool {
	if !imported(gid) || proposed[num] != gid {
		return false
	}
	g, ok := byID[gid]
	if !ok || g.Num != num {
		return false
	}
	owner, ok := numOwner[num]
	return ok && owner == gid
}

// manifestNumOwners maps campaign numbers to the sole imported group that owns them.
//
// Parameters: groups is the store snapshot; imported is true for ids listed in the manifest.
// Result: num → group id; numbers shared by several imported groups are omitted.
// Errors: none.
func manifestNumOwners(groups []config.Group, imported func(string) bool) map[int]string {
	out := map[int]string{}
	ambiguous := map[int]bool{}
	for _, g := range groups {
		if !imported(g.ID) || g.Num <= 0 {
			continue
		}
		if prev, ok := out[g.Num]; ok && prev != g.ID {
			ambiguous[g.Num] = true
		} else if !ambiguous[g.Num] {
			out[g.Num] = g.ID
		}
	}
	for num := range ambiguous {
		delete(out, num)
	}
	return out
}

// filterLogAliases keeps manifest campaign pairs whose group id exists in the store.
//
// Parameters: aliases maps tracker campaign id to group id; groups is the store snapshot.
// Result: a new map containing only pairs whose group id is present in groups.
// Errors: none.
func filterLogAliases(aliases map[int]string, groups []config.Group) map[int]string {
	out := map[int]string{}
	if len(aliases) == 0 {
		return out
	}
	gidUses := map[string]int{}
	for _, gid := range aliases {
		if gid != "" {
			gidUses[gid]++
		}
	}
	byID := groupsByID(groups)
	for num, gid := range aliases {
		if gidUses[gid] > 1 {
			continue
		}
		if _, ok := byID[gid]; ok {
			out[num] = gid
		}
	}
	return out
}

// groupsByID indexes groups from the store by id.
//
// Parameters: groups is the store snapshot.
// Result: map of non-empty group id to group row.
// Errors: none.
func groupsByID(groups []config.Group) map[string]config.Group {
	out := map[string]config.Group{}
	for _, g := range groups {
		if g.ID != "" {
			out[g.ID] = g
		}
	}
	return out
}

// groupIDSet indexes group ids from the groups store.
//
// Parameters: groups is the store snapshot.
// Result: set of non-empty group ids.
// Errors: none.
func groupIDSet(groups []config.Group) map[string]bool {
	out := map[string]bool{}
	for _, g := range groups {
		if g.ID != "" {
			out[g.ID] = true
		}
	}
	return out
}

// saveCampaignMapBackfill merges absent campaign pairs into a manifest snapshot.
//
// Parameters: merged is the full manifest for this run; add holds new pairs only; host is the tracker hostname.
// Result: merged with add applied via AddCampaignsIfAbsent is persisted.
// Errors: manifest store write failure.
func (s *Server) saveCampaignMapBackfill(merged ktimport.Manifest, add map[string]string, host string) error {
	if s.cfg.Manifest == nil || len(add) == 0 {
		return nil
	}
	next := merged
	next.AddCampaignsIfAbsent(add)
	if host != "" {
		next.Host = host
	}
	return s.cfg.Manifest.Save(next)
}
