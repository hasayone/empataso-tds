package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

type boardMembersBody struct {
	Members []config.BoardMember `json:"members"`
}

type boardAssignBody struct {
	Folders []string `json:"folders"`
	Groups  struct {
		Offers   []string `json:"offers"`
		Landings []string `json:"landings"`
		Networks []string `json:"networks"`
		Sources  []string `json:"sources"`
	} `json:"groups"`
	IDs struct {
		Campaigns []string `json:"campaigns"`
		Offers    []string `json:"offers"`
		Landings  []string `json:"landings"`
		Networks  []string `json:"networks"`
		Sources   []string `json:"sources"`
		S2S       []string `json:"s2s"`
	} `json:"ids"`
}

// handleBoardMembersPut replaces the member list of one section and syncs user board_ids.
func (s *Server) handleBoardMembersPut(w http.ResponseWriter, r *http.Request) {
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
	var body boardMembersBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if body.Members == nil {
		body.Members = []config.BoardMember{}
	}
	if err := config.ValidateBoardMembers(body.Members); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid members")
		return
	}
	for _, m := range body.Members {
		if _, ok := s.userByID(m.UserID); !ok {
			writeErr(w, http.StatusBadRequest, "unknown user")
			return
		}
	}
	ctx := r.Context()
	raw, err := s.cfg.Boards.List(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "boards error")
		return
	}
	raw = boardsWithMain(raw)
	idx := boardIndex(raw, id)
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	affected := memberUserIDs(raw[idx].Members, body.Members)
	raw[idx].Members = append([]config.BoardMember(nil), body.Members...)
	if err := s.cfg.Boards.Save(ctx, raw); err != nil {
		writeErr(w, http.StatusInternalServerError, "boards error")
		return
	}
	all, err := listBoards(ctx, s.cfg.Boards)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "boards error")
		return
	}
	if err := s.syncUsersBoardIDsFromMembers(all, affected); err != nil {
		if errors.Is(err, security.ErrUserNotFound) {
			writeErr(w, http.StatusInternalServerError, "users error")
			return
		}
		writeErr(w, http.StatusInternalServerError, "users error")
		return
	}
	writeJSON(w, http.StatusOK, raw[idx])
}

// handleBoardAssignPost sets the full catalog composition of one section.
func (s *Server) handleBoardAssignPost(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if !s.canManageBoards(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	target := config.NormalizeBoardID(r.PathValue("id"))
	if config.SameBoard(target, config.MainBoardID) {
		writeErr(w, http.StatusBadRequest, "cannot assign main board")
		return
	}
	var body boardAssignBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	ctx := r.Context()
	if s.cfg.Boards != nil {
		all, err := listBoards(ctx, s.cfg.Boards)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "boards error")
			return
		}
		if boardIndex(all, target) < 0 {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	if err := s.applyBoardAssign(ctx, target, body); err != nil {
		writeBoardAssignErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func writeBoardAssignErr(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// applyBoardAssign moves catalog rows onto target or back to main per the desired composition.
// The caller holds groupsMu.
func (s *Server) applyBoardAssign(ctx context.Context, target string, body boardAssignBody) error {
	folders := stringSet(body.Folders)
	campIDs := stringSet(body.IDs.Campaigns)
	if s.cfg.Groups != nil {
		list, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return err
		}
		for i := range list {
			g := &list[i]
			if !catalogOnMainOrTarget(g.BoardID, target) {
				continue
			}
			want := campIDs[g.ID] || (g.Folder != "" && folders[g.Folder])
			if want {
				g.BoardID = targetBoardStoredID(target)
			} else if config.SameBoard(g.BoardID, target) {
				g.BoardID = ""
			}
		}
		if err := s.cfg.Groups.Save(ctx, list); err != nil {
			return err
		}
	}
	if err := s.assignCatalogOffers(ctx, target, body); err != nil {
		return err
	}
	if err := s.assignCatalogLandings(ctx, target, body); err != nil {
		return err
	}
	if err := s.assignCatalogNetworks(ctx, target, body); err != nil {
		return err
	}
	if err := s.assignCatalogSources(ctx, target, body); err != nil {
		return err
	}
	if err := s.assignCatalogS2S(ctx, target, body.IDs.S2S); err != nil {
		return err
	}
	return nil
}

func (s *Server) assignCatalogOffers(ctx context.Context, target string, body boardAssignBody) error {
	if s.cfg.Offers == nil {
		return nil
	}
	labels := stringSet(body.Groups.Offers)
	ids := stringSet(body.IDs.Offers)
	defer s.cfg.Offers.lock()()
	list, err := s.cfg.Offers.List(ctx)
	if err != nil {
		return err
	}
	for i := range list {
		o := &list[i]
		if !catalogOnMainOrTarget(o.BoardID, target) {
			continue
		}
		want := ids[o.ID] || (o.Group != "" && labels[o.Group])
		if want {
			o.BoardID = targetBoardStoredID(target)
		} else if config.SameBoard(o.BoardID, target) {
			o.BoardID = ""
		}
	}
	return s.cfg.Offers.Save(ctx, list)
}

func (s *Server) assignCatalogLandings(ctx context.Context, target string, body boardAssignBody) error {
	if s.cfg.Landings == nil {
		return nil
	}
	labels := stringSet(body.Groups.Landings)
	ids := stringSet(body.IDs.Landings)
	defer s.cfg.Landings.lock()()
	list, err := s.cfg.Landings.List(ctx)
	if err != nil {
		return err
	}
	for i := range list {
		l := &list[i]
		if !catalogOnMainOrTarget(l.BoardID, target) {
			continue
		}
		want := ids[l.ID] || (l.Group != "" && labels[l.Group])
		if want {
			l.BoardID = targetBoardStoredID(target)
		} else if config.SameBoard(l.BoardID, target) {
			l.BoardID = ""
		}
	}
	return s.cfg.Landings.Save(ctx, list)
}

func (s *Server) assignCatalogNetworks(ctx context.Context, target string, body boardAssignBody) error {
	if s.cfg.Networks == nil {
		return nil
	}
	labels := stringSet(body.Groups.Networks)
	ids := stringSet(body.IDs.Networks)
	defer s.cfg.Networks.lock()()
	list, err := s.cfg.Networks.List(ctx)
	if err != nil {
		return err
	}
	for i := range list {
		n := &list[i]
		if !catalogOnMainOrTarget(n.BoardID, target) {
			continue
		}
		want := ids[n.ID] || (n.Group != "" && labels[n.Group])
		if want {
			n.BoardID = targetBoardStoredID(target)
		} else if config.SameBoard(n.BoardID, target) {
			n.BoardID = ""
		}
	}
	return s.cfg.Networks.Save(ctx, list)
}

func (s *Server) assignCatalogSources(ctx context.Context, target string, body boardAssignBody) error {
	if s.cfg.Sources == nil {
		return nil
	}
	labels := stringSet(body.Groups.Sources)
	ids := stringSet(body.IDs.Sources)
	defer s.cfg.Sources.lock()()
	list, err := s.cfg.Sources.List(ctx)
	if err != nil {
		return err
	}
	for i := range list {
		src := &list[i]
		if !catalogOnMainOrTarget(src.BoardID, target) {
			continue
		}
		want := ids[src.ID] || (src.Group != "" && labels[src.Group])
		if want {
			src.BoardID = targetBoardStoredID(target)
		} else if config.SameBoard(src.BoardID, target) {
			src.BoardID = ""
		}
	}
	return s.cfg.Sources.Save(ctx, list)
}

func (s *Server) assignCatalogS2S(ctx context.Context, target string, wantIDs []string) error {
	if s.cfg.S2S == nil {
		return nil
	}
	ids := stringSet(wantIDs)
	list, err := s.cfg.S2S.List(ctx)
	if err != nil {
		return err
	}
	for i := range list {
		p := &list[i]
		if !catalogOnMainOrTarget(p.BoardID, target) || isCheckerManagedID(p.ID) {
			continue
		}
		if ids[p.ID] {
			p.BoardID = targetBoardStoredID(target)
		} else if config.SameBoard(p.BoardID, target) {
			p.BoardID = ""
		}
	}
	return s.cfg.S2S.Save(ctx, list)
}

// catalogOnMainOrTarget reports whether a row may be moved by assign for target.
func catalogOnMainOrTarget(boardID, target string) bool {
	return config.SameBoard(boardID, config.MainBoardID) || config.SameBoard(boardID, target)
}

// targetBoardStoredID returns the board_id value written for a non-main section.
func targetBoardStoredID(target string) string {
	if config.SameBoard(target, config.MainBoardID) {
		return ""
	}
	return config.NormalizeBoardID(target)
}

func stringSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = true
		}
	}
	return out
}

func memberUserIDs(before, after []config.BoardMember) []string {
	seen := map[string]bool{}
	var out []string
	add := func(list []config.BoardMember) {
		for _, m := range list {
			if m.UserID == "" || seen[m.UserID] {
				continue
			}
			seen[m.UserID] = true
			out = append(out, m.UserID)
		}
	}
	add(before)
	add(after)
	return out
}

// boardIDsForMember collects section ids where userID appears in Members.
func boardIDsForMember(boards []config.Board, userID string) []string {
	var out []string
	seen := map[string]bool{}
	for _, b := range boards {
		for _, m := range b.Members {
			if m.UserID != userID {
				continue
			}
			id := config.NormalizeBoardID(b.ID)
			if seen[id] {
				break
			}
			seen[id] = true
			out = append(out, id)
			break
		}
	}
	return out
}

// syncUsersBoardIDsFromMembers updates BoardIDs for each user in affected from all board members.
func (s *Server) syncUsersBoardIDsFromMembers(boards []config.Board, affected []string) error {
	if !s.usersEnabled() || len(affected) == 0 {
		return nil
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	for _, uid := range affected {
		ids := boardIDsForMember(boards, uid)
		if err := s.users.Update(uid, func(u *security.User) error {
			u.AllBoards = false
			u.BoardIDs = ids
			return nil
		}); err != nil {
			return err
		}
	}
	return s.persistUsers()
}
