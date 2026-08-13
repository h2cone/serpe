package sessions

import (
	"container/heap"
	"context"
	"sort"
)

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
	// ListIDsPage returns at most limit IDs in strict UTF-8 byte order after
	// afterID. nextAfterID is the final returned ID only when another candidate
	// exists; otherwise it is empty.
	ListIDsPage(ctx context.Context, afterID string, limit int) (ids []string, nextAfterID string, err error)
	// Close is idempotent. Once it linearizes, data methods return ErrClosed.
	Close() error
}

func validatePage(afterID string, limit int) error {
	if afterID != "" && !validID(afterID) {
		return invalidf("invalid page cursor ID %q", afterID)
	}
	if limit < 1 || limit > 100 {
		return invalidf("page limit must be between 1 and 100")
	}
	return nil
}

type maxIDHeap []string

func (h maxIDHeap) Len() int           { return len(h) }
func (h maxIDHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h maxIDHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxIDHeap) Push(value any)    { *h = append(*h, value.(string)) }
func (h *maxIDHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	old[len(old)-1] = ""
	*h = old[:len(old)-1]
	return last
}

func selectPageID(selected *maxIDHeap, id, afterID string, capacity int) {
	if id <= afterID {
		return
	}
	if selected.Len() < capacity {
		heap.Push(selected, id)
		return
	}
	if id >= (*selected)[0] {
		return
	}
	(*selected)[0] = id
	heap.Fix(selected, 0)
}

func completeIDPage(selected *maxIDHeap, limit int) ([]string, string) {
	values := append([]string(nil), (*selected)...)
	sort.Strings(values)
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	if len(values) == 0 || !hasMore {
		return values, ""
	}
	return values, values[len(values)-1]
}
