package responses

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

func TestFunctionCallArgumentsDoneCompletesToolCall(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r","model":"m","status":"in_progress"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"now","arguments":"","status":"in_progress"}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"item-1","output_index":0,"delta":"{\"zone\":"}`,
		`data: {"type":"response.function_call_arguments.done","item_id":"item-1","output_index":0,"name":"now","arguments":"{\"zone\":\"UTC\"}"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"now","arguments":"{\"zone\":\"UTC\"}","status":"completed"}}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"call-1","name":"now","arguments":"{\"zone\":\"UTC\"}","status":"completed"}]}}`,
		"",
	}, "\n\n")
	source := NewSSEStreamSource(sse.NewReader(io.NopCloser(strings.NewReader(fixture)), 4096), "", "m", false, 4096)
	stream := models.NewStream(context.Background(), source, models.WithStreamProvider("openai"))
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	response := stream.Response()
	if response == nil {
		t.Fatal("response is nil")
	}
	want := []models.ToolCall{{ID: "call-1", Name: "now", Arguments: json.RawMessage(`{"zone":"UTC"}`)}}
	if got := response.ToolCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolCalls() = %#v, want %#v", got, want)
	}
}

func TestContentPartDoneCompletesText(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r","model":"m","status":"in_progress"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg-1","role":"assistant","content":[],"status":"in_progress"}}`,
		`data: {"type":"response.content_part.added","item_id":"msg-1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`data: {"type":"response.output_text.delta","item_id":"msg-1","output_index":0,"content_index":0,"delta":"Hello"}`,
		`data: {"type":"response.output_text.done","item_id":"msg-1","output_index":0,"content_index":0,"text":"Hello"}`,
		`data: {"type":"response.content_part.done","item_id":"msg-1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"Hello"}],"status":"completed"}}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"Hello"}],"status":"completed"}]}}`,
		"",
	}, "\n\n")
	source := NewSSEStreamSource(sse.NewReader(io.NopCloser(strings.NewReader(fixture)), 4096), "", "m", false, 4096)
	stream := models.NewStream(context.Background(), source, models.WithStreamProvider("openai"))
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if response := stream.Response(); response == nil || response.Text() != "Hello" {
		t.Fatalf("response = %#v, want text %q", response, "Hello")
	}
}

func TestReasoningTextDeltaStreamsAsReasoningSummary(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r","model":"m","status":"in_progress"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"reason-1","status":"in_progress"}}`,
		`data: {"type":"response.content_part.added","item_id":"reason-1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`data: {"type":"response.reasoning_text.delta","item_id":"reason-1","output_index":0,"content_index":0,"delta":"think"}`,
		`data: {"type":"response.reasoning_text.done","item_id":"reason-1","output_index":0,"content_index":0,"text":"think"}`,
		`data: {"type":"response.content_part.done","item_id":"reason-1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":"think"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"reason-1","status":"completed","content":[{"type":"reasoning_text","text":"think"}]}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg-1","role":"assistant","content":[],"status":"in_progress"}}`,
		`data: {"type":"response.content_part.added","item_id":"msg-1","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`data: {"type":"response.output_text.delta","item_id":"msg-1","output_index":1,"content_index":0,"delta":"answer"}`,
		`data: {"type":"response.output_text.done","item_id":"msg-1","output_index":1,"content_index":0,"text":"answer"}`,
		`data: {"type":"response.content_part.done","item_id":"msg-1","output_index":1,"content_index":0,"part":{"type":"output_text","text":"answer"}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"answer"}],"status":"completed"}}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed","output":[{"type":"reasoning","id":"reason-1","status":"completed","content":[{"type":"reasoning_text","text":"think"}]},{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"answer"}],"status":"completed"}]}}`,
		"",
	}, "\n\n")
	source := NewSSEStreamSource(sse.NewReader(io.NopCloser(strings.NewReader(fixture)), 4096), "", "m", false, 4096)
	stream := models.NewStream(context.Background(), source, models.WithStreamProvider("openai"))
	var sawReasoningDelta bool
	for stream.Next() {
		ev := stream.Event()
		if ev.Kind == models.EventPartDelta && ev.Delta.Kind == models.DeltaReasoningSummary && ev.Delta.Text == "think" {
			sawReasoningDelta = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if !sawReasoningDelta {
		t.Fatal("expected reasoning_text delta as DeltaReasoningSummary")
	}
	response := stream.Response()
	if response == nil {
		t.Fatal("response is nil")
	}
	if got := response.Text(); got != "thinkanswer" {
		t.Fatalf("Text() = %q, want %q", got, "thinkanswer")
	}
	if len(response.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(response.Candidates))
	}
	content := response.Candidates[0].Content
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2: %#v", len(content), content)
	}
	if content[0].Kind != models.ContentReasoningSummary || content[0].ReasoningSummary.Text != "think" {
		t.Fatalf("content[0] = %#v, want reasoning_summary %q", content[0], "think")
	}
	if content[1].Kind != models.ContentText || content[1].Text.Text != "answer" {
		t.Fatalf("content[1] = %#v, want text %q", content[1], "answer")
	}
}
