package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/core/models"
)

type stubTool struct {
	name string
	def  models.Tool
	fn   func(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error)
}

func newStubTool(name string, fn func(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error)) *stubTool {
	return &stubTool{
		name: name,
		def:  models.NewTool(name, name+" tool", json.RawMessage(`{"type":"object","properties":{}}`)),
		fn:   fn,
	}
}

func (t *stubTool) Definition() models.Tool { return t.def }

func (t *stubTool) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	if t.fn == nil {
		return agent.TextResult("ok"), nil
	}
	return t.fn(ctx, arguments)
}

func TestNewConfigValidation(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{}

	if _, err := agent.New(agent.Config{}); err == nil || !errors.Is(err, agent.ErrInvalidConfig) {
		t.Fatalf("nil model: %v", err)
	}
	if _, err := agent.New(agent.Config{Model: model, Tools: []agent.Tool{nil}}); err == nil || !errors.Is(err, agent.ErrInvalidConfig) {
		t.Fatalf("nil tool: %v", err)
	}
	bad := newStubTool("x", nil)
	bad.def = models.Tool{Name: ""}
	if _, err := agent.New(agent.Config{Model: model, Tools: []agent.Tool{bad}}); err == nil || !errors.Is(err, agent.ErrInvalidConfig) {
		t.Fatalf("invalid definition: %v", err)
	}
	if _, err := agent.New(agent.Config{
		Model: model,
		Tools: []agent.Tool{newStubTool("a", nil), newStubTool("a", nil)},
	}); err == nil || !errors.Is(err, agent.ErrInvalidConfig) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := agent.New(agent.Config{Model: model, Limits: agent.Limits{MaxModelTurns: -1}}); err == nil || !errors.Is(err, agent.ErrInvalidConfig) {
		t.Fatalf("negative limits: %v", err)
	}
	r, err := agent.New(agent.Config{Model: model})
	if err != nil {
		t.Fatalf("empty tools: %v", err)
	}
	if r == nil {
		t.Fatal("expected runner")
	}
}

func TestTextAndErrorResult(t *testing.T) {
	t.Parallel()
	ok := agent.TextResult("hi")
	if ok.IsError || len(ok.Content) != 1 || ok.Content[0].Text.Text != "hi" {
		t.Fatalf("TextResult: %+v", ok)
	}
	er := agent.ErrorResult("nope")
	if !er.IsError || er.Content[0].Text.Text != "nope" {
		t.Fatalf("ErrorResult: %+v", er)
	}
}
