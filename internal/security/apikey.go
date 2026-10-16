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

	"github.com/egerkuzma/kuztds/internal/atomicfile"
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
	// apiKeyBodyBytes is the entropy of the secret after the "kz_" prefix
	// (40 hex characters, so the raw key is 43 characters).
	apiKeyBodyBytes = 20
	// apiKeyPrefixLen is how many hex characters of that secret are kept for
	// display. The rest of the raw key is not stored.
	apiKeyPrefixLen = 8
	// apiKeyGenAttempts bounds retries when a fresh id or hash collides with
	// one already in the set.
	apiKeyGenAttempts = 3
)

const apiKeyPrefix = "kz_"

var (
	// ErrAPIKeyName is returned when the name is empty after trimming.
	ErrAPIKeyName = errors.New("security: api key name is required")
	// ErrAPIKeyNameLen is returned when the name is longer than 64 characters.
	ErrAPIKeyNameLen = errors.New("security: api key name is longer than 64 characters")
	// ErrAPIKeyScope is returned when scopes are empty or name an unknown scope.
	ErrAPIKeyScope = errors.New("security: api key scopes must be admin, engine, or both")
	// errAPIKeyGenerate is returned when every attempt collided with an existing
	// id or hash. It is not a validation error.
	errAPIKeyGenerate = errors.New("security: failed to generate a unique api key")
)

// APIKey is one admin or engine credential, stored without the raw secret.
//
// ID is 12 hex characters from crypto/rand. Prefix is the first 8 hex
// characters of the secret that follows "kz_", so a table can show which key
// is which. Hash is hex(sha256(raw)) and is written to the key file; Public
// clears it before an API response. Scopes contains ScopeAdmin, ScopeEngine,
// or both. RevokedAt is nil while the key is active, so the key file omits
// revoked_at for a live key.
type APIKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Hash      string     `json:"hash,omitempty"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Public returns a copy of k with Hash cleared.
//
// The copy does not share Scopes or RevokedAt with k. Callers may marshal it
// into an API response; there is no error.
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

// clone returns a copy of k whose Scopes and RevokedAt are not shared.
// Hash is kept. There is no error.
func (k APIKey) clone() APIKey {
	if k.Scopes != nil {
		k.Scopes = append([]string{}, k.Scopes...)
	}
	if k.RevokedAt != nil {
		t := *k.RevokedAt
		k.RevokedAt = &t
	}
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
// the read lock; Generate, Revoke, Save's snapshot and Reload take the write
// lock.
type KeySet struct {
	mu     sync.RWMutex
	keys   []*APIKey
	byHash map[string]*APIKey
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
//
// name is trimmed and must be non-empty and at most 64 Unicode characters.
// scopes must be a non-empty subset of ScopeAdmin and ScopeEngine; duplicates
// are removed. raw is "kz_" plus 40 hex characters from crypto/rand and is
// not stored. key is a copy of the stored record, including Hash. ErrAPIKeyName,
// ErrAPIKeyNameLen, ErrAPIKeyScope, a random-source error, or errAPIKeyGenerate
// is returned with an empty raw and a zero key; the set is unchanged then.
func (s *KeySet) Generate(name string, scopes []string) (raw string, key APIKey, err error) {
	name, scopes, err = normalizeKeyRequest(name, scopes)
	if err != nil {
		return "", APIKey{}, err
	}
	for range apiKeyGenAttempts {
		raw, rec, genErr := newAPIKey(name, scopes)
		if genErr != nil {
			return "", APIKey{}, genErr
		}
		if s.storeNew(rec) {
			return raw, rec.clone(), nil
		}
	}
	return "", APIKey{}, errAPIKeyGenerate
}

// Verify reports whether raw is an active key that has scope.
//
// An empty raw returns a zero key and false. Otherwise the SHA-256 of raw is
// looked up in byHash (the hash is not a secret, so the map key is enough to
// find it) and the stored hash is compared again with subtle.ConstantTimeCompare.
// A miss, a mismatch, a revoked key, or a key without scope returns a zero
// key and false. On success the returned key is a copy and includes Hash;
// call Public before writing it to an API response or a log.
func (s *KeySet) Verify(raw, scope string) (APIKey, bool) {
	if raw == "" {
		return APIKey{}, false
	}
	sum := sha256.Sum256([]byte(raw))
	hexHash := hex.EncodeToString(sum[:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := s.byHash[hexHash]
	if k == nil {
		return APIKey{}, false
	}
	if subtle.ConstantTimeCompare([]byte(k.Hash), []byte(hexHash)) != 1 {
		return APIKey{}, false
	}
	if !k.Active() || !k.HasScope(scope) {
		return APIKey{}, false
	}
	return k.clone(), true
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

// newAPIKey builds a record and the raw secret. The record is not stored yet.
//
// name and scopes are already normalized. raw is "kz_" plus 40 hex characters.
// The returned record holds only the hash and an 8-character prefix. An error
// means crypto/rand failed; nothing is stored.
func newAPIKey(name string, scopes []string) (string, *APIKey, error) {
	body, err := randomHex(apiKeyBodyBytes)
	if err != nil {
		return "", nil, err
	}
	id, err := randomHex(apiKeyIDBytes)
	if err != nil {
		return "", nil, err
	}
	raw := apiKeyPrefix + body
	sum := sha256.Sum256([]byte(raw))
	rec := &APIKey{
		ID:        id,
		Name:      name,
		Prefix:    body[:apiKeyPrefixLen],
		Hash:      hex.EncodeToString(sum[:]),
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

// hasID reports whether id is already stored. The caller holds s.mu.
// An empty id matches nothing. There is no error.
func (s *KeySet) hasID(id string) bool {
	if id == "" {
		return false
	}
	for _, k := range s.keys {
		if k != nil && k.ID == id {
			return true
		}
	}
	return false
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
// Save omits it. Scopes are copied. There is no error; a record this cannot
// fix is still stored and simply fails Verify.
func normalizeLoaded(rec APIKey) *APIKey {
	rec.Hash = strings.ToLower(strings.TrimSpace(rec.Hash))
	if rec.Scopes != nil {
		rec.Scopes = append([]string{}, rec.Scopes...)
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
