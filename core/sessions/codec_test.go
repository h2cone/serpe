package sessions

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/h2cone/ouro/core/models"
)

func TestCodecRoundTripAllContentKinds(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	s := &Session{
		ID:        "sess-1",
		ParentID:  "",
		CWD:       "/work",
		CreatedAt: now,
		UpdatedAt: now.Add(time.Second),
		Metadata:  map[string]string{"title": "repo Q&A"},
		Messages: []models.Message{
			models.NewUserMessage(
				models.Text("What is in this repo?"),
				models.ImageURI("https://example.com/a.png"),
			),
			func() models.Message {
				img := models.ImageBytes("image/png", []byte{0x89, 0x50, 0x4e, 0x47})
				img.Image.Detail = models.ImageDetailAuto
				m := models.NewAssistantMessage(
					models.Text("Let me look."),
					models.ReasoningSummary("scan files"),
					models.ToolCallContent("call_1", "now", json.RawMessage(`{"b":2,"a":1}`)),
				)
				m.Content = append(m.Content, img)
				m.ProviderState = &models.ProviderState{
					Provider: "openai",
					Data:     json.RawMessage(`{"cursor":"abc","n":1}`),
				}
				return m
			}(),
			models.NewUserMessage(
				models.ToolResultContent("call_1", "now", false,
					models.Text("2026-08-07T12:00:00Z"),
					models.ImageURI("https://example.com/thumb.png"),
				),
			),
			models.NewAssistantMessage(models.Refusal("I cannot do that.")),
		},
	}

	data, err := marshalSession(s)
	if err != nil {
		t.Fatalf("marshalSession: %v", err)
	}
	got, err := unmarshalSession(data)
	if err != nil {
		t.Fatalf("unmarshalSession: %v", err)
	}
	if !sessionEqual(s, got) {
		t.Fatalf("round-trip mismatch\nwant: %+v\ngot:  %+v", s, got)
	}

	// Tool arguments with different key order must still compare equal.
	altArgs := json.RawMessage(`{"a":1,"b":2}`)
	s2 := s.Clone()
	s2.Messages[1].Content[2].ToolCall.Arguments = altArgs
	data2, err := marshalSession(s2)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := unmarshalSession(data2)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionEqual(s, got2) {
		t.Fatal("tool arguments key order should not affect semantic equality after round-trip")
	}
}

func TestCodecParentIDEmptyAndNilMetadata(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	s := &Session{
		ID: "a", CWD: "/w", CreatedAt: now, UpdatedAt: now,
		Messages: []models.Message{models.NewUserMessage(models.Text("hi"))},
	}
	data, err := marshalSession(s)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["parent_id"] != "" {
		t.Fatalf("parent_id = %v, want empty string", raw["parent_id"])
	}
	if _, ok := raw["metadata"]; ok {
		t.Fatal("nil metadata should be omitted")
	}
	got, err := unmarshalSession(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata != nil {
		t.Fatalf("decoded metadata = %v, want nil", got.Metadata)
	}
}

func TestCodecRejectsInvalidToolArguments(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	// Bypass Validate by building via marshal path after constructing invalid
	// content that fails encodeContent's IsObject check.
	s := &Session{
		ID: "a", CWD: "/w", CreatedAt: now, UpdatedAt: now,
		Messages: []models.Message{
			models.NewAssistantMessage(
				models.ToolCallContent("c1", "f", json.RawMessage(`[]`)),
			),
		},
	}
	// Session.Validate will reject first; marshalSession calls Validate.
	if _, err := marshalSession(s); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("marshal invalid args = %v, want ErrInvalidSession", err)
	}

	// Decode path: craft JSON with array arguments.
	payload := []byte(`{
		"schema_version":1,
		"id":"a","parent_id":"","cwd":"/w",
		"created_at":"2026-08-07T12:00:00Z","updated_at":"2026-08-07T12:00:00Z",
		"messages":[{"role":"assistant","content":[
			{"type":"tool_call","id":"c1","name":"f","arguments":[]}
		]}]
	}`)
	if _, err := unmarshalSession(payload); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unmarshal invalid args = %v, want ErrInvalidSession", err)
	}
}

func TestCodecRobustness(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"bad json", `{`},
		{"missing schema", `{"id":"a","cwd":"/w","created_at":"2026-08-07T12:00:00Z","updated_at":"2026-08-07T12:00:00Z","messages":[]}`},
		{"wrong schema", `{"schema_version":99,"id":"a","parent_id":"","cwd":"/w","created_at":"2026-08-07T12:00:00Z","updated_at":"2026-08-07T12:00:00Z","messages":[]}`},
		{"unknown content", `{"schema_version":1,"id":"a","parent_id":"","cwd":"/w","created_at":"2026-08-07T12:00:00Z","updated_at":"2026-08-07T12:00:00Z","messages":[{"role":"user","content":[{"type":"audio"}]}]}`},
		{"empty id", `{"schema_version":1,"id":"","parent_id":"","cwd":"/w","created_at":"2026-08-07T12:00:00Z","updated_at":"2026-08-07T12:00:00Z","messages":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := unmarshalSession([]byte(tc.data))
			if !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("err = %v, want ErrInvalidSession", err)
			}
		})
	}
}

// sessionEqual reports semantic equality for tests. Nil and empty Metadata
// both compare as empty. Message payloads use Message.Equal (JSON-value
// comparison for tool arguments and provider state).
func sessionEqual(a, b *Session) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != b.ID || a.ParentID != b.ParentID || a.CWD != b.CWD {
		return false
	}
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	if len(a.Messages) != len(b.Messages) {
		return false
	}
	for i := range a.Messages {
		if !a.Messages[i].Equal(b.Messages[i]) {
			return false
		}
	}
	return metadataEqual(a.Metadata, b.Metadata)
}

func metadataEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
