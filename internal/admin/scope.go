package admin

import (
	"context"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/security"
)

const (
	resCampaigns = "campaigns"
	resOffers    = "offers"
	resLandings  = "landings"
	resSources   = "sources"
	resNetworks  = "networks"
	resDomains   = "domains"
	resS2S       = "s2s"
)

// viewer is the resolved access of one request: who asks and what they may touch.
type viewer struct {
	admin        bool
	uid          string
	grants       map[string]security.Grant
	hidden       map[string]bool
	sections     map[string]bool
	boardCurrent string
	boardsAll    bool
	boardRole    string
	includeAll   bool
	boardRoles   map[string]string
	boardOpen    map[string]bool
}

// viewerFrom loads session access for ctx. Administrators get admin=true and nil grants.
// For other users the grants and the hidden set come from accessOf.
func (s *Server) viewerFrom(ctx context.Context) viewer {
	sess := sessionFrom(ctx)
	cur := boardFrom(ctx)
	allBoards := boardsAllFrom(ctx)
	role := ""
	includeAll := false
	var roles map[string]string
	var open map[string]bool
	if b, ok := ctx.Value(boardKey).(boardSession); ok {
		role = b.role
		includeAll = b.includeAll
		roles = b.roles
		open = b.visible
	}
	if s.isAdmin(sess) {
		return viewer{admin: true, boardCurrent: cur, boardsAll: allBoards, includeAll: includeAll, boardOpen: open}
	}
	v := viewer{uid: sessionUID(sess), grants: map[string]security.Grant{}, hidden: map[string]bool{}, boardCurrent: cur, boardsAll: allBoards, boardRole: role, includeAll: includeAll, boardRoles: roles, boardOpen: open}
	if u, ok := s.sessionUser(sess); ok {
		acc := s.accessOf(u)
		if acc.Grants != nil {
			v.grants = acc.Grants
		}
		v.hidden = expandHidden(acc.Hidden)
		if scopedCampaigns(v) {
			v.hidden["origin_campaign"] = true
			v.hidden["origin_campaign_id"] = true
			v.hidden["root_cid"] = true
			v.hidden["origin_stream"] = true
			v.hidden["origin_keyword"] = true
		}
		v.sections = make(map[string]bool, len(acc.Sections))
		for _, sec := range acc.Sections {
			v.sections[sec] = true
		}
	}
	return v
}

// hasSection reports whether the panel section sec is open for v.
// Administrators have every section.
func (v viewer) hasSection(sec section) bool {
	return v.admin || v.sections[string(sec)]
}

// mode returns the grant mode for res. Administrators get ModeFull.
// When the section is open but grants has no entry for res, ModeRead applies:
// the user may read every record in that catalog, not write or create.
func (v viewer) mode(res string) security.Mode {
	if v.admin {
		return security.ModeFull
	}
	g, ok := v.grants[res]
	if !ok {
		return security.ModeRead
	}
	return g.Mode
}

// ref describes one record for access checks.
type ref struct {
	id, label, owner, board string
}

// grantHasID reports whether id appears in ids.
func grantHasID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// grantHasLabel reports whether label matches a non-empty entry in labels.
func grantHasLabel(labels []string, label string) bool {
	if label == "" {
		return false
	}
	for _, x := range labels {
		if x == label {
			return true
		}
	}
	return false
}

// boardRoleResource reports whether res is scoped by the current section member role.
func boardRoleResource(res string) bool {
	switch res {
	case resCampaigns, resOffers, resLandings, resSources, resNetworks, resS2S:
		return true
	default:
		return false
	}
}

// seesBoard reports whether v may read a row stored in rowBoard.
// rowBoard is the row section; empty means main.
// It is true for the current section, and for another section when the current
// section is main, the wide view is on, and the session may open that section.
// It does not return an error.
func (v viewer) seesBoard(rowBoard string) bool {
	if config.SameBoard(rowBoard, v.boardCurrent) {
		return true
	}
	if !v.includeAll || !config.SameBoard(v.boardCurrent, config.MainBoardID) {
		return false
	}
	if v.admin || v.boardsAll {
		return true
	}
	if v.boardOpen == nil {
		return false
	}
	return v.boardOpen[config.NormalizeBoardID(rowBoard)]
}

// roleOn returns the member role of v on board.
// board is the row section. An administrator, and a section with no listed role, return empty.
// The current section uses boardRole when the map has no entry.
// It does not return an error.
func (v viewer) roleOn(board string) string {
	if v.admin {
		return ""
	}
	id := config.NormalizeBoardID(board)
	if v.boardRoles != nil {
		if role, ok := v.boardRoles[id]; ok {
			return role
		}
	}
	if config.SameBoard(id, v.boardCurrent) {
		return v.boardRole
	}
	return ""
}

// boardRoleSee applies the member role of the row's section after global grants.
// res is the catalog. r is the row. The created role sees only rows it owns, including rows outside main.
// Administrators and other roles see the row. It does not return an error.
func (v viewer) boardRoleSee(res string, r ref) bool {
	if v.admin || !boardRoleResource(res) {
		return true
	}
	if v.roleOn(r.board) != config.BoardRoleCreated {
		return true
	}
	return r.owner == v.uid && v.uid != ""
}

// boardRoleWrite reports whether the member role on the row's section allows writes to res.
// res is the catalog. r carries the row section. The read role cannot write. Other roles can.
// Administrators can. It does not return an error.
func (v viewer) boardRoleWrite(res string, r ref) bool {
	if v.admin || !boardRoleResource(res) {
		return true
	}
	return v.roleOn(r.board) != config.BoardRoleRead
}

// canSee reports whether v may read the record described by r in res.
// Mode created shows only the records the user owns; an id or label list kept in
// such a grant opens nothing.
func (v viewer) canSee(res string, r ref) bool {
	var ok bool
	switch v.mode(res) {
	case security.ModeFull, security.ModeRead:
		ok = true
	case security.ModeSelected:
		ok = grantHasID(v.grants[res].IDs, r.id) || grantHasLabel(v.grants[res].Labels, r.label)
	case security.ModeCreated:
		ok = r.owner == v.uid && v.uid != ""
	default:
		ok = false
	}
	if !ok {
		return false
	}
	return v.boardRoleSee(res, r)
}

// canWrite reports whether v may change the record described by r in res.
func (v viewer) canWrite(res string, r ref) bool {
	if v.admin {
		return true
	}
	if !v.boardRoleWrite(res, r) {
		return false
	}
	switch v.mode(res) {
	case security.ModeRead:
		return false
	case security.ModeFull:
		return v.boardRoleSee(res, r)
	case security.ModeSelected, security.ModeCreated:
		return v.canSee(res, r)
	default:
		return false
	}
}

// scopedCampaigns reports whether v may see only some campaigns, not every one.
// Administrators and read/full campaign grants return false.
func scopedCampaigns(v viewer) bool {
	if v.admin {
		return false
	}
	switch v.mode(resCampaigns) {
	case security.ModeFull, security.ModeRead:
		return false
	default:
		return true
	}
}

// canCreate reports whether v may add new records in res.
func (v viewer) canCreate(res string) bool {
	if v.admin {
		return true
	}
	switch v.mode(res) {
	case security.ModeFull, security.ModeCreated:
		return true
	default:
		return false
	}
}
