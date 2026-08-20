package providers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers"
)

func TestAdaptersUnaryStreamConformance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		protocol   providers.Protocol
		modelID    string
		unaryPath  string
		streamPath string
	}{
		{name: "openai_chat", protocol: providers.OpenAIChatCompletions, modelID: "model-1", unaryPath: "/v1/chat/completions", streamPath: "/v1/chat/completions"},
		{name: "openai_responses", protocol: providers.OpenAIResponses, modelID: "model-1", unaryPath: "/v1/responses", streamPath: "/v1/responses"},
		{name: "anthropic", protocol: providers.AnthropicMessages, modelID: "model-1", unaryPath: "/v1/messages", streamPath: "/v1/messages"},
		{name: "gemini", protocol: providers.GeminiGenerateContent, modelID: "gemini-2.0-flash", unaryPath: "/v1beta/models/gemini-2.0-flash:generateContent", streamPath: "/v1beta/models/gemini-2.0-flash:streamGenerateContent"},
	}
	drivers := []providers.Driver{providers.DriverDefault, providers.DriverOfficialSDK}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, driver := range drivers {
				driver := driver
				t.Run(string(driver), func(t *testing.T) {
					t.Parallel()
					for _, tool := range []bool{false, true} {
						tool := tool
						name := "text"
						if tool {
							name = "tool"
						}
						t.Run(name, func(t *testing.T) {
							runConformanceCase(t, test.protocol, driver, test.modelID, test.unaryPath, test.streamPath, tool)
						})
					}
				})
			}
		})
	}
}

func TestBaseURLVersionSuffixOverridesProtocolDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		protocol    providers.Protocol
		modelID     string
		requestPath string
	}{
		{name: "openai", protocol: providers.OpenAIResponses, modelID: "model-1", requestPath: "/gateway/v2/responses"},
		{name: "anthropic", protocol: providers.AnthropicMessages, modelID: "model-1", requestPath: "/gateway/v2/messages"},
		{name: "gemini", protocol: providers.GeminiGenerateContent, modelID: "gemini-2.0-flash", requestPath: "/gateway/v2/models/gemini-2.0-flash:generateContent"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, driver := range []providers.Driver{providers.DriverDefault, providers.DriverOfficialSDK} {
				driver := driver
				t.Run(string(driver), func(t *testing.T) {
					t.Parallel()
					paths := make(chan string, 1)
					server := newLoopbackServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						paths <- request.URL.Path
						writer.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(writer, unaryFixture(test.protocol, false))
					}))
					defer server.Close()

					provider, err := providers.New(providers.Config{
						Protocol: test.protocol, Driver: driver, BaseURL: server.URL + "/gateway/v2",
						APIKey: "test-secret", HTTPClient: server.Client(),
					})
					if err != nil {
						t.Fatalf("New: %v", err)
					}
					model, err := provider.ResolveModel(test.modelID)
					if err != nil {
						t.Fatalf("Model: %v", err)
					}
					request := models.NewTextRequest("hello")
					request.Generation.MaxOutputTokens = models.Some(32)
					if _, err := model.Complete(context.Background(), request); err != nil {
						t.Fatalf("Complete: %v", err)
					}
					if got := <-paths; got != test.requestPath {
						t.Fatalf("request path = %q, want %q", got, test.requestPath)
					}
				})
			}
		})
	}
}

// The closed catalog and wire fixture below track the Google GenerateContent
// multimodal function-response and model lifecycle pages as visible on
// 2026-08-12, rechecked on 2026-08-13. Updating model aliases or the nested
// functionResponse wire requires an explicit fixture change here.
func TestGeminiMultimodalFunctionResponsePolicyAndWire(t *testing.T) {
	allowedModels := []string{
		"gemini-3-flash-preview",
		"gemini-3.1-pro-preview",
		"gemini-3.1-pro-preview-customtools",
		"gemini-3.1-flash-lite",
		"gemini-3.5-flash",
		"gemini-3.5-flash-lite",
		"gemini-3.6-flash",
	}
	deniedModels := []string{
		"gemini-2.5-flash",
		"gemini-3-future",
		"gemini-3.1-flash-image",
		"gemini-3-pro-image",
		"gemini-3.1-flash-live-preview",
		"gemini-flash-latest",
	}
	wantPolicy := models.ToolResultPolicy{
		InlineImages:     true,
		MIMETypes:        []string{"image/jpeg", "image/png", "image/webp"},
		MaxRawImageBytes: 7 << 20,
		MaxImages:        64,
		MaxWidth:         8192,
		MaxHeight:        8192,
		MaxPixels:        40_000_000,
	}
	for _, driver := range []providers.Driver{providers.DriverDefault, providers.DriverOfficialSDK} {
		t.Run(string(driver)+"_policy", func(t *testing.T) {
			provider, err := providers.New(providers.Config{Protocol: providers.GeminiGenerateContent, Driver: driver})
			if err != nil {
				t.Fatal(err)
			}
			for _, modelID := range allowedModels {
				model, err := provider.ResolveModel(modelID)
				if err != nil {
					t.Fatalf("ResolveModel(%q): %v", modelID, err)
				}
				reporter, ok := model.(models.ToolResultPolicyReporter)
				if !ok {
					t.Fatalf("%q has no ToolResultPolicyReporter", modelID)
				}
				policy, supported := reporter.ToolResultPolicy()
				if !supported || !reflect.DeepEqual(policy, wantPolicy) {
					t.Fatalf("ToolResultPolicy(%q) = (%+v, %t), want (%+v, true)", modelID, policy, supported, wantPolicy)
				}
			}
			for _, modelID := range deniedModels {
				model, err := provider.ResolveModel(modelID)
				if err != nil {
					t.Fatalf("ResolveModel(%q): %v", modelID, err)
				}
				policy, supported := model.(models.ToolResultPolicyReporter).ToolResultPolicy()
				if supported || !reflect.DeepEqual(policy, models.ToolResultPolicy{}) {
					t.Fatalf("ToolResultPolicy(%q) = (%+v, %t), want zero/false", modelID, policy, supported)
				}
			}
		})

		t.Run(string(driver)+"_wire", func(t *testing.T) {
			body := make(chan string, 1)
			server := newLoopbackServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				raw, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
					writer.WriteHeader(http.StatusInternalServerError)
					return
				}
				body <- string(raw)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, unaryFixture(providers.GeminiGenerateContent, false))
			}))
			defer server.Close()
			provider, err := providers.New(providers.Config{
				Protocol:   providers.GeminiGenerateContent,
				Driver:     driver,
				BaseURL:    server.URL,
				APIKey:     "test-secret",
				HTTPClient: server.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			model, err := provider.ResolveModel("gemini-3.6-flash")
			if err != nil {
				t.Fatal(err)
			}
			request := &models.Request{Messages: []models.Message{
				models.NewAssistantMessage(models.ToolCallContent("call-1", "lookup", json.RawMessage(`{"x":1}`))),
				models.NewUserMessage(models.ToolResultContent(
					"call-1", "lookup", false,
					models.Text("done"),
					models.ImageBytes("image/png", []byte{0x89, 0x50, 0x4e, 0x47}),
				)),
			}}
			if _, err := model.Complete(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			want := `{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"x":1}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"lookup","response":{"result":"done"},"parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw=="}}]}}]}]}`
			if got := <-body; got != want {
				t.Fatalf("Gemini multimodal function response wire\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func runConformanceCase(t *testing.T, protocol providers.Protocol, driver providers.Driver, modelID, unaryPath, streamPath string, tool bool) {
	t.Helper()
	var calls atomic.Int64
	server := newLoopbackServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(string(body), "test-secret") {
			t.Error("API key leaked into request body")
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("request JSON: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		stream := strings.Contains(request.URL.Path, ":streamGenerateContent")
		if raw, exists := envelope["stream"]; exists {
			_ = json.Unmarshal(raw, &stream)
		}
		// Official OpenAI/Anthropic streaming constructors set stream:true via SDK.
		if strings.Contains(request.Header.Get("Accept"), "text/event-stream") {
			stream = true
		}
		expectedPath := unaryPath
		if stream {
			expectedPath = streamPath
		}
		if request.URL.Path != expectedPath {
			t.Errorf("path = %q, want %q", request.URL.Path, expectedPath)
		}
		if protocol == providers.GeminiGenerateContent {
			if stream && request.URL.Query().Get("alt") != "sse" {
				t.Errorf("Gemini stream query = %q", request.URL.RawQuery)
			}
			if _, exists := envelope["model"]; exists {
				t.Error("Gemini request body unexpectedly contains model")
			}
		} else {
			var wireModel string
			if err := json.Unmarshal(envelope["model"], &wireModel); err != nil || wireModel != modelID {
				t.Errorf("wire model = %q, %v", wireModel, err)
			}
		}
		_, hasTools := envelope["tools"]
		if hasTools != tool {
			t.Errorf("wire tools present = %v, want %v", hasTools, tool)
		}
		assertAuthHeaders(t, protocol, request.Header)
		if request.Header.Get("X-Request-ID") != "client-request" {
			t.Errorf("X-Request-ID = %q, want client-request", request.Header.Get("X-Request-ID"))
		}
		writer.Header().Set("X-Request-ID", "provider-request")
		if stream {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, streamFixture(protocol, tool))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, unaryFixture(protocol, tool))
	}))
	defer server.Close()

	provider, err := providers.New(providers.Config{
		Protocol: protocol, Driver: driver, BaseURL: server.URL,
		APIKey: "test-secret", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	model, err := provider.ResolveModel(modelID)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	req := models.NewTextRequest("hello")
	req.RequestID = "client-request"
	req.Generation.MaxOutputTokens = models.Some(32)
	if tool {
		req.Tools = []models.Tool{models.NewTool("lookup", "look something up", json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`))}
		req.ToolChoice = models.ToolChoice{Kind: models.ToolChoiceAuto}
	}
	unary, err := model.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stream, err := model.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	var deltas strings.Builder
	for stream.Next() {
		deltas.WriteString(stream.Text())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream Err: %v", err)
	}
	streamed := stream.Response()
	if streamed == nil {
		t.Fatal("stream Response is nil")
	}
	if !reflect.DeepEqual(unary, streamed) {
		unaryJSON, _ := json.MarshalIndent(unary, "", "  ")
		streamJSON, _ := json.MarshalIndent(streamed, "", "  ")
		t.Fatalf("unary/stream mismatch\nunary: %s\nstream: %s", unaryJSON, streamJSON)
	}
	if tool {
		toolCalls := streamed.ToolCalls()
		if len(toolCalls) != 1 || toolCalls[0].Name != "lookup" || string(toolCalls[0].Arguments) != `{"x":1}` {
			t.Fatalf("tool calls = %#v", toolCalls)
		}
	} else if unary.Text() != "Hello" || deltas.String() != "Hello" {
		t.Fatalf("text unary=%q stream deltas=%q", unary.Text(), deltas.String())
	}
	if unary.RequestID != "provider-request" {
		t.Fatalf("RequestID = %q", unary.RequestID)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP calls = %d, want 2", calls.Load())
	}
}

func assertAuthHeaders(t *testing.T, protocol providers.Protocol, header http.Header) {
	t.Helper()
	switch protocol {
	case providers.OpenAIChatCompletions, providers.OpenAIResponses:
		if header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("Authorization = %q", header.Get("Authorization"))
		}
	case providers.AnthropicMessages:
		if header.Get("X-API-Key") != "test-secret" || header.Get("Anthropic-Version") != "2023-06-01" {
			t.Errorf("Anthropic auth/version headers = %q/%q", header.Get("X-API-Key"), header.Get("Anthropic-Version"))
		}
	case providers.GeminiGenerateContent:
		if header.Get("X-Goog-API-Key") != "test-secret" {
			t.Errorf("X-Goog-API-Key = %q", header.Get("X-Goog-API-Key"))
		}
	}
}

func unaryFixture(protocol providers.Protocol, tool bool) string {
	switch protocol {
	case providers.OpenAIChatCompletions:
		if tool {
			return `{"id":"resp-1","created":1,"model":"model-1","choices":[{"index":0,"message":{"content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
		}
		return `{"id":"resp-1","created":1,"model":"model-1","choices":[{"index":0,"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	case providers.OpenAIResponses:
		output := `{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello"}]}`
		if tool {
			output = `{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}","status":"completed"}`
		}
		return fmt.Sprintf(`{"id":"resp-1","created_at":1,"model":"model-1","status":"completed","output":[%s],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, output)
	case providers.AnthropicMessages:
		content := `{"type":"text","text":"Hello"}`
		finish := "end_turn"
		if tool {
			content = `{"type":"tool_use","id":"call-1","name":"lookup","input":{"x":1}}`
			finish = "tool_use"
		}
		return fmt.Sprintf(`{"id":"resp-1","type":"message","role":"assistant","model":"model-1","content":[%s],"stop_reason":"%s","usage":{"input_tokens":2,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`, content, finish)
	case providers.GeminiGenerateContent:
		part := `{"text":"Hello"}`
		if tool {
			part = `{"functionCall":{"id":"call-1","name":"lookup","args":{"x":1}}}`
		}
		return fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[%s]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3},"modelVersion":"%s","responseId":"resp-1"}`, part, "gemini-2.0-flash")
	default:
		return `{}`
	}
}

func streamFixture(protocol providers.Protocol, tool bool) string {
	switch protocol {
	case providers.OpenAIChatCompletions:
		if tool {
			return strings.Join([]string{
				`data: {"id":"resp-1","created":1,"model":"model-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"x\":"}}]},"finish_reason":null}]}`,
				`data: {"id":"resp-1","model":"model-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":null}]}`,
				`data: {"id":"resp-1","model":"model-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`data: {"id":"resp-1","model":"model-1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
				`data: [DONE]`, "",
			}, "\n\n")
		}
		return strings.Join([]string{
			`data: {"id":"resp-1","created":1,"model":"model-1","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
			`data: {"id":"resp-1","model":"model-1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
			`data: {"id":"resp-1","model":"model-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"resp-1","model":"model-1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
			`data: [DONE]`, "",
		}, "\n\n")
	case providers.OpenAIResponses:
		created := `data: {"type":"response.created","response":{"id":"resp-1","created_at":1,"model":"model-1","status":"in_progress","output":[]}}`
		terminal := fmt.Sprintf(`data: {"type":"response.completed","response":%s}`, unaryFixture(protocol, tool))
		if tool {
			return strings.Join([]string{
				created,
				`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":"","status":"in_progress"}}`,
				`data: {"type":"response.function_call_arguments.delta","item_id":"item-1","output_index":0,"delta":"{\"x\":"}`,
				`data: {"type":"response.function_call_arguments.delta","item_id":"item-1","output_index":0,"delta":"1}"}`,
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}","status":"completed"}}`,
				terminal, "",
			}, "\n\n")
		}
		return strings.Join([]string{
			created,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg-1","role":"assistant","status":"in_progress","content":[]}}`,
			`data: {"type":"response.content_part.added","item_id":"msg-1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
			`data: {"type":"response.output_text.delta","item_id":"msg-1","output_index":0,"content_index":0,"delta":"Hel"}`,
			`data: {"type":"response.output_text.delta","item_id":"msg-1","output_index":0,"content_index":0,"delta":"lo"}`,
			`data: {"type":"response.output_text.done","item_id":"msg-1","output_index":0,"content_index":0,"text":"Hello"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello"}]}}`,
			terminal, "",
		}, "\n\n")
	case providers.AnthropicMessages:
		// Official Anthropic SDK stream decoder requires the SSE event: field
		// (matching production wire format). Default adapters also accept it.
		start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"resp-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"model-1\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":2,\"output_tokens\":0,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}}"
		if tool {
			return strings.Join([]string{
				start,
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-1\",\"name\":\"lookup\",\"input\":{}}}",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"x\\\":\"}}",
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"1}\"}}",
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}",
				"event: message_stop\ndata: {\"type\":\"message_stop\"}", "",
			}, "\n\n")
		}
		return strings.Join([]string{
			start,
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}", "",
		}, "\n\n")
	case providers.GeminiGenerateContent:
		if tool {
			return `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3},"modelVersion":"gemini-2.0-flash","responseId":"resp-1"}` + "\n\n"
		}
		return strings.Join([]string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}],"modelVersion":"gemini-2.0-flash","responseId":"resp-1"}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3},"modelVersion":"gemini-2.0-flash","responseId":"resp-1"}`, "",
		}, "\n\n")
	default:
		return ""
	}
}
