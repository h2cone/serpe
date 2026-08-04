package agent

import "github.com/h2cone/ouro/core/models"

// EventKind identifies a run-level lifecycle event.
type EventKind string

const (
	EventRunStart   EventKind = "run_start"
	EventModelStart EventKind = "model_start"
	EventModel      EventKind = "model_event"
	EventModelEnd   EventKind = "model_end"
	EventToolStart  EventKind = "tool_start"
	EventToolEnd    EventKind = "tool_end"
	EventRunEnd     EventKind = "run_end"
)

// Event is a run-level lifecycle notice. Fields are meaningful only for
// specific kinds:
//
//   - run_start: no payload fields
//   - model_start: ModelTurn
//   - model_event: ModelTurn, Model
//   - model_end: ModelTurn, Response
//   - tool_start: ModelTurn, ToolIndex, ToolCall
//   - tool_end: ModelTurn, ToolIndex, ToolCall, ToolResult
//   - run_end: StopReason
type Event struct {
	Kind       EventKind
	ModelTurn  int
	ToolIndex  int
	Model      models.Event
	Response   *models.Response
	ToolCall   *models.ToolCall
	ToolResult *ToolResult
	StopReason StopReason
}

func (e Event) clone() Event {
	out := e
	out.Model = e.Model.Clone()
	out.Response = e.Response.Clone()
	if e.ToolCall != nil {
		call := *e.ToolCall
		call.Arguments = append([]byte(nil), e.ToolCall.Arguments...)
		out.ToolCall = &call
	}
	if e.ToolResult != nil {
		result := e.ToolResult.clone()
		out.ToolResult = &result
	}
	return out
}
