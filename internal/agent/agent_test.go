package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type fakeTransport struct {
	mu        sync.Mutex
	responses []map[string]any
	requests  []map[string]any
}

func newFakeTransport(responses ...map[string]any) *fakeTransport {
	return &fakeTransport{responses: responses}
}

func (t *fakeTransport) CreateResponse(_ context.Context, request map[string]any) (map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.requests = append(t.requests, request)
	if len(t.responses) == 0 {
		return nil, errString("no fake response queued")
	}

	response := t.responses[0]
	t.responses = t.responses[1:]
	return response, nil
}

func (t *fakeTransport) Requests() []map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()

	requests := make([]map[string]any, len(t.requests))
	copy(requests, t.requests)
	return requests
}

type errString string

func (e errString) Error() string {
	return string(e)
}

func TestAgentReturnsMessageWithoutToolCalls(t *testing.T) {
	transport := newFakeTransport(responseMessage("resp_1", "hello from model"))
	agent := New(Config{
		Transport:    transport,
		Tools:        NewToolExecutor(t.TempDir()),
		Model:        "gpt-5.4",
		Instructions: "test instructions",
	})

	text, err := agent.Run(context.Background(), "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello from model" {
		t.Fatalf("Run() = %q, want %q", text, "hello from model")
	}
}

func TestAgentExecutesToolCallsAndFeedsOutputsBack(t *testing.T) {
	tempDir := t.TempDir()
	transport := newFakeTransport(
		responseToolCall("resp_1", "write_file", "call_1", `{"path":"artifact.txt","content":"created by test"}`),
		responseMessage("resp_2", "done"),
	)
	agent := New(Config{
		Transport:    transport,
		Tools:        NewToolExecutor(tempDir),
		Model:        "gpt-5.4",
		Instructions: "test instructions",
	})

	text, err := agent.Run(context.Background(), "create a file")
	if err != nil {
		t.Fatal(err)
	}
	if text != "done" {
		t.Fatalf("Run() = %q, want %q", text, "done")
	}

	data, err := os.ReadFile(filepath.Join(tempDir, "artifact.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "created by test" {
		t.Fatalf("artifact content = %q", data)
	}

	requests := transport.Requests()
	if got := len(requests); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	if requests[0]["model"] != "gpt-5.4" {
		t.Fatalf("first request model = %v", requests[0]["model"])
	}
	if got := requests[0]["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; got != "create a file" {
		t.Fatalf("first request text = %v", got)
	}
	if requests[1]["previous_response_id"] != "resp_1" {
		t.Fatalf("previous response id = %v", requests[1]["previous_response_id"])
	}

	output := requests[1]["input"].([]any)[0].(map[string]any)
	if output["type"] != "function_call_output" || output["call_id"] != "call_1" {
		t.Fatalf("unexpected tool output envelope: %#v", output)
	}
	if !strings.Contains(output["output"].(string), "Wrote") {
		t.Fatalf("tool output = %q, want it to contain Wrote", output["output"])
	}
}

func TestAgentSurfacesUnknownToolErrorsToTheModel(t *testing.T) {
	transport := newFakeTransport(
		responseToolCall("resp_1", "does_not_exist", "call_1", `{}`),
		responseMessage("resp_2", "handled"),
	)
	agent := New(Config{
		Transport:    transport,
		Tools:        NewToolExecutor(t.TempDir()),
		Model:        "gpt-5.4",
		Instructions: "test instructions",
	})

	text, err := agent.Run(context.Background(), "try an invalid tool")
	if err != nil {
		t.Fatal(err)
	}
	if text != "handled" {
		t.Fatalf("Run() = %q, want %q", text, "handled")
	}

	output := transport.Requests()[1]["input"].([]any)[0].(map[string]any)["output"].(string)
	if !strings.Contains(output, "unknown tool") {
		t.Fatalf("tool output = %q, want unknown tool error", output)
	}
}

func TestAgentRejectsInvalidToolArguments(t *testing.T) {
	transport := newFakeTransport(responseToolCall("resp_1", "write_file", "call_1", `{not-json}`))
	agent := New(Config{
		Transport:    transport,
		Tools:        NewToolExecutor(t.TempDir()),
		Model:        "gpt-5.4",
		Instructions: "test instructions",
	})

	_, err := agent.Run(context.Background(), "bad args")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON arguments") {
		t.Fatalf("Run() error = %v, want invalid JSON arguments", err)
	}
}

func TestAgentStopsWhenToolCallsRepeatWithoutProgress(t *testing.T) {
	transport := newFakeTransport(
		responseToolCall("resp_1", "read_file", "call_1", `{"path":"missing.txt"}`),
		responseToolCall("resp_2", "read_file", "call_2", `{"path":"missing.txt"}`),
	)
	agent := New(Config{
		Transport:    transport,
		Tools:        NewToolExecutor(t.TempDir()),
		Model:        "gpt-5.4",
		Instructions: "test instructions",
	})

	_, err := agent.Run(context.Background(), "loop forever")
	if err == nil || !strings.Contains(err.Error(), "semantic stop condition") {
		t.Fatalf("Run() error = %v, want semantic stop condition", err)
	}
	if got := len(transport.Requests()); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestToolExecutorReadsAndWritesFiles(t *testing.T) {
	tempDir := t.TempDir()
	tools := NewToolExecutor(tempDir)

	writeResult, err := tools.execute(context.Background(), "write_file", map[string]any{
		"path":    "nested/note.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	readResult, err := tools.execute(context.Background(), "read_file", map[string]any{
		"path": "nested/note.txt",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(writeResult, "Wrote") {
		t.Fatalf("write result = %q, want Wrote", writeResult)
	}
	if readResult != "hello" {
		t.Fatalf("read result = %q, want hello", readResult)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "nested", "note.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestToolExecutorRunsShellCommands(t *testing.T) {
	tools := NewToolExecutor(t.TempDir())
	command := "printf hello-shell"
	if runtime.GOOS == "windows" {
		command = "Write-Output hello-shell"
	}

	output, err := tools.execute(context.Background(), "execute_shell", map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "hello-shell") {
		t.Fatalf("shell output = %q, want hello-shell", output)
	}
}

func TestCollectOutputTextJoinsMultipleChunks(t *testing.T) {
	text := collectOutputText(map[string]any{
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "output_text", "text": "first"},
					map[string]any{"type": "output_text", "text": "second"},
				},
			},
		},
	})

	if text != "first\nsecond" {
		t.Fatalf("collectOutputText() = %q", text)
	}
}

func TestParseCLI(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    CLIArgs
		wantErr string
	}{
		{
			name:    "missing model",
			wantErr: "missing required positional argument: model",
		},
		{
			name:    "missing task",
			args:    []string{"gpt-4.1-mini"},
			wantErr: "missing required positional argument: task",
		},
		{
			name: "task joins remaining arguments",
			args: []string{"gpt-4.1-mini", "say", "hi"},
			want: CLIArgs{Model: "gpt-4.1-mini", Task: "say hi"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseCLI(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ParseCLI() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ParseCLI() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func responseMessage(id, text string) map[string]any {
	return map[string]any{
		"id": id,
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "output_text", "text": text},
				},
			},
		},
	}
}

func responseToolCall(id, name, callID, arguments string) map[string]any {
	return map[string]any{
		"id": id,
		"output": []any{
			map[string]any{
				"type":      "function_call",
				"name":      name,
				"call_id":   callID,
				"arguments": arguments,
			},
		},
	}
}
