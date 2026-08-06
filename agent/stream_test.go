package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/core/models"
)

func TestStreamEventOrder(t *testing.T) {
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

	var kinds []agent.EventKind
	for stream.Next() {
		kinds = append(kinds, stream.Event().Kind)
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	want := []agent.EventKind{
		agent.EventRunStart,
		agent.EventModelStart,
		agent.EventModel,
		agent.EventModel,
		agent.EventModel,
		agent.EventModel,
		agent.EventModelEnd,
		agent.EventToolStart,
		agent.EventToolEnd,
		agent.EventModelStart,
		agent.EventModel,
		agent.EventModel,
		agent.EventModel,
		agent.EventModel,
		agent.EventModelEnd,
		agent.EventRunEnd,
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds=%v want=%v", kinds, want)
	}
}

func TestRunStreamEquivalence(t *testing.T) {
	t.Parallel()
	responses := []*models.Response{
		toolCallResponse(models.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}),
		textResponse("answer"),
	}
	newRunner := func() *agent.Runner {
		// Fresh model each time with same script.
		m := &scriptedModel{responses: cloneResponses(responses)}
		r, err := agent.NewRunner(agent.Config{Model: m, Tools: []agent.Tool{newStubTool("f", nil)}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	runResult, err := newRunner().Run(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}

	stream, err := newRunner().Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	streamResult := stream.Result()
	_ = stream.Close()

	if runResult.StopReason != streamResult.StopReason {
		t.Fatalf("stop %s vs %s", runResult.StopReason, streamResult.StopReason)
	}
	if len(runResult.Transcript) != len(streamResult.Transcript) {
		t.Fatalf("transcript len %d vs %d", len(runResult.Transcript), len(streamResult.Transcript))
	}
	if len(runResult.Steps) != len(streamResult.Steps) {
		t.Fatalf("steps %d vs %d", len(runResult.Steps), len(streamResult.Steps))
	}
	if runResult.Text() != streamResult.Text() {
		t.Fatalf("text %q vs %q", runResult.Text(), streamResult.Text())
	}
}

func cloneResponses(in []*models.Response) []*models.Response {
	out := make([]*models.Response, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

func TestEventDefensiveCopy(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"x":1}`)}),
		textResponse("done"),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	stream, err := r.Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for stream.Next() {
		ev := stream.Event()
		if ev.ToolCall != nil {
			ev.ToolCall.Arguments[0] = 'X'
		}
		if ev.ToolResult != nil && len(ev.ToolResult.Content) > 0 && ev.ToolResult.Content[0].Text != nil {
			ev.ToolResult.Content[0].Text.Text = "mut"
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	result := stream.Result()
	// Tool result in transcript should be intact.
	for _, msg := range result.Transcript {
		for _, c := range msg.Content {
			if c.Kind == models.ContentToolResult && c.ToolResult.Content[0].Text.Text == "mut" {
				t.Fatal("event mutation leaked")
			}
		}
	}
}

func TestCloseIdempotent(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{textResponse("x")}}
	r, _ := agent.NewRunner(agent.Config{Model: model})
	stream, err := r.Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if stream.Next() {
		t.Fatal("Next after Close")
	}
}

type nilStreamModel struct{}

func (nilStreamModel) Complete(context.Context, *models.Request) (*models.Response, error) {
	return nil, errors.New("not implemented")
}

func (nilStreamModel) Stream(context.Context, *models.Request) (models.Stream, error) {
	return nil, nil
}

func TestNilModelStreamFailsWithoutPanic(t *testing.T) {
	t.Parallel()
	r, _ := agent.NewRunner(agent.Config{Model: nilStreamModel{}})
	result, err := r.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, agent.ErrInvalidModelResponse) {
		t.Fatalf("err=%v", err)
	}
	if result == nil || result.StopReason != agent.StopFailed {
		t.Fatalf("result=%+v", result)
	}
}

type delayedStreamModel struct {
	started chan struct{}
	release chan struct{}
	stream  *closeTrackingModelStream
}

func (m *delayedStreamModel) Complete(context.Context, *models.Request) (*models.Response, error) {
	return nil, errors.New("not implemented")
}

func (m *delayedStreamModel) Stream(context.Context, *models.Request) (models.Stream, error) {
	close(m.started)
	<-m.release
	return m.stream, nil
}

type closeTrackingModelStream struct {
	closes atomic.Int32
}

func (*closeTrackingModelStream) Next() bool                 { return false }
func (*closeTrackingModelStream) Event() models.Event        { return models.Event{} }
func (*closeTrackingModelStream) Text() string               { return "" }
func (*closeTrackingModelStream) Err() error                 { return nil }
func (*closeTrackingModelStream) Response() *models.Response { return nil }
func (s *closeTrackingModelStream) Close() error {
	s.closes.Add(1)
	return nil
}

func TestCloseDuringModelStreamAcquisitionClosesReturnedStream(t *testing.T) {
	t.Parallel()
	returned := &closeTrackingModelStream{}
	model := &delayedStreamModel{
		started: make(chan struct{}),
		release: make(chan struct{}),
		stream:  returned,
	}
	r, _ := agent.NewRunner(agent.Config{Model: model})
	stream, err := r.Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Next() || stream.Event().Kind != agent.EventRunStart {
		t.Fatal("missing run_start")
	}

	done := make(chan bool, 1)
	go func() { done <- stream.Next() }()
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model stream acquisition did not start")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	close(model.release)
	select {
	case next := <-done:
		if next {
			t.Fatal("Next succeeded after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not finish")
	}
	if returned.closes.Load() != 1 {
		t.Fatalf("returned stream closed %d times", returned.closes.Load())
	}
}

func TestCancelContext(t *testing.T) {
	t.Parallel()
	// Blocking tool waits on ctx.
	started := make(chan struct{})
	tool := newStubTool("f", func(ctx context.Context, _ json.RawMessage) (agent.ToolResult, error) {
		close(started)
		<-ctx.Done()
		return agent.ToolResult{}, ctx.Err()
	})
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{tool}})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := r.Stream(ctx, userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	done := make(chan struct{})
	go func() {
		for stream.Next() {
		}
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not finish after cancel")
	}
	if err := stream.Err(); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	result := stream.Result()
	if result.StopReason != agent.StopCancelled {
		t.Fatalf("stop=%s", result.StopReason)
	}
}

func TestModelTurnAndToolIndex(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(
			models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)},
			models.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)},
		),
		textResponse("ok"),
	}}
	r, _ := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{newStubTool("f", nil)}})
	stream, err := r.Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var toolStarts []int
	var modelStarts []int
	for stream.Next() {
		ev := stream.Event()
		switch ev.Kind {
		case agent.EventModelStart:
			modelStarts = append(modelStarts, ev.ModelTurn)
		case agent.EventToolStart:
			toolStarts = append(toolStarts, ev.ToolIndex)
			if ev.ModelTurn != 1 {
				t.Fatalf("tool turn=%d", ev.ModelTurn)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if len(modelStarts) != 2 || modelStarts[0] != 1 || modelStarts[1] != 2 {
		t.Fatalf("modelStarts=%v", modelStarts)
	}
	if len(toolStarts) != 2 || toolStarts[0] != 0 || toolStarts[1] != 1 {
		t.Fatalf("toolStarts=%v", toolStarts)
	}
}

func TestNilContextUsesBackground(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{textResponse("ok")}}
	r, _ := agent.NewRunner(agent.Config{Model: model})
	// intentional nil context per API contract
	result, err := r.Run(nil, userReq("hi")) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() {
		t.Fatal(result.StopReason)
	}
}
