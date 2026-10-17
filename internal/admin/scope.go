package admin

import (
	"context"

	"github.com/egerkuzma/kuztds/internal/security"
)

const (
	resCampaigns = "campaigns"
	resOffers    = "offers"
	resLandings  = "landings"
	resSources   = "sources"
	resNetworks  = "networks"
	resDomains   = "domains"
)

// viewer is the resolved access of one request: who asks and what they may touch.
type viewer struct {
	admin  bool
	uid    string
	grants map[string]security.Grant
	hidden map[string]bool
}

// viewerFrom loads session access for ctx. Administrators get admin=true and nil grants.
func (s *Server) viewerFrom(ctx context.Context) viewer {
	sess := sessionFrom(ctx)
	if s.isAdmin(sess) {
		return viewer{admin: true}
	}
	v := viewer{uid: sess.UID, grants: map[string]security.Grant{}, hidden: map[string]bool{}}
	if u, ok := s.sessionUser(sess); ok {
		if u.Access.Grants != nil {
			v.grants = u.Access.Grants
		}
		for _, m := range u.Access.Hidden {
			v.hidden[m] = true
		}
	}
	return v
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
	id, label, owner string
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

// canSee reports whether v may read the record described by r in res.
func (v viewer) canSee(res string, r ref) bool {
	switch v.mode(res) {
	case security.ModeFull, security.ModeRead:
		return true
	case security.ModeSelected:
		return grantHasID(v.grants[res].IDs, r.id) || grantHasLabel(v.grants[res].Labels, r.label)
	case security.ModeCreated:
		if r.owner == v.uid && v.uid != "" {
			return true
		}
		g := v.grants[res]
		return grantHasID(g.IDs, r.id) || grantHasLabel(g.Labels, r.label)
	default:
		return false
	}
}

// canWrite reports whether v may change the record described by r in res.
func (v viewer) canWrite(res string, r ref) bool {
	if v.admin {
		return true
	}
	switch v.mode(res) {
	case security.ModeRead:
		return false
	case security.ModeFull:
		return true
	case security.ModeSelected, security.ModeCreated:
		return v.canSee(res, r)
	default:
		return false
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
