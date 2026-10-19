package admin

import (
	"errors"
	"fmt"

	"github.com/egerkuzma/kuztds/internal/security"
)

// modeNone is a grant mode that denies every record of a resource.
// It exists only in memory, for the access of a user whose role is gone.
const modeNone security.Mode = "none"

var (
	errRolesUnavailable = errors.New("roles unavailable")
	errRoleUnknown      = errors.New("unknown role")
	errRoleAdmin        = errors.New("role is not allowed for administrators")
	errRoleAssigned     = errors.New("user has a role")
	errRoleInUse        = errors.New("role in use")
	errRoleSelected     = fmt.Errorf("%w: selected is set per user", errAccessMode)
	errRoleOwnAccess    = errors.New("user with a role has no own access")
)

// emptyAccess returns the own access of a user with a role: it opens nothing and
// is never read while the role is set.
func emptyAccess() security.Access {
	return security.Access{Sections: []string{}, Grants: map[string]security.Grant{}}
}

// accessBlank reports whether a holds no section, grant or hidden field.
func accessBlank(a security.Access) bool {
	return len(a.Sections) == 0 && len(a.Grants) == 0 && len(a.Hidden) == 0
}

// closedAccess returns the access that opens nothing: no sections and no
// records in any resource. A user whose role is missing gets it.
func closedAccess() security.Access {
	grants := make(map[string]security.Grant, len(grantResources))
	for _, res := range grantResources {
		grants[res] = security.Grant{Mode: modeNone}
	}
	return security.Access{Sections: []string{}, Grants: grants}
}

// accessOf returns the access in force for u. It is the only place that reads
// user access, so a role cannot be bypassed.
//
// An administrator and a user without a role use the own record. A user with a
// role uses the role; when the role is missing or roles.json is unusable the
// result is closedAccess. Administrators are never limited by this function:
// callers check isAdmin first.
func (s *Server) accessOf(u security.User) security.Access {
	if u.RoleID == "" || u.Type == security.UserAdmin {
		return u.Access
	}
	if s.roles != nil {
		if r, ok := s.roles.Get(u.RoleID); ok {
			return r.Access
		}
	}
	return closedAccess()
}

// normalizeRoleAccess validates the access of a role for storage.
// It applies normalizeAccess, so admin sections and unknown names are refused,
// and then refuses mode selected, which lists records of one user. Mode created
// keeps no id or label lists, as normalizeAccess leaves it. Errors wrap errAccess*.
func normalizeRoleAccess(a security.Access) (security.Access, error) {
	out, err := normalizeAccess(a)
	if err != nil {
		return security.Access{}, err
	}
	for res, g := range out.Grants {
		if g.Mode == security.ModeSelected {
			return security.Access{}, fmt.Errorf("%w: %s", errRoleSelected, res)
		}
	}
	return out, nil
}

// rolesEnabled reports whether roles.json storage is configured and usable.
func (s *Server) rolesEnabled() bool {
	return s.roles != nil && s.roles.Failure() == ""
}

// loadRoles reads roles.json at start.
//
// A missing file is an empty set. A file that cannot be read or that holds an
// invalid role leaves an empty set and records the reason; see reloadRoles.
func (s *Server) loadRoles() {
	if s.cfg.RolesFile == "" {
		return
	}
	s.roles = security.NewRoleSet()
	_ = s.reloadRoles()
}

// reloadRoles replaces the roles in memory with the content of roles.json.
//
// The file passes the same check at start and here: readable, valid ids and
// names, and every access through normalizeRoleAccess (admin sections, unknown
// fields, selected mode), and the normalized access is what stays in memory. A good
// file clears the failure. A bad one empties the
// set and records the reason: users with a role have closed access,
// administrators keep full access, and changes to roles are refused until the
// file is repaired, so the damaged file is not overwritten. It returns the
// reason. The caller holds userMu, except at start. s.roles must not be nil.
func (s *Server) reloadRoles() error {
	set, err := security.LoadRoleSet(s.cfg.RolesFile)
	if err == nil {
		err = normalizeRoleSet(set)
	}
	if err != nil {
		s.roles.Replace(nil)
		s.roles.SetFailure(err.Error())
		s.logWarn("roles file not used, users with a role have no access", "path", s.cfg.RolesFile, "err", err)
		return err
	}
	s.roles.Replace(set)
	return nil
}

// normalizeRoleSet runs normalizeRoleAccess over every role of set and stores the
// normalized access in set, so the roles in force match what a save would write:
// a created grant holds no id or label list, however the file was edited.
// It returns the first error with the role name; set may then be half normalized and
// the caller discards it.
func normalizeRoleSet(set *security.RoleSet) error {
	for _, r := range set.List() {
		acc, err := normalizeRoleAccess(r.Access)
		if err == nil {
			_, err = set.Update(r.ID, func(role *security.Role) error {
				role.Access = acc
				return nil
			})
		}
		if err != nil {
			return fmt.Errorf("role %q: %w", r.Name, err)
		}
	}
	return nil
}

// persistRoles writes roles.json. The caller holds userMu.
//
// On save failure the set is reloaded from disk with reloadRoles, so memory
// matches the file.
func (s *Server) persistRoles() error {
	if err := s.roles.Save(s.cfg.RolesFile); err != nil {
		s.logWarn("roles file save failed", "err", err)
		_ = s.reloadRoles()
		return err
	}
	return nil
}

// checkRoleAssign verifies that roleID may be set on a user of type t.
// It returns errRoleAdmin for an administrator and errRoleUnknown when the
// role does not exist. The caller holds userMu so the role cannot be deleted meanwhile.
func (s *Server) checkRoleAssign(roleID string, t security.UserType) error {
	if t == security.UserAdmin {
		return errRoleAdmin
	}
	if s.roles != nil {
		if _, ok := s.roles.Get(roleID); ok {
			return nil
		}
	}
	return errRoleUnknown
}
