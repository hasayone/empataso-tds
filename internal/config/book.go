package config

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// Book is an in-memory set of catalog records loaded from a JSON array.
// A missing file is an empty book, so the engine can start before the admin
// creates the file.
type Book[T any] struct {
	mu    sync.RWMutex
	path  string
	idOf  func(T) string
	byID  map[string]T
	mod   int64
	size  int64
	ready bool
}

// OpenBook reads path. A missing file yields an empty book and a nil error.
func OpenBook[T any](path string, idOf func(T) string) (*Book[T], error) {
	b := &Book[T]{path: path, idOf: idOf, byID: map[string]T{}}
	if path == "" {
		return b, nil
	}
	if err := b.Reload(); err != nil {
		return nil, err
	}
	return b, nil
}

// All returns every record currently held.
func (b *Book[T]) All() []T {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]T, 0, len(b.byID))
	for _, v := range b.byID {
		out = append(out, v)
	}
	return out
}

// ByID returns the id-to-record map the book currently publishes, without a
// copy. Reload replaces the whole map and never changes a published one, so
// the snapshot stays valid and consistent while the caller reads it.
// The caller must not write to the returned map or to its values' slices
// and maps. A nil book yields a nil map, which reads as empty.
func (b *Book[T]) ByID() map[string]T {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.byID
}

// Get returns the record with the given id.
func (b *Book[T]) Get(id string) (T, bool) {
	var zero T
	if b == nil {
		return zero, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.byID[id]
	return v, ok
}

// Reload rereads the file when its size or modification time changed.
// A missing file clears the book only before the first successful read.
func (b *Book[T]) Reload() error {
	if b == nil || b.path == "" {
		return nil
	}
	fi, err := os.Stat(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	stamp := fi.ModTime().UnixNano()
	if b.ready && stamp == b.mod && fi.Size() == b.size {
		return nil
	}
	raw, err := os.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var list []T
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			return err
		}
	}
	byID := make(map[string]T, len(list))
	for _, item := range list {
		id := b.idOf(item)
		if id != "" {
			byID[id] = item
		}
	}
	b.mu.Lock()
	b.byID = byID
	b.mod = stamp
	b.size = fi.Size()
	b.ready = true
	b.mu.Unlock()
	return nil
}
