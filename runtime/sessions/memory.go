package sessions

import (
	"container/heap"
	"context"
	"sync"
)

// MemoryStore is a concurrency-safe in-process Store. It copies byte records
// before writing and after reading, so internal data is never exposed after a
// lock is released.
type MemoryStore struct {
	mu     sync.RWMutex
	saved  map[string][]byte
	closed bool
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{saved: make(map[string][]byte)}
}

// Create inserts an opaque record. See Store.Create.
func (s *MemoryStore) Create(ctx context.Context, id string, data []byte) error {
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, ok := s.saved[id]; ok {
		return ErrAlreadyExists
	}
	s.saved[id] = append([]byte(nil), data...)
	return nil
}

// Load returns an independent byte record. See Store.Load.
func (s *MemoryStore) Load(ctx context.Context, id string) ([]byte, error) {
	if !validID(id) {
		return nil, invalidf("invalid ID %q", id)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	got, ok := s.saved[id]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), got...), nil
}

// Save replaces the stored record. See Store.Save.
func (s *MemoryStore) Save(ctx context.Context, id string, data []byte) error {
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, ok := s.saved[id]; !ok {
		return ErrNotFound
	}
	s.saved[id] = append([]byte(nil), data...)
	return nil
}

// Delete removes a record. See Store.Delete.
func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, ok := s.saved[id]; !ok {
		return ErrNotFound
	}
	delete(s.saved, id)
	return nil
}

// List returns independent records. See Store.List.
func (s *MemoryStore) List(ctx context.Context) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(s.saved))
	for id, data := range s.saved {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		out = append(out, Record{ID: id, Data: append([]byte(nil), data...)})
	}
	return out, nil
}

// ListIDsPage selects only the smallest limit+1 candidates and never builds a
// full sorted copy of the map keys.
func (s *MemoryStore) ListIDsPage(ctx context.Context, afterID string, limit int) ([]string, string, error) {
	if err := validatePage(afterID, limit); err != nil {
		return nil, "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, "", ErrClosed
	}
	if err := contextError(ctx); err != nil {
		return nil, "", err
	}
	selected := &maxIDHeap{}
	heap.Init(selected)
	seen := 0
	for id := range s.saved {
		seen++
		if seen&255 == 0 {
			if err := contextError(ctx); err != nil {
				return nil, "", err
			}
		}
		selectPageID(selected, id, afterID, limit+1)
	}
	ids, next := completeIDPage(selected, limit)
	return ids, next, nil
}

// Close prevents new data operations. MemoryStore has no external handle, so
// the first and every subsequent call return nil.
func (s *MemoryStore) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
