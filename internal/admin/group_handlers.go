package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// handleGroupsGet answers every campaign the caller may see, with its streams, or with
// ?shared=1 the same list with the long stream filters shared. A request of the assistant tool
// carries an aiCampaignView in its context and gets the summary rows of that view instead.
// A missing store gives an empty list; a store failure is 500.
func (s *Server) handleGroupsGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeJSON(w, http.StatusOK, []config.Group{})
		return
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	v := s.viewerFrom(r.Context())
	filtered := make([]config.Group, 0, len(groups))
	for _, g := range groups {
		if !v.seesBoard(g.BoardID) {
			continue
		}
		if v.admin || v.canSee(resCampaigns, campaignRef(g)) {
			filtered = append(filtered, g)
		}
	}
	if view, ok := r.Context().Value(aiCampaignViewKey{}).(aiCampaignView); ok {
		writeJSON(w, http.StatusOK, view.rows(filtered))
		return
	}
	if r.URL.Query().Get("shared") == "1" {
		writeJSON(w, http.StatusOK, shareGroupLists(filtered))
		return
	}
	writeJSON(w, http.StatusOK, filtered)
}

// maxFolderRunes is the longest campaign folder label the admin accepts.
// The count is runes, so a 64-letter Cyrillic label still fits.
const maxFolderRunes = 64

// maxNotesRunes is the longest campaign note the admin accepts, counted in runes.
const maxNotesRunes = ktimport.MaxNotesRunes

// maxGroupsBody is the largest PUT /api/groups body.
// Several thousand imported campaigns are saved as one array.
const maxGroupsBody = 128 << 20

// maxGroupBody is the largest PUT /api/groups/{id} body.
// The payload is one campaign, not the whole list.
const maxGroupBody = 8 << 20

// handleGroupsSave replaces the campaign list with the JSON array in the body.
// The body is the full list. A missing store is 503. A body over maxGroupsBody is 413,
// another bad body is 400. Validation and save errors are written by commitGroups.
// Success writes 200 {"status":"saved"}, with "relinked" as in handleGroupPut.
func (s *Server) handleGroupsSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var body []config.Group
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGroupsBody)).Decode(&body); err != nil {
		writeBodyErr(w, err)
		return
	}
	v := s.viewerFrom(r.Context())
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	stored, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	var relinked []config.Group
	if !s.commitGroups(w, r, v, stored, body, groupWrite{fromSave: true, relinked: &relinked}) {
		return
	}
	writeJSON(w, http.StatusOK, savedBody(relinked))
}

// savedBody is the 200 answer of a campaign save: {"status":"saved"}.
// relinked holds the campaigns whose destinations were moved off the old tracker by this save;
// they are listed under "relinked" so the panel shows what is stored. The key is absent when none moved.
func savedBody(relinked []config.Group) map[string]any {
	body := map[string]any{"status": "saved"}
	if len(relinked) > 0 {
		body["relinked"] = relinked
	}
	return body
}

// handleGroupPut replaces one campaign or appends it, then saves the full list.
// The path id is the campaign id. An empty body id is taken from the path.
// A body id that differs from the path renames the stored campaign (renameGroupPut);
// an assistant write with a different id is 400 "id mismatch". A missing store is 503.
// A body over maxGroupBody is 413, another bad body is 400. Other errors match commitGroups.
// Success writes 200 {"status":"saved"}, with "relinked" when a new campaign had destinations on the
// old tracker moved to campaigns of this panel (see savedBody).
func (s *Server) handleGroupPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	id := r.PathValue("id")
	var body config.Group
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGroupBody)).Decode(&body); err != nil {
		writeBodyErr(w, err)
		return
	}
	if body.ID == "" {
		body.ID = id
	}
	if body.ID != id {
		if aiExpectFrom(r.Context()) != nil {
			writeErr(w, http.StatusBadRequest, "id mismatch")
			return
		}
		s.renameGroupPut(w, r, id, body)
		return
	}
	v := s.viewerFrom(r.Context())
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	stored, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	exp := aiExpectFrom(r.Context())
	if exp != nil {
		old, exists := findGroup(stored, id)
		if exists && !v.admin && !(v.seesBoard(old.BoardID) && v.canSee(resCampaigns, campaignRef(old))) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if err := exp.check(old, exists); err != nil {
			s.failWrite(w, err)
			return
		}
	}
	var relinked []config.Group
	if !s.commitGroups(w, r, v, stored, replaceGroup(stored, body), groupWrite{exp: exp, id: id, relinked: &relinked}) {
		return
	}
	writeJSON(w, http.StatusOK, savedBody(relinked))
}

// writeBodyErr answers a request body that could not be decoded.
// err is the decode error. A body over its http.MaxBytesReader limit is 413 "body too large",
// any other error is 400 "bad json".
func writeBodyErr(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeErr(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	writeErr(w, http.StatusBadRequest, "bad json")
}

// renameGroupPut renames the stored campaign oldID to body.ID and saves body in its place, in one write
// under groupsMu. The row keeps its position, section and owner; Num comes from body as on every save.
// Campaign links of other campaigns that pointed at oldID are moved to the new id in the same write.
// The checks of saveGroups compare the row with the stored one under the new id, so only real changes are checked.
// Errors: 400 for a new id with a space, /, ? or #; 404 for a missing or unseen oldID; 403 when the caller
// may not write the campaign or a campaign whose link moves; 409 "campaign id is taken: <id>" when the new id
// is an id or alias of another campaign; others match saveGroups. Success writes 200 {"status":"saved"}.
func (s *Server) renameGroupPut(w http.ResponseWriter, r *http.Request, oldID string, body config.Group) {
	if !validGroupID(body.ID) {
		writeErr(w, http.StatusBadRequest, "campaign id must not contain spaces, /, ? or #")
		return
	}
	v := s.viewerFrom(r.Context())
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	stored, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	old, ok := findGroup(stored, oldID)
	if !ok || !v.seesBoard(old.BoardID) || (!v.admin && !v.canSee(resCampaigns, campaignRef(old))) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !v.admin && !v.canWrite(resCampaigns, campaignRef(old)) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, taken := findGroup(stored, body.ID); taken || aliasTakenAmong(stored, oldID, body.ID) {
		writeErr(w, http.StatusConflict, "campaign id is taken: "+body.ID)
		return
	}
	body.BoardID, body.CreatedBy = old.BoardID, old.CreatedBy
	before, groups, moved := renameGroup(stored, oldID, body)
	for _, i := range moved {
		if !v.admin && !v.canWrite(resCampaigns, campaignRef(stored[i])) {
			writeErr(w, http.StatusForbidden, "read-only campaign: "+stored[i].ID)
			return
		}
	}
	if code, msg := s.saveGroups(r.Context(), v, before, groups, groupWrite{id: body.ID}); code != 0 {
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// validGroupID reports whether id can be the first path segment of a campaign link:
// it is not empty and holds no whitespace, '/', '?' or '#'.
func validGroupID(id string) bool {
	return id != "" && !strings.ContainsAny(id, " \t\r\n/?#")
}

// renameGroup builds the save candidate for renaming the campaign oldID to g.ID. stored is not modified.
// before is stored with that row under the new id, the baseline the save checks compare with.
// groups is stored with g in that row's place and every campaign link (campaign default or stream
// with redirect "group") that named oldID moved to g.ID. moved holds the indexes of the other
// campaigns whose links changed. When oldID is absent, before and groups are copies of stored.
func renameGroup(stored []config.Group, oldID string, g config.Group) (before, groups []config.Group, moved []int) {
	before = append([]config.Group(nil), stored...)
	groups = append([]config.Group(nil), stored...)
	for i := range groups {
		if groups[i].ID == oldID {
			before[i].ID = g.ID
			groups[i] = g
			continue
		}
		if moveGroupLinks(&groups[i], oldID, g.ID) {
			moved = append(moved, i)
		}
	}
	return before, groups, moved
}

// moveGroupLinks points the campaign links of g that name from at to instead.
// The streams slice is copied before a change, so a slice shared with the stored list stays as it was.
// It reports whether any link changed.
func moveGroupLinks(g *config.Group, from, to string) bool {
	changed, copied := false, false
	if g.Redirect == "group" && strings.TrimSpace(g.Out) == from {
		g.Out = to
		changed = true
	}
	for i := range g.Streams {
		if g.Streams[i].Out.Redirect != "group" || strings.TrimSpace(g.Streams[i].Out.Out) != from {
			continue
		}
		if !copied {
			g.Streams = append([]config.Stream(nil), g.Streams...)
			copied = true
		}
		g.Streams[i].Out.Out = to
		changed = true
	}
	return changed
}

// findGroup returns the campaign with the given id and whether it exists.
// stored is the list read under groupsMu. It does not return an error.
func findGroup(stored []config.Group, id string) (config.Group, bool) {
	for _, g := range stored {
		if g.ID == id {
			return g, true
		}
	}
	return config.Group{}, false
}

// handleGroupDelete removes the campaign named by the path id and saves the rest.
// A missing id is 404 "not found". A missing store is 503.
// A list error is 500. Other errors match commitGroups.
// Success writes 200 {"status":"deleted"}.
func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	id := r.PathValue("id")
	v := s.viewerFrom(r.Context())
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	stored, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	exp := aiExpectFrom(r.Context())
	full, ok := dropGroup(stored, id)
	if !ok {
		s.writeMissing(w, exp)
		return
	}
	target, _ := findGroup(stored, id)
	if !v.seesBoard(target.BoardID) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !v.admin && !v.canWrite(resCampaigns, campaignRef(target)) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := exp.check(target, true); err != nil {
		s.failWrite(w, err)
		return
	}
	if !s.commitGroups(w, r, v, stored, full, groupWrite{exp: exp, id: id}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// commitGroups merges full into stored, assigns missing campaign numbers, checks links and streams, and saves.
// A new campaign with an empty id or a sequential stub (flow_0000, flow_0001, flow plus digits) receives a
// random slug before the merge. A campaign already in stored keeps its id. A restore keeps the id it had.
// Shares above 100% are refused only for campaigns that differ from stored.
// A non-admin save that adds or edits a link with a macro the caller may not write, or
// rebinds a hidden keyword, Sub ID or price to another click parameter, is refused with 400.
// w receives writeErr when a check or Save fails. r carries the request context.
// v is the caller. stored is the list read under groupsMu. full is the complete candidate
// list, never a single row: a non-admin merge of one row would drop their other campaigns.
// The caller holds groupsMu. It returns true after Save and writes nothing on success.
// It returns false after writeErr. A nil Groups store is rejected by the caller.
// gw carries the precondition of an assistant write; the zero value is a plain save.
// When gw.fromSave is set, an empty full that would cause stored campaigns to disappear
// returns 409 "empty list would delete campaigns; use DELETE /api/groups/{id}".
func (s *Server) commitGroups(w http.ResponseWriter, r *http.Request, v viewer, stored, full []config.Group, gw groupWrite) bool {
	if !gw.restore {
		replaced, err := fillNewCampaignIDs(stored, full)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "an id cannot be generated")
			return false
		}
		if next, ok := replaced[gw.id]; ok {
			gw.id = next
		}
	}
	groups, code, msg := mergeGroups(v, stored, full)
	if code != 0 {
		writeErr(w, code, msg)
		return false
	}
	if gw.fromSave && len(full) == 0 {
		merged := make(map[string]struct{}, len(groups))
		for _, g := range groups {
			merged[g.ID] = struct{}{}
		}
		for _, g := range stored {
			if _, ok := merged[g.ID]; !ok {
				writeErr(w, http.StatusConflict, "empty list would delete campaigns; use DELETE /api/groups/{id}")
				return false
			}
		}
	}
	if code, msg := s.saveGroups(r.Context(), v, stored, groups, gw); code != 0 {
		writeErr(w, code, msg)
		return false
	}
	return true
}

// groupWrite ties one campaign write to the precondition of an assistant request.
// The zero value is a plain save with no callbacks.
type groupWrite struct {
	// exp is the precondition whose OnBefore and OnAfter run inside saveGroups. Nil runs none.
	exp *aiExpect
	// id is the campaign the write is about. OnAfter gets that row from the saved list;
	// OnBefore gets the stored row, or for a restore the row to restore.
	id string
	// restore marks a put-back of a deleted campaign: a link that no longer holds is
	// 409 ai_conflict instead of 400.
	restore bool
	// fromSave marks the call that comes from handleGroupsSave (PUT /api/groups with the full
	// body). When set, commitGroups refuses an empty body that would silently delete stored
	// campaigns: the caller must DELETE /api/groups/{id} one by one instead.
	fromSave bool
	// relinked, when set, receives the campaigns whose destinations saveGroups moved from the old
	// tracker to a campaign of this panel (see relinkNewCampaigns), so the caller can report them.
	relinked *[]config.Group
}

// saveGroups checks and saves the candidate campaign list; every campaign write ends here.
// It checks links, the rules of a non-admin caller, ids and folders, changed shares and Sub IDs,
// numbers the rows that have none, checks streams, runs OnBefore right before the save and
// OnAfter right after it with the saved row of gw.id (a delete has none).
// v is the caller, stored the list read under groupsMu and groups the candidate list, which is
// updated in place. The caller holds groupsMu.
// It returns 0 and "" after the save, otherwise the HTTP status and message:
// 400 for a refused change (409 ai_conflict for a restore whose link is gone), 500 for a store failure,
// and the answer of an OnBefore refusal.
// A campaign added by this write, such as a copy, whose destination is still a link of the old tracker
// is sent to that campaign in this panel first (relinkNewCampaigns). A restore and an assistant write
// keep their destinations as given.
func (s *Server) saveGroups(ctx context.Context, v viewer, stored, groups []config.Group, gw groupWrite) (int, string) {
	if !gw.restore && gw.exp == nil {
		rows := relinkNewCampaigns(v, stored, groups, s.keitaroCreds().host())
		if gw.relinked != nil {
			*gw.relinked = append(*gw.relinked, rows...)
		}
	}
	cat := s.loadLinkCatalog(ctx, groups)
	if msg := validateGroupLinks(v, stored, groups, cat); msg != "" {
		if gw.restore {
			return http.StatusConflict, conflictMsg(msg)
		}
		return http.StatusBadRequest, msg
	}
	if code, msg := s.checkUserGroups(ctx, v, stored, groups); code != 0 {
		return code, msg
	}
	if msg := prepareGroups(groups); msg != "" {
		return http.StatusBadRequest, msg
	}
	if msg := validateChangedShares(stored, groups); msg != "" {
		return http.StatusBadRequest, msg
	}
	if msg := validateChangedSubIDs(stored, groups); msg != "" {
		return http.StatusBadRequest, msg
	}
	assignGroupNums(groups)
	offers, err := s.offerList(ctx)
	if err != nil {
		return http.StatusInternalServerError, "read error"
	}
	if msg := validateStreams(groups, offers); msg != "" {
		return http.StatusBadRequest, msg
	}
	if gw.exp != nil {
		src := stored
		if gw.restore {
			src = groups
		}
		if cur, ok := findGroup(src, gw.id); ok {
			if err := gw.exp.before(cur); err != nil {
				return s.aiFail(err)
			}
		}
	}
	if err := s.cfg.Groups.Save(ctx, groups); err != nil {
		return http.StatusInternalServerError, "save error"
	}
	if gw.exp != nil {
		if saved, ok := findGroup(groups, gw.id); ok {
			gw.exp.after(saved)
		}
	}
	return 0, ""
}

// checkUserGroups runs the checks that apply to a non-admin caller only: macros the caller may
// not write and hidden keywords, Sub IDs or prices rebound to another click parameter.
// v is the caller, stored the list before the change and groups the candidate list.
// An administrator passes. It returns 0 and "" when allowed, otherwise the HTTP status and message
// (400 for a refused change, 500 when the source list cannot be read).
func (s *Server) checkUserGroups(ctx context.Context, v viewer, stored, groups []config.Group) (int, string) {
	if v.admin {
		return 0, ""
	}
	if msg := validateGroupMacros(v, stored, groups); msg != "" {
		return http.StatusBadRequest, msg
	}
	sources, err := s.hiddenSourceList(ctx, v)
	if err != nil {
		return http.StatusInternalServerError, "read error"
	}
	if msg := validateGroupParams(v, stored, groups, sources); msg != "" {
		return http.StatusBadRequest, msg
	}
	return 0, ""
}

// restoreGroup puts a deleted campaign back as it was stored, with the same id, owner and section.
// raw is the campaign taken before the delete. It does not go through mergeGroups, which would
// replace CreatedBy and BoardID. Under groupsMu the id and the aliases are checked again, a Num
// that another campaign now holds is cleared and numbered again (renumbered reports that), and
// the candidate list goes through saveGroups, the same checks and save as every other write.
// The viewer of ctx needs the create right, the section and the write right on the campaign.
// The aiExpect of ctx, when set, gets OnBefore with the campaign to restore and OnAfter with the saved one.
// It returns the stored campaign and http.StatusOK, or a zero campaign and an error status:
// 409 ai_conflict (id or alias taken, a link points at a missing record, the section was deleted),
// 400 for an unreadable or invalid campaign, 403 for a missing right, 503 without a store,
// 500 for a store failure.
func (s *Server) restoreGroup(ctx context.Context, raw json.RawMessage) (g config.Group, renumbered bool, code int, msg string) {
	if s.cfg.Groups == nil {
		return g, false, http.StatusServiceUnavailable, "groups store unavailable"
	}
	var cand config.Group
	if err := json.Unmarshal(raw, &cand); err != nil || cand.ID == "" {
		return g, false, http.StatusBadRequest, "bad json"
	}
	v := s.viewerFrom(ctx)
	if !v.canCreate(resCampaigns) || !s.canRestoreOnBoard(ctx, v, cand.BoardID) ||
		(!v.admin && !v.canWrite(resCampaigns, campaignRef(cand))) {
		return g, false, http.StatusForbidden, "forbidden"
	}
	if code, msg := s.restoreBoardCheck(ctx, cand.BoardID); code != 0 {
		return g, false, code, msg
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	stored, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return g, false, http.StatusInternalServerError, "groups error"
	}
	if _, taken := findGroup(stored, cand.ID); taken || aliasTakenAmong(stored, "", cand.ID) {
		return g, false, http.StatusConflict, conflictMsg("campaign id is taken")
	}
	for _, a := range cand.Aliases {
		if a != "" && aliasTakenAmong(stored, "", a) {
			return g, false, http.StatusConflict, conflictMsg("alias is taken: " + a)
		}
	}
	merged := append(append(make([]config.Group, 0, len(stored)+1), stored...), cand)
	last := len(merged) - 1
	if merged[last].Num > 0 {
		for _, other := range stored {
			if other.Num == merged[last].Num {
				merged[last].Num = 0
				renumbered = true
				break
			}
		}
	}
	if code, msg := s.saveGroups(ctx, v, stored, merged, groupWrite{exp: aiExpectFrom(ctx), id: cand.ID, restore: true}); code != 0 {
		return g, false, code, msg
	}
	return merged[last], renumbered, http.StatusOK, ""
}

// replaceGroup returns a new slice with g in place of the row whose ID equals g.ID.
// stored is not modified. g is the replacement row. When no row has that id, g is appended.
// It does not return an error.
func replaceGroup(stored []config.Group, g config.Group) []config.Group {
	full := make([]config.Group, len(stored), len(stored)+1)
	copy(full, stored)
	for i := range full {
		if full[i].ID == g.ID {
			full[i] = g
			return full
		}
	}
	return append(full, g)
}

// dropGroup returns a new slice without the row whose ID equals id.
// stored is not modified. id is the campaign id to remove.
// The bool is false when id is absent, and the slice is nil in that case.
// It does not return an error.
func dropGroup(stored []config.Group, id string) ([]config.Group, bool) {
	idx := -1
	for i := range stored {
		if stored[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, false
	}
	full := make([]config.Group, 0, len(stored)-1)
	full = append(full, stored[:idx]...)
	full = append(full, stored[idx+1:]...)
	return full, true
}

func (s *Server) handleListsIndex(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lists == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	names, err := s.cfg.Lists.Names()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lists error")
		return
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleListRead(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lists == nil {
		writeErr(w, http.StatusServiceUnavailable, "lists unavailable")
		return
	}
	content, err := s.cfg.Lists.Read(r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": r.PathValue("name"), "content": content})
}

func (s *Server) handleListWrite(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lists == nil {
		writeErr(w, http.StatusServiceUnavailable, "lists unavailable")
		return
	}
	var body struct{ Content string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.cfg.Lists.Write(r.PathValue("name"), body.Content); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// prepareGroups trims folder labels and checks group ids.
// groups is updated in place. It returns an empty string when every id is
// non-empty and unique and every folder is at most 64 runes.
// Otherwise it returns the client message. It does not return an error.
func prepareGroups(groups []config.Group) string {
	seen := map[string]bool{}
	for i := range groups {
		if groups[i].ID == "" {
			return "group id required"
		}
		if seen[groups[i].ID] {
			return "duplicate group id: " + groups[i].ID
		}
		seen[groups[i].ID] = true
		groups[i].Folder = strings.TrimSpace(groups[i].Folder)
		if utf8.RuneCountInString(groups[i].Folder) > maxFolderRunes {
			return "folder is longer than 64 characters: " + groups[i].ID
		}
		if utf8.RuneCountInString(groups[i].Notes) > maxNotesRunes {
			return "notes are longer than 10000 characters: " + groups[i].ID
		}
		if msg := prepareGroupParams(&groups[i]); msg != "" {
			return msg
		}
	}
	return ""
}

// prepareGroupParams trims the campaign parameter names and tokens.
// g is updated in place. Empty tokens are dropped. An empty map becomes nil.
// It returns "unknown parameter <key>" when a key is not a source parameter,
// "parameter is too long" when a value exceeds config.MaxValueBytes, and ""
// when the maps are acceptable.
func prepareGroupParams(g *config.Group) string {
	if msg := cleanParamMap(&g.Parameters, true); msg != "" {
		return msg
	}
	return cleanParamMap(&g.ParamTokens, false)
}

// cleanParamMap trims every value of m and checks the keys.
// keepEmpty leaves a blank value in place, so a campaign can clear a source
// name. Otherwise a blank value is removed. m is set to nil when nothing remains.
// It returns the client message, or "" when the map is acceptable.
func cleanParamMap(m *map[string]string, keepEmpty bool) string {
	if len(*m) == 0 {
		*m = nil
		return ""
	}
	for k, v := range *m {
		if !config.AllowedSourceParam(k) {
			return "unknown parameter " + k
		}
		v = strings.TrimSpace(v)
		if len(v) > config.MaxValueBytes {
			return "parameter is too long"
		}
		if v == "" && !keepEmpty {
			delete(*m, k)
			continue
		}
		(*m)[k] = v
	}
	if len(*m) == 0 {
		*m = nil
	}
	return ""
}

// assignGroupNums gives the next campaign number to every row whose Num is not positive.
// groups is updated in place. A nil slice is unchanged.
// The start is the largest Num greater than zero, or zero when none exist.
// Each Num less than or equal to zero, in slice order, becomes that start plus one, then plus two, and so on.
// A Num greater than zero is left as stored, including duplicates.
// It does not return an error.
func assignGroupNums(groups []config.Group) {
	if groups == nil {
		return
	}
	maxNum := 0
	for i := range groups {
		if groups[i].Num > maxNum {
			maxNum = groups[i].Num
		}
	}
	for i := range groups {
		if groups[i].Num <= 0 {
			maxNum++
			groups[i].Num = maxNum
		}
	}
}
