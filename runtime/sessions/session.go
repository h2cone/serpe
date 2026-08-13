package sessions

import (
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
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
//
// Session IDs (and metadata keys) use one portable alphabet shared by every
// Store backend: [A-Za-z0-9._-], length 1–128, not "."/"..", and not Windows
// reserved device names. MemoryStore and FileStore therefore accept the same
// IDs — no store-specific ID rules leak into callers.
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
	if err := validateCWD(s.CWD); err != nil {
		return err
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
	if title, ok := s.Metadata["title"]; ok {
		if !utf8.ValidString(title) || len(title) > 4<<10 || containsControl(title) {
			return invalidf("metadata title is invalid or exceeds 4096 bytes")
		}
	}
	return nil
}

func validateCWD(cwd string) error {
	if cwd == "" || !utf8.ValidString(cwd) {
		return invalidf("CWD is required and must be valid UTF-8")
	}
	if len(cwd) > 32<<10 {
		return invalidf("CWD exceeds 32768 bytes")
	}
	if containsControl(cwd) {
		return invalidf("CWD contains a control character")
	}
	if !filepath.IsAbs(cwd) {
		return invalidf("CWD must be absolute")
	}
	return nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
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

// validID reports whether id is a portable session identifier. The same rule
// applies to every Store backend and to metadata keys.
func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.TrimSpace(id) != id {
		return false
	}
	if len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return !isWindowsReservedName(id)
}

// ValidID reports whether id satisfies the portable Store/HTTP identifier
// grammar. Parent IDs may additionally be empty where their field permits it.
func ValidID(id string) bool { return validID(id) }

// isWindowsReservedName reports CON, PRN, AUX, NUL, COM1–COM9, LPT1–LPT9
// (case-insensitive), including extension variants such as CON.txt.
func isWindowsReservedName(id string) bool {
	base := id
	if i := strings.IndexByte(id, '.'); i >= 0 {
		base = id[:i]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}
