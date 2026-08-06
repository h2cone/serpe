package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/core/models"
)

func userReq(text string) *models.Request {
	return models.NewTextRequest(text)
}

func terminalResponse(status models.ResponseStatus, finish models.FinishReason, raw string, content ...models.Content) *models.Response {
	return &models.Response{
		Provider: "script",
		ID:       "terminal",
		Model:    "m",
		Status:   status,
		Candidates: []models.Candidate{{
			Index:           0,
			Content:         content,
			FinishReason:    finish,
			RawFinishReason: raw,
		}},
	}
}

func TestRunNoToolsCompletes(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{textResponse("hello")}}
	r, err := agent.NewRunner(agent.Config{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), userReq("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() || result.Text() != "hello" {
		t.Fatalf("result: %+v text=%q", result.StopReason, result.Text())
	}
	if len(result.Transcript) != 2 {
		t.Fatalf("transcript len=%d", len(result.Transcript))
	}
}

func TestRunSingleToolThenAnswer(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"v":"x"}`)}),
		textResponse("done"),
	}}
	tool := newStubTool("echo", func(_ context.Context, args json.RawMessage) (agent.ToolResult, error) {
		return agent.TextResult("echo:" + string(args)), nil
	})
	r, err := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() || result.Text() != "done" {
		t.Fatalf("stop=%s text=%q", result.StopReason, result.Text())
	}
	if len(result.Steps) != 2 || len(result.Steps[0].ToolResults) != 1 {
		t.Fatalf("steps=%+v", result.Steps)
	}
	// transcript: user, assistant(tool), user(results), assistant(final)
	if len(result.Transcript) != 4 {
		t.Fatalf("transcript len=%d", len(result.Transcript))
	}
}

func TestRunMultiToolsOrder(t *testing.T) {
	t.Parallel()
	var order []string
	var mu sync.Mutex
	makeTool := func(name string) agent.Tool {
		return newStubTool(name, func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return agent.TextResult(name), nil
		})
	}
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(
			models.ToolCall{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)},
			models.ToolCall{ID: "2", Name: "b", Arguments: json.RawMessage(`{}`)},
		),
		textResponse("ok"),
	}}
	r, err := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{makeTool("a"), makeTool("b")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), userReq("go")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "a,b" {
		t.Fatalf("order=%v", order)
	}
}

func TestUnknownToolRecoverable(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "missing", Arguments: json.RawMessage(`{}`)}),
		textResponse("recovered"),
	}}
	r, err := agent.NewRunner(agent.Config{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() {
		t.Fatalf("stop=%s", result.StopReason)
	}
	tr := result.Steps[0].ToolResults[0]
	if !tr.ToolResult.IsError {
		t.Fatal("expected error tool result")
	}
}

func TestRecoverableToolErrorResult(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
		textResponse("ok"),
	}}
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		return agent.ErrorResult("bad args"), nil
	})
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() || !result.Steps[0].ToolResults[0].ToolResult.IsError {
		t.Fatalf("unexpected: %+v", result)
	}
}

func TestFatalToolError(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{"secret":"do-not-leak"}`)}),
	}}
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		return agent.ToolResult{}, errors.New("boom")
	})
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, agent.ErrToolExecution) {
		t.Fatalf("err=%v", err)
	}
	if result == nil || result.StopReason != agent.StopFailed {
		t.Fatalf("result=%+v", result)
	}
	if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("tool arguments leaked in error: %v", err)
	}
}

func TestFatalToolErrorPreservesEarlierBatchResults(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(
			models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)},
			models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)},
		),
	}}
	var executions int32
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		if atomic.AddInt32(&executions, 1) == 1 {
			return agent.TextResult("first completed"), nil
		}
		return agent.ToolResult{}, errors.New("second failed")
	})
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, agent.ErrToolExecution) {
		t.Fatalf("err=%v", err)
	}
	if len(result.Steps) != 1 || len(result.Steps[0].ToolResults) != 1 {
		t.Fatalf("completed tool results lost: %+v", result.Steps)
	}
	got := result.Steps[0].ToolResults[0].ToolResult.Content[0].Text.Text
	if got != "first completed" {
		t.Fatalf("result=%q", got)
	}
}

func TestDuplicateCallID(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(
			models.ToolCall{ID: "same", Name: "f", Arguments: json.RawMessage(`{}`)},
			models.ToolCall{ID: "same", Name: "f", Arguments: json.RawMessage(`{}`)},
		),
	}}
	tool := newStubTool("f", nil)
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, agent.ErrInvalidModelResponse) {
		t.Fatalf("err=%v", err)
	}
	if result.StopReason != agent.StopFailed {
		t.Fatalf("stop=%s", result.StopReason)
	}
}

func TestEmptyCallID(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "", Name: "f", Arguments: json.RawMessage(`{}`)}),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	_, err := r.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, agent.ErrInvalidModelResponse) {
		t.Fatalf("err=%v", err)
	}
}

func TestRequestValidation(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{textResponse("x")}}
	r, _ := agent.NewRunner(agent.Config{Model: model})

	if _, err := r.Run(context.Background(), nil); err == nil {
		t.Fatal("nil request")
	}
	if _, err := r.Run(context.Background(), &models.Request{}); err == nil {
		t.Fatal("empty messages")
	}
	req := &models.Request{Messages: []models.Message{models.NewAssistantMessage(models.Text("a"))}}
	if _, err := r.Run(context.Background(), req); err == nil {
		t.Fatal("last not user")
	}
	req = userReq("hi")
	req.Tools = []models.Tool{models.NewTool("x", "", json.RawMessage(`{"type":"object"}`))}
	if _, err := r.Run(context.Background(), req); err == nil {
		t.Fatal("caller tools")
	}
	req = userReq("hi")
	req.Generation.CandidateCount = models.Some(2)
	if _, err := r.Run(context.Background(), req); err == nil {
		t.Fatal("candidate count")
	}
}

func TestMaxModelTurns(t *testing.T) {
	t.Parallel()
	// Always tool call → hits turn budget before executing tools on last turn.
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
		toolCallResponse(models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)}),
	}}
	var execs int32
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		atomic.AddInt32(&execs, 1)
		return agent.TextResult("ok"), nil
	})
	r, _ := agent.NewRunner(agent.Config{
		Model:  model,
		Tools:  []agent.Tool{tool},
		Limits: agent.Limits{MaxModelTurns: 1},
	})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != agent.StopMaxModelTurns {
		t.Fatalf("stop=%s", result.StopReason)
	}
	if atomic.LoadInt32(&execs) != 0 {
		t.Fatal("tools must not run without remaining model turn")
	}
	if result.Completed() || result.Text() != "" {
		t.Fatal("controlled stop must not look completed")
	}
}

func TestMaxToolCalls(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(
			models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)},
			models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)},
		),
	}}
	var execs int32
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		atomic.AddInt32(&execs, 1)
		return agent.TextResult("ok"), nil
	})
	r, _ := agent.NewRunner(agent.Config{
		Model:  model,
		Tools:  []agent.Tool{tool},
		Limits: agent.Limits{MaxToolCalls: 1},
	})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != agent.StopMaxToolCalls {
		t.Fatalf("stop=%s", result.StopReason)
	}
	if atomic.LoadInt32(&execs) != 0 {
		t.Fatal("preflight must skip entire batch")
	}
}

func TestMaxObservedTokens(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		withUsage(toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}), 100),
	}}
	var execs int32
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		atomic.AddInt32(&execs, 1)
		return agent.TextResult("ok"), nil
	})
	r, _ := agent.NewRunner(agent.Config{
		Model:  model,
		Tools:  []agent.Tool{tool},
		Limits: agent.Limits{MaxObservedTokens: 50},
	})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != agent.StopMaxObservedTokens {
		t.Fatalf("stop=%s observed=%d", result.StopReason, result.ObservedTotalTokens)
	}
	if atomic.LoadInt32(&execs) != 0 {
		t.Fatal("tools must not run after token overrun")
	}
}

func TestMaxObservedTokensFinalAnswerStillCompletes(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		withUsage(textResponse("final"), 100),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Limits: agent.Limits{MaxObservedTokens: 50}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() || result.Text() != "final" {
		t.Fatalf("stop=%s text=%q", result.StopReason, result.Text())
	}
}

func TestStallDetection(t *testing.T) {
	t.Parallel()
	// Same tool name/args/result three times.
	resp := func(id string) *models.Response {
		return toolCallResponse(models.ToolCall{ID: id, Name: "f", Arguments: json.RawMessage(`{"a":1,"b":2}`)})
	}
	model := &scriptedModel{responses: []*models.Response{
		resp("1"),
		resp("2"),
		resp("3"),
		textResponse("should not reach"),
	}}
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		return agent.TextResult("same"), nil
	})
	r, _ := agent.NewRunner(agent.Config{
		Model:  model,
		Tools:  []agent.Tool{tool},
		Limits: agent.Limits{MaxIdenticalSteps: 3},
	})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != agent.StopStalled {
		t.Fatalf("stop=%s", result.StopReason)
	}
}

func TestStallResetsOnChange(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
		toolCallResponse(models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)}),
		textResponse("done"),
	}}
	var n int32
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		v := atomic.AddInt32(&n, 1)
		return agent.TextResult(string(rune('a' + v - 1))), nil
	})
	r, _ := agent.NewRunner(agent.Config{
		Model:  model,
		Tools:  []agent.Tool{tool},
		Limits: agent.Limits{MaxIdenticalSteps: 2},
	})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() {
		t.Fatalf("stop=%s", result.StopReason)
	}
}

func TestToolChoiceRelaxation(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
		textResponse("done"),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	req := userReq("go")
	req.ToolChoice = models.ToolChoice{Kind: models.ToolChoiceRequired}
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	first := model.requestAt(0)
	second := model.requestAt(1)
	if first.ToolChoice.Kind != models.ToolChoiceRequired {
		t.Fatalf("first choice=%s", first.ToolChoice.Kind)
	}
	if second.ToolChoice.Kind != models.ToolChoiceAuto {
		t.Fatalf("second choice=%s", second.ToolChoice.Kind)
	}
}

func TestToolChoiceNonePreserved(t *testing.T) {
	t.Parallel()
	// With none, model just answers; no tools used.
	model := &scriptedModel{responses: []*models.Response{textResponse("hi")}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	req := userReq("go")
	req.ToolChoice = models.ToolChoice{Kind: models.ToolChoiceNone}
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if model.requestAt(0).ToolChoice.Kind != models.ToolChoiceNone {
		t.Fatal("none must be preserved")
	}
}

func TestProviderStatePreserved(t *testing.T) {
	t.Parallel()
	state := &models.ProviderState{Provider: "script", Data: json.RawMessage(`{"s":1}`)}
	model := &scriptedModel{responses: []*models.Response{
		withState(toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}), state),
		textResponse("done"),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	asst := result.Transcript[1]
	if asst.ProviderState == nil || string(asst.ProviderState.Data) != `{"s":1}` {
		t.Fatalf("provider state missing: %+v", asst.ProviderState)
	}
}

func TestInputDeepCopy(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{textResponse("ok")}}
	r, _ := agent.NewRunner(agent.Config{Model: model})
	req := userReq("seed")
	result, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages[0].Content[0].Text.Text = "mutated"
	if result.Transcript[0].Content[0].Text.Text != "seed" {
		t.Fatal("input mutation leaked into result")
	}
}

func TestRefusalAndReasoning(t *testing.T) {
	t.Parallel()
	resp := &models.Response{
		Provider: "script", ID: "r", Model: "m", Status: models.ResponseStatusCompleted,
		Candidates: []models.Candidate{{
			Index: 0,
			Content: []models.Content{
				models.ReasoningSummary("think"),
				models.Refusal("nope"),
			},
			FinishReason: models.FinishStop,
		}},
	}
	model := &scriptedModel{responses: []*models.Response{resp}}
	r, _ := agent.NewRunner(agent.Config{Model: model})
	result, err := r.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() {
		t.Fatalf("stop=%s", result.StopReason)
	}
	// Text includes reasoning+refusal via Response.Text
	if result.Text() == "" {
		t.Fatal("expected text from refusal/reasoning")
	}
}

func TestModelTerminalClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		response  *models.Response
		cancelled bool
	}{
		{
			name:     "incomplete status",
			response: terminalResponse(models.ResponseStatusIncomplete, models.FinishIncomplete, "incomplete", models.Text("partial")),
		},
		{
			name: "failed status without candidate",
			response: &models.Response{
				Provider: "script", ID: "failed", Model: "m", Status: models.ResponseStatusFailed,
			},
		},
		{
			name:      "cancelled status",
			response:  terminalResponse(models.ResponseStatusCancelled, models.FinishCancelled, "cancelled", models.Text("partial")),
			cancelled: true,
		},
		{
			name:     "length finish",
			response: terminalResponse(models.ResponseStatusCompleted, models.FinishLength, "max_tokens", models.Text("partial")),
		},
		{
			name:     "content filter without refusal",
			response: terminalResponse(models.ResponseStatusCompleted, models.FinishContentFilter, "SAFETY", models.Text("partial")),
		},
		{
			name:     "error finish",
			response: terminalResponse(models.ResponseStatusCompleted, models.FinishError, "error", models.Text("partial")),
		},
		{
			name:     "unknown finish",
			response: terminalResponse(models.ResponseStatusCompleted, models.FinishUnknown, "new_provider_reason", models.Text("partial")),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			model := &scriptedModel{responses: []*models.Response{tt.response}}
			r, _ := agent.NewRunner(agent.Config{Model: model})
			result, err := r.Run(context.Background(), userReq("go"))
			if err == nil || !errors.Is(err, agent.ErrModelResponse) {
				t.Fatalf("err=%v", err)
			}
			if result == nil || result.Completed() || result.LastResponse() == nil || len(result.Steps) != 1 {
				t.Fatalf("partial result=%+v", result)
			}
			if len(result.Transcript) != 1 {
				t.Fatalf("non-committable assistant leaked into transcript: %+v", result.Transcript)
			}
			if tt.cancelled {
				if !errors.Is(err, context.Canceled) || result.StopReason != agent.StopCancelled {
					t.Fatalf("cancelled err=%v stop=%s", err, result.StopReason)
				}
			} else if result.StopReason != agent.StopFailed {
				t.Fatalf("stop=%s", result.StopReason)
			}
		})
	}
}

func TestCompletedRefusalIsAValidFinalAnswer(t *testing.T) {
	t.Parallel()
	tests := []*models.Response{
		terminalResponse(models.ResponseStatusCompleted, models.FinishContentFilter, "content_filter", models.Refusal("no")),
		terminalResponse(models.ResponseStatusCompleted, models.FinishContentFilter, "refusal", models.Text("I cannot help with that.")),
	}
	for i, response := range tests {
		model := &scriptedModel{responses: []*models.Response{response}}
		r, _ := agent.NewRunner(agent.Config{Model: model})
		result, err := r.Run(context.Background(), userReq("go"))
		if err != nil || !result.Completed() {
			t.Fatalf("case %d: err=%v result=%+v", i, err, result)
		}
	}
}

func TestNonSuccessToolResponseDoesNotExecute(t *testing.T) {
	t.Parallel()
	response := toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)})
	response.Status = models.ResponseStatusIncomplete
	response.Candidates[0].FinishReason = models.FinishIncomplete
	var executions int32
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		atomic.AddInt32(&executions, 1)
		return agent.TextResult("unexpected"), nil
	})
	model := &scriptedModel{responses: []*models.Response{response}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, agent.ErrModelResponse) {
		t.Fatalf("err=%v", err)
	}
	if atomic.LoadInt32(&executions) != 0 || len(result.Transcript) != 1 {
		t.Fatalf("executions=%d transcript=%+v", executions, result.Transcript)
	}
}

func TestResponseFinishMustMatchToolCalls(t *testing.T) {
	t.Parallel()
	tests := []*models.Response{
		terminalResponse(models.ResponseStatusCompleted, models.FinishToolCall, "tool_calls", models.Text("missing call")),
		terminalResponse(models.ResponseStatusCompleted, models.FinishStop, "stop", models.ToolCallContent("1", "f", json.RawMessage(`{}`))),
	}
	for i, response := range tests {
		model := &scriptedModel{responses: []*models.Response{response}}
		r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
		result, err := r.Run(context.Background(), userReq("go"))
		if err == nil || !errors.Is(err, agent.ErrInvalidModelResponse) {
			t.Fatalf("case %d: err=%v", i, err)
		}
		if len(result.Transcript) != 1 {
			t.Fatalf("case %d: invalid assistant leaked", i)
		}
	}
}

func TestToolChoiceResponseIsEnforcedBeforeExecution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		response *models.Response
		choice   models.ToolChoice
	}{
		{
			name:     "none rejects calls",
			response: toolCallResponse(models.ToolCall{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)}),
			choice:   models.ToolChoice{Kind: models.ToolChoiceNone},
		},
		{
			name:     "required rejects no call",
			response: textResponse("no call"),
			choice:   models.ToolChoice{Kind: models.ToolChoiceRequired},
		},
		{
			name:     "specific rejects another tool",
			response: toolCallResponse(models.ToolCall{ID: "1", Name: "b", Arguments: json.RawMessage(`{}`)}),
			choice:   models.SpecificTool("a"),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var executions int32
			makeTool := func(name string) agent.Tool {
				return newStubTool(name, func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
					atomic.AddInt32(&executions, 1)
					return agent.TextResult("unexpected"), nil
				})
			}
			model := &scriptedModel{responses: []*models.Response{tt.response}}
			r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{makeTool("a"), makeTool("b")}})
			req := userReq("go")
			req.ToolChoice = tt.choice
			result, err := r.Run(context.Background(), req)
			if err == nil || !errors.Is(err, agent.ErrInvalidModelResponse) {
				t.Fatalf("err=%v", err)
			}
			if atomic.LoadInt32(&executions) != 0 || len(result.Transcript) != 1 {
				t.Fatalf("executions=%d transcript=%+v", executions, result.Transcript)
			}
		})
	}
}

func TestObservedTokenAccountingRejectsNegativeAndSaturatesOverflow(t *testing.T) {
	t.Parallel()
	t.Run("negative", func(t *testing.T) {
		response := withUsage(textResponse("bad usage"), -1)
		model := &scriptedModel{responses: []*models.Response{response}}
		r, _ := agent.NewRunner(agent.Config{Model: model})
		result, err := r.Run(context.Background(), userReq("go"))
		if err == nil || !errors.Is(err, agent.ErrInvalidModelResponse) {
			t.Fatalf("err=%v", err)
		}
		if result.ObservedTotalTokens != 0 || result.LastResponse() == nil || len(result.Steps) != 1 {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("overflow", func(t *testing.T) {
		first := withUsage(toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}), math.MaxInt64-1)
		second := withUsage(toolCallResponse(models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)}), 10)
		model := &scriptedModel{responses: []*models.Response{first, second}}
		var executions int32
		tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
			atomic.AddInt32(&executions, 1)
			return agent.TextResult("ok"), nil
		})
		r, _ := agent.NewRunner(agent.Config{
			Model: model, Tools: []agent.Tool{tool}, Limits: agent.Limits{MaxObservedTokens: math.MaxInt64},
		})
		result, err := r.Run(context.Background(), userReq("go"))
		if err != nil {
			t.Fatal(err)
		}
		if result.StopReason != agent.StopMaxObservedTokens || result.ObservedTotalTokens != math.MaxInt64 {
			t.Fatalf("stop=%s observed=%d", result.StopReason, result.ObservedTotalTokens)
		}
		if atomic.LoadInt32(&executions) != 1 {
			t.Fatalf("executions=%d", executions)
		}
	})
}

func TestConcurrentRuns(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: make([]*models.Response, 20)}
	for i := range model.responses {
		model.responses[i] = textResponse("ok")
	}
	var toolCalls int32
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		atomic.AddInt32(&toolCalls, 1)
		return agent.TextResult("x"), nil
	})
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := r.Run(context.Background(), userReq("hi"))
			if err != nil || !result.Completed() {
				t.Errorf("err=%v result=%v", err, result)
			}
		}()
	}
	wg.Wait()
}

func TestInvalidToolResultEmpty(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
	}}
	tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	})
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	_, err := r.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, agent.ErrToolExecution) {
		t.Fatalf("err=%v", err)
	}
}

func TestSpecificToolChoiceMustExist(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{textResponse("x")}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	req := userReq("go")
	req.ToolChoice = models.SpecificTool("missing")
	if _, err := r.Run(context.Background(), req); err == nil {
		t.Fatal("expected invalid specific tool")
	}
}
