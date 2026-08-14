package sessions

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/sessionwire"
)

// Manager owns one Store's lifecycle and serializes composite writes per
// session ID. Successful construction transfers exclusive Store ownership to
// the Manager; callers must thereafter use Manager.Close.
type Manager struct {
	store  Store
	limits Limits
	life   drainClose

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
		store:  store,
		limits: normalized,
		locks:  make(map[string]*sessionLock),
	}
	manager.life.init()
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
	if err := m.life.enter(); err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			m.life.leave()
			return err
		}
	}
	return nil
}

func (m *Manager) leave() { m.life.leave() }

// Close is idempotent and concurrent-safe. The first call stops admission,
// waits for entered methods/leases, then closes the owned Store exactly once;
// every caller receives the same cached result.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	return m.life.close(func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("sessions: Store.Close panic: %v", recovered)
			}
		}()
		return m.store.Close()
	})
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
