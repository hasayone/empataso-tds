package security

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/egerkuzma/emtds/internal/atomicfile"
)

// maxRoleNameRunes bounds the length of a role name.
const maxRoleNameRunes = 64

var (
	// ErrRoleNotFound is returned when no role matches the id.
	ErrRoleNotFound = errors.New("security: role not found")
	// ErrRoleExists is returned when the id or the name is already taken.
	ErrRoleExists = errors.New("security: role already exists")
	// ErrRoleName is returned when the role name is empty, too long or has control characters.
	ErrRoleName = errors.New("security: invalid role name")
)

// Role is a named access template stored in roles.json. A user that points at a
// role through User.RoleID takes Access from it instead of the own record.
type Role struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Access Access `json:"access"`
}

// clone returns a deep copy of r.
func (r Role) clone() Role {
	r.Access = r.Access.clone()
	return r
}

// ValidateRoleName checks name for storage.
// name must be trimmed, 1 to 64 runes, without control characters.
// It returns ErrRoleName otherwise.
func ValidateRoleName(name string) error {
	if name != strings.TrimSpace(name) {
		return ErrRoleName
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > maxRoleNameRunes {
		return ErrRoleName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return ErrRoleName
		}
	}
	return nil
}

// RoleSet is the in-memory roles.json collection, safe for concurrent use.
type RoleSet struct {
	mu      sync.RWMutex
	roles   []*Role
	failure string
}

// Failure returns why the last load of roles.json was refused, or "" when the
// set mirrors a usable file.
func (s *RoleSet) Failure() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failure
}

// SetFailure records the reason roles.json is not usable; "" clears it.
func (s *RoleSet) SetFailure(reason string) {
	s.mu.Lock()
	s.failure = reason
	s.mu.Unlock()
}

// Replace swaps the roles of s for those of next and clears the failure.
// next is not modified; a nil next empties the set.
func (s *RoleSet) Replace(next *RoleSet) {
	var roles []*Role
	if next != nil {
		next.mu.RLock()
		roles = make([]*Role, len(next.roles))
		for i, r := range next.roles {
			c := r.clone()
			roles[i] = &c
		}
		next.mu.RUnlock()
	}
	s.mu.Lock()
	s.roles = roles
	s.failure = ""
	s.mu.Unlock()
}

// NewRoleSet returns an empty set.
func NewRoleSet() *RoleSet {
	return &RoleSet{}
}

// LoadRoleSet reads roles from path.
//
// A missing file returns an empty set and nil error. Unreadable files, bad JSON,
// a missing or repeated id and an invalid or repeated name return nil and the
// error: the caller decides how to fail closed.
func LoadRoleSet(path string) (*RoleSet, error) {
	roles, err := readRoleFile(path)
	if err != nil {
		return nil, err
	}
	return &RoleSet{roles: roles}, nil
}

// Reload replaces the set from path. On error the previous set is unchanged.
func (s *RoleSet) Reload(path string) error {
	roles, err := readRoleFile(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.roles = roles
	s.mu.Unlock()
	return nil
}

// readRoleFile parses and validates roles.json; a missing file returns no roles.
// The error is the read or JSON error, ErrRoleName, or ErrRoleExists for an empty
// or repeated id and a repeated name.
func readRoleFile(path string) ([]*Role, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var recs []Role
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	out := make([]*Role, 0, len(recs))
	byID := make(map[string]bool, len(recs))
	byName := make(map[string]bool, len(recs))
	for _, rec := range recs {
		if rec.ID == "" || byID[rec.ID] {
			return nil, ErrRoleExists
		}
		if err := ValidateRoleName(rec.Name); err != nil {
			return nil, err
		}
		key := strings.ToLower(rec.Name)
		if byName[key] {
			return nil, ErrRoleExists
		}
		byID[rec.ID], byName[key] = true, true
		if rec.Access.Grants == nil {
			rec.Access.Grants = make(map[string]Grant)
		}
		c := rec.clone()
		out = append(out, &c)
	}
	return out, nil
}

// Save writes the set to path as indented JSON with mode 0600, atomically.
func (s *RoleSet) Save(path string) error {
	data, err := json.MarshalIndent(s.List(), "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

// List returns a copy of every role sorted by name, case-insensitive.
// The slice is never nil, so it encodes as [].
func (s *RoleSet) List() []Role {
	s.mu.RLock()
	out := make([]Role, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, r.clone())
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns a copy of the role with id.
func (s *RoleSet) Get(id string) (Role, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r := s.find(id); r != nil {
		return r.clone(), true
	}
	return Role{}, false
}

// Add inserts r after assigning a random id. The assigned id is in the result.
// It returns ErrRoleName for a bad name and ErrRoleExists when the name is taken.
func (s *RoleSet) Add(r Role) (Role, error) {
	if err := ValidateRoleName(r.Name); err != nil {
		return Role{}, err
	}
	id, err := RandomToken()
	if err != nil {
		return Role{}, err
	}
	r.ID = id
	if r.Access.Grants == nil {
		r.Access.Grants = make(map[string]Grant)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nameTaken(r.Name, "") {
		return Role{}, ErrRoleExists
	}
	c := r.clone()
	s.roles = append(s.roles, &c)
	return r.clone(), nil
}

// Update applies fn to a copy of the role with id and stores the result when
// fn succeeds. The id cannot be changed. It returns ErrRoleNotFound,
// ErrRoleName, ErrRoleExists or the error of fn.
func (s *RoleSet) Update(id string, fn func(*Role) error) (Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.find(id)
	if cur == nil {
		return Role{}, ErrRoleNotFound
	}
	next := cur.clone()
	if err := fn(&next); err != nil {
		return Role{}, err
	}
	next.ID = id
	if err := ValidateRoleName(next.Name); err != nil {
		return Role{}, err
	}
	if s.nameTaken(next.Name, id) {
		return Role{}, ErrRoleExists
	}
	if next.Access.Grants == nil {
		next.Access.Grants = make(map[string]Grant)
	}
	*cur = next
	return next.clone(), nil
}

// Remove deletes the role with id and returns ErrRoleNotFound when it is absent.
func (s *RoleSet) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.roles {
		if r.ID == id {
			s.roles = append(s.roles[:i], s.roles[i+1:]...)
			return nil
		}
	}
	return ErrRoleNotFound
}

// find returns the stored role with id; the caller holds s.mu.
func (s *RoleSet) find(id string) *Role {
	for _, r := range s.roles {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// nameTaken reports whether another role than exceptID has the name, ignoring case.
// The caller holds s.mu.
func (s *RoleSet) nameTaken(name, exceptID string) bool {
	for _, r := range s.roles {
		if r.ID != exceptID && strings.EqualFold(r.Name, name) {
			return true
		}
	}
	return false
}
