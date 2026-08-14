package loops_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/runtime/loops"
)

func TestRunOutcomeErrorContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		run       func() (*loops.Result, error)
		stop      loops.StopReason
		completed bool
		errIs     error
		errAlsoIs error
		noResult  bool
		errKind   models.ErrorKind
	}{
		{
			name: "completed",
			run: func() (*loops.Result, error) {
				runner, _ := loops.New(loops.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopCompleted, completed: true,
		},
		{
			name: "controlled limit",
			run: func() (*loops.Result, error) {
				model := &scriptedModel{responses: []*models.Response{toolCallResponse(models.ToolCall{ID: "c", Name: "f", Arguments: json.RawMessage(`{}`)})}}
				runner, _ := loops.New(loops.Config{Model: model, Tools: mustTools(t, newStubTool("f", nil)), Limits: loops.Limits{MaxModelTurns: 1}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopMaxModelTurns,
		},
		{
			name: "max tool calls",
			run: func() (*loops.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
					toolCallResponse(models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)}),
				}}
				runner, _ := loops.New(loops.Config{Model: model, Tools: mustTools(t, newStubTool("f", nil)), Limits: loops.Limits{MaxToolCalls: 1}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopMaxToolCalls,
		},
		{
			name: "max observed tokens",
			run: func() (*loops.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					withUsage(toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}), 100),
				}}
				runner, _ := loops.New(loops.Config{Model: model, Tools: mustTools(t, newStubTool("f", nil)), Limits: loops.Limits{MaxObservedTokens: 50}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopMaxObservedTokens,
		},
		{
			name: "stalled",
			run: func() (*loops.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
					toolCallResponse(models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)}),
					toolCallResponse(models.ToolCall{ID: "3", Name: "f", Arguments: json.RawMessage(`{}`)}),
				}}
				runner, _ := loops.New(loops.Config{Model: model, Tools: mustTools(t, newStubTool("f", nil)), Limits: loops.Limits{MaxIdenticalSteps: 3}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopStalled,
		},
		{
			name: "cancelled",
			run: func() (*loops.Result, error) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				runner, _ := loops.New(loops.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("unused")}}})
				return runner.Run(ctx, userReq("go"))
			},
			stop: loops.StopCancelled, errIs: context.Canceled,
		},
		{
			name: "closed before completion",
			run: func() (*loops.Result, error) {
				runner, _ := loops.New(loops.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("unused")}}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				_ = stream.Close()
				return stream.Result(), stream.Err()
			},
			stop: loops.StopCancelled, errIs: context.Canceled,
		},
		{
			name: "failed",
			run: func() (*loops.Result, error) {
				runner, _ := loops.New(loops.Config{Model: nilStreamModel{}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopFailed, errIs: loops.ErrInvalidModelResponse,
		},
		{
			name: "fatal tool error",
			run: func() (*loops.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
				}}
				tool := newStubTool("f", func(_ context.Context, _ json.RawMessage) (tools.Output, error) {
					return tools.Output{}, errors.New("boom")
				})
				runner, _ := loops.New(loops.Config{Model: model, Tools: mustTools(t, tool)})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopFailed, errIs: loops.ErrToolExecution,
		},
		{
			name: "non-committable terminal",
			run: func() (*loops.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					terminalResponse(models.ResponseStatusIncomplete, models.FinishIncomplete, "incomplete", models.Text("partial")),
				}}
				runner, _ := loops.New(loops.Config{Model: model})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopFailed, errIs: loops.ErrModelResponse,
		},
		{
			name: "cancelled terminal",
			run: func() (*loops.Result, error) {
				model := &scriptedModel{responses: []*models.Response{
					terminalResponse(models.ResponseStatusCancelled, models.FinishCancelled, "cancelled", models.Text("partial")),
				}}
				runner, _ := loops.New(loops.Config{Model: model})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: loops.StopCancelled, errIs: loops.ErrModelResponse, errAlsoIs: context.Canceled,
		},
		{
			name: "invalid request",
			run: func() (*loops.Result, error) {
				runner, _ := loops.New(loops.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}}})
				return runner.Run(context.Background(), &models.Request{})
			},
			noResult: true, errKind: models.ErrorInvalidRequest,
		},
		{
			name: "invalid config",
			run: func() (*loops.Result, error) {
				_, err := loops.New(loops.Config{})
				return nil, err
			},
			noResult: true, errIs: loops.ErrInvalidConfig,
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
		newStream func(t *testing.T) loops.Stream
		wantLast  loops.EventKind
		stop      loops.StopReason
		errIs     error
	}{
		{
			name: "completed run_end precedes exhaustion",
			newStream: func(t *testing.T) loops.Stream {
				runner, _ := loops.New(loops.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				return stream
			},
			wantLast: loops.EventRunEnd, stop: loops.StopCompleted,
		},
		{
			name: "controlled stop run_end precedes exhaustion",
			newStream: func(t *testing.T) loops.Stream {
				model := &scriptedModel{responses: []*models.Response{
					toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
				}}
				runner, _ := loops.New(loops.Config{Model: model, Tools: mustTools(t, newStubTool("f", nil)), Limits: loops.Limits{MaxModelTurns: 1}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				return stream
			},
			wantLast: loops.EventRunEnd, stop: loops.StopMaxModelTurns,
		},
		{
			name: "failure has no synthetic run_end",
			newStream: func(t *testing.T) loops.Stream {
				runner, _ := loops.New(loops.Config{Model: nilStreamModel{}})
				stream, err := runner.Stream(context.Background(), userReq("go"))
				if err != nil {
					t.Fatal(err)
				}
				return stream
			},
			wantLast: loops.EventRunStart, stop: loops.StopFailed, errIs: loops.ErrInvalidModelResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := test.newStream(t)
			defer stream.Close()
			var last loops.EventKind
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
	r, _ := loops.New(loops.Config{Model: model, Tools: mustTools(t, newStubTool("f", nil))})
	stream, err := r.Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	seen := make(map[loops.EventKind]bool)
	for stream.Next() {
		ev := stream.Event()
		seen[ev.Kind] = true
		switch ev.Kind {
		case loops.EventRunStart:
			if ev.ModelTurn != 0 || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolCall != nil || ev.ToolOutput != nil || ev.StopReason != "" {
				t.Fatalf("run_start payload: %+v", ev)
			}
		case loops.EventModelStart:
			if ev.ModelTurn == 0 || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolCall != nil || ev.ToolOutput != nil || ev.StopReason != "" {
				t.Fatalf("model_start payload: %+v", ev)
			}
		case loops.EventModel:
			if ev.ModelTurn == 0 || ev.Model.Kind == "" || ev.ToolIndex != 0 || ev.Response != nil || ev.ToolCall != nil || ev.ToolOutput != nil || ev.StopReason != "" {
				t.Fatalf("model_event payload: %+v", ev)
			}
		case loops.EventModelEnd:
			if ev.ModelTurn == 0 || ev.Response == nil || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.ToolCall != nil || ev.ToolOutput != nil || ev.StopReason != "" {
				t.Fatalf("model_end payload: %+v", ev)
			}
		case loops.EventToolStart:
			if ev.ModelTurn == 0 || ev.ToolCall == nil || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolOutput != nil || ev.StopReason != "" {
				t.Fatalf("tool_start payload: %+v", ev)
			}
		case loops.EventToolEnd:
			if ev.ModelTurn == 0 || ev.ToolCall == nil || ev.ToolOutput == nil || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.StopReason != "" {
				t.Fatalf("tool_end payload: %+v", ev)
			}
		case loops.EventRunEnd:
			if ev.StopReason == "" || ev.ModelTurn != 0 || ev.ToolIndex != 0 || ev.Model.Kind != "" || ev.Response != nil || ev.ToolCall != nil || ev.ToolOutput != nil {
				t.Fatalf("run_end payload: %+v", ev)
			}
		default:
			t.Fatalf("unexpected kind %q", ev.Kind)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []loops.EventKind{
		loops.EventRunStart, loops.EventModelStart, loops.EventModel, loops.EventModelEnd,
		loops.EventToolStart, loops.EventToolEnd, loops.EventRunEnd,
	} {
		if !seen[kind] {
			t.Fatalf("missing event kind %q", kind)
		}
	}
}

func TestResultLastResponseIsDerivedFromSteps(t *testing.T) {
	t.Parallel()
	runner, _ := loops.New(loops.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("final")}}})
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
	var nilResult *loops.Result
	if nilResult.LastResponse() != nil {
		t.Fatal("nil Result returned a response")
	}
}
