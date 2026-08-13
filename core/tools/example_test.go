package tools_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
)

type echoTool struct{}

func (echoTool) Definition() models.Tool {
	return models.NewTool("echo", "Echo arguments as text.", json.RawMessage(`{"type":"object","properties":{}}`))
}

func (echoTool) Execute(_ context.Context, in tools.Invocation) (tools.Output, error) {
	return tools.Text(string(in.Arguments)), nil
}

func (echoTool) Plan(context.Context, tools.Invocation) (tools.Plan, error) {
	return tools.Plan{Claims: nil}, nil // explicit empty claims: may run in parallel
}

func ExampleExecutor() {
	exec, err := tools.New(tools.Config{}, echoTool{})
	if err != nil {
		panic(err)
	}
	batch, err := exec.Start(context.Background(), []models.ToolCall{{
		ID: "1", Name: "echo", Arguments: json.RawMessage(`{"ok":true}`),
	}})
	if err != nil {
		panic(err)
	}
	defer batch.Close()
	for batch.Next() {
		ev := batch.Event()
		if ev.Kind == tools.BatchFinished && ev.Output != nil {
			fmt.Println(ev.Output.Content[0].Text.Text)
		}
	}
	// Output:
	// {"ok":true}
}
