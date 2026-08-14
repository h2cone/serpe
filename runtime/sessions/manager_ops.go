package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/sessionwire"
)

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
