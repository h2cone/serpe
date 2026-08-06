package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/h2cone/ouro/core/models"
)

// Manager serializes composite write operations per session ID and owns the
// commit semantics: an operation works on a private copy and only becomes
// visible when validation and the store write both succeed. Mutator errors,
// canceled contexts, and store errors never pollute the committed transcript.
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

// committed returns an independent snapshot of the state just written. It
// prefers a fresh Load so the caller observes store-normalized data, but if
// that Load fails (for example a context canceled after the write landed) it
// returns a clone of what was committed rather than reporting an error for a
// change that is already durable. This keeps "non-nil error means nothing was
// committed" true for callers even when the post-write Load races with
// cancellation.
func (m *Manager) committed(ctx context.Context, wrote *Session, id string) (*Session, error) {
	got, err := m.store.Load(ctx, id)
	if err == nil {
		return got, nil
	}
	return wrote.Clone(), nil
}

// Create validates and saves the snapshot, then returns an independent copy.
// The input is cloned at the Manager boundary, so the caller may mutate it
// freely after the call regardless of whether the store clones on write.
func (m *Manager) Create(ctx context.Context, session *Session) (*Session, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	commit := session.Clone()
	release := m.lock(session.ID)
	defer release()
	if err := m.store.Create(ctx, commit); err != nil {
		return nil, err
	}
	return m.committed(ctx, commit, session.ID)
}

// Get loads an independent, validated snapshot.
func (m *Manager) Get(ctx context.Context, id string) (*Session, error) {
	if !validID(id) {
		return nil, invalidf("get: invalid ID %q", id)
	}
	got, err := m.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := got.Validate(); err != nil {
		return nil, err
	}
	return got, nil
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

	src, err := m.store.Load(ctx, sourceID)
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
	// metadata, even with a store that does not clone on write.
	child := src.Clone()
	child.ID = newID
	child.ParentID = src.ID
	child.CreatedAt = now
	child.UpdatedAt = now
	if err := child.Validate(); err != nil {
		return nil, err
	}
	if err := m.store.Create(ctx, child); err != nil {
		return nil, err
	}
	return m.committed(ctx, child, newID)
}

// immutableCreatedAt marks the CreatedAt field on the working copy while a
// mutator runs. The Manager restores the real value afterwards; any mutator
// assignment to CreatedAt is detected by comparison with this marker, even on
// systems whose wall clock is too coarse to tell two time.Now() values apart.
//
// It is deliberately a recognizable non-zero instant (the Unix epoch) rather
// than the zero time.Time{}: it must not trip Session.Validate's IsZero guard
// if a mutator inspects or validates the working copy mid-mutation, while still
// being a value no real creation timestamp uses.
var immutableCreatedAt = time.Unix(0, 0).UTC()

// Update is the controlled extension point: it loads a snapshot, deep-copies
// it, runs the mutator on the private copy, re-validates, saves, and returns
// the new independent snapshot. The mutator pointer is valid only for the
// duration of the call. A mutator may only append to Messages and modify CWD
// and Metadata; ID, ParentID, and CreatedAt are immutable, existing messages
// must remain an unchanged prefix, and UpdatedAt assignments are ignored and
// replaced with a non-decreasing UTC time by the Manager.
//
// The per-session write lease is held for the whole mutator, so the mutator
// must not re-enter the Manager (Append, Update, Fork, or Delete) for the same
// session ID: Go's mutex is not reentrant and such a call deadlocks. Mutate
// only the private copy it receives.
func (m *Manager) Update(ctx context.Context, id string, mutate func(*Session) error) (*Session, error) {
	if mutate == nil {
		return nil, invalidf("update: nil mutator")
	}
	return m.apply(ctx, id, mutate, true)
}

// apply is the shared load-mutate-validate-save-commit pipeline for Update and
// Append. When verifyPrefix is true, existing messages are checked as an
// unchanged prefix (the Update contract); append-only callers pass false to
// skip that O(transcript) scan, since their mutator never touches existing
// messages.
func (m *Manager) apply(ctx context.Context, id string, mutate func(*Session) error, verifyPrefix bool) (*Session, error) {
	if !validID(id) {
		return nil, invalidf("update: invalid ID %q", id)
	}
	release := m.lock(id)
	defer release()

	current, err := m.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	work := current.Clone()
	work.CreatedAt = immutableCreatedAt
	if err := mutate(work); err != nil {
		return nil, err
	}
	if work.ID != current.ID || work.ParentID != current.ParentID || work.CreatedAt != immutableCreatedAt {
		return nil, invalidf("update: ID, ParentID, and CreatedAt are immutable")
	}
	work.CreatedAt = current.CreatedAt
	if len(work.Messages) < len(current.Messages) {
		return nil, invalidf("update: transcript must not be truncated")
	}
	if verifyPrefix {
		for i := range current.Messages {
			if !current.Messages[i].Equal(work.Messages[i]) {
				return nil, invalidf("update: existing transcript is immutable; only append")
			}
		}
	}
	if now := time.Now().UTC(); now.After(current.UpdatedAt) {
		work.UpdatedAt = now
	} else {
		work.UpdatedAt = current.UpdatedAt
	}
	if err := work.Validate(); err != nil {
		return nil, err
	}
	if err := m.store.Save(ctx, work); err != nil {
		return nil, err
	}
	return m.committed(ctx, work, id)
}

// Append commits the given messages in order as one atomic batch at the end
// of the transcript. An empty batch is rejected with ErrInvalidSession. It
// shares the Update pipeline but skips the existing-transcript prefix scan,
// since the mutator only appends.
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
	}, false)
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
