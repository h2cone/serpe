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
)

// invalidf wraps a format string with ErrInvalidSession.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSession, fmt.Sprintf(format, args...))
}
