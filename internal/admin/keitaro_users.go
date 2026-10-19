package admin

import (
	"strconv"
	"strings"

	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/kuztds/bridge/ktimport"
)

// importUsers creates or updates panel users from Keitaro import output.
//
// users is the mapper list. It returns how many users were created, updated or already
// matched (including manual access_source), per-user losses, and a save error.
func (s *Server) importUsers(users []ktimport.CatUser) (int, []ktimport.Loss, error) {
	if !s.usersEnabled() {
		return 0, nil, errUsersUnavailable
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	var losses []ktimport.Loss
	n := 0
	dirty := false
	for _, cu := range users {
		saved, changed, loss := s.importUser(cu)
		if loss != nil {
			losses = append(losses, *loss)
			continue
		}
		if saved {
			n++
		}
		if changed {
			dirty = true
		}
	}
	if !dirty {
		return n, losses, nil
	}
	if err := s.persistUsers(); err != nil {
		return 0, losses, err
	}
	return n, losses, nil
}

// importUser applies one CatUser row under the caller's userMu lock.
//
// cu is the mapped Keitaro user. It returns saved when the row counts toward the import
// total, changed when users.json must be written, and loss when the row is skipped.
// A user that has a role is left as it is, like a user with manual access.
func (s *Server) importUser(cu ktimport.CatUser) (saved, changed bool, loss *ktimport.Loss) {
	ref := "user " + cu.ID
	if strings.EqualFold(cu.Login, s.cfg.AdminUser) {
		return false, false, &ktimport.Loss{Kind: "skipped", Ref: ref, Detail: "логин зарезервирован"}
	}
	if err := security.ValidateLogin(cu.Login); err != nil {
		return false, false, &ktimport.Loss{Kind: "skipped", Ref: ref, Detail: err.Error()}
	}
	prefs := security.Prefs{Lang: cu.Lang, TZ: cu.TZ}
	if err := validatePrefs(prefs); err != nil {
		prefs.TZ = ""
		loss = &ktimport.Loss{Kind: "skipped", Ref: ref, Detail: err.Error()}
	}
	kid, _ := strconv.Atoi(cu.ID)
	uType := security.UserRegular
	var acc security.Access
	if cu.Type == "ADMIN" {
		uType = security.UserAdmin
	} else {
		var err error
		acc, err = catUserAccess(cu)
		if err != nil {
			return false, false, &ktimport.Loss{Kind: "skipped", Ref: ref, Detail: err.Error()}
		}
	}
	if existing, ok := s.users.ByKeitaroID(kid); ok {
		if existing.RoleID != "" {
			return true, false, loss
		}
		switch existing.AccessSource {
		case "manual":
			return true, false, loss
		case "keitaro", "":
			if err := s.users.Update(existing.ID, func(u *security.User) error {
				u.Type = uType
				u.Access = acc
				u.Prefs = prefs
				u.AccessSource = "keitaro"
				u.KeitaroID = kid
				return nil
			}); err != nil {
				return false, false, &ktimport.Loss{Kind: "skipped", Ref: ref, Detail: err.Error()}
			}
			return true, true, loss
		}
	}
	if _, ok := s.users.ByLogin(cu.Login); ok {
		return false, false, &ktimport.Loss{Kind: "skipped", Ref: ref, Detail: "логин занят"}
	}
	u := security.User{
		Login:        cu.Login,
		Type:         uType,
		Enabled:      false,
		Hash:         "",
		Prefs:        prefs,
		Access:       acc,
		KeitaroID:    kid,
		AccessSource: "keitaro",
	}
	if err := s.users.Add(u); err != nil {
		return false, false, &ktimport.Loss{Kind: "skipped", Ref: ref, Detail: err.Error()}
	}
	return true, true, loss
}

// catUserAccess converts mapper access to security.Access and validates it for USER accounts.
//
// Parameters: cu is the mapped Keitaro user with sections, grants and hidden fields.
// Returns normalized access ready for users.json. Errors come from normalizeAccess.
func catUserAccess(cu ktimport.CatUser) (security.Access, error) {
	acc := security.Access{
		Sections: append([]string{}, cu.Sections...),
		Hidden:   append([]string{}, cu.Hidden...),
		Grants:   map[string]security.Grant{},
	}
	for key, g := range cu.Grants {
		acc.Grants[key] = security.Grant{
			Mode:   security.Mode(g.Mode),
			IDs:    append([]string{}, g.IDs...),
			Labels: append([]string{}, g.Labels...),
		}
	}
	return normalizeAccess(acc)
}
