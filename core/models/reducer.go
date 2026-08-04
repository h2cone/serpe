package models

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/h2cone/ouro/internal/jsonvalue"
)

type partKey struct {
	candidate int
	part      int
}

type partAccumulator struct {
	content Content
	text    strings.Builder
	args    strings.Builder
	media   []byte
	ended   bool
}

type candidateAccumulator struct {
	index         int
	parts         map[int]*partAccumulator
	finish        FinishReason
	rawFinish     string
	providerState *ProviderState
}

type reducer struct {
	provider   string
	started    bool
	ended      bool
	response   *Response
	candidates map[int]*candidateAccumulator
	parts      map[partKey]*partAccumulator
}

func newReducer(provider string) *reducer {
	return &reducer{provider: provider, candidates: make(map[int]*candidateAccumulator), parts: make(map[partKey]*partAccumulator)}
}

func (r *reducer) apply(event Event) error {
	if err := validateEventShape(event); err != nil {
		return r.protocol("invalid event shape: " + err.Error())
	}
	if r.ended {
		return r.protocol("event received after response_end")
	}
	switch event.Kind {
	case EventResponseStart:
		return r.start(event)
	case EventPartStart:
		return r.startPart(event)
	case EventPartDelta:
		return r.deltaPart(event)
	case EventPartEnd:
		return r.endPart(event)
	case EventResponseEnd:
		return r.end(event)
	default:
		panic("validateEventShape accepted an unknown event kind")
	}
}

func (r *reducer) start(event Event) error {
	if r.started {
		return r.protocol("duplicate response_start")
	}
	if event.Response == nil {
		return r.protocol("response_start is missing response metadata")
	}
	provider := event.Response.Provider
	if provider == "" {
		provider = r.provider
	}
	r.provider = provider
	r.response = &Response{
		Provider:      provider,
		ID:            event.Response.ID,
		Model:         event.Response.Model,
		Status:        event.Response.Status,
		RequestID:     event.Response.RequestID,
		CreatedAt:     event.Response.CreatedAt,
		ProviderState: event.Response.ProviderState.clone(),
		Metadata:      maps.Clone(event.Response.Metadata),
	}
	r.started = true
	return nil
}

func (r *reducer) startPart(event Event) error {
	if !r.started {
		return r.protocol("part_start before response_start")
	}
	key := partKey{candidate: event.CandidateIndex, part: event.PartIndex}
	if _, exists := r.parts[key]; exists {
		return r.protocol("duplicate part_start")
	}
	part := &partAccumulator{content: event.Part.Clone()}
	r.parts[key] = part
	candidate := r.candidates[event.CandidateIndex]
	if candidate == nil {
		candidate = &candidateAccumulator{index: event.CandidateIndex, parts: make(map[int]*partAccumulator), finish: FinishUnknown}
		r.candidates[event.CandidateIndex] = candidate
	}
	candidate.parts[event.PartIndex] = part
	return nil
}

func validatePartStart(content Content) error {
	switch content.Kind {
	case ContentText:
		if content.Text == nil || unionCount(content) != 1 {
			return fmt.Errorf("invalid text part_start")
		}
	case ContentImage:
		if content.Image == nil || unionCount(content) != 1 {
			return fmt.Errorf("invalid image part_start")
		}
		if content.Image.URI != "" {
			if err := validateImage(*content.Image); err != nil {
				return err
			}
		} else if content.Image.MIMEType == "" {
			return fmt.Errorf("media part_start requires a MIME type")
		}
	case ContentToolCall:
		if content.ToolCall == nil || unionCount(content) != 1 || content.ToolCall.Name == "" {
			return fmt.Errorf("tool-call part_start requires a name")
		}
		if len(content.ToolCall.Arguments) > 0 && !jsonvalue.IsObject(content.ToolCall.Arguments) {
			return fmt.Errorf("tool-call part_start arguments must be a JSON object")
		}
	case ContentReasoningSummary:
		if content.ReasoningSummary == nil || unionCount(content) != 1 {
			return fmt.Errorf("invalid reasoning-summary part_start")
		}
	case ContentRefusal:
		if content.Refusal == nil || unionCount(content) != 1 {
			return fmt.Errorf("invalid refusal part_start")
		}
	default:
		return fmt.Errorf("content kind %q cannot be streamed", content.Kind)
	}
	return nil
}

func unionCount(content Content) int {
	count := 0
	for _, set := range []bool{content.Text != nil, content.Image != nil, content.ToolCall != nil, content.ToolResult != nil, content.ReasoningSummary != nil, content.Refusal != nil} {
		if set {
			count++
		}
	}
	return count
}

func (r *reducer) deltaPart(event Event) error {
	if !r.started {
		return r.protocol("part_delta before response_start")
	}
	part := r.parts[partKey{candidate: event.CandidateIndex, part: event.PartIndex}]
	if part == nil {
		return r.protocol("part_delta before part_start")
	}
	if part.ended {
		return r.protocol("part_delta after part_end")
	}
	switch event.Delta.Kind {
	case DeltaText:
		if part.content.Kind != ContentText {
			return r.protocol("text delta targets a non-text part")
		}
		part.text.WriteString(event.Delta.Text)
	case DeltaToolArguments:
		if part.content.Kind != ContentToolCall {
			return r.protocol("tool arguments delta targets a non-tool part")
		}
		part.args.WriteString(event.Delta.Text)
	case DeltaReasoningSummary:
		if part.content.Kind != ContentReasoningSummary {
			return r.protocol("reasoning delta targets a non-reasoning part")
		}
		part.text.WriteString(event.Delta.Text)
	case DeltaRefusal:
		if part.content.Kind != ContentRefusal {
			return r.protocol("refusal delta targets a non-refusal part")
		}
		part.text.WriteString(event.Delta.Text)
	case DeltaMediaBytes:
		if part.content.Kind != ContentImage || part.content.Image.URI != "" {
			return r.protocol("media delta targets a non-inline-image part")
		}
		part.media = append(part.media, event.Delta.Media...)
	default:
		return r.protocol(fmt.Sprintf("unknown delta kind %q", event.Delta.Kind))
	}
	return nil
}

func (r *reducer) endPart(event Event) error {
	if !r.started {
		return r.protocol("part_end before response_start")
	}
	part := r.parts[partKey{candidate: event.CandidateIndex, part: event.PartIndex}]
	if part == nil {
		return r.protocol("part_end before part_start")
	}
	if part.ended {
		return r.protocol("duplicate part_end")
	}
	switch part.content.Kind {
	case ContentText:
		part.content.Text.Text += part.text.String()
	case ContentReasoningSummary:
		part.content.ReasoningSummary.Text += part.text.String()
	case ContentRefusal:
		part.content.Refusal.Text += part.text.String()
	case ContentImage:
		part.content.Image.Data = append(part.content.Image.Data, part.media...)
	case ContentToolCall:
		if part.args.Len() > 0 {
			if len(part.content.ToolCall.Arguments) > 0 {
				return r.protocol("tool arguments provided in both part_start and deltas")
			}
			part.content.ToolCall.Arguments = json.RawMessage(part.args.String())
		}
	}
	if err := part.content.Validate(); err != nil {
		return r.protocol("invalid completed part: " + err.Error())
	}
	part.ended = true
	return nil
}

func (r *reducer) end(event Event) error {
	if !r.started {
		return r.protocol("response_end before response_start")
	}
	for _, part := range r.parts {
		if !part.ended {
			return r.protocol("response_end while a part is still open")
		}
	}
	if event.Response != nil {
		if event.Response.Provider != "" && event.Response.Provider != r.response.Provider {
			return r.protocol("response provider changed during stream")
		}
		if event.Response.ID != "" {
			r.response.ID = event.Response.ID
		}
		if event.Response.Model != "" {
			r.response.Model = event.Response.Model
		}
		if event.Response.Status != "" {
			r.response.Status = event.Response.Status
		}
		if event.Response.RequestID != "" {
			r.response.RequestID = event.Response.RequestID
		}
		if !event.Response.CreatedAt.IsZero() {
			r.response.CreatedAt = event.Response.CreatedAt
		}
		if event.Response.Metadata != nil {
			r.response.Metadata = maps.Clone(event.Response.Metadata)
		}
		if event.Response.ProviderState != nil {
			r.response.ProviderState = event.Response.ProviderState.clone()
		}
	}
	for _, finish := range event.Finishes {
		candidate := r.candidates[finish.CandidateIndex]
		if candidate == nil {
			candidate = &candidateAccumulator{index: finish.CandidateIndex, parts: make(map[int]*partAccumulator)}
			r.candidates[finish.CandidateIndex] = candidate
		}
		candidate.finish = finish.Reason
		if candidate.finish == "" {
			candidate.finish = FinishUnknown
		}
		candidate.rawFinish = finish.RawReason
		candidate.providerState = finish.ProviderState.clone()
	}
	if event.Usage != nil {
		r.response.Usage = event.Usage.clone()
	}
	indexes := slices.Sorted(maps.Keys(r.candidates))
	if len(indexes) == 0 {
		r.response.Candidates = nil
	} else {
		r.response.Candidates = make([]Candidate, 0, len(indexes))
	}
	for _, index := range indexes {
		acc := r.candidates[index]
		partIndexes := slices.Sorted(maps.Keys(acc.parts))
		candidate := Candidate{Index: index, FinishReason: acc.finish, RawFinishReason: acc.rawFinish, ProviderState: acc.providerState.clone()}
		if candidate.FinishReason == "" {
			candidate.FinishReason = FinishUnknown
		}
		for _, partIndex := range partIndexes {
			candidate.Content = append(candidate.Content, acc.parts[partIndex].content.Clone())
		}
		r.response.Candidates = append(r.response.Candidates, candidate)
	}
	if r.response.Status == "" {
		r.response.Status = ResponseStatusCompleted
	}
	r.ended = true
	return nil
}

func (r *reducer) protocol(message string) error {
	return &Error{Kind: ErrorProtocol, Provider: r.provider, Operation: "stream_next", Code: "invalid_event_sequence", Message: message}
}

func (r *reducer) result() *Response {
	if !r.ended {
		return nil
	}
	return r.response.Clone()
}
