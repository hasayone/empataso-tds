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
		if !v.seesBoard(p.BoardID) {
			continue
		}
		ref := ref{id: p.ID, board: p.BoardID}
		if v.admin || v.canSee(resS2S, ref) {
			out = append(out, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleS2SPut replaces the visible s2s rows with the JSON array in the body.
// A known id keeps its stored section. A new id gets the current section. The same id is not stored twice.
// A template missing from the body is removed when the session can see its section.
// A template of a hidden section stays. The body is at most 1 MiB.
// A nil store is 503. A bad body is 400. A template still used by a campaign is 400. A save error is 500.
// Success writes 200 and the accepted rows with their stored section ids.
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
	board := v.boardCurrent
	if !v.admin && !v.boardRoleWrite(resS2S, ref{board: board}) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
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
	merged, response := mergeS2SList(v, stored, list)
	if !v.admin && !s2sWriteAllowed(v, stored, response) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if msg, code := s.removedS2SUse(r.Context(), merged); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if err := s.cfg.S2S.Save(r.Context(), merged); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// mergeS2SList combines stored templates with body for the session v.
// stored is the catalog on disk. body rows already have ids from prepareS2S.
// A known id keeps its stored BoardID. A new id gets the current section.
// A template missing from body is dropped when its section is the current one
// or v.seesBoard allows that section. A template the session cannot see stays.
// The same id is not stored twice.
// It returns the catalog to save and the body rows with BoardID filled.
// There is no error.
func mergeS2SList(v viewer, stored, body []config.OutboundPostback) (merged, response []config.OutboundPostback) {
	board := v.boardCurrent
	storedByID := make(map[string]config.OutboundPostback, len(stored))
	for _, p := range stored {
		storedByID[p.ID] = p
	}
	response = append([]config.OutboundPostback(nil), body...)
	inBody := make(map[string]struct{}, len(response))
	for i := range response {
		inBody[response[i].ID] = struct{}{}
		if old, ok := storedByID[response[i].ID]; ok {
			response[i].BoardID = old.BoardID
			continue
		}
		response[i].BoardID = board
	}
	for _, p := range stored {
		if _, ok := inBody[p.ID]; ok {
			continue
		}
		if config.SameBoard(p.BoardID, board) || v.seesBoard(p.BoardID) {
			continue
		}
		merged = append(merged, p)
	}
	merged = append(merged, response...)
	return merged, response
}

// s2sWriteAllowed reports whether v may save body over stored.
// An unchanged row is allowed. A new or changed row requires canWrite on its section.
// It returns false when a write is denied. There is no error.
func s2sWriteAllowed(v viewer, stored, body []config.OutboundPostback) bool {
	byID := make(map[string]config.OutboundPostback, len(stored))
	for _, p := range stored {
		byID[p.ID] = p
	}
	for _, p := range body {
		if old, ok := byID[p.ID]; ok && sameOutbound(old, p) {
			continue
		}
		if !v.canWrite(resS2S, ref{id: p.ID, board: p.BoardID}) {
			return false
		}
	}
	return true
}

// sameOutbound reports whether a and b are the same template, including section and statuses.
// It does not return an error.
func sameOutbound(a, b config.OutboundPostback) bool {
	if a.ID != b.ID || a.Name != b.Name || a.URL != b.URL || a.Method != b.Method || a.BoardID != b.BoardID {
		return false
	}
	if len(a.Statuses) != len(b.Statuses) {
		return false
	}
	for i := range a.Statuses {
		if a.Statuses[i] != b.Statuses[i] {
			return false
		}
	}
	return true
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

// removedS2SUse reports a catalog id that next drops while any campaign still lists it.
// ctx is the request context. next is the full prepared catalog.
// Usage is counted on every campaign, not only the section being saved.
// The caller holds groupsMu. The string is the client message and the int is the HTTP status.
// Both are zero when every removed id is unused. A read error is 500.
func (s *Server) removedS2SUse(ctx context.Context, next []config.OutboundPostback) (string, int) {
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
	used := postbackIDUse(groups)
	for _, p := range prev {
		if p.ID == "" {
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
