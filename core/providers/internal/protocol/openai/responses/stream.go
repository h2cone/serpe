package responses

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

type responseSource struct {
	reader        *sse.Reader
	requestID     string
	fallbackModel string
	ignoreUnknown bool
	stateLimit    int64
	queue         shared.EventQueue
	parts         map[responsePartKey]*responsePartState
	nextPart      int
	started       bool
	finished      bool
	responseID    string
	model         string
}

type responsePartKey struct {
	kind         string
	outputIndex  int
	contentIndex int
}

type responsePartState struct {
	part     int
	kind     models.ContentKind
	content  models.Content
	open     bool
	hadDelta bool
	value    strings.Builder
}

// NewSSEStreamSource builds a Responses event source from a raw SSE response
// obtained through an official SDK request.
func NewSSEStreamSource(reader *sse.Reader, requestID, fallbackModel string, ignoreUnknown bool, stateLimit int64) models.EventSource {
	return &responseSource{reader: reader, requestID: requestID, fallbackModel: fallbackModel, ignoreUnknown: ignoreUnknown, stateLimit: stateLimit, parts: make(map[responsePartKey]*responsePartState)}
}

func (s *responseSource) Next() (models.Event, error) {
	for {
		if event, ok := s.queue.Shift(); ok {
			return event, nil
		}
		if s.finished {
			return models.Event{}, io.EOF
		}
		wireEvent, err := s.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return models.Event{}, io.EOF
			}
			return models.Event{}, streamProtocol("sse_read_error", "failed to parse Responses SSE event", err)
		}
		data := strings.TrimSpace(string(wireEvent.Data))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if s.finished {
				continue
			}
			return models.Event{}, streamProtocol("unexpected_done", "Responses stream ended without a terminal response event", nil)
		}
		var event streamEventWire
		if err := json.Unmarshal(wireEvent.Data, &event); err != nil {
			return models.Event{}, streamProtocol("invalid_json", "Responses stream event is not valid JSON", err)
		}
		if err := s.consume(event, wireEvent.ID); err != nil {
			return models.Event{}, err
		}
	}
}

func (s *responseSource) consume(event streamEventWire, eventID string) error {
	switch event.Type {
	case "response.created":
		if s.started {
			return streamProtocol("duplicate_start", "duplicate response.created event", nil)
		}
		var response responseWire
		if err := json.Unmarshal(event.Response, &response); err != nil {
			return streamProtocol("invalid_start", "response.created is missing response metadata", err)
		}
		s.responseID = response.ID
		s.model = response.Model
		if s.model == "" {
			s.model = s.fallbackModel
		}
		info := &models.ResponseInfo{Provider: "openai", ID: response.ID, Model: s.model, Status: mapStatus(response.Status), RequestID: s.requestID}
		if response.CreatedAt != 0 {
			info.CreatedAt = time.Unix(response.CreatedAt, 0).UTC()
		}
		s.queue.Push(models.Event{Kind: models.EventResponseStart, Response: info, ProviderEventID: eventID})
		s.started = true
	case "response.in_progress", "response.queued":
		return nil
	case "response.output_item.added":
		if !s.started {
			return streamProtocol("missing_start", "output item before response.created", nil)
		}
		var header itemHeader
		if err := json.Unmarshal(event.Item, &header); err != nil {
			return streamProtocol("invalid_item", "invalid output item", err)
		}
		if header.Type == "function_call" {
			var call functionCallWire
			if err := json.Unmarshal(event.Item, &call); err != nil || call.Name == "" {
				return streamProtocol("invalid_tool_call", "invalid function-call output item", err)
			}
			key := responsePartKey{kind: "function_call", outputIndex: event.OutputIndex}
			s.startPart(key, models.ToolCallContent(call.CallID, call.Name, nil), event.ItemID, call.CallID, eventID)
		}
	case "response.content_part.added":
		var part contentPartWire
		if err := json.Unmarshal(event.Part, &part); err != nil {
			return streamProtocol("invalid_part", "invalid response content part", err)
		}
		key := responsePartKey{kind: part.Type, outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		switch part.Type {
		case "output_text":
			s.startPart(key, models.Text(""), event.ItemID, "", eventID)
			if part.Text != "" {
				s.delta(key, models.Delta{Kind: models.DeltaText, Text: part.Text}, event.ItemID, "", eventID)
			}
		case "refusal":
			s.startPart(key, models.Refusal(""), event.ItemID, "", eventID)
			if part.Refusal != "" {
				s.delta(key, models.Delta{Kind: models.DeltaRefusal, Text: part.Refusal}, event.ItemID, "", eventID)
			}
		case "reasoning_text":
			// Visible reasoning text (e.g. GPT-OSS), normalized like other
			// providers' displayable reasoning.
			s.startPart(key, models.ReasoningSummary(""), event.ItemID, "", eventID)
			if part.Text != "" {
				s.delta(key, models.Delta{Kind: models.DeltaReasoningSummary, Text: part.Text}, event.ItemID, "", eventID)
			}
		}
	case "response.content_part.done":
		var part contentPartWire
		if err := json.Unmarshal(event.Part, &part); err != nil {
			return streamProtocol("invalid_part", "invalid completed response content part", err)
		}
		key := responsePartKey{kind: part.Type, outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		switch part.Type {
		case "output_text":
			return s.finishValuePart(key, models.Text(""), models.DeltaText, &part.Text, event.ItemID, eventID)
		case "refusal":
			return s.finishValuePart(key, models.Refusal(""), models.DeltaRefusal, &part.Refusal, event.ItemID, eventID)
		case "reasoning_text":
			return s.finishValuePart(key, models.ReasoningSummary(""), models.DeltaReasoningSummary, &part.Text, event.ItemID, eventID)
		}
	case "response.output_text.delta":
		key := responsePartKey{kind: "output_text", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		if s.parts[key] == nil {
			s.startPart(key, models.Text(""), event.ItemID, "", eventID)
		}
		s.delta(key, models.Delta{Kind: models.DeltaText, Text: event.Delta}, event.ItemID, "", eventID)
	case "response.refusal.delta":
		key := responsePartKey{kind: "refusal", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		if s.parts[key] == nil {
			s.startPart(key, models.Refusal(""), event.ItemID, "", eventID)
		}
		s.delta(key, models.Delta{Kind: models.DeltaRefusal, Text: event.Delta}, event.ItemID, "", eventID)
	case "response.function_call_arguments.delta":
		key := responsePartKey{kind: "function_call", outputIndex: event.OutputIndex}
		if s.parts[key] == nil {
			return streamProtocol("missing_tool_start", "function arguments delta before output item", nil)
		}
		s.delta(key, models.Delta{Kind: models.DeltaToolArguments, Text: event.Delta}, event.ItemID, "", eventID)
	case "response.function_call_arguments.done":
		key := responsePartKey{kind: "function_call", outputIndex: event.OutputIndex}
		state := s.parts[key]
		if state == nil {
			return streamProtocol("missing_tool_start", "function arguments done before output item", nil)
		}
		return s.finishValuePart(key, state.content, models.DeltaToolArguments, &event.Arguments, event.ItemID, eventID)
	case "response.reasoning_summary_part.added":
		key := responsePartKey{kind: "reasoning_summary", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		s.startPart(key, models.ReasoningSummary(""), event.ItemID, "", eventID)
	case "response.reasoning_summary_text.delta":
		key := responsePartKey{kind: "reasoning_summary", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		if s.parts[key] == nil {
			s.startPart(key, models.ReasoningSummary(""), event.ItemID, "", eventID)
		}
		s.delta(key, models.Delta{Kind: models.DeltaReasoningSummary, Text: event.Delta}, event.ItemID, "", eventID)
	case "response.reasoning_text.delta":
		// Full reasoning text (distinct from short summary); map to the same
		// displayable reasoning channel used by Anthropic/Google thought parts.
		key := responsePartKey{kind: "reasoning_text", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		if s.parts[key] == nil {
			s.startPart(key, models.ReasoningSummary(""), event.ItemID, "", eventID)
		}
		s.delta(key, models.Delta{Kind: models.DeltaReasoningSummary, Text: event.Delta}, event.ItemID, "", eventID)
	case "response.output_text.done":
		key := responsePartKey{kind: "output_text", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		return s.finishValuePart(key, models.Text(""), models.DeltaText, event.Text, event.ItemID, eventID)
	case "response.refusal.done":
		key := responsePartKey{kind: "refusal", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		return s.finishValuePart(key, models.Refusal(""), models.DeltaRefusal, event.Refusal, event.ItemID, eventID)
	case "response.reasoning_summary_text.done":
		key := responsePartKey{kind: "reasoning_summary", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		return s.finishValuePart(key, models.ReasoningSummary(""), models.DeltaReasoningSummary, event.Text, event.ItemID, eventID)
	case "response.reasoning_text.done":
		key := responsePartKey{kind: "reasoning_text", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}
		return s.finishValuePart(key, models.ReasoningSummary(""), models.DeltaReasoningSummary, event.Text, event.ItemID, eventID)
	case "response.reasoning_summary_part.done":
		s.endPart(responsePartKey{kind: "reasoning_summary", outputIndex: event.OutputIndex, contentIndex: event.ContentIndex}, eventID)
	case "response.output_item.done":
		var header itemHeader
		if json.Unmarshal(event.Item, &header) == nil && header.Type == "function_call" {
			key := responsePartKey{kind: "function_call", outputIndex: event.OutputIndex}
			state := s.parts[key]
			if state != nil && !state.hadDelta {
				var call functionCallWire
				if json.Unmarshal(event.Item, &call) == nil {
					arguments := call.Arguments
					if arguments == "" {
						arguments = "{}"
					}
					s.delta(key, models.Delta{Kind: models.DeltaToolArguments, Text: arguments}, event.ItemID, call.CallID, eventID)
				}
			}
			s.endPart(key, eventID)
		}
	case "response.completed", "response.incomplete":
		if !s.started {
			return streamProtocol("missing_start", "terminal event before response.created", nil)
		}
		var response responseWire
		if err := json.Unmarshal(event.Response, &response); err != nil {
			return streamProtocol("invalid_terminal", "terminal event has invalid response metadata", err)
		}
		return s.complete(response, eventID)
	case "response.failed":
		var response responseWire
		_ = json.Unmarshal(event.Response, &response)
		if response.Error != nil {
			return normalizeWireError(response.Error, "stream_next", s.requestID)
		}
		return &models.Error{Kind: models.ErrorUnknown, Provider: "openai", Operation: "stream_next", Code: "response_failed", Message: "Responses stream failed", RequestID: s.requestID}
	case "error":
		if event.Error != nil {
			return normalizeWireError(event.Error, "stream_next", s.requestID)
		}
		return &models.Error{Kind: models.ErrorUnknown, Provider: "openai", Operation: "stream_next", Code: "stream_error", Message: "Responses stream reported an error", RequestID: s.requestID}
	default:
		if !s.ignoreUnknown {
			return streamProtocol("unknown_event", "unknown Responses event type "+event.Type, nil)
		}
	}
	return nil
}

func (s *responseSource) startPart(key responsePartKey, content models.Content, itemID, callID, eventID string) {
	if s.parts[key] != nil {
		return
	}
	state := &responsePartState{part: s.nextPart, kind: content.Kind, content: content, open: true}
	s.nextPart++
	s.parts[key] = state
	s.queue.Push(models.Event{Kind: models.EventPartStart, CandidateIndex: 0, PartIndex: state.part, Part: content, ItemID: itemID, CallID: callID, ProviderEventID: eventID})
}

func (s *responseSource) delta(key responsePartKey, delta models.Delta, itemID, callID, eventID string) {
	state := s.parts[key]
	if state == nil || !state.open {
		return
	}
	state.hadDelta = true
	state.value.WriteString(delta.Text)
	s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: state.part, Delta: delta, ItemID: itemID, CallID: callID, ProviderEventID: eventID})
}

func (s *responseSource) finishValuePart(key responsePartKey, content models.Content, deltaKind models.DeltaKind, full *string, itemID, eventID string) error {
	if s.parts[key] == nil {
		s.startPart(key, content, itemID, "", eventID)
	}
	state := s.parts[key]
	if state == nil || !state.open {
		return nil
	}
	if full != nil {
		current := state.value.String()
		if !strings.HasPrefix(*full, current) {
			return streamProtocol("terminal_content_mismatch", "Responses done event does not extend streamed content", nil)
		}
		if missing := strings.TrimPrefix(*full, current); missing != "" {
			s.delta(key, models.Delta{Kind: deltaKind, Text: missing}, itemID, "", eventID)
		}
	}
	s.endPart(key, eventID)
	return nil
}

func (s *responseSource) endPart(key responsePartKey, eventID string) {
	state := s.parts[key]
	if state == nil || !state.open {
		return
	}
	if state.kind == models.ContentToolCall && !state.hadDelta {
		s.delta(key, models.Delta{Kind: models.DeltaToolArguments, Text: "{}"}, "", "", eventID)
	}
	state.open = false
	s.queue.Push(models.Event{Kind: models.EventPartEnd, CandidateIndex: 0, PartIndex: state.part, ProviderEventID: eventID})
}

func (s *responseSource) complete(response responseWire, eventID string) error {
	keys := make([]responsePartKey, 0, len(s.parts))
	for key, state := range s.parts {
		if state.open {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return s.parts[keys[i]].part < s.parts[keys[j]].part })
	for _, key := range keys {
		s.endPart(key, eventID)
	}
	decoded, err := decodeResponse(response, s.requestID, s.stateLimit)
	if err != nil {
		return err
	}
	streamedContent, err := s.accumulatedContent()
	if err != nil {
		return err
	}
	var finishes []models.CandidateFinish
	switch len(decoded.Candidates) {
	case 0:
		if len(streamedContent) != 0 {
			return streamProtocol("terminal_candidate_mismatch", "Responses terminal payload omitted streamed candidate content", nil)
		}
	case 1:
		candidate := decoded.Candidates[0]
		if !shared.EquivalentContent(streamedContent, candidate.Content) {
			return streamProtocol("terminal_content_mismatch", "Responses terminal payload does not match streamed content", nil)
		}
		finishes = []models.CandidateFinish{{
			CandidateIndex: candidate.Index,
			Reason:         candidate.FinishReason,
			RawReason:      candidate.RawFinishReason,
			ProviderState:  candidate.ProviderState,
		}}
	default:
		return streamProtocol("terminal_candidate_mismatch", "Responses terminal payload contains multiple candidates", nil)
	}
	usage := decoded.Usage
	info := &models.ResponseInfo{Provider: decoded.Provider, ID: decoded.ID, Model: decoded.Model, Status: decoded.Status, RequestID: decoded.RequestID, CreatedAt: decoded.CreatedAt, ProviderState: decoded.ProviderState}
	s.queue.Push(models.Event{Kind: models.EventResponseEnd, Response: info, Finishes: finishes, Usage: &usage, ProviderEventID: eventID})
	s.finished = true
	return nil
}

func (s *responseSource) accumulatedContent() ([]models.Content, error) {
	states := make([]*responsePartState, 0, len(s.parts))
	for _, state := range s.parts {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].part < states[j].part })
	content := make([]models.Content, 0, len(states))
	for _, state := range states {
		value := state.value.String()
		switch state.kind {
		case models.ContentText:
			content = append(content, models.Text(value))
		case models.ContentRefusal:
			content = append(content, models.Refusal(value))
		case models.ContentReasoningSummary:
			content = append(content, models.ReasoningSummary(value))
		case models.ContentToolCall:
			if state.content.ToolCall == nil {
				return nil, streamProtocol("invalid_tool_call", "Responses stream lost function-call metadata", nil)
			}
			if value == "" {
				value = "{}"
			}
			content = append(content, models.ToolCallContent(state.content.ToolCall.ID, state.content.ToolCall.Name, json.RawMessage(value)))
		default:
			return nil, streamProtocol("invalid_part", "Responses stream contains an unsupported normalized content part", nil)
		}
	}
	return content, nil
}

func (s *responseSource) Close() error {
	if s.reader == nil {
		return nil
	}
	return s.reader.Close()
}

func streamProtocol(code, message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: code, Message: message, Cause: cause}
}
