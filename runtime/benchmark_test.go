package runtime_test

import (
	"context"
	"testing"

	"github.com/h2cone/serpe/runtime"
	"github.com/h2cone/serpe/core/models"
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

func BenchmarkRunLongTranscript(b *testing.B) {
	request := &models.Request{Messages: make([]models.Message, 256)}
	for i := range request.Messages {
		request.Messages[i] = models.NewUserMessage(models.Text("long transcript message"))
	}
	runner, err := runtime.NewRunner(runtime.Config{Model: repeatModel{response: textResponse("done")}})
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
