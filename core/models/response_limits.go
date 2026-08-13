package models

import (
	"context"
	"io"
)

// ApplyStreamLimitsToResponse validates and re-reduces a unary response under
// the same hard limits used for streaming. It returns a detached response.
func ApplyStreamLimitsToResponse(ctx context.Context, response *Response, limits StreamLimits) (*Response, error) {
	if response == nil {
		return nil, responseLimitError("", "unary model returned a nil response", nil)
	}
	if len(response.Candidates) > maxTerminalFinishes {
		return nil, responseLimitError(response.Provider, "unary model returned too many candidates", nil)
	}
	stream := NewStream(ctx, &responseEventSource{response: response},
		WithStreamProvider(response.Provider), WithStreamLimits(limits))
	defer stream.Close()
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return stream.Response(), nil
}

type responseEventSource struct {
	response  *Response
	phase     uint8
	candidate int
	part      int
}

func (s *responseEventSource) Next() (Event, error) {
	if s.phase == 0 {
		s.phase = 1
		return Event{Kind: EventResponseStart, Response: s.responseInfo()}, nil
	}
	if s.phase == 1 || s.phase == 2 {
		for s.candidate < len(s.response.Candidates) {
			candidate := &s.response.Candidates[s.candidate]
			if s.part >= len(candidate.Content) {
				s.candidate++
				s.part = 0
				continue
			}
			if s.phase == 1 {
				s.phase = 2
				return Event{Kind: EventPartStart, CandidateIndex: candidate.Index, PartIndex: s.part, Part: candidate.Content[s.part]}, nil
			}
			s.phase = 1
			event := Event{Kind: EventPartEnd, CandidateIndex: candidate.Index, PartIndex: s.part}
			s.part++
			return event, nil
		}
		s.phase = 3
		finishes := make([]CandidateFinish, len(s.response.Candidates))
		for index := range s.response.Candidates {
			candidate := &s.response.Candidates[index]
			finishes[index] = CandidateFinish{CandidateIndex: candidate.Index, Reason: candidate.FinishReason,
				RawReason: candidate.RawFinishReason, ProviderState: candidate.ProviderState}
		}
		usage := s.response.Usage
		return Event{Kind: EventResponseEnd, Response: s.responseInfo(), Finishes: finishes, Usage: &usage}, nil
	}
	if s.phase == 3 {
		s.phase = 4
		return Event{}, io.EOF
	}
	return Event{}, io.EOF
}

func (s *responseEventSource) responseInfo() *ResponseInfo {
	return &ResponseInfo{
		Provider: s.response.Provider, ID: s.response.ID, Model: s.response.Model,
		Status: s.response.Status, RequestID: s.response.RequestID, CreatedAt: s.response.CreatedAt,
		Metadata: s.response.Metadata, ProviderState: s.response.ProviderState,
	}
}

func (*responseEventSource) Close() error { return nil }
