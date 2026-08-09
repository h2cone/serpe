package server

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/sessions"
)

// TestSSEFrameContractFixture binds the cross-language fixture to the actual
// Go wire structs. The UI decoder consumes the same fixture, so a Go wire
// change cannot pass both suites until the browser boundary accepts it.
func TestSSEFrameContractFixture(t *testing.T) {
	one := int64(1)
	two := int64(2)
	three := int64(3)
	four := int64(4)
	five := int64(5)
	call := models.ContentRecord{
		Type:      string(models.ContentToolCall),
		ID:        "c1",
		Name:      "now",
		Arguments: json.RawMessage(`{}`),
	}
	frames := []any{
		frameRunStart{T: "run_start"},
		frameModelStart{T: "model_start", Turn: 1},
		framePartStart{T: "part_start", Turn: 1, Part: 0, Kind: "tool_call", CallID: "c1", Name: "now"},
		frameDelta{T: "delta", Turn: 1, Part: 0, Kind: "tool_arguments", Text: `{}`, CallID: "c1"},
		framePartEnd{T: "part_end", Turn: 1, Part: 0, CallID: "c1"},
		frameToolStart{T: "tool_start", Turn: 1, Idx: 0, Call: call},
		frameToolEnd{
			T: "tool_end", Turn: 1, Idx: 0, Call: call,
			Result: toolResultDTO{
				Content: []models.ContentRecord{{Type: string(models.ContentText), Text: "12:00"}},
				IsError: true,
			},
		},
		frameModelEnd{
			T: "model_end", Turn: 1, Finish: "stop",
			Usage: &usageDTO{
				InputTokens: &one, OutputTokens: &two, TotalTokens: &three,
				CachedInputTokens: &four, ReasoningTokens: &five,
			},
		},
		frameRunEnd{T: "run_end", Stop: "completed"},
		frameError{T: "error", Message: "commit failed", Stop: "failed"},
		frameDone{T: "done", SessionID: "s1", Stop: "completed", MessageCount: 2},
	}

	wantJSON, err := os.ReadFile("testdata/sse_frames.json")
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(frames)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatalf("decode contract fixture: %v", err)
	}
	if err := json.Unmarshal(gotJSON, &got); err != nil {
		t.Fatalf("decode Go frames: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go SSE frames differ from server/testdata/sse_frames.json\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestSessionDetailContractFixture(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	detail, err := toDetail(&sessions.Session{
		ID: "s1", ParentID: "parent", CWD: "/work",
		CreatedAt: now, UpdatedAt: now,
		Metadata: map[string]string{metaKeyTitle: "Contract session"},
		Messages: []models.Message{
			models.NewUserMessage(
				models.Text("hello"),
				models.ImageURI("https://example.com/image.png"),
			),
			models.NewAssistantMessage(
				models.ReasoningSummary("summary"),
				models.Refusal("cannot"),
				models.ToolCallContent("c1", "now", json.RawMessage(`{"zone":"UTC"}`)),
			),
			models.NewUserMessage(
				models.ToolResultContent("c1", "now", false, models.Text("12:00")),
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := os.ReadFile("testdata/session_detail.json")
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotJSON, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go session detail differs from contract fixture\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
