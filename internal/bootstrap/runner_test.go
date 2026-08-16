package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
)

func TestNewRunnerRequiresModel(t *testing.T) {
	runner, err := NewRunner(RunnerConfig{})
	if err == nil {
		t.Fatal("NewRunner with empty model succeeded; want error")
	}
	if runner != nil {
		t.Fatal("NewRunner returned non-nil runner alongside error")
	}
}

func TestNewRunnerBindsExplicitModel(t *testing.T) {
	runner, err := NewRunner(RunnerConfig{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("NewRunner with explicit model: %v", err)
	}
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}
}

func TestNewRunnerOmitsToolsWhenNoneProvided(t *testing.T) {
	runner, err := NewRunner(RunnerConfig{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatal(err)
	}
	if defs := runner.ToolDefinitions(); len(defs) != 0 {
		t.Fatalf("empty Tools exposed tools: %v", defs)
	}
}

func TestNewRunnerRegistersProvidedTools(t *testing.T) {
	runner, err := NewRunner(RunnerConfig{
		Model: "gpt-5.6-luna",
		Tools: []tools.Tool{namedTool("read"), namedTool("write")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defs := runner.ToolDefinitions()
	if len(defs) != 2 || defs[0].Name != "read" || defs[1].Name != "write" {
		t.Fatalf("provided tools=%v", defs)
	}
}

type namedTool string

func (n namedTool) Definition() models.Tool {
	return models.NewTool(string(n), string(n), json.RawMessage(`{"type":"object","properties":{}}`))
}

func (namedTool) Execute(context.Context, tools.Invocation) (tools.Output, error) {
	return tools.Text("ok"), nil
}
