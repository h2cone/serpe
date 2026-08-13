package loops_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/runtime/loops"
)

func TestSerialToolBatchCharacterization(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(
			models.ToolCall{ID: "c0", Name: "a", Arguments: json.RawMessage(`{"n":0}`)},
			models.ToolCall{ID: "c1", Name: "b", Arguments: json.RawMessage(`{"n":1}`)},
		),
		textResponse("done"),
	}}
	var order []string
	makeTool := func(name string) tools.Tool {
		return newStubTool(name, func(_ context.Context, _ json.RawMessage) (tools.Output, error) {
			order = append(order, name)
			return tools.Text(name + "-out"), nil
		})
	}
	exec := mustTools(t, makeTool("a"), makeTool("b"))
	runner, err := loops.New(loops.Config{Model: model, Tools: exec})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := runner.Stream(context.Background(), userReq("go"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	type toolEv struct {
		kind  loops.EventKind
		index int
		id    string
		name  string
	}
	var seen []toolEv
	for stream.Next() {
		ev := stream.Event()
		switch ev.Kind {
		case loops.EventToolStart, loops.EventToolEnd:
			if ev.ToolCall == nil {
				t.Fatalf("%s missing ToolCall", ev.Kind)
			}
			seen = append(seen, toolEv{ev.Kind, ev.ToolIndex, ev.ToolCall.ID, ev.ToolCall.Name})
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	want := []toolEv{
		{loops.EventToolStart, 0, "c0", "a"},
		{loops.EventToolEnd, 0, "c0", "a"},
		{loops.EventToolStart, 1, "c1", "b"},
		{loops.EventToolEnd, 1, "c1", "b"},
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("tool events=%v want=%v", seen, want)
	}
	if got := slices.Clip(order); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("execute order=%v", got)
	}

	result := stream.Result()
	if len(result.Steps) != 2 || len(result.Steps[0].ToolResults) != 2 {
		t.Fatalf("steps=%+v", result.Steps)
	}
	if result.Steps[0].ToolResults[0].ToolResult.CallID != "c0" || result.Steps[0].ToolResults[1].ToolResult.CallID != "c1" {
		t.Fatalf("step results out of call order: %+v", result.Steps[0].ToolResults)
	}
	var resultMsg *models.Message
	for i := range result.Transcript {
		msg := &result.Transcript[i]
		if msg.Role == models.RoleUser && len(msg.Content) > 0 && msg.Content[0].Kind == models.ContentToolResult {
			resultMsg = msg
			break
		}
	}
	if resultMsg == nil || len(resultMsg.Content) != 2 {
		t.Fatalf("tool-result message=%+v", resultMsg)
	}
	if resultMsg.Content[0].ToolResult.CallID != "c0" || resultMsg.Content[1].ToolResult.CallID != "c1" {
		t.Fatalf("transcript results out of call order: %+v", resultMsg.Content)
	}
}

func TestFatalToolErrorClassification(t *testing.T) {
	t.Parallel()
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}),
	}}
	boom := newStubTool("f", func(_ context.Context, _ json.RawMessage) (tools.Output, error) {
		return tools.Output{}, errors.New("boom")
	})
	exec := mustTools(t, boom)
	runner, err := loops.New(loops.Config{Model: model, Tools: exec})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), userReq("go"))
	if err == nil || !errors.Is(err, loops.ErrToolExecution) {
		t.Fatalf("err=%v", err)
	}
	if result == nil || result.StopReason != loops.StopFailed {
		t.Fatalf("result=%+v", result)
	}
	for _, msg := range result.Transcript {
		for _, c := range msg.Content {
			if c.Kind == models.ContentToolResult {
				t.Fatalf("fatal path committed a tool result: %+v", msg)
			}
		}
	}
}
