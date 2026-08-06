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

func TestCloneAliasesIsolated(t *testing.T) {
	t.Parallel()
	img := models.ImageBytes("image/png", []byte{1, 2, 3})
	call := models.ToolCallContent("c1", "lookup", json.RawMessage(`{"q":"x"}`))
	result := models.ToolResultContent("c1", "lookup", false, models.Text("ok"), models.ImageBytes("image/png", []byte{9}))
	msg := models.Message{
		Role:          models.RoleAssistant,
		Content:       []models.Content{models.Text("hi"), img, call, result, models.ReasoningSummary("r"), models.Refusal("no")},
		ProviderState: &models.ProviderState{Provider: "p", Data: json.RawMessage(`{"k":1}`)},
	}
	clonedMsg := msg.Clone()
	msg.Content[0].Text.Text = "mutated"
	msg.Content[1].Image.Data[0] = 99
	msg.Content[2].ToolCall.Arguments[0] = 'X'
	msg.Content[3].ToolResult.Content[0].Text.Text = "mutated"
	msg.ProviderState.Data[0] = 'X'
	if clonedMsg.Content[0].Text.Text != "hi" || clonedMsg.Content[1].Image.Data[0] != 1 {
		t.Fatal("Message.Clone shares content storage")
	}
	if string(clonedMsg.Content[2].ToolCall.Arguments) != `{"q":"x"}` {
		t.Fatal("Message.Clone shares tool call arguments")
	}
	if clonedMsg.Content[3].ToolResult.Content[0].Text.Text != "ok" {
		t.Fatal("Message.Clone shares tool result content")
	}
	if string(clonedMsg.ProviderState.Data) != `{"k":1}` {
		t.Fatal("Message.Clone shares provider state")
	}

	freshImg := models.ImageBytes("image/png", []byte{1, 2, 3})
	contentClone := freshImg.Clone()
	freshImg.Image.Data[0] = 7
	if contentClone.Image.Data[0] != 1 {
		t.Fatal("Content.Clone shares image bytes")
	}

	event := models.Event{
		Kind:  models.EventPartDelta,
		Part:  models.Text("p"),
		Delta: models.Delta{Kind: models.DeltaMediaBytes, Media: []byte{4, 5}},
		Response: &models.ResponseInfo{
			Provider:      "p",
			Metadata:      map[string]string{"a": "b"},
			ProviderState: &models.ProviderState{Provider: "p", Data: json.RawMessage(`1`)},
		},
		Finishes: []models.CandidateFinish{{
			CandidateIndex: 0,
			ProviderState:  &models.ProviderState{Provider: "p", Data: json.RawMessage(`2`)},
		}},
		Usage: &models.Usage{TotalTokens: models.Some(int64(3)), Raw: json.RawMessage(`{}`)},
	}
	eventClone := event.Clone()
	event.Delta.Media[0] = 0
	event.Response.Metadata["a"] = "z"
	event.Response.ProviderState.Data[0] = '9'
	event.Finishes[0].ProviderState.Data[0] = '9'
	event.Usage.Raw[0] = '9'
	if eventClone.Delta.Media[0] != 4 || eventClone.Response.Metadata["a"] != "b" {
		t.Fatal("Event.Clone shares nested storage")
	}
	if string(eventClone.Response.ProviderState.Data) != `1` || string(eventClone.Finishes[0].ProviderState.Data) != `2` {
		t.Fatal("Event.Clone shares provider state")
	}

	req := &models.Request{
		Instructions:   []models.Instruction{{Role: models.InstructionSystem, Text: "sys"}},
		Messages:       []models.Message{models.NewUserMessage(models.Text("u"), models.ImageBytes("image/png", []byte{8}))},
		Tools:          []models.Tool{models.NewTool("t", "d", json.RawMessage(`{"type":"object"}`))},
		ToolChoice:     models.SpecificTool("t"),
		ResponseFormat: models.JSONSchemaFormat("n", "d", json.RawMessage(`{"type":"object"}`)),
		Generation: models.GenerationConfig{
			Stop:            []string{"END"},
			MaxOutputTokens: models.Some(10),
		},
		RequestID:  "rid",
		Metadata:   map[string]string{"m": "1"},
		Extensions: map[string]json.RawMessage{"vendor.x": json.RawMessage(`{"v":1}`)},
	}
	reqClone := req.Clone()
	req.Instructions[0].Text = "mut"
	req.Messages[0].Content[0].Text.Text = "mut"
	req.Messages[0].Content[1].Image.Data[0] = 0
	req.Tools[0].Parameters[0] = 'X'
	req.Generation.Stop[0] = "X"
	req.Metadata["m"] = "X"
	req.Extensions["vendor.x"][0] = 'X'
	req.ResponseFormat.Schema[0] = 'X'
	if reqClone.Instructions[0].Text != "sys" || reqClone.Messages[0].Content[0].Text.Text != "u" {
		t.Fatal("Request.Clone shares instructions/messages")
	}
	if reqClone.Messages[0].Content[1].Image.Data[0] != 8 {
		t.Fatal("Request.Clone shares image bytes")
	}
	if string(reqClone.Tools[0].Parameters) != `{"type":"object"}` {
		t.Fatal("Request.Clone shares tool schema")
	}
	if reqClone.Generation.Stop[0] != "END" || reqClone.Metadata["m"] != "1" {
		t.Fatal("Request.Clone shares generation/metadata")
	}
	if string(reqClone.Extensions["vendor.x"]) != `{"v":1}` || string(reqClone.ResponseFormat.Schema) != `{"type":"object"}` {
		t.Fatal("Request.Clone shares extensions/schema")
	}
	if req.Clone() == nil {
		t.Fatal("nil-safe Clone should return nil only for nil receiver")
	}
	var nilReq *models.Request
	if nilReq.Clone() != nil {
		t.Fatal("nil Request.Clone should return nil")
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

func TestMessageEqual(t *testing.T) {
	t.Parallel()
	base := models.Message{
		Role: models.RoleAssistant,
		Content: []models.Content{
			models.Text("hi"),
			models.ToolCallContent("c1", "lookup", json.RawMessage(`{"a":1,"b":2}`)),
		},
		ProviderState: &models.ProviderState{Provider: "p", Data: json.RawMessage(`{"x":1}`)},
	}
	if !base.Equal(base.Clone()) {
		t.Fatal("message must equal its clone")
	}
	// JSON semantics ignore whitespace and object-key order.
	reordered := base.Clone()
	reordered.Content[1].ToolCall.Arguments = json.RawMessage("{\"b\":2,  \"a\":1}")
	reordered.ProviderState.Data = json.RawMessage(" { \"x\": 1 } ")
	if !base.Equal(reordered) {
		t.Fatal("Equal must treat JSON values semantically")
	}
	cases := []struct {
		name string
		got  models.Message
	}{
		{"role", models.Message{Role: models.RoleUser, Content: base.Content}},
		{"content length", models.Message{Role: models.RoleAssistant, Content: base.Content[:1], ProviderState: base.ProviderState}},
		{"text", func() models.Message { m := base.Clone(); m.Content[0].Text.Text = "bye"; return m }()},
		{"tool call", func() models.Message {
			m := base.Clone()
			m.Content[1].ToolCall.Arguments = json.RawMessage(`{"a":2,"b":2}`)
			return m
		}()},
		{"provider", func() models.Message { m := base.Clone(); m.ProviderState.Provider = "q"; return m }()},
		{"provider data", func() models.Message { m := base.Clone(); m.ProviderState.Data = json.RawMessage(`{"x":2}`); return m }()},
		{"provider nil", models.Message{Role: models.RoleAssistant, Content: base.Content}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if base.Equal(tt.got) {
				t.Fatalf("messages must differ on %s", tt.name)
			}
		})
	}
}
