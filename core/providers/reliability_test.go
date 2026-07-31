package providers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers"
)

func TestHTTPErrorNormalizationAndRedaction(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Retry-After", "2")
		writer.Header().Set("X-Request-ID", "request-429")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, `{"error":{"message":"key test-secret was rejected","code":"rate_limit"}}`)
	}))
	defer server.Close()
	model := boundTestModel(t, providers.OpenAIResponses, server, "model-1", providers.Config{APIKey: "test-secret"})
	_, err := model.Complete(context.Background(), models.NewTextRequest("hello"))
	var modelErr *models.Error
	if !errors.As(err, &modelErr) {
		t.Fatalf("error = %#v", err)
	}
	if modelErr.Kind != models.ErrorRateLimited || !modelErr.Retryable || modelErr.HTTPStatus != 429 || modelErr.RetryAfter != 2*time.Second || modelErr.RequestID != "request-429" {
		t.Fatalf("normalized error = %#v", modelErr)
	}
	if strings.Contains(modelErr.Error(), "test-secret") || !strings.Contains(modelErr.Error(), "[REDACTED]") {
		t.Fatalf("secret was not redacted: %v", modelErr)
	}
}

func TestResponseAndSSELimits(t *testing.T) {
	t.Parallel()
	t.Run("unary", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"id":"`+strings.Repeat("x", 256)+`"}`)
		}))
		defer server.Close()
		model := boundTestModel(t, providers.OpenAIResponses, server, "model-1", providers.Config{Limits: providers.Limits{MaxResponseBytes: 32}})
		_, err := model.Complete(context.Background(), models.NewTextRequest("hello"))
		var modelErr *models.Error
		if !errors.As(err, &modelErr) || modelErr.Kind != models.ErrorProtocol || modelErr.Code != "response_too_large" {
			t.Fatalf("error = %#v", err)
		}
	})
	t.Run("sse", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: "+strings.Repeat("x", 128)+"\n\n")
		}))
		defer server.Close()
		model := boundTestModel(t, providers.OpenAIChatCompletions, server, "model-1", providers.Config{Limits: providers.Limits{MaxSSEEventBytes: 32}})
		stream, err := model.Stream(context.Background(), models.NewTextRequest("hello"))
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		for stream.Next() {
		}
		var modelErr *models.Error
		if !errors.As(stream.Err(), &modelErr) || modelErr.Kind != models.ErrorProtocol || modelErr.Code != "sse_read_error" {
			t.Fatalf("stream error = %#v", stream.Err())
		}
	})
}

func TestProviderStreamsRejectUnexpectedEOF(t *testing.T) {
	t.Parallel()
	tests := []struct {
		protocol providers.Protocol
		body     string
	}{
		{protocol: providers.OpenAIChatCompletions, body: `data: {"id":"r","model":"m","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n"},
		{protocol: providers.GeminiGenerateContent, body: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}` + "\n\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.protocol), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			modelID := "model-1"
			if test.protocol == providers.GeminiGenerateContent {
				modelID = "gemini-2.0-flash"
			}
			model := boundTestModel(t, test.protocol, server, modelID, providers.Config{})
			stream, err := model.Stream(context.Background(), models.NewTextRequest("hello"))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			var modelErr *models.Error
			if !errors.As(stream.Err(), &modelErr) || modelErr.Kind != models.ErrorProtocol || modelErr.Code != "unexpected_eof" {
				t.Fatalf("stream error = %#v", stream.Err())
			}
		})
	}
}

func TestStreamProviderErrorClassification(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, `data: {"type":"error","error":{"type":"overloaded_error","message":"busy"}}`+"\n\n")
	}))
	defer server.Close()
	model := boundTestModel(t, providers.AnthropicMessages, server, "model-1", providers.Config{})
	request := models.NewTextRequest("hello")
	request.Generation.MaxOutputTokens = models.Some(16)
	stream, err := model.Stream(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for stream.Next() {
	}
	var modelErr *models.Error
	if !errors.As(stream.Err(), &modelErr) || modelErr.Kind != models.ErrorUnavailable || !modelErr.Retryable {
		t.Fatalf("stream error = %#v", stream.Err())
	}
}

func TestUnknownStreamEventPolicy(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"r1","model":"model-1","status":"in_progress"}}`,
		`data: {"type":"response.future_optional_event","value":1}`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"model-1","status":"completed","output":[]}}`, "",
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()
	for _, test := range []struct {
		name    string
		policy  providers.UnknownEventPolicy
		wantErr bool
	}{
		{name: "strict", policy: providers.UnknownEventError, wantErr: true},
		{name: "ignore", policy: providers.UnknownEventIgnore},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := boundTestModel(t, providers.OpenAIResponses, server, "model-1", providers.Config{Policy: providers.Policy{UnknownEvent: test.policy}})
			stream, err := model.Stream(context.Background(), models.NewTextRequest("hello"))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			if (stream.Err() != nil) != test.wantErr {
				t.Fatalf("stream error = %v, wantErr %v", stream.Err(), test.wantErr)
			}
		})
	}
}

func TestAnthropicUnknownContentPolicyWithKnownContent(t *testing.T) {
	t.Parallel()
	unaryBody := `{"id":"r1","model":"model-1","role":"assistant","content":[{"type":"future_block","value":"ignored"},{"type":"text","text":"answer"}],"stop_reason":"end_turn"}`
	streamBody := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"r1","model":"model-1","role":"assistant","content":[]}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"future_block","value":"ignored"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"future_delta","value":"ignored"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`data: {"type":"message_stop"}`, "",
	}, "\n\n")
	for _, test := range []struct {
		name    string
		policy  providers.UnknownEventPolicy
		wantErr bool
	}{
		{name: "strict", policy: providers.UnknownEventError, wantErr: true},
		{name: "ignore", policy: providers.UnknownEventIgnore},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				if strings.Contains(string(body), `"stream":true`) {
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, streamBody)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, unaryBody)
			}))
			defer server.Close()

			model := boundTestModel(t, providers.AnthropicMessages, server, "model-1", providers.Config{Policy: providers.Policy{UnknownEvent: test.policy}})
			request := models.NewTextRequest("hello")
			request.Generation.MaxOutputTokens = models.Some(32)
			unary, err := model.Complete(context.Background(), request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			stream, err := model.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			if test.wantErr {
				var modelErr *models.Error
				if !errors.As(stream.Err(), &modelErr) || modelErr.Kind != models.ErrorProtocol || modelErr.Code != "unknown_content" {
					t.Fatalf("stream Err = %#v", stream.Err())
				}
				return
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("stream Err: %v", err)
			}
			if streamed := stream.Response(); !reflect.DeepEqual(unary, streamed) {
				t.Fatalf("unary/stream mismatch\nunary=%#v\nstream=%#v", unary, streamed)
			}
			if unary.Text() != "answer" {
				t.Fatalf("text = %q", unary.Text())
			}
		})
	}
}

func TestEmptyResponseUnaryStreamParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		protocol       providers.Protocol
		modelID        string
		unary          string
		stream         string
		wantCandidates int
	}{
		{
			name:           "openai_chat",
			protocol:       providers.OpenAIChatCompletions,
			modelID:        "model-1",
			unary:          `{"id":"r","model":"model-1","choices":[]}`,
			stream:         "data: {\"id\":\"r\",\"model\":\"model-1\",\"choices\":[]}\n\ndata: [DONE]\n\n",
			wantCandidates: 0,
		},
		{
			name:     "openai_responses",
			protocol: providers.OpenAIResponses,
			modelID:  "model-1",
			unary:    `{"id":"r","model":"model-1","status":"completed","output":[]}`,
			stream: strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"r","model":"model-1","status":"in_progress","output":[]}}`,
				`data: {"type":"response.completed","response":{"id":"r","model":"model-1","status":"completed","output":[]}}`, "",
			}, "\n\n"),
			wantCandidates: 0,
		},
		{
			name:     "anthropic",
			protocol: providers.AnthropicMessages,
			modelID:  "model-1",
			unary:    `{"id":"r","model":"model-1","role":"assistant","content":[],"stop_reason":"end_turn"}`,
			stream: strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"r","model":"model-1","role":"assistant","content":[]}}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
				`data: {"type":"message_stop"}`, "",
			}, "\n\n"),
			wantCandidates: 1,
		},
		{
			name:           "gemini",
			protocol:       providers.GeminiGenerateContent,
			modelID:        "gemini-2.0-flash",
			unary:          `{"candidates":[],"modelVersion":"gemini-2.0-flash","responseId":"r"}`,
			stream:         "data: {\"candidates\":[],\"modelVersion\":\"gemini-2.0-flash\",\"responseId\":\"r\"}\n\n",
			wantCandidates: 0,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				streaming := strings.Contains(request.URL.Path, ":streamGenerateContent")
				if !streaming {
					var envelope struct {
						Stream bool `json:"stream"`
					}
					_ = json.Unmarshal(body, &envelope)
					streaming = envelope.Stream
				}
				if streaming {
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, test.stream)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.unary)
			}))
			defer server.Close()

			model := boundTestModel(t, test.protocol, server, test.modelID, providers.Config{})
			request := models.NewTextRequest("hello")
			request.Generation.MaxOutputTokens = models.Some(32)
			unary, err := model.Complete(context.Background(), request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			stream, err := model.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("stream Err: %v", err)
			}
			streamed := stream.Response()
			if !reflect.DeepEqual(unary, streamed) {
				t.Fatalf("unary/stream mismatch\nunary=%#v\nstream=%#v", unary, streamed)
			}
			if len(unary.Candidates) != test.wantCandidates {
				t.Fatalf("candidate count = %d, want %d", len(unary.Candidates), test.wantCandidates)
			}
		})
	}
}

func TestMultipleCandidateUnaryStreamParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		protocol providers.Protocol
		modelID  string
		unary    string
		stream   string
	}{
		{
			name:     "openai_chat",
			protocol: providers.OpenAIChatCompletions,
			modelID:  "model-1",
			unary:    `{"id":"r","created":1,"model":"model-1","choices":[{"index":0,"message":{"content":"first"},"finish_reason":"stop"},{"index":1,"message":{"content":"second"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`,
			stream: strings.Join([]string{
				`data: {"id":"r","created":1,"model":"model-1","choices":[{"index":0,"delta":{"content":"fir"},"finish_reason":null},{"index":1,"delta":{"content":"sec"},"finish_reason":null}]}`,
				`data: {"id":"r","model":"model-1","choices":[{"index":1,"delta":{"content":"ond"},"finish_reason":"stop"},{"index":0,"delta":{"content":"st"},"finish_reason":"stop"}]}`,
				`data: {"id":"r","model":"model-1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`,
				`data: [DONE]`, "",
			}, "\n\n"),
		},
		{
			name:     "gemini",
			protocol: providers.GeminiGenerateContent,
			modelID:  "gemini-2.0-flash",
			unary:    `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"first"}]},"finishReason":"STOP"},{"index":1,"content":{"role":"model","parts":[{"text":"second"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2,"totalTokenCount":4},"modelVersion":"gemini-2.0-flash","responseId":"r"}`,
			stream: strings.Join([]string{
				`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"fir"}]}},{"index":1,"content":{"role":"model","parts":[{"text":"sec"}]}}],"modelVersion":"gemini-2.0-flash","responseId":"r"}`,
				`data: {"candidates":[{"index":1,"content":{"role":"model","parts":[{"text":"ond"}]},"finishReason":"STOP"},{"index":0,"content":{"role":"model","parts":[{"text":"st"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2,"totalTokenCount":4}}`, "",
			}, "\n\n"),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				streaming := strings.Contains(request.URL.Path, ":streamGenerateContent") || strings.Contains(string(body), `"stream":true`)
				if streaming {
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, test.stream)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.unary)
			}))
			defer server.Close()

			model := boundTestModel(t, test.protocol, server, test.modelID, providers.Config{})
			request := models.NewTextRequest("two answers")
			request.Generation.CandidateCount = models.Some(2)
			unary, err := model.Complete(context.Background(), request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			stream, err := model.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("stream Err: %v", err)
			}
			if streamed := stream.Response(); !reflect.DeepEqual(unary, streamed) {
				t.Fatalf("unary/stream mismatch\nunary=%#v\nstream=%#v", unary, streamed)
			}
			if len(unary.Candidates) != 2 || unary.Candidates[0].Index != 0 || unary.Candidates[1].Index != 1 {
				t.Fatalf("candidates = %#v", unary.Candidates)
			}
		})
	}
}

func TestParallelToolUnaryStreamParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		protocol providers.Protocol
		unary    string
		stream   string
	}{
		{
			name:     "openai_responses",
			protocol: providers.OpenAIResponses,
			unary:    `{"id":"r","model":"model-1","status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"call-1","name":"first","arguments":"{\"x\":1}","status":"completed"},{"type":"function_call","id":"item-2","call_id":"call-2","name":"second","arguments":"{\"y\":2}","status":"completed"}]}`,
			stream: strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"r","model":"model-1","status":"in_progress","output":[]}}`,
				`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"first","arguments":"","status":"in_progress"}}`,
				`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"item-2","call_id":"call-2","name":"second","arguments":"","status":"in_progress"}}`,
				`data: {"type":"response.function_call_arguments.delta","item_id":"item-1","output_index":0,"delta":"{\"x\":"}`,
				`data: {"type":"response.function_call_arguments.delta","item_id":"item-2","output_index":1,"delta":"{\"y\":"}`,
				`data: {"type":"response.function_call_arguments.delta","item_id":"item-2","output_index":1,"delta":"2}"}`,
				`data: {"type":"response.function_call_arguments.delta","item_id":"item-1","output_index":0,"delta":"1}"}`,
				`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"item-2","call_id":"call-2","name":"second","arguments":"{\"y\":2}","status":"completed"}}`,
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"first","arguments":"{\"x\":1}","status":"completed"}}`,
				`data: {"type":"response.completed","response":{"id":"r","model":"model-1","status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"call-1","name":"first","arguments":"{\"x\":1}","status":"completed"},{"type":"function_call","id":"item-2","call_id":"call-2","name":"second","arguments":"{\"y\":2}","status":"completed"}]}}`, "",
			}, "\n\n"),
		},
		{
			name:     "anthropic",
			protocol: providers.AnthropicMessages,
			unary:    `{"id":"r","model":"model-1","role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"first","input":{"x":1}},{"type":"tool_use","id":"call-2","name":"second","input":{"y":2}}],"stop_reason":"tool_use"}`,
			stream: strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"r","model":"model-1","role":"assistant","content":[]}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"first","input":{}}}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-2","name":"second","input":{}}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"y\":"}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"2}"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				`data: {"type":"message_stop"}`, "",
			}, "\n\n"),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				if strings.Contains(string(body), `"stream":true`) {
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, test.stream)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.unary)
			}))
			defer server.Close()

			model := boundTestModel(t, test.protocol, server, "model-1", providers.Config{})
			request := models.NewTextRequest("use both tools")
			request.Generation.MaxOutputTokens = models.Some(32)
			request.Tools = []models.Tool{
				models.NewTool("first", "", json.RawMessage(`{"type":"object"}`)),
				models.NewTool("second", "", json.RawMessage(`{"type":"object"}`)),
			}
			request.ToolChoice = models.ToolChoice{Kind: models.ToolChoiceAuto}
			unary, err := model.Complete(context.Background(), request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			stream, err := model.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("stream Err: %v", err)
			}
			if streamed := stream.Response(); !reflect.DeepEqual(unary, streamed) {
				t.Fatalf("unary/stream mismatch\nunary=%#v\nstream=%#v", unary, streamed)
			}
			calls := unary.ToolCalls()
			if len(calls) != 2 || calls[0].Name != "first" || calls[1].Name != "second" {
				t.Fatalf("tool calls = %#v", calls)
			}
		})
	}
}

func TestStreamContextCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	model := boundTestModel(t, providers.OpenAIChatCompletions, server, "model-1", providers.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := model.Stream(ctx, models.NewTextRequest("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	<-started
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream.Next()
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not unblock Next")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Fatalf("stream error = %v", stream.Err())
	}
}

func TestProviderStateRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		protocol providers.Protocol
		modelID  string
		first    string
		marker   string
	}{
		{
			protocol: providers.OpenAIResponses, modelID: "model-1", marker: `"type":"reasoning"`,
			first: `{"id":"r1","model":"model-1","status":"completed","output":[{"type":"reasoning","id":"reason-1","summary":[{"type":"summary_text","text":"short summary"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`,
		},
		{
			protocol: providers.AnthropicMessages, modelID: "model-1", marker: `"signature":"signed-state"`,
			first: `{"id":"r1","model":"model-1","role":"assistant","content":[{"type":"thinking","thinking":"short summary","signature":"signed-state"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		},
		{
			protocol: providers.GeminiGenerateContent, modelID: "gemini-2.0-flash", marker: `"thoughtSignature":"signed-state"`,
			first: `{"candidates":[{"content":{"role":"model","parts":[{"text":"short summary","thought":true,"thoughtSignature":"signed-state"},{"text":"answer"}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.0-flash","responseId":"r1"}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.protocol), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				call := calls.Add(1)
				body, _ := io.ReadAll(request.Body)
				writer.Header().Set("Content-Type", "application/json")
				if call == 1 {
					_, _ = io.WriteString(writer, test.first)
					return
				}
				if !strings.Contains(string(body), test.marker) {
					t.Errorf("continuation request did not preserve provider state; body=%s", body)
				}
				_, _ = io.WriteString(writer, unaryFixture(test.protocol, false))
			}))
			defer server.Close()
			model := boundTestModel(t, test.protocol, server, test.modelID, providers.Config{})
			firstReq := models.NewTextRequest("first")
			firstReq.Generation.MaxOutputTokens = models.Some(32)
			response, err := model.Complete(context.Background(), firstReq)
			if err != nil {
				t.Fatalf("first Complete: %v", err)
			}
			assistant, err := response.AssistantMessage(0)
			if err != nil || assistant.ProviderState == nil {
				t.Fatalf("AssistantMessage = %#v, %v", assistant, err)
			}
			secondReq := &models.Request{Messages: []models.Message{models.NewUserMessage(models.Text("first")), assistant, models.NewUserMessage(models.Text("next"))}}
			secondReq.Generation.MaxOutputTokens = models.Some(32)
			if _, err := model.Complete(context.Background(), secondReq); err != nil {
				t.Fatalf("second Complete: %v", err)
			}
		})
	}
}

func TestAnthropicRedactedOnlyStateRoundTripsWithoutPlaceholder(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(writer, `{"id":"r1","model":"model-1","role":"assistant","content":[{"type":"redacted_thinking","data":"encrypted-secret"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		if !strings.Contains(string(body), `"type":"redacted_thinking","data":"encrypted-secret"`) {
			t.Errorf("continuation request lost redacted state: %s", body)
		}
		_, _ = io.WriteString(writer, unaryFixture(providers.AnthropicMessages, false))
	}))
	defer server.Close()

	model := boundTestModel(t, providers.AnthropicMessages, server, "model-1", providers.Config{})
	first := models.NewTextRequest("first")
	first.Generation.MaxOutputTokens = models.Some(32)
	response, err := model.Complete(context.Background(), first)
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	assistant, err := response.AssistantMessage(0)
	if err != nil {
		t.Fatalf("AssistantMessage: %v", err)
	}
	if len(assistant.Content) != 0 || assistant.ProviderState == nil {
		t.Fatalf("assistant = %#v", assistant)
	}
	if err := assistant.Validate(); err != nil {
		t.Fatalf("assistant Validate: %v", err)
	}
	second := &models.Request{Messages: []models.Message{models.NewUserMessage(models.Text("first")), assistant, models.NewUserMessage(models.Text("next"))}}
	second.Generation.MaxOutputTokens = models.Some(32)
	if _, err := model.Complete(context.Background(), second); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
}

func TestAnthropicUsageIncludesCachedInputCategories(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"r1","model":"model-1","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"cache_creation_input_tokens":3,"cache_read_input_tokens":5,"output_tokens":7}}`)
	}))
	defer server.Close()

	model := boundTestModel(t, providers.AnthropicMessages, server, "model-1", providers.Config{})
	request := models.NewTextRequest("hello")
	request.Generation.MaxOutputTokens = models.Some(32)
	response, err := model.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !response.Usage.InputTokens.Set || response.Usage.InputTokens.Value != 10 {
		t.Fatalf("InputTokens = %#v", response.Usage.InputTokens)
	}
	if !response.Usage.CachedInputTokens.Set || response.Usage.CachedInputTokens.Value != 5 {
		t.Fatalf("CachedInputTokens = %#v", response.Usage.CachedInputTokens)
	}
	if !response.Usage.TotalTokens.Set || response.Usage.TotalTokens.Value != 17 {
		t.Fatalf("TotalTokens = %#v", response.Usage.TotalTokens)
	}
}

func TestReasoningProviderStateUnaryStreamParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		protocol providers.Protocol
		modelID  string
		unary    string
		stream   string
	}{
		{
			protocol: providers.OpenAIResponses,
			modelID:  "model-1",
			unary:    `{"id":"r1","model":"model-1","status":"completed","output":[{"type":"reasoning","id":"reason-1","summary":[{"type":"summary_text","text":"short summary"}]},{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`,
			stream: strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"r1","model":"model-1","status":"in_progress","output":[]}}`,
				`data: {"type":"response.reasoning_summary_part.added","item_id":"reason-1","output_index":0,"content_index":0,"part":{"type":"summary_text","text":""}}`,
				`data: {"type":"response.reasoning_summary_text.delta","item_id":"reason-1","output_index":0,"content_index":0,"delta":"short summary"}`,
				`data: {"type":"response.reasoning_summary_part.done","item_id":"reason-1","output_index":0,"content_index":0}`,
				`data: {"type":"response.content_part.added","item_id":"msg-1","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}`,
				`data: {"type":"response.output_text.delta","item_id":"msg-1","output_index":1,"content_index":0,"delta":"answer"}`,
				`data: {"type":"response.output_text.done","item_id":"msg-1","output_index":1,"content_index":0}`,
				`data: {"type":"response.completed","response":{"id":"r1","model":"model-1","status":"completed","output":[{"type":"reasoning","id":"reason-1","summary":[{"type":"summary_text","text":"short summary"}]},{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`, "",
			}, "\n\n"),
		},
		{
			protocol: providers.AnthropicMessages,
			modelID:  "model-1",
			unary:    `{"id":"r1","model":"model-1","role":"assistant","content":[{"type":"thinking","thinking":"short summary","signature":"signed-state"},{"type":"text","text":"answer"}],"stop_reason":"end_turn"}`,
			stream: strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"r1","model":"model-1","content":[],"stop_reason":null}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"short summary"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-state"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
				`data: {"type":"message_stop"}`, "",
			}, "\n\n"),
		},
		{
			protocol: providers.GeminiGenerateContent,
			modelID:  "gemini-2.0-flash",
			unary:    `{"candidates":[{"content":{"role":"model","parts":[{"text":"short summary","thought":true,"thoughtSignature":"signed-state"},{"text":"answer"}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.0-flash","responseId":"r1"}`,
			stream:   `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"short summary","thought":true,"thoughtSignature":"signed-state"},{"text":"answer"}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.0-flash","responseId":"r1"}` + "\n\n",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.protocol), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				isStream := strings.Contains(request.URL.Path, ":streamGenerateContent") || strings.Contains(string(body), `"stream":true`)
				if isStream {
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, test.stream)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.unary)
			}))
			defer server.Close()
			model := boundTestModel(t, test.protocol, server, test.modelID, providers.Config{})
			request := models.NewTextRequest("reason")
			request.Generation.MaxOutputTokens = models.Some(32)
			unary, err := model.Complete(context.Background(), request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			stream, err := model.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			if stream.Err() != nil {
				t.Fatalf("stream error: %v", stream.Err())
			}
			if !reflect.DeepEqual(unary, stream.Response()) {
				unaryJSON, _ := json.Marshal(unary)
				streamJSON, _ := json.Marshal(stream.Response())
				t.Fatalf("reasoning parity mismatch\nunary=%s\nstream=%s", unaryJSON, streamJSON)
			}
		})
	}
}

func TestGeminiStreamUsesLogicalPartIndexesAcrossChunks(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, strings.Join([]string{
			`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Before "}]}}],"modelVersion":"gemini-2.0-flash","responseId":"r1"}`,
			`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"tool"}]}}]}`,
			`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2,"totalTokenCount":4}}`,
			"",
		}, "\n\n"))
	}))
	defer server.Close()

	model := boundTestModel(t, providers.GeminiGenerateContent, server, "gemini-2.0-flash", providers.Config{})
	stream, err := model.Stream(context.Background(), models.NewTextRequest("use a tool"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	var starts []int
	for stream.Next() {
		if event := stream.Event(); event.Kind == models.EventPartStart {
			starts = append(starts, event.PartIndex)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if !reflect.DeepEqual(starts, []int{0, 1}) {
		t.Fatalf("part starts = %v, want [0 1]", starts)
	}
	response := stream.Response()
	if response == nil || response.Text() != "Before tool" {
		t.Fatalf("response = %#v", response)
	}
	wantCalls := []models.ToolCall{{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"x":1}`)}}
	if got := response.ToolCalls(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("ToolCalls() = %#v, want %#v", got, wantCalls)
	}
}

func TestResponsesStreamRecoversTerminalOnlyTextAndRefusal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"r1","model":"model-1","status":"in_progress"}}`,
			`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg-1","part":{"type":"output_text","text":""}}`,
			`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg-1","text":"recovered"}`,
			`data: {"type":"response.content_part.added","output_index":0,"content_index":1,"item_id":"msg-1","part":{"type":"refusal","refusal":""}}`,
			`data: {"type":"response.refusal.done","output_index":0,"content_index":1,"item_id":"msg-1","refusal":"cannot"}`,
			`data: {"type":"response.completed","response":{"id":"r1","model":"model-1","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"recovered"},{"type":"refusal","refusal":"cannot"}]}]}}`,
			"",
		}, "\n\n"))
	}))
	defer server.Close()

	model := boundTestModel(t, providers.OpenAIResponses, server, "model-1", providers.Config{})
	stream, err := model.Stream(context.Background(), models.NewTextRequest("hello"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if response := stream.Response(); response == nil || response.Text() != "recoveredcannot" {
		t.Fatalf("response = %#v", response)
	}
}

func TestResponsesStreamRejectsTerminalContentMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		output   string
		wantCode string
	}{
		{
			name:     "different content",
			output:   `[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"different"}]}]`,
			wantCode: "terminal_content_mismatch",
		},
		{
			name:     "missing candidate",
			output:   `[]`,
			wantCode: "terminal_candidate_mismatch",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, strings.Join([]string{
					`data: {"type":"response.created","response":{"id":"r1","model":"model-1","status":"in_progress"}}`,
					`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg-1","part":{"type":"output_text","text":""}}`,
					`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg-1","delta":"streamed"}`,
					`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg-1"}`,
					`data: {"type":"response.completed","response":{"id":"r1","model":"model-1","status":"completed","output":` + test.output + `}}`,
					"",
				}, "\n\n"))
			}))
			defer server.Close()

			model := boundTestModel(t, providers.OpenAIResponses, server, "model-1", providers.Config{})
			stream, err := model.Stream(context.Background(), models.NewTextRequest("hello"))
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			var modelErr *models.Error
			if !errors.As(stream.Err(), &modelErr) || modelErr.Kind != models.ErrorProtocol || modelErr.Code != test.wantCode {
				t.Fatalf("stream error = %#v, want protocol code %q", stream.Err(), test.wantCode)
			}
			if response := stream.Response(); response != nil {
				t.Fatalf("partial response = %#v, want nil after terminal mismatch", response)
			}
		})
	}
}

func TestImageStructuredOutputAndToolResultEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		protocol       providers.Protocol
		modelID        string
		imageMarker    string
		formatMarker   string
		toolResultMark string
	}{
		{providers.OpenAIChatCompletions, "model-1", `"image_url"`, `"json_schema"`, `"role":"tool"`},
		{providers.OpenAIResponses, "model-1", `"input_image"`, `"json_schema"`, `"function_call_output"`},
		{providers.AnthropicMessages, "model-1", `"type":"url"`, `"output_config"`, `"tool_result"`},
		{providers.GeminiGenerateContent, "gemini-2.0-flash", `"fileData"`, `"responseSchema"`, `"functionResponse":{"id":"call-1","name":"lookup"`},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.protocol), func(t *testing.T) {
			var call atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				current := call.Add(1)
				markers := []string{test.imageMarker, test.formatMarker}
				if current == 2 {
					markers = []string{test.toolResultMark}
				}
				for _, marker := range markers {
					if !strings.Contains(string(body), marker) {
						t.Errorf("request missing %s: %s", marker, body)
					}
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, unaryFixture(test.protocol, false))
			}))
			defer server.Close()
			model := boundTestModel(t, test.protocol, server, test.modelID, providers.Config{})
			imageReq := &models.Request{
				Messages:       []models.Message{models.NewUserMessage(models.Text("describe"), models.ImageURI("https://example.test/image.png"))},
				ResponseFormat: models.JSONSchemaFormat("answer", "", json.RawMessage(`{"type":"object"}`)),
			}
			imageReq.Generation.MaxOutputTokens = models.Some(32)
			if _, err := model.Complete(context.Background(), imageReq); err != nil {
				t.Fatalf("image/format Complete: %v", err)
			}
			conversation := &models.Request{Messages: []models.Message{
				models.NewUserMessage(models.Text("use tool")),
				models.NewAssistantMessage(models.ToolCallContent("call-1", "lookup", json.RawMessage(`{"x":1}`))),
				models.NewUserMessage(models.ToolResultContent("call-1", "lookup", false, models.Text("done"))),
			}}
			conversation.Generation.MaxOutputTokens = models.Some(32)
			if _, err := model.Complete(context.Background(), conversation); err != nil {
				t.Fatalf("tool-result Complete: %v", err)
			}
		})
	}
}

func TestStrictAndLenientInstructionMapping(t *testing.T) {
	t.Parallel()
	for _, protocol := range []providers.Protocol{providers.AnthropicMessages, providers.GeminiGenerateContent} {
		protocol := protocol
		t.Run(string(protocol), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(request.Body)
				if !strings.Contains(string(body), "[developer]") {
					t.Errorf("lenient request lacks developer boundary: %s", body)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, unaryFixture(protocol, false))
			}))
			defer server.Close()
			request := models.NewTextRequest("hello")
			request.Instructions = []models.Instruction{{Role: models.InstructionDeveloper, Text: "policy"}}
			request.Generation.MaxOutputTokens = models.Some(32)
			strict := boundTestModel(t, protocol, server, modelIDFor(protocol), providers.Config{})
			_, err := strict.Complete(context.Background(), request)
			var modelErr *models.Error
			if !errors.As(err, &modelErr) || modelErr.Kind != models.ErrorUnsupportedFeature {
				t.Fatalf("strict mapping error = %#v", err)
			}
			if calls.Load() != 0 {
				t.Fatal("strict validation sent a network request")
			}
			lenient := boundTestModel(t, protocol, server, modelIDFor(protocol), providers.Config{Policy: providers.Policy{Mapping: providers.MappingLenient}})
			if _, err := lenient.Complete(context.Background(), request); err != nil {
				t.Fatalf("lenient Complete: %v", err)
			}
		})
	}
}

func TestNamespacedExtensionCannotOverrideCanonicalFields(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"service_tier":"priority"`) {
			t.Errorf("extension not encoded: %s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, unaryFixture(providers.OpenAIResponses, false))
	}))
	defer server.Close()
	model := boundTestModel(t, providers.OpenAIResponses, server, "model-1", providers.Config{})
	request := models.NewTextRequest("hello")
	request.Extensions = map[string]json.RawMessage{"openai.responses": json.RawMessage(`{"service_tier":"priority"}`)}
	if _, err := model.Complete(context.Background(), request); err != nil {
		t.Fatalf("extension Complete: %v", err)
	}
	request.Extensions["openai.responses"] = json.RawMessage(`{"model":"override"}`)
	_, err := model.Complete(context.Background(), request)
	var modelErr *models.Error
	if !errors.As(err, &modelErr) || modelErr.Kind != models.ErrorInvalidRequest || modelErr.Code != "extension_override" {
		t.Fatalf("override error = %#v", err)
	}
}

func TestEmptyToolArgumentsUnaryStreamParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		protocol providers.Protocol
		unary    string
		stream   string
	}{
		{
			name:     "openai_chat",
			protocol: providers.OpenAIChatCompletions,
			unary:    `{"id":"r","model":"model-1","choices":[{"index":0,"message":{"content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup"}}]},"finish_reason":"tool_calls"}]}`,
			stream: strings.Join([]string{
				`data: {"id":"r","model":"model-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":""}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`, "",
			}, "\n\n"),
		},
		{
			name:     "anthropic",
			protocol: providers.AnthropicMessages,
			unary:    `{"id":"r","model":"model-1","role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"lookup","input":null}],"stop_reason":"tool_use"}`,
			stream: strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"r","model":"model-1","role":"assistant","content":[]}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":null}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				`data: {"type":"message_stop"}`, "",
			}, "\n\n"),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				var envelope struct {
					Stream bool `json:"stream"`
				}
				_ = json.Unmarshal(body, &envelope)
				if envelope.Stream {
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, test.stream)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.unary)
			}))
			defer server.Close()

			model := boundTestModel(t, test.protocol, server, "model-1", providers.Config{})
			request := models.NewTextRequest("hello")
			request.Generation.MaxOutputTokens = models.Some(32)
			unary, err := model.Complete(context.Background(), request)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			stream, err := model.Stream(context.Background(), request)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for stream.Next() {
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("stream Err: %v", err)
			}
			streamed := stream.Response()
			if !reflect.DeepEqual(unary, streamed) {
				t.Fatalf("unary/stream mismatch\nunary=%#v\nstream=%#v", unary, streamed)
			}
			calls := unary.ToolCalls()
			if len(calls) != 1 || string(calls[0].Arguments) != `{}` {
				t.Fatalf("tool calls = %#v", calls)
			}
		})
	}
}

func TestGeminiPromptBlockUnaryStreamParity(t *testing.T) {
	t.Parallel()
	body := `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"blocked detail"},"modelVersion":"gemini-2.0-flash","responseId":"r"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, ":streamGenerateContent") {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: "+body+"\n\n")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()

	model := boundTestModel(t, providers.GeminiGenerateContent, server, "gemini-2.0-flash", providers.Config{})
	request := models.NewTextRequest("hello")
	unary, err := model.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stream, err := model.Stream(context.Background(), request)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream Err: %v", err)
	}
	streamed := stream.Response()
	if !reflect.DeepEqual(unary, streamed) {
		t.Fatalf("unary/stream mismatch\nunary=%#v\nstream=%#v", unary, streamed)
	}
	if unary.Metadata["prompt_block_reason"] != "SAFETY" || unary.Metadata["prompt_block_message"] != "blocked detail" {
		t.Fatalf("metadata = %#v", unary.Metadata)
	}
}

func TestProviderStateRoundTripUsesSemanticToolArguments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		protocol        providers.Protocol
		modelID         string
		response        string
		reformatContent bool
	}{
		{
			name:     "anthropic_raw_whitespace",
			protocol: providers.AnthropicMessages,
			modelID:  "model-1",
			response: `{"id":"r","model":"model-1","role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":"sig"},{"type":"tool_use","id":"call-1","name":"lookup","input": { "x" : 1, "y": 2 }}],"stop_reason":"tool_use"}`,
		},
		{
			name:     "gemini_raw_whitespace",
			protocol: providers.GeminiGenerateContent,
			modelID:  "gemini-2.0-flash",
			response: `{"candidates":[{"content":{"role":"model","parts":[{"text":"summary","thought":true,"thoughtSignature":"sig"},{"functionCall":{"id":"call-1","name":"lookup","args": { "x" : 1, "y": 2 }}}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.0-flash","responseId":"r"}`,
		},
		{
			name:            "responses_reordered_keys",
			protocol:        providers.OpenAIResponses,
			modelID:         "model-1",
			response:        `{"id":"r","model":"model-1","status":"completed","output":[{"type":"reasoning","id":"reason-1","summary":[{"type":"summary_text","text":"summary"}]},{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"x\":1,\"y\":2}"}]}`,
			reformatContent: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.response)
			}))
			defer server.Close()

			model := boundTestModel(t, test.protocol, server, test.modelID, providers.Config{})
			first := models.NewTextRequest("first")
			first.Generation.MaxOutputTokens = models.Some(32)
			response, err := model.Complete(context.Background(), first)
			if err != nil {
				t.Fatalf("first Complete: %v", err)
			}
			assistant, err := response.AssistantMessage(0)
			if err != nil {
				t.Fatalf("AssistantMessage: %v", err)
			}
			if test.reformatContent {
				for index := range assistant.Content {
					if assistant.Content[index].Kind == models.ContentToolCall {
						assistant.Content[index].ToolCall.Arguments = json.RawMessage(` { "y": 2, "x": 1 } `)
					}
				}
			}
			second := &models.Request{Messages: []models.Message{
				models.NewUserMessage(models.Text("first")),
				assistant,
				models.NewUserMessage(models.Text("next")),
			}}
			second.Generation.MaxOutputTokens = models.Some(32)
			if _, err := model.Complete(context.Background(), second); err != nil {
				t.Fatalf("continuation Complete: %v", err)
			}
			if calls.Load() != 2 {
				t.Fatalf("HTTP calls = %d, want 2", calls.Load())
			}
		})
	}
}

func TestBoundModelConcurrentReuse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, unaryFixture(providers.OpenAIResponses, false))
	}))
	defer server.Close()
	model := boundTestModel(t, providers.OpenAIResponses, server, "model-1", providers.Config{})
	request := models.NewTextRequest("hello")
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := model.Complete(context.Background(), request)
			if err != nil || response.Text() != "Hello" {
				t.Errorf("Complete = %#v, %v", response, err)
			}
		}()
	}
	wait.Wait()
}

func boundTestModel(t *testing.T, protocol providers.Protocol, server *httptest.Server, modelID string, overrides providers.Config) models.Model {
	t.Helper()
	overrides.Protocol = protocol
	overrides.BaseURL = server.URL
	if overrides.HTTPClient == nil {
		overrides.HTTPClient = server.Client()
	}
	provider, err := providers.New(overrides)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	model, err := provider.Model(modelID)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	return model
}

func modelIDFor(protocol providers.Protocol) string {
	if protocol == providers.GeminiGenerateContent {
		return "gemini-2.0-flash"
	}
	return "model-1"
}
