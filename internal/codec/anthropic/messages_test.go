package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tw8ap/ouro/internal/canon"
)

func TestMessagesCodecPreservesRedactedThinking(t *testing.T) {
	wire := []byte(`{
		"id":"msg_1","model":"claude-test","stop_reason":"tool_use",
		"content":[
			{"type":"redacted_thinking","data":"opaque-ciphertext"},
			{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"README.md"}}
		],
		"usage":{"input_tokens":10,"output_tokens":4}
	}`)
	resp, err := (MessagesCodec{}).DecodeResponse(wire)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("content len = %d, want 2", len(resp.Content))
	}
	encoded, err := (MessagesCodec{}).EncodeRequest(&canon.Request{
		Model:     "claude-test",
		MaxTokens: 256,
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role: canon.RoleAssistant, Content: resp.Content,
		}}},
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("decode encoded request: %v", err)
	}
	redacted := body.Messages[0].Content[0]
	if redacted["type"] != "redacted_thinking" || redacted["data"] != "opaque-ciphertext" {
		t.Fatalf("redacted block = %#v", redacted)
	}
	if _, leaked := redacted[redactedThinkingMarker]; leaked {
		t.Fatalf("private marker leaked to wire: %#v", redacted)
	}

	streamBlock := anthropicDecodeStreamBlock(map[string]any{
		"type": "redacted_thinking", "data": "stream-ciphertext",
	})
	streamWire := anthropicEncodeStreamBlock(streamBlock)
	if streamWire["type"] != "redacted_thinking" || streamWire["data"] != "stream-ciphertext" {
		t.Fatalf("stream redacted block = %#v", streamWire)
	}
}

func TestMessagesDecodeStreamMergesUsage(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":11,\"output_tokens\":1,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":4}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")
	events, err := (MessagesCodec{}).DecodeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	resp, err := canon.Assemble(events)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	want := canon.Usage{InputTokens: 11, OutputTokens: 7, CacheRead: 3, CacheWrite: 4}
	if resp.Usage != want {
		t.Fatalf("usage = %+v, want %+v", resp.Usage, want)
	}
}
