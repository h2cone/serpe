package server

import (
	"github.com/h2cone/serpe/agent"
	"github.com/h2cone/serpe/core/models"
)

// SSE wire frames are bound to the UI runtime decoder through the shared
// server/testdata/sse_frames.json contract fixture. mapEvent / mapModelEvent
// return these concrete types only (no map[string]any).

type frameRunStart struct {
	T string `json:"t"`
}

type frameModelStart struct {
	T    string `json:"t"`
	Turn int    `json:"turn"`
}

type framePartStart struct {
	T      string `json:"t"`
	Turn   int    `json:"turn"`
	Part   int    `json:"part"`
	Kind   string `json:"kind"`
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

type frameDelta struct {
	T      string `json:"t"`
	Turn   int    `json:"turn"`
	Part   int    `json:"part"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	CallID string `json:"call_id,omitempty"`
}

type framePartEnd struct {
	T      string `json:"t"`
	Turn   int    `json:"turn"`
	Part   int    `json:"part"`
	CallID string `json:"call_id,omitempty"`
}

type frameToolStart struct {
	T    string               `json:"t"`
	Turn int                  `json:"turn"`
	Idx  int                  `json:"idx"`
	Call models.ContentRecord `json:"call"`
}

type frameToolEnd struct {
	T      string               `json:"t"`
	Turn   int                  `json:"turn"`
	Idx    int                  `json:"idx"`
	Call   models.ContentRecord `json:"call"`
	Result toolResultDTO        `json:"result"`
}

type frameModelEnd struct {
	T      string    `json:"t"`
	Turn   int       `json:"turn"`
	Usage  *usageDTO `json:"usage,omitempty"`
	Finish string    `json:"finish,omitempty"`
}

type frameRunEnd struct {
	T    string `json:"t"`
	Stop string `json:"stop"`
}

type frameError struct {
	T       string `json:"t"`
	Message string `json:"message"`
	Stop    string `json:"stop,omitempty"`
}

type frameDone struct {
	T            string `json:"t"`
	SessionID    string `json:"session_id"`
	Stop         string `json:"stop"`
	MessageCount int    `json:"message_count"`
}

// mapEvent converts an agent event into a typed SSE frame, or nil to drop.
func mapEvent(ev agent.Event) any {
	switch ev.Kind {
	case agent.EventRunStart:
		return frameRunStart{T: "run_start"}
	case agent.EventModelStart:
		return frameModelStart{T: "model_start", Turn: ev.ModelTurn}
	case agent.EventModel:
		return mapModelEvent(ev)
	case agent.EventModelEnd:
		frame := frameModelEnd{T: "model_end", Turn: ev.ModelTurn}
		if ev.Response != nil {
			frame.Usage = usageToDTO(&ev.Response.Usage)
			if len(ev.Response.Candidates) > 0 {
				frame.Finish = string(ev.Response.Candidates[0].FinishReason)
			}
		}
		return frame
	case agent.EventToolStart:
		return frameToolStart{
			T:    "tool_start",
			Turn: ev.ModelTurn,
			Idx:  ev.ToolIndex,
			Call: toolCallToRecord(ev.ToolCall),
		}
	case agent.EventToolEnd:
		return frameToolEnd{
			T:      "tool_end",
			Turn:   ev.ModelTurn,
			Idx:    ev.ToolIndex,
			Call:   toolCallToRecord(ev.ToolCall),
			Result: toolOutputToDTO(ev.ToolOutput),
		}
	case agent.EventRunEnd:
		return frameRunEnd{T: "run_end", Stop: string(ev.StopReason)}
	default:
		return nil
	}
}

func mapModelEvent(ev agent.Event) any {
	m := ev.Model
	switch m.Kind {
	case models.EventPartStart:
		frame := framePartStart{
			T:    "part_start",
			Turn: ev.ModelTurn,
			Part: m.PartIndex,
			Kind: partKind(m.Part),
		}
		if m.CallID != "" {
			frame.CallID = m.CallID
		} else if m.Part.ToolCall != nil && m.Part.ToolCall.ID != "" {
			frame.CallID = m.Part.ToolCall.ID
		}
		if m.Part.ToolCall != nil && m.Part.ToolCall.Name != "" {
			frame.Name = m.Part.ToolCall.Name
		}
		return frame
	case models.EventPartDelta:
		// Drop media_bytes and unknown delta kinds in V1.
		switch m.Delta.Kind {
		case models.DeltaText, models.DeltaReasoningSummary, models.DeltaRefusal, models.DeltaToolArguments:
		default:
			return nil
		}
		frame := frameDelta{
			T:    "delta",
			Turn: ev.ModelTurn,
			Part: m.PartIndex,
			Kind: deltaKind(m.Delta.Kind),
			Text: m.Delta.Text, // must read Delta.Text (DisplayText empty for tool_arguments)
		}
		if m.CallID != "" {
			frame.CallID = m.CallID
		}
		return frame
	case models.EventPartEnd:
		frame := framePartEnd{
			T:    "part_end",
			Turn: ev.ModelTurn,
			Part: m.PartIndex,
		}
		if m.CallID != "" {
			frame.CallID = m.CallID
		}
		return frame
	default:
		// response_start / response_end / others: drop
		return nil
	}
}
