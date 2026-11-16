package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/egerkuzma/emtds/internal/config"
)

func (s *Server) handleCampaignPostbacksGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	id := r.PathValue("id")
	v := s.viewerFrom(r.Context())
	for _, g := range groups {
		if g.ID == id {
			if !v.seesBoard(g.BoardID) || !v.canSee(resCampaigns, campaignRef(g)) {
				writeErr(w, http.StatusNotFound, "not found")
				return
			}
			if g.Postbacks == nil {
				g.Postbacks = []config.OutboundPostback{}
			}
			writeJSON(w, http.StatusOK, g.Postbacks)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not found")
}

func (s *Server) handleCampaignPostbacksPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var list []config.OutboundPostback
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&list); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	for i := range list {
		if strings.TrimSpace(list[i].Method) == "" {
			list[i].Method = http.MethodGet
		}
		list[i].Method = strings.ToUpper(list[i].Method)
		if msg := validPostback(list[i]); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	v := s.viewerFrom(r.Context())
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	id := r.PathValue("id")
	found := false
	for i := range groups {
		if groups[i].ID == id {
			ref := campaignRef(groups[i])
			if !v.seesBoard(groups[i].BoardID) || !v.canSee(resCampaigns, ref) {
				writeErr(w, http.StatusNotFound, "not found")
				return
			}
			if !v.canWrite(resCampaigns, ref) {
				writeErr(w, http.StatusForbidden, "forbidden")
				return
			}
			if msg := hiddenMacroUse(v, list, groups[i].Postbacks); msg != "" {
				writeErr(w, http.StatusBadRequest, msg)
				return
			}
			groups[i].Postbacks = list
			found = true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.cfg.Groups.Save(r.Context(), groups); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
