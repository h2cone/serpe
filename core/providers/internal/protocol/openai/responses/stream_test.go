package responses

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
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
