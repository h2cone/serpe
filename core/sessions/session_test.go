package sessions

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/h2cone/ouro/core/models"
)

func TestValidateAcceptsMinimalSession(t *testing.T) {
	if err := New("s1", "/work").Validate(); err != nil {
		t.Fatalf("minimal session rejected: %v", err)
	}
	// Metadata may be nil; empty transcript is legal.
	s := New("s1", "/work")
	s.Metadata = map[string]string{"title": ""} // empty value is legal
	if err := s.Validate(); err != nil {
		t.Fatalf("session with empty metadata value rejected: %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		s    *Session
	}{
		{"nil", nil},
		{"empty ID", &Session{ID: "", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
		{"leading space ID", &Session{ID: " s1", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
		{"trailing space ID", &Session{ID: "s1 ", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
		{"whitespace parent", &Session{ID: "s1", ParentID: " p", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
		{"parent equals ID", &Session{ID: "s1", ParentID: "s1", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
		{"empty CWD", &Session{ID: "s1", CWD: "", CreatedAt: now, UpdatedAt: now}},
		{"blank CWD", &Session{ID: "s1", CWD: "   ", CreatedAt: now, UpdatedAt: now}},
		{"zero timestamps", &Session{ID: "s1", CWD: "/w"}},
		{"updated before created", &Session{ID: "s1", CWD: "/w", CreatedAt: now, UpdatedAt: now.Add(-time.Hour)}},
		{"invalid message role", &Session{ID: "s1", CWD: "/w", CreatedAt: now, UpdatedAt: now, Messages: []models.Message{{Role: "bogus"}}}},
		{"blank metadata key", &Session{ID: "s1", CWD: "/w", CreatedAt: now, UpdatedAt: now, Metadata: map[string]string{" k": "v"}}},
		{"path separator ID", &Session{ID: "a/b", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
		{"windows reserved ID", &Session{ID: "CON", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
		{"space in ID", &Session{ID: "has space", CWD: "/w", CreatedAt: now, UpdatedAt: now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.s.Validate(); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("Validate() = %v, want ErrInvalidSession", err)
			}
		})
	}
}

func TestNewTimestamps(t *testing.T) {
	s := New("s1", "/work")
	if !s.CreatedAt.Equal(s.UpdatedAt) {
		t.Fatalf("CreatedAt %v != UpdatedAt %v", s.CreatedAt, s.UpdatedAt)
	}
	if s.CreatedAt.Location() != time.UTC {
		t.Fatalf("New timestamp location = %v, want UTC", s.CreatedAt.Location())
	}
	if s.ID != "s1" || s.CWD != "/work" || s.ParentID != "" || len(s.Messages) != 0 || s.Metadata != nil {
		t.Fatalf("unexpected New result: %+v", s)
	}
}

func TestCloneDeep(t *testing.T) {
	img := models.ImageBytes("image/png", []byte{1, 2, 3})
	call := models.ToolCallContent("c1", "lookup", json.RawMessage(`{"q":"x"}`))
	result := models.ToolResultContent("c1", "lookup", false,
		models.Text("ok"), models.ImageBytes("image/png", []byte{9}))
	orig := &Session{
		ID:        "s1",
		ParentID:  "s0",
		CWD:       "/wd",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Messages: []models.Message{
			models.NewUserMessage(models.Text("hi"), img),
			{
				Role: models.RoleAssistant,
				Content: []models.Content{
					call, result, models.ReasoningSummary("r"), models.Refusal("no"),
				},
				ProviderState: &models.ProviderState{Provider: "p", Data: json.RawMessage(`{"k":1}`)},
			},
		},
		Metadata: map[string]string{"title": "t"},
	}
	clone := orig.Clone()
	if clone == orig {
		t.Fatal("Clone returned the same pointer")
	}
	if err := clone.Validate(); err != nil {
		t.Fatalf("clone is invalid: %v", err)
	}
	// Mutate the original at every depth.
	orig.CWD = "/other"
	orig.Metadata["title"] = "mutated"
	orig.Messages[0].Content[1].Image.Data[0] = 99
	orig.Messages[1].Content[0].ToolCall.Arguments[0] = 'X'
	orig.Messages[1].Content[1].ToolResult.Content[1].Image.Data[0] = 7
	orig.Messages[1].Content[2].ReasoningSummary.Text = "mutated"
	orig.Messages[1].Content[3].Refusal.Text = "mutated"
	orig.Messages[1].ProviderState.Data[0] = 'X'

	if clone.CWD != "/wd" || clone.Metadata["title"] != "t" {
		t.Fatal("Clone shares scalar or metadata storage")
	}
	if clone.Messages[0].Content[1].Image.Data[0] != 1 {
		t.Fatal("Clone shares image bytes")
	}
	if string(clone.Messages[1].Content[0].ToolCall.Arguments) != `{"q":"x"}` {
		t.Fatal("Clone shares tool call arguments")
	}
	if clone.Messages[1].Content[1].ToolResult.Content[1].Image.Data[0] != 9 {
		t.Fatal("Clone shares nested tool result content")
	}
	if clone.Messages[1].Content[2].ReasoningSummary.Text != "r" || clone.Messages[1].Content[3].Refusal.Text != "no" {
		t.Fatal("Clone shares reasoning or refusal content")
	}
	if string(clone.Messages[1].ProviderState.Data) != `{"k":1}` {
		t.Fatal("Clone shares provider state")
	}

	// Mutating the clone must not affect the original either.
	clone.Messages[0].Content[0].Text.Text = "changed"
	if orig.Messages[0].Content[0].Text.Text != "hi" {
		t.Fatal("Clone aliases back into the original")
	}
}

func TestCloneNil(t *testing.T) {
	var s *Session
	if got := s.Clone(); got != nil {
		t.Fatalf("Clone of nil = %v, want nil", got)
	}
}
