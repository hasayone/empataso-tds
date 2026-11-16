package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKeyExpiry(t *testing.T) {
	s := NewKeySet()
	raw, key, err := s.Generate("short", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	at, err := ExpiryAfter(7, now)
	if err != nil || at == nil || !at.Equal(now.AddDate(0, 0, 7)) {
		t.Fatalf("ExpiryAfter(7) = %v, %v", at, err)
	}
	if _, err := s.SetExpiry(key.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(raw, ScopeAdmin, now.AddDate(0, 0, 6)); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	if _, err := s.Check(raw, ScopeAdmin, *at); !errors.Is(err, ErrAPIKeyExpired) {
		t.Fatalf("at expiry: %v, want ErrAPIKeyExpired", err)
	}
	if _, err := s.Check("em_wrong", ScopeAdmin, now); !errors.Is(err, ErrAPIKeyUnknown) {
		t.Fatalf("wrong secret: %v", err)
	}
	past := time.Now().Add(-time.Second)
	if _, err := s.SetExpiry(key.ID, &past); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(raw, ScopeAdmin); ok {
		t.Fatal("Verify accepted an expired key")
	}
	extended, err := s.SetExpiry(key.ID, nil)
	if err != nil || extended.ExpiresAt != nil || extended.Hash != key.Hash {
		t.Fatalf("extend to never: %+v %v", extended, err)
	}
	if _, err := s.Check(raw, ScopeAdmin, now.AddDate(10, 0, 0)); err != nil {
		t.Fatalf("never-expiring key refused: %v", err)
	}
	for _, days := range []int{-1, 1, 14, 366} {
		if _, err := ExpiryAfter(days, now); !errors.Is(err, ErrAPIKeyTTL) {
			t.Errorf("ExpiryAfter(%d) = %v, want ErrAPIKeyTTL", days, err)
		}
	}
	if at, err := ExpiryAfter(0, now); at != nil || err != nil {
		t.Fatalf("ExpiryAfter(0) = %v, %v", at, err)
	}
	s.Revoke(key.ID)
	if _, err := s.SetExpiry(key.ID, nil); !errors.Is(err, ErrAPIKeyRevoked) {
		t.Fatalf("SetExpiry on a revoked key: %v", err)
	}
}

func TestKeyTouchIsThrottled(t *testing.T) {
	s := NewKeySet()
	_, key, err := s.Generate("busy", []string{ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if s.TakeDirty() {
		t.Fatal("a new set has no recorded use")
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	if !s.Touch(key.ID, "203.0.113.x", now) {
		t.Fatal("first use must be recorded")
	}
	for i := 1; i < 100; i++ {
		if s.Touch(key.ID, "198.51.100.x", now.Add(time.Duration(i)*500*time.Millisecond)) {
			t.Fatalf("use %d inside a minute was recorded", i)
		}
	}
	if !s.TakeDirty() || s.TakeDirty() {
		t.Fatal("TakeDirty must report one recorded use once")
	}
	got, _ := s.Get(key.ID)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) || got.LastUsedIP != "203.0.113.x" {
		t.Fatalf("last use: %+v", got)
	}
	if !s.Touch(key.ID, "198.51.100.x", now.Add(KeyTouchEvery)) || !s.TakeDirty() {
		t.Fatal("a use a minute later must be recorded")
	}
	if s.Touch("missing", "", now) {
		t.Fatal("unknown id recorded")
	}
	s.MarkDirty()
	if !s.TakeDirty() {
		t.Fatal("MarkDirty must be seen by TakeDirty")
	}
}

func TestOldKeyFileHasNoLifetime(t *testing.T) {
	path := writeKeyFile(t, "kz_old", "")
	s, err := LoadKeySet(path)
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.Check("kz_old", ScopeAdmin, time.Now().AddDate(50, 0, 0))
	if err != nil || k.ExpiresAt != nil || k.LastUsedAt != nil || k.LastUsedIP != "" || k.Lead != "" {
		t.Fatalf("old key must never expire and have no use: %+v %v", k, err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	at := now.AddDate(0, 0, 30)
	if _, err := s.SetExpiry(k.ID, &at); err != nil {
		t.Fatal(err)
	}
	s.Touch(k.ID, "203.0.113.x", now)
	out := filepath.Join(t.TempDir(), "keys.json")
	if err := s.Save(out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	for _, field := range []string{`"expires_at"`, `"last_used_at"`, `"last_used_ip": "203.0.113.x"`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("saved file lacks %s: %s", field, data)
		}
	}
	again, err := LoadKeySet(out)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Get(k.ID)
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(at) || got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Fatalf("round trip: %+v", got)
	}
}
