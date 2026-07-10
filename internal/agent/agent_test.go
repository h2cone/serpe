package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/tw8ap/ouro/internal/canon"
)

type fakeProvider struct {
	mu              sync.Mutex
	responses       []*canon.Response
	streamResponses []*canon.Response
	requests        []*canon.Request
}

func newFakeProvider(responses ...*canon.Response) *fakeProvider {
	return &fakeProvider{responses: responses}
}

func newFakeStreamProvider(responses ...*canon.Response) *fakeProvider {
	return &fakeProvider{streamResponses: responses}
}

func (p *fakeProvider) Complete(_ context.Context, request *canon.Request) (*canon.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return nil, errString("no fake response queued")
	}

	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

func (p *fakeProvider) Stream(_ context.Context, request *canon.Request) (<-chan canon.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, request)
	if len(p.streamResponses) == 0 {
		return nil, errString("no fake stream response queued")
	}

	response := p.streamResponses[0]
	p.streamResponses = p.streamResponses[1:]
	events := eventsForResponse(response)
	ch := make(chan canon.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (p *fakeProvider) Requests() []*canon.Request {
	p.mu.Lock()
	defer p.mu.Unlock()

	requests := make([]*canon.Request, len(p.requests))
	copy(requests, p.requests)
	return requests
}

type errString string

func (e errString) Error() string {
	return string(e)
}

func TestAgentReturnsMessageWithoutToolCalls(t *testing.T) {
	provider := newFakeProvider(responseMessage("resp_1", "hello from model"))
	agent := New(Config{
		Provider:     provider,
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

	requests := provider.Requests()
	if got := len(requests); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if requests[0].Conversation.System != "test instructions" {
		t.Fatalf("system = %q", requests[0].Conversation.System)
	}
	if got := collectText(requests[0].Conversation.Messages[0].Content); got != "say hi" {
		t.Fatalf("first request text = %q", got)
	}
}

func TestAgentExecutesToolCallsAndFeedsOutputsBack(t *testing.T) {
	tempDir := t.TempDir()
	provider := newFakeProvider(
		responseToolCall("resp_1", "write", "call_1", `{"path":"artifact.txt","content":"created by test"}`),
		responseMessage("resp_2", "done"),
	)
	agent := New(Config{
		Provider:     provider,
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

	requests := provider.Requests()
	if got := len(requests); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	if requests[0].Model != "gpt-5.4" {
		t.Fatalf("first request model = %v", requests[0].Model)
	}
	if got := collectText(requests[0].Conversation.Messages[0].Content); got != "create a file" {
		t.Fatalf("first request text = %v", got)
	}

	secondMessages := requests[1].Conversation.Messages
	if got := len(secondMessages); got != 3 {
		t.Fatalf("second request messages = %d, want 3", got)
	}
	output, ok := secondMessages[2].Content[0].(*canon.ToolResultBlock)
	if !ok {
		t.Fatalf("unexpected tool output block: %#v", secondMessages[2].Content[0])
	}
	if output.ToolUseID != "call_1" {
		t.Fatalf("tool result id = %q", output.ToolUseID)
	}
	if !strings.Contains(collectText(output.Content), "Wrote") {
		t.Fatalf("tool output = %q, want it to contain Wrote", collectText(output.Content))
	}
}

func TestAgentSurfacesUnknownToolErrorsToTheModel(t *testing.T) {
	provider := newFakeProvider(
		responseToolCall("resp_1", "does_not_exist", "call_1", `{}`),
		responseMessage("resp_2", "handled"),
	)
	agent := New(Config{
		Provider:     provider,
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

	output := provider.Requests()[1].Conversation.Messages[2].Content[0].(*canon.ToolResultBlock)
	if !output.IsError {
		t.Fatalf("tool result should be marked as error")
	}
	if !strings.Contains(collectText(output.Content), "unknown tool") {
		t.Fatalf("tool output = %q, want unknown tool error", collectText(output.Content))
	}
}

func TestAgentRejectsInvalidToolArguments(t *testing.T) {
	provider := newFakeProvider(responseToolCall("resp_1", "write", "call_1", `{not-json}`))
	agent := New(Config{
		Provider:     provider,
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
	provider := newFakeProvider(
		responseToolCall("resp_1", "read", "call_1", `{"path":"missing.txt"}`),
		responseToolCall("resp_2", "read", "call_2", `{"path":"missing.txt"}`),
	)
	agent := New(Config{
		Provider:     provider,
		Tools:        NewToolExecutor(t.TempDir()),
		Model:        "gpt-5.4",
		Instructions: "test instructions",
	})

	_, err := agent.Run(context.Background(), "loop forever")
	if err == nil || !strings.Contains(err.Error(), "semantic stop condition") {
		t.Fatalf("Run() error = %v, want semantic stop condition", err)
	}
	if got := len(provider.Requests()); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestAgentRunRequestPreservesTranscript(t *testing.T) {
	provider := newFakeProvider(responseMessage("resp_1", "continued"))
	agent := New(Config{
		Provider:     provider,
		Tools:        NewToolExecutor(t.TempDir()),
		Model:        "fallback-model",
		Instructions: "fallback instructions",
		MaxTokens:    123,
	})

	seed := &canon.Request{
		Model: "request-model",
		Conversation: canon.Conversation{
			System: "request instructions",
			Messages: []canon.Message{
				{Role: canon.RoleUser, Content: []canon.ContentBlock{&canon.TextBlock{Text: "first"}}},
				{Role: canon.RoleAssistant, Content: []canon.ContentBlock{&canon.TextBlock{Text: "second"}}},
				{Role: canon.RoleUser, Content: []canon.ContentBlock{&canon.TextBlock{Text: "third"}}},
			},
		},
		MaxTokens: 99,
		Extra:     map[string]any{"sidecar": "kept"},
	}

	text, err := agent.RunRequest(context.Background(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if text != "continued" {
		t.Fatalf("RunRequest() = %q", text)
	}

	got := provider.Requests()[0]
	if got.Model != "request-model" || got.MaxTokens != 99 {
		t.Fatalf("request meta = model %q max %d", got.Model, got.MaxTokens)
	}
	if got.Conversation.System != "request instructions" {
		t.Fatalf("system = %q", got.Conversation.System)
	}
	if len(got.Conversation.Messages) != 3 {
		t.Fatalf("messages = %d", len(got.Conversation.Messages))
	}
	if got.Extra["sidecar"] != "kept" {
		t.Fatalf("extra = %#v", got.Extra)
	}
}

func TestAgentRunTurnReturnsCompletedTranscript(t *testing.T) {
	provider := newFakeProvider(
		responseToolCall("resp_1", "read", "call_1", `{"path":"missing.txt"}`),
		responseMessage("resp_2", "finished"),
	)
	runner := New(Config{
		Provider: provider,
		Tools:    NewToolExecutor(t.TempDir()),
		Model:    "gpt-5.4",
	})

	result, err := runner.RunTurn(context.Background(), &canon.Request{
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role:    canon.RoleUser,
			Content: []canon.ContentBlock{&canon.TextBlock{Text: "inspect"}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "finished" || result.Response.ID != "resp_2" {
		t.Fatalf("turn result = %#v", result)
	}
	if got := len(result.Conversation.Messages); got != 4 {
		t.Fatalf("completed transcript messages = %d, want 4", got)
	}
	if _, ok := result.Conversation.Messages[2].Content[0].(*canon.ToolResultBlock); !ok {
		t.Fatalf("third message = %#v, want tool result", result.Conversation.Messages[2])
	}
}

func TestSessionMaintainsIndependentMultiTurnTranscript(t *testing.T) {
	provider := newFakeProvider(
		responseMessage("resp_1", "first answer"),
		responseMessage("resp_2", "second answer"),
	)
	runner := New(Config{
		Provider:     provider,
		Tools:        NewToolExecutor(t.TempDir()),
		Model:        "gpt-5.4",
		Instructions: "session instructions",
	})
	session, err := NewSession(runner, nil)
	if err != nil {
		t.Fatal(err)
	}

	first, err := session.SendText(context.Background(), "first question")
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "first answer" || len(first.Conversation.Messages) != 2 {
		t.Fatalf("first turn = %#v", first)
	}

	second, err := session.SendText(context.Background(), "second question")
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "second answer" || len(second.Conversation.Messages) != 4 {
		t.Fatalf("second turn = %#v", second)
	}

	requests := provider.Requests()
	if got := len(requests[1].Conversation.Messages); got != 3 {
		t.Fatalf("second provider request messages = %d, want 3", got)
	}
	if got := collectText(requests[1].Conversation.Messages[2].Content); got != "second question" {
		t.Fatalf("second provider request text = %q", got)
	}

	snapshot := session.Snapshot()
	if snapshot.Conversation.System != "session instructions" || len(snapshot.Conversation.Messages) != 4 {
		t.Fatalf("snapshot = %#v", snapshot.Conversation)
	}
	snapshot.Conversation.Messages[0].Content[0].(*canon.TextBlock).Text = "mutated"
	snapshot.Conversation.Messages = nil
	stored := session.Snapshot()
	if got := len(stored.Conversation.Messages); got != 4 {
		t.Fatalf("snapshot mutation leaked into session: %d messages", got)
	}
	if got := collectText(stored.Conversation.Messages[0].Content); got != "first question" {
		t.Fatalf("snapshot content mutation leaked into session: %q", got)
	}
}

func TestSessionDoesNotCommitFailedTurn(t *testing.T) {
	provider := newFakeProvider(responseMessage("resp_1", "first answer"))
	runner := New(Config{
		Provider: provider,
		Tools:    NewToolExecutor(t.TempDir()),
		Model:    "gpt-5.4",
	})
	session, err := NewSession(runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendText(context.Background(), "first question"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendText(context.Background(), "will fail"); err == nil {
		t.Fatal("second turn unexpectedly succeeded")
	}
	if got := len(session.Snapshot().Conversation.Messages); got != 2 {
		t.Fatalf("failed turn changed session transcript: %d messages", got)
	}
}

func TestAgentRunStreamRequestHidesToolTurnsAndWritesFinal(t *testing.T) {
	tempDir := t.TempDir()
	provider := newFakeStreamProvider(
		&canon.Response{
			ID:    "resp_1",
			Model: "gpt-5.4",
			Content: []canon.ContentBlock{
				&canon.TextBlock{Text: "internal note"},
				&canon.ToolUseBlock{ID: "call_1", Name: "write", Input: json.RawMessage(`{"path":"stream.txt","content":"ok"}`)},
			},
			FinishReason: canon.FinishToolCalls,
		},
		responseMessage("resp_2", "streamed final"),
	)
	agent := New(Config{
		Provider:     provider,
		Tools:        NewToolExecutor(tempDir),
		Model:        "gpt-5.4",
		Instructions: "test instructions",
	})

	var out bytes.Buffer
	text, err := agent.RunStreamRequest(context.Background(), &canon.Request{
		Model: "gpt-5.4",
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role:    canon.RoleUser,
			Content: []canon.ContentBlock{&canon.TextBlock{Text: "stream it"}},
		}}},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if text != "streamed final" || out.String() != "streamed final" {
		t.Fatalf("stream text = %q writer = %q", text, out.String())
	}
	if strings.Contains(out.String(), "internal note") {
		t.Fatalf("stream leaked internal tool-turn text: %q", out.String())
	}
	data, err := os.ReadFile(filepath.Join(tempDir, "stream.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("stream artifact = %q", data)
	}
	if got := len(provider.Requests()); got != 2 {
		t.Fatalf("stream requests = %d, want 2", got)
	}
	if !provider.Requests()[0].Stream {
		t.Fatalf("first stream request did not set Stream=true")
	}
}

func TestAgentRejectsSpecificUnregisteredToolChoice(t *testing.T) {
	provider := newFakeProvider(responseMessage("resp_1", "unreached"))
	agent := New(Config{
		Provider: provider,
		Tools:    NewToolExecutor(t.TempDir()),
		Model:    "gpt-5.4",
	})

	_, err := agent.RunRequest(context.Background(), &canon.Request{
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role:    canon.RoleUser,
			Content: []canon.ContentBlock{&canon.TextBlock{Text: "hi"}},
		}}},
		ToolChoice: canon.ToolChoice{Mode: canon.ToolChoiceSpecific, Name: "missing"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported tool_choice specific") {
		t.Fatalf("RunRequest() error = %v", err)
	}
}

func TestAgentRelaxesForcedToolChoiceAfterToolRound(t *testing.T) {
	tests := []struct {
		name   string
		choice canon.ToolChoice
	}{
		{name: "required", choice: canon.ToolChoice{Mode: canon.ToolChoiceRequired}},
		{name: "specific", choice: canon.ToolChoice{Mode: canon.ToolChoiceSpecific, Name: "read"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := newFakeProvider(
				responseToolCall("resp_1", "read", "call_1", `{"path":"missing.txt"}`),
				responseMessage("resp_2", "finished"),
			)
			runner := New(Config{
				Provider: provider,
				Tools:    NewToolExecutor(t.TempDir()),
				Model:    "gpt-5.4",
			})

			text, err := runner.RunRequest(context.Background(), &canon.Request{
				Conversation: canon.Conversation{Messages: []canon.Message{{
					Role:    canon.RoleUser,
					Content: []canon.ContentBlock{&canon.TextBlock{Text: "inspect"}},
				}}},
				ToolChoice: tc.choice,
			})
			if err != nil {
				t.Fatal(err)
			}
			if text != "finished" {
				t.Fatalf("text = %q", text)
			}

			requests := provider.Requests()
			if len(requests) != 2 {
				t.Fatalf("request count = %d, want 2", len(requests))
			}
			if requests[0].ToolChoice != tc.choice {
				t.Fatalf("first tool choice = %#v, want %#v", requests[0].ToolChoice, tc.choice)
			}
			if requests[1].ToolChoice.Mode != canon.ToolChoiceAuto {
				t.Fatalf("second tool choice = %#v, want auto", requests[1].ToolChoice)
			}
		})
	}
}

func TestAgentStreamRelaxesForcedToolChoiceAfterToolRound(t *testing.T) {
	provider := newFakeStreamProvider(
		responseToolCall("resp_1", "read", "call_1", `{"path":"missing.txt"}`),
		responseMessage("resp_2", "finished"),
	)
	runner := New(Config{
		Provider: provider,
		Tools:    NewToolExecutor(t.TempDir()),
		Model:    "gpt-5.4",
	})

	var out bytes.Buffer
	_, err := runner.RunStreamRequest(context.Background(), &canon.Request{
		Conversation: canon.Conversation{Messages: []canon.Message{{
			Role:    canon.RoleUser,
			Content: []canon.ContentBlock{&canon.TextBlock{Text: "inspect"}},
		}}},
		ToolChoice: canon.ToolChoice{Mode: canon.ToolChoiceRequired},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].ToolChoice.Mode != canon.ToolChoiceRequired || requests[1].ToolChoice.Mode != canon.ToolChoiceAuto {
		t.Fatalf("tool choices = %#v, %#v", requests[0].ToolChoice, requests[1].ToolChoice)
	}
}

func TestToolExecutorReadsAndWritesFiles(t *testing.T) {
	tempDir := t.TempDir()
	tools := NewToolExecutor(tempDir)

	writeResult, err := tools.execute(context.Background(), "write", map[string]any{
		"path":    "nested/note.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	readResult, err := tools.execute(context.Background(), "read", map[string]any{
		"path":   "nested/note.txt",
		"offset": nil,
		"limit":  nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(resultText(writeResult), "Wrote") {
		t.Fatalf("write result = %q, want Wrote", resultText(writeResult))
	}
	if !strings.Contains(resultText(readResult), "1\thello") {
		t.Fatalf("read result = %q, want line-numbered hello", resultText(readResult))
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

	output, err := tools.execute(context.Background(), "bash", map[string]any{"command": command, "timeout": nil})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(output), "hello-shell") {
		t.Fatalf("shell output = %q, want hello-shell", resultText(output))
	}
}

func TestDefaultToolDefinitionsUsePlannedNames(t *testing.T) {
	defs := NewToolExecutor(t.TempDir()).Definitions()
	var names []string
	for _, def := range defs {
		names = append(names, def.Name)
	}
	want := []string{"read", "write", "edit", "bash"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", names, want)
	}
}

func TestReadToolSupportsWindowsAndImages(t *testing.T) {
	tempDir := t.TempDir()
	tools := NewToolExecutor(tempDir)
	text := strings.Join([]string{"one", "two", "three"}, "\n")
	if err := os.WriteFile(filepath.Join(tempDir, "note.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := tools.execute(context.Background(), "read", map[string]any{
		"path":   "note.md",
		"offset": 2,
		"limit":  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(result); !strings.Contains(got, "2\ttwo") || strings.Contains(got, "three") {
		t.Fatalf("read window = %q", got)
	}

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(tempDir, "tiny.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	imageResult, err := tools.execute(context.Background(), "read", map[string]any{
		"path":   "tiny.png",
		"offset": nil,
		"limit":  nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(imageResult.Content) != 2 {
		t.Fatalf("image content blocks = %#v", imageResult.Content)
	}
	if _, ok := imageResult.Content[1].(*canon.ImageBlock); !ok {
		t.Fatalf("second block = %#v, want ImageBlock", imageResult.Content[1])
	}
}

func TestEditToolRequiresUniqueMatchUnlessReplaceAll(t *testing.T) {
	tempDir := t.TempDir()
	tools := NewToolExecutor(tempDir)
	path := filepath.Join(tempDir, "note.txt")
	if err := os.WriteFile(path, []byte("same\nsame\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := tools.execute(context.Background(), "edit", map[string]any{
		"path":        "note.txt",
		"old_string":  "same",
		"new_string":  "changed",
		"replace_all": nil,
	})
	if err == nil || !strings.Contains(err.Error(), "matches 2 times") {
		t.Fatalf("edit error = %v, want multiple match error", err)
	}

	result, err := tools.execute(context.Background(), "edit", map[string]any{
		"path":        "note.txt",
		"old_string":  "same",
		"new_string":  "changed",
		"replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), "2 replacement") {
		t.Fatalf("edit result = %q", resultText(result))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "changed\nchanged\n" {
		t.Fatalf("edited content = %q", data)
	}
}

func TestBashToolReportsNonzeroAndTimeoutAsOutput(t *testing.T) {
	tools := NewToolExecutor(t.TempDir())
	failCommand := "printf nope; exit 7"
	timeoutCommand := "sleep 3"
	if runtime.GOOS == "windows" {
		failCommand = "Write-Output nope; exit 7"
		timeoutCommand = "Start-Sleep -Seconds 3"
	}

	failed, err := tools.execute(context.Background(), "bash", map[string]any{"command": failCommand, "timeout": 5})
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(failed); !strings.Contains(text, "nope") || !strings.Contains(text, "exit status: 7") {
		t.Fatalf("failed output = %q", text)
	}

	timedOut, err := tools.execute(context.Background(), "bash", map[string]any{"command": timeoutCommand, "timeout": 1})
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(timedOut); !strings.Contains(text, "timed out after 1s") {
		t.Fatalf("timeout output = %q", text)
	}
}

func TestCollectTextJoinsMultipleChunks(t *testing.T) {
	text := collectText([]canon.ContentBlock{
		&canon.TextBlock{Text: "first"},
		&canon.TextBlock{Text: "second"},
	})

	if text != "first\nsecond" {
		t.Fatalf("collectText() = %q", text)
	}
}

func responseMessage(id, text string) *canon.Response {
	return &canon.Response{
		ID:           id,
		Model:        "gpt-5.4",
		Content:      []canon.ContentBlock{&canon.TextBlock{Text: text}},
		FinishReason: canon.FinishStop,
	}
}

func responseToolCall(id, name, callID, arguments string) *canon.Response {
	return &canon.Response{
		ID:    id,
		Model: "gpt-5.4",
		Content: []canon.ContentBlock{
			&canon.ToolUseBlock{
				ID:    callID,
				Name:  name,
				Input: json.RawMessage(arguments),
			},
		},
		FinishReason: canon.FinishToolCalls,
	}
}

func resultText(r Result) string {
	return collectText(r.Content)
}

func eventsForResponse(resp *canon.Response) []canon.Event {
	events := []canon.Event{
		canon.MessageStartEvent{Response: &canon.Response{ID: resp.ID, Model: resp.Model, Provider: resp.Provider}},
	}
	for i, block := range resp.Content {
		switch b := block.(type) {
		case *canon.TextBlock:
			events = append(events,
				canon.ContentBlockStartEvent{Index: i, Block: &canon.TextBlock{}},
				canon.ContentBlockDeltaEvent{Index: i, Delta: canon.Delta{Type: canon.DeltaText, Text: b.Text}},
				canon.ContentBlockStopEvent{Index: i},
			)
		case *canon.ToolUseBlock:
			events = append(events,
				canon.ContentBlockStartEvent{Index: i, Block: &canon.ToolUseBlock{ID: b.ID, Name: b.Name}},
				canon.ContentBlockDeltaEvent{Index: i, Delta: canon.Delta{Type: canon.DeltaInputJSON, Partial: string(b.Input)}},
				canon.ContentBlockStopEvent{Index: i},
			)
		}
	}
	events = append(events,
		canon.MessageDeltaEvent{FinishReason: resp.FinishReason, Usage: &resp.Usage},
		canon.MessageStopEvent{},
	)
	return events
}
