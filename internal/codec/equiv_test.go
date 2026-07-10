package codec

import (
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/tw8ap/ouro/internal/canon"
	"github.com/tw8ap/ouro/internal/codec/anthropic"
	"github.com/tw8ap/ouro/internal/codec/openai"
)

func allCodecs() []struct {
	name  string
	codec codecCodec
} {
	return []struct {
		name  string
		codec codecCodec
	}{
		{"openai-responses", openai.ResponsesCodec{}},
		{"openai-chat", openai.ChatCodec{}},
		{"anthropic-messages", anthropic.MessagesCodec{}},
	}
}

// codecCodec mirrors the codec.Codec methods without an import cycle (codec
// defines Codec; the concrete types implement it structurally).
type codecCodec interface {
	EncodeRequest(*canon.Request) ([]byte, error)
	DecodeRequest([]byte) (*canon.Request, error)
	EncodeResponse(*canon.Response) ([]byte, error)
	DecodeResponse([]byte) (*canon.Response, error)
	DecodeStream(r io.Reader) (<-chan canon.Event, error)
	EncodeStream(w io.Writer, events <-chan canon.Event) error
}

func TestCrossProtocolRequestRoundTrip(t *testing.T) {
	req := &canon.Request{
		Model:     "test-model",
		MaxTokens: 4096,
		Conversation: canon.Conversation{
			System: "You are a helpful assistant. Be concise.",
			Messages: []canon.Message{
				{Role: canon.RoleUser, Content: []canon.ContentBlock{
					&canon.TextBlock{Text: "create a file"},
				}},
				{Role: canon.RoleAssistant, Content: []canon.ContentBlock{
					&canon.ToolUseBlock{ID: "call_1", Name: "write_file",
						Input: json.RawMessage(`{"path":"artifact.txt","content":"hi"}`)},
				}},
				{Role: canon.RoleUser, Content: []canon.ContentBlock{
					&canon.ToolResultBlock{ToolUseID: "call_1",
						Content: []canon.ContentBlock{&canon.TextBlock{Text: "Wrote 2 bytes to artifact.txt"}}},
				}},
			},
		},
		Tools: []canon.Tool{{
			Name:        "write_file",
			Description: "Write UTF-8 text to disk.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
		}},
	}

	for _, c := range allCodecs() {
		t.Run(c.name, func(t *testing.T) {
			wire, err := c.codec.EncodeRequest(req)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			back, err := c.codec.DecodeRequest(wire)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !requestsEqual(req, back) {
				t.Fatalf("%s request round-trip mismatch:\nwant %#v\ngot  %#v\nwire %s",
					c.name, req, back, wire)
			}
		})
	}
}

func TestCrossProtocolResponseRoundTrip(t *testing.T) {
	resp := &canon.Response{
		ID:    "resp_1",
		Model: "test-model",
		Content: []canon.ContentBlock{
			&canon.ToolUseBlock{ID: "call_1", Name: "write_file",
				Input: json.RawMessage(`{"path":"artifact.txt","content":"hi"}`)},
		},
		FinishReason: canon.FinishToolCalls,
		Usage:        canon.Usage{InputTokens: 100, OutputTokens: 20},
	}

	for _, c := range allCodecs() {
		t.Run(c.name, func(t *testing.T) {
			wire, err := c.codec.EncodeResponse(resp)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			back, err := c.codec.DecodeResponse(wire)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !responsesEqual(resp, back) {
				t.Fatalf("%s response round-trip mismatch:\nwant %#v\ngot  %#v\nwire %s",
					c.name, resp, back, wire)
			}
		})
	}
}

// TestCrossProtocolTextResponseRoundTrip covers a plain-text stop response, the
// other common shape (the tool-call case is covered above).
func TestCrossProtocolTextResponseRoundTrip(t *testing.T) {
	resp := &canon.Response{
		ID:    "resp_2",
		Model: "test-model",
		Content: []canon.ContentBlock{
			&canon.TextBlock{Text: "hello from model"},
		},
		FinishReason: canon.FinishStop,
		Usage:        canon.Usage{InputTokens: 50, OutputTokens: 8},
	}
	for _, c := range allCodecs() {
		t.Run(c.name, func(t *testing.T) {
			wire, err := c.codec.EncodeResponse(resp)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			back, err := c.codec.DecodeResponse(wire)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !responsesEqual(resp, back) {
				t.Fatalf("%s text response round-trip mismatch:\nwant %#v\ngot  %#v",
					c.name, resp, back)
			}
		})
	}
}

func requestsEqual(a, b *canon.Request) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Model != b.Model || a.MaxTokens != b.MaxTokens || a.Stream != b.Stream {
		return false
	}
	if !reflect.DeepEqual(a.Stop, b.Stop) {
		return false
	}
	if a.Conversation.System != b.Conversation.System {
		return false
	}
	if len(a.Conversation.Messages) != len(b.Conversation.Messages) {
		return false
	}
	for i := range a.Conversation.Messages {
		if a.Conversation.Messages[i].Role != b.Conversation.Messages[i].Role {
			return false
		}
		if !blocksEqual(a.Conversation.Messages[i].Content, b.Conversation.Messages[i].Content) {
			return false
		}
	}
	if len(a.Tools) != len(b.Tools) {
		return false
	}
	for i := range a.Tools {
		if a.Tools[i].Name != b.Tools[i].Name || a.Tools[i].Description != b.Tools[i].Description {
			return false
		}
		if !rawEqual(a.Tools[i].Parameters, b.Tools[i].Parameters) {
			return false
		}
	}
	return a.ToolChoice == b.ToolChoice
}

func responsesEqual(a, b *canon.Response) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != b.ID || a.Model != b.Model || a.FinishReason != b.FinishReason {
		return false
	}
	if a.Usage != b.Usage {
		return false
	}
	return blocksEqual(a.Content, b.Content)
}

func blocksEqual(a, b []canon.ContentBlock) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !blockEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func blockEqual(a, b canon.ContentBlock) bool {
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	switch x := a.(type) {
	case *canon.TextBlock:
		return x.Text == b.(*canon.TextBlock).Text
	case *canon.ImageBlock:
		y := b.(*canon.ImageBlock)
		return x.MediaType == y.MediaType && x.URL == y.URL && x.Data == y.Data
	case *canon.ToolUseBlock:
		y := b.(*canon.ToolUseBlock)
		return x.ID == y.ID && x.Name == y.Name && rawEqual(x.Input, y.Input)
	case *canon.ToolResultBlock:
		y := b.(*canon.ToolResultBlock)
		return x.ToolUseID == y.ToolUseID && x.IsError == y.IsError && blocksEqual(x.Content, y.Content)
	case *canon.ThinkingBlock:
		y := b.(*canon.ThinkingBlock)
		return x.Text == y.Text && x.Signature == y.Signature
	}
	return false
}

func rawEqual(a, b json.RawMessage) bool {
	if len(a) == 0 {
		a = json.RawMessage("{}")
	}
	if len(b) == 0 {
		b = json.RawMessage("{}")
	}
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}
