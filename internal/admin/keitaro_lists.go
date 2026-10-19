package admin

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

// importLists writes IP list files before groups are saved.
// res is the mapper output for this run. It returns how many files were written.
// When the lists store is missing, every stream ip_list is cleared and the
// result includes one loss for the store plus one loss per cleared stream.
// An error means a list file could not be written.
func (s *Server) importLists(res *ktimport.Result) (int, []ktimport.Loss, error) {
	if res == nil {
		return 0, nil, nil
	}
	if s.cfg.Lists == nil {
		losses := []ktimport.Loss{{
			Kind:   "skipped",
			Ref:    "lists",
			Detail: "lists store unavailable",
		}}
		groups, extra := stripMissingIPLists(res.Groups)
		res.Groups = groups
		return 0, append(losses, extra...), nil
	}
	n := 0
	for name, content := range res.Lists {
		if err := s.cfg.Lists.Write(name, content); err != nil {
			return 0, nil, err
		}
		n++
	}
	return n, nil, nil
}

// stripMissingIPLists clears ip_list on streams that name a file.
// groups is the mapper output and is updated in place.
// It returns the same slice and one skipped loss per cleared stream.
// It does not return an error.
func stripMissingIPLists(groups []ktimport.Group) ([]ktimport.Group, []ktimport.Loss) {
	var losses []ktimport.Loss
	for i := range groups {
		for j := range groups[i].Streams {
			file := groups[i].Streams[j].Rules.IPList.File
			if file == "" {
				continue
			}
			groups[i].Streams[j].Rules.IPList = ktimport.IPListFilter{}
			losses = append(losses, ktimport.Loss{
				Kind:   "skipped",
				Ref:    groups[i].Streams[j].Name,
				Detail: "ip list " + file + " missing",
			})
		}
	}
	return groups, losses
}

// importGroups merges campaigns and saves the groups that pass the checks.
// incoming is the mapper output. prev is the manifest from before this run.
// The first copy of a repeated id is kept. The most common postback is lifted
// into the s2s catalog before groups are saved, when that store is set.
// Each imported group is checked on its own.
// A failing one is skipped. Groups that were not in the manifest stay in the file.
// It returns how many incoming groups were saved and the skip report.
// An error means the file could not be read, converted or saved.
func (s *Server) importGroups(ctx context.Context, incoming []ktimport.Group, prev ktimport.Manifest, owner string, runManifest *ktimport.Manifest, host string) (int, []ktimport.Loss, error) {
	existing, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return 0, nil, err
	}
	incoming, losses := keepFirstGroups(incoming)
	incoming, err = s.extractGlobalS2S(ctx, incoming)
	if err != nil {
		return 0, losses, err
	}
	merged, extra, err := mergeImportedGroups(existing, incoming, prev, owner)
	if err != nil {
		return 0, append(losses, extra...), err
	}
	losses = append(losses, extra...)
	offers, err := s.offerList(ctx)
	if err != nil {
		return 0, losses, err
	}
	conflicts := conflictIDs(extra)
	saved, dropped := selectImportedGroups(merged, incoming, conflicts, offers)
	losses = append(losses, dropped...)
	recordAcceptedImportManifest(runManifest, incoming, conflicts, saved, losses)
	if err := s.saveImportGroupsManifest(prev, runManifest, host); err != nil {
		return 0, losses, err
	}
	if err := s.cfg.Groups.Save(ctx, saved); err != nil {
		return 0, losses, err
	}
	return countImported(incoming, saved, conflicts), losses, nil
}

// keepFirstGroups drops later copies of an id.
// incoming is the mapper output. The first group of each id is kept.
// It returns that list and one skipped loss per dropped copy.
// It does not return an error.
func keepFirstGroups(incoming []ktimport.Group) ([]ktimport.Group, []ktimport.Loss) {
	seen := map[string]bool{}
	var out []ktimport.Group
	var losses []ktimport.Loss
	for _, g := range incoming {
		if seen[g.ID] {
			losses = append(losses, ktimport.Loss{
				Kind:   "skipped",
				Ref:    g.ID,
				Detail: "duplicate group id: " + g.ID,
			})
			continue
		}
		seen[g.ID] = true
		out = append(out, g)
	}
	return out, losses
}

// extractGlobalS2S moves the most common postback into the s2s catalog.
// ctx is the request context. incoming is the deduped mapper output.
// A nil S2S store returns incoming unchanged. No chosen template does the same.
// A catalog id that is not already stored is appended and saved before the caller writes groups.
// It returns groups whose matching inline copies are removed and whose PostbackIDs hold the catalog id.
// An error means the catalog could not be read, converted or saved. Groups are not written here.
func (s *Server) extractGlobalS2S(ctx context.Context, incoming []ktimport.Group) ([]ktimport.Group, error) {
	if s.cfg.S2S == nil {
		return incoming, nil
	}
	stored, err := s.cfg.S2S.List(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := convert[[]ktimport.Postback](stored)
	if err != nil {
		return nil, err
	}
	pb, groups := ktimport.ExtractGlobalPostback(incoming, existing)
	if pb == nil {
		return incoming, nil
	}
	if s2sHasID(stored, pb.ID) {
		return groups, nil
	}
	row, err := convert[config.OutboundPostback](*pb)
	if err != nil {
		return nil, err
	}
	if err := s.cfg.S2S.Save(ctx, append(stored, row)); err != nil {
		return nil, err
	}
	return groups, nil
}

// s2sHasID reports whether list already contains id.
// list is the current catalog. id is the template id to find.
// It returns true when a row has that id. An empty id matches only an empty row id.
// It does not return an error.
func s2sHasID(list []config.OutboundPostback, id string) bool {
	for _, p := range list {
		if p.ID == id {
			return true
		}
	}
	return false
}

// selectImportedGroups applies the save checks to imported groups only.
// merged is the merge result. incoming is the deduped mapper output.
// conflicts are ids the merge refused to overwrite. offers is the offer catalog.
// A group that was not part of this import is copied unchanged.
// It returns the list to save, one skipped loss per dropped group and one approximated loss per group above 100%.
// It does not return an error.
func selectImportedGroups(merged []config.Group, incoming []ktimport.Group, conflicts map[string]bool, offers []config.Offer) ([]config.Group, []ktimport.Loss) {
	want := map[string]bool{}
	for _, g := range incoming {
		want[g.ID] = true
	}
	out := make([]config.Group, 0, len(merged))
	var losses []ktimport.Loss
	for _, g := range merged {
		if !want[g.ID] || conflicts[g.ID] {
			out = append(out, g)
			continue
		}
		if msg := importedGroupIssue(&g, offers); msg != "" {
			losses = append(losses, ktimport.Loss{Kind: "skipped", Ref: g.ID, Detail: msg})
			continue
		}
		if sharesExceed(g) {
			losses = append(losses, ktimport.Loss{Kind: "approximated", Ref: g.ID, Detail: errSharesExceed})
		}
		out = append(out, g)
	}
	return out, losses
}

// importedGroupIssue checks one imported group the way a full save used to.
// g is updated so its folder label is trimmed. offers are the catalog rows
// used to compare offer URLs inside the group.
// It returns an empty string when the group can be saved.
// It does not return an error. The string is the client message.
func importedGroupIssue(g *config.Group, offers []config.Offer) string {
	if g.ID == "" {
		return "group id required"
	}
	g.Folder = strings.TrimSpace(g.Folder)
	if utf8.RuneCountInString(g.Folder) > maxFolderRunes {
		return "folder is longer than 64 characters: " + g.ID
	}
	if msg := subIDFault(*g); msg != "" {
		return msg
	}
	return validateStreams([]config.Group{*g}, offers)
}

// conflictIDs collects ids the merge refused to overwrite.
// losses is the merge report. It returns a set of those ids.
// It does not return an error.
func conflictIDs(losses []ktimport.Loss) map[string]bool {
	out := map[string]bool{}
	for _, l := range losses {
		if l.Kind == "conflict" {
			out[l.Ref] = true
		}
	}
	return out
}

// countImported counts incoming groups that were actually saved.
// incoming is the deduped mapper output. saved is the list written to the file.
// conflicts are ids left untouched. It returns the count.
// It does not return an error.
func countImported(incoming []ktimport.Group, saved []config.Group, conflicts map[string]bool) int {
	have := map[string]bool{}
	for _, g := range saved {
		have[g.ID] = true
	}
	n := 0
	for _, g := range incoming {
		if conflicts[g.ID] || !have[g.ID] {
			continue
		}
		n++
	}
	return n
}
