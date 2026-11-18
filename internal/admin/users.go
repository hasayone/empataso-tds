package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

const userBodyLimit = 64 << 10

var errSelfDisable = errors.New("cannot disable yourself")

// registerUsers mounts CRUD routes for users.json behind cookie sessions.
func registerUsers(s *Server, mux *http.ServeMux) {
	s.handle(mux, "GET /api/users", http.HandlerFunc(s.handleUsersList))
	s.handle(mux, "POST /api/users", http.HandlerFunc(s.handleUsersCreate))
	s.handle(mux, "GET /api/users/{id}", http.HandlerFunc(s.handleUsersGet))
	s.handle(mux, "PUT /api/users/{id}", http.HandlerFunc(s.handleUsersUpdate))
	s.handle(mux, "DELETE /api/users/{id}", http.HandlerFunc(s.handleUsersDelete))
	s.handle(mux, "PUT /api/users/{id}/password", http.HandlerFunc(s.handleUsersPassword))
	s.handle(mux, "PUT /api/users/{id}/access", http.HandlerFunc(s.handleUsersAccess))
}

// envAdminRow returns the virtual list entry for the environment administrator (AdminUser).
func (s *Server) envAdminRow() security.User {
	return security.User{
		Login:   s.cfg.AdminUser,
		Type:    security.UserAdmin,
		Enabled: true,
		Source:  security.SourceEnv,
	}
}

// handleUsersList returns users visible in the panel API.
//
// The first row is always the environment administrator (AdminUser from config, not
// stored in users.json) with Source env and no id or hash. When users.json storage
// is enabled, file-backed rows follow with Hash cleared and Source file. When storage
// is disabled, the body is a one-element array containing only the environment row.
// Success is always 200; List does not produce error responses.
func (s *Server) handleUsersList(w http.ResponseWriter, _ *http.Request) {
	out := []security.User{s.envAdminRow()}
	if !s.usersEnabled() {
		writeJSON(w, http.StatusOK, out)
		return
	}
	stored := s.users.List()
	for i := range stored {
		stored[i].Source = security.SourceFile
		out = append(out, stored[i])
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUsersCreate adds one users.json record and returns the public row.
//
// The body is login, password, type, enabled, prefs, access, role_id and
// no_main_board. An empty type becomes USER; no_main_board is dropped for an
// administrator. ErrUserLogin, ErrUserType, errPasswordShort, errAccess*,
// errRoleUnknown, errRoleAdmin and errRoleOwnAccess answer 400 with the error text. errUserReserved
// and ErrUserExists answer 409. errUsersUnavailable answers 503 "users disabled".
// Success is 201 with Hash cleared.
func (s *Server) handleUsersCreate(w http.ResponseWriter, r *http.Request) {
	if !s.usersEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "users disabled")
		return
	}
	var body struct {
		Login    string            `json:"login"`
		Password string            `json:"password"`
		Type     security.UserType `json:"type"`
		Enabled  bool              `json:"enabled"`
		Prefs    security.Prefs    `json:"prefs"`
		Access   security.Access   `json:"access"`
		RoleID   string            `json:"role_id"`
		NoMain   bool              `json:"no_main_board"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if body.Type == "" {
		body.Type = security.UserRegular
	}
	if err := validatePrefs(body.Prefs); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.createUser(userDraft{
		Login:       body.Login,
		Password:    body.Password,
		Type:        body.Type,
		Enabled:     body.Enabled,
		Prefs:       body.Prefs,
		Access:      body.Access,
		RoleID:      strings.TrimSpace(body.RoleID),
		NoMainBoard: body.NoMain,
	})
	if err != nil {
		writeUserErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// handleUsersGet returns one public user by path id.
//
// A missing store or unknown id answers 404 "not found". Success is 200 without Hash.
func (s *Server) handleUsersGet(w http.ResponseWriter, r *http.Request) {
	if !s.usersEnabled() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	id := r.PathValue("id")
	u, ok := s.userByID(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, u.Public())
}

// handleUsersUpdate patches login, type, enabled, prefs, role and the main section for one user.
//
// Omitted or empty login and type leave the current values. enabled, prefs and
// no_main_board are applied only when present in JSON. role_id is applied when
// present: an empty string detaches the role, an id attaches it. A user turned
// into ADMIN loses the role and no_main_board. Section membership (board_ids) is
// not changed here. A reserved environment login answers 409 "login reserved".
// Disabling your own session answers 409. ErrLastAdmin and ErrUserExists answer
// 409. Invalid prefs, an unknown role and a role on an administrator answer 400.
// Success is 200 with the public record. Access and password are not changed here.
func (s *Server) handleUsersUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.usersEnabled() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	id := r.PathValue("id")
	var body struct {
		Login   string            `json:"login"`
		Type    security.UserType `json:"type"`
		Enabled *bool             `json:"enabled"`
		Prefs   *security.Prefs   `json:"prefs"`
		RoleID  *string           `json:"role_id"`
		NoMain  *bool             `json:"no_main_board"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if body.Login != "" && strings.EqualFold(body.Login, s.cfg.AdminUser) {
		writeErr(w, http.StatusConflict, "login reserved")
		return
	}
	sess := sessionFrom(r.Context())
	u, err := s.updateUser(id, func(u *security.User) error {
		if body.Enabled != nil {
			if sess.UID == id && !*body.Enabled {
				return errSelfDisable
			}
			u.Enabled = *body.Enabled
		}
		if body.Login != "" {
			u.Login = body.Login
		}
		if body.Type != "" {
			u.Type = body.Type
		}
		if body.Prefs != nil {
			merged := mergePrefs(u.Prefs, *body.Prefs)
			if err := validatePrefs(merged); err != nil {
				return err
			}
			u.Prefs = merged
		}
		if body.NoMain != nil {
			u.NoMainBoard = *body.NoMain
		}
		if u.Type == security.UserAdmin {
			u.NoMainBoard = false
		}
		return s.applyRole(u, body.RoleID)
	})
	if err != nil {
		writeUserUpdateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// applyRole sets or clears u.RoleID inside an updateUser callback.
// roleID nil keeps the role, "" clears it, any other value attaches that role.
// A user that is an administrator after the update has no role; asking for one
// returns errRoleAdmin. It returns errRoleUnknown for a missing role.
func (s *Server) applyRole(u *security.User, roleID *string) error {
	if roleID == nil {
		if u.Type == security.UserAdmin {
			u.RoleID = ""
		}
		return nil
	}
	id := strings.TrimSpace(*roleID)
	if id == "" {
		u.RoleID = ""
		return nil
	}
	if err := s.checkRoleAssign(id, u.Type); err != nil {
		return err
	}
	u.RoleID = id
	u.Access = emptyAccess()
	return nil
}

// handleUsersDelete removes one user by path id.
//
// Deleting your own uid answers 409 "cannot delete yourself". ErrLastAdmin
// answers 409 "last administrator". A missing user answers 404. Success is 204.
func (s *Server) handleUsersDelete(w http.ResponseWriter, r *http.Request) {
	if !s.usersEnabled() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	id := r.PathValue("id")
	if sessionFrom(r.Context()).UID == id {
		writeErr(w, http.StatusConflict, "cannot delete yourself")
		return
	}
	if err := s.deleteUser(id); err != nil {
		writeUserUpdateErr(w, err)
		return
	}
	if err := s.aiChatRemoveUser(id); err != nil {
		s.logWarn("assistant chats of a deleted user not removed", "error_type", fmt.Sprintf("%T", err))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUsersPassword replaces or clears the stored hash for one user.
//
// The body is {"password"}. An empty password clears Hash. errPasswordShort
// answers 400. A missing user answers 404. Success is 204.
func (s *Server) handleUsersPassword(w http.ResponseWriter, r *http.Request) {
	if !s.usersEnabled() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	id := r.PathValue("id")
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if _, ok := s.userByID(id); !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.setUserPassword(id, body.Password); err != nil {
		writeUserErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUsersAccess replaces access for one user after normalizeAccess.
//
// The body is a full security.Access object. errAccess* answers 400. A missing
// user answers 404. A user with a role answers 409 "user has a role": the role
// replaces the own access, so detach the role first. Success is 200 with the
// public record and AccessSource manual.
func (s *Server) handleUsersAccess(w http.ResponseWriter, r *http.Request) {
	if !s.usersEnabled() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	id := r.PathValue("id")
	var body security.Access
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	acc, err := normalizeAccess(body)
	if err != nil {
		writeUserErr(w, err)
		return
	}
	u, err := s.updateUser(id, func(u *security.User) error {
		if u.RoleID != "" {
			return errRoleAssigned
		}
		u.Access = acc
		u.AccessSource = "manual"
		return nil
	})
	if err != nil {
		writeUserUpdateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// mergePrefs overlays non-empty fields from patch onto cur.
func mergePrefs(cur security.Prefs, patch security.Prefs) security.Prefs {
	out := cur
	if patch.Lang != "" {
		out.Lang = patch.Lang
	}
	if tz := strings.TrimSpace(patch.TZ); tz != "" {
		out.TZ = tz
	}
	return out
}

// validatePrefs checks language and timezone fields for a user record.
func validatePrefs(p security.Prefs) error {
	switch p.Lang {
	case "", "ru", "en", "uk":
	default:
		return errors.New("invalid language")
	}
	tz := strings.TrimSpace(p.TZ)
	if tz == "" {
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return errors.New("invalid timezone")
	}
	return nil
}

// writeUserErr maps user creation and password errors to HTTP statuses.
// Role errors are mapped by roleErrStatus.
func writeUserErr(w http.ResponseWriter, err error) {
	if code, msg, ok := roleErrStatus(err); ok {
		writeErr(w, code, msg)
		return
	}
	switch {
	case errors.Is(err, errUsersUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "users disabled")
	case errors.Is(err, errUserReserved):
		writeErr(w, http.StatusConflict, errUserReserved.Error())
	case errors.Is(err, security.ErrUserExists):
		writeErr(w, http.StatusConflict, security.ErrUserExists.Error())
	case errors.Is(err, security.ErrUserLogin):
		writeErr(w, http.StatusBadRequest, security.ErrUserLogin.Error())
	case errors.Is(err, security.ErrUserType):
		writeErr(w, http.StatusBadRequest, security.ErrUserType.Error())
	case errors.Is(err, errPasswordShort):
		writeErr(w, http.StatusBadRequest, errPasswordShort.Error())
	case errors.Is(err, errAccessSection), errors.Is(err, errAccessResource),
		errors.Is(err, errAccessMode), errors.Is(err, errAccessLimit), errors.Is(err, errAccessHidden):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

// writeUserUpdateErr maps update and delete errors to HTTP statuses.
// Role errors are mapped by roleErrStatus.
func writeUserUpdateErr(w http.ResponseWriter, err error) {
	if code, msg, ok := roleErrStatus(err); ok {
		writeErr(w, code, msg)
		return
	}
	switch {
	case errors.Is(err, security.ErrUserNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, security.ErrUserExists):
		writeErr(w, http.StatusConflict, security.ErrUserExists.Error())
	case errors.Is(err, security.ErrLastAdmin):
		writeErr(w, http.StatusConflict, "last administrator")
	case errors.Is(err, errSelfDisable):
		writeErr(w, http.StatusConflict, errSelfDisable.Error())
	case errors.Is(err, security.ErrUserLogin), errors.Is(err, security.ErrUserType):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		if msg := err.Error(); msg == "invalid language" || msg == "invalid timezone" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}
