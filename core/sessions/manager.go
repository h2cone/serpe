package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/h2cone/serpe/core/models"
)

// Manager serializes composite write operations per session ID and owns the
// commit semantics: an operation works on a private copy and only becomes
// visible when validation and the store write both succeed. Validation,
// cancellation, and store errors never pollute the committed transcript.
type Manager struct {
	store Store
	mu    sync.Mutex
	locks map[string]*sessionLock
}

// sessionLock is a per-ID write lease with reference counting so the lock
// table cannot grow forever.
type sessionLock struct {
	mu   sync.Mutex
	refs int
}

// NewManager creates a Manager over the given store. It returns an error if
// store is nil; it never creates an implicit global store.
func NewManager(store Store) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("sessions: store is required")
	}
	return &Manager{store: store, locks: make(map[string]*sessionLock)}, nil
}

// lock acquires the write lease for id and returns its release function.
func (m *Manager) lock(id string) func() {
	m.mu.Lock()
	l, ok := m.locks[id]
	if !ok {
		l = &sessionLock{}
		m.locks[id] = l
	}
	l.refs++
	m.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		m.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(m.locks, id)
		}
		m.mu.Unlock()
	}
}

// lockTwo acquires the write leases for two IDs in stable order to avoid
// lock-order deadlocks, and returns a release function for both.
func (m *Manager) lockTwo(a, b string) func() {
	if a == b {
		return m.lock(a)
	}
	if a > b {
		a, b = b, a
	}
	unlockA := m.lock(a)
	unlockB := m.lock(b)
	return func() {
		unlockB()
		unlockA()
	}
}

// load is the single record-to-domain boundary. Backends never interpret a
// Session; Manager decodes, validates, and verifies that the stored payload's
// identity matches its storage key.
func (m *Manager) load(ctx context.Context, id string) (*Session, error) {
	data, err := m.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	got, err := unmarshalSession(data)
	if err != nil {
		return nil, err
	}
	if got.ID != id {
		return nil, invalidf("record ID %q does not match key %q", got.ID, id)
	}
	return got, nil
}

// Create validates and saves the snapshot, then returns an independent copy.
// The input is cloned before encoding, so no caller-owned Session memory
// crosses the Store boundary.
func (m *Manager) Create(ctx context.Context, session *Session) (*Session, error) {
	commit := session.Clone()
	data, err := marshalSession(commit)
	if err != nil {
		return nil, err
	}
	release := m.lock(commit.ID)
	defer release()
	if err := m.store.Create(ctx, commit.ID, data); err != nil {
		return nil, err
	}
	return commit.Clone(), nil
}

// Get loads an independent, validated snapshot.
func (m *Manager) Get(ctx context.Context, id string) (*Session, error) {
	if !validID(id) {
		return nil, invalidf("get: invalid ID %q", id)
	}
	return m.load(ctx, id)
}

// List returns independent snapshots of every stored session. Order is
// undefined; the store is not required to sort. Invalid stored rows are
// skipped so a single corrupt record cannot break the whole listing.
func (m *Manager) List(ctx context.Context) ([]*Session, error) {
	all, err := m.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(all))
	for _, record := range all {
		if !validID(record.ID) {
			continue
		}
		s, err := unmarshalSession(record.Data)
		if err != nil || s.ID != record.ID {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// Fork deep-copies the source's CWD, Messages, and Metadata into a new
// session with the given ID, ParentID set to the source ID, and fresh
// creation/update timestamps. It creates nothing when source and new ID are
// the same, the source is missing, or the new ID already exists.
func (m *Manager) Fork(ctx context.Context, sourceID, newID string) (*Session, error) {
	if !validID(sourceID) || !validID(newID) {
		return nil, invalidf("fork: invalid ID")
	}
	if sourceID == newID {
		return nil, invalidf("fork: source and new ID must differ")
	}
	release := m.lockTwo(sourceID, newID)
	defer release()

	src, err := m.load(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("fork: load source %q: %w", sourceID, err)
	}
	if _, err := m.store.Load(ctx, newID); err == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	// Deep-copy the source so the child never aliases the source transcript or
	// metadata before the two snapshots are encoded independently.
	child := src.Clone()
	child.ID = newID
	child.ParentID = src.ID
	child.CreatedAt = now
	child.UpdatedAt = now
	data, err := marshalSession(child)
	if err != nil {
		return nil, err
	}
	if err := m.store.Create(ctx, child.ID, data); err != nil {
		return nil, err
	}
	return child.Clone(), nil
}

// SetCWD atomically updates the session working directory.
func (m *Manager) SetCWD(ctx context.Context, id, cwd string) (*Session, error) {
	if strings.TrimSpace(cwd) == "" {
		return nil, invalidf("set cwd: CWD is required")
	}
	return m.apply(ctx, id, func(s *Session) error {
		s.CWD = cwd
		return nil
	})
}

// SetMetadata replaces the session metadata map. A nil map clears metadata.
// Keys must satisfy the portable ID alphabet.
func (m *Manager) SetMetadata(ctx context.Context, id string, metadata map[string]string) (*Session, error) {
	return m.apply(ctx, id, func(s *Session) error {
		if metadata == nil {
			s.Metadata = nil
			return nil
		}
		s.Metadata = make(map[string]string, len(metadata))
		for k, v := range metadata {
			s.Metadata[k] = v
		}
		return nil
	})
}

// PatchMetadata atomically applies key changes to the current metadata. A
// non-nil value sets a key and a nil value deletes it. Unmentioned keys are
// preserved. The complete read-modify-write runs under the session's write
// lease, so concurrent patches cannot overwrite unrelated keys.
//
// The changes map and pointed-to strings are copied before the write begins;
// the caller may reuse or mutate them after PatchMetadata returns. An empty
// patch performs a read without changing UpdatedAt.
func (m *Manager) PatchMetadata(ctx context.Context, id string, changes map[string]*string) (*Session, error) {
	if len(changes) == 0 {
		return m.Get(ctx, id)
	}
	owned := make(map[string]*string, len(changes))
	for key, value := range changes {
		if !validID(key) {
			return nil, invalidf("patch metadata: invalid key %q", key)
		}
		if value == nil {
			owned[key] = nil
			continue
		}
		ownedValue := *value
		owned[key] = &ownedValue
	}
	return m.apply(ctx, id, func(s *Session) error {
		for key, value := range owned {
			if value == nil {
				delete(s.Metadata, key)
				continue
			}
			if s.Metadata == nil {
				s.Metadata = make(map[string]string)
			}
			s.Metadata[key] = *value
		}
		if len(s.Metadata) == 0 {
			s.Metadata = nil
		}
		return nil
	})
}

// apply is the package-private load-mutate-validate-save transaction used by
// intent-shaped writes. Its callbacks are defined next to the public method;
// arbitrary caller code never runs while the per-session lease is held.
func (m *Manager) apply(ctx context.Context, id string, mutate func(*Session) error) (*Session, error) {
	if !validID(id) {
		return nil, invalidf("write: invalid ID %q", id)
	}
	release := m.lock(id)
	defer release()

	current, err := m.load(ctx, id)
	if err != nil {
		return nil, err
	}
	work := current.Clone()
	if err := mutate(work); err != nil {
		return nil, err
	}
	if now := time.Now().UTC(); now.After(current.UpdatedAt) {
		work.UpdatedAt = now
	} else {
		work.UpdatedAt = current.UpdatedAt
	}
	data, err := marshalSession(work)
	if err != nil {
		return nil, err
	}
	if err := m.store.Save(ctx, id, data); err != nil {
		return nil, err
	}
	return work.Clone(), nil
}

// Append commits the given messages in order as one atomic batch at the end
// of the transcript. An empty batch is rejected with ErrInvalidSession.
func (m *Manager) Append(ctx context.Context, id string, messages ...models.Message) (*Session, error) {
	if len(messages) == 0 {
		return nil, invalidf("append: at least one message is required")
	}
	return m.apply(ctx, id, func(s *Session) error {
		for i := range messages {
			if err := messages[i].Validate(); err != nil {
				return invalidf("append message %d: %v", i, err)
			}
			s.Messages = append(s.Messages, messages[i].Clone())
		}
		return nil
	})
}

// AppendAt commits messages only when len(transcript) equals at (optimistic
// CAS-append). When the length has changed, it returns ErrConflict and leaves
// the stored transcript unchanged. An empty batch or negative at is rejected
// with ErrInvalidSession.
func (m *Manager) AppendAt(ctx context.Context, id string, at int, messages ...models.Message) (*Session, error) {
	if len(messages) == 0 {
		return nil, invalidf("append: at least one message is required")
	}
	if at < 0 {
		return nil, invalidf("append: negative expected length %d", at)
	}
	return m.apply(ctx, id, func(s *Session) error {
		if len(s.Messages) != at {
			return ErrConflict
		}
		for i := range messages {
			if err := messages[i].Validate(); err != nil {
				return invalidf("append message %d: %v", i, err)
			}
			s.Messages = append(s.Messages, messages[i].Clone())
		}
		return nil
	})
}

// Delete removes the session, sharing the serialization boundary with all
// other operations on the same ID.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return invalidf("delete: invalid ID %q", id)
	}
	release := m.lock(id)
	defer release()
	return m.store.Delete(ctx, id)
}
