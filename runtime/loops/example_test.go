package loops_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/runtime/loops"
)

// ExampleRunner demonstrates a fake model that calls a tool once, then answers.
func ExampleRunner() {
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{
			ID: "call_1", Name: "now", Arguments: json.RawMessage(`{}`),
		}),
		textResponse("It is 2026-08-04T12:00:00Z."),
	}}
	now := newStubTool("now", func(_ context.Context, _ json.RawMessage) (tools.Output, error) {
		return tools.Text("2026-08-04T12:00:00Z"), nil
	})
	exec, err := tools.New(tools.Config{}, now)
	if err != nil {
		panic(err)
	}
	runner, err := loops.New(loops.Config{Model: model, Tools: exec})
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
