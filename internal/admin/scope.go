package admin

import (
	"context"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
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
}

// viewerFrom loads session access for ctx. Administrators get admin=true and nil grants.
// For other users the grants and the hidden set come from accessOf.
func (s *Server) viewerFrom(ctx context.Context) viewer {
	sess := sessionFrom(ctx)
	cur := boardFrom(ctx)
	allBoards := boardsAllFrom(ctx)
	role := ""
	if b, ok := ctx.Value(boardKey).(boardSession); ok {
		role = b.role
	}
	if s.isAdmin(sess) {
		return viewer{admin: true, boardCurrent: cur, boardsAll: allBoards}
	}
	v := viewer{uid: sess.UID, grants: map[string]security.Grant{}, hidden: map[string]bool{}, boardCurrent: cur, boardsAll: allBoards, boardRole: role}
	if u, ok := s.sessionUser(sess); ok {
		acc := s.accessOf(u)
		if acc.Grants != nil {
			v.grants = acc.Grants
		}
		v.hidden = expandHidden(acc.Hidden)
		if scopedCampaigns(v) {
			v.hidden["origin_campaign"] = true
			v.hidden["origin_campaign_id"] = true
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

// boardRoleSee applies the section member role after global grants.
func (v viewer) boardRoleSee(res string, r ref) bool {
	if v.admin || v.boardRole != config.BoardRoleCreated {
		return true
	}
	if !boardRoleResource(res) {
		return true
	}
	if r.board != "" && !config.SameBoard(r.board, v.boardCurrent) {
		return true
	}
	return r.owner == v.uid && v.uid != ""
}

// boardRoleWrite reports whether the section read role allows writes on res.
func (v viewer) boardRoleWrite(res string) bool {
	if v.admin || v.boardRole != config.BoardRoleRead {
		return true
	}
	return !boardRoleResource(res)
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
	if !v.boardRoleWrite(res) {
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
