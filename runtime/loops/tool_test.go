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

type stubTool struct {
	name string
	def  models.Tool
	fn   func(ctx context.Context, arguments json.RawMessage) (tools.Output, error)
}

func newStubTool(name string, fn func(ctx context.Context, arguments json.RawMessage) (tools.Output, error)) *stubTool {
	return &stubTool{
		name: name,
		def:  models.NewTool(name, name+" tool", json.RawMessage(`{"type":"object","properties":{}}`)),
		fn:   fn,
	}
}

func (t *stubTool) Definition() models.Tool { return t.def }

func (t *stubTool) Execute(ctx context.Context, in tools.Invocation) (tools.Output, error) {
	if t.fn == nil {
		return tools.Text("ok"), nil
	}
	return t.fn(ctx, in.Arguments)
}

func mustTools(t testing.TB, ts ...tools.Tool) *tools.Executor {
	t.Helper()
	e, err := tools.New(tools.Config{}, ts...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNewConfigValidation(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{}

	if _, err := loops.New(loops.Config{}); err == nil || !errors.Is(err, loops.ErrInvalidConfig) {
		t.Fatalf("nil model: %v", err)
	}
	if _, err := loops.New(loops.Config{Model: model, Limits: loops.Limits{MaxModelTurns: -1}}); err == nil || !errors.Is(err, loops.ErrInvalidConfig) {
		t.Fatalf("negative limits: %v", err)
	}
	r, err := loops.New(loops.Config{Model: model})
	if err != nil {
		t.Fatalf("empty tools: %v", err)
	}
	if r == nil {
		t.Fatal("expected runner")
	}
}

func TestTextAndErrorResult(t *testing.T) {
	t.Parallel()
	ok := tools.Text("hi")
	if ok.IsError || len(ok.Content) != 1 || ok.Content[0].Text.Text != "hi" {
		t.Fatalf("Text: %+v", ok)
	}
	er := tools.Error("nope")
	if !er.IsError || er.Content[0].Text.Text != "nope" {
		t.Fatalf("Error: %+v", er)
	}
}
