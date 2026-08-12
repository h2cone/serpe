package sessions

import (
	"context"
	"sync"
)

// MemoryStore is a concurrency-safe in-process Store. It copies byte records
// before writing and after reading, so internal data is never exposed after a
// lock is released.
type MemoryStore struct {
	mu    sync.RWMutex
	saved map[string][]byte
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{saved: make(map[string][]byte)}
}

// Create inserts an opaque record. See Store.Create.
func (s *MemoryStore) Create(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.saved[id]; ok {
		return ErrAlreadyExists
	}
	s.saved[id] = append([]byte(nil), data...)
	return nil
}

// Load returns an independent byte record. See Store.Load.
func (s *MemoryStore) Load(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validID(id) {
		return nil, invalidf("invalid ID %q", id)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	got, ok := s.saved[id]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), got...), nil
}

// Save replaces the stored record. See Store.Save.
func (s *MemoryStore) Save(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.saved[id]; !ok {
		return ErrNotFound
	}
	s.saved[id] = append([]byte(nil), data...)
	return nil
}

// Delete removes a record. See Store.Delete.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.saved[id]; !ok {
		return ErrNotFound
	}
	delete(s.saved, id)
	return nil
}

// List returns independent records. See Store.List.
func (s *MemoryStore) List(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, 0, len(s.saved))
	for id, data := range s.saved {
		out = append(out, Record{ID: id, Data: append([]byte(nil), data...)})
	}
	return out, nil
}
