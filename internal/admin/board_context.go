package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

const (
	// boardHeader names the section header sent by the panel and API clients.
	boardHeader = "X-Emtds-Board"
	// legacyBoardHeader is the section header of clients built before the rename; it is still read.
	legacyBoardHeader = "X-Kuztds-Board"
)

// requestBoard returns the trimmed section header of r: X-Emtds-Board first, then X-Kuztds-Board.
func requestBoard(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(boardHeader)); v != "" {
		return v
	}
	return strings.TrimSpace(r.Header.Get(legacyBoardHeader))
}

const boardKey ctxKey = 1

type boardSession struct {
	current    string
	boardsAll  bool
	switchable bool
	defaultID  string
	role       string
	includeAll bool
	roles      map[string]string
	visible    map[string]bool
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

// boardScope resolves the request section from X-Emtds-Board and stores it in ctx.
// A header naming a section the session may not open answers 403. An empty header uses the user default.
func (s *Server) boardScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		_, meta, err := s.visibleBoardItems(r.Context(), sess)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "boards error")
			return
		}
		raw := requestBoard(r)
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
			role = meta.roles[config.NormalizeBoardID(current)]
		}
		bs := boardSession{
			current:    current,
			boardsAll:  meta.all,
			switchable: meta.switchable,
			defaultID:  meta.defaultID,
			role:       role,
			includeAll: meta.includeAll,
			roles:      meta.roles,
			visible:    meta.visibleSet,
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), boardKey, bs)))
	})
}

type boardMeta struct {
	all        bool
	switchable bool
	defaultID  string
	visibleIDs []string
	visibleSet map[string]bool
	includeAll bool
	roles      map[string]string
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
	meta.includeAll = boardListIncludeAll(all)
	if !meta.all && sess.UID != "" {
		meta.roles = boardMemberRoles(all, sess.UID)
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

// boardsResponseItems returns a copy of items for the client.
// items are the stored sections. manage keeps Members; otherwise Members are cleared.
// Main gets an explicit include_all bool. Other sections omit the field.
// The stored slice and its include_all pointers are not modified. There is no error.
func boardsResponseItems(items []config.Board, manage bool) []config.Board {
	out := make([]config.Board, len(items))
	for i, b := range items {
		out[i] = b
		if !manage {
			out[i].Members = nil
		}
		if config.SameBoard(b.ID, config.MainBoardID) {
			on := config.IncludeAllEnabled(b.IncludeAll)
			out[i].IncludeAll = &on
			continue
		}
		out[i].IncludeAll = nil
	}
	return out
}

// boardClientItem returns one section as the API shows it.
// b is the stored section. Members stay. Main gets an explicit include_all bool.
// The stored value is not modified. There is no error.
func boardClientItem(b config.Board) config.Board {
	return boardsResponseItems([]config.Board{b}, true)[0]
}

// boardListIncludeAll reads the main section flag from list.
// list is the stored sections. A missing main section is enabled.
// It returns whether the wide main view is on. There is no error.
func boardListIncludeAll(list []config.Board) bool {
	for _, b := range list {
		if config.SameBoard(b.ID, config.MainBoardID) {
			return config.IncludeAllEnabled(b.IncludeAll)
		}
	}
	return true
}

// boardMemberRoles maps a normalized section id to the member role of uid.
// list is the stored sections. uid is the session user. Sections without that user are absent.
// It returns nil when uid is empty. There is no error.
func boardMemberRoles(list []config.Board, uid string) map[string]string {
	if uid == "" {
		return nil
	}
	roles := make(map[string]string)
	for _, b := range list {
		for _, m := range b.Members {
			if m.UserID == uid {
				roles[config.NormalizeBoardID(b.ID)] = m.Role
				break
			}
		}
	}
	return roles
}

// canManageBoards reports whether sess may see board members and manage boards.
// The environment administrator, ADMIN keys and administrators may; a USER key never.
func (s *Server) canManageBoards(sess security.Session) bool {
	if isUserKey(sess) {
		return false
	}
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
