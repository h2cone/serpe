package models_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func FuzzStreamReducer(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4})
	f.Add([]byte{0, 1, 2, 2, 3, 4, 9})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			data = data[:512]
		}
		events := make([]models.Event, 0, len(data))
		for index, value := range data {
			candidate := int(value>>5) % 3
			part := int(value>>2) % 4
			switch value % 5 {
			case 0:
				events = append(events, models.Event{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "fuzz"}})
			case 1:
				content := models.Text("")
				if value&0x80 != 0 {
					content = models.ToolCallContent("call", "tool", nil)
				}
				events = append(events, models.Event{Kind: models.EventPartStart, CandidateIndex: candidate, PartIndex: part, Part: content})
			case 2:
				delta := models.Delta{Kind: models.DeltaText, Text: string(rune('a' + index%26))}
				if value&0x80 != 0 {
					delta = models.Delta{Kind: models.DeltaToolArguments, Text: "{}"}
				}
				events = append(events, models.Event{Kind: models.EventPartDelta, CandidateIndex: candidate, PartIndex: part, Delta: delta})
			case 3:
				events = append(events, models.Event{Kind: models.EventPartEnd, CandidateIndex: candidate, PartIndex: part})
			case 4:
				events = append(events, models.Event{Kind: models.EventResponseEnd, Response: &models.ResponseInfo{Provider: "fuzz", Status: models.ResponseStatusCompleted}})
			}
		}
		stream := models.NewStream(context.Background(), &fuzzSource{events: events}, models.WithStreamProvider("fuzz"))
		for stream.Next() {
		}
		err := stream.Err()
		response := stream.Response()
		if err != nil {
			var modelErr *models.Error
			if !errors.As(err, &modelErr) || modelErr.Kind == "" {
				t.Fatalf("stream error is not normalized: %#v", err)
			}
			if response != nil {
				t.Fatalf("errored stream returned a response: %#v", response)
			}
		} else {
			validateFuzzResponse(t, response)
		}
		if err := stream.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

func validateFuzzResponse(t *testing.T, response *models.Response) {
	t.Helper()
	if response == nil || response.Provider == "" || response.Status == "" {
		t.Fatalf("successful stream returned invalid response metadata: %#v", response)
	}
	seen := make(map[int]struct{}, len(response.Candidates))
	for _, candidate := range response.Candidates {
		if _, exists := seen[candidate.Index]; exists {
			t.Fatalf("duplicate candidate index %d", candidate.Index)
		}
		seen[candidate.Index] = struct{}{}
		if candidate.FinishReason == "" {
			t.Fatalf("candidate %d has no finish reason", candidate.Index)
		}
		for _, content := range candidate.Content {
			if err := content.Validate(); err != nil {
				t.Fatalf("candidate %d has invalid content: %v", candidate.Index, err)
			}
		}
	}
}

type fuzzSource struct {
	events []models.Event
	index  int
}

func (s *fuzzSource) Next() (models.Event, error) {
	if s.index == len(s.events) {
		return models.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (*fuzzSource) Close() error { return nil }
