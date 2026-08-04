package models_test

import (
	"context"
	"errors"
	"testing"

	"github.com/h2cone/ouro/core/models"
)

func TestModelStreamTerminalTimingContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		source    models.EventSource
		wantError bool
	}{
		{
			name: "response appears only after terminal event is exhausted",
			source: &sliceSource{events: []models.Event{
				{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "fake"}},
				{Kind: models.EventResponseEnd, Response: &models.ResponseInfo{Provider: "fake", Status: models.ResponseStatusCompleted}},
			}},
		},
		{
			name:      "source error exposes Err and no Response",
			source:    &errorSource{err: errors.New("read failed")},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := models.NewStream(context.Background(), test.source, models.WithStreamProvider("fake"))
			defer stream.Close()
			for stream.Next() {
				if stream.Event().Kind == models.EventResponseEnd && stream.Response() != nil {
					t.Fatal("Response became visible before stream exhaustion")
				}
			}
			if !test.wantError {
				if stream.Err() != nil || stream.Response() == nil {
					t.Fatalf("Err()=%v Response()=%+v", stream.Err(), stream.Response())
				}
			} else if stream.Err() == nil || stream.Response() != nil {
				t.Fatalf("Err()=%v Response()=%+v", stream.Err(), stream.Response())
			}
		})
	}
}
