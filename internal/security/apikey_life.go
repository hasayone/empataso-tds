package security

import (
	"errors"
	"time"
)

// KeyTouchEvery is the shortest gap between two recorded uses of one key.
// A use inside the gap only answers the request; nothing is written.
const KeyTouchEvery = time.Minute

// KeyTTLDays lists the lifetimes a key may be given, in days. Zero means the
// key never expires.
var KeyTTLDays = []int{0, 7, 30, 90, 365}

var (
	// ErrAPIKeyExpired is returned by Check for a stored, active key whose
	// ExpiresAt has passed.
	ErrAPIKeyExpired = errors.New("api key expired")
	// ErrAPIKeyUnknown is returned by Check for a secret that is not an active,
	// valid key with the scope asked for.
	ErrAPIKeyUnknown = errors.New("api key unknown")
	// ErrAPIKeyTTL is returned when a lifetime is not one of KeyTTLDays.
	ErrAPIKeyTTL = errors.New("security: api key lifetime must be 0, 7, 30, 90 or 365 days")
)

// Expired reports whether k has an expiry at or before now.
// A nil or zero ExpiresAt never expires. There is no error.
func (k APIKey) Expired(now time.Time) bool {
	return k.ExpiresAt != nil && !k.ExpiresAt.IsZero() && !now.Before(*k.ExpiresAt)
}

// ExpiryAfter returns the expiry of a key given days of life from now.
//
// days must be one of KeyTTLDays; zero returns nil, a key that never expires.
// Any other value returns ErrAPIKeyTTL.
func ExpiryAfter(days int, now time.Time) (*time.Time, error) {
	for _, d := range KeyTTLDays {
		if d != days {
			continue
		}
		if days == 0 {
			return nil, nil
		}
		t := now.UTC().AddDate(0, 0, days)
		return &t, nil
	}
	return nil, ErrAPIKeyTTL
}

// Check is Verify with the reason of a refusal.
//
// raw and scope are as in Verify; now decides expiry. It returns a copy of the
// key, Hash included, and nil when the key is accepted. ErrAPIKeyExpired means
// the secret matches an active, valid key with scope whose ExpiresAt has passed.
// Every other refusal is ErrAPIKeyUnknown, so a caller cannot tell a revoked key
// from a wrong secret.
func (s *KeySet) Check(raw, scope string, now time.Time) (APIKey, error) {
	k, ok := s.lookup(raw)
	if !ok || k.invalid || !k.Active() || !k.HasScope(scope) {
		return APIKey{}, ErrAPIKeyUnknown
	}
	if k.Expired(now) {
		return APIKey{}, ErrAPIKeyExpired
	}
	return k, nil
}

// SetExpiry replaces the expiry of the active key id without changing its secret.
//
// at nil makes the key never expire. The returned key is a copy including Hash.
// ErrAPIKeyNotFound means no record has id; ErrAPIKeyRevoked means it is not active.
func (s *KeySet) SetExpiry(id string, at *time.Time) (APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findID(id)
	if k == nil {
		return APIKey{}, ErrAPIKeyNotFound
	}
	if !k.Active() {
		return APIKey{}, ErrAPIKeyRevoked
	}
	k.ExpiresAt = copyTime(at)
	return k.clone(), nil
}

// Touch records that key id was used at now from ip.
//
// ip is already masked by the caller and may be empty. A use less than
// KeyTouchEvery after the recorded one is ignored, so a busy key changes memory
// at most once a minute. A recorded use marks the set dirty for TakeDirty. It
// returns whether the use was recorded; an unknown id records nothing.
func (s *KeySet) Touch(id, ip string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.findID(id)
	if k == nil {
		return false
	}
	if k.LastUsedAt != nil && now.Sub(*k.LastUsedAt) < KeyTouchEvery {
		return false
	}
	t := now.UTC()
	k.LastUsedAt = &t
	k.LastUsedIP = ip
	s.dirty = true
	return true
}

// TakeDirty reports whether a use was recorded since the last call and clears
// the mark. The caller saves the set when it returns true and calls MarkDirty
// when that save fails. There is no error.
func (s *KeySet) TakeDirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dirty
	s.dirty = false
	return d
}

// MarkDirty sets the mark TakeDirty reads, so a failed save is retried.
// There is no error.
func (s *KeySet) MarkDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// copyTime returns a heap copy of t, or nil for nil. There is no error.
func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
