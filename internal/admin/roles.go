package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hasayone/empataso-tds/internal/security"
)

// registerRoles mounts CRUD routes for roles.json behind cookie sessions.
func registerRoles(s *Server, mux *http.ServeMux) {
	s.handle(mux, "GET /api/roles", http.HandlerFunc(s.handleRolesList))
	s.handle(mux, "POST /api/roles", http.HandlerFunc(s.handleRolesCreate))
	s.handle(mux, "POST /api/roles/reload", http.HandlerFunc(s.handleRolesReload))
	s.handle(mux, "GET /api/roles/{id}", http.HandlerFunc(s.handleRolesGet))
	s.handle(mux, "PUT /api/roles/{id}", http.HandlerFunc(s.handleRolesUpdate))
	s.handle(mux, "DELETE /api/roles/{id}", http.HandlerFunc(s.handleRolesDelete))
}

// roleView is the public row of a role: the stored fields plus how many users use it.
type roleView struct {
	security.Role
	Users int `json:"users"`
}

// roleUsage counts the users that point at each role id.
func (s *Server) roleUsage() map[string]int {
	out := map[string]int{}
	if !s.usersEnabled() {
		return out
	}
	for _, u := range s.users.List() {
		if u.RoleID != "" {
			out[u.RoleID]++
		}
	}
	return out
}

// handleRolesList returns every role sorted by name, each with its user count.
//
// Without roles storage the body is an empty list. A roles.json that could not be
// used at start answers 503 "roles unavailable". Success is 200.
func (s *Server) handleRolesList(w http.ResponseWriter, _ *http.Request) {
	if s.roles == nil {
		writeJSON(w, http.StatusOK, []roleView{})
		return
	}
	if !s.rolesEnabled() {
		writeErr(w, http.StatusServiceUnavailable, errRolesUnavailable.Error())
		return
	}
	usage := s.roleUsage()
	list := s.roles.List()
	out := make([]roleView, len(list))
	for i, r := range list {
		out[i] = roleView{Role: r, Users: usage[r.ID]}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRolesGet returns one role by path id.
//
// A missing role or missing storage answers 404 "not found". A damaged roles.json
// answers 503. Success is 200.
func (s *Server) handleRolesGet(w http.ResponseWriter, r *http.Request) {
	if s.roles == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !s.rolesEnabled() {
		writeErr(w, http.StatusServiceUnavailable, errRolesUnavailable.Error())
		return
	}
	role, ok := s.roles.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, roleView{Role: role, Users: s.roleUsage()[role.ID]})
}

// roleBody is the request body of a role create or update.
type roleBody struct {
	Name   string           `json:"name"`
	Access *security.Access `json:"access"`
}

// readRoleBody decodes and validates a role body.
// An empty name is allowed when requireName is false. A present access goes through
// normalizeRoleAccess. It returns the trimmed name and the normalized access, nil when absent.
// A decode error answers 400 "bad request" and the bool is false after any answer.
func readRoleBody(w http.ResponseWriter, r *http.Request, requireName bool) (string, *security.Access, bool) {
	var body roleBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return "", nil, false
	}
	name := strings.TrimSpace(body.Name)
	if name != "" || requireName {
		if err := security.ValidateRoleName(name); err != nil {
			writeRoleErr(w, err)
			return "", nil, false
		}
	}
	if body.Access == nil {
		return name, nil, true
	}
	acc, err := normalizeRoleAccess(*body.Access)
	if err != nil {
		writeRoleErr(w, err)
		return "", nil, false
	}
	return name, &acc, true
}

// handleRolesCreate adds one role and returns it with Users 0.
//
// The body is name and access. A bad name or access answers 400 with the error text,
// a taken name 409, unavailable storage 503. Success is 201.
func (s *Server) handleRolesCreate(w http.ResponseWriter, r *http.Request) {
	if !s.rolesEnabled() {
		writeErr(w, http.StatusServiceUnavailable, errRolesUnavailable.Error())
		return
	}
	name, acc, ok := readRoleBody(w, r, true)
	if !ok {
		return
	}
	role := security.Role{Name: name}
	if acc != nil {
		role.Access = *acc
	} else {
		role.Access = security.Access{Sections: []string{}}
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	created, err := s.roles.Add(role)
	if err == nil {
		err = s.persistRoles()
	}
	if err != nil {
		writeRoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, roleView{Role: created})
}

// handleRolesUpdate replaces the name and the access of one role.
//
// An omitted or empty name and an omitted access keep the stored values. The change
// applies to every user of the role at once. A missing role answers 404, a bad name
// or access 400, a taken name 409, unavailable storage 503. Success is 200.
func (s *Server) handleRolesUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.rolesEnabled() {
		writeErr(w, http.StatusServiceUnavailable, errRolesUnavailable.Error())
		return
	}
	name, acc, ok := readRoleBody(w, r, false)
	if !ok {
		return
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	updated, err := s.roles.Update(r.PathValue("id"), func(role *security.Role) error {
		if name != "" {
			role.Name = name
		}
		if acc != nil {
			role.Access = *acc
		}
		return nil
	})
	if err == nil {
		err = s.persistRoles()
	}
	if err != nil {
		writeRoleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roleView{Role: updated, Users: s.roleUsage()[updated.ID]})
}

// handleRolesDelete removes one role that no user uses.
//
// A role in use answers 409 "role in use". A missing role answers 404, unavailable
// storage 503. Success is 204.
func (s *Server) handleRolesDelete(w http.ResponseWriter, r *http.Request) {
	if !s.rolesEnabled() {
		writeErr(w, http.StatusServiceUnavailable, errRolesUnavailable.Error())
		return
	}
	id := r.PathValue("id")
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if s.roleUsage()[id] > 0 {
		writeRoleErr(w, errRoleInUse)
		return
	}
	err := s.roles.Remove(id)
	if err == nil {
		err = s.persistRoles()
	}
	if err != nil {
		writeRoleErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRolesReload re-reads roles.json without a restart, after a manual repair.
//
// The file is checked like at start. A good file replaces the roles in memory and
// clears the failure; the answer is 200 {"status":"reloaded","roles":N}. A bad
// file empties the roles, keeps users with a role without access and answers 503
// "roles unavailable: <reason>". Without roles storage the answer is 503.
func (s *Server) handleRolesReload(w http.ResponseWriter, _ *http.Request) {
	if s.roles == nil {
		writeErr(w, http.StatusServiceUnavailable, errRolesUnavailable.Error())
		return
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if err := s.reloadRoles(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, errRolesUnavailable.Error()+": "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reloaded", "roles": len(s.roles.List())})
}

// roleErrStatus maps a role error to an HTTP status and message.
// The bool is false for an error that is not a role error.
func roleErrStatus(err error) (int, string, bool) {
	switch {
	case errors.Is(err, errRolesUnavailable):
		return http.StatusServiceUnavailable, err.Error(), true
	case errors.Is(err, security.ErrRoleNotFound):
		return http.StatusNotFound, "not found", true
	case errors.Is(err, security.ErrRoleExists), errors.Is(err, errRoleInUse), errors.Is(err, errRoleAssigned):
		return http.StatusConflict, roleConflictText(err), true
	case errors.Is(err, security.ErrRoleName), errors.Is(err, errRoleUnknown), errors.Is(err, errRoleAdmin), errors.Is(err, errRoleOwnAccess),
		errors.Is(err, errAccessSection), errors.Is(err, errAccessResource), errors.Is(err, errAccessMode),
		errors.Is(err, errAccessLimit), errors.Is(err, errAccessHidden):
		return http.StatusBadRequest, err.Error(), true
	}
	return 0, "", false
}

// roleConflictText returns the message of a 409 role error.
func roleConflictText(err error) string {
	if errors.Is(err, security.ErrRoleExists) {
		return security.ErrRoleExists.Error()
	}
	if errors.Is(err, errRoleAssigned) {
		return errRoleAssigned.Error()
	}
	return errRoleInUse.Error()
}

// writeRoleErr writes the answer for a role error; any other error is 500 "internal error".
func writeRoleErr(w http.ResponseWriter, err error) {
	if code, msg, ok := roleErrStatus(err); ok {
		writeErr(w, code, msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal error")
}
