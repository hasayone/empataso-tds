package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/egerkuzma/kuztds/internal/config"
)

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
	if !v.admin {
		filtered := make([]config.Group, 0, len(groups))
		for _, g := range groups {
			if v.canSee(resCampaigns, campaignRef(g)) {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
	}
	writeJSON(w, http.StatusOK, groups)
}

// maxFolderRunes is the longest campaign folder label the admin accepts.
// The count is runes, so a 64-letter Cyrillic label still fits.
const maxFolderRunes = 64

// maxGroupsBody is the largest PUT /api/groups body.
// Several thousand imported campaigns are saved as one array.
const maxGroupsBody = 128 << 20

// maxGroupBody is the largest PUT /api/groups/{id} body.
// The payload is one campaign, not the whole list.
const maxGroupBody = 8 << 20

// handleGroupsSave replaces the campaign list with the JSON array in the body.
// The body is the full list. A missing store is 503. A bad body is 400.
// Validation and save errors are written by commitGroups.
// Success writes 200 {"status":"saved"}.
func (s *Server) handleGroupsSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var body []config.Group
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGroupsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
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
	if !s.commitGroups(w, r, v, stored, body) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// handleGroupPut replaces one campaign or appends it, then saves the full list.
// The path id is the campaign id. An empty body id is taken from the path.
// A body id that differs from the path is 400 "id mismatch". A missing store is 503.
// A bad body is 400. Other errors match commitGroups. Success writes 200 {"status":"saved"}.
func (s *Server) handleGroupPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	id := r.PathValue("id")
	var body config.Group
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGroupBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if body.ID == "" {
		body.ID = id
	}
	if body.ID != id {
		writeErr(w, http.StatusBadRequest, "id mismatch")
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
	if !s.commitGroups(w, r, v, stored, replaceGroup(stored, body)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
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
	full, ok := dropGroup(stored, id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !s.commitGroups(w, r, v, stored, full) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// commitGroups merges full into stored, assigns missing campaign numbers, checks links and streams, and saves.
// w receives writeErr when a check or Save fails. r carries the request context.
// v is the caller. stored is the list read under groupsMu. full is the complete candidate
// list, never a single row: a non-admin merge of one row would drop their other campaigns.
// The caller holds groupsMu. It returns true after Save and writes nothing on success.
// It returns false after writeErr. A nil Groups store is rejected by the caller.
func (s *Server) commitGroups(w http.ResponseWriter, r *http.Request, v viewer, stored, full []config.Group) bool {
	groups, code, msg := mergeGroups(v, stored, full)
	if code != 0 {
		writeErr(w, code, msg)
		return false
	}
	if !v.admin {
		cat := s.loadLinkCatalog(r.Context(), groups)
		if msg := validateUserGroupLinks(v, stored, groups, cat); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return false
		}
	}
	if msg := prepareGroups(groups); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return false
	}
	assignGroupNums(groups)
	offers, err := s.offerList(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return false
	}
	if msg := validateStreams(groups, offers); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return false
	}
	if err := s.cfg.Groups.Save(r.Context(), groups); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return false
	}
	return true
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
