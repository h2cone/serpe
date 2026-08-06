package sessions

import (
	"strings"
	"time"

	"github.com/h2cone/ouro/core/models"
)

// Session is a provider-neutral conversation snapshot.
//
// ID is generated and owned by the caller; ParentID expresses an optional
// direct fork/copy lineage; CWD is an opaque, persistable working directory
// string; CreatedAt is set once at creation; UpdatedAt is maintained by the
// Manager; Messages is the ordered transcript including internal tool-call
// rounds; Metadata is the only lightweight extension point for string
// attributes such as title, owner, or tenant. It must not carry credentials,
// large objects, or provider-private state.
type Session struct {
	ID        string
	ParentID  string
	CWD       string
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  []models.Message
	Metadata  map[string]string
}

// New creates a session with the given ID and CWD and a single UTC timestamp
// shared by CreatedAt and UpdatedAt. It retains no caller-mutable data.
func New(id, cwd string) *Session {
	now := time.Now().UTC()
	return &Session{ID: id, CWD: cwd, CreatedAt: now, UpdatedAt: now}
}

// Validate checks the provider-neutral structural invariants of the session.
func (s *Session) Validate() error {
	if s == nil {
		return invalidf("session is nil")
	}
	if !validID(s.ID) {
		return invalidf("invalid ID %q", s.ID)
	}
	if s.ParentID != "" && (!validID(s.ParentID) || s.ParentID == s.ID) {
		return invalidf("invalid parent ID %q", s.ParentID)
	}
	if strings.TrimSpace(s.CWD) == "" {
		return invalidf("CWD is required")
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return invalidf("CreatedAt and UpdatedAt must be set")
	}
	if s.UpdatedAt.Before(s.CreatedAt) {
		return invalidf("UpdatedAt precedes CreatedAt")
	}
	for i := range s.Messages {
		if err := s.Messages[i].Validate(); err != nil {
			return invalidf("message %d: %v", i, err)
		}
	}
	for k := range s.Metadata {
		if !validID(k) {
			return invalidf("invalid metadata key %q", k)
		}
	}
	return nil
}

// Clone returns a deep copy safe for the caller to retain and modify.
func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}
	out := &Session{
		ID:        s.ID,
		ParentID:  s.ParentID,
		CWD:       s.CWD,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
		Messages:  make([]models.Message, len(s.Messages)),
	}
	for i := range s.Messages {
		out.Messages[i] = s.Messages[i].Clone()
	}
	if s.Metadata != nil {
		out.Metadata = make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// validID reports a non-empty ID without leading or trailing whitespace. The
// package does not otherwise constrain ID formats.
func validID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id
}
