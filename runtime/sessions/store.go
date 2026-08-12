package sessions

import "context"

// Record is one opaque, versioned session payload stored under ID. Store
// implementations copy Data at their boundary; only Manager interprets it.
type Record struct {
	ID   string
	Data []byte
}

// Store persists opaque records. Implementations own storage mechanics only:
// concurrency, atomic visibility, byte ownership, context handling, and
// create-vs-replace semantics. Session fields, validation, cloning, and the
// record codec belong to Manager and are deliberately absent from this seam.
type Store interface {
	// Create inserts data under id, failing with ErrAlreadyExists if id exists.
	Create(ctx context.Context, id string, data []byte) error
	// Load returns an independent byte slice, failing with ErrNotFound if id
	// does not exist.
	Load(ctx context.Context, id string) ([]byte, error)
	// Save replaces data under id, failing with ErrNotFound if id does not
	// exist.
	Save(ctx context.Context, id string, data []byte) error
	// Delete removes the record, failing with ErrNotFound if id does not exist.
	Delete(ctx context.Context, id string) error
	// List returns independent records for every stored ID. Order is
	// undefined; callers that need a stable order must sort themselves.
	List(ctx context.Context) ([]Record, error)
}
