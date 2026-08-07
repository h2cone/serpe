package server_test

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/compose"
	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/sessions"
	"github.com/h2cone/ouro/server"
)

// scriptedModel implements models.Model with a fixed sequence of responses.
type scriptedModel struct {
	mu        sync.Mutex
	responses []*models.Response
	errs      []error
	calls     int
	events    [][]models.Event
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
	for _, cand := range resp.Candidates {
		for partIdx, part := range cand.Content {
			if part.Kind == models.ContentText && part.Text != nil && part.Text.Text != "" {
				empty := models.Text("")
				events = append(events, models.Event{
					Kind: models.EventPartStart, CandidateIndex: cand.Index, PartIndex: partIdx, Part: empty,
				})
				events = append(events, models.Event{
					Kind: models.EventPartDelta, CandidateIndex: cand.Index, PartIndex: partIdx,
					Delta: models.Delta{Kind: models.DeltaText, Text: part.Text.Text},
				})
				events = append(events, models.Event{
					Kind: models.EventPartEnd, CandidateIndex: cand.Index, PartIndex: partIdx,
				})
				continue
			}
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
		Usage: models.Usage{TotalTokens: models.Some(int64(10))},
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

type nowTool struct{}

func (nowTool) Definition() models.Tool {
	return models.Tool{
		Name:        "now",
		Description: "current time",
		Parameters:  []byte(`{"type":"object","properties":{}}`),
	}
}

func (nowTool) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolOutput, error) {
	return agent.TextResult("12:00"), nil
}

func newTestServer(t *testing.T, model models.Model, tools []agent.Tool, newID func() string) (*server.Server, *sessions.Manager) {
	t.Helper()
	if model == nil {
		model = &scriptedModel{responses: []*models.Response{textResponse("hello")}}
	}
	if newID == nil {
		newID = func() string { return "sess-1" }
	}
	runner, err := agent.NewRunner(agent.Config{Model: model, Tools: tools})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	mgr, err := sessions.NewManager(sessions.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	turns, err := compose.New(compose.Config{Runner: runner, Manager: mgr})
	if err != nil {
		t.Fatalf("compose.New: %v", err)
	}
	srv, err := server.New(server.Config{
		Turns:   turns,
		Manager: mgr,
		CWD:     "/tmp",
		NewID:   newID,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv, mgr
}
