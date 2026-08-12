package runtime

import "github.com/h2cone/serpe/core/models"

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
//   - tool_end: ModelTurn, ToolIndex, ToolCall, ToolOutput
//   - run_end: StopReason
type Event struct {
	Kind       EventKind
	ModelTurn  int
	ToolIndex  int
	Model      models.Event
	Response   *models.Response
	ToolCall   *models.ToolCall
	ToolOutput *ToolOutput
	StopReason StopReason
}

// Constructors keep invalid field combinations out of the package: each
// kind's payload is set by exactly one constructor. Public fields remain
// available for switch-based rendering.
func newRunStartEvent() *Event {
	return &Event{Kind: EventRunStart}
}

func newModelStartEvent(turn int) *Event {
	return &Event{Kind: EventModelStart, ModelTurn: turn}
}

func newModelEvent(turn int, model models.Event) *Event {
	return &Event{Kind: EventModel, ModelTurn: turn, Model: model}
}

func newModelEndEvent(turn int, response *models.Response) *Event {
	return &Event{Kind: EventModelEnd, ModelTurn: turn, Response: response}
}

func newToolStartEvent(turn, index int, call *models.ToolCall) *Event {
	return &Event{Kind: EventToolStart, ModelTurn: turn, ToolIndex: index, ToolCall: call}
}

func newToolEndEvent(turn, index int, call *models.ToolCall, result *ToolOutput) *Event {
	return &Event{Kind: EventToolEnd, ModelTurn: turn, ToolIndex: index, ToolCall: call, ToolOutput: result}
}

func newRunEndEvent(reason StopReason) *Event {
	return &Event{Kind: EventRunEnd, StopReason: reason}
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
	if e.ToolOutput != nil {
		result := e.ToolOutput.clone()
		out.ToolOutput = &result
	}
	return out
}
