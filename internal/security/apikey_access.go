package security

import (
	"errors"
	"strings"
)

const (
	// KeyTypeAdmin is a key with the access of an administrator. An empty
	// APIKey.Type means the same.
	KeyTypeAdmin = "ADMIN"
	// KeyTypeUser is a key limited by Access or by the access of a role,
	// like a USER account.
	KeyTypeUser = "USER"

	// ActionKeitaroImport allows POST /api/keitaro/import.
	ActionKeitaroImport = "keitaro_import"
	// ActionKeitaroPurge allows POST /api/keitaro/purge.
	ActionKeitaroPurge = "keitaro_purge"
	// ActionLogsDelete allows DELETE /api/logs.
	ActionLogsDelete = "logs_delete"
	// ActionGroupsReplace allows PUT /api/groups, which replaces the whole array.
	ActionGroupsReplace = "groups_replace"
	// ActionCron allows PUT /api/cron and POST /api/cron/run.
	ActionCron = "cron"
	// ActionBoards allows deleting, moving and importing boards, and creating,
	// reordering, patching, member-replacing and assigning them.
	ActionBoards = "boards"
	// ActionLandingFiles allows landing archive upload and file writes and deletes,
	// including DELETE /api/landings/{id}.
	ActionLandingFiles = "landing_files"
	// ActionListsRead allows GET /api/lists and GET /api/lists/{name}.
	// It is not administrator-only: a USER key may hold it.
	ActionListsRead = "lists_read"
)

var (
	// ErrAPIKeyType is returned when the key type is neither empty, ADMIN nor USER.
	ErrAPIKeyType = errors.New("security: api key type must be ADMIN or USER")
	// ErrAPIKeyAction is returned when an action name is unknown.
	ErrAPIKeyAction = errors.New("security: unknown api key action")
	// ErrAPIKeyAccess is returned when a key has both a role and its own access.
	ErrAPIKeyAccess = errors.New("security: api key has a role and its own access")
	// ErrAPIKeyUserAction is returned when a USER key asks for an administrator-only action.
	ErrAPIKeyUserAction = errors.New("security: action is not allowed for a USER api key")
)

// actionList holds every action name in the order AllActions returns it.
var actionList = []string{
	ActionKeitaroImport, ActionKeitaroPurge, ActionLogsDelete, ActionGroupsReplace,
	ActionCron, ActionBoards, ActionLandingFiles, ActionListsRead,
}

// adminOnlyActions are the actions a USER key may not hold.
var adminOnlyActions = map[string]bool{ActionCron: true, ActionBoards: true}

// KeyAccess is the part of an APIKey that decides what the key may do on the admin API.
//
// Type is empty, KeyTypeAdmin or KeyTypeUser. RoleID names a role and replaces
// Access for a USER key. Access is the own access of a USER key. Actions lists
// the dangerous operations the key may call.
type KeyAccess struct {
	Type    string
	RoleID  string
	Access  *Access
	Actions []string
}

// AllActions returns every known action name as a new slice. There is no error.
func AllActions() []string {
	return append([]string{}, actionList...)
}

// ValidActions normalizes actions into the set this package stores.
//
// Each entry is trimmed. Duplicates are dropped and the first-seen order is
// kept. An empty list is valid and returns a nil slice. An unknown entry
// returns a nil slice and ErrAPIKeyAction.
func ValidActions(actions []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		action = strings.TrimSpace(action)
		if !knownAction(action) {
			return nil, ErrAPIKeyAction
		}
		if _, ok := seen[action]; ok {
			continue
		}
		seen[action] = struct{}{}
		out = append(out, action)
	}
	return out, nil
}

// knownAction reports whether name is one of the action constants.
func knownAction(name string) bool {
	for _, a := range actionList {
		if a == name {
			return true
		}
	}
	return false
}

// ValidKeyAccess normalizes ka into the form a key stores.
//
// Type must be empty, KeyTypeAdmin or KeyTypeUser (ErrAPIKeyType). Actions go
// through ValidActions (ErrAPIKeyAction). RoleID is trimmed. A key that is not
// USER keeps no role and no access, so those fields are cleared. A USER key
// with a role and a non-empty Access gets ErrAPIKeyAccess, and a USER key
// with ActionCron or ActionBoards gets ErrAPIKeyUserAction. The result shares
// no memory with ka.
func ValidKeyAccess(ka KeyAccess) (KeyAccess, error) {
	out := KeyAccess{Type: ka.Type}
	if out.Type != "" && out.Type != KeyTypeAdmin && out.Type != KeyTypeUser {
		return KeyAccess{}, ErrAPIKeyType
	}
	actions, err := ValidActions(ka.Actions)
	if err != nil {
		return KeyAccess{}, err
	}
	out.Actions = actions
	if out.Type != KeyTypeUser {
		return out, nil
	}
	for _, a := range out.Actions {
		if adminOnlyActions[a] {
			return KeyAccess{}, ErrAPIKeyUserAction
		}
	}
	out.RoleID = strings.TrimSpace(ka.RoleID)
	if ka.Access != nil {
		acc := ka.Access.clone()
		out.Access = &acc
	}
	if out.RoleID != "" && out.Access != nil && !out.Access.blank() {
		return KeyAccess{}, ErrAPIKeyAccess
	}
	if out.RoleID != "" {
		out.Access = nil
	}
	return out, nil
}

// blank reports whether a holds no section, grant or hidden field.
func (a Access) blank() bool {
	return len(a.Sections) == 0 && len(a.Grants) == 0 && len(a.Hidden) == 0
}

// keyAccess returns the access fields of k as a KeyAccess that shares memory with k.
func (k APIKey) keyAccess() KeyAccess {
	return KeyAccess{Type: k.Type, RoleID: k.RoleID, Access: k.Access, Actions: k.Actions}
}

// apply stores ka, already normalized by ValidKeyAccess, on k and marks k usable.
func (k *APIKey) apply(ka KeyAccess) {
	k.Type, k.RoleID, k.Access, k.Actions = ka.Type, ka.RoleID, ka.Access, ka.Actions
	k.invalid = false
}

// EffectiveType returns KeyTypeUser for a USER key and KeyTypeAdmin otherwise,
// so an empty Type reads as ADMIN. There is no error.
func (k APIKey) EffectiveType() string {
	if k.Type == KeyTypeUser {
		return KeyTypeUser
	}
	return KeyTypeAdmin
}

// HasAction reports whether action is listed on the key. There is no error.
func (k APIKey) HasAction(action string) bool {
	for _, have := range k.Actions {
		if have == action {
			return true
		}
	}
	return false
}

// GenerateWith adds a key like Generate and gives it the access in ka.
//
// ka goes through ValidKeyAccess, so ErrAPIKeyType, ErrAPIKeyAction,
// ErrAPIKeyAccess and ErrAPIKeyUserAction are returned with an empty raw and a
// zero key and the set is unchanged. Other results match Generate.
func (s *KeySet) GenerateWith(name string, scopes []string, ka KeyAccess) (raw string, key APIKey, err error) {
	name, scopes, err = normalizeKeyRequest(name, scopes)
	if err != nil {
		return "", APIKey{}, err
	}
	ka, err = ValidKeyAccess(ka)
	if err != nil {
		return "", APIKey{}, err
	}
	for range apiKeyGenAttempts {
		raw, rec, genErr := newAPIKey(name, scopes)
		if genErr != nil {
			return "", APIKey{}, genErr
		}
		rec.apply(ka)
		if s.storeNew(rec) {
			return raw, rec.clone(), nil
		}
	}
	return "", APIKey{}, errAPIKeyGenerate
}

// SetAccess replaces the type, role, access and actions of the active key id.
//
// ka goes through ValidKeyAccess and its errors leave the set unchanged. The
// returned key is a copy of the updated record, including Hash. ErrAPIKeyNotFound
// means no record has id. ErrAPIKeyRevoked means the record is not active.
func (s *KeySet) SetAccess(id string, ka KeyAccess) (APIKey, error) {
	ka, err := ValidKeyAccess(ka)
	if err != nil {
		return APIKey{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findID(id)
	if k == nil {
		return APIKey{}, ErrAPIKeyNotFound
	}
	if !k.Active() {
		return APIKey{}, ErrAPIKeyRevoked
	}
	k.apply(ka)
	return k.clone(), nil
}

// Get returns a copy of the key id with Hash cleared and whether it exists.
// A revoked key is returned too. There is no error.
func (s *KeySet) Get(id string) (APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := s.findID(id)
	if k == nil {
		return APIKey{}, false
	}
	return k.Public(), true
}
