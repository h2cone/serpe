package runtime_test

import (
	"context"
	"io"
	"sync"

	"github.com/h2cone/serpe/core/models"
)

// scriptedModel implements models.Model with a fixed sequence of responses.
type scriptedModel struct {
	mu        sync.Mutex
	responses []*models.Response
	errs      []error
	calls     int
	requests  []*models.Request
	// optional per-call stream event override
	events [][]models.Event
}

func (m *scriptedModel) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	stream, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	resp := stream.Response()
	_ = stream.Close()
	return resp, nil
}

func (m *scriptedModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req.Clone())
	idx := m.calls
	m.calls++
	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx >= len(m.responses) {
		return nil, &models.Error{Kind: models.ErrorProtocol, Operation: "stream", Message: "no scripted response"}
	}
	resp := m.responses[idx]
	var events []models.Event
	if idx < len(m.events) && m.events[idx] != nil {
		events = m.events[idx]
	} else {
		events = eventsFromResponse(resp)
	}
	return models.NewStream(ctx, &sliceSource{events: events}), nil
}

func (m *scriptedModel) lastRequest() *models.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[len(m.requests)-1].Clone()
}

func (m *scriptedModel) requestAt(i int) *models.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.requests) {
		return nil
	}
	return m.requests[i].Clone()
}

type sliceSource struct {
	events []models.Event
	index  int
}

func (s *sliceSource) Next() (models.Event, error) {
	if s.index >= len(s.events) {
		return models.Event{}, io.EOF
	}
	ev := s.events[s.index]
	s.index++
	return ev, nil
}

func (*sliceSource) Close() error { return nil }

func eventsFromResponse(resp *models.Response) []models.Event {
	if resp == nil {
		return []models.Event{
			{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "script", ID: "r", Model: "m"}},
			{Kind: models.EventResponseEnd, Response: &models.ResponseInfo{Provider: "script", ID: "r", Model: "m", Status: models.ResponseStatusCompleted}},
		}
	}
	events := []models.Event{
		{Kind: models.EventResponseStart, Response: &models.ResponseInfo{
			Provider: resp.Provider, ID: resp.ID, Model: resp.Model, RequestID: resp.RequestID,
			CreatedAt: resp.CreatedAt, Metadata: resp.Metadata, ProviderState: resp.ProviderState,
		}},
	}
	if len(resp.Candidates) == 0 {
		// ensure at least empty candidate zero for reducer
	}
	for _, cand := range resp.Candidates {
		for partIdx, part := range cand.Content {
			// Emit complete parts in part_start (no argument deltas) so the
			// reducer accepts a single source of tool-call arguments.
			start := part.Clone()
			events = append(events, models.Event{
				Kind: models.EventPartStart, CandidateIndex: cand.Index, PartIndex: partIdx, Part: start,
				CallID: callIDOf(part),
			})
			events = append(events, models.Event{
				Kind: models.EventPartEnd, CandidateIndex: cand.Index, PartIndex: partIdx,
				CallID: callIDOf(part),
			})
		}
	}
	finishes := make([]models.CandidateFinish, 0, len(resp.Candidates))
	for _, cand := range resp.Candidates {
		finishes = append(finishes, models.CandidateFinish{
			CandidateIndex: cand.Index,
			Reason:         cand.FinishReason,
			RawReason:      cand.RawFinishReason,
			ProviderState:  cand.ProviderState,
		})
	}
	usage := resp.Usage
	events = append(events, models.Event{
		Kind: models.EventResponseEnd,
		Response: &models.ResponseInfo{
			Provider: resp.Provider, ID: resp.ID, Model: resp.Model, Status: resp.Status,
			RequestID: resp.RequestID, CreatedAt: resp.CreatedAt, Metadata: resp.Metadata,
			ProviderState: resp.ProviderState,
		},
		Finishes: finishes,
		Usage:    &usage,
	})
	return events
}

func callIDOf(part models.Content) string {
	if part.Kind == models.ContentToolCall && part.ToolCall != nil {
		return part.ToolCall.ID
	}
	return ""
}

func textResponse(text string) *models.Response {
	return &models.Response{
		Provider: "script",
		ID:       "r1",
		Model:    "m",
		Status:   models.ResponseStatusCompleted,
		Candidates: []models.Candidate{{
			Index:        0,
			Content:      []models.Content{models.Text(text)},
			FinishReason: models.FinishStop,
		}},
	}
}

func toolCallResponse(calls ...models.ToolCall) *models.Response {
	content := make([]models.Content, 0, len(calls))
	for _, c := range calls {
		content = append(content, models.ToolCallContent(c.ID, c.Name, c.Arguments))
	}
	return &models.Response{
		Provider: "script",
		ID:       "r1",
		Model:    "m",
		Status:   models.ResponseStatusCompleted,
		Candidates: []models.Candidate{{
			Index:        0,
			Content:      content,
			FinishReason: models.FinishToolCall,
		}},
	}
}

func withUsage(resp *models.Response, total int64) *models.Response {
	resp.Usage.TotalTokens = models.Some(total)
	return resp
}

func withState(resp *models.Response, state *models.ProviderState) *models.Response {
	resp.ProviderState = state
	resp.Candidates[0].ProviderState = state
	return resp
}
