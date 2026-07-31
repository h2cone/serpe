package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/h2cone/ouro/core/models"
)

func TestContentTaggedUnionValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content models.Content
		wantErr bool
	}{
		{name: "text", content: models.Text("hello")},
		{name: "inline image", content: models.ImageBytes("image/png", []byte{1})},
		{name: "tool call", content: models.ToolCallContent("call", "lookup", json.RawMessage(`{"q":"x"}`))},
		{name: "tool result", content: models.ToolResultContent("call", "lookup", false, models.Text("found"))},
		{name: "tool result without name", content: models.Content{Kind: models.ContentToolResult, ToolResult: &models.ToolResult{CallID: "call", Content: []models.Content{models.Text("found")}}}, wantErr: true},
		{name: "no variant", content: models.Content{Kind: models.ContentText}, wantErr: true},
		{name: "two variants", content: models.Content{Kind: models.ContentText, Text: &models.TextContent{}, Refusal: &models.RefusalContent{}}, wantErr: true},
		{name: "bad tool JSON", content: models.ToolCallContent("call", "lookup", json.RawMessage(`{"q":`)), wantErr: true},
		{name: "both image sources", content: models.Content{Kind: models.ContentImage, Image: &models.ImageContent{URI: "https://example.test/a.png", MIMEType: "image/png", Data: []byte{1}}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.content.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	t.Parallel()
	req := models.NewTextRequest("hello")
	req.Tools = []models.Tool{models.NewTool("lookup", "", json.RawMessage(`{"type":"object"}`))}
	req.ToolChoice = models.SpecificTool("lookup")
	req.ResponseFormat = models.JSONSchemaFormat("answer", "", json.RawMessage(`{"type":"object"}`))
	req.Generation.MaxOutputTokens = models.Some(100)
	if err := req.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	copy := *req
	copy.ToolChoice = models.SpecificTool("missing")
	var modelErr *models.Error
	if err := copy.Validate(); !errors.As(err, &modelErr) || modelErr.Kind != models.ErrorInvalidRequest {
		t.Fatalf("invalid tool choice error = %#v", err)
	}
}

func TestProviderStateOnlyAssistantMessageIsValid(t *testing.T) {
	t.Parallel()
	state := &models.ProviderState{Provider: "anthropic.messages", Data: json.RawMessage(`[{"type":"redacted_thinking","data":"encrypted"}]`)}
	message := models.Message{Role: models.RoleAssistant, ProviderState: state}
	if err := message.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	message.Role = models.RoleUser
	if err := message.Validate(); err == nil {
		t.Fatal("provider-state-only user message was accepted")
	}
}

func TestStreamReducesInterleavedParts(t *testing.T) {
	t.Parallel()
	events := []models.Event{
		{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "fake", ID: "r1", Model: "m1"}},
		{Kind: models.EventPartStart, CandidateIndex: 0, PartIndex: 0, Part: models.Text("")},
		{Kind: models.EventPartStart, CandidateIndex: 0, PartIndex: 1, Part: models.ToolCallContent("a", "first", nil)},
		{Kind: models.EventPartStart, CandidateIndex: 0, PartIndex: 2, Part: models.ToolCallContent("b", "second", nil)},
		{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: models.Delta{Kind: models.DeltaText, Text: "hel"}},
		{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 1, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: `{"x":`}},
		{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 2, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: `{"y":`}},
		{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: models.Delta{Kind: models.DeltaText, Text: "lo"}},
		{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 2, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: `2}`}},
		{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 1, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: `1}`}},
		{Kind: models.EventPartEnd, CandidateIndex: 0, PartIndex: 2},
		{Kind: models.EventPartEnd, CandidateIndex: 0, PartIndex: 0},
		{Kind: models.EventPartEnd, CandidateIndex: 0, PartIndex: 1},
		{Kind: models.EventResponseEnd, Response: &models.ResponseInfo{Provider: "fake", Status: models.ResponseStatusCompleted}, Finishes: []models.CandidateFinish{{CandidateIndex: 0, Reason: models.FinishToolCall, RawReason: "tools"}}},
	}
	stream := models.NewStream(context.Background(), &sliceSource{events: events}, models.WithStreamProvider("fake"))
	var text string
	for stream.Next() {
		text += stream.Text()
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if text != "hello" {
		t.Fatalf("Text deltas = %q", text)
	}
	response := stream.Response()
	if response == nil || response.Text() != "hello" {
		t.Fatalf("response = %#v", response)
	}
	wantCalls := []models.ToolCall{{ID: "a", Name: "first", Arguments: json.RawMessage(`{"x":1}`)}, {ID: "b", Name: "second", Arguments: json.RawMessage(`{"y":2}`)}}
	if got := response.ToolCalls(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("ToolCalls() = %#v, want %#v", got, wantCalls)
	}

	retained := stream.Event()
	retained.Finishes[0].RawReason = "mutated"
	if stream.Event().Finishes[0].RawReason != "tools" {
		t.Fatal("Event returned mutable internal storage")
	}
}

func TestStreamRejectsInvalidSequenceAndUnexpectedEOF(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		events []models.Event
		code   string
	}{
		{name: "delta before start", events: []models.Event{{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: models.Delta{Kind: models.DeltaText, Text: "x"}}}, code: "invalid_event_sequence"},
		{name: "invalid final tool JSON", events: []models.Event{
			{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "fake"}},
			{Kind: models.EventPartStart, CandidateIndex: 0, PartIndex: 0, Part: models.ToolCallContent("c", "f", nil)},
			{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: `{"x":`}},
			{Kind: models.EventPartEnd, CandidateIndex: 0, PartIndex: 0},
		}, code: "invalid_event_sequence"},
		{name: "unexpected EOF", events: []models.Event{{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "fake"}}}, code: "unexpected_eof"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := models.NewStream(context.Background(), &sliceSource{events: test.events}, models.WithStreamProvider("fake"))
			for stream.Next() {
			}
			var modelErr *models.Error
			if !errors.As(stream.Err(), &modelErr) || modelErr.Kind != models.ErrorProtocol || modelErr.Code != test.code {
				t.Fatalf("Err() = %#v", stream.Err())
			}
			if stream.Response() != nil {
				t.Fatal("Response() must be nil after an error")
			}
		})
	}
}

func TestStreamPreservesWrappedModelError(t *testing.T) {
	t.Parallel()
	want := &models.Error{Kind: models.ErrorRateLimited, Provider: "fake", Operation: "stream_next", Code: "limited", Retryable: true}
	stream := models.NewStream(context.Background(), &errorSource{err: fmt.Errorf("source wrapper: %w", want)}, models.WithStreamProvider("fake"))
	if stream.Next() {
		t.Fatal("Next returned true for a source error")
	}
	var got *models.Error
	if !errors.As(stream.Err(), &got) || got != want {
		t.Fatalf("Err() = %#v, want preserved wrapped error %#v", stream.Err(), want)
	}
}

func TestStreamCloseUnblocksNext(t *testing.T) {
	t.Parallel()
	source := newBlockingSource()
	stream := models.NewStream(context.Background(), source, models.WithStreamProvider("fake"))
	done := make(chan bool, 1)
	go func() { done <- stream.Next() }()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("Next did not enter source")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	select {
	case next := <-done:
		if next {
			t.Fatal("Next returned true after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Next")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Fatalf("Err() = %v, want context.Canceled", stream.Err())
	}
}

type sliceSource struct {
	events []models.Event
	index  int
}

func (s *sliceSource) Next() (models.Event, error) {
	if s.index >= len(s.events) {
		return models.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (*sliceSource) Close() error { return nil }

type errorSource struct{ err error }

func (s *errorSource) Next() (models.Event, error) { return models.Event{}, s.err }

func (*errorSource) Close() error { return nil }

type blockingSource struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingSource() *blockingSource {
	return &blockingSource{entered: make(chan struct{}), closed: make(chan struct{})}
}

func (s *blockingSource) Next() (models.Event, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.closed
	return models.Event{}, io.ErrClosedPipe
}

func (s *blockingSource) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
