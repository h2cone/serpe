package canon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBlockRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		block ContentBlock
	}{
		{"text", &TextBlock{Text: "hello"}},
		{"text with extra", &TextBlock{Text: "hi", Extra: map[string]any{"cache_control": map[string]any{"type": "ephemeral"}}}},
		{"image url", &ImageBlock{MediaType: "image/png", URL: "https://example.com/a.png"}},
		{"image data", &ImageBlock{MediaType: "image/jpeg", Data: "BASE64=="}},
		{"tool_use", &ToolUseBlock{ID: "call_1", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"hi"}`)}},
		{"tool_use empty input", &ToolUseBlock{ID: "call_2", Name: "ping"}},
		{"tool_result", &ToolResultBlock{ToolUseID: "call_1", Content: []ContentBlock{&TextBlock{Text: "Wrote 2 bytes"}}}},
		{"tool_result error", &ToolResultBlock{ToolUseID: "call_1", IsError: true, Content: []ContentBlock{&TextBlock{Text: "boom"}}}},
		{"thinking", &ThinkingBlock{Text: "reasoning here", Signature: "sig"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.block)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := unmarshalBlock(data)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !equalBlocks(got, tc.block) {
				t.Fatalf("round-trip mismatch:\nwant %#v\ngot  %#v\nwire %s", tc.block, got, data)
			}
		})
	}
}

func TestBlocksRoundTripAsSlice(t *testing.T) {
	in := []ContentBlock{
		&TextBlock{Text: "first"},
		&ToolUseBlock{ID: "call_1", Name: "write_file", Input: json.RawMessage(`{"path":"a"}`)},
		&ToolResultBlock{ToolUseID: "call_1", Content: []ContentBlock{&TextBlock{Text: "ok"}}},
		&ThinkingBlock{Text: "hmm", Signature: "s"},
		&ImageBlock{MediaType: "image/png", Data: "QUE="},
	}
	raw, err := MarshalBlocks(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := UnmarshalBlocks(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if !equalBlocks(out[i], in[i]) {
			t.Fatalf("block %d mismatch:\nwant %#v\ngot  %#v", i, in[i], out[i])
		}
	}
}

func TestUnmarshalBlocksReturnsFormatSafeTypeErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing type", raw: `[{"text":"hello"}]`, want: "missing type"},
		{name: "unknown type", raw: `[{"type":"future_block"}]`, want: "future_block"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UnmarshalBlocks(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatal("UnmarshalBlocks() unexpectedly succeeded")
			}
			if message := err.Error(); !strings.Contains(message, tc.want) {
				t.Fatalf("error = %q, want substring %q", message, tc.want)
			}
		})
	}
}

func TestConversationRoundTrip(t *testing.T) {
	in := &Conversation{
		System: "be concise",
		Messages: []Message{
			{Role: RoleUser, Content: []ContentBlock{&TextBlock{Text: "create a file"}}},
			{Role: RoleAssistant, Content: []ContentBlock{
				&TextBlock{Text: "sure"},
				&ToolUseBlock{ID: "call_1", Name: "write_file", Input: json.RawMessage(`{"path":"a","content":"hi"}`)},
			}},
			{Role: RoleUser, Content: []ContentBlock{
				&ToolResultBlock{ToolUseID: "call_1", Content: []ContentBlock{&TextBlock{Text: "Wrote 2 bytes"}}},
			}},
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Conversation
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.System != in.System || len(out.Messages) != len(in.Messages) {
		t.Fatalf("conversation mismatch: %#v", out)
	}
	for i := range in.Messages {
		if out.Messages[i].Role != in.Messages[i].Role {
			t.Fatalf("msg %d role mismatch", i)
		}
		if len(out.Messages[i].Content) != len(in.Messages[i].Content) {
			t.Fatalf("msg %d content len mismatch", i)
		}
		for j := range in.Messages[i].Content {
			if !equalBlocks(out.Messages[i].Content[j], in.Messages[i].Content[j]) {
				t.Fatalf("msg %d block %d mismatch", i, j)
			}
		}
	}
}

func TestAssembleReconstructsResponse(t *testing.T) {
	events := []Event{
		MessageStartEvent{Response: &Response{ID: "resp_1", Model: "m"}},
		ContentBlockStartEvent{Index: 0, Block: &TextBlock{}},
		ContentBlockDeltaEvent{Index: 0, Delta: Delta{Type: DeltaText, Text: "hel"}},
		ContentBlockDeltaEvent{Index: 0, Delta: Delta{Type: DeltaText, Text: "lo"}},
		ContentBlockStopEvent{Index: 0},
		ContentBlockStartEvent{Index: 1, Block: &ToolUseBlock{ID: "call_1", Name: "write_file"}},
		ContentBlockDeltaEvent{Index: 1, Delta: Delta{Type: DeltaInputJSON, Partial: `{"path":"a"`}},
		ContentBlockDeltaEvent{Index: 1, Delta: Delta{Type: DeltaInputJSON, Partial: `,"content":"hi"}`}},
		ContentBlockStopEvent{Index: 1},
		MessageDeltaEvent{FinishReason: FinishToolCalls, Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
		MessageStopEvent{},
	}
	resp, err := AssembleSlice(events)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if resp.ID != "resp_1" || resp.FinishReason != FinishToolCalls {
		t.Fatalf("response meta mismatch: %+v", resp)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("content len = %d", len(resp.Content))
	}
	tb, ok := resp.Content[0].(*TextBlock)
	if !ok || tb.Text != "hello" {
		t.Fatalf("text block = %#v", resp.Content[0])
	}
	tu, ok := resp.Content[1].(*ToolUseBlock)
	if !ok || tu.ID != "call_1" || tu.Name != "write_file" {
		t.Fatalf("tool use block = %#v", resp.Content[1])
	}
	if string(tu.Input) != `{"path":"a","content":"hi"}` {
		t.Fatalf("tool input = %s", tu.Input)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestAssembleRejectsUnterminatedStream(t *testing.T) {
	events := []Event{
		MessageStartEvent{Response: &Response{ID: "resp_partial", Model: "m"}},
		ContentBlockStartEvent{Index: 0, Block: &TextBlock{}},
		ContentBlockDeltaEvent{Index: 0, Delta: Delta{Type: DeltaText, Text: "partial"}},
		ContentBlockStopEvent{Index: 0},
	}
	_, err := AssembleSlice(events)
	if err == nil || !strings.Contains(err.Error(), "before message_stop") {
		t.Fatalf("error = %v, want unterminated stream error", err)
	}
}

// equalBlocks compares two ContentBlock values semantically, normalizing
// json.RawMessage so key order does not matter.
func equalBlocks(a, b ContentBlock) bool {
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	switch x := a.(type) {
	case *TextBlock:
		y := b.(*TextBlock)
		return x.Text == y.Text && equalExtras(x.Extra, y.Extra)
	case *ImageBlock:
		y := b.(*ImageBlock)
		return x.MediaType == y.MediaType && x.URL == y.URL && x.Data == y.Data && equalExtras(x.Extra, y.Extra)
	case *ToolUseBlock:
		y := b.(*ToolUseBlock)
		return x.ID == y.ID && x.Name == y.Name && rawEqual(x.Input, y.Input) && equalExtras(x.Extra, y.Extra)
	case *ToolResultBlock:
		y := b.(*ToolResultBlock)
		if x.ToolUseID != y.ToolUseID || x.IsError != y.IsError || !equalExtras(x.Extra, y.Extra) {
			return false
		}
		if len(x.Content) != len(y.Content) {
			return false
		}
		for i := range x.Content {
			if !equalBlocks(x.Content[i], y.Content[i]) {
				return false
			}
		}
		return true
	case *ThinkingBlock:
		y := b.(*ThinkingBlock)
		return x.Text == y.Text && x.Signature == y.Signature && equalExtras(x.Extra, y.Extra)
	}
	return false
}

func equalExtras(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !reflect.DeepEqual(v, b[k]) {
			return false
		}
	}
	return true
}

func rawEqual(a, b json.RawMessage) bool {
	// Empty input is canonically equivalent to "{}".
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
