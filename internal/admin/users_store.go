package admin

import (
	"errors"
	"strings"

	"github.com/egerkuzma/emtds/internal/security"
)

var (
	errUsersUnavailable = errors.New("users unavailable")
	errUserReserved     = errors.New("user reserved")
	errPasswordShort    = errors.New("password too short (min. 6)")
)

// userDraft holds fields for createUser.
type userDraft struct {
	Login    string
	Password string
	Type     security.UserType
	Enabled  bool
	Prefs    security.Prefs
	Access   security.Access
	RoleID   string
}

// usersEnabled reports whether users.json storage is configured.
func (s *Server) usersEnabled() bool {
	return s.cfg.UsersFile != "" && s.users != nil
}

// persistUsers writes users.json under userMu.
//
// On save failure the set is reloaded from disk so memory matches the file.
func (s *Server) persistUsers() error {
	if err := s.users.Save(s.cfg.UsersFile); err != nil {
		s.logWarn("users file save failed", "err", err)
		if rerr := s.users.Reload(s.cfg.UsersFile); rerr != nil {
			s.logWarn("users file reload failed", "err", rerr)
		}
		return err
	}
	return nil
}

// rootLogin reports whether login is the environment administrator name.
func (s *Server) rootLogin(login string) bool {
	return security.EqualTokens(login, s.cfg.AdminUser)
}

// userByID returns the user record, nil-safe when users storage is off.
func (s *Server) userByID(id string) (security.User, bool) {
	if s.users == nil || id == "" {
		return security.User{}, false
	}
	return s.users.Get(id)
}

// createUser adds a user and persists users.json.
//
// It returns the public record. users must be enabled; the login must not
// match the environment administrator; apikey-prefixed logins are rejected.
// A non-empty RoleID must name an existing role and the type must be USER; the
// draft then carries no own access (errRoleOwnAccess otherwise), and the record
// stores an empty one.
func (s *Server) createUser(d userDraft) (security.User, error) {
	if !s.usersEnabled() {
		return security.User{}, errUsersUnavailable
	}
	if strings.EqualFold(d.Login, s.cfg.AdminUser) {
		return security.User{}, errUserReserved
	}
	if d.Password != "" && len(d.Password) < 6 {
		return security.User{}, errPasswordShort
	}
	if d.RoleID != "" && !accessBlank(d.Access) {
		return security.User{}, errRoleOwnAccess
	}
	acc, err := normalizeAccess(d.Access)
	if err != nil {
		return security.User{}, err
	}
	d.Access = acc
	if d.RoleID != "" {
		d.Access = emptyAccess()
	}
	hash := ""
	if d.Password != "" {
		h, err := security.HashPassword(d.Password)
		if err != nil {
			return security.User{}, err
		}
		if err := security.ValidateHash(h); err != nil {
			return security.User{}, err
		}
		hash = h
	}
	u := security.User{
		Login:        d.Login,
		Type:         d.Type,
		Enabled:      d.Enabled,
		Hash:         hash,
		Prefs:        d.Prefs,
		Access:       d.Access,
		AccessSource: "manual",
		RoleID:       d.RoleID,
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if d.RoleID != "" {
		if err := s.checkRoleAssign(d.RoleID, d.Type); err != nil {
			return security.User{}, err
		}
	}
	if err := s.users.Add(u); err != nil {
		return security.User{}, err
	}
	if err := s.persistUsers(); err != nil {
		return security.User{}, err
	}
	out, ok := s.users.ByLogin(d.Login)
	if !ok {
		return security.User{}, security.ErrUserNotFound
	}
	return out.Public(), nil
}

// setUserPassword replaces the stored hash for id and saves users.json.
//
// An empty password clears Hash so sign-in is impossible; a non-empty password
// shorter than six characters returns errPasswordShort.
func (s *Server) setUserPassword(id, password string) error {
	if !s.usersEnabled() {
		return errUsersUnavailable
	}
	if password == "" {
		s.userMu.Lock()
		defer s.userMu.Unlock()
		if err := s.users.Update(id, func(u *security.User) error {
			u.Hash = ""
			return nil
		}); err != nil {
			return err
		}
		return s.persistUsers()
	}
	if len(password) < 6 {
		return errPasswordShort
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	if err := security.ValidateHash(hash); err != nil {
		return err
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if err := s.users.Update(id, func(u *security.User) error {
		u.Hash = hash
		return nil
	}); err != nil {
		return err
	}
	return s.persistUsers()
}

// setUserEnabled toggles the enabled flag for id and saves users.json.
func (s *Server) setUserEnabled(id string, on bool) error {
	if !s.usersEnabled() {
		return errUsersUnavailable
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if err := s.users.Update(id, func(u *security.User) error {
		u.Enabled = on
		return nil
	}); err != nil {
		return err
	}
	return s.persistUsers()
}

// updateUser applies fn to one user under userMu and persists users.json.
//
// It returns the public record after a successful save.
func (s *Server) updateUser(id string, fn func(*security.User) error) (security.User, error) {
	if !s.usersEnabled() {
		return security.User{}, errUsersUnavailable
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if err := s.users.Update(id, fn); err != nil {
		return security.User{}, err
	}
	if err := s.persistUsers(); err != nil {
		return security.User{}, err
	}
	u, ok := s.users.Get(id)
	if !ok {
		return security.User{}, security.ErrUserNotFound
	}
	return u.Public(), nil
}

// deleteUser removes one user and persists users.json.
func (s *Server) deleteUser(id string) error {
	if !s.usersEnabled() {
		return errUsersUnavailable
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if err := s.users.Remove(id); err != nil {
		return err
	}
	return s.persistUsers()
}

// credentialFor picks the hash and uid for login verification.
//
// Unknown or disabled accounts still verify against DummyHash so argon2 timing
// does not reveal whether the login exists.
func (s *Server) credentialFor(login string) (hash, uid string, userOK bool) {
	if s.rootLogin(login) {
		return s.currentHash(), "", true
	}
	if s.users != nil {
		if u, ok := s.users.ByLogin(login); ok && u.Enabled && u.Hash != "" {
			return u.Hash, u.ID, true
		}
	}
	return security.DummyHash, "", false
}
