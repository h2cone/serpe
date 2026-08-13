package sessions

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the package. All contextual errors wrap one of
// these with %w so callers can use errors.Is.
var (
	// ErrInvalidSession reports a nil session or a structurally invalid
	// session, ID, parent, CWD, timestamp, message, or metadata entry.
	ErrInvalidSession = errors.New("invalid session")
	// ErrNotFound reports that the target session does not exist. Deletion is
	// not silently idempotent.
	ErrNotFound = errors.New("session not found")
	// ErrAlreadyExists reports that a session with the same ID already exists.
	ErrAlreadyExists = errors.New("session already exists")
	// ErrConflict reports an optimistic-concurrency failure: the transcript
	// length observed before a write no longer matches at commit time.
	ErrConflict = errors.New("session transcript conflict")
	// ErrClosed reports use of a Store or Manager after Close linearized.
	ErrClosed = errors.New("sessions closed")
	// ErrRecordTooLarge reports a projected message that cannot be represented
	// by the configured detail/admission ceiling.
	ErrRecordTooLarge = errors.New("session record too large")
	// ErrCommitUncertain means atomic visibility succeeded but a subsequent
	// durability operation failed; callers must not blindly retry.
	ErrCommitUncertain = errors.New("session commit durability uncertain")
	// ErrMigrationRequired identifies a legacy FileStore layout that must be
	// migrated offline before normal use.
	ErrMigrationRequired = errors.New("session store migration required")
	// ErrStoreCorrupt identifies a contradictory marker or record filename.
	ErrStoreCorrupt = errors.New("session store corrupt")
)

// invalidf wraps a format string with ErrInvalidSession.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSession, fmt.Sprintf(format, args...))
}
