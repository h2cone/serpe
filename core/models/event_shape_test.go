package models

import "testing"

func TestEventShapeByKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		valid   Event
		invalid Event
	}{
		{
			name:    "response_start",
			valid:   Event{Kind: EventResponseStart, Response: &ResponseInfo{Provider: "test"}},
			invalid: Event{Kind: EventResponseStart, Response: &ResponseInfo{Provider: "test"}, Part: Text("dirty")},
		},
		{
			name:    "part_start",
			valid:   Event{Kind: EventPartStart, CandidateIndex: 0, PartIndex: 0, Part: Text("")},
			invalid: Event{Kind: EventPartStart, CandidateIndex: 0, PartIndex: 0, Part: Text(""), Delta: Delta{Kind: DeltaText}},
		},
		{
			name:    "part_delta",
			valid:   Event{Kind: EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: Delta{Kind: DeltaText, Text: "x"}},
			invalid: Event{Kind: EventPartDelta, CandidateIndex: 0, PartIndex: 0, Part: Text("dirty"), Delta: Delta{Kind: DeltaText}},
		},
		{
			name:    "part_end",
			valid:   Event{Kind: EventPartEnd, CandidateIndex: 0, PartIndex: 0},
			invalid: Event{Kind: EventPartEnd, CandidateIndex: 0, PartIndex: 0, Delta: Delta{Kind: DeltaText}},
		},
		{
			name:    "response_end",
			valid:   Event{Kind: EventResponseEnd, Response: &ResponseInfo{Provider: "test", Status: ResponseStatusCompleted}},
			invalid: Event{Kind: EventResponseEnd, Response: &ResponseInfo{Provider: "test"}, CallID: "dirty"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateEventShape(test.valid); err != nil {
				t.Fatalf("valid shape: %v", err)
			}
			if err := validateEventShape(test.invalid); err == nil {
				t.Fatal("invalid shape was accepted")
			}
		})
	}
}
