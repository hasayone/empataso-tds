package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

const boardHeader = "X-Kuztds-Board"

const boardKey ctxKey = 1

type boardSession struct {
	current    string
	boardsAll  bool
	switchable bool
	defaultID  string
	role       string
}

// boardFrom returns the normalized current section id from ctx, or main when unset.
func boardFrom(ctx context.Context) string {
	if b, ok := ctx.Value(boardKey).(boardSession); ok && b.current != "" {
		return config.NormalizeBoardID(b.current)
	}
	return config.MainBoardID
}

// boardsAllFrom reports whether ctx may open every section (postback-log and similar).
func boardsAllFrom(ctx context.Context) bool {
	if b, ok := ctx.Value(boardKey).(boardSession); ok {
		return b.boardsAll
	}
	return true
}

// boardScope resolves the request section from X-Kuztds-Board and stores it in ctx.
// A header naming a section the session may not open answers 403. An empty header uses the user default.
func (s *Server) boardScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		_, meta, err := s.visibleBoardItems(r.Context(), sess)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "boards error")
			return
		}
		raw := strings.TrimSpace(r.Header.Get(boardHeader))
		current := meta.defaultID
		if raw != "" {
			want := config.NormalizeBoardID(raw)
			if !meta.canOpen(want) {
				writeErr(w, http.StatusForbidden, "forbidden")
				return
			}
			current = want
		} else if !meta.canOpen(current) {
			if len(meta.visibleIDs) > 0 {
				current = meta.visibleIDs[0]
			} else {
				current = config.MainBoardID
			}
		}
		role := ""
		if !meta.all && sess.UID != "" {
			role, _ = s.boardMemberRole(r.Context(), current, sess.UID)
		}
		bs := boardSession{
			current:    current,
			boardsAll:  meta.all,
			switchable: meta.switchable,
			defaultID:  meta.defaultID,
			role:       role,
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), boardKey, bs)))
	})
}

type boardMeta struct {
	all         bool
	switchable  bool
	defaultID   string
	visibleIDs  []string
	visibleSet  map[string]bool
}

func (m boardMeta) canOpen(id string) bool {
	id = config.NormalizeBoardID(id)
	if m.all {
		return true
	}
	return m.visibleSet[id]
}

// boardMeta loads section visibility for sess and picks default_id.
func (s *Server) boardMeta(ctx context.Context, sess security.Session) (boardMeta, error) {
	envAdmin := sess.UID == ""
	if sess.UID != "" {
		u, ok := s.userByID(sess.UID)
		if !ok {
			return boardMeta{all: false, defaultID: config.MainBoardID, visibleSet: map[string]bool{}}, nil
		}
		ids, all := u.VisibleBoards(false)
		def := config.NormalizeBoardID(u.DefaultBoard)
		if def == "" {
			def = config.MainBoardID
		}
		if !all && !containsBoardID(ids, def) {
			if len(ids) > 0 {
				def = ids[0]
			} else {
				def = config.MainBoardID
			}
		}
		set := map[string]bool{}
		for _, id := range ids {
			set[config.NormalizeBoardID(id)] = true
		}
		return boardMeta{
			all:        all,
			switchable: len(ids) > 1,
			defaultID:  def,
			visibleIDs: ids,
			visibleSet: set,
		}, nil
	}
	_ = envAdmin
	return boardMeta{all: true, defaultID: config.MainBoardID, visibleSet: nil}, nil
}

func containsBoardID(ids []string, id string) bool {
	id = config.NormalizeBoardID(id)
	for _, x := range ids {
		if config.SameBoard(x, id) {
			return true
		}
	}
	return false
}

// visibleBoardItems returns board rows the session may list in GET /api/boards.
func (s *Server) visibleBoardItems(ctx context.Context, sess security.Session) ([]config.Board, boardMeta, error) {
	meta, err := s.boardMeta(ctx, sess)
	if err != nil {
		return nil, meta, err
	}
	all, err := listBoards(ctx, s.cfg.Boards)
	if err != nil {
		return nil, meta, err
	}
	if meta.all {
		meta.switchable = len(all) > 1
		return boardsResponseItems(all, s.canManageBoards(sess)), meta, nil
	}
	out := make([]config.Board, 0, len(meta.visibleIDs))
	byID := map[string]config.Board{}
	for _, b := range all {
		byID[config.NormalizeBoardID(b.ID)] = b
	}
	for _, id := range meta.visibleIDs {
		n := config.NormalizeBoardID(id)
		if b, ok := byID[n]; ok {
			out = append(out, b)
		} else if config.SameBoard(n, config.MainBoardID) {
			out = append(out, mainBoardRecord())
		}
	}
	if len(out) == 0 {
		out = []config.Board{mainBoardRecord()}
	}
	meta.switchable = len(out) > 1
	return boardsResponseItems(out, s.canManageBoards(sess)), meta, nil
}

// boardMemberRole returns the member role of uid on boardID, or empty when uid is not listed.
func (s *Server) boardMemberRole(ctx context.Context, boardID, uid string) (string, error) {
	if s.cfg.Boards == nil || uid == "" {
		return "", nil
	}
	all, err := listBoards(ctx, s.cfg.Boards)
	if err != nil {
		return "", err
	}
	boardID = config.NormalizeBoardID(boardID)
	for _, b := range all {
		if !config.SameBoard(b.ID, boardID) {
			continue
		}
		for _, m := range b.Members {
			if m.UserID == uid {
				return m.Role, nil
			}
		}
		return "", nil
	}
	return "", nil
}

// boardsResponseItems returns a copy of items, clearing Members when manage is false.
func boardsResponseItems(items []config.Board, manage bool) []config.Board {
	if manage {
		return items
	}
	out := make([]config.Board, len(items))
	for i, b := range items {
		out[i] = b
		out[i].Members = nil
	}
	return out
}

func (s *Server) canManageBoards(sess security.Session) bool {
	if sess.UID == "" {
		return true
	}
	u, ok := s.userByID(sess.UID)
	if !ok {
		return false
	}
	if u.Type == security.UserAdmin {
		return true
	}
	_, all := u.VisibleBoards(false)
	return all
}

func groupInBoard(g config.Group, board string) bool {
	return config.SameBoard(g.BoardID, board)
}
