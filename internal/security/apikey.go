package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
)

const (
	// ScopeAdmin authorizes calls to the admin API.
	ScopeAdmin = "admin"
	// ScopeEngine authorizes calls to the engine API.
	ScopeEngine = "engine"

	// maxAPIKeyNameLen is the longest accepted key name, in Unicode characters.
	maxAPIKeyNameLen = 64
	// apiKeyIDBytes is the entropy of APIKey.ID (12 hex characters).
	apiKeyIDBytes = 6
	// apiKeyBodyBytes is the entropy of the secret after the "em_" prefix
	// (40 hex characters, so the raw key is 43 characters).
	apiKeyBodyBytes = 20
	// apiKeyPrefixLen is how many hex characters of that secret are kept for
	// display. The rest of the raw key is not stored.
	apiKeyPrefixLen = 8
	// apiKeyGenAttempts bounds retries when a fresh id or hash collides with
	// one already in the set.
	apiKeyGenAttempts = 3
)

// APIKeyPrefix starts every key the tracker issues.
const APIKeyPrefix = "em_"

// HasAPIKeyPrefix reports whether raw starts with APIKeyPrefix.
// raw is the secret as sent by the client. There is no error.
func HasAPIKeyPrefix(raw string) bool {
	return strings.HasPrefix(raw, APIKeyPrefix)
}

var (
	// ErrAPIKeyName is returned when the name is empty after trimming.
	ErrAPIKeyName = errors.New("security: api key name is required")
	// ErrAPIKeyNameLen is returned when the name is longer than 64 characters.
	ErrAPIKeyNameLen = errors.New("security: api key name is longer than 64 characters")
	// ErrAPIKeyScope is returned when scopes are empty or name an unknown scope.
	ErrAPIKeyScope = errors.New("security: api key scopes must be admin, engine, or both")
	// ErrAPIKeyNotFound is returned when no stored key has the given id.
	ErrAPIKeyNotFound = errors.New("api key not found")
	// ErrAPIKeyRevoked is returned when the key exists but is not active.
	ErrAPIKeyRevoked = errors.New("api key revoked")
	// ErrAPIKeyActive is returned when Remove is asked to drop a key that is still active.
	ErrAPIKeyActive = errors.New("api key is active")
	// errAPIKeyGenerate is returned when every attempt collided with an existing
	// id or hash. It is not a validation error.
	errAPIKeyGenerate = errors.New("security: failed to generate a unique api key")
)

// APIKey is one admin or engine credential, stored without the raw secret.
//
// ID is 12 hex characters from crypto/rand. Prefix is the first 8 hex
// characters of the secret that follows APIKeyPrefix, so a table can show which
// key is which. Hash is hex(sha256(raw)) and is written to the key file; Public
// clears it before an API response. Scopes contains ScopeAdmin, ScopeEngine,
// or both. RevokedAt is nil while the key is active, so the key file omits
// revoked_at for a live key.
//
// Type, RoleID, Access and Actions decide what the key may do on the admin API
// (see KeyAccess). A key file written before they existed has none of them,
// which reads as an ADMIN key without dangerous actions. A key whose Type or
// Actions the file spells wrong is marked invalid when loaded and Verify
// refuses it, so a typo never widens access.
type APIKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Hash      string     `json:"hash,omitempty"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Type      string     `json:"type,omitempty"`
	RoleID    string     `json:"role_id,omitempty"`
	Access    *Access    `json:"access,omitempty"`
	Actions   []string   `json:"actions,omitempty"`

	// ExpiresAt nil means the key never expires; a record written before the
	// field existed has none.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// LastUsedAt and LastUsedIP are the last recorded admin API use, at most
	// KeyTouchEvery stale; LastUsedIP is masked (a.b.c.x or an IPv6 /48).
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`

	// invalid is set by normalizeLoaded when the stored access fields fail
	// ValidKeyAccess. It is not part of the key file.
	invalid bool
}

// Public returns a copy of k with Hash cleared.
//
// The copy does not share Scopes, RevokedAt, Access or Actions with k. Callers
// may marshal it into an API response; there is no error.
func (k APIKey) Public() APIKey {
	cp := k.clone()
	cp.Hash = ""
	return cp
}

// Active reports whether the key has not been revoked.
//
// A nil RevokedAt and a zero time both count as active, matching a key file
// that omits revoked_at. There is no error.
func (k APIKey) Active() bool {
	return k.RevokedAt == nil || k.RevokedAt.IsZero()
}

// HasScope reports whether scope is listed on the key.
//
// scope is compared exactly, after ValidScopes has already trimmed and
// deduplicated what Generate stores. There is no error.
func (k APIKey) HasScope(scope string) bool {
	for _, have := range k.Scopes {
		if have == scope {
			return true
		}
	}
	return false
}

// clone returns a copy of k whose Scopes, RevokedAt, Access and Actions are
// not shared. Hash is kept. There is no error.
func (k APIKey) clone() APIKey {
	if k.Scopes != nil {
		k.Scopes = append([]string{}, k.Scopes...)
	}
	if k.Actions != nil {
		k.Actions = append([]string{}, k.Actions...)
	}
	if k.Access != nil {
		acc := k.Access.clone()
		k.Access = &acc
	}
	k.RevokedAt = copyTime(k.RevokedAt)
	k.ExpiresAt = copyTime(k.ExpiresAt)
	k.LastUsedAt = copyTime(k.LastUsedAt)
	return k
}

// ValidScopes normalizes scopes into the set this package stores.
//
// Each entry is trimmed. Duplicates are dropped and the first-seen order is
// kept. The result is a new slice, or a nil slice and ErrAPIKeyScope when the
// list is empty or any entry is not ScopeAdmin or ScopeEngine.
func ValidScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, ErrAPIKeyScope
	}
	out := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope != ScopeAdmin && scope != ScopeEngine {
			return nil, ErrAPIKeyScope
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out, nil
}

// KeySet is the in-memory collection of API keys.
//
// mu guards keys and byHash. byHash indexes the stored records by Hash so
// Verify does not scan. The raw secret is never put in the set. Readers take
// the read lock; Generate, Rotate, Update, Remove, Revoke, Save's snapshot
// and Reload take the write lock.
type KeySet struct {
	mu     sync.RWMutex
	keys   []*APIKey
	byHash map[string]*APIKey
	// dirty is set by Touch and cleared by TakeDirty.
	dirty bool
}

// NewKeySet returns an empty set safe for concurrent use. There is no error.
func NewKeySet() *KeySet {
	return &KeySet{byHash: make(map[string]*APIKey)}
}

// LoadKeySet reads the key file at path.
//
// A missing file returns an empty set and a nil error, which is how a new
// install starts. Any other read error, and JSON that is not an array of
// keys, returns a nil set and the error. The raw secret is not in the file.
func LoadKeySet(path string) (*KeySet, error) {
	keys, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}
	s := NewKeySet()
	s.keys = keys
	s.reindex()
	return s, nil
}

// Reload replaces the set with the keys stored at path.
//
// On success the previous records are dropped as one assignment under the
// write lock. On a read or JSON error the set is left as it was and err is
// the error from reading the file. A missing file is not an error: the set
// becomes empty, same as LoadKeySet.
func (s *KeySet) Reload(path string) error {
	keys, err := readKeyFile(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
	s.reindex()
	return nil
}

// Save writes the set to path as a JSON array of APIKey records, hash included.
//
// Records are sorted by CreatedAt ascending, then by ID. The write goes
// through atomicfile.Write with mode 0600, so a crash cannot leave a torn
// file and other users on the machine cannot read the hashes. The raw secret
// is not a field and is not written. An error comes from encoding or from
// the write; the in-memory set is unchanged either way.
func (s *KeySet) Save(path string) error {
	s.mu.RLock()
	snap := s.snapshot()
	s.mu.RUnlock()
	sort.Slice(snap, func(i, j int) bool { return earlier(snap[i], snap[j]) })
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicfile.Write(path, data, 0o600)
}

// Generate adds a key named name with scopes and returns the raw secret once.
// The key has no type, role, access or actions, which reads as an ADMIN key
// without dangerous actions; GenerateWith sets them.
//
// name is trimmed and must be non-empty and at most 64 Unicode characters.
// scopes must be a non-empty subset of ScopeAdmin and ScopeEngine; duplicates
// are removed. raw is "em_" plus 40 hex characters from crypto/rand and is
// not stored. key is a copy of the stored record, including Hash. ErrAPIKeyName,
// ErrAPIKeyNameLen, ErrAPIKeyScope, a random-source error, or errAPIKeyGenerate
// is returned with an empty raw and a zero key; the set is unchanged then.
func (s *KeySet) Generate(name string, scopes []string) (raw string, key APIKey, err error) {
	return s.GenerateWith(name, scopes, KeyAccess{})
}

// Verify reports whether raw is an active key that has scope.
//
// An empty raw, or one that does not start with APIKeyPrefix, returns a zero key
// and false. Otherwise the SHA-256 of raw is
// looked up in byHash (the hash is not a secret, so the map key is enough to
// find it) and the stored hash is compared again with subtle.ConstantTimeCompare.
// A miss, a mismatch, a revoked or expired key, a key without scope, or a key whose
// stored type or actions are invalid returns a zero key and false. On success the returned key is a copy and includes Hash;
// call Public before writing it to an API response or a log. Check tells an expired key apart.
func (s *KeySet) Verify(raw, scope string) (APIKey, bool) {
	k, err := s.Check(raw, scope, time.Now())
	return k, err == nil
}

// lookup returns a copy of the record whose hash matches raw.
//
// raw must carry APIKeyPrefix. The SHA-256 of raw is
// looked up in byHash and compared again with subtle.ConstantTimeCompare. The
// copy keeps the invalid mark. A miss returns a zero key and false. There is no error.
func (s *KeySet) lookup(raw string) (APIKey, bool) {
	if !HasAPIKeyPrefix(raw) {
		return APIKey{}, false
	}
	sum := sha256.Sum256([]byte(raw))
	hexHash := hex.EncodeToString(sum[:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := s.byHash[hexHash]
	if k == nil || subtle.ConstantTimeCompare([]byte(k.Hash), []byte(hexHash)) != 1 {
		return APIKey{}, false
	}
	return k.clone(), true
}

// Rotate replaces the secret of the active key id and returns the new raw secret once.
//
// id selects the record. raw is "em_" plus 40 hex characters and is not stored.
// key is a copy of the updated record, including Hash. ID, Name, Scopes and
// CreatedAt stay as they were. ErrAPIKeyNotFound means no record has id.
// ErrAPIKeyRevoked means the record exists but is not active. A random-source
// error or errAPIKeyGenerate is returned when a unique hash cannot be made;
// the set is unchanged then.
func (s *KeySet) Rotate(id string) (raw string, key APIKey, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findID(id)
	if k == nil {
		return "", APIKey{}, ErrAPIKeyNotFound
	}
	if !k.Active() {
		return "", APIKey{}, ErrAPIKeyRevoked
	}
	for range apiKeyGenAttempts {
		var prefix, hash string
		raw, prefix, hash, err = freshSecret()
		if err != nil {
			return "", APIKey{}, err
		}
		if other, exists := s.byHash[hash]; exists && other != k {
			continue
		}
		if s.byHash[k.Hash] == k {
			delete(s.byHash, k.Hash)
		}
		k.Prefix = prefix
		k.Hash = hash
		s.byHash[hash] = k
		return raw, k.clone(), nil
	}
	return "", APIKey{}, errAPIKeyGenerate
}

// Update changes the name and scopes of the active key id.
//
// name is trimmed and must be non-empty and at most 64 Unicode characters.
// scopes must be a non-empty subset of ScopeAdmin and ScopeEngine; duplicates
// are removed. The returned key is a copy of the updated record, including
// Hash. ID, Prefix, Hash, CreatedAt and RevokedAt stay as they were.
// ErrAPIKeyName, ErrAPIKeyNameLen and ErrAPIKeyScope match Generate and leave
// the set unchanged. ErrAPIKeyNotFound means no record has id. ErrAPIKeyRevoked
// means the record exists but is not active.
func (s *KeySet) Update(id, name string, scopes []string) (APIKey, error) {
	name, scopes, err := normalizeKeyRequest(name, scopes)
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
	k.Name = name
	k.Scopes = append([]string{}, scopes...)
	return k.clone(), nil
}

// Remove drops a revoked key with id from the set.
//
// id selects the record. ErrAPIKeyNotFound means no record has id.
// ErrAPIKeyActive means the record is still active and must be revoked first.
// On success the record is gone from keys and byHash. There is no other error.
func (s *KeySet) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findID(id)
	if k == nil {
		return ErrAPIKeyNotFound
	}
	if k.Active() {
		return ErrAPIKeyActive
	}
	if s.byHash[k.Hash] == k {
		delete(s.byHash, k.Hash)
	}
	kept := make([]*APIKey, 0, len(s.keys))
	for _, item := range s.keys {
		if item == k {
			continue
		}
		kept = append(kept, item)
	}
	s.keys = kept
	return nil
}

// Revoke marks every stored key with id as revoked at the current time.
//
// It returns false when id is empty, when no record has that id, or when
// every match is already revoked. A second call for the same id returns
// false and does not move RevokedAt. There is no error value.
func (s *KeySet) Revoke(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	revoked := false
	now := time.Now().UTC()
	for _, k := range s.keys {
		if k == nil || k.ID != id {
			continue
		}
		found = true
		if !k.Active() {
			continue
		}
		t := now
		k.RevokedAt = &t
		revoked = true
	}
	return found && revoked
}

// List returns a copy of every key, newest CreatedAt first, with Hash cleared.
//
// Each element is Public, so the slice is safe to marshal into an API
// response. Equal timestamps are ordered by ID descending. An empty set
// returns an empty slice, not nil. There is no error.
func (s *KeySet) List() []APIKey {
	s.mu.RLock()
	out := make([]APIKey, 0, len(s.keys))
	for _, k := range s.keys {
		if k == nil {
			continue
		}
		out = append(out, k.Public())
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return earlier(out[j], out[i]) })
	return out
}

// normalizeKeyRequest trims name and normalizes scopes before a key is built.
//
// It returns the cleaned name, the scope slice from ValidScopes, and
// ErrAPIKeyName, ErrAPIKeyNameLen, or ErrAPIKeyScope. It does not touch the set.
func normalizeKeyRequest(name string, scopes []string) (string, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, ErrAPIKeyName
	}
	if utf8.RuneCountInString(name) > maxAPIKeyNameLen {
		return "", nil, ErrAPIKeyNameLen
	}
	norm, err := ValidScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	return name, norm, nil
}

// freshSecret builds one raw key and the prefix and hash stored beside it.
//
// raw is "em_" plus 40 hex characters from crypto/rand. prefix is the first
// 8 hex characters of the secret body. hash is hex(sha256(raw)). An error
// means crypto/rand failed. Nothing is stored.
func freshSecret() (raw, prefix, hash string, err error) {
	body, err := randomHex(apiKeyBodyBytes)
	if err != nil {
		return "", "", "", err
	}
	raw = APIKeyPrefix + body
	sum := sha256.Sum256([]byte(raw))
	return raw, body[:apiKeyPrefixLen], hex.EncodeToString(sum[:]), nil
}

// newAPIKey builds a record and the raw secret. The record is not stored yet.
//
// name and scopes are already normalized. raw, prefix and hash come from
// freshSecret. The returned record holds only the hash and an 8-character
// prefix. An error means crypto/rand failed; nothing is stored.
func newAPIKey(name string, scopes []string) (string, *APIKey, error) {
	raw, prefix, hash, err := freshSecret()
	if err != nil {
		return "", nil, err
	}
	id, err := randomHex(apiKeyIDBytes)
	if err != nil {
		return "", nil, err
	}
	rec := &APIKey{
		ID:        id,
		Name:      name,
		Prefix:    prefix,
		Hash:      hash,
		Scopes:    append([]string{}, scopes...),
		CreatedAt: time.Now().UTC(),
	}
	return raw, rec, nil
}

// randomHex returns nBytes of crypto/rand as lowercase hex, twice nBytes long.
// An error means the system random source failed.
func randomHex(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// storeNew inserts rec when its id and hash are not already present.
//
// It returns false when either collides, and the caller tries another key.
// rec must be non-nil. There is no error; the collision is the false result.
func (s *KeySet) storeNew(rec *APIKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec == nil || rec.Hash == "" || s.hasID(rec.ID) {
		return false
	}
	if _, exists := s.byHash[rec.Hash]; exists {
		return false
	}
	s.keys = append(s.keys, rec)
	s.byHash[rec.Hash] = rec
	return true
}

// findID returns the stored record with id. The caller holds s.mu.
//
// An empty id and an unknown id return nil. There is no error.
func (s *KeySet) findID(id string) *APIKey {
	if id == "" {
		return nil
	}
	for _, k := range s.keys {
		if k != nil && k.ID == id {
			return k
		}
	}
	return nil
}

// hasID reports whether id is already stored. The caller holds s.mu.
// An empty id matches nothing. There is no error.
func (s *KeySet) hasID(id string) bool {
	return s.findID(id) != nil
}

// snapshot copies the stored records. The caller holds s.mu.
// Nil slots are skipped. Copies do not share Scopes or RevokedAt. There is no error.
func (s *KeySet) snapshot() []APIKey {
	out := make([]APIKey, 0, len(s.keys))
	for _, k := range s.keys {
		if k == nil {
			continue
		}
		out = append(out, k.clone())
	}
	return out
}

// reindex rebuilds byHash from keys. The caller holds s.mu, or the set is
// not yet shared (LoadKeySet).
//
// An empty hash is not indexed. When two records share a hash, Verify finds
// the later one. There is no error.
func (s *KeySet) reindex() {
	s.byHash = make(map[string]*APIKey, len(s.keys))
	for _, k := range s.keys {
		if k == nil || k.Hash == "" {
			continue
		}
		s.byHash[k.Hash] = k
	}
}

// earlier reports whether a sorts before b by CreatedAt ascending, then ID.
// Equal timestamps stay deterministic. There is no error.
func earlier(a, b APIKey) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// readKeyFile parses the JSON array at path into stored records.
//
// A missing file returns a nil slice and a nil error. A read error or JSON
// that is not an array of APIKey returns an error and no records. Hashes are
// lowercased so Verify's hex digest matches a file written with upper case.
// A zero RevokedAt is cleared so a live key stays active.
func readKeyFile(path string) ([]*APIKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var recs []APIKey
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	keys := make([]*APIKey, 0, len(recs))
	for _, rec := range recs {
		keys = append(keys, normalizeLoaded(rec))
	}
	return keys, nil
}

// normalizeLoaded returns a heap copy of rec ready to store.
//
// Hash is trimmed and lowercased. A zero RevokedAt becomes nil so the next
// Save omits it. Scopes, Actions and Access are copied. The type, role, access
// and actions are kept as the file spells them, and the record is marked
// invalid when ValidKeyAccess refuses them. There is no error; a record this
// cannot fix is still stored and simply fails Verify.
func normalizeLoaded(rec APIKey) *APIKey {
	rec.Hash = strings.ToLower(strings.TrimSpace(rec.Hash))
	if rec.Scopes != nil {
		rec.Scopes = append([]string{}, rec.Scopes...)
	}
	rec = rec.clone()
	_, err := ValidKeyAccess(rec.keyAccess())
	rec.invalid = err != nil
	if rec.ExpiresAt != nil && rec.ExpiresAt.IsZero() {
		rec.ExpiresAt = nil
	}
	if rec.RevokedAt != nil {
		if rec.RevokedAt.IsZero() {
			rec.RevokedAt = nil
		} else {
			t := *rec.RevokedAt
			rec.RevokedAt = &t
		}
	}
	return &rec
}
