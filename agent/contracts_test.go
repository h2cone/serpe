package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/core/models"
)

func TestRunOutcomeErrorContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		run       func() (*agent.Result, error)
		stop      agent.StopReason
		completed bool
		errIs     error
		errAlsoIs error
		noResult  bool
		errKind   models.ErrorKind
	}{
		{
			name: "completed",
			run: func() (*agent.Result, error) {
				runner, _ := agent.NewRunner(agent.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopCompleted, completed: true,
		},
		{
			name: "controlled limit",
			run: func() (*agent.Result, error) {
				model := &scriptedModel{responses: []*models.Response{toolCallResponse(models.ToolCall{ID: "c", Name: "f", Arguments: json.RawMessage(`{}`)})}}
				runner, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}, Limits: agent.Limits{MaxModelTurns: 1}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopMaxModelTurns,
		},
		{
			name: "max tool calls",
			run: func() (*agent.Result, error) {
				model := &scriptedModel{responses: []*models.Response{toolCallResponse(
					models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)},
					models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)},
				)}}
				runner, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}, Limits: agent.Limits{MaxToolCalls: 1}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopMaxToolCalls,
		},
		{
			name: "max observed tokens",
			run: func() (*agent.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					withUsage(toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}), 100),
				}}
				runner, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}, Limits: agent.Limits{MaxObservedTokens: 50}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopMaxObservedTokens,
		},
		{
			name: "stalled",
			run: func() (*agent.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
					toolCallResponse(models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)}),
					toolCallResponse(models.ToolCall{ID: "3", Name: "f", Arguments: json.RawMessage(`{}`)}),
				}}
				runner, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}, Limits: agent.Limits{MaxIdenticalSteps: 3}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopStalled,
		},
		{
			name: "cancelled",
			run: func() (*agent.Result, error) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				runner, _ := agent.NewRunner(agent.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("unused")}}})
				return runner.Run(ctx, userReq("go"))
			},
			stop: agent.StopCancelled, errIs: context.Canceled,
		},
		{
			name: "closed before completion",
			run: func() (*agent.Result, error) {
				runner, _ := agent.NewRunner(agent.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("unused")}}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				_ = stream.Close()
				return stream.Result(), stream.Err()
			},
			stop: agent.StopCancelled, errIs: context.Canceled,
		},
		{
			name: "failed",
			run: func() (*agent.Result, error) {
				runner, _ := agent.NewRunner(agent.Config{Model: nilStreamModel{}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopFailed, errIs: agent.ErrInvalidModelResponse,
		},
		{
			name: "fatal tool error",
			run: func() (*agent.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
				}}
				tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
					return agent.ToolResult{}, errors.New("boom")
				})
				runner, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopFailed, errIs: agent.ErrToolExecution,
		},
		{
			name: "non-committable terminal",
			run: func() (*agent.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					terminalResponse(models.ResponseStatusIncomplete, models.FinishIncomplete, "incomplete", models.Text("partial")),
				}}
				runner, _ := agent.NewRunner(agent.Config{Model: model})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopFailed, errIs: agent.ErrModelResponse,
		},
		{
			name: "cancelled terminal",
			run: func() (*agent.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					terminalResponse(models.ResponseStatusCancelled, models.FinishCancelled, "cancelled", models.Text("partial")),
				}}
				runner, _ := agent.NewRunner(agent.Config{Model: model})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopCancelled, errIs: agent.ErrModelResponse, errAlsoIs: context.Canceled,
		},
		{
			name: "invalid request",
			run: func() (*agent.Result, error) {
				runner, _ := agent.NewRunner(agent.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}}})
				return runner.Run(context.Background(), &models.Request{})
			},
			noResult: true, errKind: models.ErrorInvalidRequest,
		},
		{
			name: "invalid config",
			run: func() (*agent.Result, error) {
				_, err := agent.NewRunner(agent.Config{})
				return nil, err
			},
			noResult: true, errIs: agent.ErrInvalidConfig,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.run()
			if test.noResult {
				if result != nil {
					t.Fatalf("result=%+v, want nil", result)
				}
				if err == nil {
					t.Fatal("expected error")
				}
				if test.errIs != nil && !errors.Is(err, test.errIs) {
					t.Fatalf("err=%v, want errors.Is(_, %v)", err, test.errIs)
				}
				if test.errKind != "" {
					var modelErr *models.Error
					if !errors.As(err, &modelErr) || modelErr.Kind != test.errKind {
						t.Fatalf("err=%v, want models.Error kind %q", err, test.errKind)
					}
				}
				return
			}
			if result == nil || result.StopReason != test.stop || result.Completed() != test.completed {
				t.Fatalf("result=%+v", result)
			}
			if test.errIs == nil && err != nil {
				t.Fatalf("err=%v", err)
			}
			if test.errIs != nil && !errors.Is(err, test.errIs) {
				t.Fatalf("err=%v, want errors.Is(_, %v)", err, test.errIs)
			}
			if test.errAlsoIs != nil && !errors.Is(err, test.errAlsoIs) {
				t.Fatalf("err=%v, want errors.Is(_, %v)", err, test.errAlsoIs)
			}
			if committable := err == nil && result.Completed(); committable != test.completed {
				t.Fatalf("committable=%v completed=%v err=%v", committable, test.completed, err)
			}
		})
	}
}

func TestAgentStreamTerminalTimingContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		newStream func(t *testing.T) agent.Stream
		wantLast  agent.EventKind
		stop      agent.StopReason
		errIs     error
	}{
		{
			name: "completed run_end precedes exhaustion",
			newStream: func(t *testing.T) agent.Stream {
				runner, _ := agent.NewRunner(agent.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				return stream
			},
			wantLast: agent.EventRunEnd, stop: agent.StopCompleted,
		},
		{
			name: "controlled stop run_end precedes exhaustion",
			newStream: func(t *testing.T) agent.Stream {
				model := &scriptedModel{responses: []*models.Response{
					toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
				}}
				runner, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}, Limits: agent.Limits{MaxModelTurns: 1}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				return stream
			},
			wantLast: agent.EventRunEnd, stop: agent.StopMaxModelTurns,
		},
		{
			name: "failure has no synthetic run_end",
			newStream: func(t *testing.T) agent.Stream {
				runner, _ := agent.NewRunner(agent.Config{Model: nilStreamModel{}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				return stream
			},
			wantLast: agent.EventRunStart, stop: agent.StopFailed, errIs: agent.ErrInvalidModelResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := test.newStream(t)
			defer stream.Close()
			var last agent.EventKind
			for stream.Next() {
				last = stream.Event().Kind
			}
			if last != test.wantLast {
				t.Fatalf("last event=%q want=%q", last, test.wantLast)
			}
			if test.errIs == nil && stream.Err() != nil {
				t.Fatalf("Err()=%v", stream.Err())
			}
			if test.errIs != nil && !errors.Is(stream.Err(), test.errIs) {
				t.Fatalf("Err()=%v", stream.Err())
			}
			if result := stream.Result(); result == nil || result.StopReason != test.stop {
				t.Fatalf("Result()=%+v", result)
			}
		})
	}
}

// TestEventPayloadContract locks the informal Kind→field table into an
// executable spec: each kind carries exactly its documented payload.
func TestEventPayloadContract(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{}`)}),
		textResponse("final"),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	stream, err := r.Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	seen := make(map[agent.EventKind]bool)
	for stream.Next() {
		ev := stream.Event()
		seen[ev.Kind] = true
		switch ev.Kind {
		case agent.EventRunStart:
			if ev.ModelTurn != 0 || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolCall != nil || ev.ToolResult != nil || ev.StopReason != "" {
				t.Fatalf("run_start payload: %+v", ev)
			}
		case agent.EventModelStart:
			if ev.ModelTurn == 0 || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolCall != nil || ev.ToolResult != nil || ev.StopReason != "" {
				t.Fatalf("model_start payload: %+v", ev)
			}
		case agent.EventModel:
			if ev.ModelTurn == 0 || ev.Model.Kind == "" || ev.ToolIndex != 0 || ev.Response != nil || ev.ToolCall != nil || ev.ToolResult != nil || ev.StopReason != "" {
				t.Fatalf("model_event payload: %+v", ev)
			}
		case agent.EventModelEnd:
			if ev.ModelTurn == 0 || ev.Response == nil || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.ToolCall != nil || ev.ToolResult != nil || ev.StopReason != "" {
				t.Fatalf("model_end payload: %+v", ev)
			}
		case agent.EventToolStart:
			if ev.ModelTurn == 0 || ev.ToolCall == nil || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolResult != nil || ev.StopReason != "" {
				t.Fatalf("tool_start payload: %+v", ev)
			}
		case agent.EventToolEnd:
			if ev.ModelTurn == 0 || ev.ToolCall == nil || ev.ToolResult == nil || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.StopReason != "" {
				t.Fatalf("tool_end payload: %+v", ev)
			}
		case agent.EventRunEnd:
			if ev.StopReason == "" || ev.ModelTurn != 0 || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolCall != nil || ev.ToolResult != nil {
				t.Fatalf("run_end payload: %+v", ev)
			}
		default:
			t.Fatalf("unexpected kind %q", ev.Kind)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []agent.EventKind{
		agent.EventRunStart, agent.EventModelStart, agent.EventModel, agent.EventModelEnd,
		agent.EventToolStart, agent.EventToolEnd, agent.EventRunEnd,
	} {
		if !seen[kind] {
			t.Fatalf("missing event kind %q", kind)
		}
	}
}

func TestResultLastResponseIsDerivedFromSteps(t *testing.T) {
	t.Parallel()
	runner, _ := agent.NewRunner(agent.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("final")}}})
	result, err := runner.Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 1 || result.LastResponse() != result.Steps[0].Response {
		t.Fatalf("LastResponse()=%p step response=%p", result.LastResponse(), result.Steps[0].Response)
	}
	if result.Text() != "final" {
		t.Fatalf("Text()=%q", result.Text())
	}
	var nilResult *agent.Result
	if nilResult.LastResponse() != nil {
		t.Fatal("nil Result returned a response")
	}
}
