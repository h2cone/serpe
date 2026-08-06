package sessions

import "context"

// Store persists session snapshots. Each call must be concurrency-safe with
// atomic visibility, must obey the package deep-copy rules, and must leave no
// half-applied state visible when it returns an error. Create only inserts,
// Save only replaces an existing record; neither uses fuzzy upsert semantics.
// Stores validate their input themselves and must not modify state if the
// context is already canceled.
type Store interface {
	// Create inserts the session, failing with ErrAlreadyExists if the ID
	// exists.
	Create(ctx context.Context, session *Session) error
	// Load returns an independent snapshot, failing with ErrNotFound if the
	// ID does not exist.
	Load(ctx context.Context, id string) (*Session, error)
	// Save replaces the stored record with the given snapshot, failing with
	// ErrNotFound if the ID does not exist.
	Save(ctx context.Context, session *Session) error
	// Delete removes the session, failing with ErrNotFound if the ID does not
	// exist.
	Delete(ctx context.Context, id string) error
}
