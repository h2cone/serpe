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
			name: "failed",
			run: func() (*agent.Result, error) {
				runner, _ := agent.NewRunner(agent.Config{Model: nilStreamModel{}})
				return runner.Run(context.Background(), userReq("go"))
			},
			stop: agent.StopFailed, errIs: agent.ErrInvalidModelResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.run()
			if result == nil || result.StopReason != test.stop || result.Completed() != test.completed {
				t.Fatalf("result=%+v", result)
			}
			if test.errIs == nil && err != nil {
				t.Fatalf("err=%v", err)
			}
			if test.errIs != nil && !errors.Is(err, test.errIs) {
				t.Fatalf("err=%v, want errors.Is(_, %v)", err, test.errIs)
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
