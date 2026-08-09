package sessions

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/h2cone/serpe/core/models"
)

// schemaVersion is the Manager-owned record format version. Bump when the
// codec shape changes and add migration for older records.
const schemaVersion = 1

// sessionRecord is the stable persisted JSON shape. Field names are
// independent of Go struct identifiers so core/models need not grow JSON tags
// for persistence. Content blocks use models.ContentRecord, the single
// content-kind table.
type sessionRecord struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	ParentID      string            `json:"parent_id"`
	CWD           string            `json:"cwd"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Messages      []recordMessage   `json:"messages"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type recordMessage struct {
	Role          string                 `json:"role"`
	Content       []models.ContentRecord `json:"content"`
	ProviderState *recordProviderState   `json:"provider_state,omitempty"`
}

type recordProviderState struct {
	Provider string          `json:"provider"`
	Data     json.RawMessage `json:"data"`
}

func marshalSession(s *Session) ([]byte, error) {
	if s == nil {
		return nil, invalidf("session is nil")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	out := sessionRecord{
		SchemaVersion: schemaVersion,
		ID:            s.ID,
		ParentID:      s.ParentID, // empty encodes as ""
		CWD:           s.CWD,
		CreatedAt:     s.CreatedAt.UTC(),
		UpdatedAt:     s.UpdatedAt.UTC(),
		Messages:      make([]recordMessage, len(s.Messages)),
	}
	for i := range s.Messages {
		dm, err := encodeMessage(s.Messages[i])
		if err != nil {
			return nil, invalidf("message %d: %v", i, err)
		}
		out.Messages[i] = dm
	}
	if len(s.Metadata) > 0 {
		out.Metadata = make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			out.Metadata[k] = v
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrInvalidSession, err)
	}
	return data, nil
}

func unmarshalSession(data []byte) (*Session, error) {
	var raw sessionRecord
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrInvalidSession, err)
	}
	if raw.SchemaVersion != schemaVersion {
		return nil, invalidf("unsupported schema_version %d (want %d)", raw.SchemaVersion, schemaVersion)
	}
	s := &Session{
		ID:        raw.ID,
		ParentID:  raw.ParentID,
		CWD:       raw.CWD,
		CreatedAt: raw.CreatedAt.UTC(),
		UpdatedAt: raw.UpdatedAt.UTC(),
		Messages:  make([]models.Message, len(raw.Messages)),
	}
	for i := range raw.Messages {
		m, err := decodeMessage(raw.Messages[i])
		if err != nil {
			return nil, invalidf("message %d: %v", i, err)
		}
		s.Messages[i] = m
	}
	// Normalize metadata like Session.Clone: nil when empty.
	if len(raw.Metadata) > 0 {
		s.Metadata = make(map[string]string, len(raw.Metadata))
		for k, v := range raw.Metadata {
			s.Metadata[k] = v
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func encodeMessage(m models.Message) (recordMessage, error) {
	out := recordMessage{
		Role:    string(m.Role),
		Content: make([]models.ContentRecord, len(m.Content)),
	}
	for i := range m.Content {
		rec, err := models.EncodeContent(m.Content[i])
		if err != nil {
			return recordMessage{}, fmt.Errorf("content %d: %w", i, err)
		}
		out.Content[i] = rec
	}
	if m.ProviderState != nil {
		if err := m.ProviderState.Validate(); err != nil {
			return recordMessage{}, err
		}
		out.ProviderState = &recordProviderState{
			Provider: m.ProviderState.Provider,
			Data:     append(json.RawMessage(nil), m.ProviderState.Data...),
		}
	}
	return out, nil
}

func decodeMessage(m recordMessage) (models.Message, error) {
	out := models.Message{
		Role:    models.Role(m.Role),
		Content: make([]models.Content, len(m.Content)),
	}
	for i := range m.Content {
		c, err := models.DecodeContent(m.Content[i])
		if err != nil {
			return models.Message{}, fmt.Errorf("content %d: %w", i, err)
		}
		out.Content[i] = c
	}
	if m.ProviderState != nil {
		out.ProviderState = &models.ProviderState{
			Provider: m.ProviderState.Provider,
			Data:     append(json.RawMessage(nil), m.ProviderState.Data...),
		}
	}
	return out, nil
}
