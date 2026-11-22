package admin

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// Limits of the sync step that refreshes campaigns already imported: campaigns per pass and the
// GET requests the step leaves to the click and conversion streams of the same pass.
const (
	keitaroSyncUpdateBatch   = 50
	keitaroSyncUpdateReserve = 100
)

// keitaroSyncUpdateCampaigns refreshes the campaigns the manifest of target links to Keitaro ids:
// name, notes, weight mode, domain, source, streams, stream links, offers, landings and filters.
// Campaigns are found by the Keitaro id stored in the manifest, never by the local id, and the
// stored group is located through the stream ids of its notes (see findCampaignGroup), so a campaign
// renamed here is still refreshed and its manifest link follows the new id. Uniqueness settings,
// postbacks, folder, aliases, the default answer and every field Keitaro does not carry stay as they are.
// A campaign that no longer exists in Keitaro is left untouched and counted in st.CampaignsGone.
// Campaigns go in ascending id from st.UpdateCursor round-robin; the GET budget of target.creds and
// keitaroSyncUpdateBatch bound one pass. A tracker failure, a 402 or 429 answer or a store failure
// pauses the step for keitaroCampaignsRetry and never fails the pass.
// It returns how many campaigns changed.
func (s *Server) keitaroSyncUpdateCampaigns(ctx context.Context, target keitaroSyncTarget, st *keitaroSyncFile) int {
	now := s.keitaroSyncClock()
	if s.cfg.Groups == nil || now.Before(st.UpdatePausedUntil) {
		return 0
	}
	st.UpdatePausedUntil, st.UpdateError, st.UpdateFailed = time.Time{}, "", nil
	pause := func(reason string) int {
		st.UpdatePausedUntil = now.Add(keitaroCampaignsRetry)
		st.UpdateError = reason
		s.logWarn("keitaro sync update paused", "reason", reason)
		return 0
	}
	creds := target.creds
	manifest, err := loadManifestStore(target.manifestStore)
	if err != nil {
		return pause("манифест не прочитан")
	}
	linked := manifest.LogCampaignAliases()
	st.CampaignsGone = 0
	if len(linked) == 0 {
		return 0
	}
	camps, err := s.keitaroClient(creds).FetchCampaigns(ctx)
	if code := creds.gets.refusal(); code != 0 {
		return pause(fmt.Sprintf("Keitaro ответил %d", code))
	}
	if err != nil {
		return pause(sanitize(err, creds.Key))
	}
	present := make(map[int]bool, len(camps))
	for _, c := range camps {
		present[int(c.ID)] = true
	}
	var ids []int
	for _, id := range sortedCampaignIDs(linked) {
		if present[id] {
			ids = append(ids, id)
		} else {
			st.CampaignsGone++
		}
	}
	ids = rotateAfter(ids, st.UpdateCursor)
	n := min(len(ids), keitaroSyncUpdateBatch, (creds.gets.left()-keitaroSnapshotGETs-keitaroSyncUpdateReserve)/2)
	if n <= 0 {
		return 0
	}
	snap, err := s.keitaroClient(creds).FetchCampaignsSnapshot(ctx, ids[:n])
	if code := creds.gets.refusal(); code != 0 {
		return pause(fmt.Sprintf("Keitaro ответил %d", code))
	}
	if err != nil {
		return pause(sanitize(err, creds.Key))
	}
	batch, err := s.mapCampaignSet(ctx, target.manifestStore, snap, ids[:n], creds.gets.left(), false)
	if err != nil {
		return pause("кампании не обновлены")
	}
	if len(batch.taken) == 0 {
		return 0
	}
	updated, failed, err := s.saveCampaignUpdates(ctx, target, batch, linked)
	if err != nil {
		s.logWarn("keitaro sync update save failed", "err", err)
		return pause("кампании не обновлены")
	}
	st.UpdateCursor = batch.taken[len(batch.taken)-1]
	st.UpdateFailed = failed
	s.logInfo("keitaro sync update", "updated", updated, "failed", len(failed), "gone", st.CampaignsGone)
	return updated
}

// rotateAfter returns ids, which are ascending, starting with the first id above cursor and
// continuing with the ids up to cursor, so repeated passes visit every id.
// ids is not modified.
func rotateAfter(ids []int, cursor int) []int {
	i, _ := slices.BinarySearch(ids, cursor+1)
	return append(slices.Clone(ids[i:]), ids[:i]...)
}

// saveCampaignUpdates writes a batch of refreshed campaigns: catalog rows, landing archives, lists
// and pages first, then the groups under the groups lock in one save, which also tells the engine to
// reload. linked maps a Keitaro campaign id to the group id of the manifest.
// A refreshed group that fails the save checks keeps its stored form and its Keitaro id goes to failed.
// A group found under another id than the manifest lists moves the manifest link to that id.
// It returns how many groups changed. An error means a store or the manifest could not be read or saved.
func (s *Server) saveCampaignUpdates(ctx context.Context, target keitaroSyncTarget, batch newCampaignsBatch, linked map[int]string) (int, []int, error) {
	res := batch.res
	if err := s.saveCampaignAssets(ctx, target, &res); err != nil {
		return 0, nil, err
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return 0, nil, err
	}
	offers, err := s.offerList(ctx)
	if err != nil {
		return 0, nil, err
	}
	scope := campaignMapScope{board: target.board, allBoards: target.allBoards}
	var relink ktimport.Manifest
	updated := 0
	var failed []int
	for _, in := range res.Groups {
		gi, ok := findCampaignGroup(groups, scope, in.Num, linked[in.Num], in)
		if !ok {
			s.logInfo("keitaro sync update: no group", "campaign", in.Num)
			continue
		}
		if groups[gi].ID != linked[in.Num] {
			relink.Add(ktimport.KindGroups, groups[gi].ID)
			if relink.Campaigns == nil {
				relink.Campaigns = map[string]string{}
			}
			relink.Campaigns[strconv.Itoa(in.Num)] = groups[gi].ID
		}
		next, err := overlayKeitaroGroup(groups[gi], in)
		if err != nil {
			failed = append(failed, in.Num)
			continue
		}
		if groupsEqual(groups[gi], next) {
			continue
		}
		if msg := importedGroupIssue(&next, offers); msg != "" {
			s.logWarn("keitaro sync update skipped", "campaign", in.Num, "reason", msg)
			failed = append(failed, in.Num)
			continue
		}
		groups[gi] = next
		updated++
	}
	if updated > 0 {
		if err := s.cfg.Groups.Save(ctx, groups); err != nil {
			return 0, nil, err
		}
	}
	if len(relink.Campaigns) > 0 {
		prev, err := loadManifestStore(target.manifestStore)
		if err != nil {
			return updated, failed, err
		}
		if target.manifestStore != nil {
			if err := target.manifestStore.Save(prev.Merge(relink)); err != nil {
				return updated, failed, err
			}
		}
	}
	return updated, failed, nil
}

// findCampaignGroup returns the position in groups of the stored group of Keitaro campaign cid.
// gid is the group id of the manifest, or empty. in is the mapped campaign, whose streams carry the
// Keitaro stream ids in their notes. A group belongs to the campaign only when it lies in scope and
// either holds one of those stream ids or has the number cid: the group with the id gid wins when it
// does; otherwise exactly one group holding stream ids decides, with the number cid breaking a tie
// between copies of the campaign. A group that only shares the number or the id is never taken,
// so a local campaign created later under a freed id is left alone.
// It returns false when no group or more than one qualifies.
func findCampaignGroup(groups []config.Group, scope campaignMapScope, cid int, gid string, in ktimport.Group) (int, bool) {
	wanted := map[int]bool{}
	for _, st := range in.Streams {
		if id, ok := ktimport.NoteStreamID(st.Comment); ok {
			wanted[id] = true
		}
	}
	var holders, numbered []int
	for i, g := range groups {
		if !groupInCampaignScope(g, scope) {
			continue
		}
		holds := slices.ContainsFunc(g.Streams, func(st config.Stream) bool {
			id, ok := ktimport.NoteStreamID(st.Comment)
			return ok && wanted[id]
		})
		if g.ID == gid && (holds || g.Num == cid) {
			return i, true
		}
		if holds {
			holders = append(holders, i)
			if g.Num == cid {
				numbered = append(numbered, i)
			}
		}
	}
	switch {
	case len(numbered) == 1:
		return numbered[0], true
	case len(holders) == 1:
		return holders[0], true
	default:
		return 0, false
	}
}

// overlayKeitaroGroup puts the tracker data of one campaign on its stored group.
// old is the stored group and is not modified. in is the mapper output for the campaign.
// The name always follows the tracker; the notes follow it only when the tracker sends text, so an
// empty tracker note never erases a local one. The weight mode follows the tracker; the domain and
// the source only when the tracker names one. The campaign is only ever switched off, never on.
// Uniqueness, postbacks, folder, aliases, header, default answer, number and owner are kept.
// It returns the updated copy. An error means the streams cannot be converted.
func overlayKeitaroGroup(old config.Group, in ktimport.Group) (config.Group, error) {
	next := old
	if in.Name != "" {
		next.Name = in.Name
	}
	if notes := strings.TrimSpace(in.Notes); notes != "" {
		next.Notes = notes
	}
	next.Weight = in.Weight
	if in.DomainID != "" {
		next.DomainID = in.DomainID
	}
	if in.SourceID != "" {
		next.SourceID = in.SourceID
	}
	if old.Status && !in.Status {
		next.Status = false
	}
	streams, err := overlayStreams(old.Streams, in.Streams)
	if err != nil {
		return old, err
	}
	next.Streams = streams
	return next, nil
}

// overlayStreams merges the tracker streams of a campaign into the stored ones.
// Streams are matched by the keitaro_stream_id of their notes, so a stream renamed here is still found.
// A matched stream takes the tracker rules, weight, links, offers and landings and keeps its name,
// note text, bot and separation settings and every other local field. A stream the tracker has and
// the group lacks is added after the stream of the previous tracker position, or first. A stored stream
// without a match, a stream deleted in Keitaro or one made here, stays where it is.
// old is not modified. It returns the merged list. An error means a stream cannot be converted.
func overlayStreams(old []config.Stream, in []ktimport.Stream) ([]config.Stream, error) {
	mapped, err := convert[[]config.Stream](in)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(old)
	keys := make([]int, len(out))
	for i, st := range out {
		if id, ok := ktimport.NoteStreamID(st.Comment); ok && !slices.Contains(keys, id) {
			keys[i] = id
		}
	}
	prev := 0
	for j, src := range in {
		id, ok := ktimport.NoteStreamID(src.Comment)
		if !ok {
			continue
		}
		if at := slices.Index(keys, id); at >= 0 {
			out[at] = overlayStream(out[at], mapped[j], src)
		} else {
			at := 0
			if p := slices.Index(keys, prev); prev > 0 && p >= 0 {
				at = p + 1
			}
			out = slices.Insert(out, at, mapped[j])
			keys = slices.Insert(keys, at, id)
		}
		prev = id
	}
	return out, nil
}

// overlayStream puts one tracker stream on its stored copy.
// old is the stored stream. fresh is the mapped stream and src the mapper output it came from.
// Rules go through mergeImportedRules, so rule keys the import does not fill stay. The stream is only
// ever switched off. A stream that is off in the tracker, or that the mapper had to switch off,
// keeps its stored links: only its status, weight and rules follow.
// It returns the updated stream.
func overlayStream(old, fresh config.Stream, src ktimport.Stream) config.Stream {
	out := old
	out.Weight = fresh.Weight
	out.FilterOr = fresh.FilterOr
	if rules, err := mergeImportedRules(old.Rules, src.Rules); err == nil {
		out.Rules = rules
	}
	out.Comment = ktimport.RenoteStream(old.Comment, ktimport.NoteSkipped(src.Comment))
	if !fresh.Status {
		out.Status = false
		return out
	}
	links := old.Out
	links.Redirect, links.Out, links.Chance, links.Distribution = fresh.Out.Redirect, fresh.Out.Out, fresh.Out.Chance, fresh.Out.Distribution
	links.Offers, links.Landings = fresh.Out.Offers, fresh.Out.Landings
	if links.Out != old.Out.Out {
		links.OutShares = nil
	}
	out.Out = links
	return out
}
