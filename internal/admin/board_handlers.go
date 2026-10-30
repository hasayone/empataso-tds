package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/egerkuzma/kuztds/internal/config"
)

type boardsResponse struct {
	Items      []config.Board `json:"items"`
	Current    string         `json:"current"`
	DefaultID  string         `json:"default_id"`
	Switchable bool           `json:"switchable"`
}

type boardCreateBody struct {
	Name      string `json:"name"`
	Icon      string `json:"icon,omitempty"`
	IconColor string `json:"icon_color,omitempty"`
}

// handleBoardsGet lists sections visible to the session and the active section metadata.
func (s *Server) handleBoardsGet(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	items, meta, err := s.visibleBoardItems(r.Context(), sess)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "boards error")
		return
	}
	writeJSON(w, http.StatusOK, boardsResponse{
		Items:      items,
		Current:    boardFrom(r.Context()),
		DefaultID:  meta.defaultID,
		Switchable: meta.switchable,
	})
}

// handleBoardsPost creates a section. Only env-admin, ADMIN, or users with all sections may call it.
func (s *Server) handleBoardsPost(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.canManageBoards(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.cfg.Boards == nil {
		writeErr(w, http.StatusServiceUnavailable, "boards store unavailable")
		return
	}
	var body boardCreateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	created, err := createBoard(r.Context(), s.cfg.Boards, body.Name, body.Icon, body.IconColor)
	if err != nil {
		writeBoardErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

type boardOrderBody struct {
	IDs []string `json:"ids"`
}

// handleBoardsOrderPut saves the section order. Main stays first.
// Only env-admin, ADMIN, or users with all sections may call it.
func (s *Server) handleBoardsOrderPut(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.canManageBoards(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.cfg.Boards == nil {
		writeErr(w, http.StatusServiceUnavailable, "boards store unavailable")
		return
	}
	var body boardOrderBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	items, err := reorderBoards(r.Context(), s.cfg.Boards, body.IDs)
	if err != nil {
		writeBoardErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleBoardPatch updates name and/or icon on one section.
func (s *Server) handleBoardPatch(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.canManageBoards(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.cfg.Boards == nil {
		writeErr(w, http.StatusServiceUnavailable, "boards store unavailable")
		return
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	patch, err := decodeBoardPatch(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	updated, err := updateBoard(r.Context(), s.cfg.Boards, r.PathValue("id"), patch)
	if err != nil {
		writeBoardErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleBoardDelete removes an empty section from boards.json.
func (s *Server) handleBoardDelete(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.canManageBoards(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.cfg.Boards == nil {
		writeErr(w, http.StatusServiceUnavailable, "boards store unavailable")
		return
	}
	id := config.NormalizeBoardID(r.PathValue("id"))
	if config.SameBoard(id, config.MainBoardID) {
		writeErr(w, http.StatusBadRequest, "cannot delete main board")
		return
	}
	ctx := r.Context()
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	inUse, err := s.boardCatalogInUse(ctx, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "boards error")
		return
	}
	if inUse {
		writeErr(w, http.StatusConflict, "board in use")
		return
	}
	if err := deleteBoard(ctx, s.cfg.Boards, id); err != nil {
		writeBoardErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// boardCatalogInUse reports whether boardID owns at least one catalog row.
// Nil stores are treated as empty.
func (s *Server) boardCatalogInUse(ctx context.Context, boardID string) (bool, error) {
	if s.cfg.Groups != nil {
		list, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return false, err
		}
		for _, g := range list {
			if config.SameBoard(g.BoardID, boardID) {
				return true, nil
			}
		}
	}
	if s.cfg.Offers != nil {
		list, err := s.cfg.Offers.List(ctx)
		if err != nil {
			return false, err
		}
		for _, o := range list {
			if config.SameBoard(o.BoardID, boardID) {
				return true, nil
			}
		}
	}
	if s.cfg.Landings != nil {
		list, err := s.cfg.Landings.List(ctx)
		if err != nil {
			return false, err
		}
		for _, l := range list {
			if config.SameBoard(l.BoardID, boardID) {
				return true, nil
			}
		}
	}
	if s.cfg.Networks != nil {
		list, err := s.cfg.Networks.List(ctx)
		if err != nil {
			return false, err
		}
		for _, n := range list {
			if config.SameBoard(n.BoardID, boardID) {
				return true, nil
			}
		}
	}
	if s.cfg.Sources != nil {
		list, err := s.cfg.Sources.List(ctx)
		if err != nil {
			return false, err
		}
		for _, src := range list {
			if config.SameBoard(src.BoardID, boardID) {
				return true, nil
			}
		}
	}
	if s.cfg.S2S != nil {
		list, err := s.cfg.S2S.List(ctx)
		if err != nil {
			return false, err
		}
		for _, p := range list {
			if config.SameBoard(p.BoardID, boardID) {
				return true, nil
			}
		}
	}
	return false, nil
}

func writeBoardErr(w http.ResponseWriter, err error) {
	switch err {
	case ErrBoardName:
		writeErr(w, http.StatusBadRequest, "name required")
	case ErrBoardMainCreate:
		writeErr(w, http.StatusBadRequest, "cannot create main board")
	case ErrBoardMainRename:
		writeErr(w, http.StatusBadRequest, "cannot rename main board")
	case ErrBoardMainDelete:
		writeErr(w, http.StatusBadRequest, "cannot delete main board")
	case ErrBoardNotFound:
		writeErr(w, http.StatusNotFound, "not found")
	case ErrBoardInUse:
		writeErr(w, http.StatusConflict, "board in use")
	case ErrBoardOrder:
		writeErr(w, http.StatusBadRequest, "bad board order")
	case config.ErrInvalidBoardIcon:
		writeErr(w, http.StatusBadRequest, "invalid icon")
	default:
		if err != nil && err.Error() == "admin: board patch empty" {
			writeErr(w, http.StatusBadRequest, "nothing to update")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}
