package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// UserAdmin is a full administrator account stored in users.json.
	UserAdmin UserType = "ADMIN"
	// UserRegular is a restricted panel user.
	UserRegular UserType = "USER"

	// ModeFull grants access to an entire section.
	ModeFull Mode = "full"
	// ModeRead grants read-only access.
	ModeRead Mode = "read"
	// ModeSelected grants access to listed ids.
	ModeSelected Mode = "selected"
	// ModeCreated grants access to objects the user created.
	ModeCreated Mode = "created"

	// SourceEnv marks the environment administrator in API list responses.
	SourceEnv = "env"
	// SourceFile marks a users.json record in API list responses.
	SourceFile = "file"

	maxLoginRunes = 64
)

// DummyHash is a valid argon2id hash of a random password (same parameters as
// HashPassword). Login uses it when the name is unknown or the account has no
// hash so verification time does not reveal whether the user exists.
const DummyHash = "$argon2id$v=19$m=65536,t=3,p=2$tNPwme6YNlpZ7JikbpH5kQ$WUOPeTr+SvLWo+khuzzMCTCgyrFFOPXy7V6OyQfqNg4"

// UserType distinguishes administrator and regular users in users.json.
type UserType string

// Mode describes how a grant applies within a section.
type Mode string

// Grant holds access mode and optional id or label filters.
type Grant struct {
	Mode   Mode     `json:"mode"`
	IDs    []string `json:"ids,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// Access lists visible sections and per-section grants.
type Access struct {
	Sections []string         `json:"sections"`
	Grants   map[string]Grant `json:"grants"`
	Hidden   []string         `json:"hidden,omitempty"`
}

// Prefs holds UI preferences for a user.
type Prefs struct {
	Lang string `json:"lang,omitempty"`
	TZ   string `json:"tz,omitempty"`
}

// User is one row in users.json, including the argon2id hash for login.
type User struct {
	ID      string   `json:"id"`
	Login   string   `json:"login"`
	Type    UserType `json:"type"`
	Enabled bool     `json:"enabled"`
	Hash    string   `json:"hash,omitempty"`
	Prefs   Prefs    `json:"prefs"`
	Access  Access   `json:"access"`
	// BoardIDs mirrors the sections that list this user as a member. It limits only a
	// regular user without AllBoards; an administrator sees every section.
	BoardIDs []string `json:"board_ids,omitempty"`
	// DefaultBoard is the section opened after sign-in. Empty uses MainBoardID.
	DefaultBoard string `json:"default_board,omitempty"`
	// AllBoards grants every section without listing BoardIDs.
	AllBoards    bool      `json:"all_boards,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	KeitaroID    int       `json:"keitaro_id,omitempty"`
	AccessSource string    `json:"access_source,omitempty"`
	// RoleID names a Role in roles.json. When set, the role replaces Access for a USER.
	RoleID string `json:"role_id,omitempty"`
	// Source is set only in API responses ("env" for the environment admin, "file" for users.json); it is not stored in users.json.
	Source string `json:"source,omitempty"`
}

// Public returns a copy of u with Hash cleared, safe for API responses.
func (u User) Public() User {
	cp := u.clone()
	cp.Hash = ""
	return cp
}

// Fingerprint returns PasswordFingerprint(Hash) for an enabled user with a hash.
//
// An empty string is returned when the user is disabled or has no hash. That
// fingerprint never matches a session issued while the user was active, so
// disabling the account or changing the password invalidates all sessions without
// enumerating tokens.
func (u User) Fingerprint() string {
	if !u.Enabled || u.Hash == "" {
		return ""
	}
	return PasswordFingerprint(u.Hash)
}

// clone returns a deep copy of u, including Access maps and slices.
func (u User) clone() User {
	u.Access = u.Access.clone()
	if u.BoardIDs != nil {
		u.BoardIDs = append([]string{}, u.BoardIDs...)
	}
	return u
}

// VisibleBoards returns section ids this user may open and whether every section is allowed.
// It is the single place that decides section visibility for a users.json account.
// envAdmin is true for the environment administrator (empty id or SourceEnv).
// An administrator and an AllBoards account see every section whatever BoardIDs holds:
// section membership only limits a regular user. A regular user with no BoardIDs sees main.
// When all is true, ids is nil and any section id is visible.
func (u User) VisibleBoards(envAdmin bool) (ids []string, all bool) {
	if envAdmin || u.ID == "" || u.Source == SourceEnv {
		return nil, true
	}
	if u.AllBoards || u.Type == UserAdmin {
		return nil, true
	}
	if len(u.BoardIDs) == 0 {
		return []string{config.MainBoardID}, false
	}
	seen := make(map[string]bool, len(u.BoardIDs))
	out := make([]string, 0, len(u.BoardIDs))
	for _, id := range u.BoardIDs {
		n := config.NormalizeBoardID(id)
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, false
}

// clone returns a deep copy of a; nil maps and slices stay nil.
func (a Access) clone() Access {
	if a.Grants != nil {
		grants := make(map[string]Grant, len(a.Grants))
		for k, g := range a.Grants {
			grants[k] = g.clone()
		}
		a.Grants = grants
	}
	if a.Sections != nil {
		a.Sections = append([]string{}, a.Sections...)
	}
	if a.Hidden != nil {
		a.Hidden = append([]string{}, a.Hidden...)
	}
	return a
}

func (g Grant) clone() Grant {
	if g.IDs != nil {
		g.IDs = append([]string{}, g.IDs...)
	}
	if g.Labels != nil {
		g.Labels = append([]string{}, g.Labels...)
	}
	return g
}

var (
	// ErrUserNotFound is returned when no user matches the id.
	ErrUserNotFound = errors.New("security: user not found")
	// ErrUserExists is returned when id or login is already taken.
	ErrUserExists = errors.New("security: user already exists")
	// ErrUserLogin is returned when the login string is invalid.
	ErrUserLogin = errors.New("security: invalid user login")
	// ErrUserType is returned when Type is not ADMIN or USER.
	ErrUserType = errors.New("security: invalid user type")
	// ErrLastAdmin is returned when a change would leave no enabled administrator.
	ErrLastAdmin = errors.New("security: last administrator")
)

// UserSet is the in-memory users.json collection.
type UserSet struct {
	mu            sync.RWMutex
	users         []*User
	byID          map[string]*User
	byLogin       map[string]*User
	outsideAdmins int
}

// NewUserSet returns an empty set safe for concurrent use.
func NewUserSet() *UserSet {
	return &UserSet{
		byID:    make(map[string]*User),
		byLogin: make(map[string]*User),
	}
}

// LoadUserSet reads users from path.
//
// A missing file returns an empty set and nil error. Any other read or JSON
// error returns nil and the error (fail-closed). Loaded records are validated:
// hash format, unique id and login, and allowed Type.
func LoadUserSet(path string) (*UserSet, error) {
	recs, err := readUserFile(path)
	if err != nil {
		return nil, err
	}
	s := NewUserSet()
	if err := s.loadRecords(recs); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload replaces the set from path.
//
// On error the previous set is unchanged.
func (s *UserSet) Reload(path string) error {
	recs, err := readUserFile(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceLocked(recs)
}

// Save writes the set to path as indented JSON with mode 0600.
func (s *UserSet) Save(path string) error {
	s.mu.RLock()
	snap := s.snapshot()
	s.mu.RUnlock()
	sort.Slice(snap, func(i, j int) bool {
		if !snap[i].CreatedAt.Equal(snap[j].CreatedAt) {
			return snap[i].CreatedAt.Before(snap[j].CreatedAt)
		}
		return snap[i].ID < snap[j].ID
	})
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicfile.Write(path, data, 0o600)
}

// List returns every user with Hash cleared, sorted newest CreatedAt first.
func (s *UserSet) List() []User {
	s.mu.RLock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		if u == nil {
			continue
		}
		out = append(out, u.Public())
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Get returns the user with id, including Hash, for authentication.
func (s *UserSet) Get(id string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.byID[id]
	if u == nil {
		return User{}, false
	}
	return u.clone(), true
}

// ByKeitaroID finds a user by Keitaro numeric id, including Hash.
func (s *UserSet) ByKeitaroID(id int) (User, bool) {
	if id <= 0 {
		return User{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u != nil && u.KeitaroID == id {
			return u.clone(), true
		}
	}
	return User{}, false
}

// ByLogin finds a user by login, case-insensitive, including Hash.
func (s *UserSet) ByLogin(login string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.byLogin[strings.ToLower(login)]
	if u == nil {
		return User{}, false
	}
	return u.clone(), true
}

// Len returns the number of stored users.
func (s *UserSet) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

// CountEnabledAdmins counts enabled users with Type ADMIN.
func (s *UserSet) CountEnabledAdmins() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.countEnabledAdminsLocked()
}

// countEnabledAdminsLocked counts enabled administrators; the caller holds s.mu.
func (s *UserSet) countEnabledAdminsLocked() int {
	n := 0
	for _, u := range s.users {
		if u != nil && u.Enabled && u.Type == UserAdmin {
			n++
		}
	}
	return n
}

// SetOutsideAdmins records how many enabled administrators live outside this set.
// n is that count; a negative value is stored as 0. It returns nothing.
// Update and Remove then treat those administrators as still present, so the
// last administrator stored in the set can be demoted or removed.
func (s *UserSet) SetOutsideAdmins(n int) {
	if n < 0 {
		n = 0
	}
	s.mu.Lock()
	s.outsideAdmins = n
	s.mu.Unlock()
}

// countEnabledAdminsExceptLocked counts enabled administrators other than exceptID.
// The caller holds s.mu for writing.
func (s *UserSet) countEnabledAdminsExceptLocked(exceptID string) int {
	n := s.outsideAdmins
	for _, u := range s.users {
		if u != nil && u.ID != exceptID && u.Enabled && u.Type == UserAdmin {
			n++
		}
	}
	return n
}

// Add inserts u after assigning id and CreatedAt when missing.
func (s *UserSet) Add(u User) error {
	if err := ValidateLogin(u.Login); err != nil {
		return err
	}
	if u.Type != UserAdmin && u.Type != UserRegular {
		return ErrUserType
	}
	if u.Hash != "" {
		if err := ValidateHash(u.Hash); err != nil {
			return err
		}
	}
	if u.grantsEmpty() {
		u.Access.Grants = make(map[string]Grant)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.ID == "" {
		id, err := RandomToken()
		if err != nil {
			return err
		}
		u.ID = id
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	if s.byID[u.ID] != nil {
		return ErrUserExists
	}
	key := strings.ToLower(u.Login)
	if s.byLogin[key] != nil {
		return ErrUserExists
	}
	rec := u.clonePtr()
	s.users = append(s.users, rec)
	s.byID[rec.ID] = rec
	s.byLogin[key] = rec
	return nil
}

// Update applies fn to the user with id and validates the result.
func (s *UserSet) Update(id string, fn func(*User) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.byID[id]
	if cur == nil {
		return ErrUserNotFound
	}
	updated := cur.clone()
	if err := fn(&updated); err != nil {
		return err
	}
	if err := ValidateLogin(updated.Login); err != nil {
		return err
	}
	if updated.Type != UserAdmin && updated.Type != UserRegular {
		return ErrUserType
	}
	if updated.Hash != "" {
		if err := ValidateHash(updated.Hash); err != nil {
			return err
		}
	}
	if updated.grantsEmpty() {
		updated.Access.Grants = make(map[string]Grant)
	}
	key := strings.ToLower(updated.Login)
	if other := s.byLogin[key]; other != nil && other.ID != id {
		return ErrUserExists
	}
	wasAdmin := cur.Enabled && cur.Type == UserAdmin
	willAdmin := updated.Enabled && updated.Type == UserAdmin
	if wasAdmin && !willAdmin && s.countEnabledAdminsExceptLocked(id) == 0 {
		return ErrLastAdmin
	}
	*cur = updated
	s.reindexLocked()
	return nil
}

// Remove deletes the user with id.
func (s *UserSet) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.byID[id]
	if cur == nil {
		return ErrUserNotFound
	}
	if cur.Enabled && cur.Type == UserAdmin && s.countEnabledAdminsExceptLocked(id) == 0 {
		return ErrLastAdmin
	}
	kept := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		if u != cur {
			kept = append(kept, u)
		}
	}
	s.users = kept
	s.reindexLocked()
	return nil
}

// ValidateLogin checks login for storage and sign-in lookup.
func ValidateLogin(login string) error {
	if login != strings.TrimSpace(login) {
		return ErrUserLogin
	}
	n := utf8.RuneCountInString(login)
	if n < 1 || n > maxLoginRunes {
		return ErrUserLogin
	}
	for _, r := range login {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrUserLogin
		}
	}
	if strings.HasPrefix(login, "apikey:") {
		return ErrUserLogin
	}
	return nil
}

func (u User) grantsEmpty() bool {
	return len(u.Access.Grants) == 0
}

// clonePtr returns a heap copy of u.
func (u *User) clonePtr() *User {
	c := u.clone()
	return &c
}

// snapshot copies all users for Save; the caller holds at least RLock.
func (s *UserSet) snapshot() []User {
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		if u != nil {
			out = append(out, u.clone())
		}
	}
	return out
}

// loadRecords replaces the set under the write lock after validation.
func (s *UserSet) loadRecords(recs []*User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceLocked(recs)
}

// replaceLocked validates recs and swaps indexes; the caller holds s.mu.
func (s *UserSet) replaceLocked(recs []*User) error {
	byID := make(map[string]*User, len(recs))
	byLogin := make(map[string]*User, len(recs))
	users := make([]*User, 0, len(recs))
	for _, u := range recs {
		if u == nil {
			continue
		}
		if u.Type != UserAdmin && u.Type != UserRegular {
			return fmt.Errorf("%w: id %q", ErrUserType, u.ID)
		}
		if u.Hash != "" {
			if err := ValidateHash(u.Hash); err != nil {
				return fmt.Errorf("user %q: %w", u.ID, err)
			}
		}
		if len(u.Access.Grants) == 0 {
			u.Access.Grants = make(map[string]Grant)
		}
		if byID[u.ID] != nil {
			return fmt.Errorf("%w: duplicate id %q", ErrUserExists, u.ID)
		}
		key := strings.ToLower(u.Login)
		if byLogin[key] != nil {
			return fmt.Errorf("%w: duplicate login %q", ErrUserExists, u.Login)
		}
		users = append(users, u)
		byID[u.ID] = u
		byLogin[key] = u
	}
	s.users = users
	s.byID = byID
	s.byLogin = byLogin
	return nil
}

// reindexLocked rebuilds byID and byLogin from s.users; the caller holds s.mu.
func (s *UserSet) reindexLocked() {
	s.byID = make(map[string]*User, len(s.users))
	s.byLogin = make(map[string]*User, len(s.users))
	for _, u := range s.users {
		if u == nil {
			continue
		}
		s.byID[u.ID] = u
		s.byLogin[strings.ToLower(u.Login)] = u
	}
}

// readUserFile parses users.json; a missing file returns nil, nil.
func readUserFile(path string) ([]*User, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var recs []User
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	out := make([]*User, 0, len(recs))
	for i := range recs {
		out = append(out, normalizeLoadedUser(recs[i]))
	}
	return out, nil
}

// normalizeLoadedUser prepares one decoded record for storage.
func normalizeLoadedUser(rec User) *User {
	if rec.Access.Grants == nil {
		rec.Access.Grants = make(map[string]Grant)
	}
	return rec.clonePtr()
}
