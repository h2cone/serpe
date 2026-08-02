package models

import (
	"maps"
	"time"
)

// EventKind is a normalized streaming lifecycle event kind.
type EventKind string

const (
	EventResponseStart EventKind = "response_start"
	EventPartStart     EventKind = "part_start"
	EventPartDelta     EventKind = "part_delta"
	EventPartEnd       EventKind = "part_end"
	EventResponseEnd   EventKind = "response_end"
)

// DeltaKind identifies a member of the closed Delta union.
type DeltaKind string

const (
	DeltaText             DeltaKind = "text"
	DeltaToolArguments    DeltaKind = "tool_arguments"
	DeltaReasoningSummary DeltaKind = "reasoning_summary"
	DeltaRefusal          DeltaKind = "refusal"
	DeltaMediaBytes       DeltaKind = "media_bytes"
)

// Delta is a normalized incremental content payload. Text is used for all
// textual delta kinds, while Media is used only for DeltaMediaBytes.
type Delta struct {
	Kind  DeltaKind
	Text  string
	Media []byte
}

// ResponseInfo carries response metadata available at stream start or end.
type ResponseInfo struct {
	Provider      string
	ID            string
	Model         string
	Status        ResponseStatus
	RequestID     string
	CreatedAt     time.Time
	Metadata      map[string]string
	ProviderState *ProviderState
}

// CandidateFinish associates a normalized and raw finish reason with a
// provider candidate index.
type CandidateFinish struct {
	CandidateIndex int
	Reason         FinishReason
	RawReason      string
	ProviderState  *ProviderState
}

// Event is an immutable normalized stream event. CandidateIndex and PartIndex
// are meaningful for part events. ProviderEventID, ItemID, and CallID preserve
// wire identities without synthesizing them from array positions.
type Event struct {
	Kind            EventKind
	CandidateIndex  int
	PartIndex       int
	Part            Content
	Delta           Delta
	Response        *ResponseInfo
	Finishes        []CandidateFinish
	Usage           *Usage
	ProviderEventID string
	ItemID          string
	CallID          string
}

func (e Event) clone() Event {
	out := e
	out.Part = e.Part.clone()
	out.Delta.Media = append([]byte(nil), e.Delta.Media...)
	if e.Response != nil {
		info := *e.Response
		info.ProviderState = e.Response.ProviderState.clone()
		info.Metadata = maps.Clone(e.Response.Metadata)
		out.Response = &info
	}
	if e.Finishes != nil {
		out.Finishes = make([]CandidateFinish, len(e.Finishes))
		for i := range e.Finishes {
			out.Finishes[i] = e.Finishes[i]
			out.Finishes[i].ProviderState = e.Finishes[i].ProviderState.clone()
		}
	}
	if e.Usage != nil {
		usage := e.Usage.clone()
		out.Usage = &usage
	}
	return out
}

// DisplayText returns the visible textual payload of a delta event.
func (e Event) DisplayText() string {
	if e.Kind != EventPartDelta {
		return ""
	}
	switch e.Delta.Kind {
	case DeltaText, DeltaReasoningSummary, DeltaRefusal:
		return e.Delta.Text
	default:
		return ""
	}
}
