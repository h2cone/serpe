package models

import (
	"encoding/json"
	"testing"
)

func BenchmarkStreamReducerText(b *testing.B) {
	events := []Event{
		{Kind: EventResponseStart, Response: &ResponseInfo{Provider: "bench"}},
		{Kind: EventPartStart, CandidateIndex: 0, PartIndex: 0, Part: Text("")},
		{Kind: EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: Delta{Kind: DeltaText, Text: "a small text delta"}},
		{Kind: EventPartEnd, CandidateIndex: 0, PartIndex: 0},
		{Kind: EventResponseEnd, Response: &ResponseInfo{Provider: "bench", Status: ResponseStatusCompleted}, Finishes: []CandidateFinish{{CandidateIndex: 0, Reason: FinishStop}}},
	}
	b.ReportAllocs()
	for b.Loop() {
		reducer := newReducer("bench")
		for _, event := range events {
			if err := reducer.apply(event); err != nil {
				b.Fatal(err)
			}
		}
		if reducer.result() == nil {
			b.Fatal("nil result")
		}
	}
}

func BenchmarkStreamReducerParallelTools(b *testing.B) {
	events := []Event{
		{Kind: EventResponseStart, Response: &ResponseInfo{Provider: "bench"}},
		{Kind: EventPartStart, CandidateIndex: 0, PartIndex: 0, Part: ToolCallContent("a", "first", nil)},
		{Kind: EventPartStart, CandidateIndex: 0, PartIndex: 1, Part: ToolCallContent("b", "second", nil)},
		{Kind: EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: Delta{Kind: DeltaToolArguments, Text: `{"x":`}},
		{Kind: EventPartDelta, CandidateIndex: 0, PartIndex: 1, Delta: Delta{Kind: DeltaToolArguments, Text: `{"y":`}},
		{Kind: EventPartDelta, CandidateIndex: 0, PartIndex: 0, Delta: Delta{Kind: DeltaToolArguments, Text: `1}`}},
		{Kind: EventPartDelta, CandidateIndex: 0, PartIndex: 1, Delta: Delta{Kind: DeltaToolArguments, Text: `2}`}},
		{Kind: EventPartEnd, CandidateIndex: 0, PartIndex: 0},
		{Kind: EventPartEnd, CandidateIndex: 0, PartIndex: 1},
		{Kind: EventResponseEnd, Response: &ResponseInfo{Provider: "bench", Status: ResponseStatusCompleted}, Finishes: []CandidateFinish{{CandidateIndex: 0, Reason: FinishToolCall}}},
	}
	b.ReportAllocs()
	for b.Loop() {
		reducer := newReducer("bench")
		for _, event := range events {
			if err := reducer.apply(event); err != nil {
				b.Fatal(err)
			}
		}
		if calls := reducer.result().ToolCalls(); len(calls) != 2 || !json.Valid(calls[0].Arguments) {
			b.Fatal("invalid result")
		}
	}
}
