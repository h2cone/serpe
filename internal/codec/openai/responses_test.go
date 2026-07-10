package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tw8ap/ouro/internal/canon"
)

func TestResponsesEncodeRequestMergesEncryptedReasoningInclude(t *testing.T) {
	req := &canon.Request{
		Model: "gpt-5.4",
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role: canon.RoleUser, Content: []canon.ContentBlock{&canon.TextBlock{Text: "hello"}},
		}}},
		Extra: map[string]any{
			"openai.responses.include": json.RawMessage(`["message.output_text.logprobs"]`),
		},
	}
	data, err := (ResponsesCodec{}).EncodeRequest(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var body struct {
		Store   bool     `json:"store"`
		Include []string `json:"include"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode encoded body: %v", err)
	}
	if body.Store {
		t.Fatal("store = true, want false")
	}
	want := []string{"message.output_text.logprobs", "reasoning.encrypted_content"}
	if len(body.Include) != len(want) {
		t.Fatalf("include = %#v, want %#v", body.Include, want)
	}
	for i := range want {
		if body.Include[i] != want[i] {
			t.Fatalf("include = %#v, want %#v", body.Include, want)
		}
	}
}

func TestResponsesEncodeRequestRejectsStopSequences(t *testing.T) {
	_, err := (ResponsesCodec{}).EncodeRequest(&canon.Request{
		Model: "gpt-5.4",
		Stop:  []string{"END"},
	})
	if err == nil || !strings.Contains(err.Error(), "stop sequences are not supported") {
		t.Fatalf("error = %v, want unsupported stop error", err)
	}
}

func TestResponsesDecodeResponsePreservesRefusal(t *testing.T) {
	resp, err := (ResponsesCodec{}).DecodeResponse([]byte(`{
		"id":"resp_refusal","model":"gpt-5.4","status":"completed",
		"output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I cannot help with that."}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(resp.Content))
	}
	text, ok := resp.Content[0].(*canon.TextBlock)
	if !ok || text.Text != "I cannot help with that." {
		t.Fatalf("refusal block = %#v", resp.Content[0])
	}
}

func TestResponsesDecodeStreamPreservesRefusal(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_refusal\",\"model\":\"gpt-5.4\"}}\n\n",
		"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"refusal\",\"refusal\":\"\"}}\n\n",
		"event: response.refusal.delta\ndata: {\"type\":\"response.refusal.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"I cannot help\"}\n\n",
		"event: response.refusal.done\ndata: {\"type\":\"response.refusal.done\",\"output_index\":0,\"content_index\":0,\"refusal\":\"I cannot help\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_refusal\",\"model\":\"gpt-5.4\",\"status\":\"completed\"}}\n\n",
	}, "")
	events, err := (ResponsesCodec{}).DecodeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := canon.Assemble(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(resp.Content))
	}
	text, ok := resp.Content[0].(*canon.TextBlock)
	if !ok || text.Text != "I cannot help" {
		t.Fatalf("refusal block = %#v", resp.Content[0])
	}
}

func TestResponsesDecodeStreamPreservesErrorMessages(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		data   string
		wanted string
	}{
		{
			name:   "failed response",
			event:  "response.failed",
			data:   `{"type":"response.failed","response":{"status":"failed","error":{"message":"model generation failed"}}}`,
			wanted: "model generation failed",
		},
		{
			name:   "error event",
			event:  "response.error",
			data:   `{"type":"response.error","error":{"message":"stream transport failed"}}`,
			wanted: "stream transport failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stream := "event: " + tc.event + "\ndata: " + tc.data + "\n\n"
			events, err := (ResponsesCodec{}).DecodeStream(strings.NewReader(stream))
			if err != nil {
				t.Fatal(err)
			}
			_, err = canon.Assemble(events)
			if err == nil || !strings.Contains(err.Error(), tc.wanted) {
				t.Fatalf("error = %v, want substring %q", err, tc.wanted)
			}
		})
	}
}

func TestResponsesDecodeStreamPreservesCompletedReasoningItem(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\"}}\n\n",
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"in_progress\",\"summary\":[]}}\n\n",
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"delta\":\"thinking\"}\n\n",
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"thinking\"}],\"encrypted_content\":\"ciphertext\"}}\n\n",
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"path\\\":\\\"README.md\\\"}\"}\n\n",
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
	}, "")
	events, err := (ResponsesCodec{}).DecodeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	resp, err := canon.Assemble(events)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("content len = %d, want 2", len(resp.Content))
	}
	thinking, ok := resp.Content[0].(*canon.ThinkingBlock)
	if !ok || thinking.Text != "thinking" {
		t.Fatalf("thinking block = %#v", resp.Content[0])
	}
	item, ok := thinking.Extra["openai.responses.reasoning_item"].(map[string]any)
	if !ok || item["encrypted_content"] != "ciphertext" || item["status"] != "completed" {
		t.Fatalf("reasoning sidecar = %#v", thinking.Extra)
	}
	tool, ok := resp.Content[1].(*canon.ToolUseBlock)
	if !ok || tool.ID != "call_1" || string(tool.Input) != `{"path":"README.md"}` {
		t.Fatalf("tool block = %#v", resp.Content[1])
	}
}
