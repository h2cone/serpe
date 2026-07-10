package codec

import (
	"bytes"
	"testing"

	"github.com/tw8ap/ouro/internal/canon"
)

// TestCrossProtocolStreamRoundTrip encodes a canonical event sequence to each
// protocol's SSE, decodes it back, assembles both, and compares. This locks the
// encode/decode inverse relationship for streaming.
func TestCrossProtocolStreamRoundTrip(t *testing.T) {
	events := []canon.Event{
		canon.MessageStartEvent{Response: &canon.Response{ID: "resp_1", Model: "m"}},
		canon.ContentBlockStartEvent{Index: 0, Block: &canon.TextBlock{}},
		canon.ContentBlockDeltaEvent{Index: 0, Delta: canon.Delta{Type: canon.DeltaText, Text: "hello "}},
		canon.ContentBlockDeltaEvent{Index: 0, Delta: canon.Delta{Type: canon.DeltaText, Text: "world"}},
		canon.ContentBlockStopEvent{Index: 0},
		canon.ContentBlockStartEvent{Index: 1, Block: &canon.ToolUseBlock{ID: "call_1", Name: "write_file"}},
		canon.ContentBlockDeltaEvent{Index: 1, Delta: canon.Delta{Type: canon.DeltaInputJSON, Partial: `{"path":"a"`}},
		canon.ContentBlockDeltaEvent{Index: 1, Delta: canon.Delta{Type: canon.DeltaInputJSON, Partial: `,"content":"hi"}`}},
		canon.ContentBlockStopEvent{Index: 1},
		canon.MessageDeltaEvent{FinishReason: canon.FinishToolCalls, Usage: &canon.Usage{InputTokens: 10, OutputTokens: 5}},
		canon.MessageStopEvent{},
	}

	want, err := canon.AssembleSlice(events)
	if err != nil {
		t.Fatalf("direct assemble: %v", err)
	}

	for _, c := range allCodecs() {
		t.Run(c.name, func(t *testing.T) {
			in := make(chan canon.Event, len(events))
			for _, ev := range events {
				in <- ev
			}
			close(in)

			var buf bytes.Buffer
			if err := c.codec.EncodeStream(&buf, in); err != nil {
				t.Fatalf("encode stream: %v", err)
			}

			out, err := c.codec.DecodeStream(&buf)
			if err != nil {
				t.Fatalf("decode stream: %v", err)
			}
			got, err := canon.Assemble(out)
			if err != nil {
				t.Fatalf("assemble decoded: %v", err)
			}

			if got.ID != want.ID || got.Model != want.Model {
				t.Fatalf("meta mismatch: got %+v want %+v", got, want)
			}
			if got.FinishReason != want.FinishReason {
				t.Fatalf("finish mismatch: got %q want %q", got.FinishReason, want.FinishReason)
			}
			if got.Usage != want.Usage {
				t.Fatalf("usage mismatch: got %+v want %+v", got.Usage, want.Usage)
			}
			if !blocksEqual(got.Content, want.Content) {
				t.Fatalf("content mismatch:\ngot  %#v\nwant %#v", got.Content, want.Content)
			}
		})
	}
}
