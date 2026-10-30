package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// handleS2SGet writes the s2s catalog of the current section.
// The response is a JSON array. A missing file is an empty array, never null.
// A nil store is 503. A read error is 500. Success is 200.
func (s *Server) handleS2SGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.S2S == nil {
		writeErr(w, http.StatusServiceUnavailable, "s2s store unavailable")
		return
	}
	list, err := s.cfg.S2S.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	if list == nil {
		list = []config.OutboundPostback{}
	}
	v := s.viewerFrom(r.Context())
	out := make([]config.OutboundPostback, 0, len(list))
	for _, p := range list {
		if !config.SameBoard(p.BoardID, v.boardCurrent) {
			continue
		}
		ref := ref{id: p.ID, board: p.BoardID}
		if v.admin || v.canSee(resS2S, ref) {
			out = append(out, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleS2SPut replaces the current section rows in the s2s catalog with the JSON array in the body.
// Rows of other sections are preserved. The body is at most 1 MiB.
// A nil store is 503. A bad body is 400. A save error is 500.
// Success writes 200 and the stored array of the current section.
func (s *Server) handleS2SPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.S2S == nil {
		writeErr(w, http.StatusServiceUnavailable, "s2s store unavailable")
		return
	}
	var list []config.OutboundPostback
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&list); err != nil || list == nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	v := s.viewerFrom(r.Context())
	if !v.admin && !v.boardRoleWrite(resS2S) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	board := v.boardCurrent
	for i := range list {
		list[i].BoardID = board
	}
	if msg := prepareS2S(list); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	stored, err := s.cfg.S2S.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	var other []config.OutboundPostback
	for _, p := range stored {
		if !config.SameBoard(p.BoardID, board) {
			other = append(other, p)
		}
	}
	merged := append(other, list...)
	if msg, code := s.removedS2SUse(r.Context(), merged, board); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if err := s.cfg.S2S.Save(r.Context(), merged); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// prepareS2S checks and fills list in place.
// list is the catalog from the request. An empty method becomes GET and the method is uppercased.
// An empty id becomes PostbackID of the signature. A duplicate id is reported.
// It returns the client message, or an empty string when every row is valid.
// It does not return an error.
func prepareS2S(list []config.OutboundPostback) string {
	seen := make(map[string]struct{}, len(list))
	for i := range list {
		if strings.TrimSpace(list[i].Method) == "" {
			list[i].Method = http.MethodGet
		}
		list[i].Method = strings.ToUpper(strings.TrimSpace(list[i].Method))
		if msg := validPostback(list[i]); msg != "" {
			return msg
		}
		list[i].ID = strings.TrimSpace(list[i].ID)
		if list[i].ID == "" {
			list[i].ID = config.PostbackID(list[i].Signature())
		}
		if _, ok := seen[list[i].ID]; ok {
			return "duplicate id"
		}
		seen[list[i].ID] = struct{}{}
	}
	return ""
}

// removedS2SUse reports a catalog id that next drops while a campaign of board still lists it.
// ctx is the request context. next is the full prepared catalog. board is the section being saved.
// The caller holds groupsMu. The string is the client message and the int is the HTTP status.
// Both are zero when every removed id is unused. A read error is 500.
func (s *Server) removedS2SUse(ctx context.Context, next []config.OutboundPostback, board string) (string, int) {
	prev, err := s.cfg.S2S.List(ctx)
	if err != nil {
		return "read error", http.StatusInternalServerError
	}
	keep := make(map[string]struct{}, len(next))
	for _, p := range next {
		keep[p.ID] = struct{}{}
	}
	if s.cfg.Groups == nil {
		return "", 0
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return "groups error", http.StatusInternalServerError
	}
	var section []config.Group
	for _, g := range groups {
		if groupInBoard(g, board) {
			section = append(section, g)
		}
	}
	used := postbackIDUse(section)
	for _, p := range prev {
		if p.ID == "" {
			continue
		}
		if !config.SameBoard(p.BoardID, board) {
			continue
		}
		if _, ok := keep[p.ID]; ok {
			continue
		}
		if n := used[p.ID]; n > 0 {
			return fmt.Sprintf("template %s is used by %d campaigns", p.ID, n), http.StatusBadRequest
		}
	}
	return "", 0
}

// postbackIDUse counts campaigns that list each postback id.
// groups is the campaign list. An id repeated inside one campaign counts once.
// The return maps an id to the number of campaigns. There is no error.
func postbackIDUse(groups []config.Group) map[string]int {
	counts := map[string]int{}
	for _, g := range groups {
		seen := map[string]struct{}{}
		for _, id := range g.PostbackIDs {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			counts[id]++
		}
	}
	return counts
}
