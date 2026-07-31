package models

import (
	"encoding/json"
	"fmt"
)

// Role is the author of a conversational message.
type Role string

const (
	// RoleUser identifies user-authored content.
	RoleUser Role = "user"
	// RoleAssistant identifies model-authored content.
	RoleAssistant Role = "assistant"
)

// ProviderState is bounded opaque JSON required to continue a conversation on
// the same provider protocol. It must never contain credentials.
type ProviderState struct {
	Provider string
	Data     json.RawMessage
}

// Validate checks the provider identifier and JSON representation.
func (s ProviderState) Validate() error {
	if s.Provider == "" {
		return fmt.Errorf("provider state: provider is required")
	}
	if len(s.Data) == 0 || !json.Valid(s.Data) {
		return fmt.Errorf("provider state: data must be one valid JSON value")
	}
	return nil
}

func (s *ProviderState) clone() *ProviderState {
	if s == nil {
		return nil
	}
	return &ProviderState{Provider: s.Provider, Data: append(json.RawMessage(nil), s.Data...)}
}

// Message is an ordered collection of content blocks authored by a user or an
// assistant.
type Message struct {
	Role          Role
	Content       []Content
	ProviderState *ProviderState
}

// NewUserMessage creates a user message.
func NewUserMessage(content ...Content) Message {
	return Message{Role: RoleUser, Content: append([]Content(nil), content...)}
}

// NewAssistantMessage creates an assistant message.
func NewAssistantMessage(content ...Content) Message {
	return Message{Role: RoleAssistant, Content: append([]Content(nil), content...)}
}

// Validate checks the message and its content blocks.
func (m Message) Validate() error {
	if m.Role != RoleUser && m.Role != RoleAssistant {
		return fmt.Errorf("message: invalid role %q", m.Role)
	}
	if len(m.Content) == 0 && (m.Role != RoleAssistant || m.ProviderState == nil) {
		return fmt.Errorf("message: at least one content block is required")
	}
	for i := range m.Content {
		if err := m.Content[i].Validate(); err != nil {
			return fmt.Errorf("message content %d: %w", i, err)
		}
	}
	if m.ProviderState != nil {
		if m.Role != RoleAssistant {
			return fmt.Errorf("message: provider state is only valid on assistant messages")
		}
		if err := m.ProviderState.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (m Message) clone() Message {
	out := Message{Role: m.Role, ProviderState: m.ProviderState.clone()}
	out.Content = cloneContents(m.Content)
	return out
}
