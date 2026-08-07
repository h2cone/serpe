package agent_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/core/models"
)

// ExampleRunner demonstrates a fake model that calls a tool once, then answers.
func ExampleRunner() {
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{
			ID: "call_1", Name: "now", Arguments: json.RawMessage(`{}`),
		}),
		textResponse("It is 2026-08-04T12:00:00Z."),
	}}
	now := newStubTool("now", func(_ context.Context, _ json.RawMessage) (agent.ToolOutput, error) {
		return agent.TextResult("2026-08-04T12:00:00Z"), nil
	})
	runner, err := agent.NewRunner(agent.Config{Model: model, Tools: []agent.Tool{now}})
	if err != nil {
		panic(err)
	}
	result, err := runner.Run(context.Background(), models.NewTextRequest("What time is it?"))
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Completed())
	fmt.Println(result.Text())
	// Output:
	// true
	// It is 2026-08-04T12:00:00Z.
}
