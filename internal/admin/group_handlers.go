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
	writeJSON(w, http.StatusOK, groups)
}

// maxFolderRunes is the longest campaign folder label the admin accepts.
// The count is runes, so a 64-letter Cyrillic label still fits.
const maxFolderRunes = 64

// maxGroupsBody is the largest PUT /api/groups body.
// Several thousand imported campaigns are saved as one array.
const maxGroupsBody = 128 << 20

func (s *Server) handleGroupsSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var groups []config.Group
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGroupsBody)).Decode(&groups); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if msg := prepareGroups(groups); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	offers, err := s.offerList(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	if msg := validateStreams(groups, offers); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.cfg.Groups.Save(r.Context(), groups); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
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
