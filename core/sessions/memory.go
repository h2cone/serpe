package sessions

import (
	"context"
	"sync"
)

// MemoryStore is a concurrency-safe in-process Store. It is the default
// store for single-process use and the contract reference for third-party
// implementations. It copies before writing and after reading, so internal
// data is never exposed after a lock is released.
type MemoryStore struct {
	mu    sync.RWMutex
	saved map[string]*Session
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{saved: make(map[string]*Session)}
}

// Create inserts the session. See Store.Create.
func (s *MemoryStore) Create(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.saved[session.ID]; ok {
		return ErrAlreadyExists
	}
	s.saved[session.ID] = session.Clone()
	return nil
}

// Load returns an independent snapshot. See Store.Load.
func (s *MemoryStore) Load(ctx context.Context, id string) (*Session, error) {
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
	return got.Clone(), nil
}

// Save replaces the stored record. See Store.Save.
func (s *MemoryStore) Save(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.saved[session.ID]; !ok {
		return ErrNotFound
	}
	s.saved[session.ID] = session.Clone()
	return nil
}

// Delete removes the session. See Store.Delete.
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
