package loops_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/runtime/loops"
)

type repeatModel struct {
	response *models.Response
}

func (m repeatModel) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	stream, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Next() {
	}
	return stream.Response(), stream.Err()
}

func (m repeatModel) Stream(ctx context.Context, _ *models.Request) (models.Stream, error) {
	return models.NewStream(ctx, &sliceSource{events: eventsFromResponse(m.response)}), nil
}

func BenchmarkSerialToolBatch(b *testing.B) {
	request := userReq("go")
	toolset := mustTools(b,
		newStubTool("a", nil),
		newStubTool("b", nil),
		newStubTool("c", nil),
		newStubTool("d", nil),
	)
	newRunner := func() *loops.Runner {
		model := &scriptedModel{responses: []*models.Response{
			toolCallResponse(
				models.ToolCall{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)},
				models.ToolCall{ID: "2", Name: "b", Arguments: json.RawMessage(`{}`)},
				models.ToolCall{ID: "3", Name: "c", Arguments: json.RawMessage(`{}`)},
				models.ToolCall{ID: "4", Name: "d", Arguments: json.RawMessage(`{}`)},
			),
			textResponse("done"),
		}}
		runner, err := loops.New(loops.Config{Model: model, Tools: toolset})
		if err != nil {
			b.Fatal(err)
		}
		return runner
	}
	b.ReportAllocs()
	for b.Loop() {
		result, err := newRunner().Run(context.Background(), request)
		if err != nil || !result.Completed() {
			b.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func BenchmarkRunLongTranscript(b *testing.B) {
	request := &models.Request{Messages: make([]models.Message, 256)}
	for i := range request.Messages {
		request.Messages[i] = models.NewUserMessage(models.Text("long transcript message"))
	}
	runner, err := loops.New(loops.Config{Model: repeatModel{response: textResponse("done")}})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		result, runErr := runner.Run(context.Background(), request)
		if runErr != nil || !result.Completed() {
			b.Fatalf("result=%+v err=%v", result, runErr)
		}
	}
}
