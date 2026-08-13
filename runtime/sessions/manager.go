package sessions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/sessionwire"
)

// Manager owns one Store's lifecycle and serializes composite writes per
// session ID. Successful construction transfers exclusive Store ownership to
// the Manager; callers must thereafter use Manager.Close.
type Manager struct {
	store  Store
	limits Limits

	stateMu   sync.Mutex
	stateCond *sync.Cond
	active    int
	closing   bool
	closeDone chan struct{}
	closeErr  error

	mu    sync.Mutex
	locks map[string]*sessionLock
}

// sessionLock is a cancellable per-ID lease. refs includes holders and
// waiters, allowing unused entries to be removed without racing acquisition.
type sessionLock struct {
	token chan struct{}
	refs  int
}

// SessionPatch is an atomic intent-shaped update. A nil CWD is unchanged. A
// nil metadata value deletes that key; unmentioned keys are preserved.
type SessionPatch struct {
	CWD      *string
	Metadata map[string]*string
}

// NewManager validates limits and takes lifecycle ownership of store. On
// failure ownership is not transferred and store is not closed.
func NewManager(store Store, limits Limits) (*Manager, error) {
	if store == nil || isNilStore(store) {
		return nil, fmt.Errorf("sessions: store is required")
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		store:     store,
		limits:    normalized,
		closeDone: make(chan struct{}),
		locks:     make(map[string]*sessionLock),
	}
	manager.stateCond = sync.NewCond(&manager.stateMu)
	return manager, nil
}

func isNilStore(store Store) bool {
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Limits returns the normalized immutable admission limits by value.
func (m *Manager) Limits() Limits {
	if m == nil {
		return Limits{}
	}
	return m.limits
}

func (m *Manager) enter(ctx context.Context) error {
	if m == nil {
		return ErrClosed
	}
	m.stateMu.Lock()
	if m.closing {
		m.stateMu.Unlock()
		return ErrClosed
	}
	m.active++
	m.stateMu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			m.leave()
			return err
		}
	}
	return nil
}

func (m *Manager) leave() {
	m.stateMu.Lock()
	m.active--
	if m.active == 0 {
		m.stateCond.Broadcast()
	}
	m.stateMu.Unlock()
}

// Close is idempotent and concurrent-safe. The first call stops admission,
// waits for entered methods/leases, then closes the owned Store exactly once;
// every caller receives the same cached result.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.stateMu.Lock()
	if m.closing {
		done := m.closeDone
		m.stateMu.Unlock()
		<-done
		m.stateMu.Lock()
		err := m.closeErr
		m.stateMu.Unlock()
		return err
	}
	m.closing = true
	for m.active != 0 {
		m.stateCond.Wait()
	}
	m.stateMu.Unlock()

	err := func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("sessions: Store.Close panic: %v", recovered)
			}
		}()
		return m.store.Close()
	}()
	m.stateMu.Lock()
	m.closeErr = err
	close(m.closeDone)
	m.stateMu.Unlock()
	return err
}

func (m *Manager) acquire(ctx context.Context, id string) (func(), error) {
	m.mu.Lock()
	lock := m.locks[id]
	if lock == nil {
		lock = &sessionLock{token: make(chan struct{}, 1)}
		lock.token <- struct{}{}
		m.locks[id] = lock
	}
	lock.refs++
	m.mu.Unlock()

	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	select {
	case <-lock.token:
		return func() {
			lock.token <- struct{}{}
			m.releaseRef(id, lock)
		}, nil
	case <-done:
		m.releaseRef(id, lock)
		return nil, ctx.Err()
	}
}

func (m *Manager) releaseRef(id string, lock *sessionLock) {
	m.mu.Lock()
	lock.refs--
	if lock.refs == 0 && m.locks[id] == lock {
		delete(m.locks, id)
	}
	m.mu.Unlock()
}

func (m *Manager) acquireTwo(ctx context.Context, first, second string) (func(), error) {
	if first == second {
		return m.acquire(ctx, first)
	}
	if first > second {
		first, second = second, first
	}
	releaseFirst, err := m.acquire(ctx, first)
	if err != nil {
		return nil, err
	}
	releaseSecond, err := m.acquire(ctx, second)
	if err != nil {
		releaseFirst()
		return nil, err
	}
	return func() {
		releaseSecond()
		releaseFirst()
	}, nil
}

func (m *Manager) load(ctx context.Context, id string) (*Session, error) {
	data, err := m.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	session, err := unmarshalSession(data)
	if err != nil {
		return nil, err
	}
	if session.ID != id {
		return nil, invalidf("record ID %q does not match key %q", session.ID, id)
	}
	if err := m.validateMessageSizes(session.Messages); err != nil {
		return nil, err
	}
	return session, nil
}

func (m *Manager) validateMessageSizes(messages []models.Message) error {
	for index := range messages {
		size, err := sessionwire.MessageFragmentSize(messages[index])
		if err != nil {
			return invalidf("message %d projection: %v", index, err)
		}
		if size > m.limits.MaxSessionMessageJSONBytes {
			return fmt.Errorf("%w: message %d exceeds %d JSON bytes", ErrRecordTooLarge, index, m.limits.MaxSessionMessageJSONBytes)
		}
	}
	return nil
}

func (m *Manager) validateCommit(session *Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	return m.validateMessageSizes(session.Messages)
}

// Create validates and atomically inserts a private snapshot.
func (m *Manager) Create(ctx context.Context, session *Session) (*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	if session == nil {
		return nil, invalidf("session is nil")
	}
	// Validate and measure caller-owned data before making a potentially large
	// defensive copy. Callers must not mutate it concurrently with Create.
	if err := m.validateCommit(session); err != nil {
		return nil, err
	}
	commit := session.Clone()
	data, err := marshalSession(commit)
	if err != nil {
		return nil, err
	}
	release, err := m.acquire(ctx, commit.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := m.store.Create(ctx, commit.ID, data); err != nil {
		return nil, err
	}
	return commit.Clone(), nil
}

// Get loads an independent validated snapshot.
func (m *Manager) Get(ctx context.Context, id string) (*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	if !validID(id) {
		return nil, invalidf("get: invalid ID %q", id)
	}
	return m.load(ctx, id)
}

// List is potentially expensive and retained for non-HTTP callers.
func (m *Manager) List(ctx context.Context) ([]*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	all, err := m.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(all))
	for _, record := range all {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !validID(record.ID) {
			continue
		}
		session, err := unmarshalSession(record.Data)
		if err != nil || session.ID != record.ID || m.validateMessageSizes(session.Messages) != nil {
			continue
		}
		out = append(out, session)
	}
	return out, nil
}

// ListPage loads at most one bounded ID page in ID order. Corrupt or deleted
// candidates are skipped without unbounded refill scanning.
func (m *Manager) ListPage(ctx context.Context, afterID string, limit int) ([]*Session, string, error) {
	if err := m.enter(ctx); err != nil {
		return nil, "", err
	}
	defer m.leave()
	ids, next, err := m.store.ListIDsPage(ctx, afterID, limit)
	if err != nil {
		return nil, "", err
	}
	out := make([]*Session, 0, len(ids))
	for _, id := range ids {
		session, err := m.load(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidSession) || errors.Is(err, ErrRecordTooLarge) {
				continue
			}
			return nil, "", err
		}
		out = append(out, session)
	}
	return out, next, nil
}

// Fork copies a source snapshot into a newly created child.
func (m *Manager) Fork(ctx context.Context, sourceID, newID string) (*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	if !validID(sourceID) || !validID(newID) || sourceID == newID {
		return nil, invalidf("fork: invalid or equal source/new ID")
	}
	release, err := m.acquireTwo(ctx, sourceID, newID)
	if err != nil {
		return nil, err
	}
	defer release()
	source, err := m.load(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("fork: load source %q: %w", sourceID, err)
	}
	now := time.Now().UTC()
	child := source.Clone()
	child.ID = newID
	child.ParentID = source.ID
	child.CreatedAt = now
	child.UpdatedAt = now
	if err := m.validateCommit(child); err != nil {
		return nil, err
	}
	data, err := marshalSession(child)
	if err != nil {
		return nil, err
	}
	if err := m.store.Create(ctx, child.ID, data); err != nil {
		return nil, err
	}
	return child.Clone(), nil
}

// Patch atomically applies CWD and metadata changes under one lease/Save.
func (m *Manager) Patch(ctx context.Context, id string, patch SessionPatch) (*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	owned, err := clonePatch(patch)
	if err != nil {
		return nil, err
	}
	if owned.CWD == nil && len(owned.Metadata) == 0 {
		if !validID(id) {
			return nil, invalidf("patch: invalid ID %q", id)
		}
		return m.load(ctx, id)
	}
	return m.apply(ctx, id, func(session *Session) error {
		if owned.CWD != nil {
			session.CWD = *owned.CWD
		}
		for key, value := range owned.Metadata {
			if value == nil {
				delete(session.Metadata, key)
				continue
			}
			if session.Metadata == nil {
				session.Metadata = make(map[string]string)
			}
			session.Metadata[key] = *value
		}
		if len(session.Metadata) == 0 {
			session.Metadata = nil
		}
		return nil
	})
}

func clonePatch(patch SessionPatch) (SessionPatch, error) {
	var owned SessionPatch
	if patch.CWD != nil {
		cwd := *patch.CWD
		if err := validateCWD(cwd); err != nil {
			return SessionPatch{}, err
		}
		owned.CWD = &cwd
	}
	if len(patch.Metadata) > 0 {
		owned.Metadata = make(map[string]*string, len(patch.Metadata))
		for key, value := range patch.Metadata {
			if !validID(key) {
				return SessionPatch{}, invalidf("patch metadata: invalid key %q", key)
			}
			if value == nil {
				owned.Metadata[key] = nil
				continue
			}
			copyValue := *value
			if key == "title" && (!validTextField(copyValue, 4<<10)) {
				return SessionPatch{}, invalidf("metadata title is invalid or exceeds 4096 bytes")
			}
			owned.Metadata[key] = &copyValue
		}
	}
	return owned, nil
}

func validTextField(value string, maxBytes int) bool {
	if len(value) > maxBytes {
		return false
	}
	return utf8.ValidString(value) && !containsControl(value)
}

// SetCWD delegates to the atomic Patch intent.
func (m *Manager) SetCWD(ctx context.Context, id, cwd string) (*Session, error) {
	return m.Patch(ctx, id, SessionPatch{CWD: &cwd})
}

// SetMetadata replaces the complete metadata map in one Save.
func (m *Manager) SetMetadata(ctx context.Context, id string, metadata map[string]string) (*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	owned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if !validID(key) {
			return nil, invalidf("metadata: invalid key %q", key)
		}
		if key == "title" && !validTextField(value, 4<<10) {
			return nil, invalidf("metadata title is invalid or exceeds 4096 bytes")
		}
		owned[key] = value
	}
	return m.apply(ctx, id, func(session *Session) error {
		if len(owned) == 0 {
			session.Metadata = nil
			return nil
		}
		session.Metadata = make(map[string]string, len(owned))
		for key, value := range owned {
			session.Metadata[key] = value
		}
		return nil
	})
}

// PatchMetadata delegates to Patch while preserving other fields.
func (m *Manager) PatchMetadata(ctx context.Context, id string, changes map[string]*string) (*Session, error) {
	return m.Patch(ctx, id, SessionPatch{Metadata: changes})
}

func (m *Manager) apply(ctx context.Context, id string, mutate func(*Session) error) (*Session, error) {
	if !validID(id) {
		return nil, invalidf("write: invalid ID %q", id)
	}
	release, err := m.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
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
	if err := m.validateCommit(work); err != nil {
		return nil, err
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

// Append commits messages in order as one atomic batch.
func (m *Manager) Append(ctx context.Context, id string, messages ...models.Message) (*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	owned, err := m.cloneMessages(messages)
	if err != nil {
		return nil, err
	}
	return m.apply(ctx, id, func(session *Session) error {
		session.Messages = append(session.Messages, owned...)
		return nil
	})
}

// AppendAt is a transcript-length CAS append.
func (m *Manager) AppendAt(ctx context.Context, id string, at int, messages ...models.Message) (*Session, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	if at < 0 {
		return nil, invalidf("append: negative expected length %d", at)
	}
	owned, err := m.cloneMessages(messages)
	if err != nil {
		return nil, err
	}
	return m.apply(ctx, id, func(session *Session) error {
		if len(session.Messages) != at {
			return ErrConflict
		}
		session.Messages = append(session.Messages, owned...)
		return nil
	})
}

func (m *Manager) cloneMessages(messages []models.Message) ([]models.Message, error) {
	if len(messages) == 0 {
		return nil, invalidf("append: at least one message is required")
	}
	owned := make([]models.Message, len(messages))
	for index := range messages {
		if err := messages[index].Validate(); err != nil {
			return nil, invalidf("append message %d: %v", index, err)
		}
		size, err := sessionwire.MessageFragmentSize(messages[index])
		if err != nil {
			return nil, invalidf("append message %d projection: %v", index, err)
		}
		if size > m.limits.MaxSessionMessageJSONBytes {
			return nil, fmt.Errorf("%w: append message %d exceeds %d JSON bytes", ErrRecordTooLarge, index, m.limits.MaxSessionMessageJSONBytes)
		}
		owned[index] = messages[index].Clone()
	}
	return owned, nil
}

// Delete serializes removal with writes to the same ID.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if err := m.enter(ctx); err != nil {
		return err
	}
	defer m.leave()
	if !validID(id) {
		return invalidf("delete: invalid ID %q", id)
	}
	release, err := m.acquire(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	return m.store.Delete(ctx, id)
}
